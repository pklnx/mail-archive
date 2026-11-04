package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// writeTestKey writes key to a new owner-only file outside the data
// directory, as an operator puts a recovery key there for --keep-key-file.
func writeTestKey(t *testing.T, key []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "recovery.key")
	if err := os.WriteFile(p, []byte(blobcrypt.EncodeKEK(key)+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	return p
}

// bothCopies parses the keyring file and the database copy.
func bothCopies(t *testing.T, st *store.Store, dataDir string) []*blobcrypt.Keyring {
	t.Helper()
	file, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.BlobEncryption(context.Background())
	if err != nil || row == nil {
		t.Fatalf("row %v %v", row, err)
	}
	db, err := blobcrypt.ParseKeyring(row.Keyring)
	if err != nil {
		t.Fatal(err)
	}
	return []*blobcrypt.Keyring{file, db}
}

// rotate-key writes a new archive key, replaces the keyring key and keeps
// only the given recovery key besides a new one; the old archive key opens
// neither copy afterwards. rekey then re-encrypts under a new data key.
func TestEncryptionRotateKeyAndRekey(t *testing.T) {
	s := newEncSetup(t)
	st := s.withDB(t)
	recovery := s.keygen(t)
	rk, _ := blobcrypt.ParseKEK(recovery)
	oldKEK, _, _ := config.ArchiveKeyFromEnv()
	blobs, err := blobstore.New(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	raw := "Subject: Drehung\r\n\r\nGeheimnis\r\n"
	b, _, err := blobs.Put(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runEnc(t, newEncryptionCmd(), "enable"); err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}

	newKey := filepath.Join(filepath.Dir(s.keyFile), "archive-2.key")
	if _, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", s.keyFile); err == nil || !strings.Contains(err.Error(), "never replaces") {
		t.Errorf("rotate-key onto the existing key file: %v", err)
	}
	if _, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", filepath.Join(s.dataDir, "k")); err == nil || !strings.Contains(err.Error(), "inside the data directory") {
		t.Errorf("rotate-key into the data directory: %v", err)
	}
	if _, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", newKey, "--keep-key-file", s.keyFile); err == nil {
		t.Error("keeping the configured key accepted")
	}
	if _, err := os.Stat(newKey); err == nil {
		t.Fatal("a refused rotate-key left a key file")
	}
	out, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", newKey, "--keep-key-file", writeTestKey(t, rk))
	if err != nil {
		t.Fatalf("rotate-key: %v\n%s", err, out)
	}
	m := printedKey.FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, config.EnvArchiveKeyFile+"="+newKey) || !strings.Contains(out, "encryption rekey") {
		t.Fatalf("rotate-key output:\n%s", out)
	}
	newRecovery, _ := blobcrypt.ParseKEK(m[1])
	if info, err := os.Stat(newKey); err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("new key file: %v %v", info, err)
	}
	t.Setenv(config.EnvArchiveKeyFile, newKey)
	nk, _, err := config.ArchiveKeyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range bothCopies(t, st, s.dataDir) {
		if err := k.Open(oldKEK); !errors.Is(err, blobcrypt.ErrNoSlot) {
			t.Errorf("copy %d: the old archive key: %v", i, err)
		}
		for _, key := range [][]byte{nk, rk, newRecovery} {
			if err := k.Open(key); err != nil {
				t.Errorf("copy %d: key %s: %v", i, blobcrypt.Fingerprint(key), err)
			}
		}
	}
	if out, err := runEnc(t, newEncryptionCmd(), "status"); err != nil || !strings.Contains(out, "opens the keyring") {
		t.Fatalf("status with the new key: %v\n%s", err, out)
	}

	lock, err := st.HoldWriters(context.Background(), "serve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runEnc(t, newEncryptionCmd(), "rekey"); !errors.Is(err, store.ErrWritersRunning) {
		t.Errorf("rekey while serve runs: %v", err)
	}
	lock.Release()
	before := bothCopies(t, st, s.dataDir)[0]
	if err := before.Open(nk); err != nil {
		t.Fatal(err)
	}
	out, err = runEnc(t, newEncryptionCmd(), "rekey", "--no-recovery", "--keep-key-file", writeTestKey(t, rk))
	if err != nil {
		t.Fatalf("rekey: %v\n%s", err, out)
	}
	if printedKey.MatchString(out) || !regexp.MustCompile(`deleted the old data key [0-9a-f]{8}`).MatchString(out) ||
		!strings.Contains(out, "dropped the slot of key "+blobcrypt.Fingerprint(newRecovery)) || !strings.Contains(out, "rekey done") {
		t.Fatalf("rekey output:\n%s", out)
	}
	after := openTestKeyring(t, s.dataDir)
	if after.CurrentKey() == before.CurrentKey() || len(after.DataKeyIDs()) != 1 {
		t.Fatalf("data keys before %v (current %08x), after %v", before.DataKeyIDs(), before.CurrentKey(), after.DataKeyIDs())
	}
	r, _ := blobstore.New(s.dataDir)
	if err := r.SetKeys(after, true); err != nil {
		t.Fatal(err)
	}
	if got, err := readAllMessage(r, b.SHA256); err != nil || got != raw {
		t.Fatalf("after rekey: %q %v", got, err)
	}
	if line := statusLine(t); line != "encryption at rest: on" {
		t.Errorf("status after rekey: %q", line)
	}
}

