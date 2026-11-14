package archive_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/store"
)

// encrypted turns encryption on for the env as a started writer sees it:
// the state row, the keyring, and a blob store that encrypts.
func (e *archiveEnv) encrypted() []byte {
	e.t.Helper()
	kek := e.encryption(store.BlobOn)
	a, err := e.atRest(archive.RoleReader, kek, nil, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.blobs.SetKeys(a.Keyring, true); err != nil {
		e.t.Fatal(err)
	}
	return kek
}

func TestVerifyEncryptedClean(t *testing.T) {
	e := newArchiveEnv(t)
	e.encrypted()
	b1 := e.save(e.folder, 1, msg(1))
	e.save(e.folder, 2, strings.Repeat("Subject: big\r\n", 10000)) // several chunks
	if b1.Path != blobstore.EncPath(b1.SHA256) {
		t.Fatalf("path %s", b1.Path)
	}
	res, found := e.verify(archive.VerifyOptions{Jobs: 2, Encryption: store.BlobOn})
	if res.Checked != 2 || res.Problems() != 0 || !res.Complete() || len(found) != 0 {
		t.Fatalf("result %+v, findings %v", res, found)
	}
}

// A modified ciphertext, a file moved to another hash's name and a file
// under an unknown key are corrupt, never io errors.
func TestVerifyEncryptedCorrupt(t *testing.T) {
	e := newArchiveEnv(t)
	e.encrypted()
	flipped := e.save(e.folder, 1, strings.Repeat("Subject: flipped\r\n", 8000))
	moved := e.save(e.folder, 2, msg(2))
	donor := e.save(e.folder, 3, msg(3))
	data, err := os.ReadFile(e.path(flipped.Path))
	if err != nil {
		t.Fatal(err)
	}
	data[blobcrypt.HeaderSize+10] ^= 1 // the first chunk, not the final one Open checks
	if err := os.WriteFile(e.path(flipped.Path), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(e.path(donor.Path), e.path(moved.Path)); err != nil {
		t.Fatal(err)
	}
	res, found := e.verify(archive.VerifyOptions{Jobs: 1, Encryption: store.BlobOn})
	var corrupt []string
	for _, f := range found[archive.FindingCorrupt] {
		corrupt = append(corrupt, f.SHA256)
	}
	slices.Sort(corrupt)
	want := []string{flipped.SHA256, moved.SHA256}
	slices.Sort(want)
	if !slices.Equal(corrupt, want) || res.Findings[archive.FindingIOError] != 0 {
		t.Fatalf("corrupt %v, want %v; findings %v", corrupt, want, res.Findings)
	}
	if len(found[archive.FindingMissing]) != 1 || found[archive.FindingMissing][0].SHA256 != donor.SHA256 ||
		found[archive.FindingMissing][0].Path != blobstore.EncPath(donor.SHA256) {
		t.Fatalf("missing %+v", found[archive.FindingMissing])
	}
}

// A plaintext file while encryption is on is a finding, also next to its
// encrypted file; while encrypting it is only counted; with encryption off
// nothing. An encrypted orphan is an orphan.
func TestVerifyPlaintextFinding(t *testing.T) {
	e := newArchiveEnv(t)
	plain := e.save(e.folder, 1, msg(1)) // before encryption was turned on
	e.encrypted()
	enc := e.save(e.folder, 2, msg(2))
	// Both forms of one message, as a crash between link and unlink leaves.
	both := e.save(e.folder, 3, msg(3))
	e.write(blobstore.RelPath(both.SHA256), msg(3))
	// An encrypted file without a row.
	if _, _, err := e.blobs.Put(strings.NewReader(msg(4))); err != nil {
		t.Fatal(err)
	}

	res, found := e.verify(archive.VerifyOptions{Jobs: 1, Encryption: store.BlobOn})
	var got []string
	for _, f := range found[archive.FindingPlaintext] {
		got = append(got, f.SHA256)
		if f.Path != blobstore.RelPath(f.SHA256) {
			t.Errorf("plaintext finding path %s", f.Path)
		}
	}
	slices.Sort(got)
	want := []string{plain.SHA256, both.SHA256}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("plaintext %v, want %v (encrypted-only %s)", got, want, enc.SHA256)
	}
	if res.Findings[archive.FindingCorrupt] != 0 || len(found[archive.FindingOrphan]) != 1 ||
		!strings.HasSuffix(found[archive.FindingOrphan][0].Path, ".enc") {
		t.Fatalf("findings %v, orphans %+v", res.Findings, found[archive.FindingOrphan])
	}

	res, found = e.verify(archive.VerifyOptions{Jobs: 1, Encryption: store.BlobEncrypting})
	if len(found[archive.FindingPlaintext]) != 0 || res.Plaintext != 2 {
		t.Fatalf("while encrypting: %d findings, %d counted", len(found[archive.FindingPlaintext]), res.Plaintext)
	}
	res, found = e.verify(archive.VerifyOptions{Jobs: 1})
	if len(found[archive.FindingPlaintext]) != 0 || res.Plaintext != 0 {
		t.Fatalf("off: %v", found)
	}
}

// The stored path is informational: rows written with the .eml path keep
// it after their file was encrypted, rows written later have .enc; both
// are no bad-path.
func TestVerifyBadPathAcceptsBothForms(t *testing.T) {
	e := newArchiveEnv(t)
	e.encrypted()
	b := e.save(e.folder, 1, msg(1))
	e.saveRow(e.folder, 2, store.MessageMeta{SHA256: b.SHA256, Size: b.Size, StoredPath: blobstore.RelPath(b.SHA256)})
	c, _, err := e.blobs.Put(strings.NewReader(msg(2)))
	if err != nil {
		t.Fatal(err)
	}
	e.saveRow(e.folder, 3, store.MessageMeta{SHA256: c.SHA256, Size: c.Size, StoredPath: "messages/elsewhere.eml"})
	_, found := e.verify(archive.VerifyOptions{Jobs: 1, Encryption: store.BlobOn})
	if len(found[archive.FindingBadPath]) != 1 || found[archive.FindingBadPath][0].SHA256 != c.SHA256 {
		t.Fatalf("bad-path %+v", found[archive.FindingBadPath])
	}
}

// Without the key, verify ends after the attestation check (database
// only) with the reason, before it reads a message file.
func TestVerifyMissingKey(t *testing.T) {
	e := newArchiveEnv(t)
	e.encrypted()
	e.save(e.folder, 1, msg(1))
	res, err := archive.Verify(e.ctx, e.st, e.blobs, archive.VerifyOptions{
		Jobs: 1, Encryption: store.BlobOn, KeyMissing: archive.ErrNoArchiveKey, Logger: quiet,
		Attest: &archive.AttestCheckOptions{Dir: filepath.Join(e.data, "attest")},
	})
	if !errors.Is(err, archive.ErrNoArchiveKey) || res == nil || res.Checked != 0 {
		t.Fatalf("verify without the key: %+v, %v", res, err)
	}
}

// Export writes the plaintext: from an encrypted archive the output is
// byte for byte the output of the same plaintext archive.
func TestExportEncryptedByteIdentical(t *testing.T) {
	raws := []string{
		"Subject: one\r\n\r\nFrom the start\r\n>From quoted\r\n",
		"Subject: two\n\nLF only\n\n",
		strings.Repeat("Subject: large\r\n", 9000),
	}
	exportAll := func(e *archiveEnv) (mbox []byte, maildir [][]byte) {
		t.Helper()
		for i, raw := range raws {
			e.save(e.folder, uint32(i+1), raw, `\Seen`)
		}
		out := filepath.Join(t.TempDir(), "mbox")
		if res, err := e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: out}); err != nil || len(res.Skipped) != 0 {
			t.Fatalf("mbox: %+v %v", res, err)
		}
		mbox, err := os.ReadFile(filepath.Join(out, "mail", "INBOX.mbox"))
		if err != nil {
			t.Fatal(err)
		}
		out = filepath.Join(t.TempDir(), "maildir")
		if _, err := e.export(archive.ExportOptions{Format: archive.FormatMaildir, Out: out}); err != nil {
			t.Fatal(err)
		}
		for _, sub := range []string{"cur", "new"} {
			entries, _ := os.ReadDir(filepath.Join(out, "mail", "INBOX", sub))
			for _, d := range entries {
				b, err := os.ReadFile(filepath.Join(out, "mail", "INBOX", sub, d.Name()))
				if err != nil {
					t.Fatal(err)
				}
				maildir = append(maildir, b)
			}
		}
		slices.SortFunc(maildir, bytes.Compare)
		return mbox, maildir
	}
	plain := newArchiveEnv(t)
	wantMbox, wantMaildir := exportAll(plain)
	enc := newArchiveEnv(t)
	enc.encrypted()
	gotMbox, gotMaildir := exportAll(enc)
	if !bytes.Equal(gotMbox, wantMbox) {
		t.Fatalf("mbox differs: %d bytes, want %d", len(gotMbox), len(wantMbox))
	}
	if len(gotMaildir) != len(raws) || !slices.EqualFunc(gotMaildir, wantMaildir, bytes.Equal) {
		t.Fatal("maildir files differ")
	}
}

// An encrypted file that fails authentication is skipped with a reason,
// as a corrupt plaintext file is.
func TestExportSkipsCorruptEncrypted(t *testing.T) {
	e := newArchiveEnv(t)
	e.encrypted()
	good := e.save(e.folder, 1, msg(1))
	bad := e.save(e.folder, 2, msg(2))
	data, err := os.ReadFile(e.path(bad.Path))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(e.path(bad.Path), data, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	res, err := e.export(archive.ExportOptions{Format: archive.FormatMbox, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 1 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "corrupt") {
		t.Fatalf("result %+v", res)
	}
	if got := readMbox(t, filepath.Join(out, "mail", "INBOX.mbox")); !slices.Equal(got, []string{good.SHA256}) {
		t.Fatalf("exported %v", got)
	}
}
