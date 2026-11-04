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
	"math"
	mrand "math/rand/v2"
	"sync"
	"testing"
)

// testKeys is a DataKeys of fixed keys.
type testKeys map[uint32][]byte

func (k testKeys) DataKey(id uint32) ([]byte, bool) { d, ok := k[id]; return d, ok }

func randomKey(t testing.TB) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

// pattern is deterministic plaintext of n bytes.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func encrypt(t testing.TB, keyID uint32, dk []byte, p Purpose, plain []byte, binding []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, keyID, dk, p)
	if err != nil {
		t.Fatal(err)
	}
	// Uneven writes cross chunk boundaries in every way.
	for rest := plain; len(rest) > 0; {
		n := min(len(rest), 1+len(rest)%7000)
		if _, err := w.Write(rest[:n]); err != nil {
			t.Fatal(err)
		}
		rest = rest[n:]
	}
	if err := w.Finish(binding); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

func openBytes(data []byte, keys DataKeys, want Purpose, binding []byte) (*Reader, error) {
	return Open(bytes.NewReader(data), int64(len(data)), keys, want, binding)
}

func TestRoundTrip(t *testing.T) {
	dk := randomKey(t)
	keys := testKeys{7: dk}
	for _, n := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2 * ChunkSize, 10 << 20} {
		plain := pattern(n)
		for _, p := range []Purpose{PurposeMessage, PurposeBackup} {
			var binding []byte
			if p == PurposeMessage {
				binding = sum(plain)
			}
			ct := encrypt(t, 7, dk, p, plain, binding)
			if int64(len(ct)) != EncryptedSize(int64(n)) {
				t.Errorf("%d bytes, %s: file size %d, want %d", n, p, len(ct), EncryptedSize(int64(n)))
			}
			r, err := openBytes(ct, keys, p, binding)
			if err != nil {
				t.Fatalf("%d bytes, %s: open: %v", n, p, err)
			}
			if r.Size() != int64(n) {
				t.Errorf("%d bytes: Size %d", n, r.Size())
			}
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("%d bytes, %s: read %d bytes, %v", n, p, len(got), err)
			}
		}
	}
}

func TestReadAtAndSeek(t *testing.T) {
	dk := randomKey(t)
	plain := pattern(3*ChunkSize + 12345)
	ct := encrypt(t, 1, dk, PurposeMessage, plain, sum(plain))
	r, err := openBytes(ct, testKeys{1: dk}, PurposeMessage, sum(plain))
	if err != nil {
		t.Fatal(err)
	}
	rng := mrand.New(mrand.NewPCG(1, 2))
	for range 500 {
		off := rng.Int64N(int64(len(plain)) + 10)
		n := rng.IntN(2*ChunkSize + 3)
		buf := make([]byte, n)
		got, err := r.ReadAt(buf, off)
		wantN := max(0, min(int64(n), int64(len(plain))-off))
		if int64(got) != wantN || !bytes.Equal(buf[:got], plain[min(off, int64(len(plain))):off+wantN]) {
			t.Fatalf("ReadAt(%d, %d) = %d, %v; want %d bytes", n, off, got, err, wantN)
		}
		if int64(got) < int64(n) && !errors.Is(err, io.EOF) {
			t.Fatalf("short ReadAt without EOF: %v", err)
		}
		if int64(got) == int64(n) && err != nil {
			t.Fatalf("full ReadAt with error: %v", err)
		}

		pos, err := r.Seek(off, io.SeekStart)
		if err != nil || pos != off {
			t.Fatalf("Seek(%d) = %d, %v", off, pos, err)
		}
		m, _ := io.ReadFull(r, buf)
		if int64(m) != wantN || !bytes.Equal(buf[:m], plain[min(off, int64(len(plain))):off+wantN]) {
			t.Fatalf("Read after Seek(%d): %d bytes", off, m)
		}
	}
	if pos, err := r.Seek(-10, io.SeekEnd); err != nil || pos != int64(len(plain))-10 {
		t.Errorf("SeekEnd: %d, %v", pos, err)
	}
	if pos, err := r.Seek(5, io.SeekCurrent); err != nil || pos != int64(len(plain))-5 {
		t.Errorf("SeekCurrent: %d, %v", pos, err)
	}
	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Error("negative Seek accepted")
	}
	if _, err := r.ReadAt(make([]byte, 1), -1); err == nil {
		t.Error("negative ReadAt accepted")
	}
}

// encChunk returns chunk i of a file.
func encChunk(ct []byte, i int) []byte {
	start := HeaderSize + i*encChunkSize
	return ct[start:min(len(ct), start+encChunkSize)]
}

