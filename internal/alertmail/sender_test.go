package alertmail_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keepia/archive/internal/alertmail"
	"github.com/keepia/archive/internal/alertmail/mailtest"
)

func testMessage(t *testing.T, to string) []byte {
	t.Helper()
	msg, err := alertmail.Compose(alertmail.Message{
		From: "Mail Archive <archive@relay.test>", To: to, Subject: "Mail-Archiv: Speicher der Gruppe „Müller“ voll",
		Body: "Zeile eins mit Umlaut ä\nZeile zwei\n", Date: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC),
		MessageID: alertmail.MessageID("storage:7:3", 5, "https://archive.example.com:8443"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// TestSendTLS: over STARTTLS and over TLS from the start, with AUTH
// PLAIN, the relay takes the message as composed: Date, Message-ID,
// Auto-Submitted, text/plain UTF-8 quoted-printable, and the non-ASCII
// subject as RFC 2047 encoded words.
func TestSendTLS(t *testing.T) {
	for _, mode := range []string{mailtest.StartTLS, mailtest.Implicit} {
		t.Run(mode, func(t *testing.T) {
			relay := mailtest.Start(t, mode)
			res, err := relay.Sender().Send(context.Background(), "admin@example.com", testMessage(t, "admin@example.com"))
			if err != nil {
				t.Fatal(err)
			}
			if res.Code != 250 {
				t.Fatalf("result %+v", res)
			}
			got := relay.Mails()
			if len(got) != 1 || got[0].From != "archive@relay.test" || len(got[0].To) != 1 || got[0].To[0] != "admin@example.com" {
				t.Fatalf("mails %+v", got)
			}
			m, err := mail.ReadMessage(bytes.NewReader(got[0].Data))
			if err != nil {
				t.Fatal(err)
			}
			h := m.Header
			subject, err := new(mime.WordDecoder).DecodeHeader(h.Get("Subject"))
			if err != nil || subject != "Mail-Archiv: Speicher der Gruppe „Müller“ voll" || !strings.Contains(h.Get("Subject"), "=?utf-8?") {
				t.Errorf("subject %q (%q) %v", h.Get("Subject"), subject, err)
			}
			if h.Get("Auto-Submitted") != "auto-generated" || h.Get("Message-Id") != "<storage.7.3.5@archive.example.com>" ||
				!strings.HasPrefix(h.Get("Content-Type"), "text/plain") || !strings.Contains(h.Get("Content-Type"), "utf-8") ||
				h.Get("Content-Transfer-Encoding") != "quoted-printable" || h.Get("Mime-Version") != "1.0" {
				t.Errorf("header %v", h)
			}
			if d, err := h.Date(); err != nil || !d.Equal(time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)) {
				t.Errorf("date %v %v", d, err)
			}
			body, err := io.ReadAll(quotedprintable.NewReader(m.Body))
			if err != nil || string(body) != "Zeile eins mit Umlaut ä\r\nZeile zwei\r\n" {
				t.Errorf("body %q %v", body, err)
			}
		})
	}
}

// TestSendRefusesWithoutTLS: a relay at smtp:// without STARTTLS gets no
// MAIL command; the error is permanent. A relay whose certificate is not
// trusted is refused too.
func TestSendRefusesWithoutTLS(t *testing.T) {
	relay := mailtest.Start(t, mailtest.NoTLS)
	_, err := relay.Sender().Send(context.Background(), "admin@example.com", testMessage(t, "admin@example.com"))
	var se *alertmail.SendError
	if !errors.As(err, &se) || !errors.Is(err, alertmail.ErrNoStartTLS) || !se.Permanent() || se.Phase != alertmail.PhaseTLS {
		t.Fatalf("no STARTTLS: %v", err)
	}
	if relay.MailCommands() != 0 || len(relay.Mails()) != 0 {
		t.Fatalf("sent without TLS: %d commands", relay.MailCommands())
	}

	for _, mode := range []string{mailtest.StartTLS, mailtest.Implicit} {
		relay := mailtest.Start(t, mode)
		s := relay.Sender()
		s.TLS = &tls.Config{} // the system's roots: the test certificate is unknown
		_, err := s.Send(context.Background(), "admin@example.com", testMessage(t, "admin@example.com"))
		if !errors.As(err, &se) || !se.Permanent() || relay.MailCommands() != 0 {
			t.Fatalf("%s untrusted certificate: %v, %d commands", mode, err, relay.MailCommands())
		}
	}
}

// TestSendReplies: a 4xx is worth a retry, 5.1.1 at RCPT is a bad
// mailbox, 5.7.1 is permanent but no bad mailbox; the error names the
// codes and never the relay's text.
func TestSendReplies(t *testing.T) {
	const marker = "zqreplytextzq"
	for _, c := range []struct {
		phase       string
		reply       error
		permanent   bool
		badMailbox  bool
		code, wantP string
	}{
		{"rcpt", mailtest.Error(450, 4, 2, 1, marker), false, false, "450 4.2.1", alertmail.PhaseRcpt},
		{"rcpt", mailtest.Error(550, 5, 1, 1, marker), true, true, "550 5.1.1", alertmail.PhaseRcpt},
		{"rcpt", mailtest.Error(554, 5, 7, 1, marker), true, false, "554 5.7.1", alertmail.PhaseRcpt},
		{"mail", mailtest.Error(550, 5, 1, 8, marker), true, false, "550 5.1.8", alertmail.PhaseMail},
		{"data", mailtest.Error(451, 4, 3, 0, marker), false, false, "451 4.3.0", alertmail.PhaseData},
	} {
		relay := mailtest.Start(t, mailtest.StartTLS)
		relay.On(c.phase, func(string) error { return c.reply })
		_, err := relay.Sender().Send(context.Background(), "admin@example.com", testMessage(t, "admin@example.com"))
		var se *alertmail.SendError
		if !errors.As(err, &se) || se.Phase != c.wantP || se.ReplyCode() != c.code || se.Permanent() != c.permanent || se.BadMailbox() != c.badMailbox {
			t.Errorf("%s %s: %v %+v", c.phase, c.code, err, se)
			continue
		}
		if strings.Contains(err.Error(), marker) || !strings.Contains(err.Error(), c.code) {
			t.Errorf("error text %q", err)
		}
	}
}

// stallConn records the deadlines go-smtp sets and makes each one expire
// at once: a relay that stops answering ends at the command timeout, here
// without waiting for it.
type stallConn struct {
	net.Conn
	mu        sync.Mutex
	deadlines []time.Duration
	now       func() time.Time
}

func (c *stallConn) set(t time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, t.Sub(c.now()))
	c.mu.Unlock()
	return c.Conn.SetDeadline(time.Unix(1, 0))
}

func (c *stallConn) SetDeadline(t time.Time) error      { return c.set(t) }
func (c *stallConn) SetReadDeadline(t time.Time) error  { return c.set(t) }
func (c *stallConn) SetWriteDeadline(t time.Time) error { return c.set(t) }

// TestSendTimeout: a relay that accepts the connection and never answers
// ends the delivery with a timeout, and every deadline set on the
// connection is the command timeout, also for the greeting, for which
// go-smtp's own default is 5 minutes.
func TestSendTimeout(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c // holds the connection open, says nothing
		}
	}()
	// A fixed now for the sender's caps; go-smtp's own deadlines use the
	// clock, and lie later than it.
	fixed := time.Now()
	relay := mailtest.Start(t, mailtest.StartTLS)
	s := relay.Sender()
	s.Now = func() time.Time { return fixed }
	var sc *stallConn
	s.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, l.Addr().String())
		if err != nil {
			return nil, err
		}
		sc = &stallConn{Conn: c, now: func() time.Time { return fixed }}
		return sc, nil
	}
	_, err = s.Send(context.Background(), "admin@example.com", testMessage(t, "admin@example.com"))
	var se *alertmail.SendError
	if !errors.As(err, &se) || !se.Timeout || se.Permanent() {
		t.Fatalf("stalled relay: %v", err)
	}
	if sc == nil || len(sc.deadlines) == 0 {
		t.Fatal("no deadline set")
	}
	for _, d := range sc.deadlines {
		if d != alertmail.CommandTimeout {
			t.Errorf("deadline %v from now, want %v", d, alertmail.CommandTimeout)
		}
	}
	select {
	case c := <-accepted:
		_ = c.Close()
	default:
	}
}

