package blobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
)

// ErrNoKeys means an encrypted blob was found but the store has no keyring
// (no archive key configured).
var ErrNoKeys = errors.New("the message file is encrypted, but no archive key is configured")

// Keys is what SetKeys needs of an opened keyring.
type Keys interface {
	blobcrypt.DataKeys
	Current() (uint32, []byte, error)
}

// SetKeys turns on reading encrypted blobs with the data keys of keys and,
// with encrypt, writing every new blob encrypted under its current data
// key (#392, part 2). It must be called before the store is used; the
// keyring must stay unchanged while the store is in use.
func (s *Store) SetKeys(keys Keys, encrypt bool) error {
	if keys == nil {
		return errors.New("no keyring")
	}
	id, dk, err := keys.Current()
	if err != nil {
		return err
	}
	s.keys, s.encrypt, s.keyID, s.dk = keys, encrypt, id, dk
	return nil
}

// Encrypts reports whether Put writes encrypted blobs.
func (s *Store) Encrypts() bool { return s.encrypt }

// writeEncrypted encrypts r into tmp while hashing the plaintext into h,
// and binds the final chunk to the hash. It returns the plaintext size.
func (s *Store) writeEncrypted(tmp io.Writer, h hash.Hash, r io.Reader) (int64, error) {
	w, err := blobcrypt.NewWriter(tmp, s.keyID, s.dk, blobcrypt.PurposeMessage)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(io.MultiWriter(w, h), r)
	if err != nil {
		return n, err
	}
	return n, w.Finish(h.Sum(nil))
}

// usableHeader reports whether the encrypted file at path has a valid
// header that names a data key of the keyring, so that Put may dedup onto
// it. Its content is checked by verify, as for plaintext files.
func (s *Store) usableHeader(path string) bool {
	f, err := os.Open(path) //nolint:gosec // a blob path of the store
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	var raw [blobcrypt.HeaderSize]byte
	n, _ := io.ReadFull(f, raw[:])
	h, err := blobcrypt.ParseHeader(raw[:n])
	if err != nil || h.Purpose != blobcrypt.PurposeMessage {
		return false
	}
	_, ok := s.keys.DataKey(h.KeyID)
	return ok
}

// readBack reads the file at path in full, decrypting it if encrypted,
// and checks that its plaintext hashes to hash.
func (s *Store) readBack(path, hash string, encrypted bool) error {
	m, err := s.openFile(path, hash, encrypted)
	if err != nil {
		return fmt.Errorf("read back %s: %w", path, err)
	}
	defer func() { _ = m.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, m); err != nil {
		return fmt.Errorf("read back %s: %w", path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != hash {
		return fmt.Errorf("read back %s: content hash %s, want %s", path, got, hash)
	}
	return nil
}

// HasPlaintext reports whether the plaintext file of hash exists.
func (s *Store) HasPlaintext(hash string) bool {
	_, err := os.Lstat(s.abs(RelPath(hash)))
	return err == nil
}

// Message is an opened message file: its plaintext, with random access.
// It is an io.ReadSeeker for http.ServeContent and an io.ReaderAt; ReadAt
// is safe for concurrent use, Read and Seek share one position.
type Message struct {
	f    *os.File
	r    readSeekerAt
	size int64
	enc  bool
	path string
}

type readSeekerAt interface {
	io.ReadSeeker
	io.ReaderAt
}

// Read reads the plaintext from the current position.
func (m *Message) Read(p []byte) (int, error) { return m.r.Read(p) }

// ReadAt reads the plaintext at off.
func (m *Message) ReadAt(p []byte, off int64) (int, error) { return m.r.ReadAt(p, off) }

// Seek sets the position of Read.
func (m *Message) Seek(offset int64, whence int) (int64, error) { return m.r.Seek(offset, whence) }

// Close closes the file.
func (m *Message) Close() error { return m.f.Close() }

// Size is the plaintext length.
func (m *Message) Size() int64 { return m.size }

// Encrypted reports whether the file is the encrypted form.
func (m *Message) Encrypted() bool { return m.enc }

// Path is the file's path relative to the store root.
func (m *Message) Path() string { return m.path }

// OpenMessage opens the message with this hash in either form: it tries
// the encrypted file, then the plaintext one, then the encrypted one again,
// so that it finds the message while an offline conversion of a crashed
// run is repeated, or a Put replaces one form by the other. An encrypted
// file is authenticated on open (header, size and the binding to hash, see
// blobcrypt.Open); a refusal wraps blobcrypt.ErrCorrupt or
// blobcrypt.ErrUnknownKey, a store without keys ErrNoKeys. Without either
// file the error wraps fs.ErrNotExist.
func (s *Store) OpenMessage(hash string) (*Message, error) {
	if !IsHash(hash) {
		return nil, fmt.Errorf("not a SHA-256: %q", hash)
	}
	var lastErr error
	for _, enc := range []bool{true, false, true} {
		path := RelPath(hash)
		if enc {
			path = EncPath(hash)
		}
		m, err := s.openFile(s.abs(path), hash, enc)
		if err == nil {
			m.path = path
			return m, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// openFile opens a blob file by its absolute path.
func (s *Store) openFile(path, hash string, encrypted bool) (*Message, error) {
	if s.beforeOpen != nil {
		s.beforeOpen(path)
	}
	f, err := os.Open(path) //nolint:gosec // a blob path of the store
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !encrypted {
		return &Message{f: f, r: f, size: info.Size()}, nil
	}
	if s.keys == nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", path, ErrNoKeys)
	}
	binding, err := hex.DecodeString(hash)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	r, err := blobcrypt.Open(f, info.Size(), s.keys, blobcrypt.PurposeMessage, binding)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Message{f: f, r: r, size: r.Size(), enc: true}, nil
}
