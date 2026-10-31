package attachtext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlainText(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		charset string
		html    bool
		want    string
		status  Status
	}{
		{"utf-8", "Grüße", "", false, "Grüße", StatusOK},
		// go-message decodes a declared charset before; the bytes are UTF-8 then.
		{"declared charset", "Größe;Menge", "iso-8859-1", false, "Größe;Menge", StatusOK},
		{"no charset, not UTF-8", "Gr\xf6\xdfe;\x80", "", false, "Größe;€", StatusOK},
		{"declared UTF-8 but invalid", "a\xffb", "utf-8", false, "a�b", StatusOK},
		{"NUL bytes", "a\x00b", "", false, "ab", StatusOK},
		{"empty", " \n\t", "", false, "", StatusNoText},
		{"html", "<html><head><title>t</title><style>p{}</style></head><body><p>Rechnung</p><script>evil()</script><p>Betrag</p></body></html>", "", true, "Rechnung\n\nBetrag", StatusOK},
	}
	for _, c := range cases {
		got, st := plainText([]byte(c.data), c.charset, c.html, MaxText)
		if got != c.want || st != c.status {
			t.Errorf("%s: %q %s, want %q %s", c.name, got, st, c.want, c.status)
		}
	}
	// "a" first, so that MaxText falls inside a two-byte "ü".
	long := "a" + strings.Repeat("ü", MaxText)
	got, st := plainText([]byte(long), "", false, MaxText)
	if st != StatusTruncated || len(got) != MaxText-1 || !utf8.ValidString(got) {
		t.Errorf("long text: %d bytes, %s, valid %v", len(got), st, utf8.ValidString(got))
	}
}
