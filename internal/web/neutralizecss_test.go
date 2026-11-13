package web

import (
	"strings"
	"testing"
	"unsafe"
)

// cssLoadingProperties are the CSS properties that load an image, as
// measured in Chromium 156: every one sends the archive's origin.
var cssLoadingProperties = []string{
	"background-image", "background", "list-style-image", "list-style", "border-image", "content", "cursor",
	"mask-image", "mask", "-webkit-mask-image", "shape-outside", "clip-path",
}

// neutralizeCSSCases are inputs of neutralizeCSS and their exact outputs.
// survives: cssFetchSurvivors still finds something in the output.
type neutralizeCSSCase struct {
	name, in, want string
	survives       bool
}

var neutralizeCSSCases = func() []neutralizeCSSCase {
	cs := []neutralizeCSSCase{
		{"image-set", `<p style="background-image:image-set(&quot;http://x/a&quot; 1x)">`,
			`<p style="background-image:image-set_(&quot;http://x/a&quot; 1x)">`, false},
		{"-webkit-image-set in style", `<style>.a{background-image:-webkit-image-set("http://x/a" 1x)}</style>`,
			`<style>.a{background-image:-webkit-image-set_("http://x/a" 1x)}</style>`, false},
		{"image-set with url", `<style>.a{background-image:image-set(url(http://x/a) 1x)}</style>`,
			`<style>.a{background-image:image-set_(url(data:,http://x/a) 1x)}</style>`, false},
		{"var with url", `<style>.a{--v:url(http://x/a);background-image:var(--v)}</style>`,
			`<style>.a{--v:url(data:,http://x/a);background-image:var(--v)}</style>`, false},
		{"var with string", `<style>.a{--s:"http://x/a";background-image:image-set(var(--s) 1x)}</style>`,
			`<style>.a{--s:"http://x/a";background-image:image-set_(var(--s) 1x)}</style>`, false},
		{"@function", `<style>@function --f_(--s){result:image-set(var(--s) 1x)}.a{background-image:--f_("http://x/a")}</style>`,
			`<style>@function --f_(--s){result:image-set_(var(--s) 1x)}.a{background-image:--f_("http://x/a")}</style>`, false},
		{"@import", `<style>@import url(http://x/a.css);</style>`, `<style>@import url(data:,http://x/a.css);</style>`, false},
		{"colour and shorthand kept", `<table><tr><td style="background:#036 url(http://x/a) no-repeat;color:rgb(1 2 3)">`,
			`<table><tr><td style="background:#036 url(data:,http://x/a) no-repeat;color:rgb(1 2 3)">`, false},
		{"SVG presentation attributes", `<svg><rect fill="url(http://x/f)" stroke="url('http://x/s')" mask="url(http://x/m)" ` +
			`clip-path="url(http://x/c)" marker-start="url(http://x/k)" filter="url(#f)"/></svg>`,
			`<svg><rect fill="url(data:,http://x/f)" stroke="url('data:,http://x/s')" mask="url(data:,http://x/m)" ` +
				`clip-path="url(data:,http://x/c)" marker-start="url(data:,http://x/k)" filter="url(#f)"/></svg>`, false},
		{"plain SVG style", `<svg><style>.a{fill:url(http://x/a)}</style></svg>`, `<svg><style>.a{fill:url(data:,http://x/a)}</style></svg>`, false},
		{"quot with ;", `<p style="background:url(&quot;http://x/a&quot;)">`, `<p style="background:url(&quot;data:,http://x/a&quot;)">`, false},
		{"reference in the name", `<p style="background:u&#114;l(http://x/a)">`, `<p style="background:u&#114;l(data:,http://x/a)">`, false},
		{"legacy quot without ;", `<p style="background:url(&quot/x&quot)">`, `<p style="background:url(&quot;data:,/x&quot)">`, false},
		{"hex quote without ;", `<p style='background:url(&#x22/x&#x22)'>`, `<p style='background:url(&#x22;data:,/x&#x22)'>`, false},
		{"unquoted value", `<p style=background:url(http://x/a)>`, `<p style=background:url(data:,http://x/a)>`, false},
		{"upper-case name", `<P STYLE="background:url(http://x/a)">`, `<P STYLE="background:url(data:,http://x/a)">`, false},
		{"duplicate style", `<p style="a:url(http://x/1)" style="b:url(http://x/2)">`,
			`<p style="a:url(data:,http://x/1)" style="b:url(data:,http://x/2)">`, false},
		{"style comment markers", `<style><!-- .a{background:url(http://x/a)} --></style>`,
			`<style><!-- .a{background:url(data:,http://x/a)} --></style>`, false},
		{"</styles> does not end the text", `<style>a{}</styles>b{background:url(http://x/a)}</style>`,
			`<style>a{}</styles>b{background:url(data:,http://x/a)}</style>`, false},
		{"self-closing style", `<style/>a{background:url(http://x/a)}</style>`, `<style/>a{background:url(data:,http://x/a)}</style>`, false},
		{"noscript style attribute", `<noscript><p style="background:url(http://x/a)"></noscript>`,
			`<noscript><p style="background:url(data:,http://x/a)"></noscript>`, false},
		{"noscript style element", `<noscript><style>a{background:url(http://x/a)}</style></noscript>`,
			`<noscript><style>a{background:url(data:,http://x/a)}</style></noscript>`, false},
		{"presentation attribute on an HTML element", `<font color="url(http://x/a)">x</font>`, `<font color="url(data:,http://x/a)">x</font>`, false},
		// Comment-split url( in SVG <style>: the rewrite reads "rl(" as an
		// unknown function; the browser joins the text to "url_(", which
		// loads nothing.
		{"comment-split url in SVG style", `<svg><style>.a{fill:u<!--c-->rl(http://x/a)}</style></svg>`,
			`<svg><style>.a{fill:u<!--c-->rl_(http://x/a)}</style></svg>`, false},
		{"CDATA-split url in SVG style", `<svg><style>.a{fill:u<![CDATA[rl(http://x/a)]]>}</style></svg>`,
			`<svg><style>.a{fill:u<![CDATA[rl_(http://x/a)]]>}</style></svg>`, false},
		// Unchanged.
		{"href, src and srcset", `<a href="url(http://x/a)">a</a><img src="url(http://x/b)" srcset="url(http://x/c) 1x">`,
			`<a href="url(http://x/a)">a</a><img src="url(http://x/b)" srcset="url(http://x/c) 1x">`, false},
		{"title and alt", `<img alt="url(http://x/a) and x(" title="image-set(">`, `<img alt="url(http://x/a) and x(" title="image-set(">`, false},
		{"text", `<p>url(http://x/a) image-set(</p>`, `<p>url(http://x/a) image-set(</p>`, false},
		{"title element", `<title>url(http://x/a)</title>`, `<title>url(http://x/a)</title>`, false},
		{"style tag in xmp", `<xmp><style>a{background:url(http://x/a)}</style></xmp>`, `<xmp><style>a{background:url(http://x/a)}</style></xmp>`, false},
		{"end tag attribute", `<p></p style="background:url(http://x/a)">`, `<p></p style="background:url(http://x/a)">`, false},
		{"inlined data URL", `<p style="background:url(data:image/png;base64,AAAA)">`, `<p style="background:url(data:image/png;base64,AAAA)">`, false},
		{"fragment", `<svg><rect fill="url(#g)"/></svg>`, `<svg><rect fill="url(#g)"/></svg>`, false},
		{"listed functions", `<p style="width:calc(100% - 2px);transform:rotate(1deg);color:light-dark(red,blue)">`,
			`<p style="width:calc(100% - 2px);transform:rotate(1deg);color:light-dark(red,blue)">`, false},
		{"preamble", htmlPreamble, htmlPreamble, false},
		// An SVG attribute the rewrite does not read, which the check does.
		{"unknown SVG attribute", `<svg><rect foo="foo(1)"/></svg>`, `<svg><rect foo="foo(1)"/></svg>`, true},
	}
	for _, p := range cssLoadingProperties {
		cs = append(cs,
			neutralizeCSSCase{p + " attribute", `<p style="` + p + `:url(http://x/` + p + `)">`, `<p style="` + p + `:url(data:,http://x/` + p + `)">`, false},
			neutralizeCSSCase{p + " style", `<style>.a{` + p + `:url("http://x/` + p + `")}</style>`, `<style>.a{` + p + `:url("data:,http://x/` + p + `")}</style>`, false})
	}
	return cs
}()

