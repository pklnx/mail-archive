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
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/keepia/archive/internal/blobcrypt"
)

// ConvertOutcome is what Conversion did with one file.
type ConvertOutcome int

const (
	// ConvertNone means nothing to do: the file is in the target form already,
	// it is gone, or it is not a file the conversion handles.
	ConvertNone ConvertOutcome = iota
	// ConvertDone means the target form was written and read back, or an
	// existing target form was read back; the source is unlinked by the
	// next Commit.
	ConvertDone
	// ConvertCorrupt means the source fails its hash or its authentication, or
	// names a data key the keyring lacks. It stays where it is, and so
	// does an existing target form.
	ConvertCorrupt
)

// Conversion converts existing files between the plaintext and the
// encrypted form (#392, part 3), for the offline `encryption enable` and
// `disable`, which run while no writer runs. Files are converted one at a
// time by Message and Orphan; Commit then makes the batch durable and
// removes the redundant copies:
//
//  1. Each file of the target form is written to tmp/ while the source is
//     hashed (and decrypted or encrypted), synchronized, read back in full
//     with its hash checked, and hard-linked under its final name, which
//     never replaces a file; a damaged file of the target form is replaced
//     by an atomic rename instead.
//  2. Commit synchronizes every touched directory, so that the new names
//     are durable; then it unlinks the sources and synchronizes the
//     directories again.
//
// A crash at any point leaves at least one readable copy of every file,
// and repeating the batch completes it: a source whose target form exists
// is unlinked once that target form has been read back.
type Conversion struct {
	s       *Store
	encrypt bool
	// rekey: re-encrypt files under another data key with the current one
	// (NewRekey); named collects the data keys the headers name.
	rekey  bool
	named  map[uint32]struct{}
	dirs   map[string]struct{} // absolute directories to synchronize
	unlink []string            // absolute paths of sources, for Commit
}

// NewConversion starts a conversion to the encrypted form (encrypt) or to
// the plaintext form. Both directions need the keys (SetKeys): encrypting
// writes under the current data key, decrypting reads with the keyring.
func (s *Store) NewConversion(encrypt bool) (*Conversion, error) {
	if s.keys == nil {
		return nil, ErrNoKeys
	}
	return &Conversion{s: s, encrypt: encrypt, dirs: map[string]struct{}{}}, nil
}

// NewRekey starts a rekey conversion (#392, part 5): every encrypted file
// whose header names a data key other than the current one (SetKeys) is
// re-encrypted under the current key. The new copy is written to tmp/,
// synchronized, read back in full with its hash checked, and then replaces
// the old file under the same name by one atomic rename; Commit
// synchronizes the directory. There is never an unlink followed by a link,
// and the old key stays in the keyring until the caller retires it, so a
// crash at any point leaves a readable file and a repeat completes it.
// Plaintext files met on the way (stragglers) are encrypted as by
// NewConversion(true).
func (s *Store) NewRekey() (*Conversion, error) {
	if s.keys == nil || !s.encrypt {
		return nil, ErrNoKeys
	}
	return &Conversion{s: s, encrypt: true, rekey: true, named: map[uint32]struct{}{}, dirs: map[string]struct{}{}}, nil
}

