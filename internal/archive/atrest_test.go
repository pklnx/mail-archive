package archive_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

var t0 = time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)

// newAtRestEnv is newArchiveEnv with the database URL, for a second
// connection.
func newAtRestEnv(t *testing.T) (*archiveEnv, string) {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	e := &archiveEnv{t: t, ctx: context.Background(), st: st, data: t.TempDir()}
	var err error
	if e.blobs, err = blobstore.New(e.data); err != nil {
		t.Fatal(err)
	}
	if e.user, err = e.st.CreateUser(e.ctx, "anna", "h", false); err != nil {
		t.Fatal(err)
	}
	e.acc = e.account(e.user, "mail")
	e.folder = e.folderOf(e.acc, "INBOX", 1)
	return e, url
}

// keygen writes a new keyring file into the data directory, as
// `encryption keygen` does, and returns the archive key.
func (e *archiveEnv) keygen() ([]byte, *blobcrypt.Keyring) {
	e.t.Helper()
	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		e.t.Fatal(err)
	}
	ring, err := blobcrypt.NewKeyring(kek, "archive key", t0)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(e.data), ring); err != nil {
		e.t.Fatal(err)
	}
	return kek, ring
}

func keyringCopy(t *testing.T, ring *blobcrypt.Keyring) store.KeyringCopy {
	t.Helper()
	data, err := ring.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return store.KeyringCopy{Keyring: data, ID: ring.ArchiveID(), Generation: int64(ring.Generation()), CurrentKey: ring.CurrentKey()}
}

// encryption records state with the keyring as the database copy, as part
// 3's commands do, and returns the archive key.
func (e *archiveEnv) encryption(state string) []byte {
	e.t.Helper()
	kek, ring := e.keygen()
	if err := e.st.SetBlobEncryption(e.ctx, state, keyringCopy(e.t, ring)); err != nil {
		e.t.Fatal(err)
	}
	return kek
}

// lost collects the OnLost calls of a writer.
type lost struct {
	errs chan error
}

func newLost() *lost { return &lost{errs: make(chan error, 4)} }

func (l *lost) on(err error) { l.errs <- err }

func (l *lost) wait(t *testing.T, what string) error {
	t.Helper()
	select {
	case err := <-l.errs:
		return err
	case <-time.After(testwait.Limit(t)):
		t.Fatalf("%s: the writer did not stop", what)
		return nil
	}
}

func (e *archiveEnv) atRest(role archive.AtRestRole, kek []byte, tick <-chan time.Time, l *lost) (*archive.AtRest, error) {
	e.t.Helper()
	opts := archive.AtRestOptions{DataDir: e.data, KEK: kek, Role: role, Command: "serve", Tick: tick, Logger: quiet}
	if l != nil {
		opts.OnLost = l.on
	}
	if tick == nil {
		opts.Tick = make(chan time.Time) // never ticks
	}
	a, err := archive.OpenAtRest(e.ctx, e.st, e.blobs, opts)
	if err == nil {
		e.t.Cleanup(a.Close)
	}
	return a, err
}

func (e *archiveEnv) exists(rel string) bool {
	_, err := os.Lstat(e.path(rel))
	return err == nil
}

