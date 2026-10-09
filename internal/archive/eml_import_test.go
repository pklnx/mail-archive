package archive_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// writeEML writes files below dir and sets their mtime.
func writeEML(t *testing.T, dir string, files map[string]string, mtime time.Time) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func folderNames(t *testing.T, path, folder string) ([]string, error) {
	t.Helper()
	folders, _, err := archive.ImportFolders("eml", path, folder)
	var names []string
	for _, f := range folders {
		names = append(names, f.Name)
	}
	return names, err
}

// One --folder rule for every format: a single folder gets the name, several
// get it as a prefix, and files directly in --from need it.
func TestEMLFolderNames(t *testing.T) {
	root := t.TempDir()
	writeEML(t, root, map[string]string{"1.eml": "Subject: x\n\nx\n", "a/2.eml": "Subject: y\n\nx\n"}, time.Now())
	if _, err := folderNames(t, root, ""); err == nil || !strings.Contains(err.Error(), "need --folder NAME") {
		t.Fatalf("root files without --folder: %v", err)
	}
	if got, err := folderNames(t, root, "piler"); err != nil || strings.Join(got, ",") != "piler,piler/a" {
		t.Fatalf("with --folder: %v %v", got, err)
	}

	// The usual piler layout: --from /import with the export in /import/piler.
	imp := t.TempDir()
	writeEML(t, imp, map[string]string{"piler/1.eml": "Subject: x\n\nx\n"}, time.Now())
	if got, err := folderNames(t, imp, "piler"); err != nil || strings.Join(got, ",") != "piler" {
		t.Fatalf("single folder: %v %v", got, err)
	}
	if got, err := folderNames(t, imp, ""); err != nil || strings.Join(got, ",") != "piler" {
		t.Fatalf("single folder without --folder: %v %v", got, err)
	}

	bad := t.TempDir()
	writeEML(t, bad, map[string]string{"a\x01b/1.eml": "Subject: x\n\nx\n"}, time.Now())
	if _, err := folderNames(t, bad, ""); err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Fatalf("control character in a folder name: %v", err)
	}
}

func TestImportEML(t *testing.T) {
	e := newArchiveEnv(t)
	acc := e.importAccount("piler")
	dir := t.TempDir()
	mtime := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	writeEML(t, dir, map[string]string{
		"b.eml":        "Message-ID: <b@x>\nSubject: second\nDate: Tue, 2 Jan 2024 10:00:00 +0000\n\nb\n",
		"a.eml":        "Message-ID: <a@x>\nSubject: first\nDate: Mon, 1 Jan 2024 10:00:00 +0100\n\na\n",
		"c.eml":        "Message-ID: <c@x>\nSubject: no date\n\nc\n",
		"empty.eml":    "",
		"notes.txt":    "not a message",
		"2023/x.eml":   "Message-ID: <x@x>\nSubject: older\nDate: Fri, 1 Dec 2023 08:00:00 +0000\n\nx\n",
		"._a.eml":      "AppleDouble",
		".trash/t.eml": "Subject: hidden\n\nt\n",
	}, mtime)

	res := e.runImport(e.importer(), acc, "eml", dir, "piler")
	tot := res.Total()
	if res.Status != "partial" || tot.Read != 5 || tot.Added != 4 || tot.Skipped != 1 {
		t.Fatalf("%s %+v", res.Status, tot)
	}
	got := map[string]placed{}
	for _, p := range e.placesOf(acc) {
		got[p.subject] = p
	}
	for subject, want := range map[string]struct {
		folder string
		uid    uint32
		date   time.Time
	}{
		"first":   {"piler", 1, time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)},
		"second":  {"piler", 2, time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC)},
		"no date": {"piler", 3, mtime},
		"older":   {"piler/2023", 1, time.Date(2023, 12, 1, 8, 0, 0, 0, time.UTC)},
	} {
		p, ok := got[subject]
		if !ok || p.folder != want.folder || p.uid != want.uid || !p.date.Equal(want.date) || len(p.flags) != 0 {
			t.Errorf("%s: %+v, want %+v", subject, p, want)
		}
	}
	if len(got) != 4 {
		t.Errorf("places: %+v", got)
	}

	// A re-run adds nothing; a file added to the export gets the next UID.
	if res := e.runImport(e.importer(), acc, "eml", dir, "piler"); res.Total().Added != 0 || res.Total().Present != 4 {
		t.Fatalf("re-run: %+v", res.Total())
	}
	writeEML(t, dir, map[string]string{"0.eml": "Subject: late\n\nlate\n"}, mtime)
	if res := e.runImport(e.importer(), acc, "eml", dir, "piler"); res.Total().Added != 1 {
		t.Fatalf("third run: %+v", res.Total())
	}
	for _, p := range e.placesOf(acc) {
		if p.subject == "late" && p.uid != 4 {
			t.Errorf("late file: UID %d, want 4", p.uid)
		}
	}
}

func TestImportEMLUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	e := newArchiveEnv(t)
	acc := e.importAccount("piler")
	dir := t.TempDir()
	writeEML(t, dir, map[string]string{"in/a.eml": "Subject: a\n\na\n", "in/b.eml": "Subject: b\n\nb\n"}, time.Now())
	if err := os.Chmod(filepath.Join(dir, "in", "b.eml"), 0); err != nil {
		t.Fatal(err)
	}
	res := e.runImport(e.importer(), acc, "eml", dir, "")
	if res.Status != "partial" || res.Total().Added != 1 || res.Total().Skipped != 1 {
		t.Fatalf("%s %+v", res.Status, res.Total())
	}
}

// The piler copy of a message synced from IMAP (LF instead of CRLF) is
// stored once; its date comes from the stored message's header.
func TestImportEMLDeduplicatesWithIMAP(t *testing.T) {
	alice := imapmemserver.NewUser("alice", "pw")
	createMailboxes(t, alice, "INBOX")
	appendMsg(t, alice, "INBOX", rawMessage("a1", "synced"))
	f := newFixture(t, alice)
	f.addAccount("alice", "alice", "pw")
	expectResult(t, f.sync()["alice"], 1, 1)

	dir := t.TempDir()
	lf := strings.ReplaceAll(string(rawMessage("a1", "synced")), "\r\n", "\n")
	writeEML(t, dir, map[string]string{"export/1.eml": lf}, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	acc := &store.Account{Kind: store.KindImport, Name: "piler"}
	if err := f.store.CreateAccount(f.ctx, acc); err != nil {
		t.Fatal(err)
	}
	folders, _, err := archive.ImportFolders("eml", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	im := &archive.Importer{Store: f.store, Blobs: f.blobs, Logger: quiet}
	res, err := im.Import(f.ctx, acc, folders)
	if err != nil || res.Total().Added != 1 || res.Total().New != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	if n := f.countBlobFiles(); n != 1 {
		t.Errorf("%d blob files, want 1", n)
	}
	env := &archiveEnv{t: t, ctx: f.ctx, st: f.store, blobs: f.blobs, data: f.dataDir}
	places := env.placesOf(acc)
	if want := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC); len(places) != 1 || !places[0].date.Equal(want) {
		t.Fatalf("places %+v, want the date %v from the header", places, want)
	}
}
