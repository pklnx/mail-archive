package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/config"
)

type encSetup struct {
	dataDir, keyFile string
}

// newEncSetup isolates the environment: a data directory and a keys
// directory outside it. No database is needed.
func newEncSetup(t *testing.T) *encSetup {
	t.Helper()
	s := &encSetup{dataDir: t.TempDir(), keyFile: filepath.Join(t.TempDir(), "archive.key")}
	t.Setenv(config.EnvDataDir, s.dataDir)
	t.Setenv(config.EnvArchiveKeyFile, "")
	t.Setenv(config.EnvArchiveKey, "")
	t.Setenv(config.EnvSecretKey, "")
	return s
}

func runEnc(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := runCmd(t, cmd, "", args...)
	return out.String(), err
}

var printedKey = regexp.MustCompile(`(?m)^  ([A-Za-z0-9+/]{43}=)$`)

// keygen creates the key file and keyring and returns the printed recovery key.
func (s *encSetup) keygen(t *testing.T) string {
	t.Helper()
	out, err := runEnc(t, newEncryptionCmd(), "keygen", "--key-file", s.keyFile)
	if err != nil {
		t.Fatalf("keygen: %v\n%s", err, out)
	}
	m := printedKey.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("keygen printed no recovery key:\n%s", out)
	}
	t.Setenv(config.EnvArchiveKeyFile, s.keyFile)
	return m[1]
}

func TestEncryptionKeygen(t *testing.T) {
	s := newEncSetup(t)
	recovery := s.keygen(t)

	info, err := os.Stat(s.keyFile)
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("key file mode: %v, %v", info, err)
	}
	kek, _, err := config.ArchiveKeyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	rk, err := blobcrypt.ParseKEK(recovery)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string][]byte{"archive key": kek, "recovery key": rk} {
		ring, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(s.dataDir))
		if err != nil {
			t.Fatal(err)
		}
		if err := ring.Open(key); err != nil {
			t.Errorf("%s does not open the keyring: %v", name, err)
		}
	}
	if bytes.Equal(kek, rk) {
		t.Fatal("recovery key equals the archive key")
	}
	raw, _ := os.ReadFile(blobcrypt.KeyringPath(s.dataDir))
	if bytes.Contains(raw, []byte(recovery)) || bytes.Contains(raw, []byte(blobcrypt.EncodeKEK(kek))) {
		t.Fatal("the keyring holds a key in the clear")
	}

	// Never replaced: the keyring exists, or the key file does.
	if out, err := runEnc(t, newEncryptionCmd(), "keygen", "--key-file", filepath.Join(t.TempDir(), "k")); err == nil || !strings.Contains(err.Error(), "keyring exists") {
		t.Errorf("second keygen: %v\n%s", err, out)
	}
	other := newEncSetup(t)
	if err := os.WriteFile(other.keyFile, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := runEnc(t, newEncryptionCmd(), "keygen", "--key-file", other.keyFile); err == nil || !strings.Contains(err.Error(), "never replaces a key file") {
		t.Errorf("keygen over a key file: %v", err)
	}
	if _, err := os.Stat(blobcrypt.KeyringPath(other.dataDir)); err == nil {
		t.Error("keygen left a keyring after refusing the key file")
	}
	if _, err := runEnc(t, newEncryptionCmd(), "keygen", "--key-file", filepath.Join(other.dataDir, "archive.key")); err == nil || !strings.Contains(err.Error(), "inside the data directory") {
		t.Errorf("keygen into the data directory: %v", err)
	}
	if _, err := runEnc(t, newEncryptionCmd(), "keygen"); err == nil || !strings.Contains(err.Error(), "--key-file") {
		t.Errorf("keygen without a path: %v", err)
	}
}

func TestEncryptionKeygenNoRecovery(t *testing.T) {
	s := newEncSetup(t)
	out, err := runEnc(t, newEncryptionCmd(), "keygen", "--key-file", s.keyFile, "--no-recovery")
	if err != nil || printedKey.MatchString(out) || !strings.Contains(out, "No recovery key") {
		t.Fatalf("keygen --no-recovery: %v\n%s", err, out)
	}
	ring, _ := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(s.dataDir))
	if len(ring.Slots()) != 1 {
		t.Errorf("%d slots, want 1", len(ring.Slots()))
	}
}

