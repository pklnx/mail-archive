package archive_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/testwait"
)

const convertMarker = "Zaunkoenig-Marker-0815"

// convertOpts are the options of a conversion in e, with the archive key.
func (e *archiveEnv) convertOpts(kek []byte, out io.Writer) archive.ConvertOptions {
	return archive.ConvertOptions{DataDir: e.data, KEK: kek, Out: out, Logger: quiet}
}

// newBlobs is a fresh blob store on e's data directory, as a new command
// run opens it.
func (e *archiveEnv) newBlobs() *blobstore.Store {
	e.t.Helper()
	b, err := blobstore.New(e.data)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// seed saves n plaintext messages through the store and returns their
// contents by hash.
func (e *archiveEnv) seed(n int) map[string]string {
	e.t.Helper()
	out := map[string]string{}
	for i := range n {
		raw := fmt.Sprintf("Subject: Nachricht %d\r\n\r\n%s %d\r\n", i, convertMarker, i)
		b := e.save(e.folder, uint32(i+1), raw) //nolint:gosec // small test numbers
		out[b.SHA256] = raw
	}
	return out
}

// forms counts the plaintext and the encrypted message files in
// messages/.
func (e *archiveEnv) forms() (plain, enc int) {
	e.t.Helper()
	err := e.newBlobs().Walk(func(en blobstore.Entry) error {
		if en.Kind == blobstore.EntryBlob {
			if en.Encrypted {
				enc++
			} else {
				plain++
			}
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return plain, enc
}

// readAll checks that every message reads back through a store with the
// keyring of e's data directory.
func (e *archiveEnv) readAll(kek []byte, msgs map[string]string) {
	e.t.Helper()
	b := e.newBlobs()
	ring, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(e.data))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := ring.Open(kek); err != nil {
		e.t.Fatal(err)
	}
	if err := b.SetKeys(ring, false); err != nil {
		e.t.Fatal(err)
	}
	for h, want := range msgs {
		m, err := b.OpenMessage(h)
		if err != nil {
			e.t.Fatalf("%s: %v", h[:8], err)
		}
		got, err := io.ReadAll(m)
		_ = m.Close()
		if err != nil || string(got) != want {
			e.t.Fatalf("%s: %q %v", h[:8], got, err)
		}
	}
}

func (e *archiveEnv) row() *store.BlobEncryption {
	e.t.Helper()
	r, err := e.st.BlobEncryption(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// writerStart starts a writer and stops it again (its start-up checks
// ran, its lock is released).
func (e *archiveEnv) writerStart(kek []byte) *archive.AtRest {
	e.t.Helper()
	a, err := e.atRest(archive.RoleWriter, kek, nil, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	a.Close()
	return a
}

var errInterrupt = errors.New("interrupted")

// interruptAfter is an AfterBatch hook that ends the run after n batches.
func interruptAfter(n int) func(int) error {
	return func(b int) error {
		if b >= n {
			return errInterrupt
		}
		return nil
	}
}

// enable turns an archive with plaintext messages on: every file ends
// encrypted and readable, the generation moves from the recorded off (0)
// to encrypting (1) and on (2), and the progress shows percent, rate and
// time left.
func TestEnableEncryptsArchive(t *testing.T) {
	e := newArchiveEnv(t)
	msgs := e.seed(30)
	kek, _ := e.keygen()
	e.writerStart(nil) // records off at generation 0
	if r := e.row(); r == nil || r.State != store.BlobOff || r.Generation != 0 {
		t.Fatalf("recorded off: %+v", r)
	}
	var out bytes.Buffer
	opts := e.convertOpts(kek, &out)
	opts.BatchFiles = 7
	res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != store.BlobOn || res.Converted != 30 || res.Passes != 2 || len(res.Corrupt) != 0 {
		t.Fatalf("result %+v", res)
	}
	if r := e.row(); r.State != store.BlobOn || r.Generation != 2 || r.FilesDone != 30 || r.Cursor != "" || r.FinishedAt == nil {
		t.Fatalf("row %+v", r)
	}
	if p, c := e.forms(); p != 0 || c != 30 {
		t.Fatalf("plain %d encrypted %d", p, c)
	}
	e.readAll(kek, msgs)
	for _, want := range []string{"off → encrypting", "encrypting: 100%", "files/s", "left", "checking that no file is left"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// A writer starts and encrypts; a second enable has nothing to do.
	if a := e.writerStart(kek); a.State != store.BlobOn || !e.blobs.Encrypts() {
		t.Fatalf("writer after enable: %+v", a)
	}
	if res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil || !res.Unchanged {
		t.Fatalf("enable again: %+v %v", res, err)
	}
}

// An empty archive (a new instance, maintainer decision 6) only turns on.
func TestEnableEmptyArchive(t *testing.T) {
	e := newArchiveEnv(t)
	kek, _ := e.keygen()
	res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil))
	if err != nil || res.State != store.BlobOn || res.Converted != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if r := e.row(); r.State != store.BlobOn || r.Generation != 2 {
		t.Fatalf("row %+v", r)
	}
	// Both locks are released at the end.
	unlock, ok, err := e.st.TryLockBlobs(e.ctx)
	if err != nil || !ok {
		t.Fatalf("blob lock after enable: %v %v", ok, err)
	}
	unlock()
	lock, err := e.st.HoldWriters(e.ctx, "serve")
	if err != nil {
		t.Fatalf("writers lock after enable: %v", err)
	}
	lock.Release()
}

// An interrupted enable resumes from its cursor. Until it has finished,
// writers refuse to start; the rerun ends with every file encrypted and
// makes a full pass from the start after the resumed one.
func TestEnableResumes(t *testing.T) {
	e := newArchiveEnv(t)
	msgs := e.seed(25)
	kek, _ := e.keygen()
	opts := e.convertOpts(kek, nil)
	opts.BatchFiles, opts.AfterBatch = 4, interruptAfter(3)
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts); !errors.Is(err, errInterrupt) {
		t.Fatalf("first run: %v", err)
	}
	r := e.row()
	if r.State != store.BlobEncrypting || !blobstore.IsHash(r.Cursor) || r.FilesDone != 12 {
		t.Fatalf("after the interruption: %+v", r)
	}
	if p, c := e.forms(); p != 13 || c != 12 {
		t.Fatalf("plain %d encrypted %d", p, c)
	}
	if _, err := e.atRest(archive.RoleWriter, kek, nil, nil); !errors.Is(err, archive.ErrConversionInProgress) {
		t.Fatalf("a writer during the conversion: %v", err)
	}
	var out bytes.Buffer
	opts.Out, opts.AfterBatch = &out, nil
	res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts)
	if err != nil || res.State != store.BlobOn || res.Converted != 13 || res.Passes != 2 {
		t.Fatalf("rerun: %+v %v", res, err)
	}
	if !strings.Contains(out.String(), "resuming after "+r.Cursor) {
		t.Errorf("output: %s", out.String())
	}
	if r := e.row(); r.FilesDone != 25 {
		t.Fatalf("row %+v", r)
	}
	if p, c := e.forms(); p != 0 || c != 25 {
		t.Fatalf("plain %d encrypted %d", p, c)
	}
	e.readAll(kek, msgs)
}

// plainBefore writes a plaintext message file whose hash sorts before
// cursor (a straggler behind it) and returns its hash.
func (e *archiveEnv) plainBefore(cursor string) string {
	e.t.Helper()
	for i := 0; ; i++ {
		raw := fmt.Sprintf("Subject: Nachzuegler %d\r\n\r\n%s\r\n", i, convertMarker)
		s := sha256.Sum256([]byte(raw))
		h := hex.EncodeToString(s[:])
		if h < cursor {
			e.write(blobstore.RelPath(h), raw)
			return h
		}
	}
}

// A straggler behind the cursor (a plaintext file the resumed pass does
// not reach) is converted by the full pass from the start that every run
// ends with (revision 4, review hole 2).
func TestEnableFinalPassCatchesStraggler(t *testing.T) {
	e := newArchiveEnv(t)
	e.seed(20)
	kek, _ := e.keygen()
	opts := e.convertOpts(kek, nil)
	opts.BatchFiles, opts.AfterBatch = 10, interruptAfter(1)
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts); !errors.Is(err, errInterrupt) {
		t.Fatal(err)
	}
	h := e.plainBefore(e.row().Cursor)
	opts.AfterBatch = nil
	res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts)
	if err != nil || res.State != store.BlobOn {
		t.Fatalf("%+v %v", res, err)
	}
	if e.exists(blobstore.RelPath(h)) || !e.exists(blobstore.EncPath(h)) {
		t.Fatal("the straggler stayed plaintext")
	}
}

// disable while encrypting changes direction: the cursor starts again
// and the run ends with no encrypted file left, state off, and a
// generation that never repeats.
func TestDisableChangesDirection(t *testing.T) {
	e := newArchiveEnv(t)
	msgs := e.seed(20)
	kek, _ := e.keygen()
	opts := e.convertOpts(kek, nil)
	opts.BatchFiles, opts.AfterBatch = 5, interruptAfter(2)
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts); !errors.Is(err, errInterrupt) {
		t.Fatal(err)
	}
	gen := e.row().Generation
	opts.AfterBatch = func(b int) error {
		if b == 1 {
			if r := e.row(); r.State != store.BlobDecrypting || r.Generation != gen+1 || r.FilesDone > 5 {
				t.Errorf("first batch of disable: %+v", r)
			}
		}
		return nil
	}
	res, err := archive.Disable(e.ctx, e.st, e.newBlobs(), opts)
	if err != nil || res.State != store.BlobOff {
		t.Fatalf("%+v %v", res, err)
	}
	if r := e.row(); r.State != store.BlobOff || r.Generation != gen+2 {
		t.Fatalf("row %+v", r)
	}
	if p, c := e.forms(); p != 20 || c != 0 {
		t.Fatalf("plain %d encrypted %d", p, c)
	}
	e.readAll(kek, msgs)
}

