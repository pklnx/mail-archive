package archive_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

func (e *archiveEnv) rekeyOpts(kek []byte, out *bytes.Buffer) archive.RekeyOptions {
	o := archive.RekeyOptions{ConvertOptions: e.convertOpts(kek, nil)}
	if out != nil {
		o.Out = out
	}
	return o
}

// enabled is an archive with n messages, encrypted by Enable under the
// keyring's first data key; it returns the messages, the archive key and
// that key's id.
func (e *archiveEnv) enabled(n int) (map[string]string, []byte, *blobcrypt.Keyring) {
	e.t.Helper()
	msgs := e.seed(n)
	kek, ring := e.keygen()
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil {
		e.t.Fatal(err)
	}
	return msgs, kek, ring
}

// keyOf is the data key the header of hash's encrypted file names.
func (e *archiveEnv) keyOf(hash string) uint32 {
	e.t.Helper()
	b, err := os.ReadFile(e.path(blobstore.EncPath(hash)))
	if err != nil {
		e.t.Fatal(err)
	}
	h, err := blobcrypt.ParseHeader(b)
	if err != nil {
		e.t.Fatal(err)
	}
	return h.KeyID
}

// copies are both copies of the keyring, opened with kek.
func (e *archiveEnv) copies(kek []byte) (file, db *blobcrypt.Keyring) {
	e.t.Helper()
	file, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(e.data))
	if err != nil {
		e.t.Fatal(err)
	}
	if db, err = blobcrypt.ParseKeyring(e.row().Keyring); err != nil {
		e.t.Fatal(err)
	}
	for _, k := range []*blobcrypt.Keyring{file, db} {
		if err := k.Open(kek); err != nil {
			e.t.Fatal(err)
		}
	}
	return file, db
}

// hasKey reports whether both copies hold the data key id (and fails when
// they disagree).
func (e *archiveEnv) hasKey(kek []byte, id uint32) bool {
	e.t.Helper()
	file, db := e.copies(kek)
	_, inFile := file.DataKey(id)
	_, inDB := db.DataKey(id)
	if inFile != inDB {
		e.t.Fatalf("data key %08x: in the file %v, in the database %v", id, inFile, inDB)
	}
	return inFile
}

