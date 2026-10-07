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
	"strings"
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

// Root returns the directory the store lives in.
func (s *Store) Root() string { return s.root }

// EntryKind classifies a file found by Walk.
type EntryKind int

const (
	// EntryBlob is a file named like a blob, in its directory.
	EntryBlob EntryKind = iota
	// EntryTemp is a regular file in tmp/, left by an unfinished Put.
	EntryTemp
	// EntryUnexpected is anything else: symlinks, other file types, wrong
	// names or depths. Walk does not descend into unexpected directories.
	EntryUnexpected
)

// Entry is a file or directory found by Walk.
type Entry struct {
	Kind   EntryKind
	Path   string // relative to the store root, slash separated
	SHA256 string // for EntryBlob
	Size   int64  // for regular files
	Reason string // for EntryUnexpected
}

// Walk calls fn for every entry below messages/ and tmp/. Symlinks are
// reported, never followed.
func (s *Store) Walk(fn func(Entry) error) error {
	if err := s.walkMessages(fn); err != nil {
		return err
	}
	return s.walkTemp(fn)
}

func (s *Store) walkMessages(fn func(Entry) error) error {
	base := filepath.Join(s.root, "messages")
	return filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == base {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		parts := strings.Split(rel, "/") // messages, ab, cd, <hash>.eml
		unexpected := func(reason string) error {
			if err := fn(Entry{Kind: EntryUnexpected, Path: rel, Reason: reason}); err != nil {
				return err
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return unexpected("symlink")
		}
		depth := len(parts) - 1
		if d.IsDir() {
			if depth > 2 || !isHex(parts[depth], 2) {
				return unexpected("unexpected directory")
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return unexpected("not a regular file")
		}
		hash, ok := strings.CutSuffix(parts[depth], ".eml")
		if depth != 3 || !ok || !isHex(hash, 64) || hash[0:2] != parts[1] || hash[2:4] != parts[2] {
			return unexpected("unexpected file name or place")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return fn(Entry{Kind: EntryBlob, Path: rel, SHA256: hash, Size: info.Size()})
	})
}

func (s *Store) walkTemp(fn func(Entry) error) error {
	entries, err := os.ReadDir(filepath.Join(s.root, "tmp"))
	if err != nil {
		return err
	}
	for _, d := range entries {
		e := Entry{Kind: EntryTemp, Path: "tmp/" + d.Name()}
		if !d.Type().IsRegular() {
			e.Kind, e.Reason = EntryUnexpected, "not a regular file"
		} else if info, err := d.Info(); err != nil {
			return err
		} else {
			e.Size = info.Size()
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// IsHash reports whether s is a lowercase hex SHA-256.
func IsHash(s string) bool { return isHex(s, 64) }

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
