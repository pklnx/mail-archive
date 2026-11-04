// Package blobcrypt is the encryption at rest of message files and backups:
// the STREAM-AES-256-GCM file format, the keyring that holds the data keys
// wrapped under one or more archive keys (KEKs), and the archive key itself.
// It uses only the Go standard library. The format is specified in
// docs/development/architecture.md, "Encryption at rest"; the known-answer
// vectors in testdata freeze it.
package blobcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Format constants. Changing any of them changes the format.
const (
	// Magic starts every encrypted file.
	Magic = "MA-ENC1\n"
	// HeaderSize is the length of the header in bytes.
	HeaderSize = 64
	// Version1 is the only format version.
	Version1 = 1
	// ChunkExponent is log2 of ChunkSize; the only accepted value.
	ChunkExponent = 16
	// ChunkSize is the plaintext length of every chunk but the last.
	ChunkSize = 1 << ChunkExponent
	// TagSize is the length of the AES-GCM tag after every chunk.
	TagSize = 16
	// SaltSize is the length of the random per-file salt in the header.
	SaltSize = 32
	// KeySize is the length of every key (AES-256).
	KeySize = 32
	// BindingSize is the length of a message file's binding, the SHA-256 of
	// its plaintext.
	BindingSize = sha256.Size

	encChunkSize = ChunkSize + TagSize
	nonceSize    = 12
	// maxChunks keeps the chunk counter inside the 11 counter bytes of the
	// nonce and offsets inside an int64.
	maxChunks = (1<<63 - 1 - HeaderSize) / encChunkSize
	// maxFileSize is the largest file Open accepts: the largest whole number
	// of chunks after the header that fits an int64. Open's chunk arithmetic
	// cannot overflow up to it (TestOpenImpossibleSize).
	maxFileSize = HeaderSize + maxChunks*encChunkSize
)

// blobInfo is the HKDF info prefix of a file key; the purpose byte follows.
const blobInfo = "mail-archive blob v1"

// Purpose says what an encrypted file holds. It is part of the header and
// of the key derivation, so a file of one purpose never opens as another.
type Purpose byte

// Purposes.
const (
	// PurposeMessage is a message file, bound to the SHA-256 of its plaintext.
	PurposeMessage Purpose = 1
	// PurposeBackup is a database dump; it has no binding.
	PurposeBackup Purpose = 2
)

// String names the purpose.
func (p Purpose) String() string {
	switch p {
	case PurposeMessage:
		return "message"
	case PurposeBackup:
		return "backup"
	}
	return fmt.Sprintf("purpose %d", byte(p))
}

// Errors. Every refusal wraps one of them, so callers can tell them apart
// with errors.Is. None of them ever contains key material or plaintext.
var (
	// ErrNotEncrypted: the data does not start with Magic.
	ErrNotEncrypted = errors.New("not an encrypted file (no MA-ENC1 header)")
	// ErrCorrupt: the header is invalid, the size impossible, or a chunk
	// fails authentication (modified, truncated, reordered, extended, bound
	// to another name, or encrypted with other key material).
	ErrCorrupt = errors.New("encrypted file is corrupt")
	// ErrUnknownKey: the header names a data key the keyring does not hold.
	ErrUnknownKey = errors.New("encrypted with a data key that is not in the keyring")
)

func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}

// Header is the parsed, validated header of an encrypted file.
type Header struct {
	Purpose Purpose
	KeyID   uint32 // the data key, never 0
	Salt    [SaltSize]byte
}

// Layout of the 64 header bytes.
const (
	offVersion  = 8
	offPurpose  = 9
	offExponent = 10
	offReserved = 11
	offKeyID    = 12
	offSalt     = 16
	offPadding  = offSalt + SaltSize // 48; zero up to HeaderSize
)

func (h *Header) marshal() [HeaderSize]byte {
	var b [HeaderSize]byte
	copy(b[:], Magic)
	b[offVersion] = Version1
	b[offPurpose] = byte(h.Purpose)
	b[offExponent] = ChunkExponent
	binary.BigEndian.PutUint32(b[offKeyID:], h.KeyID)
	copy(b[offSalt:], h.Salt[:])
	return b
}

