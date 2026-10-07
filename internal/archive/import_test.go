package archive_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/mailbox"
	"github.com/pklnx/mail-archive/internal/store"
)

var importStart = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func (e *archiveEnv) importAccount(name string) *store.Account {
	e.t.Helper()
	a := &store.Account{Kind: store.KindImport, Name: name, OwnerID: &e.user.ID}
	if err := e.st.CreateAccount(e.ctx, a); err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *archiveEnv) importer() *archive.Importer {
	return &archive.Importer{Store: e.st, Blobs: e.blobs, Logger: quiet, BatchSize: 2, Now: func() time.Time { return importStart }}
}

func (e *archiveEnv) runImport(im *archive.Importer, acc *store.Account, format, path, folder string) *archive.ImportResult {
	e.t.Helper()
	folders, _, err := archive.ImportFolders(format, path, folder)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := im.Import(e.ctx, acc, folders)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

type placed struct {
	folder      string
	uidValidity uint32
	uid         uint32
	flags       []string
	date        time.Time
	subject     string
}

// placesOf lists the locations of an account by folder and UID.
func (e *archiveEnv) placesOf(acc *store.Account) []placed {
	e.t.Helper()
	folders, err := e.st.ListExportFolders(e.ctx, acc.OwnerID, []int64{acc.ID}, "")
	if err != nil {
		e.t.Fatal(err)
	}
	var out []placed
	for _, f := range folders {
		locs, err := e.st.ListExportLocations(e.ctx, acc.OwnerID, f.ID, 1<<62, -1, -1, 1000)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, l := range locs {
			m, err := e.st.GetMessage(e.ctx, l.SHA256)
			if err != nil {
				e.t.Fatal(err)
			}
			p := placed{folder: f.Name, uidValidity: l.UIDValidity, uid: l.UID, flags: l.Flags, subject: m.Subject}
			if l.InternalDate != nil {
				p.date = l.InternalDate.UTC()
			}
			out = append(out, p)
		}
	}
	return out
}

func TestImportThunderbird(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.importAccount("thunderbird")
	res := e.runImport(e.importer(), acc, "mbox", "testdata/import/thunderbird", "")
	if res.Status != "ok" {
		t.Fatalf("status %s", res.Status)
	}
	if tot := res.Total(); tot.Read != 4 || tot.New != 4 || tot.Added != 4 || tot.Present != 0 || tot.Skipped != 0 {
		t.Fatalf("total %+v", tot)
	}
	v := uint32(importStart.Unix())
	want := []placed{
		{"Inbox", v, 1, []string{`\Seen`, `\Answered`}, time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC), "First message"},
		{"Inbox", v, 2, []string{`\Flagged`}, time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC), "Second message"},
		{"Inbox", v, 3, []string{}, time.Date(2026, 1, 7, 11, 30, 0, 0, time.UTC), "Third message"},
		{"Inbox/Work", v, 1, []string{`\Seen`}, time.Date(2026, 1, 8, 8, 0, 0, 0, time.UTC), "Work message"},
	}
	got := e.placesOf(acc)
	if len(got) != len(want) {
		t.Fatalf("locations %+v", got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.folder != w.folder || g.uidValidity != w.uidValidity || g.uid != w.uid || !slices.Equal(g.flags, w.flags) ||
			!g.date.Equal(w.date) || g.subject != w.subject {
			t.Errorf("location %d:\n got %+v\nwant %+v", i, g, w)
		}
	}

	// Stored with CRLF, the escaped line unescaped, X-Mozilla headers kept.
	raw, err := os.ReadFile(e.path(blobstore.RelPath(firstHash(t, e, acc))))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"X-Mozilla-Status: 0001\r\n", "\r\nFrom the start of a line, this stays in the body.\r\nFrom an escaped line.\r\n"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("stored message lacks %q:\n%s", want, raw)
		}
	}
	if bytes.Contains(bytes.ReplaceAll(raw, []byte("\r\n"), nil), []byte("\n")) {
		t.Error("bare LF left in the stored message")
	}

	// A re-run adds nothing.
	res = e.runImport(e.importer(), acc, "mbox", "testdata/import/thunderbird", "")
	if tot := res.Total(); res.Status != "ok" || tot.Read != 4 || tot.Added != 0 || tot.Present != 4 || tot.New != 0 {
		t.Fatalf("re-run %s %+v", res.Status, tot)
	}
	if got := e.placesOf(acc); len(got) != 4 {
		t.Fatalf("after re-run %d locations", len(got))
	}
	stats, _, _ := e.st.Stats(e.ctx, &e.user.ID)
	for _, s := range stats {
		if s.Account == "thunderbird" && (s.LastStatus == nil || *s.LastStatus != "ok" || s.Messages != 4 || s.Kind != store.KindImport) {
			t.Errorf("stats %+v", s)
		}
	}
}

