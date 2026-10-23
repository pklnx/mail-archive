package web

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const (
	xhtmlTrans = `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">`
	html401T   = `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01 Transitional//EN" "http://www.w3.org/TR/html4/loose.dtd">`
	html401TN  = `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01 Transitional//EN">`
	html401S   = `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01//EN" "http://www.w3.org/TR/html4/strict.dtd">`
	html32     = `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 3.2 Final//EN">`
)

// preambleCases: input, and the prefix of it that precedes the preamble.
var preambleCases = []struct{ name, in, prefix string }{
	{"no doctype", `<html><body>x`, ``},
	{"doctype", `<!DOCTYPE html><html><body>x`, `<!DOCTYPE html>`},
	{"lower case", `<!doctype html><p>x`, `<!doctype html>`},
	{"mixed case", `<!DocTyPe HTML><p>x`, `<!DocTyPe HTML>`},
	{"no space", `<!DOCTYPEhtml><p>x`, `<!DOCTYPEhtml>`},
	{"whitespace", "  \r\n\t\f<!DOCTYPE html><p>x", "  \r\n\t\f<!DOCTYPE html>"},
	{"bom", "\xef\xbb\xbf<!DOCTYPE html><p>x", "\xef\xbb\xbf<!DOCTYPE html>"},
	{"bom whitespace", "\xef\xbb\xbf \n<!DOCTYPE html><p>x", "\xef\xbb\xbf \n<!DOCTYPE html>"},
	{"bom text", "\xef\xbb\xbfhello", ``},
	{"bom alone", "\xef\xbb\xbf", ``},
	{"comment", `<!-- a --><!DOCTYPE html><p>x`, `<!-- a --><!DOCTYPE html>`},
	{"empty comment", `<!--><!DOCTYPE html><p>x`, `<!--><!DOCTYPE html>`},
	{"comments", `<!-- a --!><!-- b --><!DOCTYPE html><p>x`, `<!-- a --!><!-- b --><!DOCTYPE html>`},
	{"bang comment end", `<!--a--!><!DOCTYPE html><p>x`, `<!--a--!><!DOCTYPE html>`},
	{"bogus empty end tag", `</><!DOCTYPE html><p>x`, `</><!DOCTYPE html>`},
	{"xml declaration", "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" + xhtmlTrans + `<html>`, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" + xhtmlTrans},
	{"html 4.01 transitional", html401T + `<p>x`, html401T},
	{"html 4.01 transitional without system id", html401TN + `<p>x`, html401TN},
	{"html 4.01 strict", html401S + `<p>x`, html401S},
	{"html 3.2", html32 + `<p>x`, html32},
	{"text before", `hello<!DOCTYPE html>`, ``},
	{"element before", `<p><!DOCTYPE html>`, ``},
	{"end tag before", `</p><!DOCTYPE html>`, ``},
	{"nbsp before", " <!DOCTYPE html>", ``},
	{"space after bang", `<! DOCTYPE html><p>x`, ``},
	{"unterminated doctype", `<!DOCTYPE html`, ``},
	{"doctype in unterminated comment", `<!-- unterminated <!DOCTYPE html>`, ``},
	{"system id with >", `<!DOCTYPE html SYSTEM "a>b">x`, `<!DOCTYPE html SYSTEM "a>`},
	{"empty", ``, ``},
	{"doctype alone", `<!DOCTYPE html>`, `<!DOCTYPE html>`},
}

func TestWithPreamble(t *testing.T) {
	for _, c := range preambleCases {
		t.Run(c.name, func(t *testing.T) {
			want := c.prefix + htmlPreamble + strings.TrimPrefix(c.in, c.prefix)
			if got := withPreamble(c.in); got != want {
				t.Errorf("withPreamble(%q)\n got %q\nwant %q", c.in, got, want)
			}
		})
	}
}

func FuzzWithPreamble(f *testing.F) {
	for _, c := range preambleCases {
		f.Add(c.in)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := withPreamble(in)
		prefix, rest, ok := strings.Cut(out, htmlPreamble)
		if !ok {
			t.Fatalf("no preamble in %q", out)
		}
		// The first preamble is ours: if the input held one, ours is not
		// after it, since the prefix is at most BOM, whitespace, comments
		// and a doctype.
		if prefix+rest != in {
			t.Fatalf("removing the preamble gives %q, input %q", prefix+rest, in)
		}
		if prefix != "" && !strings.HasSuffix(prefix, ">") && prefix != utf8BOM {
			t.Fatalf("prefix %q does not end in >", prefix)
		}
		// Nothing that renders can come before our <base>: no start tag.
		z := html.NewTokenizer(strings.NewReader(strings.TrimPrefix(prefix, utf8BOM)))
		for {
			tt := z.Next()
			if tt == html.ErrorToken {
				break
			}
			if tt == html.StartTagToken || tt == html.SelfClosingTagToken || tt == html.EndTagToken {
				t.Fatalf("tag token %q before the preamble in %q", z.Raw(), prefix)
			}
		}
	})
}