// A writer started with encryption on writes every new file encrypted, and
// the readers read it by its hash.
func TestAtRestWriterEncryptsWhenOn(t *testing.T) {
	e := newArchiveEnv(t)
	kek := e.encryption(store.BlobOn)
	a, err := e.atRest(archive.RoleWriter, kek, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != store.BlobOn || a.Generation != 1 || a.Keyring == nil || !e.blobs.Encrypts() {
		t.Fatalf("state %q generation %d encrypts %v", a.State, a.Generation, e.blobs.Encrypts())
	}
	raw := "Subject: Kontoauszug Zebrafink\r\n\r\nGeheim\r\n"
	b := e.save(e.folder, 1, raw)
	if b.Path != blobstore.EncPath(b.SHA256) || !e.exists(b.Path) || e.exists(blobstore.RelPath(b.SHA256)) {
		t.Fatalf("blob %+v", b)
	}
	data, err := os.ReadFile(e.path(b.Path))
	if err != nil || bytes.Contains(data, []byte("Zebrafink")) {
		t.Fatalf("plaintext on disk: %v", err)
	}
	meta, err := archive.BuildMeta(e.ctx, e.st, e.blobs, b, true)
	if err != nil || meta.Subject != "Kontoauszug Zebrafink" {
		t.Fatalf("BuildMeta from the encrypted file: %q, %v", meta.Subject, err)
	}
}

func TestAtRestOff(t *testing.T) {
	e := newArchiveEnv(t)
	a, err := e.atRest(archive.RoleWriter, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != store.BlobOff || a.Generation != 0 || a.Keyring != nil || e.blobs.Encrypts() {
		t.Fatalf("off: %+v", a)
	}
	if b := e.save(e.folder, 1, msg(1)); b.Path != blobstore.RelPath(b.SHA256) {
		t.Fatalf("path %s", b.Path)
	}
}

// Writers refuse to start while an offline conversion is unfinished;
// readers start (maintainer decision 5).
func TestAtRestTransitionalStatesRefuseWriters(t *testing.T) {
	for state, cmd := range map[string]string{store.BlobEncrypting: "enable", store.BlobDecrypting: "disable", store.BlobRekeying: "rekey"} {
		t.Run(state, func(t *testing.T) {
			e := newArchiveEnv(t)
			kek := e.encryption(state)
			_, err := e.atRest(archive.RoleWriter, kek, nil, nil)
			if !errors.Is(err, archive.ErrConversionInProgress) || !strings.Contains(err.Error(), "encryption "+cmd) {
				t.Fatalf("writer: %v", err)
			}
			a, err := e.atRest(archive.RoleReader, kek, nil, nil)
			if err != nil || a.State != state || e.blobs.Encrypts() {
				t.Fatalf("reader: %v", err)
			}
		})
	}
}

// With encryption on, every command that reads or writes message files
// needs the archive key; verify starts and reports it after the
// attestation check.
func TestAtRestKeyRequired(t *testing.T) {
	e := newArchiveEnv(t)
	e.encryption(store.BlobOn)
	for _, role := range []archive.AtRestRole{archive.RoleWriter, archive.RoleReader} {
		if _, err := e.atRest(role, nil, nil, nil); !errors.Is(err, archive.ErrNoArchiveKey) {
			t.Fatalf("role %d without a key: %v", role, err)
		}
	}
	a, err := e.atRest(archive.RoleVerify, nil, nil, nil)
	if err != nil || !errors.Is(a.KeyMissing, archive.ErrNoArchiveKey) {
		t.Fatalf("verify without a key: %v, %v", err, a)
	}
	other, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.atRest(archive.RoleWriter, other, nil, nil); !errors.Is(err, blobcrypt.ErrNoSlot) {
		t.Fatalf("another key: %v", err)
	}
	// Nothing was written for the refused writers.
	if e.blobs.Encrypts() {
		t.Fatal("a refused start configured the store")
	}
}

// The writers lock: a writer cannot start while an encryption command
// holds it exclusively, and the command cannot take it while a writer
// runs; each refusal names the holder.
func TestAtRestWritersLock(t *testing.T) {
	e := newArchiveEnv(t)
	cmd, err := e.st.TryLockWritersExclusive(e.ctx, "enable")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.atRest(archive.RoleWriter, nil, nil, nil)
	if !errors.Is(err, store.ErrWritersExcluded) || !strings.Contains(err.Error(), `"mail-archive encryption enable"`) {
		t.Fatalf("writer during a conversion: %v", err)
	}
	cmd.Release()

	a, err := e.atRest(archive.RoleWriter, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second writer shares it.
	b, err := e.st.HoldWriters(e.ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.st.TryLockWritersExclusive(e.ctx, "enable")
	if !errors.Is(err, store.ErrWritersRunning) || !strings.Contains(err.Error(), `"mail-archive writer serve"`) ||
		!strings.Contains(err.Error(), `"mail-archive writer worker"`) {
		t.Fatalf("encryption command while writers run: %v", err)
	}
	a.Close()
	b.Release()
	cmd, err = e.st.TryLockWritersExclusive(e.ctx, "enable")
	if err != nil {
		t.Fatalf("after the writers stopped: %v", err)
	}
	cmd.Release()
}

// A writer whose lock session ends (postgres restart, pg_terminate_backend,
// a network blip) stops at its next check, and the encryption command then
// waits for the restarted writer (revision 5, B1 (a), (b)).
func TestAtRestLockLossStopsWriter(t *testing.T) {
	e, url := newAtRestEnv(t)
	tick := make(chan time.Time)
	l := newLost()
	a, err := e.atRest(archive.RoleWriter, nil, tick, l)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(e.ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(e.ctx) }()
	var pid uint32
	if err := admin.QueryRow(e.ctx, `SELECT pid FROM pg_stat_activity WHERE application_name = $1`,
		store.WriterAppName("serve")).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var ok bool
	if err := admin.QueryRow(e.ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&ok); err != nil || !ok {
		t.Fatalf("terminate: %v %v", ok, err)
	}
	tick <- time.Now()
	if err := l.wait(t, "lock loss"); !errors.Is(err, store.ErrWritersLockLost) {
		t.Fatalf("onLost: %v", err)
	}
	a.Close()

	// The restarted writer holds the lock again; the command refuses.
	if _, err := e.atRest(archive.RoleWriter, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.TryLockWritersExclusive(e.ctx, "enable"); !errors.Is(err, store.ErrWritersRunning) {
		t.Fatalf("command after the restart: %v", err)
	}
}

// Counter-check: a live connection is no loss.
func TestAtRestLockCheckPasses(t *testing.T) {
	e := newArchiveEnv(t)
	tick := make(chan time.Time)
	l := newLost()
	if _, err := e.atRest(archive.RoleWriter, nil, tick, l); err != nil {
		t.Fatal(err)
	}
	// The first check has finished when the second tick is taken; a check
	// that reported a loss has ended the watch, so the second tick waits
	// for ever: either way the loss arrives instead.
	for range 2 {
		select {
		case tick <- time.Now():
		case err := <-l.errs:
			t.Fatalf("a live lock reported lost: %v", err)
		case <-time.After(testwait.Limit(t)):
			t.Fatal("the watch took no tick")
		}
	}
	select {
	case err := <-l.errs:
		t.Fatalf("a live lock reported lost: %v", err)
	default:
	}
}

// A writer that missed a state change (its lock was gone before its watch
// noticed) cannot insert: the transaction fails and the process stops
// (revision 5, B1 (c); final plan review, point 1).
func TestAtRestGenerationChangeStopsWriter(t *testing.T) {
	e := newArchiveEnv(t)
	kek := e.encryption(store.BlobOn)
	l := newLost()
	if _, err := e.atRest(archive.RoleWriter, kek, nil, l); err != nil {
		t.Fatal(err)
	}
	e.save(e.folder, 1, msg(1))
	// A state change behind the writer's back.
	if err := e.st.SetBlobEncryption(e.ctx, store.BlobDecrypting, store.KeyringCopy{}); err != nil {
		t.Fatal(err)
	}
	b, _, err := e.blobs.Put(strings.NewReader(msg(2)))
	if err != nil {
		t.Fatal(err)
	}
	loc := store.Location{FolderID: e.folder.ID, UIDValidity: 1, UID: 2, InternalDate: t0}
	_, err = e.st.SaveBatch(e.ctx, e.folder.ID, 2, []store.MessageMeta{{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path}}, []store.Location{loc})
	if !errors.Is(err, store.ErrBlobStateChanged) {
		t.Fatalf("insert after the change: %v", err)
	}
	if err := l.wait(t, "generation change"); !errors.Is(err, store.ErrBlobStateChanged) {
		t.Fatalf("onLost: %v", err)
	}
	if ok, err := e.st.MessageExists(e.ctx, b.SHA256); err != nil || ok {
		t.Fatalf("the row was committed: %v %v", ok, err)
	}
}

// The keyring file and its database copy reconcile at start: a missing
// file is rebuilt, the newer copy is written to the older one.
func TestAtRestReconcilesKeyring(t *testing.T) {
	e := newArchiveEnv(t)
	kek := e.encryption(store.BlobOn)
	path := blobcrypt.KeyringPath(e.data)
	dbCopy := func() *blobcrypt.Keyring {
		t.Helper()
		row, err := e.st.BlobEncryption(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		k, err := blobcrypt.ParseKeyring(row.Keyring)
		if err != nil {
			t.Fatal(err)
		}
		if err := k.Open(kek); err != nil {
			t.Fatal(err)
		}
		if row.KeyringGeneration != int64(k.Generation()) || row.CurrentKey != k.CurrentKey() || row.KeyringID != k.ArchiveID() {
			t.Fatalf("row metadata %d %08x %s", row.KeyringGeneration, row.CurrentKey, row.KeyringID)
		}
		return k
	}
	fileCopy := func() *blobcrypt.Keyring {
		t.Helper()
		k, err := blobcrypt.ReadKeyringFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := k.Open(kek); err != nil {
			t.Fatal(err)
		}
		return k
	}
	start := func() {
		t.Helper()
		a, err := e.atRest(archive.RoleReader, kek, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		a.Close()
	}

	// The file is lost (data directory restored without keys/): rebuilt.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	start()
	if !fileCopy().SameAs(dbCopy()) {
		t.Fatal("file not rebuilt from the database copy")
	}

	gen1, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A recovery key added to the file (encryption add-key needs no
	// database): the database copy follows.
	k := fileCopy()
	if err := k.AddSlot(bytes.Repeat([]byte{9}, blobcrypt.KeySize), "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(path, k); err != nil {
		t.Fatal(err)
	}
	start()
	if got := dbCopy(); !got.SameAs(fileCopy()) || got.Generation() != 2 || len(got.Slots()) != 2 {
		t.Fatalf("database copy: generation %d, %d slots", got.Generation(), len(got.Slots()))
	}

	// An older file (data directory from a backup before add-key): the
	// newer database copy wins.
	if err := os.WriteFile(path, gen1, 0o600); err != nil {
		t.Fatal(err)
	}
	start()
	if got := fileCopy(); got.Generation() != 2 || len(got.Slots()) != 2 {
		t.Fatalf("file: generation %d, %d slots", got.Generation(), len(got.Slots()))
	}

	// The keyring of another archive is refused, and stays as it is.
	other, err := blobcrypt.NewKeyring(kek, "archive key", t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(path, other); err != nil {
		t.Fatal(err)
	}
	if _, err := e.atRest(archive.RoleReader, kek, nil, nil); !errors.Is(err, blobcrypt.ErrOtherArchive) {
		t.Fatalf("other archive: %v", err)
	}
	if got := fileCopy(); got.ArchiveID() != other.ArchiveID() {
		t.Fatal("the other archive's keyring was overwritten")
	}
}

// After a dump from before `enable` was restored next to newer encrypted
// files, the state is derived from the files: the writer records
// encrypting and refuses, so that `encryption enable` finishes the
// conversion (revision 1, B2).
func TestAtRestDerivesStateFromFiles(t *testing.T) {
	e := newArchiveEnv(t)
	kek, ring := e.keygen()
	// No encrypted file: off, nothing recorded.
	if a, err := e.atRest(archive.RoleWriter, kek, nil, nil); err != nil || a.State != store.BlobOff {
		t.Fatalf("no encrypted file: %v", err)
	} else {
		a.Close()
	}
	if row, err := e.st.BlobEncryption(e.ctx); err != nil || row != nil {
		t.Fatalf("row %+v %v", row, err)
	}

	enc, err := blobstore.New(e.data)
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.SetKeys(ring, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := enc.Put(strings.NewReader(msg(1))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.atRest(archive.RoleWriter, kek, nil, nil); !errors.Is(err, archive.ErrConversionInProgress) {
		t.Fatalf("writer with encrypted files: %v", err)
	}
	row, err := e.st.BlobEncryption(e.ctx)
	if err != nil || row == nil || row.State != store.BlobEncrypting || row.KeyringID != ring.ArchiveID() {
		t.Fatalf("derived row %+v %v", row, err)
	}
	file, err := os.ReadFile(blobcrypt.KeyringPath(e.data))
	if err != nil || !bytes.Equal(file, row.Keyring) {
		t.Fatalf("the database copy is not the file: %v", err)
	}
}

// With encryption off, a reader with the key still reads encrypted files
// left over.
func TestAtRestOffReadsLeftoverEncrypted(t *testing.T) {
	e := newArchiveEnv(t)
	kek, ring := e.keygen()
	enc, err := blobstore.New(e.data)
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.SetKeys(ring, true); err != nil {
		t.Fatal(err)
	}
	b, _, err := enc.Put(strings.NewReader(msg(1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.atRest(archive.RoleReader, kek, nil, nil); err != nil {
		t.Fatal(err)
	}
	m, err := e.blobs.OpenMessage(b.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	if e.blobs.Encrypts() {
		t.Fatal("off but encrypting")
	}
}
