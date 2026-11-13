// Package alertmail sends alert mail to admins (#397, part 5) through the
// relay the operator configured (MAIL_ARCHIVE_MAIL_URL): the notify_mail
// job that the notifier enqueues per admin and transition, the
// confirmation code of a notification address, and `./ma notify
// mail-test`. TLS is required: smtps, or smtp with STARTTLS, with the
// relay's certificate verified. Replies of the relay reach the logs and
// the job queue by code only, never by text, and no address is logged.
// See docs/guide/email-alerts.md.
package alertmail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/pklnx/mail-archive/internal/config"
)

// Timeouts of one delivery, all below the job lease (jobs.LeaseDuration,
// kept alive by the heartbeat).
const (
	DialTimeout     = 15 * time.Second
	CommandTimeout  = 30 * time.Second
	DeliveryTimeout = 2 * time.Minute
)

// Sender sends mail through the configured relay. The relay is the only
// host it connects to.
type Sender struct {
	Cfg *config.Mail
	// TLS is the base TLS configuration; nil means the system's roots.
	// Tests set RootCAs. ServerName is always the relay's host.
	TLS *tls.Config
	// Dial opens the TCP connection; nil means a net.Dialer with
	// DialTimeout. Tests inject one.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// LocalName is the EHLO name; empty means "localhost".
	LocalName string
	// Now is injectable for tests; it sets the command deadlines.
	Now func() time.Time
}

// Phases of a delivery, for SendError.
const (
	PhaseConnect = "connect"
	PhaseTLS     = "tls"
	PhaseAuth    = "auth"
	PhaseMail    = "mail"
	PhaseRcpt    = "rcpt"
	PhaseData    = "data"
)

// ErrNoStartTLS is a relay at smtp:// that offers no STARTTLS: refused,
// nothing is sent in the clear.
var ErrNoStartTLS = errors.New("the relay offers no STARTTLS; refusing to send without TLS")

// SendError is a failed delivery: the phase, and for a reply of the relay
// its code and enhanced code. Its text never contains the reply's text,
// an address or the password.
type SendError struct {
	Phase string
	// Code is the SMTP reply code, 0 if none arrived.
	Code     int
	Enhanced [3]int
	// Timeout: the relay did not answer in time.
	Timeout bool
	// err is the cause without a reply text: network and TLS errors.
	err error
}

func (e *SendError) Error() string {
	switch {
	case e.Code != 0:
		return fmt.Sprintf("smtp %s at %s", e.ReplyCode(), e.Phase)
	case e.Timeout:
		return "smtp timeout at " + e.Phase
	case e.err != nil:
		return fmt.Sprintf("smtp %s: %v", e.Phase, e.err)
	}
	return "smtp failed at " + e.Phase
}

func (e *SendError) Unwrap() error { return e.err }

// ReplyCode is "550 5.1.1", or "550" without an enhanced code.
func (e *SendError) ReplyCode() string {
	if e.Enhanced[0] == 0 {
		return fmt.Sprintf("%d", e.Code)
	}
	return fmt.Sprintf("%d %d.%d.%d", e.Code, e.Enhanced[0], e.Enhanced[1], e.Enhanced[2])
}

// Permanent reports a 5xx reply. Everything else (4xx, timeouts, network errors)
// is worth a retry. A missing STARTTLS or a failed certificate check is
// permanent too: a retry cannot fix the configuration.
func (e *SendError) Permanent() bool {
	if e.Code >= 500 {
		return true
	}
	return e.Code == 0 && !e.Timeout && (errors.Is(e.err, ErrNoStartTLS) || isCertError(e.err))
}

// BadMailbox reports a 5.1.x reply at RCPT (bad mailbox or address). Only this
// unconfirms an admin's address.
func (e *SendError) BadMailbox() bool {
	return e.Phase == PhaseRcpt && e.Code >= 500 && e.Enhanced[0] == 5 && e.Enhanced[1] == 1
}

func isCertError(err error) bool {
	var cv *tls.CertificateVerificationError
	return errors.As(err, &cv)
}

// sendError turns err of phase into a SendError, keeping only the codes of
// a reply.
func sendError(phase string, err error) *SendError {
	var se *SendError
	if errors.As(err, &se) {
		return se
	}
	e := &SendError{Phase: phase}
	var smtpErr *smtp.SMTPError
	var netErr net.Error
	switch {
	case errors.As(err, &smtpErr):
		e.Code = smtpErr.Code
		e.Enhanced = [3]int(smtpErr.EnhancedCode)
		if e.Enhanced[0] < 0 {
			e.Enhanced = [3]int{}
		}
	case errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, context.DeadlineExceeded):
		e.Timeout = true
	default:
		e.err = err
	}
	return e
}

// Result is the relay's answer to a delivered message: its code only.
type Result struct {
	Code     int
	Enhanced string
}