// TestComposeHeaderInjection: names with line breaks cannot add a header
// or start a line of the body; the subject stays one header.
func TestComposeHeaderInjection(t *testing.T) {
	evil := "acc\r\nBcc: zqinjectedzq@example.com\r\n\r\nbody"
	subject, body := alertmail.Texts(alertmail.LangEN, alertmail.Alert{
		AlertMailPayload: payloadSync(), Account: alertmail.Name(evil), Owner: alertmail.Name("own\ner\rX-Evil: 1"),
	})
	msg, err := alertmail.Compose(alertmail.Message{
		From: "archive@relay.test", To: "admin@example.com", Subject: subject + "\r\nBcc: zqinjectedzq@example.com",
		Body: body, Date: time.Now(), MessageID: alertmail.MessageID("1-0-failing", 1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.Get("Bcc") != "" || m.Header.Get("X-Evil") != "" {
		t.Fatalf("injected header: %v", m.Header)
	}
	sc := bufio.NewScanner(quotedprintable.NewReader(m.Body))
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "Bcc:") || strings.HasPrefix(sc.Text(), "X-Evil") || sc.Text() == "body" {
			t.Fatalf("a name starts a line: %q", sc.Text())
		}
	}
	if alertmail.MessageID("storage:instance:3", 9, "") != "storage.instance.3.9@mail-archive.invalid" {
		t.Errorf("message ID %q", alertmail.MessageID("storage:instance:3", 9, ""))
	}
}
