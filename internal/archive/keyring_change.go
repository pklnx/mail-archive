package archive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/store"
)

// offKeyring is the keyring while encryption is off (row nil or off): the
// file, opened with the archive key, merged with the copy an off row
// recorded when that copy is of the same archive and opens (the data keys
// are the union; the newer generation decides the slots). The database
// copy is not written: a writer compares it with the file to decide
// whether to look for encrypted files, so it stays as recorded. The caller
// holds the keyring lock.
func offKeyring(row *store.BlobEncryption, opts AtRestOptions) (*blobcrypt.Keyring, error) {
	path := blobcrypt.KeyringPath(opts.DataDir)
	ring, err := blobcrypt.ReadKeyringFile(path)
	if errors.Is(err, blobcrypt.ErrNoKeyring) {
		return nil, fmt.Errorf("%w (create it with `encryption keygen` first)", err)
	}
	if err != nil {
		return nil, err
	}
	if err := ring.Open(opts.KEK); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if row != nil && len(row.Keyring) > 0 {
		if old, err := blobcrypt.ParseKeyring(row.Keyring); err == nil && old.ArchiveID() == ring.ArchiveID() && old.Open(opts.KEK) == nil {
			if ring, err = blobcrypt.Reconcile(ring, old); err != nil {
				return nil, err
			}
		} else {
			opts.Logger.Warn("encryption at rest: the keyring copy recorded with state off is not this archive's or does not open; it is replaced")
		}
	}
	return ring, nil
}

// KeyringChangeOptions configures ChangeKeyring.
type KeyringChangeOptions struct {
	DataDir string
	KEK     []byte // the configured archive key
	// Command names the change in the writers lock's application_name
	// ("add-key", "remove-key", "rotate-key").
	Command string
	Logger  *slog.Logger
}

// ChangeKeyring changes the keyring of encryption at rest, for `encryption
// add-key`, `remove-key` and `rotate-key` (#392, part 5). Before the
// change, the keyring file is reconciled with the database copy, as at a
// writer's start (the newer generation decides the slots, the data keys
// are the union), so that a change to a file restored from an older backup
// is not lost to a newer database copy (coordinator note on #437).
//
// It holds the writers lock shared, so no offline conversion can run
// meanwhile (refused while one runs), and the keyring lock, so no other
// keyring change can. Writers may keep running: a change of the slots or
// of the keyring key leaves the data keys and the generation they pinned
// alone. While encryption is on or being changed, the database copy is
// written first (compare-and-swap on its keyring generation) and then the
// file; while it is off, only the file. change edits the reconciled,
// opened keyring; the result is returned, also with an error wrapping
// ErrKeyringFileNotWritten, when the change took effect.
func ChangeKeyring(ctx context.Context, st *store.Store, opts KeyringChangeOptions, change func(*blobcrypt.Keyring) error) (*blobcrypt.Keyring, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.KEK == nil {
		return nil, fmt.Errorf("%w: set %s (or %s)", ErrNoArchiveKey, config.EnvArchiveKeyFile, config.EnvArchiveKey)
	}
	lock, err := st.HoldWriters(ctx, "encryption "+opts.Command)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	unlock, err := blobcrypt.LockKeyring(opts.DataDir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	at := AtRestOptions{DataDir: opts.DataDir, KEK: opts.KEK, Logger: opts.Logger, keyringHeld: true}
	row, err := st.BlobEncryption(ctx)
	if err != nil {
		return nil, err
	}
	var ring *blobcrypt.Keyring
	if blobOff(row) {
		ring, err = offKeyring(row, at)
	} else {
		if ring, err = needKeyring(ctx, st, row, at); err == nil {
			// The copy as the reconciliation left it.
			row, err = st.BlobEncryption(ctx)
			if err == nil && blobOff(row) {
				err = errors.New("encryption at rest was turned off meanwhile; start again")
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if err := change(ring); err != nil {
		return nil, err
	}
	if !blobOff(row) {
		data, err := ring.Marshal()
		if err != nil {
			return nil, err
		}
		if err := st.UpdateBlobKeyring(ctx, row.KeyringGeneration, store.KeyringCopy{
			Keyring: data, ID: ring.ArchiveID(), Generation: int64(ring.Generation()), CurrentKey: ring.CurrentKey(), //nolint:gosec // generations stay far below 2^63
		}); err != nil {
			if errors.Is(err, store.ErrKeyringChanged) {
				return nil, fmt.Errorf("%w; start again", err)
			}
			return nil, err
		}
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(opts.DataDir), ring); err != nil {
		if !blobOff(row) {
			// The database copy holds the change: its new keys are in use.
			return ring, fmt.Errorf("%w: %w", ErrKeyringFileNotWritten, err)
		}
		return nil, err
	}
	return ring, nil
}

// ErrKeyringFileNotWritten means a keyring change reached the database copy
// but not the file. The change counts: the next start rebuilds the file
// from the newer database copy. ChangeKeyring returns the changed keyring
// with it, so that new keys are still shown to the operator.
var ErrKeyringFileNotWritten = errors.New("the keyring change is recorded in the database, but the keyring file could not be written (the next start writes it from the database copy)")