// rekey re-encrypts every file under a new data key, with a new keyring
// key: the old data key and the slots not kept are gone from both copies,
// a new recovery key opens them, a writer started afterwards encrypts
// under the new key, and every message reads back.
func TestRekeyArchive(t *testing.T) {
	e := newArchiveEnv(t)
	msgs, kek, ring := e.enabled(20)
	oldKey := ring.CurrentKey()
	oldRecovery, _ := blobcrypt.GenerateKEK()
	if _, err := archive.ChangeKeyring(e.ctx, e.st, archive.KeyringChangeOptions{DataDir: e.data, KEK: kek, Command: "add-key", Logger: quiet},
		func(k *blobcrypt.Keyring) error { return k.AddSlot(oldRecovery, "recovery key", t0) }); err != nil {
		t.Fatal(err)
	}
	gen := e.row().Generation

	var out bytes.Buffer
	opts := e.rekeyOpts(kek, &out)
	opts.BatchFiles, opts.Recovery = 6, true
	res, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), opts)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if res.State != store.BlobOn || !res.Started || res.OldKey != oldKey || res.NewKey == oldKey || res.Converted != 20 ||
		res.Passes != 2 || !slices.Equal(res.Deleted, []uint32{oldKey}) || len(res.Retained) != 0 || res.RecoveryKey == nil {
		t.Fatalf("result %+v", res)
	}
	if len(res.Dropped) != 1 || res.Dropped[0].Fingerprint != blobcrypt.Fingerprint(oldRecovery) {
		t.Fatalf("dropped %d slot(s), want the old recovery key's", len(res.Dropped))
	}
	if r := e.row(); r.State != store.BlobOn || r.Generation != gen+2 || r.CurrentKey != res.NewKey || r.Cursor != "" {
		t.Fatalf("row %+v (generation before %d)", r, gen)
	}
	for h := range msgs {
		if id := e.keyOf(h); id != res.NewKey {
			t.Fatalf("%s names %08x", h[:8], id)
		}
	}
	if e.hasKey(kek, oldKey) || !e.hasKey(kek, res.NewKey) {
		t.Fatal("old key kept or new key missing")
	}
	file, db := e.copies(res.RecoveryKey)
	if file.CurrentKey() != res.NewKey || db.CurrentKey() != res.NewKey {
		t.Fatal("the new recovery key does not open the new keyring")
	}
	for _, k := range []*blobcrypt.Keyring{file, db} {
		k2, _ := blobcrypt.ParseKeyring(must(k.Marshal()))
		if err := k2.Open(oldRecovery); !errors.Is(err, blobcrypt.ErrNoSlot) {
			t.Errorf("the dropped key opens a copy: %v", err)
		}
	}
	e.readAll(kek, msgs)
	for _, want := range []string{"on → rekeying", "re-encrypting: 100%", "dropped the slot of key " + blobcrypt.Fingerprint(oldRecovery)} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	a := e.writerStart(kek)
	if a.State != store.BlobOn || a.Keyring.CurrentKey() != res.NewKey {
		t.Fatalf("writer after rekey: %+v", a)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// encBefore writes a message file under the data key of ring's current key
// whose hash sorts before cursor (behind it) and returns its hash and
// content.
func (e *archiveEnv) encBefore(ring *blobcrypt.Keyring, cursor string) (string, string) {
	e.t.Helper()
	for i := 0; ; i++ {
		raw := fmt.Sprintf("Subject: Nachzuegler %d\r\n\r\n%s\r\n", i, convertMarker)
		s := sha256.Sum256([]byte(raw))
		h := hex.EncodeToString(s[:])
		if h >= cursor {
			continue
		}
		b := e.newBlobs()
		if err := b.SetKeys(ring, true); err != nil {
			e.t.Fatal(err)
		}
		if _, _, err := b.Put(strings.NewReader(raw)); err != nil {
			e.t.Fatal(err)
		}
		return h, raw
	}
}

// An interrupted rekey resumes, and the old data key is deleted only
// after a complete pass from the start finds no file naming it (#437,
// Done when): it stays in both copies through every batch, and a
// straggler under the old key behind the cursor, which the resumed pass
// does not reach, is re-encrypted by the full pass before the key goes.
// Counter-proof: ending the run after a resumed pass that converted
// nothing (dropping `clean &&` in converter.run) deletes the key while
// the straggler still names it, so the straggler no longer reads.
func TestRekeyDeletesOldKeyOnlyAfterCleanPass(t *testing.T) {
	e := newArchiveEnv(t)
	msgs, kek, ring := e.enabled(12)
	oldKey := ring.CurrentKey()

	opts := e.rekeyOpts(kek, nil)
	// Batches 1–3 hold 4 files each; batch 3 ends with every file done and
	// a cursor at the last hash.
	opts.BatchFiles, opts.AfterBatch = 4, interruptAfter(3)
	if _, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), opts); !errors.Is(err, errInterrupt) {
		t.Fatalf("first run: %v", err)
	}
	r := e.row()
	if r.State != store.BlobRekeying || !blobstore.IsHash(r.Cursor) || r.FilesDone != 12 {
		t.Fatalf("after the interruption: %+v", r)
	}
	if !e.hasKey(kek, oldKey) {
		t.Fatal("the old key is gone before the run finished")
	}
	if _, err := e.atRest(archive.RoleWriter, kek, nil, nil); !errors.Is(err, archive.ErrConversionInProgress) {
		t.Fatalf("a writer during the rekey: %v", err)
	}
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); !errors.Is(err, archive.ErrRekeyInProgress) {
		t.Fatalf("enable during the rekey: %v", err)
	}
	h, raw := e.encBefore(ring, r.Cursor) // ring: the old key is current there
	msgs[h] = raw

	opts.AfterBatch = func(int) error {
		if !e.hasKey(kek, oldKey) {
			return errors.New("the old key was deleted before the end")
		}
		return nil
	}
	var out bytes.Buffer
	opts.Out = &out
	res, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), opts)
	if err != nil {
		t.Fatalf("rerun: %v\n%s", err, out.String())
	}
	if res.Started || res.State != store.BlobOn || res.Converted != 1 || res.Passes != 3 || !slices.Equal(res.Deleted, []uint32{oldKey}) {
		t.Fatalf("rerun: %+v", res)
	}
	// The resumed run lists the slots and says how to get a recovery key
	// if the interrupted start's one was never shown (review round 1 of
	// #519). Counter-proof: without resumeNote, the lines are missing.
	for _, want := range []string{"resuming after " + r.Cursor, "the keyring's slots:", blobcrypt.Fingerprint(kek), "add one with `encryption add-key`"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if e.keyOf(h) != res.NewKey || e.hasKey(kek, oldKey) {
		t.Fatal("straggler not re-encrypted, or old key kept")
	}
	e.readAll(kek, msgs)
}

