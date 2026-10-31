package web

import (
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/net/html"
	"golang.org/x/net/idna"

	"github.com/pklnx/mail-archive/internal/mime"
)

// neutralizeCase is one input of neutralizeHTML and its exact output.
type neutralizeCase struct {
	name, in, want string
}

// testHosts are the app's host names in the tests: the defaults and a public
// URL's host.
var testHosts = []string{"localhost", "127.0.0.1", "::1", "archive.example"}

var testAppHosts = newAppHosts(testHosts...)

// linkCases are cases for href values: <a href="value"> in double quotes,
// renamed or kept.
func linkCases(kept bool, values ...string) []neutralizeCase {
	cs := make([]neutralizeCase, 0, len(values))
	for _, v := range values {
		in := `<a href="` + v + `">x</a>`
		want := `<a data-href="` + v + `">x</a>`
		if kept {
			want = in
		}
		name := v
		if len(name) > 40 {
			name = name[:40] + "..."
		}
		cs = append(cs, neutralizeCase{name, in, want})
	}
	return cs
}

// neutralizeCases holds one table per rule, plus the inputs that must come
// back unchanged. They also seed FuzzNeutralizeHTML.
var neutralizeCases = map[string][]neutralizeCase{
	"shadowroot": {
		{"open", `<div><template shadowrootmode="open"><a href="/api/x">x</a></template></div>`,
			`<div><template data-shadowrootmode="open"><a data-href="/api/x">x</a></template></div>`},
		{"closed", `<div><template shadowrootmode=closed><a href="/api/x">x</a></template></div>`,
			`<div><template data-shadowrootmode=closed><a data-href="/api/x">x</a></template></div>`},
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
		{"slash after a valueless name", `<template a/shadowrootmode=closed>`, `<template a/data-shadowrootmode=closed>`},
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
			`<body><noscript><div><template data-shadowrootmode=closed><a data-href=/api/x>x</a></template></div></noscript>`},
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
		{"slash after a valueless name", `<iframe a/srcdoc=x></iframe>`, `<iframe a/data-srcdoc=x></iframe>`},
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
	// Links into the app, or not to an http(s), mailto: or tel: URL: the
	// oracle sees each of them (TestLinkSurvivorsOracle).
	"link renamed": linkCases(false,
		// Relative forms.
		"/api/x", "api/x", "../x", "?q", "#top", "", "  ", "\t",
		// Slash and scheme tricks.
		"//localhost/api", "/\\localhost/api", "\\\\localhost/api", "http:/api", "http:api", "https:\\\\localhost/",
		// The app's own address.
		"http://localhost:18181/api", "HTTP://LOCALHOST/", "http://localhost./", "https://archive.example/x", "//Archive.Example.:8443/",
		"http://127.0.0.1/", "http://127.0.0.1./api", "http://links.example@localhost/", "http://%6c%6fcalhost/",
		"http://loc\nalhost/", "\x01http://localhost/", "http://localhost\\@links.example/",
		"http://127.1/", "http://2130706433/", "http://0x7f.1/", "http://0177.0.0.1/", "http://[::1]/",
		"http://\uff4c\uff4f\uff43\uff41\uff4c\uff48\uff4f\uff53\uff54/", "http://loc&shy;alhost/", "http://loc\u00adalhost/",
		// Other schemes.
		"javascript:alert(1)", "JaVaScRiPt:x", "java&#x0A;script:x", "data:text/html,x", "vbscript:x", "blob:http://localhost/u",
		"file:///etc/passwd", "about:blank", "ftp://links.example/", "sms:+49", " mailto", "mailto",
	),
	// External links the literal-only grammar renames all the same: a
	// visible cost, no safety need.
	"link renamed, literal only": linkCases(false,
		"///links.example/", "http://user@links.example/", "http&#58;//links.example/", "ht&#x09;tp://links.example/",
		"http://links.\texample/", "http://127.0.0.01/", "http://1.2.3.256/",
		"https://links.example:8a/", "https://links.example ", "https://links.example\\", "https://links.example;x",
		// The window: the host does not end within linkWindow bytes.
		"https://"+strings.Repeat("a", linkWindow)+".example/",
		strings.Repeat(" ", linkWindow)+"https://links.example/",
	),
	"link kept": linkCases(true,
		"https://links.example/x", "HTTPS://Links.Example:8443/p?a=1&amp;b=2#f", "//links.example/x", "http://203.0.113.5/",
		"http://links.example?q", "http://links.example#f", "https://xn--bcher-kva.example/", "https://a_b-c.example/",
		"https://links.example", "https://links.example:/", "mailto:a@b", "MAILTO:a@b?subject=x", "tel:+49 30 1", " \x01https://links.example/", "https://links.example/&#58;\t@localhost",
		"https://"+strings.Repeat("a", linkWindow-len("https://.example/")-1)+".example/",
		// Found by the fuzzer: a "%" that starts no escape, after the host.
		"//A#0000%0", "https://links.example/%zz?%", "//A/\x10", "//A/&#11;A", "https://links.example/a b",
	),
	"links": {
		{"unquoted", `<a href=/api/x>x</a>`, `<a data-href=/api/x>x</a>`},
		{"unquoted kept", `<a href=https://links.example>x</a>`, `<a href=https://links.example>x</a>`},
		{"unquoted, white space after", "<a href=https://links.example\fx>", "<a href=https://links.example\fx>"},
		{"single-quoted", `<a href='/api/x'>x</a>`, `<a data-href='/api/x'>x</a>`},
		{"single-quoted kept", `<a href='https://links.example'>x</a>`, `<a href='https://links.example'>x</a>`},
		{"single quote in a double-quoted value", `<a href="https://links.example'">x</a>`, `<a data-href="https://links.example'">x</a>`},
		{"spaces around the equals sign", `<a href = "/x" HREF  =  'https://links.example/'>`, `<a data-href = "/x" HREF  =  'https://links.example/'>`},
		{"valueless", `<a href>x</a>`, `<a data-href>x</a>`},
		{"empty unquoted", `<a href=>x</a>`, `<a data-href=>x</a>`},
		{"valueless before another attribute", `<a href title="https://links.example/">`, `<a data-href title="https://links.example/">`},
		{"uppercase", `<A HREF="/api/x">`, `<A data-HREF="/api/x">`},
		{"area", `<map><area href=/api/x shape=rect coords=0,0,9,9></map>`, `<map><area data-href=/api/x shape=rect coords=0,0,9,9></map>`},
		{"svg a", `<svg><a href="/api/x"><rect/></a></svg>`, `<svg><a data-href="/api/x"><rect/></a></svg>`},
		{"svg a xlink", `<svg><a xlink:href="/api/x"><rect/></a></svg>`, `<svg><a data-xlink:href="/api/x"><rect/></a></svg>`},
		{"svg a xlink uppercase", `<svg><a XLINK:HREF="/api/x"><rect/></a></svg>`, `<svg><a data-XLINK:HREF="/api/x"><rect/></a></svg>`},
		{"svg a xlink kept", `<svg><a xlink:href="https://links.example/"><rect/></a></svg>`, `<svg><a xlink:href="https://links.example/"><rect/></a></svg>`},
		{"use", `<svg><use href="#d"/><use xlink:href="#d"/></svg>`, `<svg><use data-href="#d"/><use data-xlink:href="#d"/></svg>`},
		{"image", `<svg><image href="data:image/png;base64,AAAA"/></svg>`, `<svg><image data-href="data:image/png;base64,AAAA"/></svg>`},
		{"mathml", `<math><mi href="/api/x">x</mi></math>`, `<math><mi data-href="/api/x">x</mi></math>`},
		{"link and base", `<link rel=stylesheet href=/api/x><base href="/api/">`, `<link rel=stylesheet data-href=/api/x><base data-href="/api/">`},
		{"any element", `<div href=/x><custom-el href=/x><p:q href=/x>`, `<div data-href=/x><custom-el data-href=/x><p:q data-href=/x>`},
		{"ping", `<a href="https://links.example/" ping="https://links.example/p">x</a>`, `<a href="https://links.example/" data-ping="https://links.example/p">x</a>`},
		{"ping uppercase, valueless", `<a PING>`, `<a data-PING>`},
		{"meta refresh", `<META HTTP-EQUIV=Refresh content="0;url=/api/x">`, `<META data-HTTP-EQUIV=Refresh content="0;url=/api/x">`},
		{"http-equiv elsewhere", `<p http-equiv=x>`, `<p data-http-equiv=x>`},
		{"set", `<svg><a><set attributeName=href to=/x /><rect/></a></svg>`, `<svg><a><set data-attributeName=href to=/x /><rect/></a></svg>`},
		{"animate", `<svg><a><animate attributeName="xlink:href" values="/x"/></a></svg>`, `<svg><a><animate data-attributeName="xlink:href" values="/x"/></a></svg>`},
		{"attributename uppercase", `<svg><set ATTRIBUTENAME="href" to="/x"/></svg>`, `<svg><set data-ATTRIBUTENAME="href" to="/x"/></svg>`},
		{"attributename in a template", `<template><svg><set attributename=href to=/x /></svg></template>`, `<template><svg><set data-attributename=href to=/x /></svg></template>`},
		{"attributename in foreign content", `<math><mtext><svg><animate attributeName=href values=/x /></svg></mtext></math>`, `<math><mtext><svg><animate data-attributeName=href values=/x /></svg></mtext></math>`},
		{"in style", `<style><a href=/x></style>`, `<style><a data-href=/x></style>`},
		{"in textarea", `<textarea><a href=/x></textarea>`, `<textarea><a data-href=/x></textarea>`},
		{"in title", `<title><a href=/x></title>`, `<title><a data-href=/x></title>`},
		{"in noscript", `<noscript><a href=/x></noscript>`, `<noscript><a data-href=/x></noscript>`},
		{"in a comment", `<!-- <a href=/x> -->`, `<!-- <a data-href=/x> -->`},
		{"in an attribute value", `<p title="<a href=/x>">`, `<p title="<a data-href=/x>">`},
		{"attribute in an attribute value", `<p title="x href=/x">`, `<p title="x href=/x">`},
		{"attributename in style", `<style><set attributeName=href></style>`, `<style><set data-attributeName=href></style>`},
		{"attributename in a value", `<p title="<set attributeName=href to=/x>">`, `<p title="<set data-attributeName=href to=/x>">`},
		{"slash separator", `<a/href=/x>`, `<a/data-href=/x>`},
		{"no space after a quoted value", `<a x="y"href=/x>`, `<a x="y"data-href=/x>`},
		// Before cid: inlining, href is in the value of cid:q; servedMailHTML
		// renames after inlining (TestServedMailHTMLLinksAfterInlining).
		{"before cid: inlining", `<a cid:q= href=/api>`, `<a cid:q= href=/api>`},
		{"duplicates", `<a href=/api href=https://links.example>`, `<a data-href=/api href=https://links.example>`},
		{"duplicates reversed", `<a href=https://links.example href=/api>`, `<a href=https://links.example data-href=/api>`},
		{"cut off in the value", `<a href=/x`, `<a data-href=/x`},
		{"cut off after the name", `<a href`, `<a data-href`},
		{"white space around equals", "<a href" + strings.Repeat(" ", 1000) + "=" + strings.Repeat("\n", 1000) + "https://links.example/>",
			"<a href" + strings.Repeat(" ", 1000) + "=" + strings.Repeat("\n", 1000) + "https://links.example/>"},
	},
	"unchanged": {
		{"empty", ``, ``},
		{"already renamed", `<a data-href=/x data-ping=/x><set data-attributename=href><meta data-http-equiv=refresh>`,
			`<a data-href=/x data-ping=/x><set data-attributename=href><meta data-http-equiv=refresh>`},
		{"longer and shorter names", `<a hrefx=/x xhref=/x hre=/x pings=/x attributenames=href http-equivx=refresh xlink:hrefs=/x>`,
			`<a hrefx=/x xhref=/x hre=/x pings=/x attributenames=href http-equivx=refresh xlink:hrefs=/x>`},
		{"names in text", `a href=/x ping attributeName=href http-equiv=refresh`, `a href=/x ping attributeName=href http-equiv=refresh`},
		{"not a tag", `< a href=/x><1 href=/x><!x href=/x></a href=/x>`, `< a href=/x><1 href=/x><!x href=/x></a href=/x>`},
		{"after the tag ends", `<a>href=/x</a>`, `<a>href=/x</a>`},
		{"non-ASCII case folding", "<a \u0127ref=/x>", "<a \u0127ref=/x>"},
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
				got := neutralizeHTML(c.in, testAppHosts)
				if got != c.want {
					t.Errorf("neutralizeHTML(%q)\n got %q\nwant %q", c.in, got, c.want)
				}
				if s := neutralizeSurvivors(got); len(s) > 0 {
					t.Errorf("oracle: %q keeps %v", got, s)
				}
				if again := neutralizeHTML(got, testAppHosts); again != got {
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

// The link oracle is not vacuous: it finds the link, or the attribute, in
// each input before the rewrite.
func TestLinkSurvivorsOracle(t *testing.T) {
	for _, c := range neutralizeCases["link renamed"] {
		if len(neutralizeSurvivors(c.in)) == 0 {
			t.Errorf("oracle finds nothing in %q", c.in)
		}
	}
	for _, in := range []string{
		`<svg><a xlink:href="/api/x"><rect/></a></svg>`,
		`<math><mi href="/api/x">x</mi></math>`,
		`<template><a href="//localhost/">x</a></template>`,
		`<svg><use href="#d"/></svg>`,
		`<a href=x ping=https://links.example/>`,
		`<svg><set attributeName=href to=/x /></svg>`,
		`<META HTTP-EQUIV=Refresh content="0;url=/api/x">`,
		`<a href=http://LOCALHOST.:1/>`,
	} {
		if len(neutralizeSurvivors(in)) == 0 {
			t.Errorf("oracle finds nothing in %q", in)
		}
	}
	// And it passes what neutralizeHTML keeps.
	for _, c := range neutralizeCases["link kept"] {
		if s := neutralizeSurvivors(c.in); len(s) > 0 {
			t.Errorf("oracle reports %v in %q", s, c.in)
		}
	}
}

// After inlining, a cid: reference whose match runs over "=" leaves href an
// attribute name: servedMailHTML renames it, because neutralizeHTML runs
// last (#266, #459).
func TestServedMailHTMLLinksAfterInlining(t *testing.T) {
	parts, open := servedCIDParts(t)
	served, _, err := servedMailHTML(`<a cid:q= href=/api/x>x</a>`, parts, open, testAppHosts)
	if err != nil {
		t.Fatal(err)
	}
	if want := htmlPreamble + `<a data:, data-href=/api/x>x</a>`; served != want {
		t.Errorf("served %q, want %q", served, want)
	}
}

// neutralizeSurvivors parses doc as x/net/html does with scripting off (as in
// the sandboxed frame), walks every element including template contents and
// returns each HTML-namespace element of a rule that keeps an attribute with
// the rule's prefix, and each element of any namespace that keeps a link
// attribute linkSurvives reports.
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
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				// The parser adjusts SVG's attributeName and puts xlink:href
				// in the xlink namespace with the key "href".
				if linkSurvives(strings.ToLower(a.Key), a.Val) {
					out = append(out, n.Data+" "+a.Namespace+" "+a.Key+"="+a.Val)
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

// escapeStrayPercent replaces each "%" not followed by two hex digits with
// "%25", and percent-encodes controls, spaces and DEL.
func escapeStrayPercent(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '%' && (i+2 >= len(v) || !isHex(v[i+1]) || !isHex(v[i+2])) {
			b.WriteString("%25")
			continue
		}
		if v[i] <= ' ' || v[i] == 0x7f {
			fmt.Fprintf(&b, "%%%02X", v[i])
			continue
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// oracleBase is the URL of the mail frame's document in the oracle.
var oracleBase, _ = url.Parse("http://localhost:18181/api/messages/" + strings.Repeat("ab", 32) + "/html")

// linkSurvives reports whether an attribute with this (lowercase) key and
// decoded value is one neutralizeHTML must not leave: any ping,
// attributename or http-equiv, and an href or xlink:href that is not an
// http(s) link to a host outside testHosts, a mailto: or a tel: link. It
// works independently of linkKept, on the parsed value, with net/url, IDNA
// mapping and the WHATWG IPv4 parser; where it reads a value differently
// from the browser, it errs towards reporting it.
func linkSurvives(key, val string) bool {
	switch key {
	case "ping", "attributename", "http-equiv":
		return true
	case "href", "xlink:href":
	default:
		return false
	}
	// The URL parser strips leading and trailing C0 controls and spaces, and
	// removes every tab and newline.
	v := strings.TrimFunc(val, func(r rune) bool { return r <= ' ' })
	v = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(v)
	low := strings.ToLower(v)
	if strings.HasPrefix(low, "mailto:") || strings.HasPrefix(low, "tel:") {
		return false
	}
	// Special schemes read "\" as "/". The URL parser leaves a "%" that
	// starts no escape as it is and percent-encodes controls and spaces;
	// net/url would refuse them.
	v = strings.ReplaceAll(v, "\\", "/")
	v = escapeStrayPercent(v)
	ref, err := url.Parse(v)
	if err != nil {
		return true
	}
	u := oracleBase.ResolveReference(ref)
	if u.Scheme != "http" && u.Scheme != "https" {
		return true
	}
	host := u.Hostname()
	if host == "" {
		return true
	}
	for _, r := range host {
		if r > 0x7f {
			a, err := idna.Lookup.ToASCII(host)
			if err != nil {
				return true
			}
			host = a
			break
		}
	}
	host = strings.TrimRight(strings.ToLower(host), ".")
	if ip, ok := whatwgIPv4(host); ok {
		host = ip
	}
	return slices.Contains(testHosts, host)
}

// whatwgIPv4 parses host as the WHATWG URL standard's IPv4 parser does when
// the host ends in a number (decimal, octal with a leading 0, hex with 0x;
// fewer than four parts fill the last one), and returns the dotted quad.
func whatwgIPv4(host string) (string, bool) {
	parts := strings.Split(host, ".")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 || len(parts) > 4 {
		return "", false
	}
	nums := make([]uint64, len(parts))
	for k, p := range parts {
		base := 10
		switch {
		case strings.HasPrefix(p, "0x"):
			p, base = p[2:], 16
			if p == "" {
				p = "0"
			}
		case len(p) > 1 && p[0] == '0':
			p, base = p[1:], 8
		}
		n, err := strconv.ParseUint(p, base, 64)
		if err != nil {
			return "", false
		}
		nums[k] = n
	}
	var ip uint64
	for k, n := range nums[:len(nums)-1] {
		if n > 255 {
			return "", false
		}
		ip |= n << (8 * (3 - k))
	}
	lastN := nums[len(nums)-1]
	if lastN >= 1<<(8*(5-len(nums))) {
		return "", false
	}
	ip |= lastN
	return fmt.Sprintf("%d.%d.%d.%d", ip>>24, ip>>16&255, ip>>8&255, ip&255), true
}

// neutralizeInsertions checks that out is in with neutralizePrefix inserted
// only directly before a rule prefix or a whole name of linkNames (ASCII
// case-insensitive), and returns
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
		for _, n := range linkNames {
			e := i + len(n.name)
			matches = matches || (hasPrefixASCIIFold(in[i:], n.name) && (e == len(in) || isAttrNameEnd(in[e])))
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
		out := neutralizeHTML(in, testAppHosts)
		// 1. Alignment: only insertions of the prefix, each directly before
		// a rule prefix. This holds for any bytes.
		if _, ok := neutralizeInsertions(in, out); !ok {
			t.Fatalf("not an insertion of %q before a rule prefix:\n in %q\nout %q", neutralizePrefix, in, out)
		}
		// 4. Idempotent.
		if again := neutralizeHTML(out, testAppHosts); again != out {
			t.Fatalf("not idempotent:\n in %q\nout %q\n2nd %q", in, out, again)
		}

		// The x/net/html checks run on what the handler can see: valid
		// UTF-8 without NUL.
		in = cleanLikeMIME(in)
		out = neutralizeHTML(in, testAppHosts)
		at, ok := neutralizeInsertions(in, out)
		if !ok {
			t.Fatalf("not an insertion:\n in %q\nout %q", in, out)
		}
		// 2. Every candidate, tokenized on its own, has no attribute with
		// its rule's prefix.
		if p, key, keeps := neutralizeCandidateKeeps(out); keeps {
			t.Fatalf("candidate at %d keeps %q:\n in %q\nout %q", p, key, in, out)
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

// neutralizeCandidateKeeps tokenizes every candidate in out on its own with
// x/net/html's Tokenizer and reports the first one that keeps an attribute
// with its rule's prefix, or (any tag) a link attribute linkSurvives
// reports: its offset and the attribute.
func neutralizeCandidateKeeps(out string) (at int, key string, keeps bool) {
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
			for _, a := range z.Token().Attr {
				if strings.HasPrefix(a.Key, ru.prefix) {
					return p, a.Key, true
				}
			}
		}
		if p+1 < len(out) && isASCIIAlpha(out[p+1]) {
			z := html.NewTokenizer(strings.NewReader(out[p:]))
			if tt := z.Next(); tt == html.StartTagToken || tt == html.SelfClosingTagToken {
				for _, a := range z.Token().Attr {
					if linkSurvives(a.Key, a.Val) {
						return p, a.Key + "=" + a.Val, true
					}
				}
			}
		}
		n := strings.IndexByte(out[p+1:], '<')
		if n < 0 {
			break
		}
		p += 1 + n
	}
	return 0, "", false
}

// servedCIDParts is a stored message with inline images whose Content-IDs
// end in "=" and whose base64 has no padding (one with "/"), so that
// inlining a reference changes how the bytes after it tokenize. Its parts
// and an opener feed servedMailHTML in FuzzServedMailHTML.
func servedCIDParts(t testing.TB) ([]mime.Part, func() (io.ReadCloser, error)) {
	t.Helper()
	raw := cidMessage("<p>x</p>",
		inlinePart{"image/png", "a@x=", "\x89PNG\r\n\x1a\n\xff"},
		inlinePart{"image/gif", "b@x=", "GIF89a\xff\xff\xff"},
		inlinePart{"image/png", "c@x", "\x89PNG\r\n\x1a\n-c"},
	)
	m := mime.Parse(strings.NewReader(raw))
	if len(m.Parts) != 4 {
		t.Fatalf("parts: %+v", m.Parts)
	}
	return m.Parts, func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(raw)), nil }
}

// FuzzServedMailHTML checks what the handler serves (servedMailHTML: inline
// images, preamble, rename), not neutralizeHTML alone: no rewrite after the
// rename may bring a declarative shadow root, a srcdoc frame or a link into
// the app back.
func FuzzServedMailHTML(f *testing.F) {
	for _, cases := range neutralizeCases {
		for _, c := range cases {
			f.Add(c.in)
		}
	}
	for _, s := range []string{
		`<template cid:q= shadowrootmode=closed>x</template>`,
		`<iframe cid:q= srcdoc=&lt;a&#32;href=/api/x&gt;x&lt;/a&gt;></iframe>`,
		`<template cid:a@x= shadowrootmode=closed>x</template>`,
		`<iframe cid:b@x= srcdoc=x></iframe>`,
		`<template x=cid:c@x shadowrootmode=closed><img src="cid:c@x">`,
		`<template cid:a@x=/shadowrootmode=closed>`,
		`<a cid:q= href=/api/x>x</a>`,
		`<a cid:a@x=/href=/api/x>x</a>`,
		`<svg><set cid:q= attributeName=href to=/x /></svg>`,
	} {
		f.Add(s)
	}
	parts, open := servedCIDParts(f)
	f.Fuzz(func(t *testing.T, in string) {
		in = cleanLikeMIME(in)
		served, inl, err := servedMailHTML(in, parts, open, testAppHosts)
		if err != nil {
			t.Fatal(err)
		}
		// Alignment: the inlined mail with the preamble, plus prefixes only
		// directly before a rule prefix.
		if _, ok := neutralizeInsertions(withPreamble(inl.HTML), served); !ok {
			t.Fatalf("not an insertion:\n in %q\nserved %q", in, served)
		}
		if again := neutralizeHTML(served, testAppHosts); again != served {
			t.Fatalf("served bytes not final:\n in %q\nserved %q", in, served)
		}
		if p, key, keeps := neutralizeCandidateKeeps(served); keeps {
			t.Fatalf("candidate at %d keeps %q:\n in %q\nserved %q", p, key, in, served)
		}
		if s := neutralizeSurvivors(served); len(s) > 0 {
			t.Fatalf("oracle: %v survive:\n in %q\nserved %q", s, in, served)
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
		// Links (#459): every name is an href whose value is read, kept and
		// renamed ones, candidates in values, and long white space runs
		// around "=" that the lookahead crosses.
		"links": fill(`<p><a href="https://links.example/item?id=1">a</a> <a title="<b href='/x' " href=/api/x>b</a>` +
			`<a href` + strings.Repeat(" ", 200) + "=" + strings.Repeat("\n", 200) + `"https://` + strings.Repeat("a", 300) + `"></p>`),
	}
}

func BenchmarkNeutralizeHTML(b *testing.B) {
	for _, name := range []string{"plain", "adversarial", "links"} {
		in := benchInputs()[name]
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			for b.Loop() {
				neutralizeHTML(in, testAppHosts)
			}
		})
	}
}