func TestEncryptionSlots(t *testing.T) {
	s := newEncSetup(t)
	recovery := s.keygen(t)
	rk, _ := blobcrypt.ParseKEK(recovery)

	out, err := runEnc(t, newEncryptionCmd(), "status")
	if err != nil || !strings.Contains(out, "opens the keyring") || !strings.Contains(out, "archive key (configured)") ||
		!strings.Contains(out, blobcrypt.Fingerprint(rk)+"  ") || !regexp.MustCompile(`generation:\s+2\n`).MatchString(out) {
		t.Fatalf("status: %v\n%s", err, out)
	}

	out, err = runEnc(t, newEncryptionCmd(), "add-key", "--label", "second recovery")
	if err != nil {
		t.Fatalf("add-key: %v\n%s", err, out)
	}
	m := printedKey.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("add-key printed no key:\n%s", out)
	}
	nk, _ := blobcrypt.ParseKEK(m[1])
	ring, _ := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(s.dataDir))
	if err := ring.Open(nk); err != nil || ring.Generation() != 3 {
		t.Fatalf("new key: %v, generation %d", err, ring.Generation())
	}

	kek, _, _ := config.ArchiveKeyFromEnv()
	if _, err := runEnc(t, newEncryptionCmd(), "remove-key", blobcrypt.Fingerprint(kek)); err == nil || !strings.Contains(err.Error(), "configured archive key") {
		t.Errorf("remove-key of the configured key: %v", err)
	}
	if _, err := runEnc(t, newEncryptionCmd(), "remove-key", "xyz"); err == nil {
		t.Error("remove-key of a malformed fingerprint accepted")
	}
	if out, err := runEnc(t, newEncryptionCmd(), "remove-key", strings.ToUpper(blobcrypt.Fingerprint(rk))); err != nil {
		t.Fatalf("remove-key: %v\n%s", err, out)
	}
	ring, _ = blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(s.dataDir))
	if err := ring.Open(rk); !errors.Is(err, blobcrypt.ErrNoSlot) {
		t.Errorf("removed recovery key: %v", err)
	}

	// Another key: status says so and fails; add-key refuses.
	t.Setenv(config.EnvArchiveKeyFile, "")
	other, _ := blobcrypt.GenerateKEK()
	t.Setenv(config.EnvArchiveKey, blobcrypt.EncodeKEK(other))
	out, err = runEnc(t, newEncryptionCmd(), "status")
	if err == nil || !strings.Contains(out, "does not open the keyring") {
		t.Errorf("status with another key: %v\n%s", err, out)
	}
	if _, err := runEnc(t, newEncryptionCmd(), "add-key"); !errors.Is(err, blobcrypt.ErrNoSlot) {
		t.Errorf("add-key with another key: %v", err)
	}
	t.Setenv(config.EnvArchiveKey, "")
	out, err = runEnc(t, newEncryptionCmd(), "status")
	if err != nil || !strings.Contains(out, "unverified") || !strings.Contains(out, "not configured") {
		t.Errorf("status without a key: %v\n%s", err, out)
	}
	if _, err := runEnc(t, newEncryptionCmd(), "add-key"); err == nil || !strings.Contains(err.Error(), "no archive key") {
		t.Errorf("add-key without a key: %v", err)
	}
	if strings.Contains(out, recovery) || strings.Contains(out, blobcrypt.EncodeKEK(kek)) {
		t.Error("status prints a key")
	}
}

func TestEncryptionStatusNoKeyring(t *testing.T) {
	newEncSetup(t)
	out, err := runEnc(t, newEncryptionCmd(), "status")
	if err != nil || !strings.Contains(out, "keyring: none") || !strings.Contains(out, "encryption keygen") {
		t.Errorf("status without keyring: %v\n%s", err, out)
	}
}

