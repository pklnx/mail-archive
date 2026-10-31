package attachtext

import (
	"context"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pklnx/mail-archive/internal/attachtext/pdftest"
)

// FuzzExtract feeds arbitrary bytes to PDFium. Every input returns, with a
// status, and only ok or truncated come with text. CI runs the seeds.
func FuzzExtract(f *testing.F) {
	valid := pdftest.Text("seed one", "seed two")
	real, err := os.ReadFile("testdata/invoice.pdf")
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{
		nil, []byte("%PDF-"), valid, real, pdftest.Truncated(valid), pdftest.Truncated(real),
		pdftest.XrefPastEnd(), pdftest.CyclicPages(), pdftest.NestedArrays(1000),
		pdftest.Encrypted("user", "owner", "x"), pdftest.Encrypted("", "owner", "x"),
		pdftest.Pages(50, func(int) string { return "p" }), pdftest.Bomb(4 << 20),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := shared.PDF(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		switch res.Status {
		case StatusOK, StatusTruncated:
			if !utf8.ValidString(res.Text) || strings.ContainsRune(res.Text, 0) || len(res.Text) > MaxText {
				t.Fatalf("bad text for status %s", res.Status)
			}
		case StatusNoText, StatusEncrypted, StatusFailed, StatusTimeout, StatusTooLarge:
			if res.Text != "" {
				t.Fatalf("status %s with text", res.Status)
			}
		default:
			t.Fatalf("status %q", res.Status)
		}
	})
}
