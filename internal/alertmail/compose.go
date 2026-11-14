package alertmail

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"

	"github.com/keepia/archive/internal/config"
)

// Message is one mail to compose: its texts and headers. Subject and
// Body are plain text; names in them were cleaned by Name.
type Message struct {
	// From is the From header (config.Mail.FromHeader).
	From string
	To   string
	// Subject and Body in the recipient's language.
	Subject string
	Body    string
	Date    time.Time
	// MessageID is without the angle brackets.
	MessageID string
}

// Compose writes m as a text/plain UTF-8 message, quoted-printable, with
// Date, Message-ID and Auto-Submitted: auto-generated. Non-ASCII in the
// subject is written as RFC 2047 encoded words. Lines end with CRLF.
func Compose(m Message) ([]byte, error) {
	from, err := config.ParseMailAddress(m.From)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	to, err := config.ParseMailAddress(m.To)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	var h mail.Header
	h.SetAddressList("From", []*mail.Address{{Name: from.Name, Address: from.Address}})
	h.SetAddressList("To", []*mail.Address{{Address: to.Address}})
	h.SetSubject(oneLine(m.Subject))
	h.SetDate(m.Date)
	h.SetMessageID(m.MessageID)
	h.Set("Auto-Submitted", "auto-generated")
	h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	var buf bytes.Buffer
	w, err := mail.CreateSingleInlineWriter(&buf, h)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(m.Body)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Name cleans a name (account, owner, group) for a subject or body: line
// breaks and other control characters become spaces, so a name cannot
// start a header or a line of its own, and it is cut to 200 characters.
func Name(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == ' ' || r == ' ' || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// oneLine replaces any line break left in a subject.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}

// MessageID is "<event>.<user>@<host>" without brackets: the characters of
// the event ID that may not stand in a message ID become '.'. host is the
// host of KEEPIA_ARCHIVE_PUBLIC_URL, or "mail-archive.invalid" without one.
func MessageID(event string, userID int64, publicURL string) string {
	left := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-/=?^_`{|}~", r) {
			return r
		}
		return '.'
	}, event)
	left = strings.Trim(left, ".")
	for strings.Contains(left, "..") {
		left = strings.ReplaceAll(left, "..", ".")
	}
	host := "mail-archive.invalid"
	if u, err := url.Parse(publicURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
		if strings.Contains(host, ":") { // an IPv6 address
			host = "[" + host + "]"
		}
	}
	return fmt.Sprintf("%s.%d@%s", left, userID, host)
}