// A file that cannot be decrypted is left as it is and listed, and the old
// key it names stays in the keyring; the rekey still ends on.
// Counter-proof: deleting every old key regardless of the named ones
// drops the key (the assertion on Retained and on both copies fails).
func TestRekeyKeepsKeyOfUndecryptableFile(t *testing.T) {
	e := newArchiveEnv(t)
	msgs, kek, ring := e.enabled(5)
	oldKey := ring.CurrentKey()
	var damaged string
	for h := range msgs {
		damaged = h
		break
	}
	p := e.path(blobstore.EncPath(damaged))
	data, _ := os.ReadFile(p)
	data[blobcrypt.HeaderSize+1] ^= 1
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	res, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), e.rekeyOpts(kek, &out))
	if err != nil || res.State != store.BlobOn {
		t.Fatalf("%+v %v", res, err)
	}
	if !slices.Equal(res.Retained, []uint32{oldKey}) || len(res.Deleted) != 0 || !slices.Equal(res.Corrupt, []string{blobstore.EncPath(damaged)}) {
		t.Fatalf("result %+v", res)
	}
	if !e.hasKey(kek, oldKey) {
		t.Fatal("the key of the undecryptable file was deleted")
	}
	if after, _ := os.ReadFile(p); !bytes.Equal(after, data) {
		t.Fatal("the undecryptable file changed")
	}
	if !strings.Contains(out.String(), "cannot re-encrypt "+blobstore.EncPath(damaged)) {
		t.Errorf("output: %s", out.String())
	}
	delete(msgs, damaged)
	e.readAll(kek, msgs)
}

// The end writes the keyring file first and then the database copy with
// the state on. Interrupted in between, the state stays rekeying; the
// rerun reconciles the copies (the union brings the old key back) and
// deletes it again, so both copies end without it.
// Counter-proof: recording the state on before the file (swapping the two
// writes in finishRekey) leaves the state on after the interruption.
func TestRekeyInterruptedAtTheEnd(t *testing.T) {
	e := newArchiveEnv(t)
	msgs, kek, ring := e.enabled(4)
	oldKey := ring.CurrentKey()
	opts := e.rekeyOpts(kek, nil)
	opts.AfterFinalFile = func() error { return errInterrupt }
	if _, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), opts); !errors.Is(err, errInterrupt) {
		t.Fatalf("first run: %v", err)
	}
	if r := e.row(); r.State != store.BlobRekeying {
		t.Fatalf("state %s after an interruption at the end", r.State)
	}
	file, db := e.copies(kek)
	if _, ok := file.DataKey(oldKey); ok {
		t.Fatal("the file still has the old key")
	}
	if _, ok := db.DataKey(oldKey); !ok {
		t.Fatal("the database copy lost the old key before the state on")
	}
	opts.AfterFinalFile = nil
	res, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), opts)
	if err != nil || res.State != store.BlobOn || !slices.Equal(res.Deleted, []uint32{oldKey}) {
		t.Fatalf("rerun: %+v %v", res, err)
	}
	if e.hasKey(kek, oldKey) {
		t.Fatal("the old key survived")
	}
	e.readAll(kek, msgs)
}