// NamedKeys lists, in ascending order, the data keys named by the headers
// of the encrypted files a rekey conversion met since it started or since
// ResetNamed: the current key for each file it re-encrypted or found
// current, the old key for each file it could not re-encrypt.
func (c *Conversion) NamedKeys() []uint32 {
	ids := make([]uint32, 0, len(c.named))
	for id := range c.named {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// ResetNamed starts a new NamedKeys list, at the start of a pass.
func (c *Conversion) ResetNamed() { clear(c.named) }

// Pending is the number of sources the next Commit unlinks.
func (c *Conversion) Pending() int { return len(c.unlink) }

// Message converts the blob of hash in messages/ to the target form.
func (c *Conversion) Message(hash string) (ConvertOutcome, error) {
	if !IsHash(hash) {
		return ConvertNone, fmt.Errorf("not a SHA-256: %q", hash)
	}
	src, dst := c.s.abs(RelPath(hash)), c.s.abs(EncPath(hash))
	if !c.encrypt {
		src, dst = dst, src
	}
	if !c.rekey {
		return c.convert(src, hash, func(string) string { return dst })
	}
	// Rekey: the encrypted file first, so that a plaintext straggler next
	// to it is dropped against the copy under the current key (or replaces
	// an encrypted copy that cannot be re-encrypted).
	out, err := c.rekeyFile(dst, hash)
	if err != nil {
		return out, err
	}
	plain, err := c.convert(src, hash, func(string) string { return dst })
	if err != nil || plain != ConvertNone {
		return plain, err
	}
	return out, nil
}

var (
	plainNameRe = regexp.MustCompile(`^([0-9a-f]{64})\.eml$`)
	encNameRe   = regexp.MustCompile(`^([0-9a-f]{64})\.enc$`)
)

// Orphan converts a regular file below orphans/ (rel is relative to the
// store root), where verify --fix-orphans moved message files without a
// row and files left in tmp/. Encrypting, a file <sha256>.eml becomes
// <sha256>.enc next to it, and any other file not named .enc becomes
// <sha256 of its content>.enc; decrypting, a file <sha256>.enc becomes
// <sha256>.eml. So `decrypt` opens each of them by its name.
func (c *Conversion) Orphan(rel string) (ConvertOutcome, error) {
	abs := c.s.abs(rel)
	info, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return ConvertNone, nil
	}
	if err != nil {
		return ConvertNone, err
	}
	if !info.Mode().IsRegular() {
		return ConvertNone, nil
	}
	dir, base := filepath.Dir(abs), filepath.Base(abs)
	if c.rekey && strings.HasSuffix(base, encExt) {
		if m := encNameRe.FindStringSubmatch(base); m != nil {
			return c.rekeyFile(abs, m[1])
		}
		// No hash in the name: it cannot be opened, so it cannot be
		// re-encrypted; its key still counts as named.
		return c.rekeyUnbound(abs)
	}
	if !c.encrypt {
		m := encNameRe.FindStringSubmatch(base)
		if m == nil {
			return ConvertNone, nil
		}
		return c.convert(abs, m[1], func(h string) string { return filepath.Join(dir, h+plainExt) })
	}
	if strings.HasSuffix(base, encExt) {
		return ConvertNone, nil
	}
	hash := ""
	if m := plainNameRe.FindStringSubmatch(base); m != nil {
		hash = m[1]
	}
	return c.convert(abs, hash, func(h string) string { return filepath.Join(dir, h+encExt) })
}

// convert converts the file src, whose plaintext hashes to hash (empty:
// unknown, then the result names the target), to the target form at
// dstOf(hash).
func (c *Conversion) convert(src, hash string, dstOf func(string) string) (ConvertOutcome, error) {
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return ConvertNone, nil
	} else if err != nil {
		return ConvertNone, err
	}
	replace := false
	if hash != "" {
		ok, err := c.targetUsable(dstOf(hash), hash)
		if err != nil {
			return ConvertNone, err
		}
		if ok {
			c.queue(src, dstOf(hash))
			return ConvertDone, nil
		}
		_, err = os.Lstat(dstOf(hash))
		replace = err == nil
	}
	tmp, got, err := c.write(src, hash)
	if errors.Is(err, errSourceCorrupt) {
		return ConvertCorrupt, nil
	}
	if err != nil {
		return ConvertNone, err
	}
	defer func() { _ = os.Remove(tmp) }()
	dst := dstOf(got)
	if hash == "" {
		ok, err := c.targetUsable(dst, got)
		if err != nil {
			return ConvertNone, err
		}
		if ok {
			c.queue(src, dst)
			return ConvertDone, nil
		}
		_, err = os.Lstat(dst)
		replace = err == nil
	}
	if replace {
		if err := c.s.ops.rename(tmp, dst); err != nil {
			return ConvertNone, fmt.Errorf("replace damaged %s: %w", dst, err)
		}
	} else if err := c.s.ops.link(tmp, dst); err != nil {
		return ConvertNone, fmt.Errorf("publish %s: %w", dst, err)
	}
	c.queue(src, dst)
	return ConvertDone, nil
}