func readAllMessage(b *blobstore.Store, hash string) (string, error) {
	m, err := b.OpenMessage(hash)
	if err != nil {
		return "", err
	}
	defer func() { _ = m.Close() }()
	got, err := io.ReadAll(m)
	return string(got), err
}

// blockKeyringFile replaces the keyring file with a non-empty directory, so
// that the next atomic replace of it fails (also for root).
func blockKeyringFile(t *testing.T, dataDir string) {
	t.Helper()
	p := blobcrypt.KeyringPath(dataDir)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// A rotate-key whose change reached the database copy but not the keyring
// file keeps the new key file and prints the new recovery key: the
// database copy opens only with them, and the next start rebuilds the
// file from it. Counter-proof: removing the key file on every error (the
// cleanup before this fix) leaves no key that opens the new keyring.
func TestEncryptionRotateKeyFileNotWritten(t *testing.T) {
	s := newEncSetup(t)
	st := s.withDB(t)
	s.keygen(t)
	if out, err := runEnc(t, newEncryptionCmd(), "enable"); err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	rotateKeyRewrapped = func() { blockKeyringFile(t, s.dataDir) }
	t.Cleanup(func() { rotateKeyRewrapped = func() {} })
	newKey := filepath.Join(filepath.Dir(s.keyFile), "archive-2.key")
	out, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", newKey)
	if !errors.Is(err, archive.ErrKeyringFileNotWritten) {
		t.Fatalf("rotate-key: %v\n%s", err, out)
	}
	m := printedKey.FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "wrote archive key "+newKey) {
		t.Fatalf("output:\n%s", out)
	}
	rk, _ := blobcrypt.ParseKEK(m[1])
	if err := os.RemoveAll(blobcrypt.KeyringPath(s.dataDir)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvArchiveKeyFile, newKey)
	nk, _, err := config.ArchiveKeyFromEnv()
	if err != nil {
		t.Fatalf("the new key file: %v", err)
	}
	db := bothCopiesDB(t, st)
	for _, k := range [][]byte{nk, rk} {
		if err := db.Open(k); err != nil {
			t.Errorf("the database copy with key %s: %v", blobcrypt.Fingerprint(k), err)
		}
	}
}

func bothCopiesDB(t *testing.T, st *store.Store) *blobcrypt.Keyring {
	t.Helper()
	row, err := st.BlobEncryption(context.Background())
	if err != nil || row == nil {
		t.Fatalf("row %v %v", row, err)
	}
	k, err := blobcrypt.ParseKeyring(row.Keyring)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
