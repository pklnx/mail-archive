// Package smtpd receives copies of every message from a mail server over
// SMTP (journaling) and stores them in smtp accounts.
//
// A message is acknowledged with 250 only after it is stored for every
// account it is addressed to; any failure answers 451 and the mail server
// retries. Mail servers drop a BCC copy that gets a 5xx without telling
// anyone, so every 5xx is logged at error level.
package smtpd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// Defaults for the limits of a Server.
const (
	DefaultMaxSize       = 50 << 20
	DefaultMaxConns      = 10
	DefaultMaxRecipients = 100
	DefaultReadTimeout   = 5 * time.Minute
	DefaultWriteTimeout  = time.Minute
	// ShutdownGrace is how long Serve waits for open connections to end
	// after its context is cancelled.
	ShutdownGrace = 30 * time.Second
	// maxLineLength is far above RFC 5321's 1000: the archive stores what
	// arrives, and a refused line would keep the message queued for good.
	maxLineLength = 1 << 20
	// storeTimeout bounds storing one message after DATA.
	storeTimeout = 2 * time.Minute
)

// Server receives mail for smtp accounts.
type Server struct {
	Store  *store.Store
	Blobs  *blobstore.Store
	Logger *slog.Logger
	// Allowed lists the networks clients may connect from; required.
	Allowed []netip.Prefix
	// Hostname is the name in the greeting.
	Hostname string
	// TLS turns STARTTLS on; nil leaves it off.
	TLS           *tls.Config
	MaxSize       int64
	MaxConns      int
	MaxRecipients int
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
	Now           func() time.Time

	// receive stores a message for one account; tests replace it.
	receive func(ctx context.Context, account int64, meta store.MessageMeta, env store.Envelope) (store.ReceiveResult, error)
	// inflight counts messages being stored, so that Serve can wait for
	// them after closing the connections.
	inflight sync.WaitGroup
}

func (s *Server) defaults() {
	if s.MaxSize <= 0 {
		s.MaxSize = DefaultMaxSize
	}
	if s.MaxConns <= 0 {
		s.MaxConns = DefaultMaxConns
	}
	if s.MaxRecipients <= 0 {
		s.MaxRecipients = DefaultMaxRecipients
	}
	if s.ReadTimeout <= 0 {
		s.ReadTimeout = DefaultReadTimeout
	}
	if s.WriteTimeout <= 0 {
		s.WriteTimeout = DefaultWriteTimeout
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Hostname == "" {
		s.Hostname = "localhost"
	}
	if s.receive == nil {
		s.receive = s.Store.ReceiveMessage
	}
}

// ListenAndServe listens on addr and serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, l)
}

// Serve accepts connections on l until ctx is cancelled. Then it stops
// accepting, gives open connections ShutdownGrace to end, closes the rest
// and waits for messages that are being stored.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	if len(s.Allowed) == 0 {
		_ = l.Close()
		return errors.New("smtp: no allowed networks")
	}
	s.defaults()
	srv := smtp.NewServer(smtp.BackendFunc(s.newSession))
	srv.Domain = s.Hostname
	srv.TLSConfig = s.TLS
	srv.MaxMessageBytes = s.MaxSize
	srv.MaxRecipients = s.MaxRecipients
	srv.MaxLineLength = maxLineLength
	srv.ReadTimeout = s.ReadTimeout
	srv.WriteTimeout = s.WriteTimeout
	srv.EnableSMTPUTF8 = true
	srv.ErrorLog = slogPrinter{s.Logger}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(newGate(l, s.Allowed, s.MaxConns, s.Hostname, s.Logger)) }()
	select {
	case err := <-errc:
		s.inflight.Wait()
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), ShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	<-errc
	s.inflight.Wait()
	return nil
}

func (s *Server) newSession(c *smtp.Conn) (smtp.Session, error) {
	return &session{s: s, conn: c, ip: remoteIP(c.Conn())}, nil
}

// recipient is an accepted RCPT TO.
type recipient struct {
	account  int64
	original string
}

type session struct {
	s     *Server
	conn  *smtp.Conn
	ip    netip.Addr
	from  string
	rcpts []recipient
	raw   []string // RCPT TO as received, for logs
}

func (ss *session) Reset() {
	ss.from, ss.rcpts, ss.raw = "", nil, nil
}

func (ss *session) Logout() error { return nil }

func (ss *session) Mail(from string, _ *smtp.MailOptions) error {
	ss.Reset()
	ss.from = clean(from)
	return nil
}

