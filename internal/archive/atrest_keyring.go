package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/store"
)

// readKeyringBytes reads the keyring file of dataDir; a missing file wraps
// blobcrypt.ErrNoKeyring.
func readKeyringBytes(dataDir string) ([]byte, error) {
	path := blobcrypt.KeyringPath(dataDir)
	data, err := os.ReadFile(path) //nolint:gosec // the configured data directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s: %w", blobcrypt.ErrNoKeyring, path, err)
	}
	return data, err
}

// reconcileKeyring opens the database copy and the file copy of the
// keyring with the archive key, merges them (blobcrypt.Reconcile) and
// writes the result back to each copy that differs (revision 1, B2;
// revision 4, keyring union). A missing file is rebuilt from the database.
// A copy that does not open with the key is replaced only by a newer one
// that does. It refuses copies of different archives, a corrupt copy, and
// a key that opens no copy.
func reconcileKeyring(ctx context.Context, st *store.Store, row *store.BlobEncryption, opts AtRestOptions) (*blobcrypt.Keyring, error) {
	dbRing, err := blobcrypt.ParseKeyring(row.Keyring)
	if err != nil {
		return nil, fmt.Errorf("the database copy of the keyring: %w", err)
	}
	dbErr := dbRing.Open(opts.KEK)
	if errors.Is(dbErr, blobcrypt.ErrKeyringCorrupt) {
		return nil, fmt.Errorf("the database copy of the keyring: %w", dbErr)
	}

	path := blobcrypt.KeyringPath(opts.DataDir)
	fileBytes, err := readKeyringBytes(opts.DataDir)
	var fileRing *blobcrypt.Keyring
	var fileErr error
	switch {
	case errors.Is(err, blobcrypt.ErrNoKeyring):
		fileBytes = nil
	case err != nil:
		return nil, err
	default:
		if fileRing, err = blobcrypt.ParseKeyring(fileBytes); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if fileRing.ArchiveID() != dbRing.ArchiveID() {
			return nil, fmt.Errorf("%w: %s has archive id %s, the database %s", blobcrypt.ErrOtherArchive,
				path, fileRing.ArchiveID(), dbRing.ArchiveID())
		}
		if fileErr = fileRing.Open(opts.KEK); errors.Is(fileErr, blobcrypt.ErrKeyringCorrupt) {
			return nil, fmt.Errorf("%s: %w", path, fileErr)
		}
	}

	var result *blobcrypt.Keyring
	switch {
	case dbErr != nil && (fileRing == nil || fileErr != nil):
		return nil, fmt.Errorf("the archive key does not open the keyring: %w", dbErr)
	case fileRing == nil:
		result = dbRing
		opts.Logger.Warn("encryption at rest: the keyring file is missing; rebuilt from the database copy", "file", path)
	case dbErr != nil:
		if dbRing.Generation() >= fileRing.Generation() {
			return nil, fmt.Errorf("the archive key opens %s but not the newer database copy of the keyring: %w", path, dbErr)
		}
		result = fileRing
	case fileErr != nil:
		if fileRing.Generation() >= dbRing.Generation() {
			return nil, fmt.Errorf("the archive key opens the database copy of the keyring but not the newer %s: %w", path, fileErr)
		}
		result = dbRing
	default:
		if result, err = blobcrypt.Reconcile(fileRing, dbRing); err != nil {
			return nil, err
		}
	}

	if dbErr != nil || !result.SameAs(dbRing) {
		data, err := result.Marshal()
		if err != nil {
			return nil, err
		}
		if err := st.UpdateBlobKeyring(ctx, row.KeyringGeneration, store.KeyringCopy{
			Keyring: data, ID: result.ArchiveID(), Generation: int64(result.Generation()), CurrentKey: result.CurrentKey(), //nolint:gosec // generations stay far below 2^63
		}); err != nil {
			return nil, err
		}
		opts.Logger.Info("encryption at rest: updated the database copy of the keyring", "generation", result.Generation())
	}
	if fileRing == nil || fileErr != nil || !result.SameAs(fileRing) {
		if err := writeKeyringIfUnchanged(opts, fileBytes, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// writeKeyringIfUnchanged replaces the keyring file with k under the
// keyring lock, if the file still holds was (nil: missing). While an
// encryption command holds the lock, the file is left to it and to the
// next start: the result is used from memory.
func writeKeyringIfUnchanged(opts AtRestOptions, was []byte, k *blobcrypt.Keyring) error {
	if !opts.keyringHeld {
		unlock, err := blobcrypt.LockKeyring(opts.DataDir)
		if errors.Is(err, blobcrypt.ErrLocked) {
			opts.Logger.Warn("encryption at rest: the keyring file is locked; it is reconciled at the next start")
			return nil
		}
		if err != nil {
			return err
		}
		defer unlock()
	}
	now, err := readKeyringBytes(opts.DataDir)
	if errors.Is(err, blobcrypt.ErrNoKeyring) {
		now, err = nil, nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(now, was) {
		return store.ErrKeyringChanged
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(opts.DataDir), k); err != nil {
		return fmt.Errorf("write the reconciled keyring: %w", err)
	}
	opts.Logger.Info("encryption at rest: updated the keyring file", "generation", k.Generation())
	return nil
}