// ParseHeader validates the first HeaderSize bytes of a file strictly:
// only the known magic, version, purposes and chunk size, a non-zero key
// id, and zero reserved and padding bytes are accepted. Anything else is
// ErrCorrupt (or ErrNotEncrypted for another magic), before any buffer is
// allocated.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < len(Magic) || string(b[:len(Magic)]) != Magic {
		if len(b) < len(Magic) && bytes.HasPrefix([]byte(Magic), b) {
			return Header{}, corrupt("truncated header")
		}
		return Header{}, ErrNotEncrypted
	}
	if len(b) < HeaderSize {
		return Header{}, corrupt("truncated header")
	}
	var h Header
	switch {
	case b[offVersion] != Version1:
		return Header{}, corrupt("unknown format version %d", b[offVersion])
	case b[offPurpose] != byte(PurposeMessage) && b[offPurpose] != byte(PurposeBackup):
		return Header{}, corrupt("unknown purpose %d", b[offPurpose])
	case b[offExponent] != ChunkExponent:
		return Header{}, corrupt("unsupported chunk size 2^%d", b[offExponent])
	case b[offReserved] != 0:
		return Header{}, corrupt("reserved header byte is not zero")
	}
	for _, c := range b[offPadding:HeaderSize] {
		if c != 0 {
			return Header{}, corrupt("header padding is not zero")
		}
	}
	h.Purpose = Purpose(b[offPurpose])
	h.KeyID = binary.BigEndian.Uint32(b[offKeyID:])
	if h.KeyID == 0 {
		return Header{}, corrupt("data key id 0")
	}
	copy(h.Salt[:], b[offSalt:offPadding])
	return h, nil
}

// DataKeys looks up a data key by its id. An opened Keyring implements it.
type DataKeys interface {
	DataKey(id uint32) (key []byte, ok bool)
}

// fileAEAD derives the file key HKDF-SHA256(DK, salt, info ‖ purpose).
func fileAEAD(dk []byte, h *Header) (cipher.AEAD, error) {
	if len(dk) != KeySize {
		return nil, fmt.Errorf("data key must be %d bytes", KeySize)
	}
	key, err := hkdf.Key(sha256.New, dk, h.Salt[:], blobInfo+string([]byte{byte(h.Purpose)}), KeySize)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// chunkNonce sets n to the 11-byte big-endian chunk counter followed by
// the last-chunk flag (1 for the final chunk, else 0).
func chunkNonce(n *[nonceSize]byte, counter uint64, last bool) {
	*n = [nonceSize]byte{}
	binary.BigEndian.PutUint64(n[3:11], counter)
	if last {
		n[11] = 1
	}
}

// checkBinding: a message file is bound to its 32-byte plaintext SHA-256,
// a backup to nothing.
func checkBinding(p Purpose, binding []byte) error {
	switch p {
	case PurposeMessage:
		if len(binding) != BindingSize {
			return fmt.Errorf("a message file needs its %d-byte SHA-256 as binding, got %d bytes", BindingSize, len(binding))
		}
	case PurposeBackup:
		if len(binding) != 0 {
			return errors.New("a backup has no binding")
		}
	default:
		return fmt.Errorf("unknown purpose %d", byte(p))
	}
	return nil
}

// Writer encrypts a stream. It holds one chunk (64 KiB plus the tag) and
// writes each chunk once the next byte shows that it is not the last.
type Writer struct {
	w       io.Writer
	aead    cipher.AEAD
	purpose Purpose
	header  [HeaderSize]byte
	buf     []byte // ChunkSize plaintext + TagSize, sealed in place
	n       int    // plaintext bytes in buf
	counter uint64
	nonce   [nonceSize]byte
	err     error // sticky
}

// NewWriter writes the header of a new file encrypted with the data key
// (keyID, dk) to w and returns a Writer for its plaintext. The salt comes
// from crypto/rand.
func NewWriter(w io.Writer, keyID uint32, dk []byte, p Purpose) (*Writer, error) {
	var salt [SaltSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, err
	}
	return newWriterSalt(w, keyID, dk, p, salt)
}

func newWriterSalt(w io.Writer, keyID uint32, dk []byte, p Purpose, salt [SaltSize]byte) (*Writer, error) {
	if p != PurposeMessage && p != PurposeBackup {
		return nil, fmt.Errorf("unknown purpose %d", byte(p))
	}
	if keyID == 0 {
		return nil, errors.New("data key id 0")
	}
	h := Header{Purpose: p, KeyID: keyID, Salt: salt}
	aead, err := fileAEAD(dk, &h)
	if err != nil {
		return nil, err
	}
	wr := &Writer{w: w, aead: aead, purpose: p, header: h.marshal(), buf: make([]byte, encChunkSize)}
	if _, err := w.Write(wr.header[:]); err != nil {
		return nil, err
	}
	return wr, nil
}

// Write encrypts p. A chunk is written only when it is full and more
// plaintext follows, so the final chunk is always written by Finish.
func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	written := 0
	for len(p) > 0 {
		if w.n == ChunkSize {
			if err := w.flush(false, nil); err != nil {
				return written, err
			}
		}
		c := copy(w.buf[w.n:ChunkSize], p)
		w.n += c
		written += c
		p = p[c:]
	}
	return written, nil
}

