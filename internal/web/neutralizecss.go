package web

import (
	"strings"

	"golang.org/x/net/html"
)

// Chromium sends the archive's origin with every image a document's CSS
// loads (background-image, list-style-image, border-image, mask-image,
// shape-outside, SVG fill and so on), in Referer and for some in Origin,
// whatever the document's referrer policy: it uses the default policy on
// the document's origin. No header, <meta> or CSP directive changes that,
// and img-src governs <img> and CSS images alike. So a served mail may not
// make such requests: neutralizeCSS makes every URL in its CSS inert. With
// "Load remote images" a mail with inline SVG or MathML gets no remote
// images (foreignContent), and for any other mail cssFetchSurvivors checks
// the served bytes as the browser parses them and refuses remote images if
// anything is left (remoteImagesRefusal).

// neutralizeURLPrefix goes in front of every URL the tokenizer reports
// (cssURLStart): the URL becomes a data: URL that loads nothing, and the
// declaration stays valid, so a shorthand keeps its other values.
const neutralizeURLPrefix = "data:,"

// neutralizeFuncMark goes before the "(" of every function the tokenizer
// reports (cssFuncBreak): an unknown function, so the browser drops that
// declaration (or, in a selector or at-rule prelude, that rule).
const neutralizeFuncMark = "_"

// cssAttrNames are the attributes whose values neutralizeCSS reads as CSS:
// style, and the SVG presentation attributes (SVG 2, section 6.6, and
// Chromium's map of them, with the geometry properties), on any element. An
// attribute missing here is not rewritten, but the parse check reads every
// attribute of an SVG element, so it fails closed.
var cssAttrNames = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(`style
		alignment-baseline baseline-shift buffered-rendering clip clip-path clip-rule color
		color-interpolation color-interpolation-filters color-rendering cursor direction display
		dominant-baseline fill fill-opacity fill-rule filter flood-color flood-opacity font-family
		font-size font-size-adjust font-stretch font-style font-variant font-weight image-rendering
		letter-spacing lighting-color marker marker-end marker-mid marker-start mask mask-type opacity
		overflow paint-order pointer-events shape-rendering stop-color stop-opacity stroke
		stroke-dasharray stroke-dashoffset stroke-linecap stroke-linejoin stroke-miterlimit
		stroke-opacity stroke-width text-anchor text-decoration text-overflow text-rendering
		transform transform-origin unicode-bidi vector-effect visibility white-space word-spacing
		writing-mode gradienttransform patterntransform x y width height cx cy r rx ry d`) {
		m[n] = true
	}
	return m
}()

// neutralizeCSS returns doc with neutralizeURLPrefix inserted before every
// URL start and neutralizeFuncMark before every function break the CSS
// tokenizer reports (cssurl.go), in the values of cssAttrNames on every
// start tag and in the raw text of every <style>. An insertion right after
// a character reference without ";" (as in url(&quot/x&quot)) starts with
// ";", which ends the reference as it was, so it decodes as before. Nothing
// else changes. A doc without such a URL or function is returned as is,
// without a copy.
//
// It walks doc with x/net/html's Tokenizer, which models no tree
// construction: after <noscript> it reads markup, as the sandboxed frame
// does with scripting off; inside SVG and MathML it still reads <style> as
// raw text, where the browser reads markup. There, and after a character
// reference that an insertion would change, the browser's CSS can differ
// from what the rewrite saw. The rewrite only keeps legitimate mail looking
// as it should; the guarantee is the refusal of remote images
// (remoteImagesRefusal: foreignContent, then cssFetchSurvivors).
//
// The insertions hold no white space, "/", "<", ">", "=", quote or "&", and
// every inserted byte is valid in a tag name, an attribute name, an
// attribute value of any kind and in text. So wherever one lands, in the
// browser's view too, it only lengthens the token it lands in: it never
// ends a token, starts one or changes its type. No name that neutralizeHTML
// renames, and no element name with meaning, contains "_", ";" or "data:,",
// so neutralizeHTML stays a fixpoint on the result (FuzzServedMailHTML).
func neutralizeCSS(doc string) string {
	var ins []cssInsertion
	z := html.NewTokenizer(strings.NewReader(doc))
	off := 0
	style := false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		start := off
		off += len(z.Raw())
		wasStyle := style
		style = false
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "style":
				style = true
			case "noscript":
				z.NextIsNotRawText()
			}
			forEachAttrValue(doc[start:off], func(name string, vs, ve int) {
				if cssAttrNames[name] {
					ins = cssInsertions(ins, doc[start+vs:start+ve], true, start+vs)
				}
			})
		case html.TextToken:
			if wasStyle {
				ins = cssInsertions(ins, doc[start:off], false, start)
			}
		}
	}
	if len(ins) == 0 {
		return doc
	}
	var b strings.Builder
	b.Grow(len(doc) + len(ins)*len(";"+neutralizeURLPrefix))
	last := 0
	for _, in := range ins {
		b.WriteString(doc[last:in.at])
		b.WriteString(in.text)
		last = in.at
	}
	b.WriteString(doc[last:])
	return b.String()
}

