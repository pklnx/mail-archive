package mime

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"
)

// attachMessage builds a message with a text body, an inline cid image and
// the given attachments (name → body, base64).
func attachMessage(names []string, bodies ...[]byte) string {
	var b strings.Builder
	b.WriteString("Subject: a\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n")
	b.WriteString("--m\r\nContent-Type: multipart/related; boundary=r\r\n\r\n")
	b.WriteString("--r\r\nContent-Type: text/html\r\n\r\n<img src=cid:i@x>\r\n")
	b.WriteString("--r\r\nContent-Type: image/png\r\nContent-ID: <i@x>\r\nContent-Disposition: inline; filename=i.png\r\n\r\npng\r\n--r--\r\n")
	for i, body := range bodies {
		fmt.Fprintf(&b, "--m\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Disposition: attachment; filename=%s\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n",
			names[i], base64.StdEncoding.EncodeToString(body))
	}
	b.WriteString("--m--\r\n")
	return b.String()
}

func all(Part) int64 { return 1 << 20 }

func collect(t *testing.T, raw string, limit func(Part) int64) []Attachment {
	t.Helper()
	var got []Attachment
	if err := Attachments(strings.NewReader(raw), limit, func(a Attachment) bool {
		got = append(got, a)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAttachmentsYieldsAttachmentsOnly(t *testing.T) {
	raw := attachMessage([]string{"a.txt", "b.txt"}, []byte("Gr\xf6\xdfe"), []byte("two"))
	got := collect(t, raw, all)
	if len(got) != 2 {
		t.Fatalf("got %d attachments", len(got))
	}
	m := Parse(strings.NewReader(raw))
	for _, a := range got {
		p := m.Parts[a.Index]
		if p.Filename != a.Filename || !p.Attachment || p.Size != a.Size {
			t.Errorf("attachment %+v, Parse part %+v", a.Part, p)
		}
	}
	// The charset is reported and the body already decoded.
	if got[0].Charset != "iso-8859-1" || string(got[0].Data) != "Größe" || string(got[1].Data) != "two" {
		t.Errorf("got %q %q", got[0].Data, got[1].Data)
	}
	// limit 0 skips a part; returning false stops.
	got = collect(t, raw, func(p Part) int64 {
		if p.Filename == "a.txt" {
			return 0
		}
		return 10
	})
	if len(got) != 1 || got[0].Filename != "b.txt" {
		t.Errorf("skip: %+v", got)
	}
	n := 0
	_ = Attachments(strings.NewReader(raw), all, func(Attachment) bool { n++; return false })
	if n != 1 {
		t.Errorf("stop: %d calls", n)
	}
}

func TestAttachmentsTooLargeReadsAtMostLimit(t *testing.T) {
	raw := attachMessage([]string{"big.txt", "small.txt"}, bytes.Repeat([]byte("x"), 1000), []byte("ok"))
	var read []*int64
	var got []Attachment
	err := attachments(strings.NewReader(raw), func(Part) int64 { return 100 }, func(a Attachment) bool {
		got = append(got, a)
		return true
	}, maxDecoded, counted(&read))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].TooLarge || got[0].Data != nil || string(got[1].Data) != "ok" {
		t.Fatalf("got %+v", got)
	}
	if *read[0] > 101 {
		t.Errorf("read %d bytes of the large part", *read[0])
	}
}

func TestAttachmentsCorruptPart(t *testing.T) {
	raw := "Subject: c\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n" +
		"--m\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=bad.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\n%%%% !!!!\r\n" +
		"--m\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=good.txt\r\n\r\ngood\r\n--m--\r\n"
	got := collect(t, raw, all)
	if len(got) != 2 || got[0].Err == nil || got[0].Data != nil || string(got[1].Data) != "good" || got[1].Index != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestAttachmentsGuards(t *testing.T) {
	// MaxParts: the parts Parse does not list are not yielded.
	got := collect(t, manyParts(5000), all)
	if len(got) != MaxParts-1 || got[len(got)-1].Index != MaxParts-1 {
		t.Errorf("many parts: %d yielded", len(got))
	}
	// MaxDepth.
	deep := "Subject: d\r\n"
	for i := range 100 {
		deep += fmt.Sprintf("Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	deep += "Content-Type: text/plain\r\nContent-Disposition: attachment; filename=deep.txt\r\n\r\ndeep\r\n"
	if got := collect(t, deep, all); len(got) != 0 {
		t.Errorf("deep: %d yielded", len(got))
	}
	// Decoded bytes: the part that crosses the limit is too large, and
	// nothing after it is yielded, as Parse lists nothing after it.
	raw := attachMessage([]string{"a.txt", "b.txt", "c.txt"}, bytes.Repeat([]byte("a"), 60), bytes.Repeat([]byte("b"), 60), []byte("c"))
	var names []string
	_ = attachments(strings.NewReader(raw), all, func(a Attachment) bool {
		names = append(names, fmt.Sprintf("%s:%v", a.Filename, a.TooLarge))
		return true
	}, 100, func(r io.Reader) io.Reader { return r })
	if strings.Join(names, ",") != "a.txt:false,b.txt:true" {
		t.Errorf("decoded limit: %v", names)
	}
}
