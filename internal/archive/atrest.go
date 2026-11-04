package archive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// AtRestRole is what a command does with the message files, which decides
// its start-up checks of encryption at rest (#392, part 2).
type AtRestRole int

const (
	// RoleWriter writes message files: serve, worker, sync, import, fill,
	// reindex. It holds the writers lock for its whole run, refuses to
	// start during an unfinished conversion, needs the archive key while
	// encryption is on, and stops when the state changes under it.
	RoleWriter AtRestRole = iota
	// RoleReader only reads message files (export). It needs the archive
	// key whenever encryption is on or being changed.
	RoleReader
	// RoleVerify is RoleReader that starts without the key: verify checks
	// the attestations first and then reports the missing key (KeyMissing).
	RoleVerify
)

// AtRestOptions configures OpenAtRest.
type AtRestOptions struct {
	DataDir string
	KEK     []byte // the configured archive key; nil when not configured
	Role    AtRestRole
	// Command names a writer in the writers lock's application_name.
	Command string
	// OnLost is called once when a writer must stop: its lock connection
	// failed, or an inserting transaction found the state changed. The
	// caller ends the process with a non-zero exit (revision 5, B1 (b),
	// (c)).
	OnLost func(error)
	// Tick drives the writers lock's check (default: every
	// store.WritersPing).
	Tick   <-chan time.Time
	Logger *slog.Logger
}

// AtRest is the state of encryption at rest a command started with.
type AtRest struct {
	State      string // store.BlobOff, store.BlobOn or a transitional state
	Generation int64
	// Keyring is the reconciled, opened keyring; nil without a key or
	// keyring.
	Keyring *blobcrypt.Keyring
	// KeyMissing, for RoleVerify, is why encrypted files cannot be read.
	KeyMissing error

	lock   *store.WritersLock
	ticker *time.Ticker
}

// Close releases the writers lock.
func (a *AtRest) Close() {
	if a.ticker != nil {
		a.ticker.Stop()
	}
	if a.lock != nil {
		a.lock.Release()
	}
}

// ErrConversionInProgress means a writer refused to start because an
// offline conversion has not finished.
var ErrConversionInProgress = errors.New("an encryption change is in progress")

// ErrNoArchiveKey means encryption is on but no archive key is configured.
var ErrNoArchiveKey = errors.New("no archive key configured")

// OpenAtRest runs the start-up checks of encryption at rest for a command
// and configures blobs accordingly (#392, part 2):
//
//   - A writer takes the writers lock shared first (refused while an
//     encryption command holds it), so the state cannot change after it is
//     read; it refuses to start in a transitional state; and it pins the
//     generation, so an inserting transaction after a missed change fails
//     and the process stops.
//   - With no row but a keyring file and encrypted files on disk (a dump
//     from before `enable` restored next to newer files), a writer derives
//     the state from the files: it records `encrypting` and refuses, so
//     that `encryption enable` finishes the conversion.
//   - While encryption is on or being changed, the archive key must open
//     the keyring; the file copy and the database copy are reconciled
//     (blobcrypt.Reconcile) and written back where they differ.
//   - With state on, writers encrypt every new file.
func OpenAtRest(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts AtRestOptions) (*AtRest, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	a := &AtRest{}
	started := false
	defer func() {
		if !started {
			a.Close()
		}
	}()
	var err error
	if opts.Role == RoleWriter {
		if a.lock, err = st.HoldWriters(ctx, opts.Command); err != nil {
			return nil, err
		}
	}
	row, err := st.BlobEncryption(ctx)
	if err != nil {
		return nil, err
	}
	if row == nil && opts.Role == RoleWriter {
		if row, err = deriveFromFiles(ctx, st, blobs, opts); err != nil {
			return nil, err
		}
	}
	if row != nil {
		a.State, a.Generation = row.State, row.Generation
	}
	if opts.Role == RoleWriter && store.BlobTransitional(a.State) {
		cmd := map[string]string{store.BlobEncrypting: "enable", store.BlobDecrypting: "disable", store.BlobRekeying: "rekey"}[a.State]
		return nil, fmt.Errorf("%w (state %s): run `encryption %s` to finish it, then start again", ErrConversionInProgress, a.State, cmd)
	}

	if row == nil {
		// Off. A keyring the key opens still reads encrypted files left
		// over (a reader such as verify or export).
		a.Keyring = openFileKeyring(opts)
	} else {
		ring, err := needKeyring(ctx, st, row, opts)
		if err != nil {
			if opts.Role != RoleVerify {
				return nil, err
			}
			a.KeyMissing = err
		}
		a.Keyring = ring
		// The reconciliation may have changed the current key.
		if row, err = st.BlobEncryption(ctx); err != nil {
			return nil, err
		}
		if row == nil {
			return nil, errors.New("encryption at rest was turned off during start-up; start again")
		}
		a.Generation = row.Generation
	}
	if a.Keyring != nil {
		if err := blobs.SetKeys(a.Keyring, a.State == store.BlobOn); err != nil {
			return nil, err
		}
	}
	if opts.Role == RoleWriter {
		onLost := func(err error) {
			opts.Logger.Error("writer stops", "err", err)
			if opts.OnLost != nil {
				opts.OnLost(err)
			}
		}
		st.PinBlobGeneration(a.Generation, func() { onLost(store.ErrBlobStateChanged) })
		tick := opts.Tick
		if tick == nil {
			a.ticker = time.NewTicker(store.WritersPing)
			tick = a.ticker.C
		}
		a.lock.Watch(tick, onLost)
		if a.State == store.BlobOn {
			opts.Logger.Info("encryption at rest on", "data_key", fmt.Sprintf("%08x", a.Keyring.CurrentKey()),
				"key_fingerprint", blobcrypt.Fingerprint(opts.KEK))
		}
	}
	started = true
	return a, nil
}

