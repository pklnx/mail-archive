package web

import (
	"encoding/base64"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/pklnx/mail-archive/internal/mime"
)

// maxInlineBytes bounds the data: URLs that one HTML view adds for inline
// images, counted per reference. A variable so tests can lower it.
var maxInlineBytes int64 = 10 << 20

// cidRef matches a cid: URL (RFC 2392) up to the next quote, white space,
// ")", "<" or ">". The match is consumed whole and never echoed, so its
// content cannot reach the output.
var cidRef = regexp.MustCompile(`(?i)\bcid:([^"'\s)<>]+)`)

// emptyImage replaces a cid: reference that is not inlined: an empty image
// that makes no request and violates no CSP rule.
const emptyImage = "data:,"

// inlineResult is the HTML body with cid: references replaced.
type inlineResult struct {
	HTML    string
	Inlined int // references replaced by an image
	Omitted int // references replaced by emptyImage
}

// inlineImages replaces every cid: reference in html with a data: URL of the
// referenced part, so the sandboxed frame needs no request to show it (its
// img-src has no 'self', and the Fetch Metadata guard refuses image
// requests to the API). Only parts of inlineTypes
// are inlined, within maxInlineBytes in document order; unknown references,
// other types (SVG, HTML, ...), references past the budget and parts that
// cannot be read become emptyImage.
//
// open opens the stored message; it is called only when there is something
// to inline. A read error is logged by the caller and leaves the affected
// references empty: a broken image must not hide the mail.
func inlineImages(html string, parts []mime.Part, open func() (io.ReadCloser, error)) (inlineResult, error) {
	locs := cidRef.FindAllStringSubmatchIndex(html, -1)
	if len(locs) == 0 {
		return inlineResult{HTML: html}, nil
	}
	// With duplicate Content-IDs the first part in walk order wins.
	byID := map[string]mime.Part{}
	for _, p := range parts {
		if _, dup := byID[p.ContentID]; p.ContentID != "" && !dup {
			byID[p.ContentID] = p
		}
	}
	// Choose the references to inline before any image bytes are read.
	chosen := make([]*mime.Part, len(locs))
	want := map[int]bool{}
	var used, total int64
	for i, loc := range locs {
		p, ok := lookupCID(byID, html[loc[2]:loc[3]])
		if !ok || !inlineTypes[p.ContentType] {
			continue
		}
		cost := int64(len(dataPrefix(p.ContentType))) + int64(base64.StdEncoding.EncodedLen(int(p.Size)))
		if used+cost > maxInlineBytes {
			continue
		}
		used += cost
		chosen[i] = &p
		if !want[p.Index] {
			want[p.Index] = true
			total += p.Size
		}
	}
	var (
		data    map[int][]byte
		readErr error
	)
	if len(want) > 0 {
		data, readErr = readInline(open, want, total)
	}

	var (
		b       strings.Builder
		res     inlineResult
		encoded = map[int]string{}
		last    int
	)
	b.Grow(len(html) + int(used))
	for i, loc := range locs {
		b.WriteString(html[last:loc[0]])
		last = loc[1]
		p := chosen[i]
		d, ok := []byte(nil), false
		if p != nil {
			d, ok = data[p.Index]
		}
		// The size must be the one the budget was checked with.
		if !ok || int64(len(d)) != p.Size {
			b.WriteString(emptyImage)
			res.Omitted++
			continue
		}
		s, done := encoded[p.Index]
		if !done {
			// The type is an inlineTypes key and the payload base64, so the
			// URL holds only [A-Za-z0-9+/=:;,] and cannot leave its attribute.
			s = dataPrefix(p.ContentType) + base64.StdEncoding.EncodeToString(d)
			encoded[p.Index] = s
		}
		b.WriteString(s)
		res.Inlined++
	}
	b.WriteString(html[last:])
	res.HTML = b.String()
	return res, readErr
}

func dataPrefix(contentType string) string { return "data:" + contentType + ";base64," }

// lookupCID finds the part of a cid: URL's value, percent-decoded as RFC 2392
// asks; a value that is not valid percent-encoding is looked up as written.
func lookupCID(byID map[string]mime.Part, raw string) (mime.Part, bool) {
	if id, err := url.PathUnescape(raw); err == nil {
		if p, ok := byID[id]; ok {
			return p, true
		}
	}
	p, ok := byID[raw]
	return p, ok
}

// readInline reads the parts whose indexes are in want from the stored
// message, at most total decoded bytes.
func readInline(open func() (io.ReadCloser, error), want map[int]bool, total int64) (map[int][]byte, error) {
	f, err := open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return mime.InlineParts(f, func(p mime.Part) bool { return want[p.Index] }, total)
}
