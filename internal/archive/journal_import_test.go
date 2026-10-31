package archive_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// journalRaw is a message as the source holds it, LF only, with an
// optional Message-ID.
func journalRaw(id, subject string) string {
	h := ""
	if id != "" {
		h = "Message-ID: <" + id + "@example.com>\n"
	}
	return h + "From: a@example.com\nSubject: " + subject + "\nDate: Tue, 3 Mar 2020 10:00:00 +0000\n\nBody of " + subject + "\n"
}

// stored is raw as the import stores it: CRLF.
func stored(raw string) []byte { return []byte(strings.ReplaceAll(raw, "\n", "\r\n")) }

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (e *archiveEnv) smtpAccount(name string) *store.Account {
	e.t.Helper()
	acc := &store.Account{Name: name, Enabled: true, OwnerID: &e.user.ID}
	if err := e.st.CreateSMTPAccount(e.ctx, acc, []string{name + "@archive.test"}); err != nil {
		e.t.Fatal(err)
	}
	return acc
}

// deliver stores raw in the smtp account as received over SMTP, with an
// envelope, and returns its hash.
func (e *archiveEnv) deliver(acc *store.Account, raw []byte) string {
	e.t.Helper()
	blob, created, err := e.blobs.Put(bytes.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	meta, err := archive.BuildMeta(e.ctx, e.st, e.blobs, blob, created)
	if err != nil {
		e.t.Fatal(err)
	}
	env := store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"alice@example.com"}, ReceivedAt: time.Now(), HELO: "mx"}
	if _, err := e.st.ReceiveMessage(e.ctx, acc.ID, meta, env, func() bool { return true }); err != nil {
		e.t.Fatal(err)
	}
	return blob.SHA256
}

func (e *archiveEnv) smtpImport(acc *store.Account, label, format string) *store.SMTPImport {
	e.t.Helper()
	imp := &store.SMTPImport{AccountID: acc.ID, Label: label, Format: format, Source: "/import/" + label}
	if err := e.st.CreateSMTPImport(e.ctx, imp); err != nil {
		e.t.Fatal(err)
	}
	return imp
}

