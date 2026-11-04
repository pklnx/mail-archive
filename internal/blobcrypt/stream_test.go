package blobcrypt

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func streamAll(data []byte, keys DataKeys, want Purpose, binding []byte) ([]byte, error) {
	s, err := NewStreamReader(bytes.NewReader(data), keys, want, binding)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(s)
}

// TestStreamRoundTrip: the StreamReader returns what the Writer wrote, at
// every size around the chunk boundaries, also through a reader that
// returns one byte at a time.
func TestStreamRoundTrip(t *testing.T) {
	dk := randomKey(t)
	keys := testKeys{7: dk}
	for _, n := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2 * ChunkSize, 3*ChunkSize + 7} {
		plain := pattern(n)
		for _, p := range []Purpose{PurposeBackup, PurposeMessage} {
			var binding []byte
			if p == PurposeMessage {
				binding = sum(plain)
			}
			ct := encrypt(t, 7, dk, p, plain, binding)
			got, err := streamAll(ct, keys, p, binding)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("%s of %d bytes: %v, %d bytes back", p, n, err, len(got))
			}
			s, err := NewStreamReader(iotest.OneByteReader(bytes.NewReader(ct)), keys, 0, binding)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := io.ReadAll(s); err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("%s of %d bytes, one byte at a time: %v", p, n, err)
			}
			if s.Header().KeyID != 7 || s.Header().Purpose != p {
				t.Errorf("header %+v", s.Header())
			}
		}
	}
}

// TestStreamKnownAnswers: the StreamReader reads the frozen vectors too.
func TestStreamKnownAnswers(t *testing.T) {
	for i, s := range vectorSizes {
		v, ct := makeVector(t, i, s.p, s.n)
		binding, _ := hex.DecodeString(v.Binding)
		if len(binding) == 0 {
			binding = nil
		}
		got, err := streamAll(ct, testKeys{vectorKeyID: vectorDataKey()}, s.p, binding)
		if err != nil || !bytes.Equal(got, pattern(s.n)) {
			t.Errorf("vector %d: %v", i, err)
		}
	}
}

