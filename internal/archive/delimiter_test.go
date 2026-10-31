package archive_test

import (
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// delimiters returns the stored delimiter per folder of the account
// ("<nil>" while unknown).
func (f *fixture) delimiters(account string) map[string]string {
	f.t.Helper()
	owner, err := f.store.GetUserByName(f.ctx, "owner")
	if err != nil {
		owner, err = f.store.CreateUser(f.ctx, "owner", "x", false) // the first user gets the accounts
	}
	if err != nil {
		f.t.Fatal(err)
	}
	list, err := f.store.ListAccountFolders(f.ctx, storetest.Viewer(f.t, f.store, owner.ID))
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]string{}
	for _, a := range list {
		if a.Name != account {
			continue
		}
		for _, fc := range a.Folders {
			out[fc.Name] = "<nil>"
			if fc.Delimiter != nil {
				out[fc.Name] = *fc.Delimiter
			}
		}
	}
	return out
}

func TestSyncRecordsFolderDelimiters(t *testing.T) {
	alice := imapmemserver.NewUser("alice", "pw")
	createMailboxes(t, alice, "INBOX", "Archive", "Archive/2024", "Spam")
	appendMsg(t, alice, "Archive/2024", rawMessage("d1", "Old"))
	f := newFixture(t, alice)
	f.addAccount("alice", "alice", "pw", "Spam")
	acc, err := f.account("alice")
	if err != nil {
		t.Fatal(err)
	}
	// Rows from before the upgrade: Spam was archived before it was
	// excluded, Gone has vanished from the server.
	for _, name := range []string{"Spam", "Gone"} {
		if _, err := f.store.GetOrCreateFolder(f.ctx, acc.ID, name); err != nil {
			t.Fatal(err)
		}
	}
	expectResult(t, f.sync()["alice"], 1, 1)
	got := f.delimiters("alice")
	want := map[string]string{"INBOX": "/", "Archive": "/", "Archive/2024": "/", "Spam": "/", "Gone": "<nil>"}
	for name, d := range want {
		if got[name] != d {
			t.Errorf("%s: delimiter %q, want %q (all: %v)", name, got[name], d, got)
		}
	}
	// Unchanged: the next sync writes nothing.
	n, err := f.store.SetFolderDelimiters(f.ctx, acc.ID, map[string]string{"INBOX": "/", "Archive": "/"})
	if err != nil || n != 0 {
		t.Errorf("same delimiters: changed %d, %v", n, err)
	}
	if n, err := f.store.SetFolderDelimiters(f.ctx, acc.ID, map[string]string{"INBOX": ""}); err != nil || n != 1 {
		t.Errorf("NIL delimiter: changed %d, %v", n, err)
	}
	if d := f.delimiters("alice")["INBOX"]; d != "" {
		t.Errorf("INBOX: delimiter %q after NIL", d)
	}
}

func TestSyncRecordsDotDelimiter(t *testing.T) {
	bob := imapmemserver.NewUser("bob", "pw")
	createMailboxes(t, bob, "INBOX", "INBOX.Sub")
	appendMsg(t, bob, "INBOX.Sub", rawMessage("d2", "Dot"))
	f := newFixture(t, bob)
	f.host, f.port = imaptest.StartWithDelimiter(t, '.', map[string]imap.MailboxAttr{"INBOX": "", "INBOX.Sub": ""}, bob)
	f.addAccount("bob", "bob", "pw")
	expectResult(t, f.sync()["bob"], 1, 1)
	if got := f.delimiters("bob"); got["INBOX"] != "." || got["INBOX.Sub"] != "." {
		t.Errorf("delimiters %v, want . for both", got)
	}
}