// openFileKeyring opens the keyring file with the configured key, if both
// exist; nil otherwise (encryption off).
func openFileKeyring(opts AtRestOptions) *blobcrypt.Keyring {
	if opts.KEK == nil {
		return nil
	}
	ring, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(opts.DataDir))
	if err == nil {
		err = ring.Open(opts.KEK)
	}
	if err != nil {
		if !errors.Is(err, blobcrypt.ErrNoKeyring) {
			opts.Logger.Warn("encryption at rest is off; the keyring file does not open with the archive key", "err", err)
		}
		return nil
	}
	return ring
}

// deriveFromFiles is the state of an archive without a blob_encryption
// row: off, unless a keyring file exists and an encrypted file is found
// (revision 1, B2). Then the row is recorded as encrypting, with the file
// as the database copy, and returned.
func deriveFromFiles(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts AtRestOptions) (*store.BlobEncryption, error) {
	ring, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(opts.DataDir))
	if errors.Is(err, blobcrypt.ErrNoKeyring) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	errFound := errors.New("found")
	found := ""
	err = blobs.Walk(func(e blobstore.Entry) error {
		if e.Kind == blobstore.EntryBlob && e.Encrypted {
			found = e.Path
			return errFound
		}
		return ctx.Err()
	})
	if err != nil && !errors.Is(err, errFound) {
		return nil, err
	}
	if found == "" {
		return nil, nil
	}
	// The file as it is: unopened, it cannot be marshalled again.
	data, err := readKeyringBytes(opts.DataDir)
	if err != nil {
		return nil, err
	}
	if err := st.SetBlobEncryption(ctx, store.BlobEncrypting, store.KeyringCopy{
		Keyring: data, ID: ring.ArchiveID(), Generation: int64(ring.Generation()), CurrentKey: ring.CurrentKey(), //nolint:gosec // generations stay far below 2^63
	}); err != nil {
		return nil, err
	}
	opts.Logger.Warn("encryption at rest: encrypted files found while the database says off (restored from an older dump?); recorded the state as encrypting",
		"file", found)
	return st.BlobEncryption(ctx)
}

// needKeyring opens and reconciles the keyring while encryption is on or
// being changed.
func needKeyring(ctx context.Context, st *store.Store, row *store.BlobEncryption, opts AtRestOptions) (*blobcrypt.Keyring, error) {
	if opts.KEK == nil {
		return nil, fmt.Errorf("%w: encryption at rest is %s; set %s (or %s)",
			ErrNoArchiveKey, row.State, config.EnvArchiveKeyFile, config.EnvArchiveKey)
	}
	var err error
	for range 3 {
		var ring *blobcrypt.Keyring
		ring, err = reconcileKeyring(ctx, st, row, opts)
		if !errors.Is(err, store.ErrKeyringChanged) {
			return ring, err
		}
		if row, err = st.BlobEncryption(ctx); err != nil {
			return nil, err
		}
		if row == nil {
			return nil, errors.New("encryption at rest was turned off during start-up; start again")
		}
	}
	return nil, err
}