// rekey refuses while encryption is off or a conversion is unfinished, and
// while a writer runs.
func TestRekeyRefusals(t *testing.T) {
	e := newArchiveEnv(t)
	kek, _ := e.keygen()
	if _, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), e.rekeyOpts(kek, nil)); !errors.Is(err, archive.ErrRekeyNeedsOn) {
		t.Fatalf("rekey while off: %v", err)
	}
	if err := e.st.SetBlobEncryption(e.ctx, store.BlobEncrypting, keyringCopy(t, mustRing(t, e.data, kek))); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), e.rekeyOpts(kek, nil)); !errors.Is(err, archive.ErrRekeyNeedsOn) {
		t.Fatalf("rekey while encrypting: %v", err)
	}
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil {
		t.Fatal(err)
	}
	lock, err := e.st.HoldWriters(e.ctx, "serve")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), e.rekeyOpts(kek, nil)); !errors.Is(err, store.ErrWritersRunning) {
		t.Fatalf("rekey while serve runs: %v", err)
	}
	if _, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), e.rekeyOpts(nil, nil)); err == nil {
		t.Fatal("rekey without a key accepted")
	}
}

func mustRing(t *testing.T, dataDir string, kek []byte) *blobcrypt.Keyring {
	t.Helper()
	r, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Open(kek); err != nil {
		t.Fatal(err)
	}
	return r
}

// A key added to a keyring file restored from an older backup keeps its
// slot: the change is made on the file reconciled with the newer database
// copy, so the next start does not let that copy win over it (coordinator
// note on #437). Counter-proof: changing the file alone (no
// reconciliation) leaves it a generation behind the database copy, the
// next writer start takes the database's slots, and the new key no longer
// opens the keyring.
func TestChangeKeyringReconcilesRestoredFile(t *testing.T) {
	e := newArchiveEnv(t)
	_, kek, _ := e.enabled(2)
	path := blobcrypt.KeyringPath(e.data)
	restored, err := os.ReadFile(path) // the file as a backup holds it
	if err != nil {
		t.Fatal(err)
	}
	change := func(label string, key []byte) {
		t.Helper()
		_, err := archive.ChangeKeyring(e.ctx, e.st, archive.KeyringChangeOptions{DataDir: e.data, KEK: kek, Command: "add-key", Logger: quiet},
			func(k *blobcrypt.Keyring) error { return k.AddSlot(key, label, t0) })
		if err != nil {
			t.Fatal(err)
		}
	}
	// Two changes after the backup, recorded in both copies: the database
	// copy is two generations ahead of the restored file.
	var later [][]byte
	for i := range 2 {
		k, _ := blobcrypt.GenerateKEK()
		ring := mustRing(t, e.data, kek)
		if err := ring.AddSlot(k, fmt.Sprintf("later %d", i), t0); err != nil {
			t.Fatal(err)
		}
		if err := e.st.UpdateBlobKeyring(e.ctx, e.row().KeyringGeneration, keyringCopy(t, ring)); err != nil {
			t.Fatal(err)
		}
		if err := blobcrypt.WriteKeyringFile(path, ring); err != nil {
			t.Fatal(err)
		}
		later = append(later, k)
	}
	if err := os.WriteFile(path, restored, 0o600); err != nil {
		t.Fatal(err)
	}
	added, _ := blobcrypt.GenerateKEK()
	change("added after the restore", added)

	a := e.writerStart(kek)
	if !a.Keyring.HasSlot(blobcrypt.Fingerprint(added)) {
		t.Fatal("the slot added to the restored file is lost after a start")
	}
	for _, k := range append(later, added) {
		e.copies(k) // both copies open with each key
	}
}

// The keyring commands hold the writers lock shared: they run while a
// writer runs, and refuse while a conversion holds it exclusively.
func TestChangeKeyringWritersLock(t *testing.T) {
	e := newArchiveEnv(t)
	_, kek, _ := e.enabled(1)
	opts := archive.KeyringChangeOptions{DataDir: e.data, KEK: kek, Command: "add-key", Logger: quiet}
	k, _ := blobcrypt.GenerateKEK()
	add := func(r *blobcrypt.Keyring) error { return r.AddSlot(k, "recovery", t0) }
	gen := e.row().Generation
	lock, err := e.st.HoldWriters(e.ctx, "serve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.ChangeKeyring(e.ctx, e.st, opts, add); err != nil {
		t.Fatalf("while serve runs: %v", err)
	}
	lock.Release()
	if g := e.row().Generation; g != gen {
		t.Fatalf("a slot change moved the generation from %d to %d (running writers would stop)", gen, g)
	}
	ex, err := e.st.TryLockWritersExclusive(e.ctx, "enable")
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Release()
	if _, err := archive.ChangeKeyring(e.ctx, e.st, opts, func(*blobcrypt.Keyring) error { return nil }); !errors.Is(err, store.ErrWritersExcluded) {
		t.Fatalf("during a conversion: %v", err)
	}
}