// Every real change adds 1 to the generation, from the recorded off at 0:
// enable (1, 2), disable (3, 4), enable again through the off row (5, 6).
func TestConversionGenerations(t *testing.T) {
	e := newArchiveEnv(t)
	msgs := e.seed(3)
	kek, _ := e.keygen()
	e.writerStart(nil)
	for i, step := range []func() (*archive.ConvertResult, error){
		func() (*archive.ConvertResult, error) {
			return archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil))
		},
		func() (*archive.ConvertResult, error) {
			return archive.Disable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil))
		},
		func() (*archive.ConvertResult, error) {
			return archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil))
		},
	} {
		if _, err := step(); err != nil {
			t.Fatal(err)
		}
		if g := e.row().Generation; g != int64(2*(i+1)) {
			t.Fatalf("step %d: generation %d", i, g)
		}
	}
	e.readAll(kek, msgs)
}

// enable writes the keyring file with a new generation before it encrypts
// the first file. So a dump recorded off, restored next to files that an
// interrupted enable already encrypted, does not match the keyring file
// any more: the next writer looks for encrypted files, finds them and
// refuses to start until enable finishes (#435, implementer note
// 6635701136).
func TestEnableNewKeyringGenerationFirst(t *testing.T) {
	e := newArchiveEnv(t)
	e.seed(10)
	kek, ring := e.keygen()
	e.writerStart(nil)
	dump := keyringCopy(t, ring) // the database copy a dump made now holds
	opts := e.convertOpts(kek, nil)
	opts.BatchFiles, opts.AfterBatch = 3, func(int) error {
		file, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(e.data))
		if err != nil {
			return err
		}
		if file.Generation() != ring.Generation()+1 || e.row().KeyringGeneration != int64(file.Generation()) { //nolint:gosec // small
			t.Errorf("keyring file generation %d, database copy %d, before %d", file.Generation(), e.row().KeyringGeneration, ring.Generation())
		}
		return errInterrupt
	}
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts); !errors.Is(err, errInterrupt) {
		t.Fatal(err)
	}
	if _, c := e.forms(); c == 0 {
		t.Fatal("nothing encrypted")
	}
	// Restore the dump: state off with the old keyring copy.
	if err := e.st.ClearBlobEncryption(e.ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecordBlobOff(e.ctx, dump); err != nil {
		t.Fatal(err)
	}
	if _, err := e.atRest(archive.RoleWriter, kek, nil, nil); !errors.Is(err, archive.ErrConversionInProgress) {
		t.Fatalf("a writer after the restore: %v", err)
	}
	if r := e.row(); r.State != store.BlobEncrypting {
		t.Fatalf("derived %+v", r)
	}
	if res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil || res.State != store.BlobOn {
		t.Fatalf("finish: %+v %v", res, err)
	}
}

