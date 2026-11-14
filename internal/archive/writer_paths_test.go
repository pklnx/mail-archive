package archive_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/store"
)

// encryptBlobs makes a writer's blob store encrypt, as OpenAtRest does with
// encryption on.
func encryptBlobs(t *testing.T, blobs *blobstore.Store) {
	t.Helper()
	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := blobcrypt.NewKeyring(kek, "archive key", t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobs.SetKeys(ring, true); err != nil {
		t.Fatal(err)
	}
}

// assertEncrypted checks what a writer path left: only encrypted message
// files, none of them with a plaintext header line in any byte, nothing in
// tmp/, and verify (which decrypts every row's file and hashes it) clean.
func assertEncrypted(t *testing.T, st *store.Store, blobs *blobstore.Store) {
	t.Helper()
	files := 0
	err := blobs.Walk(func(e blobstore.Entry) error {
		switch {
		case e.Kind == blobstore.EntryBlob && e.Encrypted:
			files++
			data, err := os.ReadFile(filepath.Join(blobs.Root(), filepath.FromSlash(e.Path)))
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("Subject: ")) {
				t.Errorf("%s holds plaintext", e.Path)
			}
		default:
			t.Errorf("unexpected entry %+v", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no message files")
	}
	var findings []archive.Finding
	res, err := archive.Verify(context.Background(), st, blobs, archive.VerifyOptions{
		Jobs: 1, Encryption: store.BlobOn, Logger: quiet,
		OnFinding: func(f archive.Finding) { findings = append(findings, f) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked == 0 || res.Problems() != 0 || !res.Complete() {
		t.Fatalf("verify: %+v %+v", res, findings)
	}
}

// Every writer path stores encrypted files when its store encrypts (tests
// per writer path, #392 part 2): sync, import (mbox and EML), journal
// import and fill here, the SMTP receiver in internal/smtpd.
func TestWriterPathSyncEncrypts(t *testing.T) {
	alice := imapmemserver.NewUser("alice", "pw-a")
	createMailboxes(t, alice, "INBOX", "Archive")
	appendMsg(t, alice, "INBOX", rawMessage("a1", "Kontoauszug"))
	appendMsg(t, alice, "INBOX", rawMessage("a2", "Vertrag"))
	appendMsg(t, alice, "Archive", rawMessage("a3", "Archiviert"))
	f := newFixture(t, alice)
	encryptBlobs(t, f.blobs)
	f.addAccount("alice", "alice", "pw-a")
	expectResult(t, f.sync()["alice"], 3, 3)
	assertEncrypted(t, f.store, f.blobs)
	// A second sync deduplicates onto the encrypted files.
	expectResult(t, f.sync()["alice"], 0, 0)
	if n := f.countBlobFiles(); n != 3 {
		t.Fatalf("%d files", n)
	}
}

func TestWriterPathImportEncrypts(t *testing.T) {
	e := newArchiveEnv(t)
	encryptBlobs(t, e.blobs)
	acc := e.importAccount("old")
	res := e.runImport(e.importer(), acc, "mbox", "testdata/import/thunderbird", "")
	if res.Status != "ok" {
		t.Fatalf("mbox import: %+v", res)
	}
	dir := t.TempDir()
	writeEML(t, dir, map[string]string{"a.eml": "Subject: eml one\r\n\r\nbody\r\n", "b.eml": "Subject: eml two\r\nDate: Mon, 5 Oct 2026 10:00:00 +0000\r\n\r\nbody\r\n"},
		time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
	if res := e.runImport(e.importer(), e.importAccount("eml"), "eml", dir, "Saved"); res.Status != "ok" {
		t.Fatalf("eml import: %+v", res)
	}
	assertEncrypted(t, e.st, e.blobs)
}

func TestWriterPathJournalImportEncrypts(t *testing.T) {
	e := newArchiveEnv(t)
	encryptBlobs(t, e.blobs)
	acc := e.smtpAccount("journal")
	e.deliver(acc, stored(journalRaw("m2", "two")))
	dir := t.TempDir()
	writeEML(t, dir, map[string]string{"1.eml": journalRaw("m1", "one"), "2.eml": journalRaw("m2", "two")},
		time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
	res := e.importJournal(acc, e.smtpImport(acc, "piler", "eml"), dir)
	if tot := res.Total(); res.Status != "ok" || tot.Read != 2 || tot.Added != 1 {
		t.Fatalf("%s %+v", res.Status, tot)
	}
	assertEncrypted(t, e.st, e.blobs)
}

func TestWriterPathFillEncrypts(t *testing.T) {
	f := newFixture(t, fillUser(t))
	encryptBlobs(t, f.blobs)
	acc := f.smtpAccount()
	f.journal(acc, "m2")
	fill := f.createFill(acc, "secret", "Spam")
	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil || got.Status != store.FillOK {
		t.Fatalf("fill %+v %v", got, err)
	}
	assertEncrypted(t, f.store, f.blobs)
}
