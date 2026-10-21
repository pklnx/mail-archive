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
	"time"
)

// Store is a content-addressed file store rooted at a directory.
//
// Durability: on Linux, New, Put and Remove return success only after
// the files and directory entries their result depends on have been
// synchronized with fsync. An error may still leave the result visible
// (a published file, a removed one); callers treat it as a failure and
// retry. Visibility, Stat and Exists are no durability barriers. See
// docs/development/architecture.md, "Blob durability".
type Store struct {
	root string // as configured, returned by Root
	// phys is the absolute root with symlinks resolved when New ran. The
	// directory barriers use it, so that the target's ancestry is
	// synchronized, not the lexical parents of a symlink.
	phys string
	ops  fsops
}

// syncCloser is an open file or directory that can be synchronized.
type syncCloser interface {
	Sync() error
	Close() error
}

// fsops are the filesystem operations whose failure or order matters for
// durability. Production uses the os functions; tests of this package
// replace them per Store to inject failures and record the order.
type fsops struct {
	// open opens a file or directory read-only, to synchronize it.
	open func(name string) (syncCloser, error)
	// temp returns the handle Put synchronizes and closes its temporary
	// file through.
	temp func(f *os.File) syncCloser
	// link publishes the temporary file under its final name; it fails
	// with fs.ErrExist if the name exists and never replaces it.
	link func(oldname, newname string) error
	// unlink removes a name.
	unlink func(name string) error
}

var osOps = fsops{
	open:   func(name string) (syncCloser, error) { return os.Open(name) }, //nolint:gosec // paths within the store and its ancestors
	temp:   func(f *os.File) syncCloser { return f },
	link:   os.Link,
	unlink: os.Remove,
}

// Blob describes a stored object.
type Blob struct {
	SHA256 string // lowercase hex
	Size   int64
	Path   string // relative to the store root, slash separated
}

// AncestorError is a failed directory barrier on a parent of the store
// root. New synchronizes every ancestor of the root, so each of them must
// be readable by the service user.
type AncestorError struct {
	Dir string // the physical path of the ancestor
	Err error
}

// Error names the ancestor and the cause.
func (e *AncestorError) Error() string {
	return fmt.Sprintf("parent directory %s of the blob store: %v", e.Dir, e.Err)
}

// Unwrap returns the cause.
func (e *AncestorError) Unwrap() error { return e.Err }

// New creates the store directories if needed and makes their creation
// durable. Every call, also a retry after a failed one, synchronizes
// messages/ and tmp/, then the physical store root and each of its
// ancestors up to the filesystem root, bottom up: a directory that
// already existed may still be the unsynchronized result of an earlier
// attempt.
func New(root string) (*Store, error) {
	return newStore(root, osOps)
}

func newStore(root string, ops fsops) (*Store, error) {
	for _, dir := range []string{"messages", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			return nil, err
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	phys, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, phys: phys, ops: ops}
	for _, dir := range []string{"messages", "tmp"} {
		if err := s.syncDir(filepath.Join(phys, dir)); err != nil {
			return nil, err
		}
	}
	if err := s.syncDir(phys); err != nil {
		return nil, err
	}
	for dir := phys; ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		if err := s.syncDir(dir); err != nil {
			return nil, &AncestorError{Dir: dir, Err: err}
		}
	}
	return s, nil
}

// syncDir opens a directory, synchronizes and closes it. A close error
// after a failed sync does not hide the sync error.
func (s *Store) syncDir(dir string) error {
	d, err := s.ops.open(dir)
	if err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return syncClose(d, "sync directory "+dir)
}

// syncClose synchronizes and closes f; what names it in errors.
func syncClose(f syncCloser, what string) error {
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: %w", what, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("%s: close: %w", what, err)
	}
	return nil
}

// RelPath returns the relative path for a hash: messages/ab/cd/<hash>.eml.
func RelPath(hash string) string {
	return "messages/" + hash[0:2] + "/" + hash[2:4] + "/" + hash + ".eml"
}

// publishAttempts bounds how often Put switches between publishing its
// own file and synchronizing an existing one, when another writer
// creates the final name and a removal takes it away again meanwhile.
const publishAttempts = 3

