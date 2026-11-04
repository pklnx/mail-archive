package blobcrypt

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// KeysDir is the keyring's directory below the data directory, and
// KeyringFile its file name.
const (
	KeysDir     = "keys"
	KeyringFile = "keyring"
	lockFile    = "keyring.lock"
)

// ErrNoKeyring means the keyring file does not exist.
var ErrNoKeyring = errors.New("no keyring")

// KeyringPath is <data dir>/keys/keyring.
func KeyringPath(dataDir string) string { return filepath.Join(dataDir, KeysDir, KeyringFile) }

// ReadKeyringFile reads and parses a keyring file. A missing file wraps
// ErrNoKeyring and fs.ErrNotExist.
func ReadKeyringFile(path string) (*Keyring, error) {
	f, err := os.Open(path) //nolint:gosec // the configured data directory
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w at %s: %w", ErrNoKeyring, path, err)
		}
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(f, maxKeyringSize+1))
	if err != nil {
		return nil, err
	}
	k, err := ParseKeyring(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

// fileOps are the steps of an atomic replace whose failure matters;
// tests replace them to inject a crash.
type fileOps struct {
	rename func(oldpath, newpath string) error
}

var osFileOps = fileOps{rename: os.Rename}

// WriteKeyringFile replaces the keyring file atomically: a temporary file
// (0600) in the same directory is written and synchronized, renamed over
// the old file, and the directory synchronized. A crash leaves the old or
// the new file, never a mix; a leftover temporary file is ignored. The
// directory is created (0700) when missing.
func WriteKeyringFile(path string, k *Keyring) error {
	return writeKeyringFile(path, k, osFileOps)
}

func writeKeyringFile(path string, k *Keyring, ops fileOps) (err error) {
	data, err := k.Marshal()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+KeyringFile+"-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ops.rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // the keyring's directory
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// ErrLocked means another process changes the keyring right now.
var ErrLocked = errors.New("another encryption command is changing the keyring; try again when it has finished")

// LockKeyring takes an exclusive lock on the keyring of dataDir for a
// read-modify-write, without waiting: ErrLocked when it is held. The keys
// directory is created (0700) when missing.
func LockKeyring(dataDir string) (unlock func(), err error) {
	dir := filepath.Join(dataDir, KeysDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return lockFileExclusive(filepath.Join(dir, lockFile))
}
