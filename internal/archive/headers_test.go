package archive

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/imapsync"
)

func TestParseHeaders(t *testing.T) {
	raw := "Message-ID: <abc@example.com>\r\n" +
		"From: =?ISO-8859-1?Q?J=FCrgen_M=FCller?= <j@example.com>\r\n" +
		"Subject: =?UTF-8?B?R3LDvMOfZSBhdXMgS8O2bG4=?=\r\n" +
		"Date: Mon, 5 Oct 2026 10:00:00 +0200\r\n" +
		"\r\n" +
		"body\r\n"
	h := ParseHeaders(strings.NewReader(raw))
	if h.MessageID != "abc@example.com" {
		t.Errorf("MessageID = %q", h.MessageID)
	}
	if h.From != "Jürgen Müller <j@example.com>" {
		t.Errorf("From = %q", h.From)
	}
	if h.Subject != "Grüße aus Köln" {
		t.Errorf("Subject = %q", h.Subject)
	}
	if h.Date == nil || h.Date.UTC().Hour() != 8 {
		t.Errorf("Date = %v", h.Date)
	}
}

func TestParseHeadersLenient(t *testing.T) {
	h := ParseHeaders(strings.NewReader("garbage without headers"))
	if !reflect.DeepEqual(h, Headers{}) {
		t.Errorf("expected empty headers, got %+v", h)
	}

	raw := "Subject: bad \xff\x00 bytes\r\nDate: not a date\r\n\r\n"
	h = ParseHeaders(strings.NewReader(raw))
	if h.Subject != "bad � bytes" {
		t.Errorf("Subject = %q", h.Subject)
	}
	if h.Date != nil {
		t.Errorf("Date should be nil, got %v", h.Date)
	}
}

func TestFolderSelected(t *testing.T) {
	cases := []struct {
		name               string
		included, excluded []string
		want               bool
	}{
		{"INBOX", nil, nil, true},
		{"Trash", nil, []string{"trash"}, false},
		{"[Gmail]/All Mail", []string{"[Gmail]/All Mail"}, nil, true},
		{"Work", []string{"[Gmail]/All Mail"}, nil, false},
		{"inbox", []string{"INBOX"}, []string{"INBOX"}, false},
	}
	for _, c := range cases {
		if got := FolderSelected(c.name, c.included, c.excluded); got != c.want {
			t.Errorf("FolderSelected(%q, %v, %v) = %v, want %v", c.name, c.included, c.excluded, got, c.want)
		}
	}
}

func TestSuggestExclusions(t *testing.T) {
	folders := []imapsync.Folder{
		{Name: "INBOX"},
		{Name: "Junk", Attrs: []string{`\Junk`}},
		{Name: "Papierkorb", Attrs: []string{`\HasNoChildren`, `\Trash`}},
		{Name: "Sent", Attrs: []string{`\Sent`}},
	}
	if got := SuggestExclusions(folders, nil, nil); !slices.Equal(got, []string{"Junk", "Papierkorb"}) {
		t.Errorf("no filters: got %v", got)
	}
	if got := SuggestExclusions(folders, nil, []string{"junk"}); !slices.Equal(got, []string{"Papierkorb"}) {
		t.Errorf("junk excluded: got %v", got)
	}
	if got := SuggestExclusions(folders, []string{"INBOX"}, nil); len(got) != 0 {
		t.Errorf("include list without junk/trash: got %v", got)
	}
}

func TestParseHeadersRecipients(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: =?UTF-8?Q?Finanzamt_K=C3=B6ln?= <poststelle@fa-koeln.example>,\r\n" +
		"  \"Steuer, Berater\" <berater@example.com>\r\n" +
		"Cc: Team: x@example.com, y@example.com;\r\n" +
		"Subject: s\r\n" +
		"\r\n"
	h := ParseHeaders(strings.NewReader(raw))
	if h.To != `Finanzamt Köln <poststelle@fa-koeln.example>, "Steuer, Berater" <berater@example.com>` {
		t.Errorf("To = %q", h.To)
	}
	if h.Cc != "Team: x@example.com, y@example.com;" {
		t.Errorf("Cc = %q", h.Cc)
	}

	h = ParseHeaders(strings.NewReader("To: undisclosed-recipients:;\r\n\r\n"))
	if h.To != "undisclosed-recipients:;" || h.Cc != "" {
		t.Errorf("To = %q, Cc = %q", h.To, h.Cc)
	}
}

func TestParseHeadersRecipientCap(t *testing.T) {
	var list []string
	for range 1000 {
		list = append(list, "empfaenger@example.com")
	}
	long := strings.Join(list, ", ")
	h := ParseHeaders(strings.NewReader("Subject: " + long + "\r\nTo: " + long + "\r\n\r\n"))
	if len(h.To) != maxRecipientField || !strings.HasPrefix(h.To, "empfaenger@example.com, ") {
		t.Errorf("To has %d bytes", len(h.To))
	}
	if len(h.Subject) != maxHeaderField {
		t.Errorf("Subject has %d bytes", len(h.Subject))
	}
}