func TestDecrypt(t *testing.T) {
	s := newEncSetup(t)
	s.keygen(t)
	ring, _, err := openKeyring(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	id, dk, _ := ring.Current()
	plain := bytes.Repeat([]byte("Subject: hello\r\n\r\nbody\r\n"), 5000)
	h := sha256.Sum256(plain)
	sha := hex.EncodeToString(h[:])
	var ct bytes.Buffer
	w, _ := blobcrypt.NewWriter(&ct, id, dk, blobcrypt.PurposeMessage)
	_, _ = w.Write(plain)
	if err := w.Finish(h[:]); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	named := filepath.Join(dir, sha+".enc")
	renamed := filepath.Join(dir, "renamed.enc")
	for _, p := range []string{named, renamed} {
		if err := os.WriteFile(p, ct.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runEnc(t, newDecryptCmd(), named)
	if err != nil || out != string(plain) {
		t.Fatalf("decrypt to stdout: %v, %d bytes", err, len(out))
	}
	outFile := filepath.Join(dir, "out.eml")
	if _, err := runEnc(t, newDecryptCmd(), renamed, "--sha", sha, "-o", outFile); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(outFile); !bytes.Equal(got, plain) {
		t.Fatal("--out differs")
	}
	if _, err := runEnc(t, newDecryptCmd(), renamed, "-o", filepath.Join(dir, "x")); err == nil || !strings.Contains(err.Error(), "--sha") {
		t.Errorf("renamed file without --sha: %v", err)
	}
	if _, err := runEnc(t, newDecryptCmd(), named, "-o", outFile); err == nil {
		t.Error("existing --out replaced")
	}

	// A damaged file: non-zero exit, --out not created.
	bad := filepath.Join(dir, "bad.enc")
	data := bytes.Clone(ct.Bytes())
	data[blobcrypt.HeaderSize+10] ^= 1
	_ = os.WriteFile(bad, data, 0o600)
	badOut := filepath.Join(dir, "bad.eml")
	if _, err := runEnc(t, newDecryptCmd(), bad, "--sha", sha, "-o", badOut); !errors.Is(err, blobcrypt.ErrCorrupt) {
		t.Errorf("damaged file: %v", err)
	}
	if _, err := os.Stat(badOut); err == nil {
		t.Error("--out created for a damaged file")
	}
	other := sha256.Sum256([]byte("other"))
	if _, err := runEnc(t, newDecryptCmd(), renamed, "--sha", hex.EncodeToString(other[:]), "-o", filepath.Join(dir, "y")); !errors.Is(err, blobcrypt.ErrCorrupt) {
		t.Errorf("wrong --sha: %v", err)
	}
	plainFile := filepath.Join(dir, sha+".eml")
	_ = os.WriteFile(plainFile, plain, 0o600)
	if _, err := runEnc(t, newDecryptCmd(), plainFile); !errors.Is(err, blobcrypt.ErrNotEncrypted) {
		t.Errorf("plaintext file: %v", err)
	}
	// An explicit keyring path works without the data directory.
	t.Setenv(config.EnvDataDir, t.TempDir())
	if _, err := runEnc(t, newDecryptCmd(), named, "--keyring", blobcrypt.KeyringPath(s.dataDir), "-o", filepath.Join(dir, "z")); err != nil {
		t.Errorf("--keyring: %v", err)
	}
}

func TestParseOwner(t *testing.T) {
	if u, g, err := parseOwner("65532:65532"); err != nil || u != 65532 || g != 65532 {
		t.Errorf("parseOwner: %d %d %v", u, g, err)
	}
	if u, _, err := parseOwner(""); err != nil || u != -1 {
		t.Errorf("empty: %d %v", u, err)
	}
	for _, s := range []string{"1", "a:b", "-1:2", "1:"} {
		if _, _, err := parseOwner(s); err == nil {
			t.Errorf("parseOwner(%q) accepted", s)
		}
	}
}

// TestEncryptionKeygenRechecksUnderLock: a keyring that appears between
// the first check and the lock (another keygen) is never replaced.
func TestEncryptionKeygenRechecksUnderLock(t *testing.T) {
	s := newEncSetup(t)
	other, _ := blobcrypt.GenerateKEK()
	ring, err := blobcrypt.NewKeyring(other, "archive key", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	keygenLocked = func() {
		if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(s.dataDir), ring); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { keygenLocked = func() {} })
	if _, err := runEnc(t, newEncryptionCmd(), "keygen", "--key-file", s.keyFile); err == nil || !strings.Contains(err.Error(), "keyring exists") {
		t.Fatalf("keygen racing another: %v", err)
	}
	if _, err := os.Stat(s.keyFile); err == nil {
		t.Error("key file written although the keyring was refused")
	}
	got, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(s.dataDir))
	if err != nil || got.ArchiveID() != ring.ArchiveID() {
		t.Fatalf("the other keygen's keyring was replaced: %v", err)
	}
}

// TestEncryptionKeygenCleansUp: a failure after the keyring was written
// (chown) leaves neither the key file nor the keyring, so a rerun works.
func TestEncryptionKeygenCleansUp(t *testing.T) {
	s := newEncSetup(t)
	owner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	lchown = func(string, int, int) error { return errors.New("injected chown failure") }
	_, err := runEnc(t, newEncryptionCmd(), "keygen", "--key-file", s.keyFile, "--chown", owner)
	lchown = os.Lchown
	if err == nil || !strings.Contains(err.Error(), "injected chown failure") {
		t.Fatalf("keygen with a failing chown: %v", err)
	}
	for _, p := range []string{s.keyFile, blobcrypt.KeyringPath(s.dataDir)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s left behind", p)
		}
	}
	if out, err := runEnc(t, newEncryptionCmd(), "keygen", "--key-file", s.keyFile, "--chown", owner); err != nil {
		t.Fatalf("rerun: %v\n%s", err, out)
	}
	t.Setenv(config.EnvArchiveKeyFile, s.keyFile)
	if _, _, err := openKeyring(s.dataDir); err != nil {
		t.Fatalf("after the rerun: %v", err)
	}
}
