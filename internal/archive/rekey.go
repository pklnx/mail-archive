package archive

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// RekeyOptions configures Rekey.
type RekeyOptions struct {
	ConvertOptions
	// Keep are further keys (recovery keys) whose slots stay; each must
	// open the keyring. The configured archive key always stays; every
	// other slot is dropped, because a new keyring key can only be wrapped
	// for keys at hand.
	Keep [][]byte
	// Recovery adds a new recovery key (RekeyResult.RecoveryKey).
	Recovery bool
	// ShowRecoveryKey is called with the new recovery key as soon as its
	// slot is recorded, before the keyring file is written and before any
	// file is converted: the old slots are gone by then, so the operator
	// must have the key even if the run is killed later (review round 1
	// of #519). The command prints it.
	ShowRecoveryKey func(key []byte)
	// AfterFinalFile, for tests, runs at the end after the keyring file
	// without the old data keys is written and before the state on is
	// recorded; an error ends the run as an interruption would.
	AfterFinalFile func() error
}

// RekeyResult is what Rekey did.
type RekeyResult struct {
	ConvertResult
	// Started is set when this run created the new data key and keyring
	// key; a resumed run only continues.
	Started bool
	OldKey  uint32 // the current data key before the rekey (when Started)
	NewKey  uint32 // the current data key
	// RecoveryKey is the new recovery key (Started with Recovery), set once
	// its slot is recorded: it must be shown to the operator once, also
	// when Rekey then fails.
	RecoveryKey []byte
	// Dropped are the slots dropped at the start.
	Dropped []blobcrypt.Slot
	// Deleted are the old data keys deleted at the end; Retained the old
	// ones kept because a file that could not be re-encrypted names them.
	Deleted, Retained []uint32
}

// ErrRekeyNeedsOn means `encryption rekey` found encryption at rest off or
// an unfinished enable or disable.
var ErrRekeyNeedsOn = errors.New("rekey needs encryption at rest on")

// Rekey creates a new data key and a new keyring key, then re-encrypts
// every message file under the new data key, offline and resumably like
// Enable (#392, part 5; maintainer decision 5):
//
//   - It takes the writers lock and the blob lock exclusively and refuses
//     while a writer, verify, a sweep or a purge runs.
//   - From on, it reconciles the keyring copies, adds a new current data
//     key, and wraps a new keyring key into the slots it keeps (the
//     configured archive key, opts.Keep, a new recovery key): a key whose
//     slot is dropped opens no copy of the keyring from now on, and so no
//     new data key (coordinator decision on #437). The database copy and
//     the state rekeying are recorded in one statement, then the file is
//     written.
//   - From rekeying (an interrupted run) it resumes from the cursor.
//   - Each file under an old key is re-encrypted into tmp/, read back and
//     checked against its SHA-256, and replaces the old file by an atomic
//     rename; the directory is synchronized before the cursor moves on.
//   - The old data keys are deleted only after a complete pass from the
//     start that converted nothing, and only those no file names. The file
//     is written first, then the database copy with the state on, so that
//     an interruption in between leaves the state rekeying and the rerun
//     deletes them again.
func Rekey(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts RekeyOptions) (*RekeyResult, error) {
	opts.defaults()
	res := &RekeyResult{}
	off, err := beginOffline(ctx, st, blobs, opts.ConvertOptions, "rekey", &res.ConvertResult)
	if err != nil {
		return nil, err
	}
	defer off.close()
	ctx = off.ctx

	row, err := st.BlobEncryption(ctx)
	if err != nil {
		return nil, err
	}
	state := store.BlobOff
	if row != nil {
		state = row.State
	}
	if state != store.BlobOn && state != store.BlobRekeying {
		if state == store.BlobOff {
			return nil, fmt.Errorf("%w; it is off (turn it on with `encryption enable`)", ErrRekeyNeedsOn)
		}
		return nil, fmt.Errorf("%w; it is %s (finish that first)", ErrRekeyNeedsOn, state)
	}
	if opts.KEK == nil {
		return nil, fmt.Errorf("%w: `encryption rekey` needs it", ErrNoArchiveKey)
	}
	at := AtRestOptions{DataDir: opts.DataDir, KEK: opts.KEK, Logger: opts.Logger, keyringHeld: true}
	ring, err := needKeyring(ctx, st, row, at)
	if err != nil {
		return nil, err
	}
	if row, err = st.BlobEncryption(ctx); err != nil {
		return nil, err
	}
	if row == nil || row.State != state {
		return nil, errors.New("encryption at rest changed during start-up; start again")
	}
	if state == store.BlobOn {
		if err := startRekey(ctx, st, row, ring, opts, res); err != nil {
			return nil, err
		}
		if row, err = st.BlobEncryption(ctx); err != nil {
			return nil, err
		}
		if row == nil || row.State != store.BlobRekeying {
			return nil, errors.New("encryption at rest changed during start-up; start again")
		}
	} else {
		resumeNote(opts, ring)
	}
	res.NewKey = ring.CurrentKey()
	if err := blobs.SetKeys(ring, true); err != nil {
		return nil, err
	}
	conv, err := blobs.NewRekey()
	if err != nil {
		return nil, err
	}
	res.State = store.BlobRekeying
	c := &converter{
		ctx: ctx, st: st, blobs: blobs, conv: conv, opts: opts.ConvertOptions, encrypt: true, rekey: true,
		gen: row.Generation, cursor: row.Cursor, done: row.FilesDone, corrupt: row.FilesCorrupt,
		res: &res.ConvertResult, orphanDir: off.orphans,
	}
	if err := c.run(); err != nil {
		return res, off.runErr(err)
	}
	if err := finishRekey(ctx, st, row, ring, conv.NamedKeys(), opts, res); err != nil {
		return res, off.runErr(err)
	}
	res.State = store.BlobOn
	return res, nil
}