// targetUsable reports whether the file of the target form at dst exists
// and reads back to hash. A damaged one (or one under an unknown data key)
// is not usable and is replaced; an I/O error is returned.
func (c *Conversion) targetUsable(dst, hash string) (bool, error) {
	if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	err := c.s.readBack(dst, hash, c.encrypt)
	if err == nil {
		return true, nil
	}
	if damaged(err) || errors.Is(err, blobcrypt.ErrUnknownKey) {
		return false, nil
	}
	return false, err
}

// headerKey reads the data key id from the header of the encrypted file at
// path. A missing file wraps fs.ErrNotExist; an invalid header is
// errSourceCorrupt.
func (c *Conversion) headerKey(path string) (uint32, error) {
	f, err := os.Open(path) //nolint:gosec // a file of the store
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	var raw [blobcrypt.HeaderSize]byte
	n, err := io.ReadFull(f, raw[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}
	h, err := blobcrypt.ParseHeader(raw[:n])
	if err != nil || h.Purpose != blobcrypt.PurposeMessage {
		return 0, errSourceCorrupt
	}
	return h.KeyID, nil
}

// rekeyUnbound is a rekey of an encrypted file whose name carries no hash:
// one under the current key needs nothing, any other is corrupt (it stays,
// and so does its key).
func (c *Conversion) rekeyUnbound(path string) (ConvertOutcome, error) {
	id, err := c.headerKey(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ConvertNone, nil
	case errors.Is(err, errSourceCorrupt):
		return ConvertCorrupt, nil
	case err != nil:
		return ConvertNone, err
	}
	c.named[id] = struct{}{}
	if id == c.s.keyID {
		return ConvertNone, nil
	}
	return ConvertCorrupt, nil
}

// rekeyFile re-encrypts the encrypted file at path, bound to hash, under
// the current data key, unless its header already names that key. A file
// that cannot be decrypted (damaged, or under a key the keyring lacks)
// stays as it is: ConvertCorrupt, and its key counts as named.
func (c *Conversion) rekeyFile(path, hash string) (ConvertOutcome, error) {
	id, err := c.headerKey(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ConvertNone, nil
	case errors.Is(err, errSourceCorrupt):
		return ConvertCorrupt, nil
	case err != nil:
		return ConvertNone, err
	}
	if id == c.s.keyID {
		c.named[id] = struct{}{}
		return ConvertNone, nil
	}
	tmp, err := c.reencrypt(path, hash)
	if errors.Is(err, errSourceCorrupt) {
		c.named[id] = struct{}{}
		return ConvertCorrupt, nil
	}
	if err != nil {
		return ConvertNone, err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := c.s.ops.rename(tmp, path); err != nil {
		// The old file is untouched and still names its key.
		c.named[id] = struct{}{}
		return ConvertNone, fmt.Errorf("replace %s: %w", path, err)
	}
	c.named[c.s.keyID] = struct{}{}
	c.dirs[filepath.Dir(path)] = struct{}{}
	return ConvertDone, nil
}

// reencrypt decrypts the file at path, bound to hash, and encrypts it
// under the current data key into a new file in tmp/, while hashing the
// plaintext; it synchronizes the new file and reads it back. Another hash
// or a decryption failure is errSourceCorrupt.
func (c *Conversion) reencrypt(path, hash string) (tmpPath string, err error) {
	m, err := c.s.openFile(path, hash, true)
	if err != nil {
		if sourceCorrupt(err) {
			return "", errSourceCorrupt
		}
		return "", err
	}
	defer func() { _ = m.Close() }()
	tmp, err := os.CreateTemp(filepath.Join(c.s.root, "tmp"), "rekey-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	open := true
	defer func() {
		if open {
			_ = tmp.Close()
		}
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	h := sha256.New()
	if _, err = c.s.writeEncrypted(tmp, h, m); err != nil {
		if sourceCorrupt(err) {
			return "", errSourceCorrupt
		}
		return "", fmt.Errorf("re-encrypt %s: %w", path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != hash {
		return "", errSourceCorrupt
	}
	open = false
	if err = syncClose(c.s.ops.temp(tmp), "sync temp file"); err != nil {
		return "", err
	}
	if err = c.s.readBack(name, hash, true); err != nil {
		return "", err
	}
	return name, nil
}

// errSourceCorrupt is a source that cannot be converted: another hash, an
// authentication failure, an unknown data key.
var errSourceCorrupt = errors.New("source corrupt")

// write writes the target form of src to a new file in tmp/, synchronizes
// it and reads it back. It returns the temporary path and the plaintext
// hash; with hash set, another hash is errSourceCorrupt.
func (c *Conversion) write(src, hash string) (tmpPath, got string, err error) {
	var r io.Reader
	var closer io.Closer
	if c.encrypt {
		f, err := os.Open(src) //nolint:gosec // a file of the store
		if err != nil {
			return "", "", err
		}
		r, closer = f, f
	} else {
		bind := hash
		if bind == "" {
			return "", "", errors.New("decrypting needs the hash of the file name")
		}
		m, err := c.s.openFile(src, bind, true)
		if err != nil {
			if sourceCorrupt(err) {
				return "", "", errSourceCorrupt
			}
			return "", "", err
		}
		r, closer = m, m
	}
	defer func() { _ = closer.Close() }()

	tmp, err := os.CreateTemp(filepath.Join(c.s.root, "tmp"), "convert-*")
	if err != nil {
		return "", "", err
	}
	name := tmp.Name()
	open := true
	defer func() {
		if open {
			_ = tmp.Close()
		}
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	h := sha256.New()
	if c.encrypt {
		_, err = c.s.writeEncrypted(tmp, h, r)
	} else {
		_, err = io.Copy(io.MultiWriter(tmp, h), r)
	}
	if err != nil {
		if sourceCorrupt(err) {
			return "", "", errSourceCorrupt
		}
		return "", "", fmt.Errorf("convert %s: %w", src, err)
	}
	got = hex.EncodeToString(h.Sum(nil))
	if hash != "" && got != hash {
		return "", "", errSourceCorrupt
	}
	open = false
	if err := syncClose(c.s.ops.temp(tmp), "sync temp file"); err != nil {
		return "", "", err
	}
	if err := c.s.readBack(name, got, c.encrypt); err != nil {
		return "", "", err
	}
	return name, got, nil
}

// sourceCorrupt reports whether err from reading a source means the
// source itself cannot be converted.
func sourceCorrupt(err error) bool {
	return damaged(err) || errors.Is(err, blobcrypt.ErrUnknownKey)
}

// queue records src for unlinking at Commit, after the directories of src
// and dst are synchronized.
func (c *Conversion) queue(src, dst string) {
	c.dirs[filepath.Dir(src)] = struct{}{}
	c.dirs[filepath.Dir(dst)] = struct{}{}
	c.unlink = append(c.unlink, src)
}

// Commit makes the batch durable: it synchronizes every touched directory,
// so that the files of the target form survive a crash, then unlinks the
// sources and synchronizes the directories again. After an error the
// batch is incomplete but safe; repeating it completes it.
func (c *Conversion) Commit() error {
	dirs := make([]string, 0, len(c.dirs))
	for d := range c.dirs {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		if err := c.s.syncDir(d); err != nil {
			return err
		}
	}
	for _, p := range c.unlink {
		if err := c.s.ops.unlink(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	for _, d := range dirs {
		if err := c.s.syncDir(d); err != nil {
			return err
		}
	}
	c.unlink = c.unlink[:0]
	clear(c.dirs)
	return nil
}

// MoveAside moves the file at rel (relative to the store root) to the
// same relative path below dir (relative to the store root, such as
// orphans/<time>), durably. `encryption disable` moves encrypted files it
// cannot decrypt out of messages/ this way, so that no encrypted file is
// left there to make a later start derive the state "encrypting".
func (s *Store) MoveAside(rel, dir string) error {
	src := filepath.Join(s.phys, filepath.FromSlash(rel))
	dst := filepath.Join(s.phys, filepath.FromSlash(dir), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	if err := s.ops.rename(src, dst); err != nil {
		return err
	}
	// The new name and every directory created for it, up to the root.
	for d := filepath.Dir(dst); ; d = filepath.Dir(d) {
		if err := s.syncDir(d); err != nil {
			return err
		}
		if d == s.phys || d == filepath.Dir(d) {
			break
		}
	}
	return s.syncDir(filepath.Dir(src))
}

// MoveTempAside moves every file in tmp/ to dir/tmp/ (dir relative to the
// store root, such as orphans/<time>), durably, and returns the new paths
// relative to the root. `encryption enable` and `disable` call it under
// the exclusive writers lock, when no writer and so no Put can own a file
// in tmp/: what is there is left by a process that ended mid-write. Below
// orphans/ the conversion encrypts it like every other orphan, so no
// plaintext stays in tmp/ (review round 1 of #513).
func (s *Store) MoveTempAside(dir string) ([]string, error) {
	var temps []string
	err := s.walkTemp(func(e Entry) error {
		if e.Kind == EntryTemp {
			temps = append(temps, e.Path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var moved []string
	for _, rel := range temps {
		if err := s.MoveAside(rel, dir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return moved, err
		}
		moved = append(moved, dir+"/"+rel)
	}
	return moved, nil
}

// MessagesAfter calls fn for every hash with a file in messages/, in
// either form, in ascending order of the hash, starting after the hash
// after (empty: from the start); size is the sum of the sizes of its
// files. Shard directories before after are skipped without being read.
// Files and directories that are not named like blobs are skipped (verify
// reports them).
func (s *Store) MessagesAfter(after string, fn func(hash string, size int64) error) error {
	base := filepath.Join(s.root, "messages")
	tops, err := readDirSorted(base)
	if err != nil {
		return err
	}
	for _, top := range tops {
		if !top.IsDir() || !isHex(top.Name(), 2) || (after != "" && top.Name() < after[:min(2, len(after))]) {
			continue
		}
		subs, err := readDirSorted(filepath.Join(base, top.Name()))
		if err != nil {
			return err
		}
		for _, sub := range subs {
			prefix := top.Name() + sub.Name()
			if !sub.IsDir() || !isHex(sub.Name(), 2) || (after != "" && prefix < after[:min(4, len(after))]) {
				continue
			}
			files, err := readDirSorted(filepath.Join(base, top.Name(), sub.Name()))
			if err != nil {
				return err
			}
			cur, size := "", int64(0)
			flush := func() error {
				if cur == "" {
					return nil
				}
				h := cur
				cur = ""
				return fn(h, size)
			}
			for _, f := range files {
				if !f.Type().IsRegular() {
					continue
				}
				name := f.Name()
				h, ok := strings.CutSuffix(name, plainExt)
				if !ok {
					h, ok = strings.CutSuffix(name, encExt)
				}
				if !ok || !isHex(h, 64) || h[:4] != prefix || h <= after {
					continue
				}
				info, err := f.Info()
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				if h != cur {
					if err := flush(); err != nil {
						return err
					}
					cur, size = h, 0
				}
				size += info.Size()
			}
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func readDirSorted(dir string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(dir) // sorted by name
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// OrphansDir is the directory below the store root where verify
// --fix-orphans moves files.
const OrphansDir = "orphans"

// OrphansAfter calls fn for every regular file below orphans/ (rel is
// relative to the store root, slash separated), in the order of
// filepath.WalkDir, starting after the path after (empty: from the start;
// compared component by component, as WalkDir orders them). Symlinks are
// not followed.
func (s *Store) OrphansAfter(after string, fn func(rel string, size int64) error) error {
	base := filepath.Join(s.root, OrphansDir)
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// A directory wholly before after is skipped.
			if after != "" && path != base && ComparePaths(rel, after) < 0 && !strings.HasPrefix(after, rel+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || (after != "" && ComparePaths(rel, after) <= 0) {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return fn(rel, info.Size())
	})
	if errors.Is(err, fs.ErrNotExist) {
		if _, serr := os.Lstat(base); errors.Is(serr, fs.ErrNotExist) {
			return nil
		}
	}
	return err
}

// ComparePaths compares two slash-separated paths component by component,
// the order of filepath.WalkDir.
func ComparePaths(a, b string) int {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := strings.Compare(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return len(as) - len(bs)
}
