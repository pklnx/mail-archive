package main

import (
	"testing"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
)

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"INBOX":            "INBOX",
		"me@mail.de":       "me@mail.de",
		"[Gmail]/All Mail": "'[Gmail]/All Mail'",
		"Bob's":            `'Bob'\''s'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExclusionHint(t *testing.T) {
	t.Setenv("MAIL_ARCHIVE_COMMAND", "./ma")
	folders := []imapsync.Folder{
		{Name: "INBOX"},
		{Name: "Spam", Attrs: []string{`\Junk`}},
		{Name: "Gelöschte Elemente", Attrs: []string{`\Trash`}},
	}
	acc := &store.Account{Name: "private", ExcludedFolders: []string{"Archiv"}}
	got := exclusionHint(acc, folders)
	want := "\nhint: Spam, Gelöschte Elemente marked as junk/trash by the server and will be archived.\n" +
		"      To skip them: ./ma account set-folders private --exclude Archiv --exclude Spam --exclude 'Gelöschte Elemente'\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	acc.ExcludedFolders = []string{"Spam", "Gelöschte Elemente"}
	if got := exclusionHint(acc, folders); got != "" {
		t.Errorf("expected no hint when already excluded, got %q", got)
	}
}
