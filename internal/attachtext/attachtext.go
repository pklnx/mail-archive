// Package attachtext extracts the text of a message's attachments for
// search: plain text, CSV, TSV, Markdown, HTML and PDF.
//
// Attachments are untrusted input. Every attachment gets a status instead
// of an error, and the limits below bound what one message can cost. PDFs
// are read by PDFium compiled to WebAssembly, in a sandbox inside the
// process (see pdfEngine).
package attachtext

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pklnx/mail-archive/internal/mime"
)

// Limits. They are fixed and documented in docs/reference/security.md.
const (
	// MaxPDFSize is the largest decoded PDF read.
	MaxPDFSize = 25 << 20
	// MaxTextSize is the largest decoded text or HTML attachment read
	// (the limit for bodies in package mime).
	MaxTextSize = 4 << 20
	// MaxText is the most text kept per attachment (mime.MaxIndexText,
	// which keeps each tsvector under PostgreSQL's 1 MB limit).
	MaxText = mime.MaxIndexText
	// MaxMessageText is the most text kept per message.
	MaxMessageText = 1 << 20
	// MaxAttachments is how many attachments of a message are attempted.
	MaxAttachments = 20
	// maxPages is how many pages of a PDF are read.
	maxPages = 500
	// pdfTimeout bounds one PDF, messageTimeout one message.
	pdfTimeout     = 15 * time.Second
	messageTimeout = 60 * time.Second
	// pdfMemoryLimit bounds the WebAssembly linear memory of one PDF,
	// reserved with mmap and returned after each document. The plan
	// allows at most 128 MiB; the gate in #390 set it to the smallest of
	// 64, 96 and 128 MiB that is at least 1.5 times the largest peak of
	// the corpus (49.9 MiB) and gives every corpus PDF the same text as
	// 256 MiB (TestGateMeasurements).
	pdfMemoryLimit = 96 << 20
)

// Status says how the extraction of one attachment ended.
type Status string

// The statuses. Only StatusOK and StatusTruncated come with text.
const (
	StatusOK        Status = "ok"
	StatusTruncated Status = "truncated" // a text or page limit was reached
	StatusNoText    Status = "no_text"   // no text layer, for example a scan
	StatusEncrypted Status = "encrypted" // needs a password to open
	StatusTooLarge  Status = "too_large"
	StatusFailed    Status = "failed" // damaged, not a PDF, or out of memory
	StatusTimeout   Status = "timeout"
)

// Result is the outcome for one attachment.
type Result struct {
	// Part is the leaf part index, as in mime.Parse and the parts API.
	Part     int
	Filename string
	Status   Status
	// Text is set for StatusOK and StatusTruncated: valid UTF-8 without
	// NUL bytes, at most MaxText bytes.
	Text string
}

// kind is what an attachment is read as.
type kind int

const (
	kindNone kind = iota
	kindText
	kindHTML
	kindPDF
)

// textTypes are the text types read, by content type.
var textTypes = map[string]kind{
	"text/plain":                kindText,
	"text/csv":                  kindText,
	"text/tab-separated-values": kindText,
	"text/markdown":             kindText,
	"text/x-markdown":           kindText,
	"text/html":                 kindHTML,
}

// pdfTypes are the content types under which a PDF is looked for. A part
// of another non-text type is looked at when its file name ends in .pdf.
// Either way only the %PDF- signature makes it a PDF.
var pdfTypes = map[string]bool{
	"application/pdf":     true,
	"application/x-pdf":   true,
	"application/acrobat": true,
}

// candidate returns how p would be read, from its type and name.
func candidate(p mime.Part) kind {
	if k, ok := textTypes[p.ContentType]; ok {
		return k
	}
	if pdfTypes[p.ContentType] || !strings.HasPrefix(p.ContentType, "text/") && strings.HasSuffix(strings.ToLower(p.Filename), ".pdf") {
		return kindPDF
	}
	return kindNone
}

// isPDF reports whether data starts with %PDF- within its first KiB, as
// PDF readers accept.
func isPDF(data []byte) bool {
	return bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF-"))
}

// Extractor extracts attachment text. It is safe for concurrent use; PDFs
// are read one at a time in the whole process, also across Extractors (see
// pdfEngine); text, CSV and HTML attachments are read alongside. The zero value is not usable; use
// New.
type Extractor struct {
	pdf *pdfEngine
}

// New returns an Extractor. The WebAssembly module is compiled on the
// first PDF, or by Warm.
func New(logger *slog.Logger) *Extractor {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Extractor{pdf: &pdfEngine{alloc: newAllocator(), logger: logger}}
}

// Warm compiles the PDF module now instead of on the first PDF.
func (e *Extractor) Warm() error { return e.pdf.warm() }

// Close releases the compiled module.
func (e *Extractor) Close() error {
	e.pdf.mu.Lock()
	defer e.pdf.mu.Unlock()
	if e.pdf.pool == nil {
		return nil
	}
	err := e.pdf.pool.Close()
	e.pdf.pool = nil
	return err
}

