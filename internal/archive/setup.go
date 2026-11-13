package archive

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// keyringLabelArchiveKey is the label of the archive key's slot in a
// keyring that EnableAtSetup creates, as `encryption keygen` names it.
const keyringLabelArchiveKey = "archive key"

// errNotFresh ends EnableAtSetup when the instance is no longer new once
// it holds the locks.
var errNotFresh = errors.New("not a new instance")

// EnableAtSetup turns encryption at rest on for a new instance at its
// first start (#396, revision 1, step 3b): new instances start encrypted
// (#392, maintainer decision 6). serve and worker call it under the
// migration lock, so one process does it and the other finds it done.
//
// It is `encryption enable` on an empty archive: it takes the writers
// lock and the blob lock exclusively without waiting (Enable), so it
// refuses with store.ErrWritersRunning, naming the holder, while a
// command-line writer runs (an import or sync of accounts without an
// owner; plan approval, note 1). Under those locks it checks again that
// the instance is new (store.FreshInstance) and does nothing otherwise.
// Without a keyring file it creates one with a slot for opts.KEK alone:
// the archive key comes from the operator's key custody, and a recovery
// key is added later with `encryption add-key`. enabled reports that it
// turned encryption on.
func EnableAtSetup(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts ConvertOptions) (enabled bool, err error) {
	if opts.KEK == nil {
		return false, fmt.Errorf("%w: encryption at setup needs it", ErrNoArchiveKey)
	}
	opts.defaults()
	opts.prepare = func(ctx context.Context) error {
		fresh, err := st.FreshInstance(ctx)
		if err != nil {
			return err
		}
		if !fresh {
			return errNotFresh
		}
		// The caller holds the keyring lock (beginOffline).
		path := blobcrypt.KeyringPath(opts.DataDir)
		if _, err := os.Lstat(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return err // the operator's keyring (encryption keygen), opened by Enable
		}
		ring, err := blobcrypt.NewKeyring(opts.KEK, keyringLabelArchiveKey, opts.Now())
		if err != nil {
			return err
		}
		if err := blobcrypt.WriteKeyringFile(path, ring); err != nil {
			return err
		}
		opts.Logger.Info("encryption at setup: created the keyring with a slot for the archive key; add a recovery key with `encryption add-key`",
			"key_fingerprint", blobcrypt.Fingerprint(opts.KEK))
		return nil
	}
	res, err := convertArchive(ctx, st, blobs, opts, true)
	if errors.Is(err, errNotFresh) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return res.State == store.BlobOn && !res.Unchanged, nil
}