// copyTree copies the directory src to dst (created), files and modes.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		b, err := os.ReadFile(p) //nolint:gosec // the test's own data directory
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Plan revision 1, B2 (from #436's note on #437): a data directory restored
// from before a rekey, next to a database from after it, keeps every file
// readable. The keyring file from before holds the old data key, the
// database copy only the new one; the next start keeps both (the union)
// with the new key current, and writes the result to both copies.
// Counter-proof: blobcrypt.Reconcile without the union (the older copy's
// keys not added) loses the old key, and the restored files do not read.
func TestRekeyDataDirFromBeforeRestored(t *testing.T) {
	e := newArchiveEnv(t)
	msgs, kek, ring := e.enabled(4)
	oldKey := ring.CurrentKey()
	before := t.TempDir()
	copyTree(t, e.data, before)

	res, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), e.rekeyOpts(kek, nil))
	if err != nil || !slices.Equal(res.Deleted, []uint32{oldKey}) {
		t.Fatalf("rekey: %+v %v", res, err)
	}
	// Restore data/ from before the rekey; the database stays.
	if err := os.RemoveAll(e.data); err != nil {
		t.Fatal(err)
	}
	copyTree(t, before, e.data)
	for h := range msgs {
		if e.keyOf(h) != oldKey {
			t.Fatal("the restored files do not name the old key")
		}
	}

	a, err := e.atRest(archive.RoleWriter, kek, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	if a.Keyring.CurrentKey() != res.NewKey {
		t.Fatalf("current %08x, want the new key %08x", a.Keyring.CurrentKey(), res.NewKey)
	}
	if !e.hasKey(kek, oldKey) || !e.hasKey(kek, res.NewKey) {
		t.Fatal("the copies do not hold both data keys")
	}
	e.readAll(kek, msgs)
}

// The new recovery key is shown as soon as its slot is recorded, before
// the first batch: the start has already dropped the old recovery slots,
// so a run killed during the conversion must not leave the operator
// without one (review round 1 of #519, both reviews). Counter-proof:
// calling ShowRecoveryKey only after the conversion leaves it unshown at
// the first batch.
func TestRekeyShowsRecoveryKeyBeforeFirstBatch(t *testing.T) {
	e := newArchiveEnv(t)
	_, kek, _ := e.enabled(8)
	oldRecovery, _ := blobcrypt.GenerateKEK()
	if _, err := archive.ChangeKeyring(e.ctx, e.st, archive.KeyringChangeOptions{DataDir: e.data, KEK: kek, Command: "add-key", Logger: quiet},
		func(k *blobcrypt.Keyring) error { return k.AddSlot(oldRecovery, "recovery key", t0) }); err != nil {
		t.Fatal(err)
	}
	var shown []byte
	opts := e.rekeyOpts(kek, nil)
	opts.Recovery, opts.BatchFiles = true, 3
	opts.ShowRecoveryKey = func(k []byte) { shown = k }
	opts.AfterBatch = func(b int) error {
		if b != 1 {
			return nil
		}
		if shown == nil {
			return errors.New("the new recovery key was not shown before the first batch")
		}
		// It is the key that opens the database copy now; the old one
		// does not.
		db, err := blobcrypt.ParseKeyring(e.row().Keyring)
		if err != nil {
			return err
		}
		if err := db.Open(shown); err != nil {
			return fmt.Errorf("the shown key does not open the database copy: %w", err)
		}
		if db.HasSlot(blobcrypt.Fingerprint(oldRecovery)) {
			return errors.New("the old recovery slot is still there")
		}
		return nil
	}
	res, err := archive.Rekey(e.ctx, e.st, e.newBlobs(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shown, res.RecoveryKey) {
		t.Fatal("the shown key is not the result's recovery key")
	}
}
