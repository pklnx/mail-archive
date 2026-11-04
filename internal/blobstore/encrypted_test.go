package blobstore

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
)

const marker = "Zebrafink-Marker-4711"

// testRing is a new, opened keyring.
func testRing(t testing.TB) *blobcrypt.Keyring {
	t.Helper()
	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := blobcrypt.NewKeyring(kek, "archive key", time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func benchRing(b *testing.B) *blobcrypt.Keyring { return testRing(b) }

// newEncrypted is a recorded store that writes encrypted blobs.
func newEncrypted(t *testing.T, ring *blobcrypt.Keyring) (*Store, *recorder, string) {
	t.Helper()
	s, rec, root := newRecorded(t)
	if err := s.SetKeys(ring, true); err != nil {
		t.Fatal(err)
	}
	return s, rec, root
}

func readMessage(t *testing.T, s *Store, hash string) (string, *Message) {
	t.Helper()
	m, err := s.OpenMessage(hash)
	if err != nil {
		t.Fatalf("OpenMessage: %v", err)
	}
	defer func() { _ = m.Close() }()
	b, err := io.ReadAll(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), m
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func sha(content string) string {
	s := sha256.Sum256([]byte(content))
	return hex.EncodeToString(s[:])
}

// Put encrypts while it hashes: the file is <sha>.enc under the current
// data key, no plaintext reaches the disk, and the identity stays the
// plaintext hash.
func TestPutEncrypted(t *testing.T) {
	ring := testRing(t)
	s, rec, root := newEncrypted(t, ring)
	content := "Subject: " + marker + "\r\n\r\n" + strings.Repeat(marker+" body\r\n", 9000) // > 64 KiB
	blob, created, err := s.Put(strings.NewReader(content))
	if err != nil || !created {
		t.Fatalf("put: %v created=%v", err, created)
	}
	if blob.SHA256 != sha(content) || blob.Size != int64(len(content)) || blob.Path != EncPath(blob.SHA256) {
		t.Fatalf("blob %+v", blob)
	}
	final := filepath.Join(root, filepath.FromSlash(blob.Path))
	want := append([]string{"sync:TEMP", "close:TEMP", "link:" + final}, barrier(shardDirs(root, blob.SHA256)...)...)
	equal(t, "new encrypted blob", rec.take(), want)
	raw, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatal("plaintext in the encrypted file")
	}
	if int64(len(raw)) != blobcrypt.EncryptedSize(blob.Size) {
		t.Fatalf("file size %d, want %d", len(raw), blobcrypt.EncryptedSize(blob.Size))
	}
	h, err := blobcrypt.ParseHeader(raw[:blobcrypt.HeaderSize])
	if err != nil || h.KeyID != ring.CurrentKey() || h.Purpose != blobcrypt.PurposeMessage {
		t.Fatalf("header %+v, %v", h, err)
	}
	if exists(s.abs(RelPath(blob.SHA256))) {
		t.Fatal("a plaintext file was written")
	}
	got, m := readMessage(t, s, blob.SHA256)
	if got != content || !m.Encrypted() || m.Size() != blob.Size || m.Path() != blob.Path {
		t.Fatalf("read back %d bytes, encrypted=%v size=%d path=%s", len(got), m.Encrypted(), m.Size(), m.Path())
	}
	assertNoTemp(t, root)

	// The duplicate dedups onto the encrypted file.
	if b2, created, err := s.Put(strings.NewReader(content)); err != nil || created || b2 != blob {
		t.Fatalf("duplicate: %+v created=%v %v", b2, created, err)
	}
	equal(t, "duplicate", rec.take(), append(barrier(final), barrier(shardDirs(root, blob.SHA256)...)...))
	if !s.Exists(blob.SHA256) {
		t.Fatal("Exists is false for an encrypted blob")
	}
	assertNoTemp(t, root)
}

// Random access through ReadAt and Seek matches the plaintext, as
// http.ServeContent and the extractors use it.
func TestMessageRandomAccess(t *testing.T) {
	ring := testRing(t)
	s, _, _ := newEncrypted(t, ring)
	content := make([]byte, 3*blobcrypt.ChunkSize+123)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	blob, _, err := s.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.OpenMessage(blob.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if end, err := m.Seek(0, io.SeekEnd); err != nil || end != int64(len(content)) {
		t.Fatalf("seek end: %d %v", end, err)
	}
	for range 200 {
		o, _ := rand.Int(rand.Reader, big.NewInt(int64(len(content))))
		n, _ := rand.Int(rand.Reader, big.NewInt(2*blobcrypt.ChunkSize))
		off, want := o.Int64(), content[o.Int64():min(o.Int64()+n.Int64(), int64(len(content)))]
		got := make([]byte, len(want))
		if _, err := m.ReadAt(got, off); err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("ReadAt(%d, %d) differs", off, len(want))
		}
		if _, err := m.Seek(off, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(m, got); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("Seek(%d)+Read differs: %v", off, err)
		}
	}
}

// writeForm puts content into a store of the other form, so that root
// holds that file before the store under test writes.
func writeForm(t *testing.T, root, content string, ring *blobcrypt.Keyring) string {
	t.Helper()
	o, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if ring != nil {
		if err := o.SetKeys(ring, true); err != nil {
			t.Fatal(err)
		}
	}
	b, _, err := o.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, filepath.FromSlash(b.Path))
}

// Put never dedups onto the other form: facing a plaintext file in an
// encrypting store it writes the encrypted one, reads it back, and only
// then unlinks the plaintext (#392, part 2, review of revision 5).
func TestPutReplacesPlaintext(t *testing.T) {
	ring := testRing(t)
	s, rec, root := newEncrypted(t, ring)
	content := "Subject: " + marker + "\r\n\r\nold plaintext copy\r\n"
	eml := writeForm(t, root, content, nil)
	rec.take()

	blob, created, err := s.Put(strings.NewReader(content))
	if err != nil || created {
		t.Fatalf("put: %v created=%v (the message was there)", err, created)
	}
	enc := s.abs(EncPath(blob.SHA256))
	leaf := shardDirs(root, blob.SHA256)[0]
	want := append([]string{"sync:TEMP", "close:TEMP", "link:" + enc}, barrier(shardDirs(root, blob.SHA256)...)...)
	want = append(append(want, "unlink:"+eml), barrier(leaf)...)
	equal(t, "replace plaintext", rec.take(), want)
	if exists(eml) || !exists(enc) {
		t.Fatalf("after put: eml=%v enc=%v", exists(eml), exists(enc))
	}
	if got, m := readMessage(t, s, blob.SHA256); got != content || !m.Encrypted() {
		t.Fatal("content changed")
	}
	assertNoTemp(t, root)
}

// The plaintext store (encryption off) replaces an encrypted file the same
// way.
func TestPutPlaintextReplacesEncrypted(t *testing.T) {
	ring := testRing(t)
	s, _, root := newRecorded(t)
	content := "Subject: back to plaintext\r\n\r\nbody\r\n"
	enc := writeForm(t, root, content, ring)
	blob, created, err := s.Put(strings.NewReader(content))
	if err != nil || created {
		t.Fatalf("put: %v created=%v", err, created)
	}
	if exists(enc) || !exists(s.abs(blob.Path)) || blob.Path != RelPath(blob.SHA256) {
		t.Fatalf("after put: enc=%v %s", exists(enc), blob.Path)
	}
}

// A crash between the link of the encrypted file and the unlink of the
// plaintext one leaves both; every reader finds the message, and the next
// Put of it completes the step.
func TestPutRepeatsUnlinkAfterCrash(t *testing.T) {
	ring := testRing(t)
	s, rec, root := newEncrypted(t, ring)
	content := "Subject: crash\r\n\r\nbody\r\n"
	eml := writeForm(t, root, content, nil)
	rec.failOnce("unlink:"+eml, errInjected)
	if _, _, err := s.Put(strings.NewReader(content)); !errors.Is(err, errInjected) {
		t.Fatalf("put: %v", err)
	}
	hash := sha(content)
	if !exists(eml) || !exists(s.abs(EncPath(hash))) {
		t.Fatal("want both forms after the failed unlink")
	}
	if got, m := readMessage(t, s, hash); got != content || !m.Encrypted() {
		t.Fatal("OpenMessage does not prefer the encrypted copy")
	}
	rec.take()
	if _, created, err := s.Put(strings.NewReader(content)); err != nil || created {
		t.Fatalf("retry: %v created=%v", err, created)
	}
	if exists(eml) {
		t.Fatal("the retry did not unlink the plaintext copy")
	}
	ev := rec.take()
	if !slices.Contains(ev, "unlink:"+eml) {
		t.Fatalf("retry events %v", ev)
	}
}

// The redundant copy goes only after the surviving one was read back. An
// encrypted file with a valid header that fails authentication, next to
// the plaintext one, is replaced by Put's own copy (read back first, then
// renamed over it); the plaintext goes only after that, so the message
// heals instead of failing on every retry (review round 1 of #510).
func TestPutReplacesCorruptEncryptedCopy(t *testing.T) {
	ring := testRing(t)
	s, rec, root := newEncrypted(t, ring)
	content := "Subject: keep\r\n\r\nbody\r\n"
	eml := writeForm(t, root, content, nil)
	hash := sha(content)
	rec.failOnce("unlink:"+eml, errInjected) // leave both
	if _, _, err := s.Put(strings.NewReader(content)); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	enc := s.abs(EncPath(hash))
	raw, err := os.ReadFile(enc)
	if err != nil {
		t.Fatal(err)
	}
	raw[blobcrypt.HeaderSize+1] ^= 1 // a ciphertext bit, the header stays valid
	if err := os.WriteFile(enc, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.usableHeader(enc) {
		t.Fatal("the header was spoiled too")
	}
	rec.take()
	blob, created, err := s.Put(strings.NewReader(content))
	if err != nil || created {
		t.Fatalf("put onto a corrupt encrypted copy: %v created=%v", err, created)
	}
	// The plaintext stays until the replacement is in place and durable.
	ops := rec.take()
	at := func(op string) int {
		t.Helper()
		for i, o := range ops {
			if o == op {
				return i
			}
		}
		t.Fatalf("no %s in %v", op, ops)
		return -1
	}
	if rename, unlink := at("rename:"+enc), at("unlink:"+eml); rename > unlink {
		t.Fatalf("the plaintext was unlinked before the replacement: %v", ops)
	}
	if exists(eml) {
		t.Fatal("the plaintext copy stays")
	}
	if got, m := readMessage(t, s, blob.SHA256); got != content || !m.Encrypted() {
		t.Fatal("content after the replacement")
	}
	assertNoTemp(t, root)
}

// An encrypted file that Put cannot use (an unknown data key, as left by a
// writer of another keyring, or an invalid header) is replaced by an
// atomic rename after the new copy was read back; never deduplicated onto.
func TestPutReplacesUnusableEncrypted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(t *testing.T, path string)
	}{
		{"unknown data key", func(*testing.T, string) {}},
		{"invalid header", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("MA-ENC9\n garbage"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ring := testRing(t)
			s, rec, root := newEncrypted(t, ring)
			content := "Subject: stale writer\r\n\r\nbody\r\n"
			enc := writeForm(t, root, content, testRing(t)) // another keyring
			tc.spoil(t, enc)
			if _, err := s.OpenMessage(sha(content)); err == nil {
				t.Fatal("the unusable file opens")
			}
			rec.take()
			blob, created, err := s.Put(strings.NewReader(content))
			if err != nil || created {
				t.Fatalf("put: %v created=%v", err, created)
			}
			want := append([]string{"sync:TEMP", "close:TEMP", "rename:" + enc}, barrier(shardDirs(root, blob.SHA256)...)...)
			equal(t, "replace by rename", rec.take(), want)
			if got, _ := readMessage(t, s, blob.SHA256); got != content {
				t.Fatal("content after the replacement")
			}
			assertNoTemp(t, root)
		})
	}
}