// disable moves an encrypted file it cannot decrypt out of messages/ to
// orphans/<time>/: a later change of the keyring file then makes the next
// writer walk messages/, and it finds no encrypted file, so it stays off.
// After a clean disable the keyring file matches the database copy, so
// writers start without walking at all.
func TestDisableMovesUndecryptableAside(t *testing.T) {
	e := newArchiveEnv(t)
	msgs := e.seed(6)
	kek, _ := e.keygen()
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil {
		t.Fatal(err)
	}
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
	res, err := archive.Disable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, &out))
	if err != nil || res.State != store.BlobOff {
		t.Fatalf("%+v %v", res, err)
	}
	moved := res.MovedTo + "/" + blobstore.EncPath(damaged)
	if len(res.MovedAside) != 1 || !e.exists(moved) || e.exists(blobstore.EncPath(damaged)) {
		t.Fatalf("moved %v to %s", res.MovedAside, res.MovedTo)
	}
	if !strings.Contains(out.String(), "moved to "+res.MovedTo) {
		t.Errorf("output: %s", out.String())
	}
	if pl, c := e.forms(); pl != 5 || c != 0 {
		t.Fatalf("plain %d encrypted %d", pl, c)
	}
	// The keyring file matches the off row: no walk at start. An
	// encrypted file planted in messages/ is therefore not seen.
	planted := blobstore.EncPath(strings.Repeat("ab", 32))
	e.write(planted, "x")
	if a := e.writerStart(nil); a.State != store.BlobOff {
		t.Fatalf("writer after disable: %+v", a)
	}
	if err := os.Remove(e.path(planted)); err != nil {
		t.Fatal(err)
	}
	// A new keyring generation makes the next writer walk once: nothing
	// encrypted is left in messages/, so it stays off.
	ring, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(e.data))
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.Open(kek); err != nil {
		t.Fatal(err)
	}
	if err := ring.AddSlot(bytes.Repeat([]byte{7}, 32), "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(e.data), ring); err != nil {
		t.Fatal(err)
	}
	if a := e.writerStart(nil); a.State != store.BlobOff {
		t.Fatalf("writer after a keyring change: %+v", a)
	}
}