func (ss *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	to = clean(to)
	base, original := Decode(to)
	addr, err := store.NormalizeSMTPAddress(base)
	if err != nil {
		return ss.refuse(550, smtp.EnhancedCode{5, 1, 1}, "no such address", []string{to}, "invalid address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	route, err := ss.s.Store.LookupSMTPAddress(ctx, addr)
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && route.Removed:
		return ss.refuse(550, smtp.EnhancedCode{5, 1, 1}, "no such address", []string{to}, "no smtp account receives for "+addr)
	case err != nil:
		ss.s.Logger.Error("smtp lookup failed", "client_ip", ss.ip.String(), "recipient", to, "err", err)
		return tempFail()
	case !route.Enabled:
		ss.s.Logger.Warn("smtp recipient deferred", "code", 451, "client_ip", ss.ip.String(), "sender", ss.from,
			"recipient", to, "reason", "the smtp account is disabled")
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 2, 1}, Message: "mailbox disabled, try again later"}
	}
	ss.rcpts = append(ss.rcpts, recipient{account: route.AccountID, original: original})
	ss.raw = append(ss.raw, to)
	return nil
}

func (ss *session) Data(r io.Reader) error {
	ss.s.inflight.Add(1)
	defer ss.s.inflight.Done()
	log := ss.s.Logger.With("client_ip", ss.ip.String(), "sender", ss.from, "recipients", ss.raw)

	blob, created, err := ss.s.Blobs.Put(r)
	if errors.Is(err, smtp.ErrDataTooLarge) {
		log.Error("smtp message refused", "code", 552, "reason", fmt.Sprintf("larger than %d bytes", ss.s.MaxSize))
		return smtp.ErrDataTooLarge
	}
	if errors.Is(err, smtp.ErrTooLongLine) {
		log.Error("smtp message not stored", "code", 451, "reason", fmt.Sprintf("a line is longer than %d bytes", maxLineLength))
		return tempFail()
	}
	if err != nil {
		log.Error("smtp message not stored", "code", 451, "err", err)
		return tempFail()
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	meta, err := archive.BuildMeta(ctx, ss.s.Store, ss.s.Blobs, blob, created)
	if err != nil {
		log.Error("smtp message not stored", "code", 451, "err", err)
		return tempFail()
	}
	env := store.Envelope{
		MailFrom: ss.from, ReceivedAt: ss.s.Now(), ClientIP: ss.ip, HELO: clean(ss.conn.Hostname()),
	}
	// One transaction per account; 250 only after all of them committed.
	// A retry after a partial failure merges into what was stored.
	for _, account := range ss.accounts() {
		env.RcptTo = env.RcptTo[:0]
		for _, rc := range ss.rcpts {
			if rc.account == account {
				env.RcptTo = append(env.RcptTo, rc.original)
			}
		}
		res, err := ss.s.receive(ctx, account, meta, env)
		if err != nil {
			log.Error("smtp message not stored", "code", 451, "account_id", account, "err", err)
			return tempFail()
		}
		log.Info("smtp message received", "account_id", account, "uid", res.UID, "size", blob.Size,
			"new_location", res.New, "added_recipients", res.AddedRecipients)
	}
	return nil
}

// accounts lists the accepted recipients' accounts in order.
func (ss *session) accounts() []int64 {
	var out []int64
	for _, rc := range ss.rcpts {
		seen := false
		for _, a := range out {
			seen = seen || a == rc.account
		}
		if !seen {
			out = append(out, rc.account)
		}
	}
	return out
}

func (ss *session) refuse(code int, enh smtp.EnhancedCode, msg string, rcpts []string, reason string) error {
	ss.s.Logger.Error("smtp recipient refused", "code", code, "client_ip", ss.ip.String(), "sender", ss.from,
		"recipients", rcpts, "reason", reason)
	return &smtp.SMTPError{Code: code, EnhancedCode: enh, Message: msg}
}

func tempFail() error {
	return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "temporary failure, try again later"}
}

// clean drops control characters and bounds the length of a value from
// the client before it is logged or stored.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 512 {
		s = strings.ToValidUTF8(s[:512], "")
	}
	return s
}

// slogPrinter adapts go-smtp's error log.
type slogPrinter struct{ log *slog.Logger }

func (p slogPrinter) Printf(format string, v ...any) {
	p.log.Warn("smtp: " + fmt.Sprintf(format, v...))
}

func (p slogPrinter) Println(v ...any) {
	p.log.Warn("smtp: " + strings.TrimSuffix(fmt.Sprintln(v...), "\n"))
}
