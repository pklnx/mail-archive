package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/store"
)

// RestoreOptions configures ReconcileRestored.
type RestoreOptions struct {
	DataDir string
	KEK     []byte // the configured archive key; nil when not configured
	Logger  *slog.Logger
}

// KeyringSide is one copy of the keyring as ReconcileRestored found or
// left it.
type KeyringSide struct {
	Present    bool
	Generation uint64
	ArchiveID  string
}

// RestoreReport is what ReconcileRestored found and changed.
type RestoreReport struct {
	// StateBefore and StateAfter are the state of encryption at rest in
	// the restored database and after the reconciliation (store.BlobOff
	// for off or no row).
	StateBefore, StateAfter string
	// DBBefore, FileBefore, DBAfter and FileAfter are the database copy
	// and the keyring file before and after.
	DBBefore, FileBefore, DBAfter, FileAfter KeyringSide
	// DBWritten and FileWritten say which copy was replaced.
	DBWritten, FileWritten bool
	// Derived: the database said off, but encrypted files were found, so
	// the state was derived from the files (revision 1, B2).
	Derived bool
	// DataKeys and CurrentKey are those of the reconciled keyring, when it
	// was opened.
	DataKeys   []uint32
	CurrentKey uint32
}

// ReconcileRestored brings the restored database and the data directory
// together after a restore (#392, part 4; revision 1, B2), and reports
// what it changed. The caller holds the writers lock exclusively, so that
// no writer starts meanwhile.
//
//   - While the restored database says off (or has no row), the state is
//     derived from the files as at a writer's start, but always with a walk
//     of messages/: encrypted files make it encrypting, so that `encryption
//     enable` finishes the conversion; otherwise off is recorded with the
//     keyring file as the copy.
//   - Otherwise the archive key must open the keyring. The database copy
//     and the keyring file are reconciled: the newer generation decides the
//     current data key and the slots, the data keys are the union, and each
//     copy that differs from the result is written. A data directory
//     restored after an add-key or a rekey therefore keeps its newer
//     keyring. Copies of different archives are refused.
func ReconcileRestored(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts RestoreOptions) (*RestoreReport, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	atOpts := AtRestOptions{DataDir: opts.DataDir, KEK: opts.KEK, Logger: opts.Logger, keyringHeld: true}
	row, err := st.BlobEncryption(ctx)
	if err != nil {
		return nil, err
	}
	fileBefore, fileBytes, err := keyringFileSide(opts.DataDir)
	if err != nil {
		return nil, err
	}
	rep := &RestoreReport{StateBefore: stateOf(row), DBBefore: dbSide(row), FileBefore: fileBefore}
	if blobOff(row) && !fileBefore.Present {
		// Nothing encrypted can be read without a keyring: plain off.
		rep.StateAfter, rep.DBAfter, rep.FileAfter = rep.StateBefore, rep.DBBefore, rep.FileBefore
		return rep, nil
	}
	unlock, err := blobcrypt.LockKeyring(opts.DataDir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if blobOff(row) {
		if row, err = deriveFromFiles(ctx, st, blobs, row, atOpts, true); err != nil {
			return nil, err
		}
		rep.Derived = !blobOff(row)
	} else {
		ring, err := needKeyring(ctx, st, row, atOpts)
		if err != nil {
			return rep, err
		}
		rep.DataKeys, rep.CurrentKey = ring.DataKeyIDs(), ring.CurrentKey()
		if row, err = st.BlobEncryption(ctx); err != nil {
			return nil, err
		}
	}
	rep.StateAfter, rep.DBAfter = stateOf(row), dbSide(row)
	fileAfter, fileBytesAfter, err := keyringFileSide(opts.DataDir)
	if err != nil {
		return nil, err
	}
	rep.FileAfter = fileAfter
	rep.FileWritten = !bytes.Equal(fileBytes, fileBytesAfter)
	rep.DBWritten = rep.DBAfter != rep.DBBefore
	return rep, nil
}

func stateOf(row *store.BlobEncryption) string {
	if row == nil {
		return store.BlobOff
	}
	return row.State
}

func dbSide(row *store.BlobEncryption) KeyringSide {
	if row == nil || len(row.Keyring) == 0 {
		return KeyringSide{}
	}
	return KeyringSide{Present: true, Generation: uint64(row.KeyringGeneration), ArchiveID: row.KeyringID} //nolint:gosec // generations are never negative
}

// keyringFileSide reads the keyring file's metadata, and its bytes.
func keyringFileSide(dataDir string) (KeyringSide, []byte, error) {
	data, err := readKeyringBytes(dataDir)
	if errors.Is(err, blobcrypt.ErrNoKeyring) {
		return KeyringSide{}, nil, nil
	}
	if err != nil {
		return KeyringSide{}, nil, err
	}
	ring, err := blobcrypt.ParseKeyring(data)
	if err != nil {
		return KeyringSide{}, nil, fmt.Errorf("%s: %w", blobcrypt.KeyringPath(dataDir), err)
	}
	return KeyringSide{Present: true, Generation: ring.Generation(), ArchiveID: ring.ArchiveID()}, data, nil
}

// Lines describes the report for the operator, one finding per line.
// command is how to call the CLI (./ma or mail-archive).
func (r *RestoreReport) Lines(command string) []string {
	name := func(s string) string {
		if s == store.BlobOff {
			return "off"
		}
		return s
	}
	side := func(s KeyringSide) string {
		if !s.Present {
			return "none"
		}
		return fmt.Sprintf("generation %d", s.Generation)
	}
	out := []string{
		fmt.Sprintf("restored database: encryption at rest %s, keyring copy %s", name(r.StateBefore), side(r.DBBefore)),
		fmt.Sprintf("data directory: keyring file %s", side(r.FileBefore)),
	}
	switch {
	case r.Derived:
		out = append(out, fmt.Sprintf("state: the database said off, but the data directory holds encrypted files: recorded %s; "+
			"run `%s encryption enable` to finish the conversion before anything else starts", name(r.StateAfter), command))
	case r.StateAfter == store.BlobOff:
		if r.FileBefore.Present {
			out = append(out, "state: off; no encrypted files in messages/")
		} else {
			out = append(out, "state: off; nothing to reconcile")
		}
	default:
		out = append(out, "state: "+name(r.StateAfter)+", as in the dump")
	}
	if r.DataKeys != nil {
		keys := make([]string, len(r.DataKeys))
		for i, id := range r.DataKeys {
			keys[i] = fmt.Sprintf("%08x", id)
		}
		out = append(out, fmt.Sprintf("keyring: generation %d, data keys %s, current %08x",
			r.DBAfter.Generation, strings.Join(keys, ", "), r.CurrentKey))
	}
	switch {
	case r.DBWritten && r.FileWritten:
		out = append(out, fmt.Sprintf("keyring: merged both copies into generation %d and wrote it to the database and the keyring file", r.FileAfter.Generation))
	case r.DBWritten && r.FileBefore.Present && r.DBBefore.Present && r.FileBefore.Generation > r.DBBefore.Generation:
		out = append(out, fmt.Sprintf("keyring: the keyring file is newer than the dump's copy (keys added or changed after the backup): "+
			"wrote it to the database (generation %d)", r.DBAfter.Generation))
	case r.DBWritten:
		out = append(out, fmt.Sprintf("keyring: wrote the keyring file to the database (generation %d)", r.DBAfter.Generation))
	case r.FileWritten && !r.FileBefore.Present:
		out = append(out, fmt.Sprintf("keyring: the keyring file was missing: rebuilt it from the database (generation %d)", r.FileAfter.Generation))
	case r.FileWritten:
		out = append(out, fmt.Sprintf("keyring: the dump's copy is newer than the keyring file: wrote it to the keyring file (generation %d)", r.FileAfter.Generation))
	case r.FileBefore.Present && r.DBBefore.Present:
		out = append(out, "keyring: the database copy and the keyring file agree")
	}
	return out
}
