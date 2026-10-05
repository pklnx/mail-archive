package crypto

import (
	"bytes"
	"testing"
)

func newTestSealer(t *testing.T) *Sealer {
	t.Helper()
	encoded, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ParseKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	s := newTestSealer(t)
	sealed, err := s.Seal([]byte("hunter2"), []byte("account:1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hunter2")) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := s.Open(sealed, []byte("account:1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hunter2" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenRejectsWrongContextOrKey(t *testing.T) {
	s := newTestSealer(t)
	sealed, err := s.Seal([]byte("secret"), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(sealed, []byte("b")); err == nil {
		t.Fatal("expected error for wrong associated data")
	}
	other := newTestSealer(t)
	if _, err := other.Open(sealed, []byte("a")); err == nil {
		t.Fatal("expected error for wrong key")
	}
	if _, err := s.Open([]byte{1, 2}, nil); err == nil {
		t.Fatal("expected error for short input")
	}
}

func TestParseKey(t *testing.T) {
	if _, err := ParseKey("not base64!"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
	if _, err := ParseKey("c2hvcnQ="); err == nil {
		t.Fatal("expected error for short key")
	}
}
