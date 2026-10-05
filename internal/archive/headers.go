package archive

import (
	"bufio"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
)

// Headers is the subset of message headers stored as metadata.
type Headers struct {
	MessageID string
	Subject   string
	From      string
	Date      *time.Time
}

const maxHeaderField = 2000

var wordDecoder = &mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		enc, err := htmlindex.Get(charset)
		if err != nil {
			return nil, fmt.Errorf("unsupported charset %q", charset)
		}
		return enc.NewDecoder().Reader(input), nil
	},
}

// ParseHeaders extracts metadata from a raw RFC 5322 message. It is lenient:
// malformed headers yield empty fields rather than an error, because the raw
// message is archived regardless.
func ParseHeaders(r io.Reader) Headers {
	msg, err := mail.ReadMessage(bufio.NewReader(r))
	if err != nil {
		return Headers{}
	}
	h := msg.Header
	out := Headers{
		MessageID: clean(strings.Trim(strings.TrimSpace(h.Get("Message-Id")), "<>")),
		Subject:   clean(decode(h.Get("Subject"))),
		From:      clean(decode(h.Get("From"))),
	}
	if d, err := mail.ParseDate(h.Get("Date")); err == nil {
		out.Date = &d
	}
	return out
}

func decode(s string) string {
	if dec, err := wordDecoder.DecodeHeader(s); err == nil {
		return dec
	}
	return s
}

// clean makes a header value safe for a PostgreSQL TEXT column.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxHeaderField {
		cut := maxHeaderField
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s
}
