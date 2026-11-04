package blobcrypt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the known-answer vectors in testdata")

// The vectors' keys are derived from public labels: they are test inputs,
// not secrets, and no key appears in testdata.
func vectorDataKey() []byte {
	s := sha256.Sum256([]byte("mail-archive test vector data key 1"))
	return s[:]
}

func vectorSalt(i int) (s [SaltSize]byte) {
	h := sha256.Sum256(fmt.Appendf(nil, "mail-archive test vector salt %d", i))
	copy(s[:], h[:])
	return s
}

const vectorKeyID = 0x5eed0001

type vector struct {
	Purpose   Purpose `json:"purpose"`
	Plaintext int     `json:"plaintext_len"` // bytes of pattern()
	Binding   string  `json:"binding_hex"`
	// Ciphertext is the whole file for small vectors; CiphertextSHA its
	// SHA-256 for all.
	Ciphertext    string `json:"ciphertext_hex,omitempty"`
	CiphertextSHA string `json:"ciphertext_sha256"`
}

var vectorSizes = []struct {
	p Purpose
	n int
}{
	{PurposeMessage, 0}, {PurposeMessage, 1}, {PurposeMessage, 100},
	{PurposeMessage, ChunkSize - 1}, {PurposeMessage, ChunkSize}, {PurposeMessage, ChunkSize + 1},
	{PurposeBackup, 0}, {PurposeBackup, 3*ChunkSize + 7},
}

func makeVector(t testing.TB, i int, p Purpose, n int) (vector, []byte) {
	plain := pattern(n)
	var binding []byte
	if p == PurposeMessage {
		binding = sum(plain)
	}
	var buf bytes.Buffer
	w, err := newWriterSalt(&buf, vectorKeyID, vectorDataKey(), p, vectorSalt(i))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(plain)
	if err := w.Finish(binding); err != nil {
		t.Fatal(err)
	}
	v := vector{Purpose: p, Plaintext: n, Binding: hex.EncodeToString(binding), CiphertextSHA: hex.EncodeToString(sum(buf.Bytes()))}
	if buf.Len() <= 256 {
		v.Ciphertext = hex.EncodeToString(buf.Bytes())
	}
	return v, buf.Bytes()
}

// TestKnownAnswers freezes the file format: the Writer must reproduce every
// vector byte for byte, and the Reader must open the stored ciphertexts.
func TestKnownAnswers(t *testing.T) {
	path := filepath.Join("testdata", "vectors.json")
	var got []vector
	for i, s := range vectorSizes {
		v, _ := makeVector(t, i, s.p, s.n)
		got = append(got, v)
	}
	if *update {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil { //nolint:gosec // test data
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []vector
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("%d vectors in testdata, %d generated", len(want), len(got))
	}
	keys := testKeys{vectorKeyID: vectorDataKey()}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("vector %d (%s, %d bytes) changed: the format must not change\n got %+v\nwant %+v", i, want[i].Purpose, want[i].Plaintext, got[i], want[i])
		}
		if want[i].Ciphertext == "" {
			continue
		}
		ct, _ := hex.DecodeString(want[i].Ciphertext)
		binding, _ := hex.DecodeString(want[i].Binding)
		if want[i].Binding == "" {
			binding = nil
		}
		r, err := openBytes(ct, keys, want[i].Purpose, binding)
		if err != nil {
			t.Fatalf("vector %d: %v", i, err)
		}
		plain, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(plain, pattern(want[i].Plaintext)) {
			t.Errorf("vector %d: plaintext differs (%v)", i, err)
		}
	}
}

// FuzzOpen: no input panics, hangs or makes the Reader allocate more than
// its one chunk buffer; whatever opens yields only authenticated plaintext
// (here: the seed's).
func FuzzOpen(f *testing.F) {
	plain := pattern(ChunkSize + 300)
	binding := sum(plain)
	_, valid := makeVector(f, 99, PurposeMessage, 0)
	var big bytes.Buffer
	w, _ := newWriterSalt(&big, vectorKeyID, vectorDataKey(), PurposeMessage, vectorSalt(98))
	_, _ = w.Write(plain)
	_ = w.Finish(binding)

	f.Add(valid)
	f.Add(big.Bytes())
	f.Add([]byte{})
	f.Add([]byte(Magic))
	// Crafted headers: every field out of range, alone.
	for _, mut := range []struct {
		off int
		val byte
	}{
		{offVersion, 0}, {offVersion, 2}, {offVersion, 0xff},
		{offPurpose, 0}, {offPurpose, 3}, {offPurpose, 0xff},
		{offExponent, 0}, {offExponent, 15}, {offExponent, 17}, {offExponent, 63}, {offExponent, 0xff},
		{offReserved, 1}, {offKeyID, 0xff}, {offPadding, 1}, {HeaderSize - 1, 0x80},
	} {
		c := bytes.Clone(big.Bytes())
		c[mut.off] = mut.val
		f.Add(c)
	}
	zeroID := bytes.Clone(valid)
	copy(zeroID[offKeyID:offKeyID+4], []byte{0, 0, 0, 0})
	f.Add(zeroID)
	f.Add(append(bytes.Clone(big.Bytes()), 0))
	f.Add(big.Bytes()[:HeaderSize+encChunkSize])
	f.Add(big.Bytes()[:HeaderSize+encChunkSize+TagSize])

	keys := testKeys{vectorKeyID: vectorDataKey()}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Open(bytes.NewReader(data), int64(len(data)), keys, PurposeMessage, binding)
		if err != nil {
			if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrNotEncrypted) && !errors.Is(err, ErrUnknownKey) {
				t.Fatalf("unclassified error: %v", err)
			}
			return
		}
		if cap(r.buf) != encChunkSize {
			t.Fatalf("reader buffer of %d bytes", cap(r.buf))
		}
		got, err := io.ReadAll(r)
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("unclassified read error: %v", err)
			}
			return
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("a modified file decrypted to %d bytes of other plaintext", len(got))
		}
	})
}

func benchmarkWrite(b *testing.B, n int) {
	dk := randomKey(b)
	plain := pattern(n)
	b.SetBytes(int64(n))
	b.ReportAllocs()
	for b.Loop() {
		w, err := NewWriter(io.Discard, 1, dk, PurposeMessage)
		if err != nil {
			b.Fatal(err)
		}
		h := sha256.New() // Put hashes while it encrypts
		_, _ = io.Copy(io.MultiWriter(w, h), bytes.NewReader(plain))
		if err := w.Finish(h.Sum(nil)); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkRead(b *testing.B, n int) {
	dk := randomKey(b)
	plain := pattern(n)
	ct := encrypt(b, 1, dk, PurposeMessage, plain, sum(plain))
	keys := testKeys{1: dk}
	b.SetBytes(int64(n))
	b.ReportAllocs()
	for b.Loop() {
		r, err := openBytes(ct, keys, PurposeMessage, sum(plain))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			b.Fatal(err)
		}
	}
}

// 75 KB is the plan's typical message; 10 MiB a large attachment.
func BenchmarkWrite75KB(b *testing.B)  { benchmarkWrite(b, 75_000) }
func BenchmarkWrite10MiB(b *testing.B) { benchmarkWrite(b, 10<<20) }
func BenchmarkRead75KB(b *testing.B)   { benchmarkRead(b, 75_000) }
func BenchmarkRead10MiB(b *testing.B)  { benchmarkRead(b, 10<<20) }