// cssInsertOnly reports whether out is in with only ";data:,", "data:," or
// "_" inserted. Alignments are ambiguous ("url(d" can be followed by the
// input's "d" or by an inserted "data:,"), so it searches all of them.
func cssInsertOnly(in, out string) bool {
	type pos struct{ i, j int }
	seen := map[pos]bool{}
	stack := []pos{{0, 0}}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		if p.i == len(in) && p.j == len(out) {
			return true
		}
		if p.i < len(in) && p.j < len(out) && in[p.i] == out[p.j] {
			stack = append(stack, pos{p.i + 1, p.j + 1})
		}
		for _, t := range []string{";" + neutralizeURLPrefix, neutralizeURLPrefix, neutralizeFuncMark} {
			if strings.HasPrefix(out[p.j:], t) {
				stack = append(stack, pos{p.i, p.j + len(t)})
			}
		}
	}
	return false
}

func TestNeutralizeCSS(t *testing.T) {
	for _, c := range neutralizeCSSCases {
		t.Run(c.name, func(t *testing.T) {
			got := neutralizeCSS(c.in)
			if got != c.want {
				t.Fatalf("neutralizeCSS(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
			if !cssInsertOnly(c.in, got) {
				t.Errorf("not insert-only: %q -> %q", c.in, got)
			}
			if again := neutralizeCSS(got); again != got {
				t.Errorf("not idempotent: %q -> %q", got, again)
			}
			// The CSS rewrite adds nothing for the link and frame rewrite.
			if s, was := neutralizeSurvivors(got), neutralizeSurvivors(c.in); len(s) != len(was) {
				t.Errorf("neutralizeSurvivors(%q) = %v, was %v", got, s, was)
			}
			if s := cssFetchSurvivors(got); (len(s) > 0) != c.survives {
				t.Errorf("cssFetchSurvivors(%q) = %v", got, s)
			}
			if c.in == c.want && unsafe.StringData(got) != unsafe.StringData(c.in) {
				t.Errorf("unchanged input %q was copied", c.in)
			}
		})
	}
}

// The check is not vacuous: it finds the remote URL or function in every
// input the rewrite changes, before the rewrite.
func TestCSSFetchSurvivorsOracle(t *testing.T) {
	for _, c := range neutralizeCSSCases {
		// An HTML element's color attribute is not CSS: rewritten, but
		// harmless either way.
		if c.name == "presentation attribute on an HTML element" {
			continue
		}
		if c.in != c.want && len(cssFetchSurvivors(c.in)) == 0 {
			t.Errorf("oracle finds nothing in %q", c.in)
		}
	}
	for _, in := range []string{
		`<math><mi style="background:url(http://x/a)">x</mi></math>`,
		`<math><style>.a{background:url(http://x/a)}</style></math>`,
		`<template><p style="background:url(http://x/a)"></p></template>`,
		`<svg><rect fill="image-set('http://x/a' 1x)"/></svg>`,
	} {
		if len(cssFetchSurvivors(in)) == 0 {
			t.Errorf("oracle finds nothing in %q", in)
		}
	}
}

// cssSVGStyleSurvivors are SVG <style> contents the rewrite cannot see as
// the browser does: the browser's CSS is the direct text children joined,
// with references decoded, while the rewrite reads the raw text. Each one
// leaks the origin without the fix and must be refused on its own.
var cssSVGStyleSurvivors = map[string]string{
	"comment":            `<svg><style>.a{fill:url<!--c-->(http://x/a)}</style></svg>`,
	"comment around":     `<svg><style>.a{fill:u<!--/*-->rl(http://x/a)<!--*/-->}</style></svg>`,
	"CDATA":              `<svg><style>.a{fill:url<![CDATA[(http://x/a)]]>}</style></svg>`,
	"decimal reference":  `<svg><style>.a{fill:url&#40;http://x/a)}</style></svg>`,
	"hex reference":      `<svg><style>.a{fill:url&#x28;http://x/a)}</style></svg>`,
	"processing instr.":  `<svg><style>.a{fill:url<?x?>(http://x/a)}</style></svg>`,
	"child element":      `<svg><style>.a{fill:url<g>zz</g>(http://x/a)}</style></svg>`,
	"select":             `<select><svg><style>.a{fill:url<!--c-->(http://x/a)}</style></svg></select>`,
	"select div":         `<select><div><svg><style>.a{fill:url<!--c-->(http://x/a)}</style></svg></div></select>`,
	"MathML style":       `<math><style>.a{background:url<!--c-->(http://x/a)}</style></math>`,
	"function reference": `<svg><style>.a{fill:image-set&#40;"http://x/a" 1x)}</style></svg>`,
}

func TestCSSFetchSurvivorsSVGStyle(t *testing.T) {
	for name, in := range cssSVGStyleSurvivors {
		served := neutralizeCSS(neutralizeHTML(in, testAppHosts))
		if s := cssFetchSurvivors(served); len(s) == 0 {
			t.Errorf("%s: nothing found in %q", name, served)
		}
	}
}

// cssParserDivergences are mails where x/net/html v0.62.0 builds a
// different tree than Chromium (found in review), so the parse check
// does not see the SVG <style> the browser applies: a <template> inside a
// MathML or SVG integration point ends x/net/html's document, and a
// duplicate attribute with the unquoted value "/" makes x/net/html's
// Tokenizer call the tag self-closing, so the <style> after it lands in the
// HTML namespace. Each one leaked the origin in Chromium 156 after "Load
// remote images"; foreignContent refuses them.
var cssParserDivergences = map[string]string{
	"template in mi":    `<math><mi><template></template></mi></math><svg><style>div{background-image:url<!--c-->(http://x/a)}</style></svg><div>x</div>`,
	"template in mtext": `<math><mtext><template></template></mtext></math><svg><style>div{background-image:url<![CDATA[(]]>http://x/a)}</style></svg><div>x</div>`,
	"template in foreignObject div": `<svg><foreignObject><div><template></template></div></foreignObject></svg>` +
		`<svg><style>div{background-image:url<!--c-->(http://x/a)}</style></svg><div>x</div>`,
	"duplicate attribute slash": `<svg><foreignObject><svg data-x=a data-x=/><style>div{background-image:url<!--c-->(http://x/a)}</style></svg></foreignObject></svg><div>x</div>`,
	"bare duplicate slash":      `<svg><foreignObject><svg data-x data-x=/><style>div{background-image:url<!--c-->(http://x/a)}</style></svg></foreignObject></svg><div>x</div>`,
}

// The divergences are real: the parse check alone finds nothing in their
// served bytes (if x/net/html is fixed, this test says so, and the cases
// stay as regression cases of foreignContent).
func TestCSSParserDivergencesEscapeCheck(t *testing.T) {
	for name, in := range cssParserDivergences {
		served := neutralizeCSS(neutralizeHTML(in, testAppHosts))
		if served != in {
			t.Errorf("%s: the rewrite changed %q to %q", name, in, served)
		}
		if s := cssFetchSurvivors(served); len(s) > 0 {
			t.Logf("%s: the parse check now finds %v", name, s)
		}
		if f := foreignContent(served); f == "" {
			t.Errorf("%s: foreignContent finds nothing in %q", name, served)
		}
	}
}

func TestForeignContent(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{``, ""},
		{`<p>plain</p><img src="https://x/a">`, ""},
		{`<svg>`, "svg"},
		{`<SVG width=1>`, "svg"},
		{`<sVg/>`, "svg"},
		{`<svgx>`, "svg"},
		{`<math>`, "math"},
		{`<MaTh display=block>`, "math"},
		{`<p>x</p><math><mi>1</mi></math><svg>`, "math"},
		{`<p title="<svg">`, "svg"},
		{`<!-- <svg> -->`, "svg"},
		{`text <svg`, "svg"},
		{`</svg></math>`, ""},
		{`&lt;svg&gt; &lt;math&gt;`, ""},
		{`< svg>`, ""},
		{`<sv`, ""},
		{`<mat`, ""},
		{`<s` + "\u017f" + `vg><ſvg><mAth`, "math"},
		{`<<svg`, "svg"},
	} {
		if got := foreignContent(c.in); got != c.want {
			t.Errorf("foreignContent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The check skips SVG attributes that are not CSS, so ordinary SVG keeps
// its remote images.
func TestCSSFetchSurvivorsSkips(t *testing.T) {
	for _, in := range []string{
		`<svg xmlns:inkscape="x"><g inkscape:label="Layer(1)" class="bg-[url(x)]" id="a(b)" aria-label="x(" data-x="y(" ` +
			`href="url(x)" xlink:title="Sale(new)" transform="translate(1,2) rotate(3)"/></svg>`,
		`<img alt="url(http://x/a)" src="http://x/b">`,
		`<p title="image-set(">x</p>`,
		`<svg><text>url(http://x/a)</text></svg>`,
	} {
		if s := cssFetchSurvivors(in); len(s) > 0 {
			t.Errorf("%q: %v", in, s)
		}
	}
}

// benchNewsletter is a newsletter of about 300 KB: tables with inline
// styles, a <style> block, images and links.
func benchNewsletter() string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><style>body{margin:0;font:14px/1.4 Arial,sans-serif}` +
		`.hero{background:#036 url(https://news.example/hero.jpg) center/cover no-repeat}` +
		`@media (max-width:600px){.col{width:100%!important}}</style></head><body>`)
	for b.Len() < 300<<10 {
		b.WriteString(`<table role="presentation" width="100%" style="border-collapse:collapse;background:linear-gradient(#fff,#eee)">` +
			`<tr><td class="hero" style="padding:16px;background-image:url('https://news.example/bg.png');color:rgb(20 20 20)">` +
			`<img src="https://news.example/item.jpg" width="200" alt="Item" style="display:block;width:calc(100% - 8px)">` +
			`<a href="https://news.example/item?id=1&amp;s=2" style="color:#036">Read more</a></td></tr></table>` + "\n")
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

// benchInline is a body at the inline budget: 10 MiB of images as data:
// URLs, about 13.4 MiB of base64, in <img> and in a CSS background.
func benchInline() map[string]string {
	b64 := strings.Repeat("iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2", (10<<20)*4/3/44/2)
	return map[string]string{
		"img": `<p>x</p><img src="data:image/png;base64,` + b64 + `"><img src="data:image/png;base64,` + b64 + `">`,
		"css": `<p>x</p><div style="background:url(data:image/png;base64,` + b64 + `)"></div>` +
			`<style>.a{background:url("data:image/png;base64,` + b64 + `")}</style>`,
	}
}

func BenchmarkCSSCheck(b *testing.B) {
	inputs := map[string]string{"newsletter": benchNewsletter()}
	for k, v := range benchInline() {
		inputs["inline-"+k] = v
	}
	for _, name := range []string{"newsletter", "inline-img", "inline-css"} {
		in := inputs[name]
		served := neutralizeCSS(in)
		b.Run("rewrite/"+name, func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			b.ReportAllocs()
			for b.Loop() {
				neutralizeCSS(in)
			}
		})
		b.Run("check/"+name, func(b *testing.B) {
			b.SetBytes(int64(len(served)))
			b.ReportAllocs()
			for b.Loop() {
				if s := cssFetchSurvivors(served); len(s) > 0 {
					b.Fatal(s)
				}
			}
		})
	}
}
