package blobcrypt

import (
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
)

// StreamReader decrypts an encrypted file read once from start to end,
// such as a backup piped into pg_restore (#392, part 4). Unlike Open it
// needs neither an io.ReaderAt nor the size: it holds one chunk and reads
// one byte ahead to tell the final chunk from the others.
//
// Every chunk is authenticated before its plaintext is returned. A stream
// that is cut, extended, reordered, modified, or bound to something else
// fails at the first chunk that differs, and at the latest at its end:
// Read then returns an error wrapping ErrCorrupt instead of io.EOF, so a
// consumer sees only authenticated bytes and never a clean end of a
// damaged stream.
type StreamReader struct {
	r        io.Reader
	header   Header
	raw      [HeaderSize]byte
	finalAAD []byte // header ‖ binding
	aead     cipher.AEAD
	buf      []byte  // one encrypted chunk, decrypted in place
	pt       []byte  // plaintext of the current chunk not yet returned
	ahead    [1]byte // the byte read after the current chunk
	hasAhead bool
	counter  uint64
	nonce    [nonceSize]byte
	done     bool  // the final chunk was authenticated
	err      error // sticky
}

// NewStreamReader reads and checks the header from r and returns a
// StreamReader for the plaintext that follows. want and binding are as
// for Open. Refusals wrap ErrNotEncrypted, ErrCorrupt or ErrUnknownKey.
func NewStreamReader(r io.Reader, keys DataKeys, want Purpose, binding []byte) (*StreamReader, error) {
	s := &StreamReader{r: r}
	n, err := io.ReadFull(r, s.raw[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read header: %w", err)
	}
	h, err := ParseHeader(s.raw[:n])
	if err != nil {
		return nil, err
	}
	if want != 0 && h.Purpose != want {
		return nil, corrupt("a %s file, not a %s file", h.Purpose, want)
	}
	if err := checkBinding(h.Purpose, binding); err != nil {
		return nil, err
	}
	dk, ok := keys.DataKey(h.KeyID)
	if !ok {
		return nil, fmt.Errorf("%w (data key %08x)", ErrUnknownKey, h.KeyID)
	}
	if s.aead, err = fileAEAD(dk, &h); err != nil {
		return nil, err
	}
	s.header = h
	s.finalAAD = append(s.raw[:], binding...)
	s.buf = make([]byte, encChunkSize)
	return s, nil
}

// Header returns the stream's header.
func (s *StreamReader) Header() Header { return s.header }

// Read implements io.Reader. It returns io.EOF only after the final chunk
// was authenticated and its plaintext returned.
func (s *StreamReader) Read(p []byte) (int, error) {
	for len(s.pt) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		if s.done {
			return 0, io.EOF
		}
		if err := s.next(); err != nil {
			s.err = err
			return 0, err
		}
	}
	n := copy(p, s.pt)
	s.pt = s.pt[n:]
	return n, nil
}

// next reads and authenticates the next chunk into buf. A chunk is the
// final one when the stream ends right after it.
func (s *StreamReader) next() error {
	have := 0
	if s.hasAhead {
		s.buf[0] = s.ahead[0]
		have, s.hasAhead = 1, false
	}
	n, err := io.ReadFull(s.r, s.buf[have:])
	have += n
	last := false
	switch {
	case err == nil:
		// A full chunk; one more byte tells whether another follows.
		m, aerr := io.ReadFull(s.r, s.ahead[:])
		switch {
		case m == 1:
			s.hasAhead = true
		case errors.Is(aerr, io.EOF):
			last = true
		default:
			return fmt.Errorf("read chunk %d: %w", s.counter, aerr)
		}
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		last = true
	default:
		return fmt.Errorf("read chunk %d: %w", s.counter, err)
	}
	if s.counter >= maxChunks {
		return corrupt("too many chunks")
	}
	switch {
	case have < TagSize:
		return corrupt("truncated: chunk %d is %d bytes", s.counter, have)
	case last && s.counter > 0 && have == TagSize:
		// Writers never produce an empty final chunk after a full one.
		return corrupt("truncated or extended: empty final chunk")
	}
	ct := s.buf[:have]
	aad := s.raw[:]
	if last {
		aad = s.finalAAD
	}
	chunkNonce(&s.nonce, s.counter, last)
	pt, err := s.aead.Open(ct[:0], s.nonce[:], ct, aad)
	if err != nil {
		if last {
			return corrupt("final chunk %d fails authentication (modified, truncated, extended, or another key)", s.counter)
		}
		return corrupt("chunk %d fails authentication (modified, reordered, truncated, or another key)", s.counter)
	}
	s.counter++
	s.pt, s.done = pt, last
	return nil
}
