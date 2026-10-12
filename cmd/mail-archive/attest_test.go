package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/attest"
)

func runAttestCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newAttestCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := runCmd(t, cmd, "", args...)
	return out.String(), err
}

func TestAttestCommands(t *testing.T) {
	s := newArchiveSetup(t)
	dir := filepath.Join(s.dataDir, "attest")

	if _, err := runAttestCmd(t); err == nil || !strings.Contains(err.Error(), "attest keygen") {
		t.Fatalf("attest without key: %v", err)
	}
	if _, err := runAttestCmd(t, "public-key"); err == nil || !strings.Contains(err.Error(), "attest keygen") {
		t.Fatalf("public-key without key: %v", err)
	}

	out, err := runAttestCmd(t, "keygen")
	if err != nil || !strings.Contains(out, "public key ") || !strings.Contains(out, "off this host") {
		t.Fatalf("keygen: %v\n%s", err, out)
	}
	pub, err := attest.LoadPublicKey(filepath.Join(dir, attest.PublicKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, attest.EncodeKey(pub)) {
		t.Errorf("keygen does not print the public key:\n%s", out)
	}
	if _, err := runAttestCmd(t, "keygen"); err == nil || !strings.Contains(err.Error(), "--rotate") {
		t.Errorf("second keygen: %v", err)
	}

	out, err = runAttestCmd(t)
	if err != nil || !strings.Contains(out, "attestation #1: 2 new message(s), 0 deletion(s), 2 message(s) in total") {
		t.Fatalf("attest: %v\n%s", err, out)
	}
	out, err = runAttestCmd(t, "public-key")
	if err != nil || !strings.Contains(out, "BEGIN PUBLIC KEY") || !strings.Contains(out, attest.EncodeKey(pub)) {
		t.Fatalf("public-key: %v\n%s", err, out)
	}

	out, err = runAttestCmd(t, "keygen", "--rotate")
	if err != nil || !strings.Contains(out, "retired key "+attest.EncodeKey(pub)) {
		t.Fatalf("rotate: %v\n%s", err, out)
	}
	if out, err = runAttestCmd(t); err != nil || !strings.Contains(out, "attestation #2: 0 new message(s)") {
		t.Fatalf("attest after rotate: %v\n%s", err, out)
	}

	// A key file the group can read is refused, with the fix.
	keyFile := filepath.Join(dir, attest.DefaultKeyFile)
	if err := os.Chmod(keyFile, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := runAttestCmd(t); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("readable key: %v", err)
	}
}

func TestStartAttester(t *testing.T) {
	newArchiveSetup(t)
	ctx := context.Background()
	a, err := openApp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	// No key: serve starts without attestations.
	startAttester(ctx, a, log)()
	if !strings.Contains(logs.String(), "attestations off") || !strings.Contains(logs.String(), "attest keygen") {
		t.Fatalf("no key:\n%s", logs.String())
	}

	if _, err := attest.Keygen(a.cfg.AttestKeyFile, a.cfg.AttestDir()); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	a.cfg.AttestInterval = time.Hour
	stop := startAttester(ctx, a, log)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if n, _ := a.store.CountAttestations(ctx); n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no attestation written:\n%s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	if !strings.Contains(logs.String(), "attestations on") {
		t.Errorf("on:\n%s", logs.String())
	}

	// The key is gone while attestations exist: an error, still no failure.
	if err := os.Remove(a.cfg.AttestKeyFile); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	startAttester(ctx, a, log)()
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "attestations exist") {
		t.Errorf("lost key:\n%s", logs.String())
	}
	if _, err := os.Stat(filepath.Join(a.cfg.AttestDir(), attest.ChainFile)); err != nil {
		t.Error(err)
	}
}

