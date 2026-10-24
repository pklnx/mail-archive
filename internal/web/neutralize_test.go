package web

import (
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/net/html"
)

// neutralizeCase is one input of neutralizeHTML and its exact output.
type neutralizeCase struct {
	name, in, want string
}

// neutralizeCases holds one table per rule, plus the inputs that must come
// back unchanged. They also seed FuzzNeutralizeHTML.
var neutralizeCases = map[string][]neutralizeCase{
	"shadowroot": {
		{"open", `<div><template shadowrootmode="open"><a href="/api/x">x</a></template></div>`,
			`<div><template data-shadowrootmode="open"><a href="/api/x">x</a></template></div>`},
		{"closed", `<div><template shadowrootmode=closed><a href="/api/x">x</a></template></div>`,
			`<div><template data-shadowrootmode=closed><a href="/api/x">x</a></template></div>`},
		{"legacy", `<div><template shadowroot="closed">x</template></div>`,
			`<div><template data-shadowroot="closed">x</template></div>`},
		{"uppercase", `<div><TEMPLATE ShadowRootMode=Closed>x</TEMPLATE></div>`,
			`<div><TEMPLATE data-ShadowRootMode=Closed>x</TEMPLATE></div>`},
		{"mixed case tag", `<TeMpLaTe SHADOWROOTMODE=closed>`, `<TeMpLaTe data-SHADOWROOTMODE=closed>`},
		{"attributes before and after", `<template id=a class="b" shadowrootmode=closed title='c'>`,
			`<template id=a class="b" data-shadowrootmode=closed title='c'>`},
		{"valueless attribute before", `<template hidden shadowrootmode=closed>`, `<template hidden data-shadowrootmode=closed>`},
		{"unquoted value before", `<template a=b shadowrootmode=closed>`, `<template a=b data-shadowrootmode=closed>`},
		{"form feed", "<template\fshadowrootmode=closed>", "<template\fdata-shadowrootmode=closed>"},
		{"tab, LF and CR", "<template\t\r\n shadowrootmode=closed\r\nx>", "<template\t\r\n data-shadowrootmode=closed\r\nx>"},
		{"slash separator", `<template/shadowrootmode=closed>`, `<template/data-shadowrootmode=closed>`},
		{"slash and space", `<template / shadowrootmode=closed>`, `<template / data-shadowrootmode=closed>`},
		{"slash after a value", `<template a="x"/shadowrootmode=closed>`, `<template a="x"/data-shadowrootmode=closed>`},
		{"no space after a quoted value", `<template x="a"shadowrootmode=closed>`, `<template x="a"data-shadowrootmode=closed>`},
		{"no space after a single-quoted value", `<template x='a'shadowrootmode=closed>`, `<template x='a'data-shadowrootmode=closed>`},
		{"spaces around the equals sign", `<template a = "x" shadowrootmode = closed>`, `<template a = "x" data-shadowrootmode = closed>`},
		{"entity in the value", `<template shadowrootmode="&#99;losed">`, `<template data-shadowrootmode="&#99;losed">`},
		{"gt in quoted values", `<template title="a>b" alt='>' shadowrootmode=closed>`,
			`<template title="a>b" alt='>' data-shadowrootmode=closed>`},
		{"duplicates", `<template shadowrootmode=bogus shadowrootmode=closed>`,
			`<template data-shadowrootmode=bogus data-shadowrootmode=closed>`},
		{"other shadowroot attributes", `<template shadowrootmode=open shadowrootclonable shadowrootdelegatesfocus shadowrootserializable>`,
			`<template data-shadowrootmode=open data-shadowrootclonable data-shadowrootdelegatesfocus data-shadowrootserializable>`},
		{"self-closing", `<template shadowrootmode=closed/>`, `<template data-shadowrootmode=closed/>`},
		{"templates in templates", `<template shadowrootmode=open><div><template shadowrootmode=closed>x</template></div></template>`,
			`<template data-shadowrootmode=open><div><template data-shadowrootmode=closed>x</template></div></template>`},
		{"cut off in the name", `<p><template shadowrootmode`, `<p><template data-shadowrootmode`},
		{"cut off in the value", `<p><template shadowrootmode="clo`, `<p><template data-shadowrootmode="clo`},
		{"noscript context", `<body><noscript><div><template shadowrootmode=closed><a href=/api/x>x</a></template></div></noscript>`,
			`<body><noscript><div><template data-shadowrootmode=closed><a href=/api/x>x</a></template></div></noscript>`},
		{"math title context", `<math><mtext><style></style></mtext><title><div><template shadowrootmode=closed>x</template></div></title></math>`,
			`<math><mtext><style></style></mtext><title><div><template data-shadowrootmode=closed>x</template></div></title></math>`},
		{"svg breakout", `<svg><p><template shadowrootmode=closed>x</template></svg>`,
			`<svg><p><template data-shadowrootmode=closed>x</template></svg>`},
		{"in a comment", `<!-- <template shadowrootmode=closed> -->`, `<!-- <template data-shadowrootmode=closed> -->`},
		{"in style", `<style><!--</style><template shadowrootmode=closed>x</template>`,
			`<style><!--</style><template data-shadowrootmode=closed>x</template>`},
		{"in an attribute value", `<p title="<template shadowrootmode=closed>">`, `<p title="<template data-shadowrootmode=closed>">`},
		{"candidate in a candidate's value", `<template a="<template shadowroot=x>" shadowrootmode=closed>`,
			`<template a="<template data-shadowroot=x>" data-shadowrootmode=closed>`},
		{"double less-than", `<<template shadowrootmode=closed>`, `<<template data-shadowrootmode=closed>`},
	},
	"srcdoc": {
		{"plain", `<iframe srcdoc="&lt;a href=/api/x target=_blank&gt;x&lt;/a&gt;"></iframe>`,
			`<iframe data-srcdoc="&lt;a href=/api/x target=_blank&gt;x&lt;/a&gt;"></iframe>`},
		{"uppercase", `<IFRAME SrcDoc="x"></IFRAME>`, `<IFRAME data-SrcDoc="x"></IFRAME>`},
		{"form feed", "<iframe\fsrcdoc=x></iframe>", "<iframe\fdata-srcdoc=x></iframe>"},
		{"slash separator", `<iframe/srcdoc=x></iframe>`, `<iframe/data-srcdoc=x></iframe>`},
		{"no space after a quoted value", `<iframe style="border:0"srcdoc=x></iframe>`, `<iframe style="border:0"data-srcdoc=x></iframe>`},
		{"entity-escaped content", `<iframe srcdoc="&#x3C;a href=/api/x&#x3E;x&#x3C;/a&#x3E;"></iframe>`,
			`<iframe data-srcdoc="&#x3C;a href=/api/x&#x3E;x&#x3C;/a&#x3E;"></iframe>`},
		{"in noscript", `<noscript><iframe srcdoc=x></iframe></noscript>`, `<noscript><iframe data-srcdoc=x></iframe></noscript>`},
		{"content with an escaped closed root", `<iframe srcdoc="&lt;template shadowrootmode=closed&gt;x&lt;/template&gt;"></iframe>`,
			`<iframe data-srcdoc="&lt;template shadowrootmode=closed&gt;x&lt;/template&gt;"></iframe>`},
		{"content with a literal closed root", `<iframe srcdoc="<div><template shadowrootmode=closed>x</template></div>"></iframe>`,
			`<iframe data-srcdoc="<div><template data-shadowrootmode=closed>x</template></div>"></iframe>`},
		{"other srcdoc attributes", `<iframe srcdocx=1 SRCDOC=2>`, `<iframe data-srcdocx=1 data-SRCDOC=2>`},
		{"cut off", `<p><iframe srcdoc`, `<p><iframe data-srcdoc`},
		{"both rules", `<template shadowrootmode=closed><iframe srcdoc=x></iframe></template>`,
			`<template data-shadowrootmode=closed><iframe data-srcdoc=x></iframe></template>`},
	},
	"unchanged": {
		{"empty", ``, ``},
		{"neither tag", `<p>Hello <b>world</b></p>`, `<p>Hello <b>world</b></p>`},
		{"already prefixed", `<template data-shadowrootmode=closed><iframe data-srcdoc=x>`,
			`<template data-shadowrootmode=closed><iframe data-srcdoc=x>`},
		{"longer tag names", `<templates shadowrootmode=closed><template-x shadowrootmode=closed><iframes srcdoc=x><iframe-x srcdoc=x>`,
			`<templates shadowrootmode=closed><template-x shadowrootmode=closed><iframes srcdoc=x><iframe-x srcdoc=x>`},
		{"attributes on other elements", `<div shadowrootmode=closed srcdoc=x><iframe shadowrootmode=closed><template srcdoc=x>`,
			`<div shadowrootmode=closed srcdoc=x><iframe shadowrootmode=closed><template srcdoc=x>`},
		{"words in text", `template shadowrootmode iframe srcdoc`, `template shadowrootmode iframe srcdoc`},
		{"in values", `<template title="shadowrootmode=closed" alt='srcdoc'><iframe title=srcdoc>`,
			`<template title="shadowrootmode=closed" alt='srcdoc'><iframe title=srcdoc>`},
		{"after a valueless equals", `<template =shadowrootmode>`, `<template =shadowrootmode>`},
		{"iframe without srcdoc", `<iframe src="https://x.example/" width=10></iframe>`,
			`<iframe src="https://x.example/" width=10></iframe>`},
		{"end tags", `</template shadowrootmode=closed></iframe srcdoc=x>`, `</template shadowrootmode=closed></iframe srcdoc=x>`},
		{"after the tag ends", `<template>shadowrootmode=closed</template><iframe> srcdoc=x`,
			`<template>shadowrootmode=closed</template><iframe> srcdoc=x`},
		{"non-ASCII case folding", "<template \u017fhadowrootmode=closed><iframe \u017frcdoc=x>",
			"<template \u017fhadowrootmode=closed><iframe \u017frcdoc=x>"},
		{"cut off tag name", `<p><template`, `<p><template`},
	},
}