// cssInsertion is one insertion of neutralizeCSS: text goes before doc[at].
type cssInsertion struct {
	at   int
	text string
}

// cssInsertions appends the insertions for the CSS in s, which starts at
// offset base of doc.
func cssInsertions(ins []cssInsertion, s string, refs bool, base int) []cssInsertion {
	scanCSS(s, refs, false, func(r cssReport) bool {
		text := neutralizeFuncMark
		if r.kind == cssURLStart {
			text = neutralizeURLPrefix
			if r.at.bare {
				text = ";" + text
			}
		}
		ins = append(ins, cssInsertion{at: base + r.at.off, text: text})
		return true
	})
	return ins
}

// forEachAttrValue calls fn with the lowercase name and the value's span
// (relative to tag) of every attribute that has a value, in the raw bytes
// of a start tag as x/net/html's Tokenizer returns it ("<name ...>"). It
// follows the HTML spec's tag name state and attribute states, which do not
// depend on context within a tag; character references are left to the
// caller.
func forEachAttrValue(tag string, fn func(name string, vs, ve int)) {
	i := 1
	for i < len(tag) && !isTagNameEnd(tag[i]) {
		i++
	}
	st := stBeforeName
	nameStart, nameEnd := -1, -1
	for ; i < len(tag); i++ {
		c := tag[i]
		cls := attrClassOf(c)
		switch st {
		case stBeforeName:
			switch cls {
			case clsSpace:
			case clsSlash:
				st = stSelfClosing
			case clsGT:
				return
			default:
				st, nameStart = stName, i
			}
		case stName:
			switch cls {
			case clsSpace:
				st, nameEnd = stAfterName, i
			case clsSlash:
				st, nameEnd = stSelfClosing, i
			case clsGT:
				return
			case clsEq:
				st, nameEnd = stBeforeValue, i
			}
		case stAfterName:
			switch cls {
			case clsSpace:
			case clsSlash:
				st = stSelfClosing
			case clsEq:
				st = stBeforeValue
			case clsGT:
				return
			default:
				st, nameStart = stName, i
			}
		case stBeforeValue:
			switch cls {
			case clsSpace:
			case clsDQuote, clsSQuote:
				q := c
				e := strings.IndexByte(tag[i+1:], q)
				if e < 0 {
					return // cut off: the tag does not end
				}
				fn(strings.ToLower(tag[nameStart:nameEnd]), i+1, i+1+e)
				i += 1 + e
				st = stAfterQuoted
			case clsGT:
				return
			default:
				e := i
				for e < len(tag) && attrClassOf(tag[e]) != clsSpace && tag[e] != '>' {
					e++
				}
				fn(strings.ToLower(tag[nameStart:nameEnd]), i, e)
				i = e - 1
				st = stBeforeName
			}
		case stAfterQuoted, stSelfClosing:
			switch cls {
			case clsSpace:
				st = stBeforeName
			case clsSlash:
				st = stSelfClosing
			case clsGT:
				return
			default:
				st, nameStart = stName, i
			}
		}
	}
}

// cssFetchSurvivorsMax bounds what cssFetchSurvivors returns: one is enough
// to refuse.
const cssFetchSurvivorsMax = 8

