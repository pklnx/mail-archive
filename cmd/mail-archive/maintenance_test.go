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
	"github.com/pklnx/mail-archive/internal/config"
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
	url     string
	accs    map[string]*store.Account
}

func newArchiveSetup(t *testing.T) *archiveSetup {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	dataDir := t.TempDir()
	t.Setenv(config.EnvDatabaseURL, url)
	t.Setenv(config.EnvDataDir, dataDir)
	t.Setenv(config.EnvLogLevel, "error")
	blobs, err := blobstore.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s := &archiveSetup{st: st, blobs: blobs, dataDir: dataDir, url: url, accs: map[string]*store.Account{}}
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
	t.Setenv(config.EnvDatabaseURL, "postgres://nobody:x@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
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

// fakePgDump writes a pg_dump that records its arguments and environment
// (logDir/version-env for --version, logDir/env for the dump). It fails
// while logDir/fail exists: pg_dump gets no variable of the test's
// environment.
func fakePgDump(t *testing.T, version string) (path, logDir string) {
	t.Helper()
	dir := t.TempDir()
	logDir = t.TempDir()
	script := `#!/bin/sh
if [ "$1" = --version ]; then env > "` + logDir + `/version-env"; echo "pg_dump (PostgreSQL) ` + version + `"; exit 0; fi
printf '%s\n' "$@" > "` + logDir + `/args"
env > "` + logDir + `/env"
if [ -e "` + logDir + `/fail" ]; then echo "connection refused" >&2; printf 'half'; exit 1; fi
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
	basics := setEnvMarkers(t)
	dir := filepath.Join(t.TempDir(), "backups")
	const dbURL = "postgres://mailarchive:s3cret-pw@db.internal:5433/archive?sslmode=require"
	job := backupJob{
		PgDump: testPgTool(t, pgDump, dbURL, 18),
		Dir:    dir, Name: "mailarchive-20261007T190102Z.dump", Note: "the note\n",
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
	for _, name := range []string{"version-env", "env"} {
		assertPgToolEnv(t, "pg_dump "+name, filepath.Join(logDir, name), append(basics, "PGPASSWORD=s3cret-pw", "PGHOST=db.internal")...)
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
	if err := os.WriteFile(filepath.Join(logDir, "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	job.Name = "second.dump"
	if _, err := runBackup(context.Background(), job); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("failing pg_dump: %v", err)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "second.dump*")); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}

	// An older pg_dump than the server is refused before it runs.
	old, oldLog := fakePgDump(t, "16.4")
	conn, err := resolvePgToolConn("pg_dump", dbURL, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkPgTool(context.Background(), "pg_dump", old, conn, 18); err == nil || !strings.Contains(err.Error(), "version 16, the server 18") {
		t.Errorf("old pg_dump: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldLog, "args")); !os.IsNotExist(err) {
		t.Error("old pg_dump ran")
	}
}

func TestBackupNote(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	t.Setenv(config.EnvDatabaseURL, url)
	key, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, key)
	t.Setenv(config.EnvDataDir, "/srv/archive")
	a, err := openApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	note, err := backupNote(context.Background(), a, "mailarchive-x.dump", nil, time.Date(2026, 10, 7, 19, 1, 2, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(note, key) {
		t.Fatal("the note contains the key")
	}
	if !regexpMatch(`Last migration: \d{5}_\w+\.sql\n`, note) {
		t.Errorf("note lacks the last migration:\n%s", note)
	}
	for _, want := range []string{"mailarchive-x.dump", "2026-10-07T19:01:02Z", "/srv/archive", config.EnvSecretKey, "pg_restore --no-owner --single-transaction --exit-on-error", "create an empty database"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
}

// envMarker is a value no variable passed to pg_dump or pg_restore may hold.
const envMarker = "marker-must-not-leak"

// setEnvMarkers puts the marker into the secrets of this process, an
// unrelated variable and a parent PGPASSWORD, gives every allowlisted
// basic but PATH a known value, and returns those basics as NAME=value.
func setEnvMarkers(t *testing.T) []string {
	t.Helper()
	tmp := t.TempDir() // TMPDIR must stay a usable directory
	var basics []string
	for _, name := range []string{config.EnvSecretKey, config.EnvArchiveKey, config.EnvDatabaseURL,
		config.EnvNotifyWebhookAuthorization, config.EnvNotifyWebhookURL, "UNRELATED_L15", "PGPASSWORD", "PGAPPNAME"} {
		t.Setenv(name, envMarker+"-"+name)
	}
	for _, name := range pgToolEnvNames {
		value := "basic-" + name
		switch name {
		case "PATH": // the fake tools are shell scripts
			continue
		case "TMPDIR":
			value = tmp
		}
		t.Setenv(name, value)
		basics = append(basics, name+"="+value)
	}
	return basics
}

// pgURLEnvNames are the variables pgToolConn.env derives from the database
// URL.
var pgURLEnvNames = []string{"PGHOST", "PGPORT", "PGUSER", "PGDATABASE", "PGPASSWORD", "PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY",
	"PGSSLSNI", "PGSSLNEGOTIATION", "PGTARGETSESSIONATTRS", "PGCHANNELBINDING", "PGREQUIREAUTH", "PGGSSENCMODE"}

// checkPgToolEnv reports what is wrong with env, the environment pg_dump or
// pg_restore got after setEnvMarkers: a marker, a variable outside the
// allowlist or a missing want entry.
func checkPgToolEnv(env []string, want ...string) []string {
	allowed := map[string]bool{}
	for _, name := range append(append([]string{}, pgToolEnvNames...), pgURLEnvNames...) {
		allowed[name] = true
	}
	var problems []string
	have := map[string]bool{}
	for _, kv := range env {
		have[kv] = true
		name, _, _ := strings.Cut(kv, "=")
		if strings.Contains(kv, envMarker) {
			problems = append(problems, "marker in "+name)
		}
		if !allowed[name] {
			problems = append(problems, "not allowlisted: "+name)
		}
	}
	for _, kv := range want {
		if !have[kv] {
			problems = append(problems, "lacks "+kv)
		}
	}
	return problems
}

// assertPgToolEnv checks the environment a fake tool wrote to path with env.
func assertPgToolEnv(t *testing.T, what, path string, want ...string) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // the test's own log
	if err != nil {
		t.Errorf("%s: %v", what, err)
		return
	}
	var env []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		// env prints the variables the shell itself sets, too.
		if name, _, _ := strings.Cut(line, "="); name != "PWD" && name != "SHLVL" && name != "_" && name != "OLDPWD" {
			env = append(env, line)
		}
	}
	for _, p := range checkPgToolEnv(env, want...) {
		t.Errorf("%s: %s", what, p)
	}
}

// TestPgEnvAllowlist: pg_dump and pg_restore get the libpq variables of the
// URL and the allowlisted basics, never a KEEPIA_ARCHIVE_* or MAIL_ARCHIVE_*
// variable, an unrelated one or a PG* variable of this process.
func TestPgEnvAllowlist(t *testing.T) {
	// The allowlist is fixed; a change here is a security change.
	if got, want := strings.Join(pgToolEnvNames, " "), "PATH HOME TMPDIR TZ LANG LC_ALL LC_CTYPE LC_MESSAGES LD_LIBRARY_PATH "+
		"SSL_CERT_FILE SSL_CERT_DIR OPENSSL_CONF KRB5CCNAME KRB5_CONFIG"; got != want {
		t.Errorf("allowlist %q, want %q", got, want)
	}
	basics := setEnvMarkers(t)
	t.Setenv("PATH", "/opt/pg/bin:/usr/bin")
	env := pgToolEnv(t, "postgres://u:url-pw@db.example:6543/arch?sslmode=require&connect_timeout=5&application_name=x")
	for _, p := range checkPgToolEnv(env, append(basics, "PATH=/opt/pg/bin:/usr/bin", "PGHOST=db.example", "PGPORT=6543", "PGUSER=u",
		"PGDATABASE=arch", "PGPASSWORD=url-pw", "PGSSLMODE=require", "PGGSSENCMODE=disable",
		"PGSSLROOTCERT=basic-HOME/.postgresql/root.crt", "PGSSLCERT=basic-HOME/.postgresql/postgresql.crt", "PGSSLKEY=basic-HOME/.postgresql/postgresql.key",
		"PGSSLSNI=1", "PGSSLNEGOTIATION=postgres", "PGTARGETSESSIONATTRS=any", "PGCHANNELBINDING=prefer")...) {
		t.Error(p)
	}
	// Every basic (PATH included), the five connection variables, the TLS
	// variables but the empty require_auth, and PGGSSENCMODE=disable; the
	// other URL parameters are not passed on, as before, and neither is
	// the parent's PGAPPNAME (setEnvMarkers). TestPgToolConn covers the
	// TLS keys.
	if len(env) != len(pgToolEnvNames)+5+8+1 {
		t.Errorf("%d variables: %v", len(env), env)
	}

	// An unset basic is left out, not passed on empty. (A URL without a
	// password takes PGPASSWORD of this process as the app's connection
	// does, through pgconn, so it is unset here.)
	for _, name := range []string{"TZ", "PGPASSWORD"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	env = pgToolEnv(t, "postgres://u@db.example/arch")
	for _, kv := range env {
		if strings.HasPrefix(kv, "TZ=") || strings.HasPrefix(kv, "PGPASSWORD=") || strings.Contains(kv, envMarker) {
			t.Errorf("passed on: %s", kv)
		}
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