func TestNeutralizeHTML(t *testing.T) {
	for rule, cases := range neutralizeCases {
		for _, c := range cases {
			t.Run(rule+"/"+c.name, func(t *testing.T) {
				got := neutralizeHTML(c.in)
				if got != c.want {
					t.Errorf("neutralizeHTML(%q)\n got %q\nwant %q", c.in, got, c.want)
				}
				if s := neutralizeSurvivors(got); len(s) > 0 {
					t.Errorf("oracle: %q keeps %v", got, s)
				}
				if again := neutralizeHTML(got); again != got {
					t.Errorf("not idempotent: %q -> %q", got, again)
				}
				// Unchanged input comes back without a copy.
				if c.in == c.want && c.in != "" && unsafe.StringData(got) != unsafe.StringData(c.in) {
					t.Errorf("unchanged input %q was copied", c.in)
				}
			})
		}
	}
}

// The parse oracle is not vacuous: it finds the attributes in inputs that
// x/net/html parses into such elements.
func TestNeutralizeSurvivorsOracle(t *testing.T) {
	for _, in := range []string{
		`<div><template shadowrootmode=closed>x</template></div>`,
		`<div><template><template ShadowRoot=open>x</template></template></div>`,
		`<noscript><iframe srcdoc=x></iframe></noscript>`,
		`<template x="a"shadowrootmode=closed>`,
		`<iframe/SRCDOC=x>`,
	} {
		if len(neutralizeSurvivors(in)) == 0 {
			t.Errorf("oracle finds nothing in %q", in)
		}
	}
}

