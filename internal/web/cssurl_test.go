package web

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// scanReports returns the tokenizer's reports for s as "U<offset>" (URL
// start) and "F<offset>" (function break), with ";" after the offset for a
// URL start right after a reference without ";".
func scanReports(s string, refs bool) []string {
	var out []string
	scanCSS(s, refs, false, func(r cssReport) bool {
		k := "U"
		if r.kind == cssFuncBreak {
			k = "F"
		}
		e := ""
		if r.kind == cssURLStart && r.at.bare {
			e = ";"
		}
		out = append(out, fmt.Sprintf("%s%d%s", k, r.at.off, e))
		return true
	})
	return out
}

// cssScanCases are inputs of the tokenizer and the reports it must make,
// written down from CSS Syntax Level 3 (sections 3.3 and 4), not from the
// implementation. Offsets are byte offsets in the input.
var cssScanCases = []struct {
	name string
	in   string
	refs bool
	want []string
}{
	// url tokens and url( functions.
	{"url token", `url(x)`, false, []string{"U4"}},
	{"upper case", `URL(x)`, false, []string{"U4"}},
	{"mixed case", `uRl(x)`, false, []string{"U4"}},
	{"leading white space", `url(  x)`, false, []string{"U6"}},
	{"trailing white space", `url(x  )`, false, []string{"U4"}},
	{"double-quoted string", `url("x")`, false, []string{"U5"}},
	{"single-quoted string with white space", `url( 'x' )`, false, []string{"U6"}},
	{"string after two spaces", `url(  "x")`, false, []string{"U7"}},
	{"in a declaration", `a{background:#036 url(http://x/a) no-repeat}`, false, []string{"U22"}},
	{"two urls", `url(a)url(b)`, false, []string{"U4", "U10"}},
	{"custom property", `--x:url(y)`, false, []string{"U8"}},
	{"url in a function", `image-set(url(x) 1x)`, false, []string{"F9", "U14"}},

	// Escapes in the name and the value (4.3.7).
	{"escaped u", `\75 rl(x)`, false, []string{"U7"}},
	{"escaped r", `u\72l(x)`, false, []string{"U6"}},
	{"escaped l", `ur\l(x)`, false, []string{"U5"}},
	{"escaped upper case", `\55 RL(x)`, false, []string{"U7"}},
	{"six hex digits", `u\000072l(x)`, false, []string{"U10"}},
	{"escape at the start of the value", `url(\68ttp://x)`, false, []string{"U4"}},
	{"tab escape in the value", `url(\9 x)`, false, []string{"U4"}},
	{"escape that spells data:", `url(\64 ata:x)`, false, nil},
	{"escape at EOF in the value", `url(x\`, false, []string{"U4"}},

	// Values that are not reported.
	{"data URL", `url(data:image/png;base64,AAAA)`, false, nil},
	{"data URL upper case", `url(DATA:x)`, false, nil},
	{"data URL in a string", `url("data:x")`, false, nil},
	{"fragment", `url(#a)`, false, nil},
	{"fragment in a string", `url('#a')`, false, nil},
	{"empty", `url()`, false, nil},
	{"empty with white space", `url(  )`, false, nil},
	{"empty string", `url("")`, false, nil},
	{"data without colon", `url(data)`, false, []string{"U4"}},

	// Names that are not url.
	{"longer name", `myurl(x)`, false, []string{"F5"}},
	{"comment before the parenthesis", `url/**/(x)`, false, nil},
	{"escaped parenthesis", `url\28x`, false, nil},
	{"dimension", `1url(x)`, false, nil},
	{"hash", `#url(x)`, false, nil},
	{"at-keyword", `@url(x)`, false, nil},
	{"url_", `url_(x)`, false, nil},
	{"escaped newline", "u\\\nrl(x)", false, []string{"F5"}},

	// Strings and comments.
	{"in a string", `a{content:"url(x)"}`, false, nil},
	{"in a single-quoted string", `'image-set(x)'`, false, nil},
	{"in a comment", `/* url(x) image-set( */`, false, nil},
	{"comment at EOF", `/* url(x)`, false, nil},
	{"string at EOF", `"url(x)`, false, nil},
	{"url string at EOF", `url("x`, false, []string{"U5"}},
	{"bad string", "url(\"x\ny\")", false, []string{"U5"}},
	{"escaped newline in the url string", "url(\"\\\nx\")", false, []string{"U5"}},
	{"string with an escaped quote", `"a\"url(x)"url(y)`, false, []string{"U15"}},
	{"comment inside url token", `url(/**/x)`, false, []string{"U4"}},

	// Bad urls and their recovery (4.3.6, 4.3.14).
	{"white space inside", `url(x y) url(z)`, false, []string{"U4", "U13"}},
	{"quote inside", `url(x"y\)z) url(w)`, false, []string{"U4", "U16"}},
	{"parenthesis inside", `url(x(y)url(z)`, false, []string{"U4", "U12"}},
	{"non-printable", "url(x\x01)url(z)", false, []string{"U4", "U11"}},
	{"invalid escape", "url(x\\\n)url(z)", false, []string{"U4", "U12"}},

	// Preprocessing (3.3).
	{"CRLF", "\r\nurl(x)", false, []string{"U6"}},
	{"CR", "\rurl(x)", false, []string{"U5"}},
	{"FF before the value", "url(\fx)", false, []string{"U5"}},
	{"FF as white space between tokens", "a\furl(x)", false, []string{"U6"}},
	{"CR in a url token", "url(x\r)", false, []string{"U4"}},
	{"NUL", "url(\x00)", false, []string{"U4"}},
	{"NUL in a name", "f\x00(", false, []string{"F2"}},
	{"CDO and CDC", `<!--url(x)-->`, false, []string{"U8"}},
	{"CDC before a function", `-->foo(`, false, []string{"F6"}},

	// Functions: breaks for every name not on the list.
	{"image-set", `image-set(`, false, []string{"F9"}},
	{"-webkit-image-set", `-webkit-image-set(`, false, []string{"F17"}},
	{"-WebKit-Image-Set", `-WebKit-Image-Set(`, false, []string{"F17"}},
	{"IMAGE-SET", `IMAGE-SET(`, false, []string{"F9"}},
	{"image", `IMAGE(`, false, []string{"F5"}},
	{"src", `src(`, false, []string{"F3"}},
	{"escaped image-set", `\69mage-set(`, false, []string{"F11"}},
	{"escaped e", `imag\65-set(`, false, []string{"F11"}},
	{"in a custom property", `--x:image-set(a)`, false, []string{"F13"}},
	{"cross-fade", `cross-fade(`, false, []string{"F10"}},
	{"element", `element(`, false, []string{"F7"}},
	{"paint", `paint(`, false, []string{"F5"}},
	{"expression", `expression(`, false, []string{"F10"}},
	{"unknown", `foo(`, false, []string{"F3"}},
	{"custom function", `--g()`, false, []string{"F3"}},
	{"custom function ending in _", `--f_("x")`, false, nil},
	{"@function body", `@function --f_(--s){result:image-set(var(--s) 1x)}`, false, []string{"F36"}},
	{"@function with url", `@function --g(){result:url(x)}`, false, []string{"F13", "U27"}},
	{"vendor prefix only", `-webkit-(`, false, []string{"F8"}},
	{"long name", strings.Repeat("a", 50) + "(", false, []string{"F50"}},
	{"long name ending in _", strings.Repeat("a", 50) + "_(", false, nil},
	{"name with an escape at EOF", `a\`, false, nil},
	{"escaped zero", `a\0(`, false, []string{"F3"}},
	{"escaped surrogate", `a\d800 (`, false, []string{"F7"}},
	{"escape past U+10FFFF", `a\110000(`, false, []string{"F8"}},
	{"non-ASCII name", "×url(x)", false, []string{"F5"}},
	{"listed function", `a{color:rgb(0 0 0);width:calc(1px + 2%)}`, false, nil},
	{"listed with prefix", `-webkit-linear-gradient(red,blue)`, false, nil},
	{"selector", `p:not(.a):nth-child(2n+1){}`, false, nil},
	{"if with url", `if(media(print): url(x))`, false, []string{"U21"}},

	// Character references (refs: an attribute value).
	{"quot with ;", `url(&quot;x&quot;)`, true, []string{"U10"}},
	{"quot without ;", `url(&quot/x&quot)`, true, []string{"U9;"}},
	{"hex quote without ;", `url(&#x22/x)`, true, []string{"U9;"}},
	{"decimal quote without ;", `url(&#34/x)`, true, []string{"U8;"}},
	{"reference in the name", `u&#114;l(x)`, true, []string{"U9"}},
	{"reference to a parenthesis", `url&#40;x)`, true, []string{"U8"}},
	{"reference to a CR", `url(&#13;x)`, true, []string{"U9"}},
	{"reference to an FF", `url(&#12;x)`, true, []string{"U9"}},
	{"ampersand", `&amp;url(x)`, true, []string{"U9"}},
	{"quot followed by a letter", `url(&quotx)`, true, []string{"U4"}},
	{"quot followed by =", `url(&quot=x)`, true, []string{"U4"}},
	{"not a reference", `url(&nosuch;x)`, true, []string{"U4"}},
	{"legacy prefix", `url(&notit;x)`, true, []string{"U4"}},
	{"references off", `u&#114;l(x)`, false, []string{"F8"}},
	{"reference to a data URL", `url(&#100;ata:x)`, true, nil},
	{"reference to a function", `image-set&lpar;`, true, []string{"F9"}},
}

func TestScanCSS(t *testing.T) {
	for _, c := range cssScanCases {
		t.Run(c.name, func(t *testing.T) {
			if got := scanReports(c.in, c.refs); !slices.Equal(got, c.want) {
				t.Errorf("scanCSS(%q, refs=%v) = %v, want %v", c.in, c.refs, got, c.want)
			}
		})
	}
}

// Every function on the list is safe in every spelling the tokenizer folds:
// upper case, an escape, a vendor prefix. A name with one more letter is not.
func TestScanCSSAllowlist(t *testing.T) {
	if len(cssSafeFunctions) < 140 {
		t.Fatalf("only %d functions listed", len(cssSafeFunctions))
	}
	for name := range cssSafeFunctions {
		if strings.ToLower(name) != name || strings.ContainsAny(name, " _") || name == "url" || len(name) >= cssNameMax {
			t.Errorf("bad entry %q", name)
		}
		for _, in := range []string{
			name + "(",
			strings.ToUpper(name) + "(",
			fmt.Sprintf(`\%x %s(`, name[0], name[1:]),
			"-webkit-" + name + "(",
			"-MOZ-" + name + "(",
		} {
			if got := scanReports(in, false); len(got) != 0 {
				t.Errorf("%q: %v", in, got)
			}
		}
		if got := scanReports(name+"x(", false); len(got) != 1 && !cssSafeFunctions[name+"x"] {
			t.Errorf("%qx(: %v", name, got)
		}
	}
	for _, name := range []string{"image-set", "image", "src", "cross-fade", "element", "paint", "url_", "expression"} {
		if cssSafeFunctions[name] {
			t.Errorf("%q is listed", name)
		}
	}
}

// The report callback can stop the scan.
func TestScanCSSStops(t *testing.T) {
	n := 0
	scanCSS(`a(b(c(`, false, false, func(cssReport) bool { n++; return false })
	if n != 1 {
		t.Errorf("%d reports after stop", n)
	}
}

// cssPreprocess is section 3.3 on a string, the slow way.
func cssPreprocess(s string) []rune {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.NewReplacer("\r", "\n", "\f", "\n").Replace(s)
	var out []rune
	for _, r := range s {
		if r == 0 {
			r = utf8.RuneError
		}
		out = append(out, r)
	}
	return out
}

// decoded returns the code points of the streaming decoder over s.
func decoded(s string, refs bool) []cssPoint {
	d := cssDecoder{s: s, refs: refs}
	var out []cssPoint
	bare := false
	for {
		p, ok := d.next(&bare)
		if !ok {
			return out
		}
		out = append(out, p)
	}
}

// attrValueByParser is the slow reference decoder: x/net/html's Tokenizer
// on an attribute value (without double quotes), then section 3.3.
func attrValueByParser(v string) []rune {
	z := html.NewTokenizer(strings.NewReader(`<a b="` + v + `">`))
	z.Next()
	tok := z.Token()
	if len(tok.Attr) == 0 {
		return nil
	}
	return cssPreprocess(tok.Attr[0].Val)
}

func runes(ps []cssPoint) []rune {
	out := make([]rune, len(ps))
	for i, p := range ps {
		out[i] = p.r
	}
	return out
}

func TestCSSDecoderMatchesParser(t *testing.T) {
	for _, v := range []string{
		`&quot;a&quot`, `&quotx`, `&quot=x`, `&notit;`, `&notin;`, `&not`, `&fjlig;`, `&ThickSpace;`, `&nosuch;`,
		`&#x22`, `&#34;`, `&#0;`, `&#x110000;`, `&#xD800;`, `&#x80;&#x9F`, `&#;`, `&#x;`, `&`, `&#`, `&amp`, "\r\n&#13;&#10;\r&#10;",
		`&AMP;&AMP`, `&semi;`, `&lpar;&rpar;`, `&lt&gt`, "&#13;\r\n", "a\fb\x00c", "\xff\xfe",
	} {
		if got, want := runes(decoded(v, true)), attrValueByParser(v); !slices.Equal(got, want) {
			t.Errorf("%q: decoder %q, parser %q", v, string(got), string(want))
		}
	}
}

func FuzzCSSScan(f *testing.F) {
	for _, c := range cssScanCases {
		f.Add(c.in, c.refs)
	}
	f.Fuzz(func(t *testing.T, in string, refs bool) {
		last := 0
		scanCSS(in, refs, true, func(r cssReport) bool {
			if r.at.off < last || r.at.off > len(in) {
				t.Fatalf("%q: report at %d after %d", in, r.at.off, last)
			}
			last = r.at.off
			return true
		})
		// The streaming decoder against the slow one, and its offsets.
		pts := decoded(in, refs)
		for k, p := range pts {
			if p.off < 0 || p.off >= len(in) || (k > 0 && p.off < pts[k-1].off) {
				t.Fatalf("%q: offset %d at %d", in, p.off, k)
			}
		}
		if !utf8.ValidString(in) {
			return
		}
		if !refs {
			if got, want := runes(pts), cssPreprocess(strings.ReplaceAll(in, "\r\n", "\n")); !slices.Equal(got, want) {
				t.Fatalf("%q: decoder %q, want %q", in, string(got), string(want))
			}
			return
		}
		v := strings.ReplaceAll(in, `"`, "")
		if got, want := runes(decoded(v, true)), attrValueByParser(v); !slices.Equal(got, want) {
			t.Fatalf("%q: decoder %q, parser %q", v, string(got), string(want))
		}
	})
}