func (w *Writer) flush(last bool, binding []byte) error {
	if w.counter >= maxChunks {
		w.err = errors.New("encrypted stream too long")
		return w.err
	}
	chunkNonce(&w.nonce, w.counter, last)
	aad := w.header[:]
	if last {
		aad = append(append(make([]byte, 0, HeaderSize+len(binding)), w.header[:]...), binding...)
	}
	ct := w.aead.Seal(w.buf[:0], w.nonce[:], w.buf[:w.n], aad)
	if _, err := w.w.Write(ct); err != nil {
		w.err = err
		return err
	}
	w.counter++
	w.n = 0
	return nil
}

// errFinished is the sticky error after Finish.
var errFinished = errors.New("encrypted stream already finished")

// Finish writes the final chunk, bound to binding: the plaintext's SHA-256
// for a message file, nil for a backup. It does not close the underlying
// writer. The Writer cannot be used afterwards.
func (w *Writer) Finish(binding []byte) error {
	if w.err != nil {
		return w.err
	}
	if err := checkBinding(w.purpose, binding); err != nil {
		return err
	}
	if err := w.flush(true, binding); err != nil {
		return err
	}
	w.err = errFinished
	return nil
}

// Reader decrypts an encrypted file with random access. Open authenticates
// the final chunk, so the size and the binding are proven before the first
// read; every other chunk is authenticated when it is read. It holds one
// chunk buffer and is safe for concurrent use.
type Reader struct {
	ra       io.ReaderAt
	header   Header
	raw      [HeaderSize]byte
	finalAAD []byte // header ‖ binding
	aead     cipher.AEAD
	chunks   int64 // number of chunks, at least 1
	lastEnc  int64 // ciphertext length of the final chunk, with its tag
	size     int64 // plaintext length

	mu     sync.Mutex
	buf    []byte
	cached int64 // chunk in buf, or -1
	plain  int   // plaintext bytes of the cached chunk
	off    int64 // position of Read and Seek
	nonce  [nonceSize]byte
}

