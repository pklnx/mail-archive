package web

import (
	"strings"

	"golang.org/x/net/html"
)

// htmlPreamble goes into every HTML mail, after its doctype (#206). Links
// open in a new tab instead of inside the viewer frame. The style keeps
// height:100% chains on html and body from tying the content to the frame's
// height, so the app can size the frame to the content.
const htmlPreamble = `<base target="_blank"><style>html,body{height:auto!important;min-height:0!important}</style>` + "\n"

const utf8BOM = "\xef\xbb\xbf"

// withPreamble inserts htmlPreamble into the mail so that the mail keeps the
// rendering mode its own doctype asks for.
//
// The HTML parser's "initial" insertion mode skips only a BOM, ASCII
// whitespace and comments (bogus comments such as <?xml ...?> included)
// before a doctype. Any other token, a <base> among them, puts the document
// in quirks mode, and a doctype after it is ignored. So the preamble goes
// right after a doctype that follows nothing but those. Otherwise it goes in
// front of the mail: the browser renders such a mail in quirks mode anyway,
// and 0 keeps exactly that. Only a doctype that ends in ">" counts: a
// doctype cut off by the end of the mail would swallow the preamble.
// Only the prefix is tokenized, never the whole mail.
func withPreamble(doc string) string {
	n := preambleOffset(doc)
	return doc[:n] + htmlPreamble + doc[n:]
}

func preambleOffset(doc string) int {
	off := 0
	if strings.HasPrefix(doc, utf8BOM) {
		off = len(utf8BOM)
	}
	z := html.NewTokenizer(strings.NewReader(doc[off:]))
	for {
		tt := z.Next()
		raw := z.Raw()
		switch tt {
		case html.CommentToken:
			off += len(raw)
		case html.TextToken:
			if strings.Trim(string(raw), "\t\n\f\r ") != "" {
				return 0
			}
			off += len(raw)
		case html.DoctypeToken:
			if len(raw) > 0 && raw[len(raw)-1] == '>' {
				return off + len(raw)
			}
			return 0
		default:
			return 0
		}
	}
}
