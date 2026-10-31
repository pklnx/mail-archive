// Package pdftest builds small PDFs in Go: valid ones with a text layer and
// hostile ones (broken structure, decompression bombs, many pages,
// passwords). It is not a test package, so the tests of other packages and
// the demo can use it; nothing in the server imports it.
//
// The text uses the standard font Helvetica with WinAnsiEncoding, so it
// needs no embedded font; characters outside Windows-1252 become '?'.
package pdftest

import (
	"bytes"
	"crypto/md5" //nolint:gosec // the PDF standard security handler is MD5-based
	"crypto/rc4" //nolint:gosec // RC4 is what PDF 1.4 encryption uses
	"fmt"
	"io"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

// writer assembles a PDF from numbered objects and writes the
// cross-reference table and trailer.
type writer struct {
	objs [][]byte // objs[i] is object i+1, without "n 0 obj"/"endobj"
	// encrypt, if set, encrypts strings and streams of object n.
	encrypt func(n int, data []byte) []byte
}

// add appends an object and returns its number.
func (w *writer) add(body string) int {
	w.objs = append(w.objs, []byte(body))
	return len(w.objs)
}

// reserve appends an empty object to fill in later with set.
func (w *writer) reserve() int { return w.add("") }

func (w *writer) set(n int, body string) { w.objs[n-1] = []byte(body) }

// stream returns a stream object body; filter is "" or a /Filter value.
func (w *writer) streamObj(n int, dict string, data []byte) string {
	if w.encrypt != nil {
		data = w.encrypt(n, data)
	}
	return fmt.Sprintf("<<%s/Length %d>>\nstream\n%s\nendstream", dict, len(data), data)
}

// bytes writes the file with the given trailer entries (besides /Size).
func (w *writer) bytes(trailer string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(w.objs))
	for i, o := range w.objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n", i+1)
		b.Write(o)
		b.WriteString("\nendobj\n")
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(w.objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<</Size %d%s>>\nstartxref\n%d\n%%%%EOF\n", len(w.objs)+1, trailer, xref)
	return b.Bytes()
}

// literal encodes s as a PDF literal string in WinAnsiEncoding.
func literal(s string) string {
	enc, err := charmap.Windows1252.NewEncoder().String(s)
	if err != nil {
		enc = strings.Map(func(r rune) rune {
			if r > 0x7e {
				return '?'
			}
			return r
		}, s)
		if e, err2 := charmap.Windows1252.NewEncoder().String(enc); err2 == nil {
			enc = e
		}
	}
	r := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`, "\r", `\r`, "\n", `\n`)
	return "(" + r.Replace(enc) + ")"
}

// Content returns a page content stream that shows text, one line per
// "\n"-separated line, in Helvetica 10 pt from the top of an A4 page.
func Content(text string) []byte {
	var b bytes.Buffer
	b.WriteString("BT /F1 10 Tf 12 TL 40 800 Td\n")
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteString("T* ")
		}
		b.WriteString(literal(line))
		b.WriteString(" Tj\n")
	}
	b.WriteString("ET\n")
	return b.Bytes()
}

const font = "<</Type/Font/Subtype/Type1/BaseFont/Helvetica/Encoding/WinAnsiEncoding>>"

// document writes pages (content streams, already filtered as filter says)
// under one page tree. extra, if not nil, is called with the page
// dictionary entries and may add resources to them.
func (w *writer) document(contents [][]byte, filter string, extraRes string) (catalog int) {
	fontObj := w.add(font)
	pagesObj := w.reserve()
	kids := make([]string, 0, len(contents))
	for _, c := range contents {
		cn := w.reserve()
		w.set(cn, w.streamObj(cn, filter, c))
		p := w.add(fmt.Sprintf("<</Type/Page/Parent %d 0 R/MediaBox[0 0 595 842]/Resources<</Font<</F1 %d 0 R>>%s>>/Contents %d 0 R>>",
			pagesObj, fontObj, extraRes, cn))
		kids = append(kids, fmt.Sprintf("%d 0 R", p))
	}
	w.set(pagesObj, fmt.Sprintf("<</Type/Pages/Kids[%s]/Count %d>>", strings.Join(kids, " "), len(kids)))
	return w.add(fmt.Sprintf("<</Type/Catalog/Pages %d 0 R>>", pagesObj))
}

// Text returns a valid PDF with one page per argument, showing its text.
func Text(pages ...string) []byte {
	w := &writer{}
	contents := make([][]byte, len(pages))
	for i, p := range pages {
		contents[i] = Content(p)
	}
	cat := w.document(contents, "", "")
	return w.bytes(fmt.Sprintf("/Root %d 0 R", cat))
}

// Pages returns a PDF with n pages; page i shows text(i). Pages with the
// same text share one content stream, so 10,000 equal pages stay small.
func Pages(n int, text func(i int) string) []byte {
	w := &writer{}
	fontObj := w.add(font)
	pagesObj := w.reserve()
	shared := map[string]int{}
	var kids strings.Builder
	for i := range n {
		t := text(i)
		cn, ok := shared[t]
		if !ok {
			cn = w.reserve()
			w.set(cn, w.streamObj(cn, "", Content(t)))
			shared[t] = cn
		}
		p := w.add(fmt.Sprintf("<</Type/Page/Parent %d 0 R/MediaBox[0 0 595 842]/Resources<</Font<</F1 %d 0 R>>>>/Contents %d 0 R>>",
			pagesObj, fontObj, cn))
		fmt.Fprintf(&kids, "%d 0 R ", p)
	}
	w.set(pagesObj, fmt.Sprintf("<</Type/Pages/Kids[%s]/Count %d>>", kids.String(), n))
	cat := w.add(fmt.Sprintf("<</Type/Catalog/Pages %d 0 R>>", pagesObj))
	return w.bytes(fmt.Sprintf("/Root %d 0 R", cat))
}

// Bomb returns a one-page PDF whose content stream is Flate-compressed and
// inflates to inflated bytes: the text "bomb" followed by spaces. The
// stream is written directly, at Deflate's best ratio (about 1:1032), so
// 1 GiB takes about 1 MiB in the file and builds in milliseconds.
func Bomb(inflated int64) []byte {
	w := &writer{}
	cat := w.document([][]byte{bombStream(Content("bomb"), inflated)}, "/Filter/FlateDecode", "")
	return w.bytes(fmt.Sprintf("/Root %d 0 R", cat))
}

// bitWriter writes Deflate's LSB-first bit stream.
type bitWriter struct {
	out  []byte
	acc  uint64
	nacc uint
}

func (b *bitWriter) bits(v uint64, n uint) {
	b.acc |= v << b.nacc
	b.nacc += n
	for b.nacc >= 8 {
		b.out = append(b.out, byte(b.acc)) //nolint:gosec // truncates on purpose
		b.acc >>= 8
		b.nacc -= 8
	}
}

// code writes a Huffman code of n bits, most significant bit first.
func (b *bitWriter) code(c uint64, n uint) {
	var r uint64
	for i := range n {
		r |= (c >> i & 1) << (n - 1 - i)
	}
	b.bits(r, n)
}

func (b *bitWriter) align() {
	if b.nacc > 0 {
		b.bits(0, 8-b.nacc)
	}
}

// bombStream returns a zlib stream of head followed by spaces, inflated
// bytes in all: a stored block with head, then one dynamic block whose
// literal/length code has three symbols (' ' and end of block in 2 bits,
// length 258 in 1 bit) and whose distance code has one (distance 1, 1 bit).
// Each 258 copied bytes then take 2 zero bits.
func bombStream(head []byte, inflated int64) []byte {
	b := &bitWriter{out: []byte{0x78, 0xda}}
	// Stored block, not final.
	b.bits(0, 1)
	b.bits(0, 2)
	b.align()
	n := len(head)
	b.out = append(b.out, byte(n), byte(n>>8), byte(^n), byte(^n>>8)) //nolint:gosec // truncates on purpose
	b.out = append(b.out, head...)
	// Dynamic block, final.
	b.bits(1, 1)
	b.bits(2, 2)
	b.bits(286-257, 5) // HLIT
	b.bits(0, 5)       // HDIST: one distance code
	b.bits(18-4, 4)    // HCLEN: code length codes up to symbol 1 in the order below
	order := []int{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1}
	clen := map[int]uint64{18: 1, 1: 2, 2: 2}
	for _, sym := range order {
		b.bits(clen[sym], 3)
	}
	// Code length codes: 18 = 0, 1 = 10, 2 = 11.
	zeros := func(n int) { // n >= 11
		for n > 0 {
			k := min(n, 138)
			if n-k > 0 && n-k < 11 {
				k = n - 11
			}
			b.code(0, 1)
			b.bits(uint64(k-11), 7) //nolint:gosec // truncates on purpose
			n -= k
		}
	}
	two := func() { b.code(3, 2) }
	one := func() { b.code(2, 2) }
	zeros(32)       // 0..31
	two()           // ' ' (32)
	zeros(256 - 33) // 33..255
	two()           // end of block (256)
	zeros(285 - 257)
	one() // length 258 (285)
	one() // distance 1
	// Literal/length codes: 285 = 0, ' ' = 10, 256 = 11.
	space := func() { b.code(2, 2) }
	left := inflated - int64(len(head))
	space()
	left--
	copies := left / 258
	for i := int64(0); i < copies%4; i++ {
		b.bits(0, 2)
	}
	b.out = append(b.out, make([]byte, copies/4)...) // 4 copies per zero byte
	for range left % 258 {
		space()
	}
	b.code(3, 2) // end of block
	b.align()
	// Adler-32 of head and the spaces, the spaces in closed form.
	const mod = 65521
	s1, s2 := uint64(1), uint64(0)
	for _, c := range head {
		s1 = (s1 + uint64(c)) % mod
		s2 = (s2 + s1) % mod
	}
	// After k more bytes c: s2 += k*s1 + c*k(k+1)/2, s1 += k*c.
	kk := uint64(inflated - int64(len(head))) //nolint:gosec // truncates on purpose
	s2 = (s2 + (kk%mod)*s1 + 32*(tri(kk)%mod)) % mod
	s1 = (s1 + (kk%mod)*32) % mod
	sum := s2<<16 | s1
	return append(b.out, byte(sum>>24), byte(sum>>16), byte(sum>>8), byte(sum)) //nolint:gosec // truncates on purpose
}

// tri returns k(k+1)/2 modulo 65521 without overflow.
func tri(k uint64) uint64 {
	const mod = 65521
	a, c := k, k+1
	if a%2 == 0 {
		a /= 2
	} else {
		c /= 2
	}
	return (a % mod) * (c % mod) % mod
}

// Large returns a valid one-page PDF of exactly size bytes: a page that
// shows text and draws an uncompressed grey image that fills the rest.
func Large(size int, text string) []byte {
	build := func(pixels int) []byte {
		w := &writer{}
		img := w.reserve()
		w.set(img, w.streamObj(img, fmt.Sprintf("/Type/XObject/Subtype/Image/Width %d/Height 1/ColorSpace/DeviceGray/BitsPerComponent 8", pixels),
			bytes.Repeat([]byte{0x80}, pixels)))
		content := append(Content(text), []byte("q 100 0 0 10 40 100 cm /Im1 Do Q\n")...)
		cat := w.document([][]byte{content}, "", fmt.Sprintf("/XObject<</Im1 %d 0 R>>", img))
		return w.bytes(fmt.Sprintf("/Root %d 0 R", cat))
	}
	small := build(1)
	pixels := size - len(small) + 1
	// The length fields grow with the pixel count; adjust once or twice.
	for range 3 {
		got := build(pixels)
		if len(got) == size {
			return got
		}
		pixels += size - len(got)
	}
	return build(pixels)
}

// XrefPastEnd returns a PDF whose startxref and cross-reference entries
// point past the end of the file.
func XrefPastEnd() []byte {
	b := Text("xref")
	i := bytes.LastIndex(b, []byte("startxref\n"))
	out := append([]byte{}, b[:i]...)
	out = append(out, []byte("startxref\n99999999\n%%EOF\n")...)
	return bytes.Replace(out, []byte("0000000009 00000 n"), []byte("0999999999 00000 n"), 1)
}

// CyclicPages returns a PDF whose page tree is a cycle: the root /Pages
// node's kid is a /Pages node whose kid is the root again.
func CyclicPages() []byte {
	w := &writer{}
	root := w.reserve()
	inner := w.reserve()
	w.set(root, fmt.Sprintf("<</Type/Pages/Kids[%d 0 R]/Count 1/Parent %d 0 R>>", inner, inner))
	w.set(inner, fmt.Sprintf("<</Type/Pages/Kids[%d 0 R]/Count 1/Parent %d 0 R>>", root, root))
	cat := w.add(fmt.Sprintf("<</Type/Catalog/Pages %d 0 R>>", root))
	return w.bytes(fmt.Sprintf("/Root %d 0 R", cat))
}

// NestedArrays returns a one-page PDF whose page dictionary holds an array
// nested depth levels deep.
func NestedArrays(depth int) []byte {
	w := &writer{}
	fontObj := w.add(font)
	pagesObj := w.reserve()
	cn := w.reserve()
	w.set(cn, w.streamObj(cn, "", Content("nested")))
	nested := strings.Repeat("[", depth) + strings.Repeat("]", depth)
	p := w.add(fmt.Sprintf("<</Type/Page/Parent %d 0 R/MediaBox[0 0 595 842]/Deep %s/Resources<</Font<</F1 %d 0 R>>>>/Contents %d 0 R>>",
		pagesObj, nested, fontObj, cn))
	w.set(pagesObj, fmt.Sprintf("<</Type/Pages/Kids[%d 0 R]/Count 1>>", p))
	cat := w.add(fmt.Sprintf("<</Type/Catalog/Pages %d 0 R>>", pagesObj))
	return w.bytes(fmt.Sprintf("/Root %d 0 R", cat))
}

// padding is the 32-byte password padding of the standard security handler
// (PDF 1.7, 7.6.3.3, algorithm 2).
var padding = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

func pad(pw string) []byte {
	b := append([]byte(pw), padding...)
	return b[:32]
}

func rc4Crypt(key, data []byte) []byte {
	c, _ := rc4.NewCipher(key) //nolint:gosec // see the import
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// Encrypted returns a PDF with one page per argument, encrypted with the
// standard security handler, revision 2 (RC4, 40-bit key). An empty user
// password opens without a password (owner password only); a non-empty
// one is needed to open the file.
func Encrypted(user, owner string, pages ...string) []byte {
	if owner == "" {
		owner = user
	}
	// Algorithm 3: the /O entry.
	oh := md5.Sum(pad(owner)) //nolint:gosec // see the import
	o := rc4Crypt(oh[:5], pad(user))
	perms := int32(-4) // all permissions
	id := []byte("0123456789abcdef")
	// Algorithm 2: the file key.
	h := md5.New() //nolint:gosec // see the import
	h.Write(pad(user))
	h.Write(o)
	h.Write([]byte{byte(perms), byte(perms >> 8), byte(perms >> 16), byte(perms >> 24)}) //nolint:gosec // truncates on purpose
	h.Write(id)
	key := h.Sum(nil)[:5]
	// Algorithm 4: the /U entry.
	u := rc4Crypt(key, padding)

	w := &writer{encrypt: func(n int, data []byte) []byte {
		// Algorithm 1: the object key.
		oh := md5.New() //nolint:gosec // see the import
		oh.Write(key)
		oh.Write([]byte{byte(n), byte(n >> 8), byte(n >> 16), 0, 0}) //nolint:gosec // truncates on purpose
		return rc4Crypt(oh.Sum(nil)[:10], data)
	}}
	contents := make([][]byte, len(pages))
	for i, p := range pages {
		contents[i] = Content(p)
	}
	cat := w.document(contents, "", "")
	enc := w.add(fmt.Sprintf("<</Filter/Standard/V 1/R 2/Length 40/O<%x>/U<%x>/P %d>>", o, u, perms))
	return w.bytes(fmt.Sprintf("/Root %d 0 R/Encrypt %d 0 R/ID[<%x><%x>]", cat, enc, id, id))
}

// Truncated returns the first half of b.
func Truncated(b []byte) []byte { return append([]byte{}, b[:len(b)/2]...) }

// CountingReader counts the bytes read through it.
type CountingReader struct {
	R io.Reader
	N int64
}

func (c *CountingReader) Read(p []byte) (int, error) {
	n, err := c.R.Read(p)
	c.N += int64(n)
	return n, err
}