func TestRefusals(t *testing.T) {
	dk := randomKey(t)
	keys := testKeys{9: dk}
	plain := pattern(3*ChunkSize + 100) // four chunks
	binding := sum(plain)
	ct := encrypt(t, 9, dk, PurposeMessage, plain, binding)
	small := encrypt(t, 9, dk, PurposeMessage, nil, sum(nil))

	clone := func(b []byte) []byte { return bytes.Clone(b) }
	flip := func(b []byte, i int) []byte { c := clone(b); c[i] ^= 0x01; return c }
	swap01 := func() []byte {
		c := clone(ct)
		a, b := clone(encChunk(ct, 0)), clone(encChunk(ct, 1))
		copy(c[HeaderSize:], b)
		copy(c[HeaderSize+encChunkSize:], a)
		return c
	}
	dropChunk := func(i int) []byte {
		c := clone(ct[:HeaderSize+i*encChunkSize])
		return append(c, ct[HeaderSize+(i+1)*encChunkSize:]...)
	}
	otherBinding := sum([]byte("another message"))

	cases := []struct {
		name    string
		data    []byte
		keys    DataKeys
		want    Purpose
		binding []byte
		err     error
	}{
		{"truncated by one byte", ct[:len(ct)-1], keys, PurposeMessage, binding, ErrCorrupt},
		{"truncated at a chunk boundary", ct[:HeaderSize+3*encChunkSize], keys, PurposeMessage, binding, ErrCorrupt},
		{"truncated to the header", ct[:HeaderSize], keys, PurposeMessage, binding, ErrCorrupt},
		{"truncated header", ct[:HeaderSize-1], keys, PurposeMessage, binding, ErrCorrupt},
		{"empty", nil, keys, PurposeMessage, binding, ErrCorrupt},
		{"chunk dropped", dropChunk(1), keys, PurposeMessage, binding, ErrCorrupt},
		{"one trailing byte", append(clone(ct), 0), keys, PurposeMessage, binding, ErrCorrupt},
		{"trailing tag-sized data", append(clone(ct), make([]byte, TagSize)...), keys, PurposeMessage, binding, ErrCorrupt},
		{"trailing chunk", append(clone(ct), encChunk(ct, 1)...), keys, PurposeMessage, binding, ErrCorrupt},
		{"final chunk modified", flip(ct, len(ct)-20), keys, PurposeMessage, binding, ErrCorrupt},
		{"salt bit flipped", flip(ct, offSalt+3), keys, PurposeMessage, binding, ErrCorrupt},
		{"magic bit flipped", flip(ct, 2), keys, PurposeMessage, binding, ErrNotEncrypted},
		{"version bit flipped", flip(ct, offVersion), keys, PurposeMessage, binding, ErrCorrupt},
		{"purpose bit flipped", flip(ct, offPurpose), keys, 0, nil, ErrCorrupt},
		{"exponent bit flipped", flip(ct, offExponent), keys, PurposeMessage, binding, ErrCorrupt},
		{"reserved bit flipped", flip(ct, offReserved), keys, PurposeMessage, binding, ErrCorrupt},
		{"padding bit flipped", flip(ct, HeaderSize-1), keys, PurposeMessage, binding, ErrCorrupt},
		{"key id bit flipped", flip(ct, offKeyID+3), keys, PurposeMessage, binding, ErrUnknownKey},
		{"wrong data key", ct, testKeys{9: randomKey(t)}, PurposeMessage, binding, ErrCorrupt},
		{"unknown data key", ct, testKeys{10: dk}, PurposeMessage, binding, ErrUnknownKey},
		{"bound to another name", ct, keys, PurposeMessage, otherBinding, ErrCorrupt},
		{"empty file bound to another name", small, keys, PurposeMessage, otherBinding, ErrCorrupt},
		{"wrong purpose", ct, keys, PurposeBackup, nil, ErrCorrupt},
		{"plaintext", plain, keys, PurposeMessage, binding, ErrNotEncrypted},
	}
	// All of these are refused by Open itself, before any plaintext.
	for _, c := range cases {
		if _, err := openBytes(c.data, c.keys, c.want, c.binding); !errors.Is(err, c.err) {
			t.Errorf("%s: Open = %v, want %v", c.name, err, c.err)
		}
	}

	// Reordered and modified inner chunks open (the final chunk is intact)
	// but fail when read.
	for name, data := range map[string][]byte{"reordered": swap01(), "inner chunk modified": flip(ct, HeaderSize+encChunkSize+5)} {
		r, err := openBytes(data, keys, PurposeMessage, binding)
		if err != nil {
			t.Fatalf("%s: open: %v", name, err)
		}
		if _, err := io.ReadAll(r); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: read: %v, want ErrCorrupt", name, err)
		}
	}
	// The final chunk of one file does not end another of the same key.
	other := encrypt(t, 9, dk, PurposeMessage, plain, binding)
	mixed := append(clone(ct[:len(ct)-len(encChunk(ct, 3))]), encChunk(other, 3)...)
	if _, err := openBytes(mixed, keys, PurposeMessage, binding); !errors.Is(err, ErrCorrupt) {
		t.Errorf("final chunk of another file: %v", err)
	}
}

