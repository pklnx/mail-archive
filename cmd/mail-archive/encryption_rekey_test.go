package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

// rotate-key writes a new archive key and replaces only the configured
// key's slot: the old archive key opens neither copy, the untouched
// recovery key still opens both, and no recovery key is printed
// (coordinator decision on #437, 6640615690). Counter-proofs: without the
// removal of the replaced slot the old key opens the keyring; with the
// former Rewrap that kept no unsupplied slot, the recovery key does not.
// rekey then keeps the configured key and the --keep-key-file key, drops
// the other slot, and re-encrypts under a new data key.
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
		t.Error("rotate-key took --keep-key-file, which only rekey has")
	}
	if _, err := os.Stat(newKey); err == nil {
		t.Fatal("a refused rotate-key left a key file")
	}
	out, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", newKey)
	if err != nil {
		t.Fatalf("rotate-key: %v\n%s", err, out)
	}
	if printedKey.MatchString(out) || !strings.Contains(out, config.EnvArchiveKeyFile+"="+newKey) || !strings.Contains(out, "encryption rekey") ||
		!strings.Contains(out, "replaces the slot of key "+blobcrypt.Fingerprint(oldKEK)) || strings.Contains(out, "warning:") ||
		strings.Contains(out, "no recovery key remains") {
		t.Fatalf("rotate-key output:\n%s", out)
	}
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
		if len(k.Slots()) != 2 {
			t.Errorf("copy %d: %d slots, want 2", i, len(k.Slots()))
		}
		for _, key := range [][]byte{nk, rk} {
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
	// A second recovery key that rekey is not given: its slot is dropped.
	out, err = runEnc(t, newEncryptionCmd(), "add-key", "--label", "second recovery")
	if err != nil {
		t.Fatalf("add-key: %v\n%s", err, out)
	}
	m := printedKey.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("add-key output:\n%s", out)
	}
	second, _ := blobcrypt.ParseKEK(m[1])
	before := bothCopies(t, st, s.dataDir)[0]
	if err := before.Open(nk); err != nil {
		t.Fatal(err)
	}
	out, err = runEnc(t, newEncryptionCmd(), "rekey", "--no-recovery", "--keep-key-file", writeTestKey(t, rk))
	if err != nil {
		t.Fatalf("rekey: %v\n%s", err, out)
	}
	if printedKey.MatchString(out) || !regexp.MustCompile(`deleted the old data key [0-9a-f]{8}`).MatchString(out) ||
		!strings.Contains(out, "dropped the slot of key "+blobcrypt.Fingerprint(second)) || !strings.Contains(out, "rekey done") {
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
// file keeps the new key file: the database copy no longer opens with the
// replaced key, and the next start rebuilds the file from it.
// Counter-proofs: removing the key file on every error leaves no archive
// key that opens the new keyring; ChangeKeyring returning no keyring on a
// file error prints nothing.
func TestEncryptionRotateKeyFileNotWritten(t *testing.T) {
	s := newEncSetup(t)
	st := s.withDB(t)
	recovery := s.keygen(t)
	rk, _ := blobcrypt.ParseKEK(recovery)
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
	if !strings.Contains(out, "wrote archive key "+newKey) {
		t.Fatalf("output:\n%s", out)
	}
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

// rekeyStartWriter snapshots, at the first line of the conversion, what the
// operator has seen so far.
type rekeyStartWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	atStart string
	seen    bool
}

func (w *rekeyStartWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.seen && strings.Contains(string(p), "re-encrypting the message files") {
		w.atStart, w.seen = w.buf.String(), true
	}
	return w.buf.Write(p)
}

// rekey prints the new recovery key before it converts the first file: the
// start has dropped the old recovery slot, so a run killed during the
// conversion must not leave the operator without a recovery key (review
// round 1 of #519; the test follows reviewer A's). Counter-proof: printing
// the key only after archive.Rekey returns, as before.
func TestEncryptionRekeyShowsRecoveryKeyFirst(t *testing.T) {
	s := newEncSetup(t)
	st := s.withDB(t)
	s.keygen(t)
	blobs, _ := blobstore.New(s.dataDir)
	if _, _, err := blobs.Put(strings.NewReader("Subject: x\r\n\r\ny\r\n")); err != nil {
		t.Fatal(err)
	}
	if out, err := runEnc(t, newEncryptionCmd(), "enable"); err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	w := &rekeyStartWriter{}
	if err := encryptionRekey(context.Background(), w, nil, true); err != nil {
		t.Fatal(err)
	}
	m := printedKey.FindStringSubmatch(w.atStart)
	if !w.seen || m == nil {
		t.Fatalf("the new recovery key was not shown before the conversion began; output so far:\n%s", w.atStart)
	}
	rk, _ := blobcrypt.ParseKEK(m[1])
	if err := bothCopiesDB(t, st).Open(rk); err != nil {
		t.Fatalf("the shown key: %v", err)
	}
}

// restore-dump refuses while a rekey runs (it holds the writers lock
// exclusively) and names it; archive.TestRekeyAndRestoreDumpExclude covers
// the other direction. Counter-proof: restore-dump taking the writers lock
// shared fails with ErrWritersExcluded instead.
func TestRestoreDumpRefusesDuringRekey(t *testing.T) {
	s := newEncSetup(t)
	st := s.withDB(t)
	lock, err := st.TryLockWritersExclusive(context.Background(), "rekey")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := runEnc(t, newRestoreDumpCmd(), "--reconcile-only"); !errors.Is(err, store.ErrWritersRunning) ||
		!strings.Contains(err.Error(), "mail-archive encryption rekey") {
		t.Fatalf("restore-dump during rekey: %v", err)
	}
}

// The lost-key procedure (review round 2 of #519): with the archive key O
// lost, the operator configures the recovery key R and runs rotate-key
// --replace with O's fingerprint. R and the new key N open both copies, O
// opens neither, N's slot is labelled "archive key", R keeps its slot and
// label, and a warning lists the slots because the configured key is a
// recovery key. Counter-proof: ignoring --replace (replacing the
// configured key's slot) drops R and keeps O.
func TestEncryptionRotateKeyLostArchiveKey(t *testing.T) {
	s := newEncSetup(t)
	st := s.withDB(t)
	recovery := s.keygen(t)
	rk, _ := blobcrypt.ParseKEK(recovery)
	lost, _, _ := config.ArchiveKeyFromEnv()
	if out, err := runEnc(t, newEncryptionCmd(), "enable"); err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	// The key file is lost: the recovery key is configured instead.
	t.Setenv(config.EnvArchiveKeyFile, "")
	t.Setenv(config.EnvArchiveKey, recovery)
	newKey := filepath.Join(filepath.Dir(s.keyFile), "archive-2.key")

	// --replace forgotten: the configured key is a recovery key, so the
	// slot to replace is ambiguous; rotate-key refuses, names the slots,
	// writes no key file and changes neither copy (review round 3 of #519).
	// Counter-proof: the former warn-only behaviour replaces R's slot.
	fileBefore, _ := os.ReadFile(blobcrypt.KeyringPath(s.dataDir))
	rowBefore, _ := st.BlobEncryption(context.Background())
	_, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", newKey)
	if !errors.Is(err, errRotateAmbiguous) || !strings.Contains(err.Error(), "--replace") ||
		!strings.Contains(err.Error(), blobcrypt.Fingerprint(lost)+" (archive key)") || !strings.Contains(err.Error(), blobcrypt.Fingerprint(rk)+" (recovery key)") {
		t.Fatalf("rotate-key without --replace: %v", err)
	}
	if _, err := os.Stat(newKey); err == nil {
		t.Fatal("the refused rotate-key left a key file")
	}
	if fileAfter, _ := os.ReadFile(blobcrypt.KeyringPath(s.dataDir)); !bytes.Equal(fileAfter, fileBefore) {
		t.Fatal("the refused rotate-key changed the keyring file")
	}
	if rowAfter, _ := st.BlobEncryption(context.Background()); !bytes.Equal(rowAfter.Keyring, rowBefore.Keyring) || rowAfter.Generation != rowBefore.Generation {
		t.Fatal("the refused rotate-key changed the database copy")
	}

	out, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", newKey, "--replace", blobcrypt.Fingerprint(lost))
	if err != nil {
		t.Fatalf("rotate-key: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "the configured key "+blobcrypt.Fingerprint(rk)+" (recovery key) keeps its slot") {
		t.Errorf("output lacks the warning:\n%s", out)
	}
	if strings.Contains(out, "no recovery key remains") {
		t.Errorf("the recovery key remains, but the output says otherwise:\n%s", out)
	}
	t.Setenv(config.EnvArchiveKey, "")
	t.Setenv(config.EnvArchiveKeyFile, newKey)
	nk, _, err := config.ArchiveKeyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range bothCopies(t, st, s.dataDir) {
		if err := k.Open(lost); !errors.Is(err, blobcrypt.ErrNoSlot) {
			t.Errorf("copy %d: the lost key: %v", i, err)
		}
		for _, key := range [][]byte{nk, rk} {
			if err := k.Open(key); err != nil {
				t.Errorf("copy %d: key %s: %v", i, blobcrypt.Fingerprint(key), err)
			}
		}
		if sl, ok := k.Slot(blobcrypt.Fingerprint(nk)); !ok || sl.Label != labelArchiveKey {
			t.Errorf("copy %d: the new key's slot label %q", i, sl.Label)
		}
		if sl, ok := k.Slot(blobcrypt.Fingerprint(rk)); !ok || sl.Label != labelRecoveryKey {
			t.Errorf("copy %d: the recovery key's slot label %q", i, sl.Label)
		}
	}
	// An unknown fingerprint is refused, and no key file is left.
	other := filepath.Join(filepath.Dir(s.keyFile), "archive-3.key")
	if _, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", other, "--replace", "0123456789abcdef"); err == nil ||
		!strings.Contains(err.Error(), "no slot with fingerprint") {
		t.Errorf("--replace with an unknown fingerprint: %v", err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Error("a refused rotate-key left a key file")
	}
}

// --replace with the configured key's own fingerprint chooses explicitly:
// a configured recovery key's slot is replaced, the warning says so, and
// since no slot labelled "recovery key" remains, the output says to add
// one (review round 3 of #519). Counter-proof: without the note, the
// output does not say that no recovery key remains.
func TestEncryptionRotateKeyReplacesRecoverySlotExplicitly(t *testing.T) {
	s := newEncSetup(t)
	st := s.withDB(t)
	recovery := s.keygen(t)
	rk, _ := blobcrypt.ParseKEK(recovery)
	if out, err := runEnc(t, newEncryptionCmd(), "enable"); err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	t.Setenv(config.EnvArchiveKeyFile, "")
	t.Setenv(config.EnvArchiveKey, recovery)
	newKey := filepath.Join(filepath.Dir(s.keyFile), "archive-2.key")
	out, err := runEnc(t, newEncryptionCmd(), "rotate-key", "--new-key-file", newKey, "--replace", blobcrypt.Fingerprint(rk))
	if err != nil {
		t.Fatalf("rotate-key: %v\n%s", err, out)
	}
	if !strings.Contains(out, `warning: the replaced slot was labelled "recovery key"`) ||
		!strings.Contains(out, "no recovery key remains: run `"+commandName()+" encryption add-key`") {
		t.Fatalf("output:\n%s", out)
	}
	for i, k := range bothCopies(t, st, s.dataDir) {
		if err := k.Open(rk); !errors.Is(err, blobcrypt.ErrNoSlot) {
			t.Errorf("copy %d: the replaced recovery key: %v", i, err)
		}
	}
}