// Put streams r to disk while hashing it. If a blob with the same hash
// already exists, the new copy is discarded and created is false.
//
// The bytes go to a temporary file in tmp/, which is synchronized and
// closed, then hard-linked under its final name, which never replaces an
// existing file: of identical concurrent writers exactly one creates it
// (created is true). Every successful call, also one that found the file,
// synchronizes the final file and its directories (messages/ab/cd,
// messages/ab, messages) before it returns, so a retry after a failure
// completes what the failed call left. After an error the final file may
// exist; it stays for the retry. Files are readable only by the owner
// (mode 0600, from os.CreateTemp).
func (s *Store) Put(r io.Reader) (blob Blob, created bool, err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "put-*")
	if err != nil {
		return Blob{}, false, err
	}
	tmpName := tmp.Name()
	open := true
	defer func() {
		if open {
			_ = tmp.Close()
		}
		// The temporary name goes in every case; after publication it is
		// a second link to the final file. Not durable: a stale name in
		// tmp/ after a crash is a known state (verify).
		_ = os.Remove(tmpName)
	}()

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return Blob{}, false, fmt.Errorf("write temp file: %w", err)
	}
	hash := hex.EncodeToString(h.Sum(nil))
	blob = Blob{SHA256: hash, Size: size, Path: RelPath(hash)}
	final := s.abs(blob.Path)

	synced := false
	for attempt := 1; ; attempt++ {
		_, err := os.Lstat(final)
		if err == nil {
			err = s.syncFile(final)
			if err == nil {
				if err := s.syncShard(hash); err != nil {
					return Blob{}, false, err
				}
				return blob, false, nil
			}
			// A removal (sweep, verify --fix-orphans) took the file away
			// between the lookup and the open: publish this call's copy.
			if !errors.Is(err, fs.ErrNotExist) || attempt == publishAttempts {
				return Blob{}, false, err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Blob{}, false, err
		}

		if !synced {
			open = false
			if err := syncClose(s.ops.temp(tmp), "sync temp file"); err != nil {
				return Blob{}, false, err
			}
			synced = true
		}
		if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
			return Blob{}, false, err
		}
		err = s.ops.link(tmpName, final)
		if err == nil {
			if err := s.syncShard(hash); err != nil {
				return Blob{}, false, err
			}
			return blob, true, nil
		}
		// Another writer published the same content first: synchronize
		// its file instead.
		if !errors.Is(err, fs.ErrExist) || attempt == publishAttempts {
			return Blob{}, false, fmt.Errorf("publish blob: %w", err)
		}
	}
}

// syncFile synchronizes an existing file.
func (s *Store) syncFile(name string) error {
	f, err := s.ops.open(name)
	if err != nil {
		return err
	}
	return syncClose(f, "sync blob "+name)
}

// syncShard synchronizes the directories of a blob, bottom up:
// messages/ab/cd, messages/ab, messages. They are synchronized on every
// call, also when they existed: another writer may have created them and
// not finished its barrier, or an earlier attempt failed after creating
// them. The store root was made durable by New.
func (s *Store) syncShard(hash string) error {
	leaf := filepath.Join(s.phys, "messages", hash[0:2], hash[2:4])
	for _, dir := range []string{leaf, filepath.Dir(leaf), filepath.Dir(filepath.Dir(leaf))} {
		if err := s.syncDir(dir); err != nil {
			return err
		}
	}
	return nil
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

// Remove deletes the blob for hash and makes the removal durable before
// it returns nil: it synchronizes the containing directory, also when the
// file was already gone, since an earlier call may have unlinked it and
// failed before its barrier. If shard directories are missing, the
// nearest existing directory and its ancestors up to the store root are
// synchronized; a missing store root is an error. The caller holds the
// blob lock exclusively and has checked that no row refers to it.
func (s *Store) Remove(hash string) error {
	if !IsHash(hash) {
		return fmt.Errorf("not a SHA-256: %q", hash)
	}
	if err := s.ops.unlink(s.abs(RelPath(hash))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dir := filepath.Join(s.phys, "messages", hash[0:2], hash[2:4])
	err := s.syncDir(dir)
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// The shard is gone: the absence of the file is durable once the
	// removal of each missing directory is.
	for dir != s.phys {
		dir = filepath.Dir(dir)
		err := s.syncDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			if dir == s.phys {
				return fmt.Errorf("blob store root unavailable: %w", err)
			}
			continue
		}
		if err != nil {
			return err
		}
		// Found the nearest existing directory: synchronize the rest of
		// the chain through the root.
		for dir != s.phys {
			dir = filepath.Dir(dir)
			if err := s.syncDir(dir); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

func (s *Store) abs(relPath string) string {
	return filepath.Join(s.root, filepath.FromSlash(relPath))
}

// Root returns the directory the store lives in, as passed to New.
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
	// ModTime is the last write of a file in tmp/, for EntryTemp.
	ModTime time.Time
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
		} else if info, err := d.Info(); errors.Is(err, fs.ErrNotExist) {
			continue // a write finished meanwhile: Put removed its temporary name
		} else if err != nil {
			return err
		} else {
			e.Size, e.ModTime = info.Size(), info.ModTime()
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
