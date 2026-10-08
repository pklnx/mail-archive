package archive_test

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

func hashOf(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// An original in INBOX, the reply in Sent and an answer imported from a
// Maildir end up in one conversation.
func TestSyncAndImportLinkConversation(t *testing.T) {
	original := "Message-ID: <offer@kitchen.example>\r\nFrom: info@kitchen.example\r\nSubject: Offer\r\n" +
		"Date: Mon, 5 Oct 2026 10:00:00 +0000\r\n\r\nOur offer.\r\n"
	reply := "Message-ID: <reply@example.com>\r\nFrom: alice@example.com\r\nSubject: Re: Offer\r\n" +
		"In-Reply-To: <offer@kitchen.example>\r\nReferences: <offer@kitchen.example>\r\n" +
		"Date: Tue, 6 Oct 2026 10:00:00 +0000\r\n\r\nAccepted.\r\n"
	answer := "Message-ID: <answer@kitchen.example>\nFrom: info@kitchen.example\nSubject: Re: Offer\n" +
		"In-Reply-To: <reply@example.com>\nReferences: <offer@kitchen.example>\n <reply@example.com>\n" +
		"Date: Wed, 7 Oct 2026 10:00:00 +0000\n\nThanks.\n"

	alice := imapmemserver.NewUser("alice", "pw-a")
	createMailboxes(t, alice, "INBOX", "Sent")
	appendMsg(t, alice, "INBOX", []byte(original))
	appendMsg(t, alice, "Sent", []byte(reply))
	f := newFixture(t, alice)
	f.addAccount("alice", "alice", "pw-a")
	user, err := f.store.CreateUser(f.ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := f.account("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetAccountOwner(f.ctx, acc.Ref(), user.ID); err != nil {
		t.Fatal(err)
	}
	expectResult(t, f.sync()["alice"], 2, 2)

	dir := filepath.Join(t.TempDir(), "Maildir")
	writeMaildir(t, dir, map[string]string{"cur/1700000000.1.host:2,S": answer}, time.Now())
	imported := &store.Account{Kind: store.KindImport, Name: "old", OwnerID: &user.ID}
	if err := f.store.CreateAccount(f.ctx, imported); err != nil {
		t.Fatal(err)
	}
	folders, _, err := archive.ImportFolders("maildir", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	im := &archive.Importer{Store: f.store, Blobs: f.blobs, Logger: quiet}
	if res, err := im.Import(f.ctx, imported, folders); err != nil || res.Status != "ok" {
		t.Fatalf("import: %v %+v", err, res)
	}

	rows, err := f.store.SearchThreads(f.ctx, store.SearchFilter{Owner: user.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Count != 3 || rows[0].Thread != "offer@kitchen.example" {
		t.Fatalf("threads: %+v", rows)
	}
	c, err := f.store.GetConversation(f.ctx, user.ID, hashOf(reply))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range c.Messages {
		got = append(got, m.Subject+"/"+m.Relation)
	}
	if strings.Join(got, ", ") != "Offer/parent, Re: Offer/self, Re: Offer/reply" {
		t.Errorf("conversation: %v", got)
	}
}
