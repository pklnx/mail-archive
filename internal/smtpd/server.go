// Package smtpd receives copies of every message from a mail server over
// SMTP (journaling) and stores them in smtp accounts.
//
// A message is acknowledged with 250 only after it is stored for every
// account it is addressed to; any failure answers 451 and the mail server
// retries. Mail servers drop a BCC copy that gets a 5xx without telling
// anyone, so every 5xx is logged at error level.
//
// With Auth on (#395), a mail server logs in with the SMTP secret of one smtp
// account over STARTTLS, and hands over mail only for that account. Without
// it, the allowlist is the only gate.
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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// Defaults for the limits of a Server.
const (
	DefaultMaxSize  = 50 << 20
	DefaultMaxConns = 16
	// DefaultMaxConnsPerAddr caps the connections of one client address
	// (IPv6: one /64) with Auth.
	DefaultMaxConnsPerAddr = 8
	// DefaultPreAuthTimeout is how long a connection may stay open
	// without logging in, with Auth.
	DefaultPreAuthTimeout = time.Minute
	DefaultMaxRecipients  = 100
	DefaultReadTimeout    = 5 * time.Minute
	DefaultWriteTimeout   = time.Minute
	// ShutdownGrace is how long Serve waits for open connections to end
	// after its context is cancelled.
	ShutdownGrace = 30 * time.Second
	// maxLineLength is far above RFC 5321's 1000: the archive stores what
	// arrives, and a refused line would keep the message queued for good.
	maxLineLength = 1 << 20
	// storeTimeout bounds storing one message after DATA.
	storeTimeout = 2 * time.Minute
	// queryTimeout bounds one lookup of an address or a login.
	queryTimeout = 30 * time.Second
	// maxRefusals is how many refused AUTH and MAIL commands a connection
	// gets before it is closed. A mail server that is set up right gets
	// none; one that is not (no TLS, no or an old secret) gets one per
	// attempt and Postfix then ends the session, so 3 leaves room for a
	// retry of AUTH and a MAIL on the same connection. go-smtp closes after
	// 3 protocol errors as well. Without the cap, pipelined refusals cost
	// a client a few bytes each and the log about 4 times as much.
	maxRefusals = 3
)