// resumeNote tells the operator, on a resumed rekey, which keys open the
// keyring: the start dropped the slots not kept, and the recovery key it
// added may never have been shown if that run was killed early.
func resumeNote(opts RekeyOptions, ring *blobcrypt.Keyring) {
	if len(opts.Keep) > 0 || opts.Recovery {
		fmt.Fprintln(opts.Out, "a rekey in progress resumes; its slots were set when it started, so the keys given now change nothing")
	}
	fmt.Fprintln(opts.Out, "the keyring's slots:")
	for _, s := range ring.Slots() {
		fmt.Fprintf(opts.Out, "  %s  %s  %s\n", s.Fingerprint, s.Created.UTC().Format("2006-01-02T15:04:05Z"), s.Label)
	}
	fmt.Fprintln(opts.Out, "If the recovery key of the interrupted start was not shown, add one with `encryption add-key`.")
}

// startRekey is the step from on to rekeying: a new current data key and a
// new keyring key in the slots kept, recorded with the state in the
// database first, then in the file. The caller holds the keyring lock.
func startRekey(ctx context.Context, st *store.Store, row *store.BlobEncryption, ring *blobcrypt.Keyring, opts RekeyOptions, res *RekeyResult) error {
	keep := append([][]byte{opts.KEK}, opts.Keep...)
	var add []blobcrypt.NewSlot
	var rk []byte
	if opts.Recovery {
		var err error
		if rk, err = blobcrypt.GenerateKEK(); err != nil {
			return err
		}
		add = append(add, blobcrypt.NewSlot{KEK: rk, Label: "recovery key", Created: opts.Now()})
	}
	res.OldKey = ring.CurrentKey()
	if _, err := ring.AddCurrentKey(); err != nil {
		return err
	}
	dropped, err := ring.Rewrap(keep, add)
	if err != nil {
		return err
	}
	data, err := ring.Marshal()
	if err != nil {
		return err
	}
	if err := st.StartBlobRekey(ctx, row.KeyringGeneration, store.KeyringCopy{
		Keyring: data, ID: ring.ArchiveID(), Generation: int64(ring.Generation()), CurrentKey: ring.CurrentKey(), //nolint:gosec // generations stay far below 2^63
	}); err != nil {
		return err
	}
	// Recorded: the recovery key has its slot and must be shown now,
	// whatever happens next.
	res.Started, res.Dropped, res.RecoveryKey = true, dropped, rk
	fmt.Fprintf(opts.Out, "encryption at rest: on → rekeying (data key %08x → %08x, new keyring key, keyring generation %d)\n",
		res.OldKey, ring.CurrentKey(), ring.Generation())
	for _, s := range dropped {
		fmt.Fprintf(opts.Out, "dropped the slot of key %s (%s); that key no longer opens the keyring\n", s.Fingerprint, s.Label)
	}
	if rk != nil && opts.ShowRecoveryKey != nil {
		opts.ShowRecoveryKey(rk)
	}
	// A crash before the file is written leaves a newer database copy,
	// which the rerun reconciles into the file.
	return blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(opts.DataDir), ring)
}

// finishRekey deletes the old data keys that the clean pass found named by
// no file, writes the keyring file and then records it with the state on.
func finishRekey(ctx context.Context, st *store.Store, row *store.BlobEncryption, ring *blobcrypt.Keyring, named []uint32, opts RekeyOptions, res *RekeyResult) error {
	cur := ring.CurrentKey()
	for _, id := range ring.DataKeyIDs() {
		switch {
		case id == cur:
		case slices.Contains(named, id):
			res.Retained = append(res.Retained, id)
		default:
			res.Deleted = append(res.Deleted, id)
		}
	}
	if err := ring.DeleteDataKeys(res.Deleted); err != nil {
		return err
	}
	if len(res.Deleted) > 0 {
		if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(opts.DataDir), ring); err != nil {
			return err
		}
	}
	if opts.AfterFinalFile != nil {
		if err := opts.AfterFinalFile(); err != nil {
			return err
		}
	}
	data, err := ring.Marshal()
	if err != nil {
		return err
	}
	return st.FinishBlobRekey(ctx, row.Generation, row.KeyringGeneration, store.KeyringCopy{
		Keyring: data, ID: ring.ArchiveID(), Generation: int64(ring.Generation()), CurrentKey: cur, //nolint:gosec // generations stay far below 2^63
	})
}