// TestParseHeader: the strict checks hold on their own, without relying on
// authentication.
func TestParseHeader(t *testing.T) {
	h := Header{Purpose: PurposeMessage, KeyID: 42}
	good := h.marshal()
	if got, err := ParseHeader(good[:]); err != nil || got != h {
		t.Fatalf("ParseHeader(good) = %+v, %v", got, err)
	}
	set := func(off int, v byte) []byte { b := good; b[off] = v; return b[:] }
	for name, b := range map[string][]byte{
		"version 0":     set(offVersion, 0),
		"version 2":     set(offVersion, 2),
		"purpose 0":     set(offPurpose, 0),
		"purpose 3":     set(offPurpose, 3),
		"exponent 15":   set(offExponent, 15),
		"exponent 17":   set(offExponent, 17),
		"reserved":      set(offReserved, 1),
		"padding":       set(offPadding+7, 1),
		"last padding":  set(HeaderSize-1, 1),
		"key id 0":      append(append([]byte(nil), good[:offKeyID]...), append([]byte{0, 0, 0, 0}, good[offSalt:]...)...),
		"short":         good[:HeaderSize-1],
		"magic only":    good[:len(Magic)],
		"part of magic": good[:3],
		"empty":         nil,
	} {
		if _, err := ParseHeader(b); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %v, want ErrCorrupt", name, err)
		}
	}
	if _, err := ParseHeader([]byte("From: someone\r\n")); !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("plaintext: %v, want ErrNotEncrypted", err)
	}
}

func TestWriterRefusals(t *testing.T) {
	dk := randomKey(t)
	var buf bytes.Buffer
	if _, err := NewWriter(&buf, 0, dk, PurposeMessage); err == nil {
		t.Error("key id 0 accepted")
	}
	if _, err := NewWriter(&buf, 1, dk[:16], PurposeMessage); err == nil {
		t.Error("short key accepted")
	}
	if _, err := NewWriter(&buf, 1, dk, 3); err == nil {
		t.Error("unknown purpose accepted")
	}
	w, _ := NewWriter(&buf, 1, dk, PurposeMessage)
	if err := w.Finish(nil); err == nil {
		t.Error("message without binding accepted")
	}
	if err := w.Finish(sum(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Error("Write after Finish accepted")
	}
	b, _ := NewWriter(&bytes.Buffer{}, 1, dk, PurposeBackup)
	if err := b.Finish(sum(nil)); err == nil {
		t.Error("backup with binding accepted")
	}
}

// TestSpecByHand builds a two-chunk file from the primitives as
// docs/development/architecture.md specifies them and compares it with the
// Writer's output, so code and spec cannot drift apart.
func TestSpecByHand(t *testing.T) {
	dk := randomKey(t)
	var salt [SaltSize]byte
	copy(salt[:], pattern(SaltSize)[1:])
	plain := pattern(ChunkSize + 5)
	binding := sum(plain)

	var hdr [HeaderSize]byte
	copy(hdr[:], "MA-ENC1\n")
	hdr[8], hdr[9], hdr[10] = 1, 1, 16
	binary.BigEndian.PutUint32(hdr[12:], 0x01020304)
	copy(hdr[16:48], salt[:])

	key, err := hkdf.Key(sha256.New, dk, salt[:], "mail-archive blob v1\x01", 32)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	want := append([]byte(nil), hdr[:]...)
	n0 := make([]byte, 12) // counter 0, not last
	want = gcm.Seal(want, n0, plain[:ChunkSize], hdr[:])
	n1 := make([]byte, 12)
	n1[10], n1[11] = 1, 1 // counter 1, last
	want = gcm.Seal(want, n1, plain[ChunkSize:], append(hdr[:], binding...))

	var got bytes.Buffer
	w, err := newWriterSalt(&got, 0x01020304, dk, PurposeMessage, salt)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(plain)
	if err := w.Finish(binding); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatal("Writer output differs from the specified construction")
	}
}

// TestReaderSingleBuffer: a Reader holds one chunk buffer; reading a file
// of 64 chunks allocates no more than reading one of a single chunk.
func TestReaderSingleBuffer(t *testing.T) {
	dk := randomKey(t)
	allocs := func(n int) float64 {
		ct := encrypt(t, 1, dk, PurposeBackup, pattern(n), nil)
		buf := make([]byte, 32<<10)
		return testing.AllocsPerRun(5, func() {
			r, err := openBytes(ct, testKeys{1: dk}, PurposeBackup, nil)
			if err != nil {
				t.Fatal(err)
			}
			for {
				if _, err := r.Read(buf); err != nil {
					break
				}
			}
		})
	}
	one, many := allocs(100), allocs(64*ChunkSize)
	if many > one+2 {
		t.Errorf("%v allocations for 64 chunks, %v for one; the reader should reuse its buffer", many, one)
	}
}

