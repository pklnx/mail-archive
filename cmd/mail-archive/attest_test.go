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