func TestAttestCheckAndVerify(t *testing.T) {
	s := newArchiveSetup(t)
	dir := filepath.Join(s.dataDir, "attest")
	ctx := context.Background()

	// No key, no attestations: verify works as before.
	code, out := runVerifyCmd(t)
	if code != 0 || !strings.Contains(out, "note: tamper evidence off") || !strings.Contains(out, "checked 2 message(s): no problems") {
		t.Fatalf("verify without a key: exit %d\n%s", code, out)
	}
	status := captureStdout(t, func() {
		if err := runCmd(t, newStatusCmd(), ""); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(status, "attestations: off (no key at "+filepath.Join(dir, attest.DefaultKeyFile)) {
		t.Errorf("status without a key:\n%s", status)
	}

	for _, args := range [][]string{{"keygen"}, nil} {
		if out, err := runAttestCmd(t, args...); err != nil {
			t.Fatalf("attest %v: %v\n%s", args, err, out)
		}
	}
	status = captureStdout(t, func() { _ = runCmd(t, newStatusCmd(), "") })
	if !regexpMatch(`attestations: #1 at \d{4}-\d\d-\d\d \d\d:\d\d, 2 messages`, status) {
		t.Errorf("status:\n%s", status)
	}
	if code, out := runVerifyCmd(t); code != 0 || !strings.Contains(out, "checked 2 message(s) and 1 attestation(s): no problems") {
		t.Fatalf("verify: exit %d\n%s", code, out)
	}
	out, err := runAttestCmd(t, "check")
	if err != nil || !strings.Contains(out, "checked 1 attestation(s): no problems") {
		t.Fatalf("attest check: %v\n%s", err, out)
	}

	// An off-host copy of the directory.
	copyDir := t.TempDir()
	for _, f := range []string{attest.ChainFile, attest.PublicKeyFile, filepath.Join(attest.LeavesDir, "1.txt")} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		_ = os.MkdirAll(filepath.Dir(filepath.Join(copyDir, f)), 0o700)
		if err := os.WriteFile(filepath.Join(copyDir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	copyFile, copyPub := filepath.Join(copyDir, attest.ChainFile), filepath.Join(copyDir, attest.PublicKeyFile)
	if out, err := runAttestCmd(t, "check", "--file", copyFile, "--public-key", copyPub); err != nil ||
		!strings.Contains(out, "no problems") {
		t.Fatalf("check --file: %v\n%s", err, out)
	}
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", "postgres://nobody@127.0.0.1:1/none")
	if out, err := runAttestCmd(t, "check", "--offline", "--file", copyFile); err != nil ||
		!strings.Contains(out, "checked 1 attestation(s) offline: no problems") {
		t.Fatalf("check --offline: %v\n%s", err, out)
	}
	_ = os.Remove(filepath.Join(copyDir, attest.LeavesDir, "1.txt"))
	if out, err := runAttestCmd(t, "check", "--offline", "--file", copyFile); exitCodeOf(err) != 1 || !strings.Contains(out, "missing") {
		t.Fatalf("check --offline without leaves: %v\n%s", err, out)
	}
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", s.url)

	// Another key than the one that signed: findings, exit 1.
	otherDir := t.TempDir()
	if _, err := attest.Keygen(filepath.Join(otherDir, "k"), otherDir); err != nil {
		t.Fatal(err)
	}
	otherPub := filepath.Join(otherDir, attest.PublicKeyFile)
	if out, err := runAttestCmd(t, "check", "--public-key", otherPub); exitCodeOf(err) != 1 || !strings.Contains(out, "chain") {
		t.Fatalf("check with another key: %v\n%s", err, out)
	}

	// A removed message: verify names it, exit 1.
	c, err := pgx.Connect(ctx, s.url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(ctx) }()
	var sha string
	if err := c.QueryRow(ctx, "SELECT sha256 FROM messages ORDER BY seq LIMIT 1").Scan(&sha); err != nil {
		t.Fatal(err)
	}
	// As someone with owner rights on the database, past the guards of
	// retention.
	for _, sql := range []string{
		"ALTER TABLE message_locations DISABLE TRIGGER message_locations_retention_guard",
		"ALTER TABLE messages DISABLE TRIGGER messages_tombstone_guard",
	} {
		if _, err := c.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for _, sql := range []string{
		"DELETE FROM message_locations WHERE message_sha256 = $1",
		"DELETE FROM messages WHERE sha256 = $1",
	} {
		if _, err := c.Exec(ctx, sql, sha); err != nil {
			t.Fatal(err)
		}
	}
	code, out = runVerifyCmd(t)
	if code != 1 || !strings.Contains(out, "removed    "+sha+" (attested by #1 on ") ||
		!strings.Contains(out, "1 removed") {
		t.Fatalf("verify after a removal: exit %d\n%s", code, out)
	}
}

func TestBackupWritesAttestation(t *testing.T) {
	newArchiveSetup(t)
	if out, err := runAttestCmd(t, "keygen"); err != nil {
		t.Fatalf("keygen: %v\n%s", err, out)
	}
	cmd := newBackupCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	note := captureStdout(t, func() {
		if err := runCmd(t, cmd, "", "--note-only", "mailarchive-x.dump"); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr.String(), "attestation #1 written before the dump") {
		t.Errorf("stderr: %s", stderr.String())
	}
	for _, want := range []string{`then "attest" before anything archives`, "Attestations (tamper evidence)", "copies kept off this host"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
}
