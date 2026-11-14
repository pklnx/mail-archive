package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/crypto"
	"github.com/keepia/archive/internal/store"
)

func runEncryption(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newEncryptionCmd()
	cmd.SetOut(&out)
	err := runCmd(t, cmd, "", args...)
	return out.String(), err
}

func statusLine(t *testing.T) string {
	t.Helper()
	out := captureStdout(t, func() {
		if err := runCmd(t, newStatusCmd(), ""); err != nil {
			t.Error(err)
		}
	})
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "encryption at rest:") {
			return line
		}
	}
	t.Fatalf("status without an encryption line:\n%s", out)
	return ""
}

// encryption enable and disable: they refuse while a writer runs, convert
// the files, and `status` shows an unfinished change with the command that
// finishes it (#392, part 3).
func TestEncryptionEnableDisableCommands(t *testing.T) {
	st, _ := jobsEnv(t)
	secret, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, secret)
	ctx := context.Background()
	dataDir := config.DataDirFromEnv()
	blobs, err := blobstore.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	raw := "Subject: Befehl\r\n\r\nGeheimnis\r\n"
	b, _, err := blobs.Put(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := blobcrypt.NewKeyring(kek, "archive key", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(dataDir), ring); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvArchiveKey, blobcrypt.EncodeKEK(kek))
	if line := statusLine(t); line != "encryption at rest: off" {
		t.Errorf("status before: %q", line)
	}

	lock, err := st.HoldWriters(ctx, "serve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runEncryption(t, "enable"); !errors.Is(err, store.ErrWritersRunning) || !strings.Contains(err.Error(), "mail-archive writer serve") {
		t.Errorf("enable while serve runs: %v", err)
	}
	lock.Release()

	// An unfinished change, as an interrupted enable leaves it.
	data, err := ring.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetBlobEncryption(ctx, store.BlobEncrypting, store.KeyringCopy{
		Keyring: data, ID: ring.ArchiveID(), Generation: int64(ring.Generation()), CurrentKey: ring.CurrentKey(),
	}); err != nil {
		t.Fatal(err)
	}
	if line := statusLine(t); !strings.Contains(line, "encrypting, unfinished") || !strings.Contains(line, "encryption enable` to finish it") {
		t.Errorf("status during the change: %q", line)
	}
	out, err := runEncryption(t, "enable")
	if err != nil || !strings.Contains(out, "encrypting: 100%") || !strings.Contains(out, "encryption at rest is on") {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	if blobs.HasPlaintext(b.SHA256) {
		t.Error("the plaintext file stayed")
	}
	if line := statusLine(t); line != "encryption at rest: on" {
		t.Errorf("status after: %q", line)
	}
	if out, err := runEncryption(t, "enable"); err != nil || !strings.Contains(out, "already on") {
		t.Errorf("enable again: %v\n%s", err, out)
	}
	if out, err := runEncryption(t, "disable"); err != nil || !strings.Contains(out, "encryption at rest is off") {
		t.Fatalf("disable: %v\n%s", err, out)
	}
	if !blobs.HasPlaintext(b.SHA256) {
		t.Error("not decrypted")
	}
	if line := statusLine(t); line != "encryption at rest: off" {
		t.Errorf("status after disable: %q", line)
	}
}