func TestOpenMessage(t *testing.T) {
	ring := testRing(t)
	s, _, root := newEncrypted(t, ring)
	content := "Subject: open\r\n\r\nbody\r\n"
	hash := sha(content)
	if _, err := s.OpenMessage(hash); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := s.OpenMessage("../x"); err == nil {
		t.Fatal("not a hash accepted")
	}
	writeForm(t, root, content, nil)
	if got, m := readMessage(t, s, hash); got != content || m.Encrypted() || m.Path() != RelPath(hash) {
		t.Fatal("plaintext form not found")
	}

	// A plaintext-only store cannot read an encrypted file.
	other := t.TempDir()
	writeForm(t, other, content, ring)
	plain, err := New(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.OpenMessage(hash); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("no keys: %v", err)
	}
	// A file moved to another name fails its binding.
	s2, err := New(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.SetKeys(ring, false); err != nil {
		t.Fatal(err)
	}
	moved := "Subject: other\r\n\r\nbody\r\n"
	dst := s2.abs(EncPath(sha(moved)))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s2.abs(EncPath(hash)), dst); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.OpenMessage(sha(moved)); !errors.Is(err, blobcrypt.ErrCorrupt) {
		t.Fatalf("moved file: %v", err)
	}
	// Under a key the keyring does not hold.
	s3, err := New(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := s3.SetKeys(testRing(t), true); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.OpenMessage(sha(moved)); !errors.Is(err, blobcrypt.ErrUnknownKey) {
		t.Fatalf("unknown key: %v", err)
	}
}

// OpenMessage looks for .enc, .eml, then .enc again, so it finds a message
// whose file changes form between two lookups.
func TestOpenMessageLookupOrder(t *testing.T) {
	ring := testRing(t)
	s, _, root := newEncrypted(t, ring)
	content := "Subject: converting\r\n\r\nbody\r\n"
	hash := sha(content)
	eml := writeForm(t, root, content, nil)
	encSrc := filepath.Join(t.TempDir(), "x.enc")
	staged := writeForm(t, t.TempDir(), content, ring)
	if err := os.Rename(staged, encSrc); err != nil {
		t.Fatal(err)
	}
	var seen []string
	s.beforeOpen = func(path string) {
		seen = append(seen, filepath.Base(path))
		// After the first .enc lookup missed: the conversion publishes the
		// .enc and unlinks the .eml before the .eml lookup.
		if len(seen) == 2 {
			if err := os.Rename(encSrc, s.abs(EncPath(hash))); err != nil {
				t.Error(err)
			}
			if err := os.Remove(eml); err != nil {
				t.Error(err)
			}
		}
	}
	got, m := readMessage(t, s, hash)
	if got != content || !m.Encrypted() {
		t.Fatal("not found in the converted form")
	}
	if want := []string{hash + ".enc", hash + ".eml", hash + ".enc"}; !slices.Equal(seen, want) {
		t.Fatalf("lookups %v, want %v", seen, want)
	}
}

func TestBothFormsExistRemoveWalk(t *testing.T) {
	ring := testRing(t)
	s, _, root := newEncrypted(t, ring)
	a := "Subject: a\r\n\r\nbody\r\n"
	b := "Subject: b\r\n\r\nbody\r\n"
	writeForm(t, root, a, nil)
	writeForm(t, root, b, ring)
	// Both forms of one hash, as a crash between link and unlink leaves.
	if err := os.WriteFile(s.abs(RelPath(sha(b))), []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{sha(a), sha(b)} {
		if !s.Exists(h) {
			t.Fatalf("Exists(%s) false", h)
		}
	}
	got := map[string]bool{}
	if err := s.Walk(func(e Entry) error {
		if e.Kind != EntryBlob {
			t.Errorf("entry %+v", e)
		}
		got[e.Path] = e.Encrypted
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{RelPath(sha(a)): false, EncPath(sha(b)): true, RelPath(sha(b)): false}
	if len(got) != len(want) {
		t.Fatalf("walk %v", got)
	}
	for p, enc := range want {
		if e, ok := got[p]; !ok || e != enc {
			t.Fatalf("walk %v, want %v", got, want)
		}
	}
	if err := s.Remove(sha(b)); err != nil {
		t.Fatal(err)
	}
	if s.Exists(sha(b)) || exists(s.abs(RelPath(sha(b)))) || exists(s.abs(EncPath(sha(b)))) {
		t.Fatal("Remove left a form")
	}
	if !s.Exists(sha(a)) {
		t.Fatal("Remove took another blob")
	}
}

// A failed rename keeps the unusable file and the error.
func TestPutReplaceRenameFails(t *testing.T) {
	ring := testRing(t)
	s, rec, root := newEncrypted(t, ring)
	content := "Subject: rename fails\r\n\r\nbody\r\n"
	enc := writeForm(t, root, content, testRing(t))
	rec.failOnce("rename:"+enc, syscall.EIO)
	if _, _, err := s.Put(strings.NewReader(content)); !errors.Is(err, syscall.EIO) {
		t.Fatalf("put: %v", err)
	}
	assertNoTemp(t, root)
	if _, _, err := s.Put(strings.NewReader(content)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got, _ := readMessage(t, s, sha(content)); got != content {
		t.Fatal("content after the retry")
	}
}
