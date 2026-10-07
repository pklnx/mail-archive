package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// archiveSetup is a test database with users anna and bob, who both have an
// account named "mail" with one message in INBOX.
type archiveSetup struct {
	st      *store.Store
	blobs   *blobstore.Store
	dataDir string
	accs    map[string]*store.Account
}

func newArchiveSetup(t *testing.T) *archiveSetup {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	dataDir := t.TempDir()
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", dataDir)
	t.Setenv("MAIL_ARCHIVE_LOG_LEVEL", "error")
	blobs, err := blobstore.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s := &archiveSetup{st: st, blobs: blobs, dataDir: dataDir, accs: map[string]*store.Account{}}
	ctx := context.Background()
	for _, name := range []string{"anna", "bob"} {
		u, err := st.CreateUser(ctx, name, "h", false)
		if err != nil {
			t.Fatal(err)
		}
		acc := &store.Account{Name: "mail", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &u.ID}
		if err := st.CreateAccount(ctx, acc); err != nil {
			t.Fatal(err)
		}
		s.accs[name] = acc
		f, err := st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := blobs.Put(strings.NewReader("Subject: for " + name + "\r\n\r\nhi\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		meta := store.MessageMeta{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path}
		loc := store.Location{FolderID: f.ID, UID: 1}
		if _, err := st.SaveBatch(ctx, f.ID, 1, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var e *exitError
	if errors.As(err, &e) {
		return e.exitCode()
	}
	return 1
}

func runVerifyCmd(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := newVerifyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := runCmd(t, cmd, "", args...)
	return exitCodeOf(err), out.String()
}

func TestVerifyExitCodes(t *testing.T) {
	s := newArchiveSetup(t)
	if code, out := runVerifyCmd(t); code != 0 || !strings.Contains(out, "checked 2 message(s): no problems") {
		t.Fatalf("clean: exit %d\n%s", code, out)
	}

	orphan, _, _ := s.blobs.Put(strings.NewReader("Subject: orphan\r\n\r\n"))
	code, out := runVerifyCmd(t)
	if code != 1 || !strings.Contains(out, "orphan") || !strings.Contains(out, orphan.Path) {
		t.Fatalf("orphan: exit %d\n%s", code, out)
	}

	// A sync writes files: the orphan check is skipped.
	unlock, ok, err := s.st.TryLockSyncForWrite(context.Background(), s.accs["anna"].ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	code, out = runVerifyCmd(t, "--fix-orphans", "--lock-timeout", "100ms")
	unlock()
	if code != 2 || !strings.Contains(out, "orphan check skipped") || !strings.Contains(out, "moved nothing") {
		t.Fatalf("busy: exit %d\n%s", code, out)
	}
	if !s.blobs.Exists(orphan.SHA256) {
		t.Fatal("orphan moved while a sync was writing")
	}

	// Moving the only finding leaves nothing to report.
	if code, out := runVerifyCmd(t, "--fix-orphans"); code != 0 || !strings.Contains(out, "moved 1 file(s)") {
		t.Fatalf("fix: exit %d\n%s", code, out)
	}

	// A missing file shows where the message was found.
	missing := blobstore.RelPath(sha("anna"))
	if err := os.Remove(filepath.Join(s.dataDir, missing)); err != nil {
		t.Fatal(err)
	}
	if code, out := runVerifyCmd(t); code != 1 || !strings.Contains(out, "in anna/mail/INBOX UID 1") {
		t.Fatalf("missing: exit %d\n%s", code, out)
	}

	// Errors that are not findings exit 2, also usage errors.
	if code, _ := runVerifyCmd(t, "--jobs", "0"); code != 2 {
		t.Errorf("--jobs 0: exit %d", code)
	}
	if code, _ := runVerifyCmd(t, "--jobs", "many"); code != 2 {
		t.Errorf("--jobs many: exit %d", code)
	}
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", "postgres://nobody:x@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if code, out := runVerifyCmd(t); code != 2 || !strings.Contains(out, "error:") {
		t.Errorf("no database: exit %d\n%s", code, out)
	}
}

// sha is the hash of the message newArchiveSetup stores for a user.
func sha(name string) string {
	sum := sha256.Sum256([]byte("Subject: for " + name + "\r\n\r\nhi\r\n"))
	return hex.EncodeToString(sum[:])
}

func TestExportCommand(t *testing.T) {
	s := newArchiveSetup(t)
	out := filepath.Join(t.TempDir(), "x")

	if err := runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", out); err == nil || !strings.Contains(err.Error(), "--user or --account") {
		t.Errorf("no selection: %v", err)
	}
	if err := runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", out, "--user", "anna", "--folder", "INBOX"); err == nil {
		t.Error("--folder without --account accepted")
	}
	if err := runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", out, "--account", "mail"); err == nil || !strings.Contains(err.Error(), "several users") {
		t.Errorf("ambiguous account: %v", err)
	}
	if err := runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", out, "--user", "anna"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "mail", "INBOX.mbox"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "for anna") || strings.Contains(string(data), "for bob") {
		t.Errorf("anna's export:\n%s", data)
	}
	if err := runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", out, "--user", "anna"); err == nil {
		t.Error("existing directory accepted")
	}
	out2 := filepath.Join(t.TempDir(), "y")
	if err := runCmd(t, newExportCmd(), "", "--format", "maildir", "--out", out2, "--account", "mail", "--user", "bob", "--folder", "INBOX"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(out2, "mail", "INBOX", "cur")); len(entries) != 1 {
		t.Errorf("bob's Maildir: %v", entries)
	}

	// A missing file is skipped and the command exits 1.
	if err := os.Remove(filepath.Join(s.dataDir, blobstore.RelPath(sha("bob")))); err != nil {
		t.Fatal(err)
	}
	err = runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", filepath.Join(t.TempDir(), "z"), "--user", "bob")
	if code := exitCodeOf(err); code != 1 || err == nil {
		t.Errorf("missing file: exit %d, %v", code, err)
	}
}