// TestReaderConcurrent: several goroutines share one Reader, as
// http.ServeContent and part readers will: ReadAt at mixed offsets from
// four of them, Seek and Read from a fifth, all against the plaintext.
// Run under -race.
func TestReaderConcurrent(t *testing.T) {
	dk := randomKey(t)
	plain := pattern(6*ChunkSize + 777)
	ct := encrypt(t, 1, dk, PurposeMessage, plain, sum(plain))
	r, err := openBytes(ct, testKeys{1: dk}, PurposeMessage, sum(plain))
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 5)
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := mrand.New(mrand.NewPCG(uint64(g), 7))
			buf := make([]byte, ChunkSize+100)
			for range 300 {
				off := rng.Int64N(int64(len(plain)))
				n := 1 + rng.IntN(len(buf)-1)
				got, err := r.ReadAt(buf[:n], off)
				want := plain[off:min(off+int64(n), int64(len(plain)))]
				if got != len(want) || !bytes.Equal(buf[:got], want) || (got == n && err != nil) {
					errs <- fmt.Errorf("goroutine %d: ReadAt(%d, %d) = %d, %w", g, n, off, got, err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := mrand.New(mrand.NewPCG(99, 7))
		buf := make([]byte, 5000)
		for range 300 {
			off := rng.Int64N(int64(len(plain)))
			if _, err := r.Seek(off, io.SeekStart); err != nil {
				errs <- err
				return
			}
			got, err := r.Read(buf)
			want := plain[off:min(off+int64(len(buf)), int64(len(plain)))]
			if err != nil || got != len(want) || !bytes.Equal(buf[:got], want) {
				errs <- fmt.Errorf("Read at %d = %d, %w", off, got, err)
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestEmptyFinalChunkRefused: a file that ends a full chunk with an
// authentic empty final chunk is a second encoding of the same plaintext;
// writers never produce it, and Open refuses it although every chunk
// authenticates.
func TestEmptyFinalChunkRefused(t *testing.T) {
	dk := randomKey(t)
	plain := pattern(ChunkSize)
	binding := sum(plain)
	h := Header{Purpose: PurposeMessage, KeyID: 3}
	copy(h.Salt[:], pattern(SaltSize))
	hdr := h.marshal()
	aead, err := fileAEAD(dk, &h)
	if err != nil {
		t.Fatal(err)
	}
	var n [nonceSize]byte
	file := append([]byte(nil), hdr[:]...)
	chunkNonce(&n, 0, false)
	file = aead.Seal(file, n[:], plain, hdr[:])
	chunkNonce(&n, 1, true)
	file = aead.Seal(file, n[:], nil, append(hdr[:], binding...))
	if _, err := openBytes(file, testKeys{3: dk}, PurposeMessage, binding); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("empty final chunk after a full one: %v, want ErrCorrupt", err)
	}
}

// TestOpenImpossibleSize: a negative or overflowing size is refused, not a
// panic.
func TestOpenImpossibleSize(t *testing.T) {
	dk := randomKey(t)
	ct := encrypt(t, 1, dk, PurposeBackup, pattern(10), nil)
	for _, size := range []int64{-1, math.MinInt64, math.MaxInt64, math.MaxInt64 - encChunkSize, maxFileSize + 1} {
		if _, err := Open(bytes.NewReader(ct), size, testKeys{1: dk}, PurposeBackup, nil); !errors.Is(err, ErrCorrupt) {
			t.Errorf("size %d: %v, want ErrCorrupt", size, err)
		}
	}
	// The largest accepted sizes and those just below them: a valid header,
	// then zeros, without allocating the file. Open must get as far as the
	// final chunk and refuse it, never overflow.
	f := headerThenZeros{header: ct[:HeaderSize]}
	for _, size := range []int64{maxFileSize, maxFileSize - 1, maxFileSize - encChunkSize/2, maxFileSize - encChunkSize, maxFileSize - encChunkSize - TagSize - 1} {
		if _, err := Open(f, size, testKeys{1: dk}, PurposeBackup, nil); !errors.Is(err, ErrCorrupt) {
			t.Errorf("size %d: %v, want ErrCorrupt", size, err)
		}
	}
}

// headerThenZeros is a ReaderAt of any length: a header, then zero bytes.
type headerThenZeros struct{ header []byte }

func (h headerThenZeros) ReadAt(p []byte, off int64) (int, error) {
	clear(p)
	if off < int64(len(h.header)) {
		copy(p, h.header[off:])
	}
	return len(p), nil
}