// Send delivers msg (a complete message with CRLF line ends) to the one
// address to, within DeliveryTimeout. A failure is a *SendError.
func (s *Sender) Send(ctx context.Context, to string, msg []byte) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, DeliveryTimeout)
	defer cancel()
	c, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	if s.Cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok || !c.SupportsAuth(sasl.Plain) {
			return nil, &SendError{Phase: PhaseAuth, err: errors.New("the relay offers no AUTH PLAIN")}
		}
		if err := c.Auth(sasl.NewPlainClient("", s.Cfg.Username, s.Cfg.Password)); err != nil {
			return nil, sendError(PhaseAuth, err)
		}
	}
	if err := c.Mail(s.Cfg.From, nil); err != nil {
		return nil, sendError(PhaseMail, err)
	}
	if err := c.Rcpt(to, nil); err != nil {
		return nil, sendError(PhaseRcpt, err)
	}
	w, err := c.Data()
	if err != nil {
		return nil, sendError(PhaseData, err)
	}
	if _, err := w.Write(msg); err != nil {
		return nil, sendError(PhaseData, err)
	}
	resp, err := w.CloseWithResponse()
	if err != nil {
		return nil, sendError(PhaseData, err)
	}
	_ = c.Quit()
	res := &Result{Code: 250}
	if f := strings.Fields(resp.StatusText); len(f) > 0 && strings.Count(f[0], ".") == 2 && f[0][0] == '2' {
		res.Enhanced = f[0]
	}
	return res, nil
}

// connect dials the relay, starts TLS (at once for smtps, by STARTTLS for
// smtp, which must be offered) and greets it. Every command gets at most
// CommandTimeout, and the whole delivery ends with ctx.
func (s *Sender) connect(ctx context.Context) (*smtp.Client, error) {
	dial := s.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: DialTimeout}).DialContext
	}
	dctx, cancel := context.WithTimeout(ctx, DialTimeout)
	raw, err := dial(dctx, "tcp", s.Cfg.Addr())
	cancel()
	if err != nil {
		return nil, sendError(PhaseConnect, err)
	}
	conn := &deadlineConn{Conn: raw, limit: CommandTimeout, now: s.now}
	// The whole delivery ends with ctx: closing the connection ends any
	// command that waits.
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	tlsCfg := s.tlsConfig()
	fail := func(phase string, c *smtp.Client, err error) (*smtp.Client, error) {
		stop()
		if c != nil {
			_ = c.Close()
		}
		_ = raw.Close()
		return nil, sendError(phase, err)
	}
	var c *smtp.Client
	if s.Cfg.ImplicitTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail(PhaseTLS, nil, err)
		}
		c = smtp.NewClient(tc)
	} else {
		// Greeting, EHLO and STARTTLS; refused if the relay does not
		// offer it. go-smtp names that case by text only.
		c, err = smtp.NewClientStartTLS(conn, tlsCfg)
		if err != nil {
			var smtpErr *smtp.SMTPError
			var netErr net.Error
			if !errors.As(err, &smtpErr) && !errors.As(err, &netErr) && strings.Contains(err.Error(), "STARTTLS") {
				err = ErrNoStartTLS
			}
			return fail(PhaseTLS, nil, err)
		}
	}
	c.CommandTimeout, c.SubmissionTimeout = CommandTimeout, CommandTimeout
	if err := c.Hello(s.localName()); err != nil {
		return fail(PhaseConnect, c, err)
	}
	if _, ok := c.TLSConnectionState(); !ok {
		return fail(PhaseTLS, c, ErrNoStartTLS)
	}
	return c, nil
}

func (s *Sender) tlsConfig() *tls.Config {
	var cfg *tls.Config
	if s.TLS != nil {
		cfg = s.TLS.Clone()
	} else {
		cfg = &tls.Config{}
	}
	cfg.ServerName = s.Cfg.Host
	cfg.InsecureSkipVerify = false
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
	}
	return cfg
}

func (s *Sender) localName() string {
	if s.LocalName != "" {
		return s.LocalName
	}
	return "localhost"
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// deadlineConn caps every deadline at limit from now: go-smtp sets a
// deadline of its own for each command and none between them, and the
// greeting and STARTTLS use its default of 5 minutes.
type deadlineConn struct {
	net.Conn
	limit time.Duration
	now   func() time.Time
}

func (c *deadlineConn) cap(t time.Time) time.Time {
	if d := c.now().Add(c.limit); t.IsZero() || t.After(d) {
		return d
	}
	return t
}

func (c *deadlineConn) SetDeadline(t time.Time) error      { return c.Conn.SetDeadline(c.cap(t)) }
func (c *deadlineConn) SetReadDeadline(t time.Time) error  { return c.Conn.SetReadDeadline(c.cap(t)) }
func (c *deadlineConn) SetWriteDeadline(t time.Time) error { return c.Conn.SetWriteDeadline(c.cap(t)) }
