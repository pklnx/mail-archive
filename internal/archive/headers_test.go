package archive

import (
	"strings"
	"testing"
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
	if h != (Headers{}) {
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
