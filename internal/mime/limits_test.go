package mime

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// manyParts builds a multipart message with n attachments.
func manyParts(n int) string {
	var b strings.Builder
	b.WriteString("Subject: many\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n")
	b.WriteString("--b\r\nContent-Type: text/plain\r\n\r\nbody\r\n")
	for i := range n {
		fmt.Fprintf(&b, "--b\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=f%d.bin\r\n\r\nx\r\n", i)
	}
	b.WriteString("--b--\r\n")
	return b.String()
}

// deepNesting builds a message with depth nested multiparts around one text part.
func deepNesting(depth int) string {
	var b strings.Builder
	b.WriteString("Subject: deep\r\n")
	for i := range depth {
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	b.WriteString("Content-Type: text/plain\r\n\r\ninnermost\r\n")
	for i := depth - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "--b%d--\r\n", i)
	}
	return b.String()
}

func TestPartLimit(t *testing.T) {
	m := Parse(strings.NewReader(manyParts(5000)))
	if len(m.Parts) != MaxParts || !m.Truncated || m.Text != "body" {
		t.Fatalf("parts %d, truncated %v, text %q", len(m.Parts), m.Truncated, m.Text)
	}
	idx := Index(strings.NewReader(manyParts(5000)))
	if !idx.HasAttachment || len(idx.AttachmentNames) != MaxAttachmentNames {
		t.Errorf("index %d names", len(idx.AttachmentNames))
	}
	if _, _, err := OpenPart(strings.NewReader(manyParts(5000)), MaxParts-1, 1<<20); err != nil {
		t.Errorf("last listed part: %v", err)
	}
	if _, _, err := OpenPart(strings.NewReader(manyParts(5000)), MaxParts, 1<<20); !errors.Is(err, ErrNoPart) {
		t.Errorf("part beyond the limit: %v", err)
	}
	// A message within the limits is not truncated.
	if m := Parse(strings.NewReader(manyParts(10))); m.Truncated || len(m.Parts) != 11 {
		t.Errorf("small message: %d parts, truncated %v", len(m.Parts), m.Truncated)
	}
}

func TestDepthLimit(t *testing.T) {
	start := time.Now()
	m := Parse(strings.NewReader(deepNesting(10000)))
	if !m.Truncated || m.Text != "" || m.Subject != "deep" {
		t.Fatalf("deep message: truncated %v, text %q", m.Truncated, m.Text)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("parsing took %v", d)
	}
	// Nesting within the limit still finds the text.
	if m := Parse(strings.NewReader(deepNesting(MaxDepth - 1))); m.Truncated || m.Text != "innermost" {
		t.Errorf("nesting within the limit: truncated %v, text %q", m.Truncated, m.Text)
	}
	if _, _, err := OpenPart(bytes.NewReader([]byte(deepNesting(10000))), 0, 1<<20); !errors.Is(err, ErrNoPart) {
		t.Errorf("part below the depth limit: %v", err)
	}
}