// The conversion commands refuse while a writer runs (one case per writer
// command, named by its application_name), while the blob lock is held,
// during a rekey, without the archive key and without a keyring.
func TestConversionRefusals(t *testing.T) {
	e := newArchiveEnv(t)
	e.seed(2)
	kek, _ := e.keygen()
	for _, cmd := range []string{"serve", "worker", "sync", "fill"} {
		lock, err := e.st.HoldWriters(e.ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		_, err = archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil))
		if !errors.Is(err, store.ErrWritersRunning) || !strings.Contains(err.Error(), `"mail-archive writer `+cmd+`"`) {
			t.Errorf("%s running: %v", cmd, err)
		}
		lock.Release()
	}
	unlock, ok, err := e.st.TryLockBlobs(e.ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := archive.Disable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); !errors.Is(err, archive.ErrBlobLockHeld) {
		t.Errorf("blob lock held: %v", err)
	}
	unlock()
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(nil, nil)); !errors.Is(err, archive.ErrNoArchiveKey) {
		t.Errorf("no key: %v", err)
	}
	if err := os.Remove(blobcrypt.KeyringPath(e.data)); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); !errors.Is(err, blobcrypt.ErrNoKeyring) {
		t.Errorf("no keyring: %v", err)
	}
	if r := e.row(); r != nil && r.State != store.BlobOff {
		t.Fatalf("a refusal changed the state: %+v", r)
	}
	_, ring := e.keygen()
	if err := e.st.SetBlobEncryption(e.ctx, store.BlobRekeying, keyringCopy(t, ring)); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Disable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); !errors.Is(err, archive.ErrRekeyInProgress) {
		t.Errorf("rekeying: %v", err)
	}
}

