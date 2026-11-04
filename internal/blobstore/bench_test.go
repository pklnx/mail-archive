package blobstore

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
)

// The cost of encryption at rest on a write and a read of a typical 75 KB
// message (#392, part 2): run with -cpu 2 -benchmem on the target hardware.
// Put includes the fsync barriers, so it measures the disk as well.
func benchMessage(b *testing.B) []byte {
	b.Helper()
	m := make([]byte, 75<<10)
	if _, err := rand.Read(m); err != nil {
		b.Fatal(err)
	}
	return m
}

func benchPut(b *testing.B, encrypt bool) {
	s, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	if encrypt {
		if err := s.SetKeys(benchRing(b), true); err != nil {
			b.Fatal(err)
		}
	}
	m := benchMessage(b)
	b.SetBytes(int64(len(m)))
	b.ResetTimer()
	for i := range b.N {
		m[0], m[1], m[2], m[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24) // a new blob each time
		if _, _, err := s.Put(bytes.NewReader(m)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPutPlain(b *testing.B)     { benchPut(b, false) }
func BenchmarkPutEncrypted(b *testing.B) { benchPut(b, true) }

func benchRead(b *testing.B, encrypt bool) {
	s, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	if encrypt {
		if err := s.SetKeys(benchRing(b), true); err != nil {
			b.Fatal(err)
		}
	}
	m := benchMessage(b)
	blob, _, err := s.Put(bytes.NewReader(m))
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(m)))
	b.ResetTimer()
	for range b.N {
		f, err := s.OpenMessage(blob.SHA256)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, f); err != nil {
			b.Fatal(err)
		}
		_ = f.Close()
	}
}

func BenchmarkOpenMessagePlain(b *testing.B)     { benchRead(b, false) }
func BenchmarkOpenMessageEncrypted(b *testing.B) { benchRead(b, true) }
