package mime

import (
	"strings"

	"golang.org/x/net/html"
)

// HTMLToText converts an HTML document to readable plain text. Scripts,
// styles and the head are dropped; block elements become line breaks.
func HTMLToText(src string) string {
	z := html.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	skip := 0 // depth inside elements whose text is not content
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return collapse(b.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			if skipped[string(name)] && tt == html.StartTagToken {
				skip++
			}
			if blocks[string(name)] {
				b.WriteByte('\n')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if skipped[string(name)] && skip > 0 {
				skip--
			}
			if blocks[string(name)] {
				b.WriteByte('\n')
			}
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		}
	}
}

var skipped = map[string]bool{"script": true, "style": true, "head": true, "title": true, "noscript": true, "template": true}

var blocks = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "table": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "blockquote": true, "pre": true, "hr": true,
	"section": true, "article": true, "header": true, "footer": true, "ul": true, "ol": true,
}

// collapse trims each line, squeezes runs of spaces and keeps at most one
// empty line between paragraphs.
func collapse(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, " ", " "), "\n")
	out := make([]string, 0, len(lines))
	blank := true
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, l)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