// Leak test (#435): after enable, no byte of the data directory holds a
// marker word: not the message files, not the files verify moved to
// orphans/, not a file left in tmp/ (old or young: no writer runs, so it
// is moved to orphans/ and encrypted there), not the keyring.
func TestEnableLeavesNoPlaintext(t *testing.T) {
	e := newArchiveEnv(t)
	msgs := e.seed(12)
	kek, _ := e.keygen()
	orphan := fmt.Sprintf("Subject: verwaist\r\n\r\n%s\r\n", convertMarker)
	s := sha256.Sum256([]byte(orphan))
	e.write("orphans/20261101T000000Z/"+blobstore.RelPath(hex.EncodeToString(s[:])), orphan)
	e.write("orphans/20261101T000000Z/tmp/put-1", "partial "+convertMarker)
	e.write("tmp/put-2", "stale "+convertMarker) // e.write ages it past StaleTempAge
	young := e.path("tmp/put-3")
	if err := os.WriteFile(young, []byte("young "+convertMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, &out))
	if err != nil || res.State != store.BlobOn || len(res.TempMoved) != 2 || res.Converted != 16 {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(out.String(), "left in tmp/") {
		t.Errorf("output: %s", out.String())
	}
	e.assertNoMarker()
	e.readAll(kek, msgs)
}

// assertNoMarker reads every byte of the data directory for the marker.
func (e *archiveEnv) assertNoMarker() {
	e.t.Helper()
	t := e.t
	files := 0
	err := filepath.WalkDir(e.data, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files++
		data, err := os.ReadFile(path) //nolint:gosec // the test's data directory
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(convertMarker)) || bytes.Contains(data, []byte("Zaunkoenig")) {
			t.Errorf("%s holds the marker", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 15 {
		t.Fatalf("only %d files checked", files)
	}
}

// A file left in tmp/ while encryption is already on (a writer killed
// mid-write before the window) is moved and encrypted by the next enable,
// which otherwise has nothing to do (review round 1 of #513, A1).
func TestEnableWhenOnEncryptsTemp(t *testing.T) {
	e := newArchiveEnv(t)
	e.seed(12)
	kek, _ := e.keygen()
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.path("tmp/put-9"), []byte("young "+convertMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil))
	if err != nil || !res.Unchanged || len(res.TempMoved) != 1 || res.Converted != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	e.assertNoMarker()
}

// The terminal state needs a pass that started from the beginning: a run
// interrupted right after the batch that converted the last file resumes
// with a pass that converts nothing, and that pass must not end the run.
// A straggler behind the cursor shows it (review round 1 of #513, A2/B2).
func TestEnableResumedPassDoesNotEnd(t *testing.T) {
	e := newArchiveEnv(t)
	e.seed(20)
	kek, _ := e.keygen()
	opts := e.convertOpts(kek, nil)
	opts.BatchFiles, opts.AfterBatch = 10, interruptAfter(2)
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts); !errors.Is(err, errInterrupt) {
		t.Fatal(err)
	}
	if p, _ := e.forms(); p != 0 {
		t.Fatalf("%d plaintext files left before the rerun", p)
	}
	h := e.plainBefore(e.row().Cursor)
	opts.AfterBatch = nil
	res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts)
	if err != nil || res.State != store.BlobOn {
		t.Fatalf("%+v %v", res, err)
	}
	if e.exists(blobstore.RelPath(h)) || !e.exists(blobstore.EncPath(h)) {
		t.Fatal("the straggler stayed plaintext")
	}
}

// disable on a recorded off derives the state from the files, as a writer
// start does: a dump recorded off, restored next to files that enable
// encrypted later, is decrypted instead of reported as already off
// (review round 1 of #513, follow-up 1).
func TestDisableDerivesFromFiles(t *testing.T) {
	e := newArchiveEnv(t)
	msgs := e.seed(5)
	kek, ring := e.keygen()
	dump := keyringCopy(t, ring)
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.ClearBlobEncryption(e.ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecordBlobOff(e.ctx, dump); err != nil {
		t.Fatal(err)
	}
	res, err := archive.Disable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil))
	if err != nil || res.Unchanged || res.State != store.BlobOff || res.Converted != 5 {
		t.Fatalf("%+v %v", res, err)
	}
	if p, c := e.forms(); p != 5 || c != 0 {
		t.Fatalf("plain %d encrypted %d", p, c)
	}
	e.readAll(kek, msgs)
}