func firstHash(t *testing.T, e *archiveEnv, acc *store.Account) string {
	t.Helper()
	folders, _ := e.st.ListExportFolders(e.ctx, acc.OwnerID, []int64{acc.ID}, "Inbox")
	locs, err := e.st.ListExportLocations(e.ctx, acc.OwnerID, folders[0].ID, 1<<62, -1, -1, 1)
	if err != nil || len(locs) != 1 {
		t.Fatal(locs, err)
	}
	return locs[0].SHA256
}

func TestImportAppleBundleWithPrefix(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.importAccount("apple")
	e.runImport(e.importer(), acc, "mbox", "testdata/import/apple", "Mac")
	got := e.placesOf(acc)
	if len(got) != 2 || got[0].folder != "Mac" || got[1].subject != "Archived two" {
		t.Fatalf("locations %+v", got)
	}
	// A later import into the same folder continues the UIDs.
	e.runImport(e.importer(), acc, "mbox", "testdata/import/thunderbird/Inbox.sbd/Work", "Mac")
	got = e.placesOf(acc)
	if len(got) != 3 || got[2].uid != 3 || got[2].uidValidity != got[0].uidValidity {
		t.Fatalf("after the second import %+v", got)
	}
}

// writeMaildir creates a Maildir with the messages, LF line endings, as an
// older Dovecot stores them.
func writeMaildir(t *testing.T, dir string, files map[string]string, mtime time.Time) {
	t.Helper()
	for _, sub := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		for _, sub := range []string{"cur", "new", "tmp"} {
			if err := os.MkdirAll(filepath.Join(filepath.Dir(filepath.Dir(p)), sub), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

// A message synced from IMAP (CRLF) and imported from a Maildir (LF) is one
// file with two locations.
func TestImportMaildirDeduplicatesWithIMAP(t *testing.T) {
	alice := imapmemserver.NewUser("alice", "pw-a")
	createMailboxes(t, alice, "INBOX")
	appendMsg(t, alice, "INBOX", rawMessage("a1", "synced"))
	f := newFixture(t, alice)
	f.addAccount("alice", "alice", "pw-a")
	expectResult(t, f.sync()["alice"], 1, 1)

	dir := filepath.Join(t.TempDir(), "Maildir")
	lf := strings.ReplaceAll(string(rawMessage("a1", "synced")), "\r\n", "\n")
	mtime := time.Date(2025, 12, 24, 18, 0, 0, 0, time.UTC)
	writeMaildir(t, dir, map[string]string{
		"cur/1700000000.1.host:2,RS":               lf,
		"new/1700000001.2.host":                    strings.ReplaceAll(string(rawMessage("a2", "only here")), "\r\n", "\n"),
		".Archiv.2024/cur/1700000002.3.host:2,F":   lf,
		".Entw&APw-rfe/new/1700000003.4.host":      "Subject: draft\n\nx\n",
		".Entw&APw-rfe/cur/1700000004.5.host:2,DS": "Subject: draft\n\nx\n", // the same bytes again
	}, mtime)

	acc := &store.Account{Kind: store.KindImport, Name: "old server"}
	if err := f.store.CreateAccount(f.ctx, acc); err != nil {
		t.Fatal(err)
	}
	folders, _, err := archive.ImportFolders("maildir", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	im := &archive.Importer{Store: f.store, Blobs: f.blobs, Logger: quiet}
	res, err := im.Import(f.ctx, acc, folders)
	if err != nil {
		t.Fatal(err)
	}
	tot := res.Total()
	if res.Status != "ok" || tot.Read != 5 || tot.Added != 4 || tot.Present != 1 || tot.New != 2 {
		t.Fatalf("%s %+v %+v", res.Status, tot, res.Folders)
	}
	if n := f.countBlobFiles(); n != 3 {
		t.Errorf("%d blob files, want 3 (a1 once, a2, the draft)", n)
	}
	sum := sha256.Sum256(rawMessage("a1", "synced"))
	places, err := f.store.LocationsOfMessages(f.ctx, []string{hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 3 {
		t.Errorf("a1 locations: %+v", places)
	}
	env := &archiveEnv{t: t, ctx: f.ctx, st: f.store, blobs: f.blobs, data: f.dataDir}
	got := env.placesOf(acc)
	byFolder := map[string]placed{}
	for _, p := range got {
		byFolder[p.folder] = p
	}
	if p := byFolder["INBOX"]; !p.date.Equal(mtime) {
		t.Errorf("INBOX date %v", p.date)
	}
	if p := byFolder["Archiv/2024"]; !slices.Equal(p.flags, []string{`\Flagged`}) {
		t.Errorf("Archiv/2024 %+v", p)
	}
	if p, ok := byFolder["Entwürfe"]; !ok || !slices.Equal(p.flags, []string{}) && !slices.Equal(p.flags, nil) {
		t.Errorf("Entwürfe %+v (folders %v)", p, got)
	}
}

// An import stopped after its first batch ends, after a re-run, in the same
// state as one that ran through.
func TestImportResumes(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.importAccount("resumed")
	ctx, cancel := context.WithCancel(e.ctx)
	im := e.importer()
	im.Progress = func(archive.ImportFolderResult) { cancel() }
	folders, _, err := archive.ImportFolders("mbox", "testdata/import/thunderbird", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := im.Import(ctx, acc, folders)
	if !errors.Is(err, context.Canceled) || res.Status != "failed" {
		t.Fatalf("cancelled: %v %+v", err, res)
	}
	if got := e.placesOf(acc); len(got) != 2 {
		t.Fatalf("after the first batch: %+v", got)
	}
	res = e.runImport(e.importer(), acc, "mbox", "testdata/import/thunderbird", "")
	if tot := res.Total(); res.Status != "ok" || tot.Present != 2 || tot.Added != 2 {
		t.Fatalf("resumed: %s %+v", res.Status, tot)
	}

	other := e.importAccount("straight")
	e.runImport(e.importer(), other, "mbox", "testdata/import/thunderbird", "")
	a, b := e.placesOf(acc), e.placesOf(other)
	if len(a) != len(b) {
		t.Fatalf("%d vs %d locations", len(a), len(b))
	}
	for i := range a {
		if a[i].folder != b[i].folder || a[i].uid != b[i].uid || a[i].subject != b[i].subject {
			t.Errorf("location %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestImportSkipsLargeMessages(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.importAccount("big")
	im := e.importer()
	im.MaxMessageSize = 200
	res := e.runImport(im, acc, "mbox", "testdata/import/thunderbird", "")
	tot := res.Total()
	// The first message has about 300 bytes; the others fit.
	if res.Status != "partial" || tot.Skipped != 1 || tot.Added != 3 {
		t.Fatalf("%s %+v", res.Status, tot)
	}
	if entries, _ := os.ReadDir(e.path("tmp")); len(entries) != 0 {
		t.Errorf("temp files left: %v", entries)
	}
}

// Only one import of an account runs; account removal and the orphan check
// of verify wait for it.
func TestImportHoldsTheLocks(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.importAccount("locked")
	inside := make(chan struct{})
	release := make(chan struct{})
	im := e.importer()
	var once sync.Once
	im.Progress = func(archive.ImportFolderResult) {
		once.Do(func() { close(inside); <-release })
	}
	folders, _, err := archive.ImportFolders("mbox", "testdata/import/thunderbird", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := im.Import(e.ctx, acc, folders)
		done <- err
	}()
	<-inside
	func() {
		defer close(release)
		if _, err := e.importer().Import(e.ctx, acc, folders); !errors.Is(err, archive.ErrSyncRunning) {
			t.Errorf("second import: %v", err)
		}
		if _, ok, err := e.st.TryLockSync(e.ctx, acc.ID); err != nil || ok {
			t.Errorf("remove could lock the account: %v %v", ok, err)
		}
		res, err := archive.Verify(e.ctx, e.st, e.blobs, archive.VerifyOptions{FixOrphans: true, LockTimeout: 100 * time.Millisecond, Logger: quiet})
		if err != nil || !res.OrphansSkipped || res.Moved != 0 {
			t.Errorf("verify during the import: %+v %v", res, err)
		}
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestImportAccountsAreNeverSynced(t *testing.T) {
	alice := imapmemserver.NewUser("alice", "pw-a")
	createMailboxes(t, alice, "INBOX")
	f := newFixture(t, alice)
	f.addAccount("alice", "alice", "pw-a")
	acc := &store.Account{Kind: store.KindImport, Name: "imported"}
	if err := f.store.CreateAccount(f.ctx, acc); err != nil {
		t.Fatal(err)
	}
	if r := f.syncer.SyncAccount(f.ctx, acc); !errors.Is(r.Err, archive.ErrImportAccount) {
		t.Errorf("SyncAccount: %v", r.Err)
	}
	results, err := f.syncer.SyncAll(f.ctx, []string{"imported"}, nil)
	if err != nil || len(results) != 0 {
		t.Errorf("SyncAll by name: %+v %v", results, err)
	}
	results, err = f.syncer.SyncAll(f.ctx, nil, nil)
	if err != nil || len(results) != 1 || results[0].Account != "alice" {
		t.Errorf("SyncAll: %+v %v", results, err)
	}
	// An IMAP account cannot take an import.
	imap, _ := f.account("alice")
	if _, err := (&archive.Importer{Store: f.store, Blobs: f.blobs}).Import(f.ctx, imap, nil); err == nil {
		t.Error("import into an IMAP account")
	}
}

// What #39's writer exports imports back to the same bytes.
func TestImportRoundTripsExport(t *testing.T) {
	e := newArchiveEnv(t)
	messages := []string{
		"Subject: one\r\n\r\nFrom the start\r\n>From quoted\r\n",
		"Subject: two\r\n\r\nends with a blank line\r\n\r\n",
		"Subject: three\r\n\r\nmixed\nline\r\n",
	}
	path := filepath.Join(t.TempDir(), "INBOX.mbox")
	var buf bytes.Buffer
	w := mailbox.NewMboxWriter(&buf)
	var want []string
	for _, m := range messages {
		if err := w.WriteMessage(strings.NewReader(m), importStart); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(m))
		want = append(want, hex.EncodeToString(sum[:]))
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	acc := e.importAccount("roundtrip")
	e.runImport(e.importer(), acc, "mbox", path, "")
	folders, _ := e.st.ListExportFolders(e.ctx, acc.OwnerID, []int64{acc.ID}, "INBOX")
	locs, err := e.st.ListExportLocations(e.ctx, acc.OwnerID, folders[0].ID, 1<<62, -1, -1, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range locs {
		got = append(got, l.SHA256)
	}
	if !slices.Equal(got, want) {
		t.Errorf("hashes %v, want %v", got, want)
	}
}
