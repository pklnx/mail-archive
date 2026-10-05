// Package blobstore stores raw messages on disk, addressed by the SHA-256
// hash of their content. Identical messages are stored exactly once.
package blobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Store is a content-addressed file store rooted at a directory.
type Store struct {
	root string
}

// Blob describes a stored object.
type Blob struct {
	SHA256 string // lowercase hex
	Size   int64
	Path   string // relative to the store root, slash separated
}

// New creates the store directories if needed.
func New(root string) (*Store, error) {
	for _, dir := range []string{"messages", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{root: root}, nil
}

// RelPath returns the relative path for a hash: messages/ab/cd/<hash>.eml.
func RelPath(hash string) string {
	return "messages/" + hash[0:2] + "/" + hash[2:4] + "/" + hash + ".eml"
}

// Put streams r to disk while hashing it. If a blob with the same hash
// already exists, the new copy is discarded and created is false.
// The final file is written atomically (temp file, fsync, rename) and is
// readable only by the owner (mode 0600, from os.CreateTemp).
func (s *Store) Put(r io.Reader) (blob Blob, created bool, err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "put-*")
	if err != nil {
		return Blob{}, false, err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpName) // no-op after successful rename
	}()

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return Blob{}, false, fmt.Errorf("write temp file: %w", err)
	}
	hash := hex.EncodeToString(h.Sum(nil))
	blob = Blob{SHA256: hash, Size: size, Path: RelPath(hash)}

	final := s.abs(blob.Path)
	if _, err := os.Stat(final); err == nil {
		return blob, false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Blob{}, false, err
	}

	if err := tmp.Sync(); err != nil {
		return Blob{}, false, err
	}
	if err := tmp.Close(); err != nil {
		tmp = nil
		return Blob{}, false, err
	}
	tmp = nil
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return Blob{}, false, err
	}
	if err := os.Rename(tmpName, final); err != nil {
		return Blob{}, false, err
	}
	return blob, true, nil
}

// Open opens a stored blob by its relative path.
func (s *Store) Open(relPath string) (*os.File, error) {
	return os.Open(s.abs(relPath))
}

// Exists reports whether the blob for hash is present on disk.
func (s *Store) Exists(hash string) bool {
	_, err := os.Stat(s.abs(RelPath(hash)))
	return err == nil
}

func (s *Store) abs(relPath string) string {
	return filepath.Join(s.root, filepath.FromSlash(relPath))
}
