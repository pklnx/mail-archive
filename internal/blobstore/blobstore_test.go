package blobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPutDeduplicates(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	content := "Subject: hi\r\n\r\nhello\r\n"
	sum := sha256.Sum256([]byte(content))
	want := hex.EncodeToString(sum[:])

	b1, created, err := s.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if !created || b1.SHA256 != want || b1.Size != int64(len(content)) {
		t.Fatalf("unexpected first put: %+v created=%v", b1, created)
	}
	if b1.Path != "messages/"+want[:2]+"/"+want[2:4]+"/"+want+".eml" {
		t.Fatalf("unexpected path %q", b1.Path)
	}

	b2, created, err := s.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if created || b2 != b1 {
		t.Fatalf("second put should dedup: %+v created=%v", b2, created)
	}

	f, err := s.Open(b1.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if string(got) != content {
		t.Fatalf("content mismatch: %q", got)
	}
	if !s.Exists(want) {
		t.Fatal("Exists returned false")
	}

	tmpEntries, err := os.ReadDir(filepath.Join(root, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpEntries) != 0 {
		t.Fatalf("temp files left behind: %d", len(tmpEntries))
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestPutCleansUpOnError(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Put(failingReader{}); err == nil {
		t.Fatal("expected error")
	}
	tmpEntries, _ := os.ReadDir(filepath.Join(root, "tmp"))
	if len(tmpEntries) != 0 {
		t.Fatalf("temp files left behind: %d", len(tmpEntries))
	}
}