func (e *archiveEnv) importJournal(acc *store.Account, imp *store.SMTPImport, path string) *archive.ImportResult {
	e.t.Helper()
	folders, _, err := archive.JournalImportFolders(imp.Format, path)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := e.importer().ImportJournal(e.ctx, acc, imp, folders)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// origin returns the locations of a message in the account: whether it has
// an envelope, and the label of its import.
func (e *archiveEnv) origin(acc *store.Account, sha string) []string {
	e.t.Helper()
	d, err := e.st.GetMessageDetail(e.ctx, storetest.Viewer(e.t, e.st, *acc.OwnerID), sha)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, l := range d.Locations {
		if l.Account != acc.Name {
			continue
		}
		if l.Folder != store.SMTPFolder {
			e.t.Errorf("location in %s", l.Folder)
		}
		switch {
		case l.Envelope != nil:
			out = append(out, "journaled")
		case l.Import != nil && l.Filled && l.Fill == nil:
			out = append(out, "import:"+l.Import.Label)
		default:
			out = append(out, fmt.Sprintf("other %+v", l))
		}
	}
	return out
}

func (e *archiveEnv) journalLocations(acc *store.Account) int {
	e.t.Helper()
	n := 0
	for _, p := range e.placesOf(acc) {
		if p.folder != store.SMTPFolder {
			e.t.Errorf("place %+v", p)
		}
		n++
	}
	return n
}

func TestImportJournalEML(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	identical := journalRaw("same", "same bytes")
	// Journaled: m2 with other bytes (a Received: line), a message without
	// Message-ID, and one with exactly the bytes of the file.
	e.deliver(acc, append([]byte("Received: from mx by archive\r\n"), stored(journalRaw("m2", "two"))...))
	e.deliver(acc, stored(journalRaw("", "journaled without id")))
	e.deliver(acc, stored(identical))

	dir := t.TempDir()
	mtime := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	writeEML(t, dir, map[string]string{
		"1.eml": journalRaw("m1", "one"),
		"2.eml": journalRaw("m2", "two"),
		"3.eml": journalRaw("", "no id"),
		"4.eml": identical,
	}, mtime)
	imp := e.smtpImport(acc, "piler", "eml")
	res := e.importJournal(acc, imp, dir)
	if tot := res.Total(); res.Status != "ok" || tot.Read != 4 || tot.Added != 2 || tot.New != 2 || tot.Present != 1 || tot.Journaled != 1 || tot.Skipped != 0 {
		t.Fatalf("%s %+v", res.Status, tot)
	}
	if len(res.Folders) != 1 || res.Folders[0].Folder != "." {
		t.Errorf("folders %+v", res.Folders)
	}
	for sha, want := range map[string]string{
		shaOf(stored(journalRaw("m1", "one"))):   "import:piler",
		shaOf(stored(journalRaw("", "no id"))):   "import:piler",
		shaOf(stored(identical)):                 "journaled",
		shaOf(stored(journalRaw("m2", "two"))):   "",
		shaOf(stored(journalRaw("", "missing"))): "",
	} {
		if got := strings.Join(e.origin(acc, sha), ","); got != want {
			t.Errorf("%s: %q, want %q", sha[:8], got, want)
		}
	}
	// The Date header is the internal date; the locations are numbered
	// after the deliveries.
	for _, p := range e.placesOf(acc) {
		if p.subject == "one" && (!p.date.Equal(time.Date(2020, 3, 3, 10, 0, 0, 0, time.UTC)) || p.uid < 4) {
			t.Errorf("imported place %+v", p)
		}
	}
	if n := e.journalLocations(acc); n != 5 {
		t.Errorf("%d locations", n)
	}

	// A re-run adds nothing; a second run with a new file keeps the labels
	// apart.
	writeEML(t, dir, map[string]string{"5.eml": journalRaw("m5", "five")}, mtime)
	res = e.importJournal(acc, e.smtpImport(acc, "second", "eml"), dir)
	if tot := res.Total(); tot.Read != 5 || tot.Added != 1 || tot.Present != 3 || tot.Journaled != 1 {
		t.Fatalf("again %+v", tot)
	}
	if got := e.origin(acc, shaOf(stored(journalRaw("m5", "five")))); len(got) != 1 || got[0] != "import:second" {
		t.Errorf("second run %v", got)
	}
	if got := e.origin(acc, shaOf(stored(journalRaw("m1", "one")))); len(got) != 1 || got[0] != "import:piler" {
		t.Errorf("first run %v", got)
	}
	if n := e.journalLocations(acc); n != 6 {
		t.Errorf("%d locations after the second run", n)
	}
}

// TestImportJournalKnownMessages is the regression test for the Message-ID
// of messages the archive already holds: BuildMeta leaves it empty, so the
// journaled check must read it from the message. The piler stock sits in
// an import account first, then goes into JOURNAL.
func TestImportJournalKnownMessages(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	dir := t.TempDir()
	writeEML(t, dir, map[string]string{"a.eml": journalRaw("p1", "piler one"), "b.eml": journalRaw("p2", "piler two")}, time.Now())
	e.runImport(e.importer(), e.importAccount("piler-alice"), "eml", dir, "piler")
	e.deliver(acc, append([]byte("Received: from mx by archive\r\n"), stored(journalRaw("p1", "piler one"))...))

	res := e.importJournal(acc, e.smtpImport(acc, "piler", "eml"), dir)
	if tot := res.Total(); tot.Read != 2 || tot.Journaled != 1 || tot.Added != 1 || tot.New != 0 {
		t.Fatalf("%+v", tot)
	}
	if got := e.origin(acc, shaOf(stored(journalRaw("p1", "piler one")))); len(got) != 0 {
		t.Errorf("journaled piler copy stored again: %v", got)
	}
	if n := e.journalLocations(acc); n != 2 {
		t.Errorf("%d locations", n)
	}
}

// A journaled message whose file only the import wrote is scheduled for
// removal, so it does not stay behind as an orphan.
func TestImportJournalRemovesJournaledFiles(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	e.deliver(acc, append([]byte("Received: from mx by archive\r\n"), stored(journalRaw("j1", "journaled"))...))
	dir := t.TempDir()
	writeEML(t, dir, map[string]string{"a.eml": journalRaw("j1", "journaled")}, time.Now())
	if tot := e.importJournal(acc, e.smtpImport(acc, "files", "eml"), dir).Total(); tot.Journaled != 1 {
		t.Fatalf("%+v", tot)
	}
	sha := shaOf(stored(journalRaw("j1", "journaled")))
	if n, err := archive.SweepBlobs(e.ctx, e.st, e.blobs, archive.SweepOptions{Logger: quiet}); err != nil || n != 1 || e.blobs.Exists(sha) {
		t.Fatalf("sweep %d, %v, file there: %v", n, err, e.blobs.Exists(sha))
	}
	res, err := archive.Verify(e.ctx, e.st, e.blobs, archive.VerifyOptions{LockTimeout: time.Second, Logger: quiet})
	if err != nil || res.Problems() != 0 {
		t.Fatalf("verify %+v, %v", res, err)
	}
}

// Two copies of a journaled message in one batch both count as journaled,
// not one as present, and the file is removed once (#306).
func TestImportJournalDuplicateJournaled(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	e.deliver(acc, append([]byte("Received: from mx by archive\r\n"), stored(journalRaw("j1", "journaled"))...))
	dir := t.TempDir()
	raw := journalRaw("j1", "journaled")
	writeEML(t, dir, map[string]string{"a.eml": raw, "b.eml": raw}, time.Now())
	if tot := e.importJournal(acc, e.smtpImport(acc, "files", "eml"), dir).Total(); tot.Read != 2 || tot.Journaled != 2 || tot.Present != 0 || tot.Added != 0 {
		t.Fatalf("%+v", tot)
	}

	// The control: two copies of a new message are stored once.
	dir = t.TempDir()
	fresh := journalRaw("fresh", "fresh")
	writeEML(t, dir, map[string]string{"a.eml": fresh, "b.eml": fresh}, time.Now())
	if tot := e.importJournal(acc, e.smtpImport(acc, "fresh", "eml"), dir).Total(); tot.Read != 2 || tot.Added != 1 || tot.Present != 1 || tot.Journaled != 0 {
		t.Fatalf("new copies %+v", tot)
	}
	if n := e.journalLocations(acc); n != 2 {
		t.Errorf("%d locations", n)
	}

	sha := shaOf(stored(raw))
	if n, err := archive.SweepBlobs(e.ctx, e.st, e.blobs, archive.SweepOptions{Logger: quiet}); err != nil || n != 1 || e.blobs.Exists(sha) {
		t.Fatalf("sweep %d, %v, file there: %v", n, err, e.blobs.Exists(sha))
	}
	res, err := archive.Verify(e.ctx, e.st, e.blobs, archive.VerifyOptions{LockTimeout: time.Second, Logger: quiet})
	if err != nil || res.Problems() != 0 {
		t.Fatalf("verify %+v, %v", res, err)
	}
}

// mbox files and Maildirs with subfolders are merged into JOURNAL; a
// message in two subfolders is stored once.
func TestImportJournalMboxAndMaildir(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	res := e.importJournal(acc, e.smtpImport(acc, "thunderbird", "mbox"), "testdata/import/thunderbird")
	if tot := res.Total(); res.Status != "ok" || tot.Read != 4 || tot.Added != 4 || len(res.Folders) != 2 {
		t.Fatalf("mbox %s %+v %+v", res.Status, tot, res.Folders)
	}
	flagged := false
	for _, p := range e.placesOf(acc) {
		if p.subject == "Second message" {
			flagged = strings.Contains(strings.Join(p.flags, " "), `\Flagged`)
		}
	}
	if !flagged {
		t.Errorf("mbox flags lost: %+v", e.placesOf(acc))
	}

	dir := filepath.Join(t.TempDir(), "Maildir")
	writeMaildir(t, dir, map[string]string{
		"cur/1700000000.1.host:2,S":              journalRaw("md1", "maildir one"),
		".Archive/cur/1700000001.2.host:2,S":     journalRaw("md1", "maildir one"), // the same message again
		".Archive/new/1700000002.3.host":         journalRaw("md2", "maildir two"),
		".Archive.2024/cur/1700000003.4.host:2,": journalRaw("md3", "maildir three"),
	}, time.Now())
	res = e.importJournal(acc, e.smtpImport(acc, "maildir", "maildir"), dir)
	if tot := res.Total(); res.Status != "ok" || tot.Read != 4 || tot.Added != 3 || tot.Present != 1 || len(res.Folders) != 3 {
		t.Fatalf("maildir %s %+v %+v", res.Status, tot, res.Folders)
	}
	if n := e.journalLocations(acc); n != 7 {
		t.Errorf("%d locations", n)
	}
	if got := e.origin(acc, shaOf(stored(journalRaw("md1", "maildir one")))); len(got) != 1 || got[0] != "import:maildir" {
		t.Errorf("md1 %v", got)
	}
}

func TestImportJournalRefuses(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	imp := e.smtpImport(acc, "files", "mbox")
	folders, _, err := archive.JournalImportFolders("mbox", "testdata/import/thunderbird")
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []*store.Account{e.importAccount("piler"), e.acc} {
		if _, err := e.importer().ImportJournal(e.ctx, other, &store.SMTPImport{ID: imp.ID, AccountID: other.ID}, folders); err == nil {
			t.Errorf("import into %q (%s)", other.Name, other.Kind)
		}
	}
	if _, err := e.importer().ImportJournal(e.ctx, acc, &store.SMTPImport{ID: imp.ID, AccountID: e.acc.ID}, folders); err == nil {
		t.Error("an import of another account")
	}

	unlock, ok, err := e.st.TryLockSync(e.ctx, acc.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := e.importer().ImportJournal(e.ctx, acc, imp, folders); !errors.Is(err, archive.ErrSyncRunning) {
		t.Errorf("held lock: %v", err)
	}
	unlock()

	e.deliver(acc, stored(journalRaw("kept", "kept")))
	fresh, err := e.st.GetAccount(e.ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := e.st.DeleteOrRemoveAccount(e.ctx, fresh.Ref()); err != nil || r != store.AccountRemoved {
		t.Fatalf("remove: %v %v", r, err)
	}
	if _, err := e.importer().ImportJournal(e.ctx, acc, imp, folders); !errors.Is(err, archive.ErrAccountRemoved) {
		t.Errorf("removed account: %v", err)
	}
}

// retainedOld imports an old message into JOURNAL and lets retention
// delete it; it returns the message as the source holds it.
func (e *archiveEnv) retainedOld(acc *store.Account) string {
	e.t.Helper()
	old := "Message-ID: <old@example.com>\nSubject: old\nDate: Fri, 05 Mar 2010 10:00:00 +0000\n\nold\n"
	dir := e.t.TempDir()
	writeEML(e.t, dir, map[string]string{"old.eml": old}, time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC))
	if tot := e.importJournal(acc, e.smtpImport(acc, "files", "eml"), dir).Total(); tot.Added != 1 {
		e.t.Fatalf("%+v", tot)
	}
	if _, err := e.st.AddRetentionRule(e.ctx, store.NewRetentionRule{AccountID: acc.ID, Folder: store.SMTPFolder, Years: 1}); err != nil {
		e.t.Fatal(err)
	}
	run, err := e.st.ProposeDeletion(e.ctx, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.ConfirmDeletionRun(e.ctx, run.ID, nil); err != nil {
		e.t.Fatal(err)
	}
	if done, err := archive.ExecuteDeletionRun(e.ctx, e.st, run.ID, archive.DeletionOptions{Logger: quiet}); err != nil || done.Deleted != 1 {
		e.t.Fatalf("deletion %+v, %v", done, err)
	}
	return old
}

// A message retention deleted from JOURNAL is not stored again.
func TestImportJournalAfterRetention(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	old := e.retainedOld(acc)
	dir := t.TempDir()
	writeEML(t, dir, map[string]string{"old.eml": old}, time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC))
	tot := e.importJournal(acc, e.smtpImport(acc, "again", "eml"), dir).Total()
	if tot.Added != 0 || tot.Deleted != 1 || tot.New != 0 {
		t.Fatalf("after retention %+v", tot)
	}
	if n := e.journalLocations(acc); n != 0 {
		t.Errorf("%d locations", n)
	}
}

// Two copies of a message retention deleted, in one batch, both count as
// deleted, not one as present (#306).
func TestImportJournalDuplicateAfterRetention(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.smtpAccount("journal")
	old := e.retainedOld(acc)
	dir := t.TempDir()
	writeEML(t, dir, map[string]string{"old.eml": old, "copy.eml": old}, time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC))
	tot := e.importJournal(acc, e.smtpImport(acc, "again", "eml"), dir).Total()
	if tot.Read != 2 || tot.Deleted != 2 || tot.Present != 0 || tot.Added != 0 || tot.New != 0 {
		t.Fatalf("after retention %+v", tot)
	}
	if n := e.journalLocations(acc); n != 0 {
		t.Errorf("%d locations", n)
	}

	// The control: two copies of a new message are stored once.
	dir = t.TempDir()
	fresh := journalRaw("fresh", "fresh")
	writeEML(t, dir, map[string]string{"a.eml": fresh, "b.eml": fresh}, time.Now())
	tot = e.importJournal(acc, e.smtpImport(acc, "fresh", "eml"), dir).Total()
	if tot.Read != 2 || tot.Added != 1 || tot.Present != 1 || tot.New != 1 || tot.Deleted != 0 {
		t.Fatalf("new copies %+v", tot)
	}
	if n := e.journalLocations(acc); n != 1 {
		t.Errorf("%d locations", n)
	}
}