// Server receives mail for smtp accounts.
type Server struct {
	Store  *store.Store
	Blobs  *blobstore.Store
	Logger *slog.Logger
	// Allowed lists the networks clients may connect from. Required
	// without Auth; with Auth, empty means any address.
	Allowed []netip.Prefix
	// Hostname is the name in the greeting.
	Hostname string
	// TLS turns STARTTLS on; nil leaves it off. Required with Auth.
	TLS *tls.Config
	// Auth makes clients log in with an SMTP secret (AUTH PLAIN or LOGIN,
	// only after STARTTLS) before MAIL; a login hands over mail only for
	// its own smtp account.
	Auth bool
	// Limiter counts failed logins per client address (auth.LimitKey);
	// nil: a new one. Not the web server's.
	Limiter *auth.Limiter
	// MaxConnsPerAddr caps the connections of one client address with
	// Auth (default DefaultMaxConnsPerAddr); without Auth there is none.
	MaxConnsPerAddr int
	// PreAuthTimeout is how long a connection may stay open without a
	// login, with Auth (default DefaultPreAuthTimeout).
	PreAuthTimeout time.Duration
	MaxSize        int64
	MaxConns       int
	MaxRecipients  int
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	// Now is the clock of envelopes and of the expiry of SMTP secrets.
	Now func() time.Time

	// slotFreed, if set, is called after a connection's slot (MaxConns)
	// is free again; tests set it.
	slotFreed func()
	// logins checks SMTP secrets; tests replace it.
	logins loginStore
	// refusalLimit is maxRefusals; tests raise it.
	refusalLimit int
	// afterFunc starts the pre-auth timer; tests replace it.
	afterFunc func(time.Duration, func()) (stop func() bool)
	// receive stores a message for one account; tests replace it.
	receive func(ctx context.Context, account int64, meta store.MessageMeta, env store.Envelope, fileExists func() bool) (store.ReceiveResult, error)
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
	if s.logins == nil {
		s.logins = s.Store
	}
	if s.refusalLimit <= 0 {
		s.refusalLimit = maxRefusals
	}
	if s.Limiter == nil {
		s.Limiter = auth.NewLimiter()
	}
	if s.MaxConnsPerAddr <= 0 {
		s.MaxConnsPerAddr = DefaultMaxConnsPerAddr
	}
	if s.PreAuthTimeout <= 0 {
		s.PreAuthTimeout = DefaultPreAuthTimeout
	}
	if s.afterFunc == nil {
		s.afterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
}

// loginStore is what the server asks the store about SMTP secrets.
type loginStore interface {
	SMTPLogin(ctx context.Context, hash []byte, now time.Time) (*store.SMTPLogin, error)
	SMTPCredentialUsable(ctx context.Context, id int64, now time.Time) error
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
	if len(s.Allowed) == 0 && !s.Auth {
		_ = l.Close()
		return errors.New("smtp: no allowed networks")
	}
	if s.Auth && s.TLS == nil {
		_ = l.Close()
		return errors.New("smtp: authentication needs TLS")
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
	g := newGate(l, s.Allowed, s.MaxConns, s.Hostname, s.Logger)
	g.slotFreed = s.slotFreed
	if s.Auth {
		g.perAddr, g.preAuth, g.afterFunc = s.MaxConnsPerAddr, s.PreAuthTimeout, s.afterFunc
	}
	go func() { errc <- srv.Serve(g) }()
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
	s    *Server
	conn *smtp.Conn
	ip   netip.Addr
	// cred and account are the login of AUTH (0 without one). Reset keeps
	// them: a mail server sends many messages per login. STARTTLS starts a
	// new session, which drops them.
	cred    int64
	account int64
	from    string
	rcpts   []recipient
	raw     []string // RCPT TO as received, for logs
}

func (ss *session) Reset() {
	ss.from, ss.rcpts, ss.raw = "", nil, nil
}

func (ss *session) Logout() error { return nil }

// tls reports whether the connection runs over TLS.
func (ss *session) tls() bool {
	if ss.conn == nil {
		return false
	}
	_, ok := ss.conn.TLSConnectionState()
	return ok
}

// Mail starts a message. With Auth it checks first, in this order, and
// answers 451 so that the mail server keeps the copy queued (a 5xx would
// drop it without a notice): TLS, a login, and that the login's secret
// still works now (revoked or expired since AUTH).
func (ss *session) Mail(from string, _ *smtp.MailOptions) error {
	ss.Reset()
	if ss.s.Auth {
		if ss.closing() {
			return errMailRefused
		}
		if err := ss.checkLogin(); err != nil {
			return err
		}
	}
	ss.from = clean(from)
	return nil
}

func (ss *session) checkLogin() error {
	if !ss.tls() {
		return ss.refuseMail("STARTTLS required", "no_tls")
	}
	if ss.cred == 0 {
		return ss.refuseMail("authentication required", "no_auth")
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	err := ss.s.logins.SMTPCredentialUsable(ctx, ss.cred, ss.s.Now())
	if reason := loginRefusal(err); reason != "" {
		return ss.refuseMail("authentication required", reason)
	}
	if err != nil {
		ss.s.Logger.Error("smtp login check failed", "client_ip", ss.ip.String(), "credential_id", ss.cred, "err", err)
		return tempFail()
	}
	return nil
}

func (ss *session) refuseMail(msg, reason string) error {
	if ss.refused() {
		ss.s.Logger.Warn("smtp mail refused", "code", 451, "client_ip", ss.ip.String(), "reason", reason)
	}
	return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: msg}
}

// errMailRefused answers MAIL on a connection that is being closed.
var errMailRefused = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: "authentication required"}

// refused counts a refused AUTH or MAIL of the connection and reports
// whether to log it. The refusal that reaches the limit closes the
// connection once its answer is written; commands the client pipelined
// behind it are answered without a log line or a lookup.
func (ss *session) refused() bool {
	g := ss.gate()
	if g == nil {
		return true
	}
	n := int(g.refusals.Add(1))
	if n == ss.s.refusalLimit {
		g.closeAfterWrite.Store(true)
		ss.s.Logger.Warn("smtp connection closed", "client_ip", ss.ip.String(), "reason", fmt.Sprintf("%d refused commands", n))
	}
	return n <= ss.s.refusalLimit
}

// closing reports whether the connection reached the refusal limit.
func (ss *session) closing() bool {
	g := ss.gate()
	return g != nil && int(g.refusals.Load()) >= ss.s.refusalLimit
}

// gate is the connection's gatedConn, also below TLS; nil without one.
func (ss *session) gate() *gatedConn {
	if ss.conn == nil {
		return nil
	}
	return gated(ss.conn.Conn())
}

func (ss *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	to = clean(to)
	rc, err := ss.checkRcpt(to)
	if err != nil {
		return err
	}
	ss.rcpts = append(ss.rcpts, rc)
	ss.raw = append(ss.raw, to)
	return nil
}

// checkRcpt decides a RCPT TO (cleaned), in this order:
//
//	a. with Auth, TLS and a login again (Mail checked them): 451 4.7.0
//	b. no smtp account receives for the address, or it was removed:
//	   550 5.1.1
//	c. with Auth, the address belongs to another smtp account than the
//	   login: 451 4.7.1, so that a message for two accounts stays queued
//	   for the other account's login instead of losing that copy
//	d. the account is disabled: 451 4.2.1
//
// A quota check (#397) belongs after d.
func (ss *session) checkRcpt(to string) (recipient, error) {
	if ss.s.Auth && (!ss.tls() || ss.cred == 0) {
		ss.s.Logger.Warn("smtp recipient deferred", "code", 451, "client_ip", ss.ip.String(), "reason", "no_auth")
		return recipient{}, &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: "authentication required"}
	}
	base, original := Decode(to)
	addr, err := store.NormalizeSMTPAddress(base)
	if err != nil {
		return recipient{}, ss.refuse(550, smtp.EnhancedCode{5, 1, 1}, "no such address", []string{to}, "invalid address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	route, err := ss.s.Store.LookupSMTPAddress(ctx, addr)
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && route.Removed:
		return recipient{}, ss.refuse(550, smtp.EnhancedCode{5, 1, 1}, "no such address", []string{to}, "no smtp account receives for "+addr)
	case err != nil:
		ss.s.Logger.Error("smtp lookup failed", "client_ip", ss.ip.String(), "recipient", to, "err", err)
		return recipient{}, tempFail()
	case ss.s.Auth && route.AccountID != ss.account:
		// Error: Postfix keeps the copy until maximal_queue_lifetime and
		// then drops it. Never the address: only the operator's log.
		ss.s.Logger.Error("smtp recipient deferred", "code", 451, "client_ip", ss.ip.String(),
			"reason", "the address belongs to another smtp account than the login",
			"login_account_id", ss.account, "recipient_account_id", route.AccountID)
		return recipient{}, &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1},
			Message: "not accepted with this login, try again later"}
	case !route.Enabled:
		ss.s.Logger.Warn("smtp recipient deferred", "code", 451, "client_ip", ss.ip.String(), "sender", ss.from,
			"recipient", to, "reason", "the smtp account is disabled")
		return recipient{}, &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 2, 1}, Message: "mailbox disabled, try again later"}
	}
	// Quota (#397): here, after the account checks.
	return recipient{account: route.AccountID, original: original}, nil
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
	accounts := ss.accounts()
	// With Auth, checkRcpt accepted only the login's account; store for no
	// other.
	if ss.s.Auth && slices.ContainsFunc(accounts, func(a int64) bool { return a != ss.account }) {
		log.Error("smtp message not stored", "code", 451, "reason", "a recipient of another smtp account than the login")
		return tempFail()
	}
	// One transaction per account; 250 only after all of them committed.
	// A retry after a partial failure merges into what was stored.
	for _, account := range accounts {
		env.RcptTo = env.RcptTo[:0]
		for _, rc := range ss.rcpts {
			if rc.account == account {
				env.RcptTo = append(env.RcptTo, rc.original)
			}
		}
		res, err := ss.s.receive(ctx, account, meta, env, func() bool { return ss.s.Blobs.Exists(blob.SHA256) })
		if err != nil {
			log.Error("smtp message not stored", "code", 451, "account_id", account, "err", err)
			return tempFail()
		}
		if res.Skipped {
			// 250 all the same: a 5xx would make the mail server drop it
			// silently anyway, and a 4xx would retry it for days.
			log.Info("smtp message deleted by retention, not archived again", "account_id", account, "sha256", blob.SHA256)
			continue
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
