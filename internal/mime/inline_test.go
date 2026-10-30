package mime

import (
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
)

// inlineMessage builds a multipart/related message with an HTML body and
// one base64 part per body, typed image/png, with Content-ID img<i>@x.
func inlineMessage(bodies ...[]byte) string {
	var b strings.Builder
	b.WriteString("Subject: inline\r\nContent-Type: multipart/related; boundary=b\r\n\r\n")
	b.WriteString("--b\r\nContent-Type: text/html\r\n\r\n<p>x</p>\r\n")
	for i, body := range bodies {
		fmt.Fprintf(&b, "--b\r\nContent-Type: image/png\r\nContent-ID: <img%d@x>\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n",
			i, base64.StdEncoding.EncodeToString(body))
	}
	b.WriteString("--b--\r\n")
	return b.String()
}

func keys(m map[int][]byte) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func TestInlinePartsReturnsWantedBodies(t *testing.T) {
	raw := inlineMessage([]byte("one"), []byte("two"), []byte("three"))
	got, err := InlineParts(strings.NewReader(raw), func(p Part) bool { return p.Index == 1 || p.Index == 3 }, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(keys(got)) != "[1 3]" || string(got[1]) != "one" || string(got[3]) != "three" {
		t.Errorf("got %v", keys(got))
	}
	// The indexes are Parse's.
	m := Parse(strings.NewReader(raw))
	if m.Parts[3].ContentID != "img2@x" || m.Parts[3].Size != 5 {
		t.Errorf("Parse parts = %+v", m.Parts)
	}
	// want sees the part's description.
	var seen []string
	_, _ = InlineParts(strings.NewReader(raw), func(p Part) bool {
		seen = append(seen, p.ContentType+" "+p.ContentID)
		return false
	}, 1<<20)
	if fmt.Sprint(seen) != "[text/html  image/png img0@x image/png img1@x image/png img2@x]" {
		t.Errorf("want saw %q", seen)
	}
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n *int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// counted returns a wrap function for inlineParts that records, per wanted
// part in walk order, how many bytes were read from its body.
func counted(read *[]*int64) func(io.Reader) io.Reader {
	return func(r io.Reader) io.Reader {
		n := new(int64)
		*read = append(*read, n)
		return countingReader{r, n}
	}
}

func TestInlinePartsSkipsPartOverBudget(t *testing.T) {
	small, big := []byte("small"), []byte(strings.Repeat("B", 1<<20))
	raw := inlineMessage(small, big, small)
	const limit = 100
	var read []*int64
	got, err := inlineParts(strings.NewReader(raw), func(Part) bool { return true }, limit, counted(&read))
	if err != nil {
		t.Fatal(err)
	}
	// The big part is skipped; the small one after it still fits.
	if fmt.Sprint(keys(got)) != "[0 1 3]" || string(got[1]) != "small" || string(got[3]) != "small" {
		t.Errorf("got parts %v", keys(got))
	}
	// Of the big part (index 2), only what was left of the budget plus one
	// byte was read.
	left := int64(limit - len("<p>x</p>") - len(small))
	if len(read) != 4 || *read[2] > left+1 {
		t.Fatalf("read %d bytes of the big part, want at most %d", *read[2], left+1)
	}

	// A part bigger than the whole budget is not returned either.
	read = nil
	got, err = inlineParts(strings.NewReader(raw), func(p Part) bool { return p.Index == 2 }, limit, counted(&read))
	if err != nil || len(got) != 0 || len(read) != 1 || *read[0] > limit+1 {
		t.Errorf("big part alone: err %v, got %v, read %d", err, keys(got), *read[0])
	}

	// Once the budget is used up, no further part is read at all.
	read = nil
	got, _ = inlineParts(strings.NewReader(raw), func(p Part) bool { return p.Index > 0 }, int64(len(small)), counted(&read))
	if fmt.Sprint(keys(got)) != "[1]" || len(read) != 1 {
		t.Errorf("exact budget: got %v, %d parts read", keys(got), len(read))
	}
}

func TestInlinePartsBrokenPart(t *testing.T) {
	raw := strings.Replace(inlineMessage([]byte("good"), []byte("corrupt"), []byte("after")),
		base64.StdEncoding.EncodeToString([]byte("corrupt")), "!!!!not*base64", 1)
	got, err := InlineParts(strings.NewReader(raw), func(p Part) bool { return p.Index > 0 }, 1<<20)
	if err == nil {
		t.Fatal("no error for corrupt base64")
	}
	// The broken part is missing; the others are there.
	if fmt.Sprint(keys(got)) != "[1 3]" || string(got[1]) != "good" || string(got[3]) != "after" {
		t.Errorf("got %v", keys(got))
	}
}

func TestInlinePartsLimits(t *testing.T) {
	// Parts beyond MaxParts are not seen, like in Parse.
	last := -1
	got, err := InlineParts(strings.NewReader(manyParts(5000)), func(p Part) bool {
		last = p.Index
		return true
	}, 1<<20)
	if err != nil || last != MaxParts-1 || len(got) != MaxParts {
		t.Errorf("many parts: err %v, last index %d, %d parts", err, last, len(got))
	}
	// Too deep nesting: nothing below the limit is read.
	got, err = InlineParts(strings.NewReader(deepNesting(10000)), func(Part) bool { return true }, 1<<20)
	if err != nil || len(got) != 0 {
		t.Errorf("deep: err %v, parts %v", err, keys(got))
	}
	// A message cut off inside its last part: the parts before it are
	// returned with the indexes Parse lists; the cut part is not complete,
	// so it is skipped with an error.
	raw := inlineMessage([]byte("one"), []byte("two"))
	cut := raw[:strings.LastIndex(raw, "--b--")-len("\r\n")]
	m := Parse(strings.NewReader(cut))
	got, err = InlineParts(strings.NewReader(cut), func(Part) bool { return true }, 1<<20)
	if err == nil || len(m.Parts) != 3 || fmt.Sprint(keys(got)) != "[0 1]" || string(got[1]) != "one" {
		t.Errorf("cut message: err %v, got %v, Parse lists %d parts", err, keys(got), len(m.Parts))
	}
	// Not a message at all: one empty text part, as in Parse.
	if got, _ := InlineParts(strings.NewReader(""), func(Part) bool { return true }, 1<<20); fmt.Sprint(keys(got)) != "[0]" || len(got[0]) != 0 {
		t.Errorf("empty: %v", keys(got))
	}
}

// withAttachment adds a base64 application/pdf attachment with body after
// the last part of a message built by inlineMessage.
func withAttachment(raw string, body []byte) string {
	end := strings.LastIndex(raw, "--b--")
	return raw[:end] + "--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=big.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(body) + "\r\n" + raw[end:]
}

func TestInlinePartsStopsAfterBudget(t *testing.T) {
	img := []byte("small-image")
	raw := withAttachment(inlineMessage(img), []byte(strings.Repeat("P", 1<<20)))
	wantImage := func(p Part) bool { return p.Index == 1 }

	// The budget is exactly the wanted part: once it is read, the trailing
	// attachment is not drained from the source.
	var n int64
	got, err := InlineParts(countingReader{strings.NewReader(raw), &n}, wantImage, int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(keys(got)) != "[1]" || string(got[1]) != string(img) {
		t.Errorf("exact budget: got %v", keys(got))
	}
	if n >= 64<<10 {
		t.Errorf("exact budget: read %d bytes of a %d-byte message, want < %d", n, len(raw), 64<<10)
	}

	// A budget larger than the wanted parts is never used up: the walk goes
	// to the end, as before, and returns the same parts.
	n = 0
	got, err = InlineParts(countingReader{strings.NewReader(raw), &n}, wantImage, int64(len(img))+1)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(keys(got)) != "[1]" || string(got[1]) != string(img) {
		t.Errorf("larger budget: got %v", keys(got))
	}
	if n != int64(len(raw)) {
		t.Errorf("larger budget: read %d bytes, want the whole message (%d)", n, len(raw))
	}
}