// TestStreamRefusals: every damage Open refuses makes the stream fail, and
// what it returned before failing is a prefix of the plaintext: only
// authenticated bytes. A cut stream never ends with io.EOF.
func TestStreamRefusals(t *testing.T) {
	dk := randomKey(t)
	keys := testKeys{9: dk}
	plain := pattern(3*ChunkSize + 100) // four chunks
	binding := sum(plain)
	ct := encrypt(t, 9, dk, PurposeMessage, plain, binding)
	backupPlain := pattern(2 * ChunkSize) // two full chunks, the second final
	backup := encrypt(t, 9, dk, PurposeBackup, backupPlain, nil)

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
	other := encrypt(t, 9, dk, PurposeMessage, plain, binding)

	cases := []struct {
		name    string
		data    []byte
		keys    DataKeys
		want    Purpose
		binding []byte
		plain   []byte
		err     error
	}{
		{"truncated by one byte", ct[:len(ct)-1], keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"truncated at a chunk boundary", ct[:HeaderSize+3*encChunkSize], keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"truncated at the first chunk boundary", ct[:HeaderSize+encChunkSize], keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"truncated to the header", ct[:HeaderSize], keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"truncated header", ct[:HeaderSize-1], keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"empty", nil, keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"chunk dropped", dropChunk(1), keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"reordered", swap01(), keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"inner chunk modified", flip(ct, HeaderSize+encChunkSize+5), keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"one trailing byte", append(clone(ct), 0), keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"trailing tag-sized data", append(clone(ct), make([]byte, TagSize)...), keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"trailing chunk", append(clone(ct), encChunk(ct, 1)...), keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"final chunk modified", flip(ct, len(ct)-20), keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"final chunk of another file", append(clone(ct[:len(ct)-len(encChunk(ct, 3))]), encChunk(other, 3)...), keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"salt bit flipped", flip(ct, offSalt+3), keys, PurposeMessage, binding, plain, ErrCorrupt},
		{"magic bit flipped", flip(ct, 2), keys, PurposeMessage, binding, plain, ErrNotEncrypted},
		{"purpose bit flipped", flip(ct, offPurpose), keys, 0, nil, plain, ErrCorrupt},
		{"key id bit flipped", flip(ct, offKeyID+3), keys, PurposeMessage, binding, plain, ErrUnknownKey},
		{"wrong data key", ct, testKeys{9: randomKey(t)}, PurposeMessage, binding, plain, ErrCorrupt},
		{"unknown data key", ct, testKeys{10: dk}, PurposeMessage, binding, plain, ErrUnknownKey},
		{"bound to another name", ct, keys, PurposeMessage, sum([]byte("another")), plain, ErrCorrupt},
		{"wrong purpose", ct, keys, PurposeBackup, nil, plain, ErrCorrupt},
		{"plaintext", plain, keys, PurposeMessage, binding, plain, ErrNotEncrypted},
		{"backup cut after its first full chunk", backup[:HeaderSize+encChunkSize], keys, PurposeBackup, nil, backupPlain, ErrCorrupt},
		{"backup with an empty final chunk appended", append(clone(backup), encChunk(backup, 1)[:TagSize]...), keys, PurposeBackup, nil, backupPlain, ErrCorrupt},
	}
	for _, c := range cases {
		s, err := NewStreamReader(bytes.NewReader(c.data), c.keys, c.want, c.binding)
		var got []byte
		if err == nil {
			got, err = io.ReadAll(s)
			// Sticky: a second read fails the same way.
			if _, again := s.Read(make([]byte, 1)); !errors.Is(again, c.err) {
				t.Errorf("%s: read after the failure: %v", c.name, again)
			}
		}
		if !errors.Is(err, c.err) {
			t.Errorf("%s: %v, want %v", c.name, err, c.err)
		}
		if !bytes.HasPrefix(c.plain, got) {
			t.Errorf("%s: returned %d bytes that are not a prefix of the plaintext", c.name, len(got))
		}
	}
}

// TestStreamReadError: an error of the underlying reader is returned, not
// taken for the end of the stream.
func TestStreamReadError(t *testing.T) {
	dk := randomKey(t)
	ct := encrypt(t, 9, dk, PurposeBackup, pattern(2*ChunkSize+5), nil)
	boom := errors.New("disk on fire")
	for _, at := range []int{HeaderSize + 10, HeaderSize + encChunkSize, HeaderSize + encChunkSize + 1} {
		r := io.MultiReader(bytes.NewReader(ct[:at]), iotest.ErrReader(boom))
		s, err := NewStreamReader(r, testKeys{9: dk}, PurposeBackup, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(s); !errors.Is(err, boom) {
			t.Errorf("error after %d bytes: %v", at, err)
		}
	}
	if _, err := NewStreamReader(iotest.ErrReader(boom), testKeys{9: dk}, PurposeBackup, nil); !errors.Is(err, boom) {
		t.Errorf("error in the header: %v", err)
	}
}

// TestStreamSingleBuffer: the reader holds one chunk, whatever the size:
// forty chunks take no more allocations than two.
func TestStreamSingleBuffer(t *testing.T) {
	dk := randomKey(t)
	allocs := func(chunks int) float64 {
		ct := encrypt(t, 9, dk, PurposeBackup, pattern(chunks*ChunkSize), nil)
		return testing.AllocsPerRun(5, func() {
			s, err := NewStreamReader(bytes.NewReader(ct), testKeys{9: dk}, PurposeBackup, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, s); err != nil {
				t.Fatal(err)
			}
		})
	}
	if few, many := allocs(2), allocs(40); many > few {
		t.Errorf("%.0f allocations for 2 chunks, %.0f for 40", few, many)
	}
}