// Open checks the file in ra (fileSize bytes long) and returns a Reader
// for its plaintext. want is the expected purpose, or 0 for any; binding
// is the plaintext SHA-256 the file must be bound to for a message, nil
// for a backup. Refusals wrap ErrNotEncrypted, ErrCorrupt or ErrUnknownKey.
func Open(ra io.ReaderAt, fileSize int64, keys DataKeys, want Purpose, binding []byte) (*Reader, error) {
	// No stat returns these; refuse them before any arithmetic on the size.
	if fileSize < 0 || fileSize > maxFileSize {
		return nil, corrupt("impossible file size %d", fileSize)
	}
	var raw [HeaderSize]byte
	n, err := ra.ReadAt(raw[:min(fileSize, HeaderSize)], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read header: %w", err)
	}
	h, err := ParseHeader(raw[:n])
	if err != nil {
		return nil, err
	}
	if want != 0 && h.Purpose != want {
		return nil, corrupt("a %s file, not a %s file", h.Purpose, want)
	}
	if err := checkBinding(h.Purpose, binding); err != nil {
		return nil, err
	}
	enc := fileSize - HeaderSize
	if enc < TagSize {
		return nil, corrupt("truncated: no final chunk")
	}
	// Rounded up without enc + encChunkSize - 1, which overflows near
	// maxFileSize.
	chunks := enc / encChunkSize
	if enc%encChunkSize != 0 {
		chunks++
	}
	lastEnc := enc - (chunks-1)*encChunkSize
	// Writers never produce an empty final chunk after a full one.
	if chunks > 1 && lastEnc <= TagSize {
		return nil, corrupt("truncated or extended: final chunk of %d bytes", lastEnc)
	}
	if chunks > maxChunks {
		return nil, corrupt("too many chunks")
	}
	dk, ok := keys.DataKey(h.KeyID)
	if !ok {
		return nil, fmt.Errorf("%w (data key %08x)", ErrUnknownKey, h.KeyID)
	}
	aead, err := fileAEAD(dk, &h)
	if err != nil {
		return nil, err
	}
	r := &Reader{
		ra: ra, header: h, raw: raw, finalAAD: append(raw[:], binding...), aead: aead,
		chunks: chunks, lastEnc: lastEnc,
		size: (chunks-1)*ChunkSize + lastEnc - TagSize,
		buf:  make([]byte, encChunkSize), cached: -1,
	}
	if err := r.load(chunks - 1); err != nil {
		return nil, err
	}
	return r, nil
}

// Header returns the file's header.
func (r *Reader) Header() Header { return r.header }

// Size is the plaintext length.
func (r *Reader) Size() int64 { return r.size }

// load decrypts chunk i into buf. Called with mu held (or before r is shared).
func (r *Reader) load(i int64) error {
	if r.cached == i {
		return nil
	}
	r.cached = -1
	last := i == r.chunks-1
	encLen := int64(encChunkSize)
	if last {
		encLen = r.lastEnc
	}
	ct := r.buf[:encLen]
	if n, err := r.ra.ReadAt(ct, HeaderSize+i*encChunkSize); n < len(ct) {
		if err == nil || errors.Is(err, io.EOF) {
			return corrupt("chunk %d: truncated", i)
		}
		return fmt.Errorf("read chunk %d: %w", i, err)
	}
	aad := r.raw[:]
	if last {
		aad = r.finalAAD
	}
	chunkNonce(&r.nonce, uint64(i), last) //nolint:gosec // i is in [0, maxChunks)
	pt, err := r.aead.Open(ct[:0], r.nonce[:], ct, aad)
	if err != nil {
		if last {
			return corrupt("final chunk fails authentication (modified, truncated, extended, bound to another name, or another key)")
		}
		return corrupt("chunk %d fails authentication (modified, reordered or another key)", i)
	}
	r.cached, r.plain = i, len(pt)
	return nil
}

// ReadAt implements io.ReaderAt over the plaintext.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readAt(p, off)
}

func (r *Reader) readAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("blobcrypt: negative offset")
	}
	n := 0
	for n < len(p) {
		if off >= r.size {
			return n, io.EOF
		}
		i := off / ChunkSize
		if err := r.load(i); err != nil {
			return n, err
		}
		start := off - i*ChunkSize
		if start >= int64(r.plain) {
			// Cannot happen for a size derived as in Open; never loop.
			return n, corrupt("chunk %d shorter than its position", i)
		}
		c := copy(p[n:], r.buf[start:r.plain])
		n += c
		off += int64(c)
	}
	return n, nil
}

// Read implements io.Reader from the position Seek set.
func (r *Reader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	n, err := r.readAt(p, r.off)
	r.off += int64(n)
	if n > 0 && errors.Is(err, io.EOF) {
		err = nil
	}
	return n, err
}

// Seek implements io.Seeker over the plaintext.
func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.off + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, errors.New("blobcrypt: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("blobcrypt: negative position")
	}
	r.off = abs
	return abs, nil
}

// EncryptedSize is the file size of a plaintext of n bytes.
func EncryptedSize(n int64) int64 {
	chunks := max(1, (n+ChunkSize-1)/ChunkSize)
	return HeaderSize + n + chunks*TagSize
}