// fakePgDump writes a pg_dump that records its arguments and environment.
func fakePgDump(t *testing.T, version string) (path, logDir string) {
	t.Helper()
	dir := t.TempDir()
	logDir = t.TempDir()
	script := `#!/bin/sh
if [ "$1" = --version ]; then echo "pg_dump (PostgreSQL) ` + version + `"; exit 0; fi
printf '%s\n' "$@" > "` + logDir + `/args"
env > "` + logDir + `/env"
if [ -n "$FAKE_FAIL" ]; then echo "connection refused" >&2; printf 'half'; exit 1; fi
printf 'PGDMP'
`
	path = filepath.Join(dir, "pg_dump")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // an executable test script
		t.Fatal(err)
	}
	return path, logDir
}

func TestBackup(t *testing.T) {
	pgDump, logDir := fakePgDump(t, "18.1 (Debian 18.1-1)")
	t.Setenv("PGPASSWORD", "inherited")
	t.Setenv("PGAPPNAME", "inherited")
	dir := filepath.Join(t.TempDir(), "backups")
	job := backupJob{
		PgDump:      pgDump,
		DatabaseURL: "postgres://mailarchive:s3cret-pw@db.internal:5433/archive?sslmode=require",
		Dir:         dir, Name: "mailarchive-20261007T190102Z.dump", ServerMajor: 18, Note: "the note\n",
	}
	path, err := runBackup(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "PGDMP" {
		t.Errorf("dump %q", data)
	}
	args, _ := os.ReadFile(filepath.Join(logDir, "args"))
	if strings.Contains(string(args), "s3cret") || string(args) != "--format=custom\n--no-password\n" {
		t.Errorf("args %q", args)
	}
	env, _ := os.ReadFile(filepath.Join(logDir, "env"))
	for _, want := range []string{"PGPASSWORD=s3cret-pw\n", "PGHOST=db.internal\n", "PGPORT=5433\n", "PGUSER=mailarchive\n",
		"PGDATABASE=archive\n", "PGSSLMODE=require\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("env lacks %q", want)
		}
	}
	if strings.Contains(string(env), "inherited") {
		t.Error("inherited PG variables reach pg_dump")
	}
	for _, p := range []string{path, filepath.Join(dir, backupNoteName)} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", p, fi, err)
		}
	}
	if note, _ := os.ReadFile(filepath.Join(dir, backupNoteName)); string(note) != "the note\n" {
		t.Errorf("note %q", note)
	}

	// A failing pg_dump leaves neither a dump nor a partial file.
	t.Setenv("FAKE_FAIL", "1")
	job.Name = "second.dump"
	if _, err := runBackup(context.Background(), job); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("failing pg_dump: %v", err)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "second.dump*")); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}

	// An older pg_dump than the server is refused before it runs.
	old, oldLog := fakePgDump(t, "16.4")
	job.PgDump, job.Name = old, "third.dump"
	if _, err := runBackup(context.Background(), job); err == nil || !strings.Contains(err.Error(), "version 16, the server 18") {
		t.Errorf("old pg_dump: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldLog, "args")); !os.IsNotExist(err) {
		t.Error("old pg_dump ran")
	}
}

func TestBackupNote(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", "/srv/archive")
	a, err := openApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	note, err := backupNote(context.Background(), a, "mailarchive-x.dump", time.Date(2026, 10, 7, 19, 1, 2, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(note, key) {
		t.Fatal("the note contains the key")
	}
	if !regexpMatch(`Last migration: \d{5}_\w+\.sql\n`, note) {
		t.Errorf("note lacks the last migration:\n%s", note)
	}
	for _, want := range []string{"mailarchive-x.dump", "2026-10-07T19:01:02Z", "/srv/archive", "MAIL_ARCHIVE_SECRET_KEY", "pg_restore --clean --if-exists"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
}

func TestSSLSettings(t *testing.T) {
	got := sslSettings("postgresql://u@h/db?sslmode=verify-full&sslrootcert=/ca.pem&application_name=x")
	if len(got) != 2 || got["PGSSLMODE"] != "verify-full" || got["PGSSLROOTCERT"] != "/ca.pem" {
		t.Errorf("URL: %v", got)
	}
	got = sslSettings("host=h dbname=db sslmode='verify-ca' sslkey=/k.pem")
	if len(got) != 2 || got["PGSSLMODE"] != "verify-ca" || got["PGSSLKEY"] != "/k.pem" {
		t.Errorf("key=value: %v", got)
	}
}

func TestPgDumpMajor(t *testing.T) {
	for in, want := range map[string]int{"pg_dump (PostgreSQL) 18.1\n": 18, "pg_dump (PostgreSQL) 16.4 (Debian 16.4-1)": 16, "pg_dump (PostgreSQL) 9.6.24": 9} {
		if got, err := pgDumpMajor(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	if _, err := pgDumpMajor("something else"); err == nil {
		t.Error("garbage accepted")
	}
}