// A conversion whose writers lock connection fails stops like a writer
// does: the run is cancelled at the next file, the state stays
// transitional, and the rerun finishes (review round 1 of #513, note 1).
func TestConversionStopsOnLockLoss(t *testing.T) {
	e, url := newAtRestEnv(t)
	e.seed(20)
	kek, _ := e.keygen()
	conn, err := pgx.Connect(e.ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(e.ctx) }()
	tick := make(chan time.Time)
	lostErr := make(chan error, 1)
	opts := e.convertOpts(kek, nil)
	opts.BatchFiles, opts.Tick = 5, tick
	opts.OnLost = func(err error) { lostErr <- err }
	opts.AfterBatch = func(b int) error {
		if b != 1 {
			return nil
		}
		var n int
		if err := conn.QueryRow(e.ctx, `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
			WHERE application_name = 'mail-archive encryption enable' AND datname = current_database()`).Scan(&n); err != nil || n != 1 {
			t.Errorf("terminate: %d, %v", n, err)
		}
		select {
		case tick <- time.Now():
		case <-time.After(testwait.Limit(t)):
			t.Error("the watch took no tick")
		}
		select {
		case <-lostErr:
		case <-time.After(testwait.Limit(t)):
			t.Error("the conversion did not notice the lost lock")
		}
		return nil
	}
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts); !errors.Is(err, store.ErrWritersLockLost) {
		t.Fatalf("after the lock loss: %v", err)
	}
	if r := e.row(); r.State != store.BlobEncrypting || r.FilesDone != 5 {
		t.Fatalf("row %+v", r)
	}
	opts.AfterBatch, opts.OnLost = nil, nil
	if res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), opts); err != nil || res.State != store.BlobOn {
		t.Fatalf("rerun: %+v %v", res, err)
	}
	if p, c := e.forms(); p != 0 || c != 20 {
		t.Fatalf("plain %d encrypted %d", p, c)
	}
}

// The documented recovery of a file disable moved aside: put the encrypted
// file back into messages/ and run disable again. With the state off and
// the keyring file matching the database copy, disable still walks
// messages/, finds it and decrypts it (review round 2 of #513).
func TestDisableDecryptsFilePutBack(t *testing.T) {
	e := newArchiveEnv(t)
	msgs := e.seed(3)
	kek, _ := e.keygen()
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil {
		t.Fatal(err)
	}
	var h string
	for h = range msgs {
		break
	}
	enc, err := os.ReadFile(e.path(blobstore.EncPath(h)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Disable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil {
		t.Fatal(err)
	}
	// Only the encrypted file is there, as after moving it back.
	if err := os.Remove(e.path(blobstore.RelPath(h))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.path(blobstore.EncPath(h)), enc, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := archive.Disable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil))
	if err != nil || res.Unchanged || res.State != store.BlobOff || res.Converted != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if p, c := e.forms(); p != 3 || c != 0 {
		t.Fatalf("plain %d encrypted %d", p, c)
	}
	e.readAll(kek, msgs)
}

// The final step of disable is guarded by the generation the run worked
// at: when the state changed after the last batch, the run fails and the
// state stays as the other change left it (review round 2 of #513).
func TestDisableFinishGuardedByGeneration(t *testing.T) {
	e := newArchiveEnv(t)
	e.seed(4)
	kek, _ := e.keygen()
	if _, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil)); err != nil {
		t.Fatal(err)
	}
	opts := e.convertOpts(kek, nil)
	// One batch per pass: the converting pass, then the clean pass; the
	// state changes after the clean pass's progress was saved.
	opts.AfterBatch = func(b int) error {
		if b == 2 {
			return e.st.SetBlobEncryption(e.ctx, store.BlobDecrypting, store.KeyringCopy{})
		}
		return nil
	}
	gen := e.row().Generation
	if _, err := archive.Disable(e.ctx, e.st, e.newBlobs(), opts); !errors.Is(err, store.ErrBlobStateChanged) {
		t.Fatalf("disable after a state change: %v", err)
	}
	if r := e.row(); r.State != store.BlobDecrypting || r.Generation != gen+2 {
		t.Fatalf("row %+v, generation before %d", r, gen)
	}
}
