package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// Marker words in the test data: none may appear in an encrypted dump
// (the leak test of #392, part 4).
const (
	leakMarker1 = "zebrafinkenlied"
	leakMarker2 = "Quarzuhrenwerk"
)

// testKeyring creates an archive key (set in the environment) and a
// keyring file in dataDir.
func testKeyring(t *testing.T, dataDir string) (*blobcrypt.Keyring, []byte) {
	t.Helper()
	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := blobcrypt.NewKeyring(kek, labelArchiveKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(dataDir), ring); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvArchiveKey, blobcrypt.EncodeKEK(kek))
	return ring, kek
}

// turnOn records encryption at rest as on, with ring as the database copy.
func turnOn(t *testing.T, st *store.Store, ring *blobcrypt.Keyring) {
	t.Helper()
	data, err := ring.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetBlobEncryption(context.Background(), store.BlobOn, store.KeyringCopy{
		Keyring: data, ID: ring.ArchiveID(), Generation: int64(ring.Generation()), CurrentKey: ring.CurrentKey(), //nolint:gosec // small
	}); err != nil {
		t.Fatal(err)
	}
}

func unsealBytes(t *testing.T, data []byte, keys blobcrypt.DataKeys) ([]byte, error) {
	t.Helper()
	r, err := blobcrypt.NewStreamReader(bytes.NewReader(data), keys, blobcrypt.PurposeBackup, nil)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func assertNoMarkers(t *testing.T, what string, data []byte) {
	t.Helper()
	for _, m := range []string{leakMarker1, leakMarker2, "PGDMP"} {
		if bytes.Contains(data, []byte(m)) {
			t.Errorf("%s contains %q in plaintext", what, m)
		}
	}
}

// fakePgDumpMarkers is a pg_dump whose dump holds the marker words; it
// fails while a file "fail" lies next to it (pg_dump gets no variable of
// the test's environment).
func fakePgDumpMarkers(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = --version ]; then echo "pg_dump (PostgreSQL) 18.1"; exit 0; fi
if [ -e "$(dirname "$0")/fail" ]; then printf 'PGDMP half'; echo "connection refused" >&2; exit 1; fi
printf 'PGDMP ` + leakMarker1 + ` '
i=0; while [ $i -lt 3000 ]; do printf '` + leakMarker2 + ` %d\n' $i; i=$((i+1)); done
`
	path := filepath.Join(dir, "pg_dump")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // an executable test script
		t.Fatal(err)
	}
	return path
}

// TestBackupSealed: with a keyring, runBackup encrypts pg_dump's output on
// its way to DIR/<name>.dump.enc and writes the keyring copy next to it.
// The file holds none of the dump's words (leak test), and decrypts to
// exactly what pg_dump wrote. A failing pg_dump leaves neither a dump nor
// a keyring copy.
func TestBackupSealed(t *testing.T) {
	dataDir := t.TempDir()
	ring, kek := testKeyring(t, dataDir)
	if err := ring.Open(kek); err != nil {
		t.Fatal(err)
	}
	pgDump := fakePgDumpMarkers(t)
	want, err := exec.Command(pgDump).Output()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "backups")
	job := backupJob{
		PgDump: testPgTool(t, pgDump, "postgres://u:p@h/db", 18), Dir: dir, Name: "mailarchive-20261104T120000Z.dump",
		Note: "the note\n", Seal: ring,
	}
	path, err := runBackup(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "mailarchive-20261104T120000Z.dump.enc" {
		t.Errorf("dump path %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertNoMarkers(t, "the .dump.enc", data)
	got, err := unsealBytes(t, data, ring)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("decrypted dump: %v, %d bytes, want %d", err, len(got), len(want))
	}
	copyPath := filepath.Join(dir, "mailarchive-20261104T120000Z.keyring")
	copyData, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	copyRing, err := blobcrypt.ParseKeyring(copyData)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyRing.Open(kek); err != nil {
		t.Fatal(err)
	}
	if got, err := unsealBytes(t, data, copyRing); err != nil || !bytes.Equal(got, want) {
		t.Errorf("the keyring copy does not open the dump: %v", err)
	}
	for _, p := range []string{path, copyPath} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", p, fi, err)
		}
	}

	if err := os.WriteFile(filepath.Join(filepath.Dir(pgDump), "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	job.Name = "second.dump"
	if _, err := runBackup(context.Background(), job); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("failing pg_dump: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "second*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

// TestBackupNoteSealed: the note of an encrypted dump names the .dump.enc,
// the keyring copy, the slots' fingerprints and restore-dump, and never a
// key.
func TestBackupNoteSealed(t *testing.T) {
	ring, kek := testKeyring(t, t.TempDir())
	if err := ring.Open(kek); err != nil {
		t.Fatal(err)
	}
	rk, _ := blobcrypt.GenerateKEK()
	if err := ring.AddSlot(rk, labelRecoveryKey, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, compose := range []bool{false, true} {
		note := formatBackupNote(backupNoteData{
			Time: time.Now(), Version: "x", Migration: "m", Dump: sealedName("mailarchive-T.dump", ring), DataDir: "/srv/data",
			Compose: compose, Keyring: keyringCopyName("mailarchive-T.dump"), Slots: ring.Slots(),
			ArchiveID: ring.ArchiveID(), DataKey: ring.CurrentKey(), Configured: blobcrypt.Fingerprint(kek),
		})
		for _, want := range []string{"Database dump: mailarchive-T.dump.enc\n", "Keyring copy:  mailarchive-T.keyring\n",
			blobcrypt.Fingerprint(kek) + "  archive key (the configured archive key)", blobcrypt.Fingerprint(rk) + "  recovery key",
			"restore-dump ", config.EnvArchiveKeyFile, "four parts"} {
			if !strings.Contains(note, want) {
				t.Errorf("compose %v: note lacks %q:\n%s", compose, want, note)
			}
		}
		if strings.Contains(note, "--plaintext") {
			t.Errorf("compose %v: the note of an encrypted dump names --plaintext:\n%s", compose, note)
		}
		for _, key := range [][]byte{kek, rk} {
			if strings.Contains(note, blobcrypt.EncodeKEK(key)) {
				t.Fatal("the note contains a key")
			}
		}
	}
}

// TestSealUnsealStream: seal-stream and unseal-stream round trip with the
// data directory's keyring; unseal-stream also finds the data key in the
// keyring copy ./ma passes; a cut stream fails before its end, having
// written only a prefix; an unknown data key names --keyring.
func TestSealUnsealStream(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(config.EnvDataDir, dataDir)
	ring, kek := testKeyring(t, dataDir)
	plain := bytes.Repeat([]byte(leakMarker1+" "), 20000) // several chunks

	var sealed bytes.Buffer
	if err := sealStream(bytes.NewReader(plain), &sealed, ""); err != nil {
		t.Fatal(err)
	}
	assertNoMarkers(t, "seal-stream's output", sealed.Bytes())
	var out bytes.Buffer
	r, err := openBackupStream(bytes.NewReader(sealed.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&out, r); err != nil || !bytes.Equal(out.Bytes(), plain) {
		t.Fatalf("round trip: %v", err)
	}

	// Cut at a chunk boundary: an error, and only a prefix came out.
	cut := sealed.Bytes()[:blobcrypt.HeaderSize+2*(blobcrypt.ChunkSize+blobcrypt.TagSize)]
	out.Reset()
	r, err = openBackupStream(bytes.NewReader(cut), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&out, r); !errors.Is(err, blobcrypt.ErrCorrupt) || !bytes.HasPrefix(plain, out.Bytes()) {
		t.Errorf("cut stream: %v, %d bytes out", err, out.Len())
	}

	// Empty input is refused.
	if err := sealStream(strings.NewReader(""), io.Discard, ""); err == nil {
		t.Error("empty input sealed")
	}

	// The data directory's keyring is replaced (another archive's, same
	// key): the dump opens only with the copy next to it.
	if err := ring.Open(kek); err != nil {
		t.Fatal(err)
	}
	copyData, err := ring.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	other, err := blobcrypt.NewKeyring(kek, labelArchiveKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(dataDir), other); err != nil {
		t.Fatal(err)
	}
	if _, err := openBackupStream(bytes.NewReader(sealed.Bytes()), ""); !errors.Is(err, blobcrypt.ErrUnknownKey) || !strings.Contains(err.Error(), "--keyring") {
		t.Errorf("without the copy: %v", err)
	}
	copyPath := filepath.Join(t.TempDir(), "mailarchive-T.keyring")
	if err := os.WriteFile(copyPath, copyData, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func() string{
		"--keyring": func() string { return copyPath },
		envKeyringCopy: func() string {
			t.Setenv(envKeyringCopy, base64.StdEncoding.EncodeToString(copyData))
			return ""
		},
	} {
		r, err := openBackupStream(bytes.NewReader(sealed.Bytes()), setup())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, plain) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// seal-stream seals with the copy ./ma passes, not the data
	// directory's keyring.
	t.Setenv(envKeyringCopy, base64.StdEncoding.EncodeToString(copyData))
	sealed.Reset()
	if err := sealStream(bytes.NewReader(plain), &sealed, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := unsealBytes(t, sealed.Bytes(), ring); err != nil || !bytes.Equal(got, plain) {
		t.Errorf("sealed with the copy: %v", err)
	}
}

// fakePgRestore is a pg_restore that copies its standard input to
// logDir/stdin and writes logDir/done only after reading it to the end. It
// records its environment in logDir/version-env for --version and in
// logDir/env for the restore.
func fakePgRestore(t *testing.T, version string) (path, logDir string) {
	t.Helper()
	dir := t.TempDir()
	logDir = t.TempDir()
	script := `#!/bin/sh
if [ "$1" = --version ]; then env > "` + logDir + `/version-env"; echo "pg_restore (PostgreSQL) ` + version + `"; exit 0; fi
printf '%s\n' "$@" > "` + logDir + `/args"
env > "` + logDir + `/env"
cat > "` + logDir + `/stdin"
echo done > "` + logDir + `/done"
`
	path = filepath.Join(dir, "pg_restore")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // an executable test script
		t.Fatal(err)
	}
	return path, logDir
}

// TestRunRestore: pg_restore gets the dump in one transaction; a damaged
// encrypted dump stops pg_restore before it reads the end of its input, so
// that it cannot commit; an old pg_restore is refused.
func TestRunRestore(t *testing.T) {
	dk := make([]byte, blobcrypt.KeySize)
	plain := bytes.Repeat([]byte("PGDMP restore "), 30000)
	var sealed bytes.Buffer
	w, err := blobcrypt.NewWriter(&sealed, 5, dk, blobcrypt.PurposeBackup)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(plain)
	if err := w.Finish(nil); err != nil {
		t.Fatal(err)
	}
	open := func(data []byte) io.Reader {
		r, err := blobcrypt.NewStreamReader(bytes.NewReader(data), staticKeys{5: dk}, blobcrypt.PurposeBackup, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	basics := setEnvMarkers(t)
	pgRestore, logDir := fakePgRestore(t, "18.0")
	job := restoreJob{PgRestore: testPgTool(t, pgRestore, "postgres://u:p@h/archive", 18), In: open(sealed.Bytes()), Name: "x.dump.enc", Sealed: true}
	var out bytes.Buffer
	if err := runRestore(context.Background(), job, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(logDir, "stdin")); !bytes.Equal(got, plain) {
		t.Errorf("pg_restore read %d bytes, want %d", len(got), len(plain))
	}
	args, _ := os.ReadFile(filepath.Join(logDir, "args"))
	for _, want := range []string{"--no-owner", "--single-transaction", "--exit-on-error", "--dbname=dbname='archive'"} {
		if !strings.Contains(string(args), want+"\n") {
			t.Errorf("args lack %s:\n%s", want, args)
		}
	}
	if !strings.Contains(out.String(), "restored the encrypted dump x.dump.enc") {
		t.Errorf("output %q", out.String())
	}
	for _, name := range []string{"version-env", "env"} {
		assertPgToolEnv(t, "pg_restore "+name, filepath.Join(logDir, name), append(basics, "PGPASSWORD=p", "PGDATABASE=archive")...)
	}

	// Cut at a chunk boundary: everything before the cut reaches
	// pg_restore, but not the end of its input.
	_ = os.Remove(filepath.Join(logDir, "done"))
	job.In = open(sealed.Bytes()[:blobcrypt.HeaderSize+3*(blobcrypt.ChunkSize+blobcrypt.TagSize)])
	if err := runRestore(context.Background(), job, io.Discard); !errors.Is(err, blobcrypt.ErrCorrupt) || !strings.Contains(err.Error(), "database is unchanged") {
		t.Errorf("cut dump: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "done")); !errors.Is(err, os.ErrNotExist) {
		t.Error("pg_restore saw the end of a cut dump")
	}

	old, oldLog := fakePgRestore(t, "16.4")
	conn, err := resolvePgToolConn("pg_restore", "postgres://u:p@h/archive", os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkPgTool(context.Background(), "pg_restore", old, conn, 18); err == nil || !strings.Contains(err.Error(), "version 16, the server 18") {
		t.Errorf("old pg_restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldLog, "args")); !errors.Is(err, os.ErrNotExist) {
		t.Error("old pg_restore ran")
	}
}

type staticKeys map[uint32][]byte

func (k staticKeys) DataKey(id uint32) ([]byte, bool) { d, ok := k[id]; return d, ok }

// TestBackupCommandRefusesPlaintext: with encryption on and no archive key,
// backup refuses before pg_dump runs; --note-only names the .dump.enc and
// --keyring-copy prints the keyring once the key is there.
func TestBackupCommandRefusesPlaintext(t *testing.T) {
	s := newArchiveSetup(t)
	secret, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, secret)
	ring, kek := testKeyring(t, s.dataDir)
	turnOn(t, s.st, ring)
	t.Setenv(config.EnvArchiveKey, "")
	pgDump := fakePgDumpMarkers(t)
	t.Setenv("PATH", filepath.Dir(pgDump)+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := filepath.Join(t.TempDir(), "b")
	if err := runCmd(t, newBackupCmd(), "", dir); err == nil || !strings.Contains(err.Error(), "never written in plaintext") {
		t.Errorf("backup without the key: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Error("the backup directory was created")
	}

	t.Setenv(config.EnvArchiveKey, blobcrypt.EncodeKEK(kek))
	note := captureStdout(t, func() {
		if err := runCmd(t, newBackupCmd(), "", "--note-only", "mailarchive-T.dump"); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(note, "Database dump: mailarchive-T.dump.enc\n") || !strings.Contains(note, "Keyring copy:  mailarchive-T.keyring\n") {
		t.Errorf("note:\n%s", note)
	}
	var copyOut bytes.Buffer
	cmd := newBackupCmd()
	cmd.SetOut(&copyOut)
	if err := runCmd(t, cmd, "", "--keyring-copy"); err != nil {
		t.Fatal(err)
	}
	if got, err := blobcrypt.ParseKeyring(copyOut.Bytes()); err != nil || got.ArchiveID() != ring.ArchiveID() {
		t.Errorf("keyring copy: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runCmd(t, newBackupCmd(), "", dir); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, ".dump.enc (encrypted)") {
		t.Errorf("backup output:\n%s", out)
	}
	dumps, _ := filepath.Glob(filepath.Join(dir, "mailarchive-*.dump.enc"))
	copies, _ := filepath.Glob(filepath.Join(dir, "mailarchive-*.keyring"))
	if len(dumps) != 1 || len(copies) != 1 {
		t.Fatalf("backup wrote %v %v", dumps, copies)
	}
	data, _ := os.ReadFile(dumps[0])
	assertNoMarkers(t, dumps[0], data)
}

// restoreEnv is a migrated database with encryption at rest on and a
// message whose subject holds the marker words, and the tools for a real
// backup and restore. It skips without pg_dump and pg_restore of the
// server's version.
func restoreEnv(t *testing.T) (*archiveSetup, *blobcrypt.Keyring, []byte) {
	t.Helper()
	s := restoreEnvPlain(t)
	ring, kek := testKeyring(t, s.dataDir)
	turnOn(t, s.st, ring)
	return s, ring, kek
}

// restoreEnvPlain is restoreEnv on an archive that never used encryption
// at rest: no archive key, no keyring.
func restoreEnvPlain(t *testing.T) *archiveSetup {
	t.Helper()
	s := newArchiveSetup(t)
	secret, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, secret)
	major, err := s.st.ServerMajorVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not in PATH", tool)
		}
		v, err := exec.Command(path, "--version").Output()
		if err != nil {
			t.Fatal(err)
		}
		if m, err := pgDumpMajor(string(v)); err != nil || m < major {
			t.Skipf("%s is older than the server (%s)", tool, strings.TrimSpace(string(v)))
		}
	}
	if _, err := s.st.CreateUser(context.Background(), leakMarker1, "h", false); err != nil {
		t.Fatal(err)
	}
	return s
}

func runRestoreDump(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newRestoreDumpCmd()
	cmd.SetOut(&out)
	err := runCmd(t, cmd, "", args...)
	return out.String(), err
}

// switchDatabase points the commands at a new, empty database.
func switchDatabase(t *testing.T) (*store.Store, *pgx.Conn) {
	t.Helper()
	url := storetest.NewUnmigrated(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	st, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return st, conn
}

// TestBackupRestoreRoundTrip: backup with real pg_dump writes an encrypted
// dump without the marker words; restore-dump loads it into a fresh
// database, and encryption at rest and the users are back. A recovery key
// added after the backup is in the newer keyring file, which wins over
// the dump's copy: restore-dump writes it to the database and says so
// (revision 1, B2).
func TestBackupRestoreRoundTrip(t *testing.T) {
	_, ring, kek := restoreEnv(t)
	dir := filepath.Join(t.TempDir(), "backups")
	captureStdout(t, func() {
		if err := runCmd(t, newBackupCmd(), "", dir); err != nil {
			t.Fatal(err)
		}
	})
	dumps, _ := filepath.Glob(filepath.Join(dir, "*.dump.enc"))
	if len(dumps) != 1 {
		t.Fatalf("dumps %v", dumps)
	}
	data, err := os.ReadFile(dumps[0])
	if err != nil {
		t.Fatal(err)
	}
	assertNoMarkers(t, "the .dump.enc", data)
	// The control: pg_dump compresses the rows, but its header and the
	// schema are plaintext bytes of the dump; in the encrypted file they
	// are not.
	plain, err := unsealBytes(t, data, ring)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"PGDMP", "CREATE TABLE public.users"} {
		if !bytes.Contains(plain, []byte(m)) {
			t.Errorf("control: the decrypted dump lacks %q", m)
		}
		if bytes.Contains(data, []byte(m)) {
			t.Errorf("the .dump.enc contains %q in plaintext", m)
		}
	}

	// After the backup: a recovery key, in the keyring file only.
	out, err := runEncryption(t, "add-key")
	if err != nil {
		t.Fatalf("add-key: %v\n%s", err, out)
	}
	file, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(config.DataDirFromEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if file.Generation() <= ring.Generation() {
		t.Fatalf("add-key left generation %d", file.Generation())
	}

	st, _ := switchDatabase(t)
	out, err = runRestoreDump(t, dumps[0])
	if err != nil {
		t.Fatalf("restore-dump: %v\n%s", err, out)
	}
	for _, want := range []string{
		"restored the encrypted dump",
		"restored database: encryption at rest on, keyring copy generation 1",
		"data directory: keyring file generation 2",
		"state: on, as in the dump",
		"keyring: the keyring file is newer than the dump's copy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := st.GetUserByName(context.Background(), leakMarker1); err != nil {
		t.Errorf("restored user: %v", err)
	}
	row, err := st.BlobEncryption(context.Background())
	if err != nil || row == nil || row.State != store.BlobOn {
		t.Fatalf("restored state %+v %v", row, err)
	}
	dbRing, err := blobcrypt.ParseKeyring(row.Keyring)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbRing.Open(kek); err != nil {
		t.Fatal(err)
	}
	if len(dbRing.Slots()) != 2 || dbRing.Generation() != file.Generation() {
		t.Errorf("the database copy lost the recovery key: %d slots, generation %d", len(dbRing.Slots()), dbRing.Generation())
	}

	// Again: the copies agree now.
	if out, err := runRestoreDump(t, "--reconcile-only"); err != nil || !strings.Contains(out, "agree") {
		t.Errorf("reconcile again: %v\n%s", err, out)
	}
}

// TestRestoreDumpRefusals: restore-dump refuses while a writer runs, and
// a damaged encrypted dump leaves the database as it was.
func TestRestoreDumpRefusals(t *testing.T) {
	s, _, _ := restoreEnv(t)
	dir := filepath.Join(t.TempDir(), "backups")
	captureStdout(t, func() {
		if err := runCmd(t, newBackupCmd(), "", dir); err != nil {
			t.Fatal(err)
		}
	})
	dumps, _ := filepath.Glob(filepath.Join(dir, "*.dump.enc"))
	if len(dumps) != 1 {
		t.Fatalf("dumps %v", dumps)
	}

	lock, err := s.st.HoldWriters(context.Background(), "serve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runRestoreDump(t, dumps[0]); !errors.Is(err, store.ErrWritersRunning) {
		t.Errorf("while serve runs: %v", err)
	}
	lock.Release()

	// verify, a sweep, a purge or a retention run holds the blob lock.
	unlock, ok, err := s.st.TryLockBlobs(context.Background())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	for _, args := range [][]string{{dumps[0]}, holdArgs("sealed", "x.dump.enc"), {"--reconcile-only"}} {
		if _, err := runRestoreDump(t, args...); !errors.Is(err, archive.ErrBlobLockHeld) {
			t.Errorf("%v with the blob lock held: %v", args, err)
		}
	}
	unlock()

	data, err := os.ReadFile(dumps[0])
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(dir, "cut.dump.enc")
	if err := os.WriteFile(cut, data[:len(data)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, keyringCopyName(filepath.Base(dumps[0]))), filepath.Join(dir, "cut.keyring")); err != nil {
		t.Fatal(err)
	}
	_, conn := switchDatabase(t)
	if _, err := runRestoreDump(t, cut); !errors.Is(err, blobcrypt.ErrCorrupt) {
		t.Errorf("cut dump: %v", err)
	}
	var tables int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("a cut dump left %d tables", tables)
	}
}

// TestExportWarnsInsideDataDir: with encryption at rest, an mbox export
// into the data directory warns that it is plaintext; one outside does
// not.
func TestExportWarnsInsideDataDir(t *testing.T) {
	s := newArchiveSetup(t)
	testKeyring(t, s.dataDir)
	run := func(out string) string {
		var stderr bytes.Buffer
		cmd := newExportCmd()
		cmd.SetErr(&stderr)
		captureStdout(t, func() {
			if err := runCmd(t, cmd, "", "--format", "mbox", "--out", out, "--user", "anna"); err != nil {
				t.Error(err)
			}
		})
		return stderr.String()
	}
	if got := run(filepath.Join(s.dataDir, "export", "anna")); !strings.Contains(got, "the export is not encrypted") {
		t.Errorf("inside the data directory: %q", got)
	}
	if got := run(filepath.Join(t.TempDir(), "anna")); strings.Contains(got, "not encrypted") {
		t.Errorf("outside the data directory: %q", got)
	}
}

// TestRestoreDumpIntoNonEmptyDatabase: a dump is restored only into an
// empty database. Over an existing one, pg_restore --clean cannot drop the
// partitions of audit_log (their inherited constraints), so restore-dump
// refuses before pg_restore runs, names how to get an empty database, and
// changes nothing (CI fix of #518).
func TestRestoreDumpIntoNonEmptyDatabase(t *testing.T) {
	s, _, _ := restoreEnv(t)
	dir := filepath.Join(t.TempDir(), "backups")
	captureStdout(t, func() {
		if err := runCmd(t, newBackupCmd(), "", dir); err != nil {
			t.Fatal(err)
		}
	})
	dumps, _ := filepath.Glob(filepath.Join(dir, "*.dump.enc"))
	if len(dumps) != 1 {
		t.Fatalf("dumps %v", dumps)
	}
	if _, err := s.st.CreateUser(context.Background(), "after-the-backup", "h", false); err != nil {
		t.Fatal(err)
	}
	out, err := runRestoreDump(t, dumps[0])
	if err == nil || !strings.Contains(err.Error(), "not empty") || !strings.Contains(err.Error(), "createdb") {
		t.Fatalf("restore over an existing database: %v\n%s", err, out)
	}
	if _, err := s.st.GetUserByName(context.Background(), "after-the-backup"); err != nil {
		t.Errorf("the refused restore changed the database: %v", err)
	}
	// --hold, which ./ma runs around its own pg_restore, refuses too.
	if _, err := runRestoreDump(t, holdArgs("sealed", "x.dump.enc")...); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("--hold on an existing database: %v", err)
	}
}

// holdArgs are the arguments ./ma gives the holder for a dump whose
// header it read as header (sealed or plain), plus extra.
func holdArgs(header, name string, extra ...string) []string {
	return append([]string{"--hold", "--dump-header", header, "--dump-name", name}, extra...)
}

// startHold runs restore-dump --hold as ./ma does for an encrypted dump,
// with its input and output as pipes, and waits for "ready".
func startHold(t *testing.T) (in io.WriteCloser, out *bufio.Reader, done <-chan error) {
	t.Helper()
	return startHoldWith(t, io.Discard, holdArgs("sealed", "mailarchive-T.dump.enc")...)
}

// startHoldWith is startHold with the holder's arguments and its standard
// error on errOut. It fails the test unless the first line of the
// holder's standard output is exactly "ready", as ./ma requires.
func startHoldWith(t *testing.T, errOut io.Writer, args ...string) (in io.WriteCloser, out *bufio.Reader, done <-chan error) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	cmd := newRestoreDumpCmd()
	cmd.SetArgs(args)
	cmd.SetIn(inR)
	cmd.SetOut(outW)
	cmd.SetErr(errOut)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	t.Cleanup(cancel)
	ch := make(chan error, 1)
	go func() {
		err := cmd.ExecuteContext(ctx)
		_ = outW.CloseWithError(io.EOF)
		ch <- err
	}()
	out = bufio.NewReader(outR)
	line, err := out.ReadString('\n')
	if err != nil || line != holdReady+"\n" {
		// Unblock a holder that still writes or waits for its input
		// before waiting for it.
		_ = outR.CloseWithError(io.ErrClosedPipe)
		_ = inW.Close()
		t.Fatalf("--hold said %q, %v; then %v", line, err, <-ch)
	}
	return inW, out, ch
}

// TestRestoreDumpHold: ./ma restore-dump keeps the writers lock from its
// checks to the end of the reconciliation (review round 1 of #518, the
// lock gap): while --hold waits for pg_restore, no writer starts; after
// "restored" it reconciles, then releases the lock. Without "restored" it
// reconciles nothing.
func TestRestoreDumpHold(t *testing.T) {
	_, ring, _ := restoreEnv(t)
	st, _ := switchDatabase(t)
	ctx := context.Background()

	in, out, done := startHold(t)
	if _, err := st.HoldWriters(ctx, "sync"); !errors.Is(err, store.ErrWritersExcluded) || !strings.Contains(err.Error(), "restore-dump") {
		t.Fatalf("a writer during the restore: %v", err)
	}
	// What ./ma's pg_restore would do: the schema and the dump's state.
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	turnOn(t, st, ring)
	if _, err := io.WriteString(in, holdRestored+"\n"); err != nil {
		t.Fatal(err)
	}
	_ = in.Close()
	rest, _ := io.ReadAll(out)
	if err := <-done; err != nil {
		t.Fatalf("--hold: %v\n%s", err, rest)
	}
	if !strings.Contains(string(rest), "state: on, as in the dump") {
		t.Errorf("no reconciliation:\n%s", rest)
	}
	lock, err := st.HoldWriters(ctx, "sync")
	if err != nil {
		t.Fatalf("a writer after the restore: %v", err)
	}
	lock.Release()

	// A failed restore: the input ends without "restored".
	switchDatabase(t)
	in, out, done = startHold(t)
	_ = in.Close()
	rest, _ = io.ReadAll(out)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "did not finish") || len(rest) != 0 {
		t.Errorf("without restored: %v\n%s", err, rest)
	}

	// ./ma's "failed" ends the holder by itself, while its input stays
	// open: it does not depend on the end of its input reaching it.
	switchDatabase(t)
	in, _, done = startHold(t)
	defer in.Close() //nolint:errcheck // a pipe
	if _, err := io.WriteString(in, "failed\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not finish") {
			t.Errorf("after failed: %v", err)
		}
	case <-time.After(testwait.Limit(t)):
		t.Fatal("--hold still waits for the end of its input after \"failed\"")
	}
}

// TestRestoreDumpForeignKeyring: a keyring copy of another archive than the
// data directory's is refused before pg_restore runs, so the new database
// stays empty (review round 1 of #518), on the Go path and in --hold.
func TestRestoreDumpForeignKeyring(t *testing.T) {
	s, _, kek := restoreEnv(t)
	dir := filepath.Join(t.TempDir(), "backups")
	captureStdout(t, func() {
		if err := runCmd(t, newBackupCmd(), "", dir); err != nil {
			t.Fatal(err)
		}
	})
	dumps, _ := filepath.Glob(filepath.Join(dir, "*.dump.enc"))
	if len(dumps) != 1 {
		t.Fatalf("dumps %v", dumps)
	}
	// The data directory now belongs to another archive (same key).
	other, err := blobcrypt.NewKeyring(kek, labelArchiveKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(s.dataDir), other); err != nil {
		t.Fatal(err)
	}
	_, conn := switchDatabase(t)
	if _, err := runRestoreDump(t, dumps[0]); !errors.Is(err, blobcrypt.ErrOtherArchive) || !strings.Contains(err.Error(), "nothing was restored") {
		t.Errorf("another archive's dump: %v", err)
	}
	var tables int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("the refused restore left %d tables", tables)
	}

	copyData, err := os.ReadFile(filepath.Join(dir, keyringCopyName(filepath.Base(dumps[0]))))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(envKeyringCopy, base64.StdEncoding.EncodeToString(copyData))
	if _, err := runRestoreDump(t, holdArgs("sealed", filepath.Base(dumps[0]))...); !errors.Is(err, blobcrypt.ErrOtherArchive) {
		t.Errorf("--hold with another archive's copy: %v", err)
	}
}

// TestCheckDumpKind: a dump without encryption is refused wherever the
// archive shows a sign of encryption at rest, each sign on its own, unless
// the operator passes --plaintext; --plaintext on an encrypted dump is
// refused; without any sign a dump without encryption passes
// (GHSA-w6jg-ffvw-q4f5).
func TestCheckDumpKind(t *testing.T) {
	key := make([]byte, blobcrypt.KeySize)
	for _, c := range []struct {
		name          string
		facts         dumpFacts
		ring          bool // the data directory has a keyring
		key           bool // an archive key is configured
		refused       error
		wantErr, note []string // substrings; note nil: no note
	}{
		{name: "no sign", facts: dumpFacts{Name: "x.dump"}},
		{name: ".enc name only", facts: dumpFacts{Name: "x.dump.enc"}, refused: errPlaintextDump,
			wantErr: []string{"x.dump.enc has no encryption header", "the file name ends in .enc", "--plaintext", "nothing was restored"}},
		{name: "sibling only", facts: dumpFacts{Name: "x.dump", SiblingKeyring: true}, refused: errPlaintextDump,
			wantErr: []string{"a keyring copy lies next to the dump"}},
		{name: "data-dir keyring only", facts: dumpFacts{Name: "x.dump"}, ring: true, refused: errPlaintextDump,
			wantErr: []string{"the data directory has a keyring", filepath.Join(blobcrypt.KeysDir, blobcrypt.KeyringFile)}},
		{name: "archive key only", facts: dumpFacts{Name: "x.dump"}, key: true, refused: errPlaintextDump,
			wantErr: []string{"an archive key is configured", config.EnvArchiveKeyFile}},
		{name: "all signs, --plaintext", facts: dumpFacts{Name: "x.dump.enc", SiblingKeyring: true, Plaintext: true}, ring: true, key: true,
			note: []string{"restoring an unencrypted dump (--plaintext)", "not authenticated", "archive key", "data directory has a keyring", ".enc", "keyring copy"}},
		{name: "no sign, --plaintext", facts: dumpFacts{Name: "x.dump", Plaintext: true}},
		{name: "sealed, --plaintext", facts: dumpFacts{Name: "x.dump.enc", Sealed: true, SiblingKeyring: true, Plaintext: true}, ring: true, key: true,
			wantErr: []string{"x.dump.enc is encrypted; drop --plaintext"}},
		{name: "sealed", facts: dumpFacts{Name: "x.dump.enc", Sealed: true, SiblingKeyring: true}, ring: true, key: true},
		{name: "sealed, no sign", facts: dumpFacts{Name: "x", Sealed: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dataDir := t.TempDir()
			if c.ring {
				if err := os.MkdirAll(filepath.Dir(blobcrypt.KeyringPath(dataDir)), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(blobcrypt.KeyringPath(dataDir), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var archiveKey []byte
			if c.key {
				archiveKey = key
			}
			note, err := checkDumpKind(c.facts, dataDir, archiveKey)
			if c.wantErr == nil && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if c.wantErr != nil {
				if err == nil {
					t.Fatalf("not refused (note %q)", note)
				}
				if c.refused != nil && !errors.Is(err, c.refused) {
					t.Errorf("error %v, want %v", err, c.refused)
				}
				for _, want := range c.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error lacks %q: %v", want, err)
					}
				}
			}
			if c.note == nil && note != "" {
				t.Errorf("note %q", note)
			}
			for _, want := range c.note {
				if !strings.Contains(note, want) {
					t.Errorf("note lacks %q: %q", want, note)
				}
			}
		})
	}
}

// publicTables counts the tables of the database conn points at.
func publicTables(t *testing.T, conn *pgx.Conn) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRestoreDumpRefusesPlaintextDowngrade: a real pg_dump without
// encryption in place of an encrypted backup is refused by restore-dump
// before pg_restore runs, with every sign of encryption at rest and with
// each sign on its own, so the new database stays empty
// (GHSA-w6jg-ffvw-q4f5). With --plaintext it restores and prints the note
// first; --plaintext on an encrypted dump and flags that do nothing are
// refused.
func TestRestoreDumpRefusesPlaintextDowngrade(t *testing.T) {
	s, ring, kek := restoreEnv(t)
	dir := filepath.Join(t.TempDir(), "backups")
	captureStdout(t, func() {
		if err := runCmd(t, newBackupCmd(), "", dir); err != nil {
			t.Fatal(err)
		}
	})
	dumps, _ := filepath.Glob(filepath.Join(dir, "*.dump.enc"))
	if len(dumps) != 1 {
		t.Fatalf("dumps %v", dumps)
	}
	sealed, err := os.ReadFile(dumps[0])
	if err != nil {
		t.Fatal(err)
	}
	plain, err := unsealBytes(t, sealed, ring)
	if err != nil {
		t.Fatal(err)
	}
	copyData, err := os.ReadFile(filepath.Join(dir, keyringCopyName(filepath.Base(dumps[0]))))
	if err != nil {
		t.Fatal(err)
	}
	ringPath := blobcrypt.KeyringPath(s.dataDir)
	ringData, err := os.ReadFile(ringPath)
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	refused := func(what, file string, signs ...string) {
		t.Helper()
		_, conn := switchDatabase(t)
		out, err := runRestoreDump(t, file)
		if !errors.Is(err, errPlaintextDump) {
			t.Errorf("%s: %v\n%s", what, err, out)
			return
		}
		for _, sign := range signs {
			if !strings.Contains(err.Error(), sign) {
				t.Errorf("%s: the refusal lacks %q: %v", what, sign, err)
			}
		}
		if n := publicTables(t, conn); n != 0 {
			t.Errorf("%s: the refused restore left %d tables", what, n)
		}
	}

	// (a) Over the real .dump.enc, in place: every sign.
	encrypted := write(filepath.Join(t.TempDir(), "real", filepath.Base(dumps[0])), sealed)
	write(filepath.Join(filepath.Dir(encrypted), keyringCopyName(filepath.Base(encrypted))), copyData)
	write(dumps[0], plain)
	refused("in place", dumps[0], "archive key is configured", "data directory has a keyring", "ends in .enc", "keyring copy lies next to")

	// (f) --plaintext on an encrypted dump.
	_, conn := switchDatabase(t)
	if _, err := runRestoreDump(t, "--plaintext", encrypted); err == nil || !strings.Contains(err.Error(), "is encrypted; drop --plaintext") {
		t.Errorf("--plaintext on an encrypted dump: %v", err)
	}
	if n := publicTables(t, conn); n != 0 {
		t.Errorf("--plaintext on an encrypted dump left %d tables", n)
	}

	// (g) Flags that do nothing are refused before any lock is taken:
	// with a writer running, the refusal is the flag's, not the lock's.
	lock, err := s.st.HoldWriters(context.Background(), "serve")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--reconcile-only", "--plaintext"}, "--plaintext has no effect with --reconcile-only"},
		{[]string{"--reconcile-only", "--dump-header", "plain"}, "only for --hold"},
		{[]string{dumps[0], "--dump-name", "x.dump"}, "only for --hold"},
		{[]string{dumps[0], "--dump-header", "plain"}, "only for --hold"},
		{holdArgs("plain", "x.dump", "--keyring", "k"), "--keyring has no effect with --hold"},
		{[]string{"--hold"}, "./ma is older than the image"},
		{[]string{"--hold", "--dump-header", "sealed"}, "./ma is older than the image"},
		{holdArgs("maybe", "x.dump"), `--dump-header "maybe"`},
	} {
		if _, err := runRestoreDump(t, c.args...); err == nil || errors.Is(err, store.ErrWritersRunning) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
	lock.Release()

	// (e) The same file with --plaintext restores, after the note.
	st, _ := switchDatabase(t)
	out, err := runRestoreDump(t, "--plaintext", dumps[0])
	if err != nil {
		t.Fatalf("--plaintext: %v\n%s", err, out)
	}
	note := strings.Index(out, "restoring an unencrypted dump (--plaintext)")
	restored := strings.Index(out, "restored the dump "+dumps[0])
	if note < 0 || restored < 0 || note > restored {
		t.Errorf("--plaintext: the note does not come before the restore:\n%s", out)
	}
	if _, err := st.GetUserByName(context.Background(), leakMarker1); err != nil {
		t.Errorf("--plaintext: restored user: %v", err)
	}

	// One sign at a time.
	// (b) Only a keyring copy next to the dump.
	t.Setenv(config.EnvArchiveKey, "")
	if err := os.Remove(ringPath); err != nil {
		t.Fatal(err)
	}
	only := t.TempDir()
	write(filepath.Join(only, "x.keyring"), copyData)
	refused("sibling only", write(filepath.Join(only, "x.dump"), plain), "keyring copy lies next to")
	// (c) Only the data directory's keyring.
	write(ringPath, ringData)
	refused("data-directory keyring only", write(filepath.Join(t.TempDir(), "y.dump"), plain), "data directory has a keyring")
	// (d) Only the archive key.
	if err := os.Remove(ringPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvArchiveKey, blobcrypt.EncodeKEK(kek))
	refused("archive key only", write(filepath.Join(t.TempDir(), "z.dump"), plain), "archive key is configured")
}

// TestRestoreDumpHoldRefusesPlaintext: the holder ./ma runs refuses a dump
// that ./ma read as without encryption on an encrypted archive before it
// says anything on its standard output, so ./ma never starts pg_restore
// (GHSA-w6jg-ffvw-q4f5). With --plaintext its first line is exactly
// "ready" and the note is on its standard error. --hold without the facts
// of a current ./ma is refused.
func TestRestoreDumpHoldRefusesPlaintext(t *testing.T) {
	restoreEnv(t)
	switchDatabase(t)
	copyData, err := os.ReadFile(blobcrypt.KeyringPath(config.DataDirFromEnv()))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		args []string
		env  bool // ./ma passes a keyring copy next to the dump
	}{
		{"name .dump.enc", holdArgs("plain", "x.dump.enc"), false},
		{"name .dump", holdArgs("plain", "x.dump"), false},
		{"keyring copy next to it", holdArgs("plain", "x.dump"), true},
	} {
		t.Setenv(envKeyringCopy, "")
		if c.env {
			t.Setenv(envKeyringCopy, base64.StdEncoding.EncodeToString(copyData))
		}
		out, err := runRestoreDump(t, c.args...)
		if !errors.Is(err, errPlaintextDump) || out != "" {
			t.Errorf("%s: %v, standard output %q", c.name, err, out)
		}
	}
	t.Setenv(envKeyringCopy, "")
	if out, err := runRestoreDump(t, "--hold"); err == nil || !strings.Contains(err.Error(), "./ma is older than the image") || out != "" {
		t.Errorf("--hold without --dump-header: %v, standard output %q", err, out)
	}

	var stderr bytes.Buffer
	in, out, done := startHoldWith(t, &stderr, holdArgs("plain", "x.dump.enc", "--plaintext")...)
	_ = in.Close()
	rest, _ := io.ReadAll(out)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "did not finish") || len(rest) != 0 {
		t.Errorf("--plaintext, then no restore: %v\n%s", err, rest)
	}
	if !strings.Contains(stderr.String(), "restoring an unencrypted dump (--plaintext)") {
		t.Errorf("--plaintext: standard error %q", stderr.String())
	}
}

// TestRestoreDumpPlaintextArchive: on an archive that never used
// encryption at rest (no archive key, no keyring), the dump backup writes
// restores without --plaintext, and the note names no --plaintext.
func TestRestoreDumpPlaintextArchive(t *testing.T) {
	restoreEnvPlain(t)
	dir := filepath.Join(t.TempDir(), "backups")
	captureStdout(t, func() {
		if err := runCmd(t, newBackupCmd(), "", dir); err != nil {
			t.Fatal(err)
		}
	})
	dumps, _ := filepath.Glob(filepath.Join(dir, "mailarchive-*.dump"))
	if len(dumps) != 1 {
		t.Fatalf("dumps %v", dumps)
	}
	if note, err := os.ReadFile(filepath.Join(dir, backupNoteName)); err != nil || strings.Contains(string(note), "--plaintext") {
		t.Errorf("note: %v\n%s", err, note)
	}
	st, _ := switchDatabase(t)
	out, err := runRestoreDump(t, dumps[0])
	if err != nil {
		t.Fatalf("restore-dump: %v\n%s", err, out)
	}
	if !strings.Contains(out, "restored the dump "+dumps[0]) || strings.Contains(out, "--plaintext") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := st.GetUserByName(context.Background(), leakMarker1); err != nil {
		t.Errorf("restored user: %v", err)
	}
}

// TestBackupNotePlaintext: the note of a dump without encryption on an
// archive with an archive key or a keyring (after encryption disable)
// gives restore-dump --plaintext, the command the restore needs; without
// a sign it does not.
func TestBackupNotePlaintext(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	secret, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, secret)
	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		key, ring bool
		compose   bool
		want      bool
	}{
		{"no sign", false, false, false, false},
		{"no sign, ./ma", false, false, true, false},
		{"archive key", true, false, false, true},
		{"archive key, ./ma", true, false, true, true},
		{"keyring", false, true, false, true},
	} {
		dataDir := t.TempDir()
		t.Setenv(config.EnvDataDir, dataDir)
		t.Setenv(config.EnvArchiveKey, "")
		t.Setenv("MAIL_ARCHIVE_COMMAND", "")
		if c.key {
			t.Setenv(config.EnvArchiveKey, blobcrypt.EncodeKEK(kek))
		}
		if c.ring {
			if err := os.MkdirAll(filepath.Dir(blobcrypt.KeyringPath(dataDir)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(blobcrypt.KeyringPath(dataDir), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if c.compose {
			t.Setenv("MAIL_ARCHIVE_COMMAND", "./ma")
		}
		a, err := openApp(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		note, err := backupNote(context.Background(), a, "mailarchive-x.dump", nil, time.Now())
		a.close()
		if err != nil {
			t.Fatal(err)
		}
		cmd := "mail-archive restore-dump --plaintext mailarchive-x.dump"
		if c.compose {
			cmd = "./ma restore-dump --plaintext <this directory>/mailarchive-x.dump"
		}
		if got := strings.Contains(note, cmd); got != c.want {
			t.Errorf("%s: %q in the note: %v, want %v\n%s", c.name, cmd, got, c.want, note)
		}
		if !c.want && strings.Contains(note, "--plaintext") {
			t.Errorf("%s: the note names --plaintext:\n%s", c.name, note)
		}
	}
}