// cssFetchSurvivors parses doc as x/net/html does with scripting off (as
// in the sandboxed frame) and returns where its CSS could still load
// something: a URL start or a function break of the tokenizer in
//
//   - the style attribute of every element, in every namespace;
//   - every attribute of an SVG element, except URL attributes (href,
//     xlink:href, src, srcset), id, class, aria-* and data-*, and names with
//     a namespace or ":" (xml:space, inkscape:label): Chromium reads CSS
//     only from presentation attributes, which have plain names;
//   - the text of every element named style, in every namespace: its direct
//     text children together, as the browser takes it.
//
// This is a superset of the CSS the browser applies. It holds as long as
// x/net/html builds the tree the browser builds (it does not always in SVG
// and MathML, which is why foreignContent refuses those mails first), the tokenizer reads CSS as
// the browser does, and cssSafeFunctions holds only functions that load
// nothing. Template contents are read too. A doc that x/net/html cannot
// parse counts as one survivor.
func cssFetchSurvivors(doc string) []string {
	root, err := html.ParseWithOptions(strings.NewReader(doc), html.ParseOptionEnableScripting(false))
	if err != nil {
		return []string{"parse error: " + err.Error()}
	}
	var out []string
	check := func(where, css string) {
		scanCSS(css, false, true, func(r cssReport) bool {
			what := "url(" + r.name
			if r.kind == cssFuncBreak {
				what = r.name + "("
			}
			out = append(out, where+": "+what)
			return len(out) < cssFetchSurvivorsMax
		})
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= cssFetchSurvivorsMax {
			return
		}
		if n.Type == html.ElementNode {
			el := n.Data
			if n.Namespace != "" {
				el = n.Namespace + " " + el
			}
			for _, a := range n.Attr {
				key := strings.ToLower(a.Key)
				switch {
				case a.Namespace == "" && key == "style":
					check("<"+el+" style>", a.Val)
				case n.Namespace == "svg" && svgAttrChecked(a.Namespace, key):
					check("<"+el+" "+key+">", a.Val)
				}
			}
			if n.Data == "style" {
				var b strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						b.WriteString(c.Data)
					}
				}
				check("<"+el+">", b.String())
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// foreignContent reports whether doc holds the bytes "<svg" or "<math",
// ASCII case-insensitive, anywhere: in markup, text, comments or attribute
// values alike. It returns "svg" or "math" for the first one, else "".
//
// Inside inline SVG and MathML the browser builds <style> from its text
// children, so there the rewrite cannot see the CSS the browser applies,
// and cssFetchSurvivors stands alone on x/net/html's tree. x/net/html
// builds a different tree than the browser in foreign content (a <template>
// in an integration point ends the document; a duplicate attribute with the
// unquoted value "/" makes a tag self-closing). So a mail with inline SVG
// or MathML gets no remote images at all (handleMessageHTML). The plain
// byte scan reads no tree, so no parser divergence can hide such an
// element: every SVG or MathML element in the tree starts with a tag "<svg"
// or "<math" (the HTML parser lowercases ASCII tag names only, and a
// character reference never makes a tag). It may also match where no
// element is made (a comment, a text "<svg"); that only fails closed.
func foreignContent(doc string) string {
	for i := 0; i < len(doc); i++ {
		j := strings.IndexByte(doc[i:], '<')
		if j < 0 {
			return ""
		}
		i += j
		rest := doc[i+1:]
		if asciiPrefixFold(rest, "svg") {
			return "svg"
		}
		if asciiPrefixFold(rest, "math") {
			return "math"
		}
	}
	return ""
}

// asciiPrefixFold reports whether s starts with the lowercase ASCII word w,
// ASCII case-insensitive, as the HTML tokenizer compares tag names.
func asciiPrefixFold(s, w string) bool {
	if len(s) < len(w) {
		return false
	}
	for i := 0; i < len(w); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != w[i] {
			return false
		}
	}
	return true
}

// remoteImagesRefusal returns why handleMessageHTML refuses remote images
// for the served bytes doc, or "" if it allows them: "svg" or "math" for
// inline SVG or MathML (foreignContent), else "css" when cssFetchSurvivors
// finds CSS that could still load something.
func remoteImagesRefusal(doc string) string {
	if f := foreignContent(doc); f != "" {
		return f
	}
	if len(cssFetchSurvivors(doc)) > 0 {
		return "css"
	}
	return ""
}

// svgAttrChecked reports whether cssFetchSurvivors reads an SVG element's
// attribute (namespace, lowercase key) as CSS.
func svgAttrChecked(ns, key string) bool {
	if ns != "" || strings.Contains(key, ":") {
		return false
	}
	switch key {
	case "href", "src", "srcset", "id", "class":
		return false
	}
	return !strings.HasPrefix(key, "aria-") && !strings.HasPrefix(key, "data-")
}