// Message extracts the text of the attachments of a raw message, at most
// MaxAttachments of them, within messageTimeout and MaxMessageText.
// Attachments of other types get no Result. Attachments that the deadline
// (messageTimeout, or ctx's) never reached get StatusTimeout. The time a PDF
// waits for another one in the process (one at a time) does not count
// against messageTimeout. The error is ctx's when ctx was cancelled (not
// when a deadline passed): the results are incomplete then and must not be
// stored. It wraps ErrUnavailable when the message has a PDF and the PDF
// module cannot be compiled, and is ErrBusy when ctx's deadline passed
// while a PDF waited for its turn: the message is to be tried again.
func (e *Extractor) Message(ctx context.Context, r io.Reader) ([]Result, error) {
	var (
		out    []Result
		left   = MaxMessageText
		budget = messageTimeout // work time left for the message
		ctxEr  error
	)
	limit := func(p mime.Part) int64 {
		switch candidate(p) {
		case kindText, kindHTML:
			return MaxTextSize
		case kindPDF:
			return MaxPDFSize
		}
		return 0
	}
	_ = mime.Attachments(r, limit, func(a mime.Attachment) bool {
		if err := ctx.Err(); err != nil && !isDeadline(err) {
			ctxEr = err
			return false
		}
		var res Result
		var err error
		if ctx.Err() != nil || budget <= 0 {
			res = unreached(a)
		} else {
			start := time.Now()
			var waited time.Duration
			res, waited, err = e.attachment(ctx, a, min(MaxText, left), budget)
			budget -= time.Since(start) - waited
		}
		if err != nil {
			ctxEr = err
			return false
		}
		if res.Status == "" {
			return true // not a PDF after all
		}
		left -= len(res.Text)
		out = append(out, res)
		return len(out) < MaxAttachments && left > 0
	})
	if ctxEr != nil && !isDeadline(ctxEr) {
		return nil, ctxEr
	}
	return out, nil
}

func isDeadline(err error) bool { return errors.Is(err, context.DeadlineExceeded) }

// unreached is the Result of an attachment that the message's deadline did
// not leave time for: StatusTimeout, so that the row records it, unless
// its size or decoding already decide.
func unreached(a mime.Attachment) Result {
	res := Result{Part: a.Index, Filename: a.Filename, Status: StatusTimeout}
	switch {
	case a.TooLarge:
		res.Status = StatusTooLarge
	case a.Err != nil:
		res.Status = StatusFailed
	case candidate(a.Part) == kindPDF && !isPDF(a.Data) && !pdfTypes[a.ContentType]:
		return Result{} // a file named .pdf that is not one
	}
	return res
}

// attachment extracts one attachment within budget, and returns how long
// it waited for its turn to read a PDF. A Result without Status means it
// is not one of the types read.
func (e *Extractor) attachment(ctx context.Context, a mime.Attachment, maxText int, budget time.Duration) (Result, time.Duration, error) {
	res := Result{Part: a.Index, Filename: a.Filename}
	k := candidate(a.Part)
	switch {
	case a.TooLarge:
		res.Status = StatusTooLarge
		return res, 0, nil
	case a.Err != nil:
		res.Status = StatusFailed
		return res, 0, nil //nolint:nilerr // an undecodable part is a status, not an error
	}
	switch k {
	case kindText, kindHTML:
		res.Text, res.Status = plainText(a.Data, a.Charset, k == kindHTML, maxText)
	case kindPDF:
		if !isPDF(a.Data) {
			if !pdfTypes[a.ContentType] {
				return Result{}, 0, nil // a file named .pdf that is not one
			}
			res.Status = StatusFailed
			return res, 0, nil
		}
		t, waited, err := e.pdf.extract(ctx, a.Data, maxText, budget)
		if err != nil {
			return Result{}, waited, err
		}
		res.Text, res.Status = t.text, t.status
		return res, waited, nil
	}
	return res, 0, nil
}

// PDF extracts the text of one PDF, at most MaxText bytes, within the PDF
// limits; data larger than MaxPDFSize is StatusTooLarge. The error is ctx's
// when ctx was cancelled, ErrBusy when its deadline passed while the PDF
// waited for another one.
func (e *Extractor) PDF(ctx context.Context, data []byte) (Result, error) {
	if len(data) > MaxPDFSize {
		return Result{Status: StatusTooLarge}, nil
	}
	if !isPDF(data) {
		return Result{Status: StatusFailed}, nil
	}
	t, _, err := e.pdf.extract(ctx, data, MaxText, pdfTimeout)
	if err != nil {
		return Result{}, err
	}
	return Result{Status: t.status, Text: t.text}, nil
}

// cleanText makes text safe for PostgreSQL TEXT columns and JSON.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.ReplaceAll(s, "\x00", "")
}

// truncateText cuts s to at most max bytes at a rune boundary.
func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
