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

func TestWalk(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	blob, _, err := s.Put(strings.NewReader("Subject: hi\r\n\r\nhello\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.eml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := blob.SHA256
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tmp/put-1")
	write("messages/" + h[:2] + "/" + h[2:4] + "/notes.txt")
	write("messages/" + h[:2] + "/" + h + ".eml")
	write("messages/zz/00/x.eml")
	write("messages/" + h[:2] + "/ff/" + h + ".eml") // wrong directory for the hash
	write("export/a.mbox")                           // not walked
	if err := os.Symlink(outside, filepath.Join(root, "messages", "ab")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.eml"), filepath.Join(root, "messages", h[:2], h[2:4], strings.Repeat("0", 64)+".eml")); err != nil {
		t.Fatal(err)
	}

	got := map[string]EntryKind{}
	err = s.Walk(func(e Entry) error {
		if strings.Contains(e.Path, "secret") {
			t.Errorf("followed a symlink: %s", e.Path)
		}
		got[e.Path] = e.Kind
		if e.Kind == EntryBlob && (e.SHA256 != h || e.Size != blob.Size) {
			t.Errorf("blob entry %+v", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]EntryKind{
		blob.Path:   EntryBlob,
		"tmp/put-1": EntryTemp,
		"messages/" + h[:2] + "/" + h[2:4] + "/notes.txt": EntryUnexpected,
		"messages/" + h[:2] + "/" + h + ".eml":            EntryUnexpected,
		"messages/zz":                                     EntryUnexpected,
		"messages/" + h[:2] + "/ff/" + h + ".eml":         EntryUnexpected,
		"messages/ab":                                     EntryUnexpected,
		"messages/" + h[:2] + "/" + h[2:4] + "/" + strings.Repeat("0", 64) + ".eml": EntryUnexpected,
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
	for p, k := range want {
		if g, ok := got[p]; !ok || g != k {
			t.Errorf("%s: got %v (found %v), want %v", p, g, ok, k)
		}
	}
}
