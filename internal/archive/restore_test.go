package archive_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/store"
)

func (e *archiveEnv) reconcileRestored(kek []byte) (*archive.RestoreReport, error) {
	e.t.Helper()
	return archive.ReconcileRestored(e.ctx, e.st, e.blobs, archive.RestoreOptions{DataDir: e.data, KEK: kek})
}

func reportText(r *archive.RestoreReport) string { return strings.Join(r.Lines("./ma"), "\n") }

// A plaintext archive restored: nothing to reconcile, nothing written.
func TestReconcileRestoredOff(t *testing.T) {
	e := newArchiveEnv(t)
	rep, err := e.reconcileRestored(nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.StateAfter != store.BlobOff || rep.DBWritten || rep.FileWritten || !strings.Contains(reportText(rep), "nothing to reconcile") {
		t.Errorf("report %+v\n%s", rep, reportText(rep))
	}
	if _, err := os.Stat(blobcrypt.KeyringPath(e.data)); !errors.Is(err, os.ErrNotExist) {
		t.Error("a keyring file appeared")
	}
}

// A dump that recorded off, restored next to a data directory whose files
// were encrypted later: the state is derived from the files (encrypting),
// even when the dump's keyring copy matches the file, and the report says
// how to finish (revision 1, B2).
func TestReconcileRestoredDerivesFromFiles(t *testing.T) {
	e := newArchiveEnv(t)
	kek, ring := e.keygen()
	if err := e.st.RecordBlobOff(e.ctx, keyringCopy(t, ring)); err != nil {
		t.Fatal(err)
	}
	e.putEncrypted(ring, msg(1))
	rep, err := e.reconcileRestored(kek)
	if err != nil {
		t.Fatal(err)
	}
	row, err := e.st.BlobEncryption(e.ctx)
	if err != nil || row == nil || row.State != store.BlobEncrypting {
		t.Fatalf("state after: %+v %v", row, err)
	}
	text := reportText(rep)
	if !rep.Derived || rep.StateBefore != store.BlobOff || !strings.Contains(text, "run `./ma encryption enable` to finish") {
		t.Errorf("report %+v\n%s", rep, text)
	}
}

// The data directory's keyring is newer than the dump's copy (a recovery
// key added after the backup): the file wins and is written to the
// database; the slot survives.
func TestReconcileRestoredFileNewer(t *testing.T) {
	e := newArchiveEnv(t)
	kek, ring := e.keygen()
	if err := e.st.SetBlobEncryption(e.ctx, store.BlobOn, keyringCopy(t, ring)); err != nil {
		t.Fatal(err)
	}
	rk, _ := blobcrypt.GenerateKEK()
	if err := ring.AddSlot(rk, "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(e.data), ring); err != nil {
		t.Fatal(err)
	}
	rep, err := e.reconcileRestored(kek)
	if err != nil {
		t.Fatal(err)
	}
	text := reportText(rep)
	if !rep.DBWritten || rep.FileWritten || rep.DBAfter.Generation != ring.Generation() || !strings.Contains(text, "the keyring file is newer") {
		t.Errorf("report %+v\n%s", rep, text)
	}
	row, err := e.st.BlobEncryption(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	db, err := blobcrypt.ParseKeyring(row.Keyring)
	if err != nil {
		t.Fatal(err)
	}
	if !db.HasSlot(blobcrypt.Fingerprint(rk)) {
		t.Error("the recovery key is not in the database copy")
	}
	if row.State != store.BlobOn {
		t.Errorf("state %q", row.State)
	}
}

// The dump's copy is newer than the data directory's keyring (a data
// directory copied before a key was added): the dump's copy is written to
// the file.
func TestReconcileRestoredDumpNewer(t *testing.T) {
	e := newArchiveEnv(t)
	kek, ring := e.keygen()
	rk, _ := blobcrypt.GenerateKEK()
	if err := ring.AddSlot(rk, "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetBlobEncryption(e.ctx, store.BlobOn, keyringCopy(t, ring)); err != nil {
		t.Fatal(err)
	}
	rep, err := e.reconcileRestored(kek)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.FileWritten || rep.DBWritten || !strings.Contains(reportText(rep), "the dump's copy is newer") {
		t.Errorf("report %+v\n%s", rep, reportText(rep))
	}
	file, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(e.data))
	if err != nil || !file.HasSlot(blobcrypt.Fingerprint(rk)) {
		t.Errorf("the keyring file lacks the recovery key: %v", err)
	}
}

// A data directory without a keyring file gets it back from the dump.
func TestReconcileRestoredFileMissing(t *testing.T) {
	e := newArchiveEnv(t)
	kek := e.encryption(store.BlobOn)
	if err := os.Remove(blobcrypt.KeyringPath(e.data)); err != nil {
		t.Fatal(err)
	}
	rep, err := e.reconcileRestored(kek)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.FileWritten || rep.FileBefore.Present || !strings.Contains(reportText(rep), "rebuilt it from the database") {
		t.Errorf("report %+v\n%s", rep, reportText(rep))
	}
	if _, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(e.data)); err != nil {
		t.Error(err)
	}
}

// Refused: a keyring file of another archive, and encryption on without
// the archive key. Nothing is written.
func TestReconcileRestoredRefusals(t *testing.T) {
	e := newArchiveEnv(t)
	kek := e.encryption(store.BlobOn)
	before, _ := e.st.BlobEncryption(e.ctx)

	if _, err := e.reconcileRestored(nil); !errors.Is(err, archive.ErrNoArchiveKey) {
		t.Errorf("without the key: %v", err)
	}

	other, err := blobcrypt.NewKeyring(kek, "archive key", t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(e.data), other); err != nil {
		t.Fatal(err)
	}
	fileBefore, _ := os.ReadFile(blobcrypt.KeyringPath(e.data))
	rep, err := e.reconcileRestored(kek)
	if !errors.Is(err, blobcrypt.ErrOtherArchive) {
		t.Fatalf("another archive's keyring: %v", err)
	}
	if rep == nil || !strings.Contains(reportText(rep), "restored database: encryption at rest on") {
		t.Errorf("the report of the refusal: %+v", rep)
	}
	after, _ := e.st.BlobEncryption(e.ctx)
	fileAfter, _ := os.ReadFile(blobcrypt.KeyringPath(e.data))
	if string(after.Keyring) != string(before.Keyring) || string(fileAfter) != string(fileBefore) {
		t.Error("a refused reconciliation wrote a copy")
	}
}