// neutralizeSurvivors parses doc as x/net/html does with scripting off (as in
// the sandboxed frame), walks every element including template contents and
// returns each HTML-namespace element of a rule that keeps an attribute with
// the rule's prefix.
func neutralizeSurvivors(doc string) []string {
	root, err := html.ParseWithOptions(strings.NewReader(doc), html.ParseOptionEnableScripting(false))
	if err != nil {
		return []string{"parse error: " + err.Error()}
	}
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Namespace == "" {
			for _, ru := range neutralizeRules {
				if n.Data != ru.tag {
					continue
				}
				for _, a := range n.Attr {
					if strings.HasPrefix(strings.ToLower(a.Key), ru.prefix) {
						out = append(out, n.Data+" "+a.Key)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// neutralizeInsertions checks that out is in with neutralizePrefix inserted
// only directly before a rule prefix (ASCII case-insensitive), and returns
// the insertion points as offsets in in. ok is false otherwise.
func neutralizeInsertions(in, out string) (at []int, ok bool) {
	i, j := 0, 0
	for i < len(in) || j < len(out) {
		if i < len(in) && j < len(out) && in[i] == out[j] {
			i++
			j++
			continue
		}
		if !strings.HasPrefix(out[j:], neutralizePrefix) {
			return nil, false
		}
		matches := false
		for _, ru := range neutralizeRules {
			matches = matches || hasPrefixASCIIFold(in[i:], ru.prefix)
		}
		if !matches {
			return nil, false
		}
		at = append(at, i)
		j += len(neutralizePrefix)
	}
	return at, true
}

// neutralizeTokens returns the token types of doc as x/net/html's Tokenizer
// splits it, and the end offset of each token.
func neutralizeTokens(doc string) (types []html.TokenType, ends []int) {
	z := html.NewTokenizer(strings.NewReader(doc))
	off := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return types, ends
		}
		off += len(z.Raw())
		types = append(types, tt)
		ends = append(ends, off)
	}
}

// cleanLikeMIME applies what clean() in internal/mime does to every body
// before it is served: valid UTF-8, no NUL.
func cleanLikeMIME(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

func FuzzNeutralizeHTML(f *testing.F) {
	for _, cases := range neutralizeCases {
		for _, c := range cases {
			f.Add(c.in)
		}
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := neutralizeHTML(in)
		// 1. Alignment: only insertions of the prefix, each directly before
		// a rule prefix. This holds for any bytes.
		if _, ok := neutralizeInsertions(in, out); !ok {
			t.Fatalf("not an insertion of %q before a rule prefix:\n in %q\nout %q", neutralizePrefix, in, out)
		}
		// 4. Idempotent.
		if again := neutralizeHTML(out); again != out {
			t.Fatalf("not idempotent:\n in %q\nout %q\n2nd %q", in, out, again)
		}

		// The x/net/html checks run on what the handler can see: valid
		// UTF-8 without NUL.
		in = cleanLikeMIME(in)
		out = neutralizeHTML(in)
		at, ok := neutralizeInsertions(in, out)
		if !ok {
			t.Fatalf("not an insertion:\n in %q\nout %q", in, out)
		}
		// 2. Every candidate, tokenized on its own, has no attribute with
		// its rule's prefix.
		for p := strings.IndexByte(out, '<'); p >= 0; {
			for _, ru := range neutralizeRules {
				e := p + 1 + len(ru.tag)
				if e >= len(out) || !hasPrefixASCIIFold(out[p+1:], ru.tag) || !isTagNameEnd(out[e]) {
					continue
				}
				z := html.NewTokenizer(strings.NewReader(out[p:]))
				if tt := z.Next(); tt != html.StartTagToken && tt != html.SelfClosingTagToken {
					continue
				}
				tok := z.Token()
				for _, a := range tok.Attr {
					if strings.HasPrefix(a.Key, ru.prefix) {
						t.Fatalf("candidate at %d keeps %q:\n in %q\nout %q", p, a.Key, in, out)
					}
				}
			}
			n := strings.IndexByte(out[p+1:], '<')
			if n < 0 {
				break
			}
			p += 1 + n
		}
		// 3. The parse oracle.
		if s := neutralizeSurvivors(out); len(s) > 0 {
			t.Fatalf("oracle: %v survive:\n in %q\nout %q", s, in, out)
		}
		// 5. Token boundaries and token types are unchanged: every token of
		// the output is the input's token with the insertions inside it.
		ti, ei := neutralizeTokens(in)
		to, eo := neutralizeTokens(out)
		if len(ti) != len(to) {
			t.Fatalf("token count %d -> %d:\n in %q\nout %q", len(ti), len(to), in, out)
		}
		for k := range ti {
			shift := 0
			for _, p := range at {
				if p < ei[k] {
					shift += len(neutralizePrefix)
				}
			}
			if ti[k] != to[k] || ei[k]+shift != eo[k] {
				t.Fatalf("token %d: %v ending at %d -> %v ending at %d:\n in %q\nout %q", k, ti[k], ei[k], to[k], eo[k], in, out)
			}
		}
	})
}

// benchInputs builds the 4 MiB inputs in memory.
func benchInputs() map[string]string {
	const size = 4 << 20
	fill := func(unit string) string {
		return strings.Repeat(unit, size/len(unit)+1)[:size]
	}
	return map[string]string{
		// A typical mail: tables, styles and links, no candidate.
		"plain": fill(`<table role="presentation" style="width:100%"><tr><td style="padding:8px;font:14px sans-serif">` +
			`Hello, here is the <a href="https://news.example/item?id=1&amp;s=2">news</a> of the week.</td></tr></table>` + "\n"),
		// Candidates inside other candidates' quoted values: candidates of
		// both rules are active at once, in different states, and two
		// names per unit get the prefix.
		"adversarial": fill(`<template a="<iframe b='<template shadowroot=x <iframe srcdoc=y `),
	}
}

func BenchmarkNeutralizeHTML(b *testing.B) {
	for _, name := range []string{"plain", "adversarial"} {
		in := benchInputs()[name]
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			for b.Loop() {
				neutralizeHTML(in)
			}
		})
	}
}
