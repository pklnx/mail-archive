// Package mime parses archived messages for display and indexing.
//
// Parsing is lenient: malformed messages yield whatever could be decoded,
// never an error that hides the rest of the message.
package mime

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decode legacy charsets to UTF-8
	"github.com/emersion/go-message/mail"
)

// Limits protect the server from huge or hostile messages.
const (
	// MaxIndexText is the maximum length of text stored for full-text search.
	MaxIndexText = 256 << 10
	// maxBody is the maximum size of a text or HTML body kept in memory.
	maxBody = 4 << 20
	// MaxAttachmentNames and MaxAttachmentNameBytes bound the attachment
	// file names stored for search; the bytes count the names joined by
	// newlines.
	MaxAttachmentNames     = 100
	MaxAttachmentNameBytes = 16 << 10
)

// Message is a parsed message.
type Message struct {
	Subject string
	From    string
	To      string
	Cc      string
	Date    string
	Text    string // first text/plain body that is not an attachment
	HTML    string // first text/html body that is not an attachment
	Parts   []Part // all leaf parts in walk order
	// Truncated is set when a body exceeded the in-memory limit.
	Truncated bool
}

// Part is a leaf MIME part. Index is its position among all leaf parts and
// addresses it in OpenPart.
type Part struct {
	Index       int
	ContentType string
	Filename    string
	ContentID   string // without angle brackets
	Size        int64  // decoded size in bytes
	// Attachment: Content-Disposition: attachment, or a named non-text
	// part that is not referenced by Content-ID (Apple Mail sends PDFs as
	// named inline parts). Images embedded via cid: are not attachments.
	Attachment bool
	Inline     bool // Content-Disposition: inline
}

// Parse reads a raw RFC 5322 message.
func Parse(r io.Reader) *Message {
	m := &Message{}
	ent, err := message.Read(r)
	if ent == nil {
		_ = err
		return m
	}
	h := mail.Header{Header: ent.Header}
	m.Subject, _ = h.Subject()
	m.From = addresses(h, "From")
	m.To = addresses(h, "To")
	m.Cc = addresses(h, "Cc")
	m.Date = ent.Header.Get("Date")

	index := 0
	_ = ent.Walk(func(_ []int, part *message.Entity, _ error) error {
		if part.MultipartReader() != nil {
			return nil
		}
		p := describe(part, index)
		index++
		isBody := !p.Attachment && (p.ContentType == "text/plain" || p.ContentType == "text/html")
		switch {
		case isBody && p.ContentType == "text/plain" && m.Text == "":
			m.Text, p.Size = readText(part.Body, &m.Truncated)
		case isBody && p.ContentType == "text/html" && m.HTML == "":
			m.HTML, p.Size = readText(part.Body, &m.Truncated)
		default:
			n, _ := io.Copy(io.Discard, part.Body)
			p.Size = n
		}
		m.Parts = append(m.Parts, p)
		return nil
	})
	return m
}

// ErrNoPart is returned by OpenPart for an index that does not exist.
var ErrNoPart = errors.New("no such part")

// OpenPart returns the decoded content of the leaf part with the given index.
// The caller must not read more than it needs; the returned bytes are the
// whole part.
func OpenPart(r io.Reader, index int, maxSize int64) (Part, []byte, error) {
	ent, _ := message.Read(r)
	if ent == nil {
		return Part{}, nil, ErrNoPart
	}
	var (
		found Part
		data  []byte
		i     int
		ok    bool
	)
	stop := errors.New("stop")
	err := ent.Walk(func(_ []int, part *message.Entity, _ error) error {
		if part.MultipartReader() != nil {
			return nil
		}
		if i == index {
			found = describe(part, i)
			b, err := io.ReadAll(io.LimitReader(part.Body, maxSize+1))
			if err != nil {
				return err
			}
			if int64(len(b)) > maxSize {
				return errors.New("part too large")
			}
			found.Size, data, ok = int64(len(b)), b, true
			return stop
		}
		i++
		return nil
	})
	if ok {
		return found, data, nil
	}
	if err != nil && !errors.Is(err, stop) {
		return Part{}, nil, err
	}
	return Part{}, nil, ErrNoPart
}

// Indexed is what search stores about a message's body and parts.
type Indexed struct {
	// Text is the text body, or the HTML body converted to text, truncated
	// to MaxIndexText.
	Text string
	// AttachmentNames are the file names of the attachments, in walk
	// order, within MaxAttachmentNames and MaxAttachmentNameBytes.
	AttachmentNames []string
	HasAttachment   bool
}

// Index parses a raw message for search.
func Index(r io.Reader) Indexed {
	m := Parse(r)
	text := m.Text
	if strings.TrimSpace(text) == "" && m.HTML != "" {
		text = HTMLToText(m.HTML)
	}
	out := Indexed{Text: truncate(clean(text), MaxIndexText)}
	size, full := 0, false
	for _, p := range m.Parts {
		if !p.Attachment {
			continue
		}
		out.HasAttachment = true
		if full || p.Filename == "" {
			continue
		}
		add := len(p.Filename)
		if len(out.AttachmentNames) > 0 {
			add++ // newline separator
		}
		// Stop at the first name that does not fit, so the stored list is
		// a prefix of the message's attachments.
		if len(out.AttachmentNames) == MaxAttachmentNames || size+add > MaxAttachmentNameBytes {
			full = true
			continue
		}
		size += add
		out.AttachmentNames = append(out.AttachmentNames, p.Filename)
	}
	return out
}

func describe(part *message.Entity, index int) Part {
	p := Part{Index: index, ContentType: "text/plain"}
	if t, _, err := part.Header.ContentType(); err == nil && t != "" {
		p.ContentType = strings.ToLower(t)
	}
	disp, params, _ := part.Header.ContentDisposition()
	p.Filename = params["filename"]
	if p.Filename == "" {
		if _, ctParams, err := part.Header.ContentType(); err == nil {
			p.Filename = ctParams["name"]
		}
	}
	p.Filename = sanitizeFilename(p.Filename)
	p.ContentID = strings.Trim(strings.TrimSpace(part.Header.Get("Content-Id")), "<>")
	switch strings.ToLower(disp) {
	case "attachment":
		p.Attachment = true
	case "inline":
		p.Inline = true
	}
	if !p.Attachment && p.Filename != "" && !strings.HasPrefix(p.ContentType, "text/") && (!p.Inline || p.ContentID == "") {
		p.Attachment = true
	}
	return p
}

func readText(r io.Reader, truncated *bool) (string, int64) {
	var buf bytes.Buffer
	n, _ := io.Copy(&buf, io.LimitReader(r, maxBody+1))
	if n > maxBody {
		*truncated = true
		rest, _ := io.Copy(io.Discard, r)
		n += rest
		return clean(truncate(buf.String(), maxBody)), n
	}
	return clean(buf.String()), n
}

func addresses(h mail.Header, key string) string {
	list, err := h.AddressList(key)
	if err != nil || len(list) == 0 {
		raw, _ := h.Text(key)
		if raw == "" {
			raw = h.Get(key)
		}
		return clean(raw)
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		if a.Name == "" {
			out = append(out, a.Address) // no "<...>" around a bare address
			continue
		}
		out = append(out, a.String())
	}
	// Address.String quotes and encodes names; decode them for display.
	s := strings.Join(out, ", ")
	if dec, err := new(mime.WordDecoder).DecodeHeader(s); err == nil {
		s = dec
	}
	return clean(s)
}

func sanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return truncate(strings.TrimSpace(name), 255)
}

// clean makes text safe for PostgreSQL TEXT columns and JSON.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.ReplaceAll(s, "\x00", "")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
