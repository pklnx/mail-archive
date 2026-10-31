package pdftest

import (
	"bytes"
	"compress/zlib"
	"io"
	"testing"
)

func TestBombStreamInflates(t *testing.T) {
	head := Content("bomb")
	for _, n := range []int64{int64(len(head)) + 1, 5000, 1<<20 + 7} {
		z, err := zlib.NewReader(bytes.NewReader(bombStream(head, n)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(z)
		if err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		want := append(append([]byte{}, head...), bytes.Repeat([]byte{' '}, int(n)-len(head))...)
		if !bytes.Equal(got, want) {
			t.Fatalf("%d: got %d bytes", n, len(got))
		}
	}
	if n := len(bombStream(head, 1<<30)); n > 1100<<10 {
		t.Errorf("1 GiB bomb takes %d bytes", n)
	}
}
