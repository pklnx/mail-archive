package smtpd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/netip"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// fakeClock is the server's and the limiter's clock in the AUTH tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// countingLogins is the store seam: it counts secret lookups and can fail
// the next one.
type countingLogins struct {
	st       *store.Store
	lookups  atomic.Int64
	failNext atomic.Bool
}

func (c *countingLogins) SMTPLogin(ctx context.Context, hash []byte, now time.Time) (*store.SMTPLogin, error) {
	c.lookups.Add(1)
	if c.failNext.Swap(false) {
		return nil, errors.New("database unreachable")
	}
	return c.st.SMTPLogin(ctx, hash, now)
}

func (c *countingLogins) SMTPCredentialUsable(ctx context.Context, id int64, now time.Time) error {
	return c.st.SMTPCredentialUsable(ctx, id, now)
}

// fakeTimers replaces the pre-auth timers; a test fires them itself.
type fakeTimers struct {
	mu   sync.Mutex
	list []*fakeTimer
}

type fakeTimer struct {
	f       func()
	stopped bool
	fired   bool
}

func (ft *fakeTimers) after(_ time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	t := &fakeTimer{f: f}
	ft.list = append(ft.list, t)
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		pending := !t.stopped && !t.fired
		t.stopped = true
		return pending
	}
}

// timer returns the timer of the i-th connection and whether it was
// stopped.
func (ft *fakeTimers) timer(t *testing.T, i int) (*fakeTimer, bool) {
	t.Helper()
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if i >= len(ft.list) {
		t.Fatalf("%d timers, want connection %d", len(ft.list), i)
	}
	return ft.list[i], ft.list[i].stopped
}

// fire runs the i-th timer unless it was stopped.
func (ft *fakeTimers) fire(t *testing.T, i int) bool {
	t.Helper()
	tm, stopped := ft.timer(t, i)
	if stopped {
		return false
	}
	ft.mu.Lock()
	tm.fired = true
	ft.mu.Unlock()
	tm.f()
	return true
}

// testCert is a self-signed ECDSA P-256 certificate for archive.test and
// 127.0.0.1, valid from notBefore to notAfter.
func testCert(t *testing.T, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "archive.test"},
		NotBefore: notBefore, NotAfter: notAfter,
		DNSNames: []string{"archive.test"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
}

// tlsPair returns a server config and the client's roots for a new test
// certificate.
func tlsPair(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	certPEM, keyPEM := testCert(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, pool
}

type authFixture struct {
	*fixture
	clock  *fakeClock
	logins *countingLogins
	timers *fakeTimers
	roots  *x509.CertPool
}

// newAuthFixture starts a server with Auth (required mode) and no
// allowlist; configure runs last.
func newAuthFixture(t *testing.T, configure func(*Server)) *authFixture {
	t.Helper()
	serverTLS, roots := tlsPair(t)
	a := &authFixture{
		clock: &fakeClock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}, timers: &fakeTimers{}, roots: roots,
	}
	a.fixture = newFixture(t, func(s *Server) {
		s.Auth, s.Allowed, s.TLS = true, nil, serverTLS
		s.Now = a.clock.Now
		s.Limiter = auth.NewLimiter().WithClock(a.clock.Now)
		a.logins = &countingLogins{st: s.Store}
		s.logins = a.logins
		s.afterFunc = a.timers.after
		if configure != nil {
			configure(s)
		}
	})
	return a
}

// secret creates a secret for account with the given grace for the one
// before it, and returns it with its ID.
func (a *authFixture) secret(acc *store.Account, grace time.Duration) (string, int64) {
	a.t.Helper()
	ctx := context.Background()
	cur, err := a.st.GetAccount(ctx, acc.ID)
	if err != nil {
		a.t.Fatal(err)
	}
	secret, hash, hint, err := auth.NewSMTPSecret()
	if err != nil {
		a.t.Fatal(err)
	}
	c, _, err := a.st.CreateSMTPCredential(ctx, cur.Ref(), store.NewSMTPCredential{Hash: hash, Hint: hint, Grace: grace, Now: a.clock.Now()})
	if err != nil {
		a.t.Fatal(err)
	}
	return secret, c.ID
}

// rawClient speaks SMTP line by line, so that a test sees every answer.
type rawClient struct {
	t    *testing.T
	conn net.Conn
	text *textproto.Conn
}

// dial connects and reads the greeting; it returns the greeting's code.
func (f *fixture) raw() (*rawClient, int) {
	f.t.Helper()
	conn, err := net.Dial("tcp", f.addr)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = conn.Close() })
	c := &rawClient{t: f.t, conn: conn, text: textproto.NewConn(conn)}
	code, _, err := c.text.ReadResponse(0)
	if err != nil {
		return c, 0
	}
	return c, code
}

// cmd sends one line and returns the answer.
func (c *rawClient) cmd(format string, args ...any) (int, string) {
	c.t.Helper()
	if err := c.text.PrintfLine(format, args...); err != nil {
		return 0, err.Error()
	}
	code, msg, err := c.text.ReadResponse(0)
	if err != nil && code == 0 {
		return 0, err.Error()
	}
	return code, msg
}

func (c *rawClient) must(want int, format string, args ...any) string {
	c.t.Helper()
	code, msg := c.cmd(format, args...)
	if code != want {
		c.t.Fatalf("%s: %d %s, want %d", strings.Fields(fmt.Sprintf(format, args...))[0], code, msg, want)
	}
	return msg
}

// startTLS runs STARTTLS and a new EHLO; it returns the EHLO's lines.
func (c *rawClient) startTLS(roots *x509.CertPool) string {
	c.t.Helper()
	c.must(250, "EHLO mx.example.com")
	c.must(220, "STARTTLS")
	tc := tls.Client(c.conn, &tls.Config{RootCAs: roots, ServerName: "archive.test", MinVersion: tls.VersionTLS12})
	if err := tc.Handshake(); err != nil {
		c.t.Fatal(err)
	}
	c.conn, c.text = tc, textproto.NewConn(tc)
	return c.must(250, "EHLO mx.example.com")
}

func plain(authzid, user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(authzid + "\x00" + user + "\x00" + pass))
}

// login connects over TLS and logs in with PLAIN; it returns the AUTH
// answer's code.
func (a *authFixture) login(user, pass string) (*rawClient, int) {
	a.t.Helper()
	c, _ := a.raw()
	c.startTLS(a.roots)
	code, _ := c.cmd("AUTH PLAIN %s", plain("", user, pass))
	return c, code
}

// deliver sends one message and returns the code and text of the first
// refusal, MAIL to the end of DATA; 250 and "" when it was stored. A
// refused RCPT does not end the transaction while another was accepted.
func (c *rawClient) deliver(from string, rcpts []string, body string) (int, string) {
	c.t.Helper()
	if code, msg := c.cmd("MAIL FROM:<%s>", from); code != 250 {
		return code, msg
	}
	accepted, firstCode, firstMsg := 0, 250, ""
	for _, r := range rcpts {
		code, msg := c.cmd("RCPT TO:<%s>", r)
		if code == 250 {
			accepted++
		} else if firstCode == 250 {
			firstCode, firstMsg = code, msg
		}
	}
	if accepted == 0 {
		c.must(250, "RSET")
		return firstCode, firstMsg
	}
	if code, msg := c.cmd("DATA"); code != 354 {
		return code, msg
	}
	w := c.text.DotWriter()
	if _, err := w.Write([]byte(body)); err != nil {
		c.t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		c.t.Fatal(err)
	}
	code, msg, err := c.text.ReadResponse(0)
	if err != nil && code == 0 {
		c.t.Fatal(err)
	}
	if code != 250 {
		return code, msg
	}
	return firstCode, firstMsg
}

// logLacks fails if the log contains any of the strings.
func (f *fixture) logLacks(what ...string) {
	f.t.Helper()
	logs := f.logs.String()
	for _, w := range what {
		if w != "" && strings.Contains(logs, w) {
			f.t.Errorf("log contains %q:\n%s", w, logs)
		}
	}
}

// logHas fails unless the log contains every string.
func (f *fixture) logHas(what ...string) {
	f.t.Helper()
	logs := f.logs.String()
	for _, w := range what {
		if !strings.Contains(logs, w) {
			f.t.Errorf("log lacks %q:\n%s", w, logs)
		}
	}
}

// AUTH PLAIN and LOGIN after STARTTLS, then a message is stored; the log
// names the user name but never the secret.
func TestAuthSuccess(t *testing.T) {
	f := newAuthFixture(t, nil)
	secret, id := f.secret(f.journal, 0)
	user := auth.SMTPUsername(f.journal.ID)

	c, code := f.login(user, secret)
	if code != 235 {
		t.Fatalf("AUTH PLAIN: %d", code)
	}
	if code, msg := c.deliver("sender@example.com", []string{"archive+alice=example.com@archive.test"}, message("plain")); code != 250 {
		t.Fatalf("delivery after PLAIN: %d %s", code, msg)
	}

	// LOGIN without and with the user name on the AUTH line; an
	// authorization identity equal to the user name works for PLAIN.
	b64 := base64.StdEncoding.EncodeToString
	c2, _ := f.raw()
	c2.startTLS(f.roots)
	if msg := c2.must(334, "AUTH LOGIN"); msg != b64([]byte("Username:")) {
		t.Errorf("first challenge %q", msg)
	}
	if msg := c2.must(334, "%s", b64([]byte(user))); msg != b64([]byte("Password:")) {
		t.Errorf("second challenge %q", msg)
	}
	c2.must(235, "%s", b64([]byte(secret)))
	if code, msg := c2.deliver("sender@example.com", []string{"archive+bob=example.com@archive.test"}, message("login")); code != 250 {
		t.Fatalf("delivery after LOGIN: %d %s", code, msg)
	}
	c3, _ := f.raw()
	c3.startTLS(f.roots)
	c3.must(334, "AUTH LOGIN %s", b64([]byte(user)))
	c3.must(235, "%s", b64([]byte(secret)))
	c4, _ := f.raw()
	c4.startTLS(f.roots)
	c4.must(235, "AUTH PLAIN %s", plain(user, user, secret))

	if got := f.envelopes("plain"); !slices.Equal(got["journal"], []string{"alice@example.com"}) {
		t.Errorf("envelopes %v", got)
	}
	if got := f.envelopes("login"); !slices.Equal(got["journal"], []string{"bob@example.com"}) {
		t.Errorf("envelopes %v", got)
	}
	f.logHas(
		fmt.Sprintf(`level=INFO msg="smtp auth" client_ip=127.0.0.1 mechanism=PLAIN username=%s credential_id=%d`, user, id),
		fmt.Sprintf(`level=INFO msg="smtp auth" client_ip=127.0.0.1 mechanism=LOGIN username=%s credential_id=%d`, user, id),
	)
	f.logLacks(secret, secret[len(secret)-8:], hex.EncodeToString(auth.HashSMTPSecret(secret)), "level=ERROR")
}

// Before STARTTLS, EHLO offers no AUTH, AUTH answers 523 and MAIL 451; after
// it without a login, MAIL answers 451 too. Both refusals are logged.
func TestAuthRequired(t *testing.T) {
	f := newAuthFixture(t, nil)
	secret, _ := f.secret(f.journal, 0)
	user := auth.SMTPUsername(f.journal.ID)

	c, code := f.raw()
	if code != 220 {
		t.Fatalf("greeting %d", code)
	}
	ehlo := c.must(250, "EHLO mx.example.com")
	if strings.Contains(ehlo, "AUTH") || !strings.Contains(ehlo, "STARTTLS") {
		t.Errorf("EHLO before STARTTLS:\n%s", ehlo)
	}
	c.must(523, "AUTH PLAIN %s", plain("", user, secret))
	if msg := c.must(451, "MAIL FROM:<sender@example.com>"); !strings.Contains(msg, "STARTTLS required") {
		t.Errorf("MAIL without TLS: %s", msg)
	}
	c.must(502, "RCPT TO:<archive@archive.test>") // go-smtp: no MAIL

	ehlo = c.startTLS(f.roots)
	if !strings.Contains(ehlo, "AUTH PLAIN LOGIN") {
		t.Errorf("EHLO after STARTTLS:\n%s", ehlo)
	}
	if msg := c.must(451, "MAIL FROM:<sender@example.com>"); !strings.Contains(msg, "authentication required") {
		t.Errorf("MAIL without AUTH: %s", msg)
	}
	f.logHas(
		`level=WARN msg="smtp mail refused" code=451 client_ip=127.0.0.1 reason=no_tls`,
		`level=WARN msg="smtp mail refused" code=451 client_ip=127.0.0.1 reason=no_auth`,
	)
	if n := f.logins.lookups.Load(); n != 0 {
		t.Errorf("%d secret lookups without AUTH", n)
	}
}

// Every refused login answers 535 and is logged with its reason, without
// the password, its hash or a user name that is not one.
func TestAuthFailures(t *testing.T) {
	f := newAuthFixture(t, nil)
	other := f.account("other", "other@archive.test")
	secret, _ := f.secret(f.journal, 0)
	otherSecret, _ := f.secret(other, 0)
	user := auth.SMTPUsername(f.journal.ID)
	unknown, _, _, err := auth.NewSMTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	const marker = "Marker-Mailbox-Password-7f3a"
	markerHash := sha256.Sum256([]byte(marker))
	revoked, revokedID := f.secret(f.journal, time.Hour) // secret becomes the expiring one
	if _, err := f.st.RevokeSMTPCredential(context.Background(), nil, revokedID, nil, f.clock.Now()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, user, pass, reason, logUser string
	}{
		{"unknown secret", user, unknown, "unknown", "username=" + user},
		{"another account's user name", auth.SMTPUsername(other.ID), secret, "wrong_user", "username=" + auth.SMTPUsername(other.ID)},
		{"user name of no account", "smtp-999999", otherSecret, "wrong_user", "username=smtp-999999"},
		{"secret as user name", unknown, secret, "wrong_user", "username_valid=false"},
		{"marker as user name", marker, secret, "wrong_user", "username_valid=false"},
		{"mailbox password", user, marker, "malformed", "username=" + user},
		{"revoked", user, revoked, "revoked", "username=" + user},
	} {
		_, code := f.login(tc.user, tc.pass)
		if code != 535 {
			t.Errorf("%s: %d, want 535", tc.name, code)
		}
		f.logHas(fmt.Sprintf(`level=WARN msg="smtp auth failed" client_ip=127.0.0.1 mechanism=PLAIN %s reason=%s`, tc.logUser, tc.reason))
	}
	// A mismatched authorization identity and a broken PLAIN answer.
	c, _ := f.raw()
	c.startTLS(f.roots)
	c.must(535, "AUTH PLAIN %s", plain(auth.SMTPUsername(other.ID), user, secret))
	c.must(535, "AUTH PLAIN %s", base64.StdEncoding.EncodeToString([]byte("no separators")))
	c.must(535, "AUTH LOGIN %s", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", maxSASLField+1))))
	if n := strings.Count(f.logs.String(), "reason=malformed"); n != 4 {
		t.Errorf("%d malformed logins, want 4", n)
	}
	// A password in the wrong format is never looked up: 6 lookups for the
	// 10 logins, 4 of them malformed.
	if n := f.logins.lookups.Load(); n != 6 {
		t.Errorf("%d lookups, want 6", n)
	}
	f.logLacks(marker, hex.EncodeToString(markerHash[:]), secret, otherSecret, unknown, revoked,
		base64.StdEncoding.EncodeToString(markerHash[:]), "level=ERROR")
}

// A rotated secret works with the new one during the grace period and is
// refused once the clock passes its end.
func TestAuthRotation(t *testing.T) {
	f := newAuthFixture(t, nil)
	user := auth.SMTPUsername(f.journal.ID)
	old, _ := f.secret(f.journal, 0)
	current, _ := f.secret(f.journal, 24*time.Hour)
	for _, s := range []string{old, current} {
		if _, code := f.login(user, s); code != 235 {
			t.Fatalf("during the grace: %d", code)
		}
	}
	f.clock.Add(24*time.Hour + time.Second)
	if _, code := f.login(user, old); code != 535 {
		t.Errorf("old secret after the grace: %d", code)
	}
	if _, code := f.login(user, current); code != 235 {
		t.Errorf("current secret after the grace: %d", code)
	}
	f.logHas(`reason=expired`)
}

// A secret revoked while the connection is logged in is refused at the
// next MAIL with 451; the connection's earlier message was stored.
func TestAuthRevokedDuringConnection(t *testing.T) {
	f := newAuthFixture(t, nil)
	secret, id := f.secret(f.journal, 0)
	c, code := f.login(auth.SMTPUsername(f.journal.ID), secret)
	if code != 235 {
		t.Fatalf("AUTH: %d", code)
	}
	if code, msg := c.deliver("sender@example.com", []string{"archive@archive.test"}, message("before")); code != 250 {
		t.Fatalf("before the revocation: %d %s", code, msg)
	}
	// As an admin does it (part 1's admin route revokes the same way).
	if _, err := f.st.RevokeSMTPCredential(context.Background(), nil, id, nil, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if code, msg := c.deliver("sender@example.com", []string{"archive@archive.test"}, message("after")); code != 451 || !strings.Contains(msg, "authentication required") {
		t.Errorf("after the revocation: %d %s", code, msg)
	}
	f.logHas(`level=WARN msg="smtp mail refused" code=451 client_ip=127.0.0.1 reason=revoked`)
}

// The login survives RSET and a second EHLO: a mail server sends many
// messages per connection without logging in again.
func TestAuthSurvivesReset(t *testing.T) {
	f := newAuthFixture(t, nil)
	secret, _ := f.secret(f.journal, 0)
	c, code := f.login(auth.SMTPUsername(f.journal.ID), secret)
	if code != 235 {
		t.Fatalf("AUTH: %d", code)
	}
	if code, msg := c.deliver("sender@example.com", []string{"archive+a=example.com@archive.test"}, message("first")); code != 250 {
		t.Fatalf("first: %d %s", code, msg)
	}
	c.must(250, "RSET")
	c.must(250, "EHLO mx.example.com")
	if code, msg := c.deliver("sender@example.com", []string{"archive+b=example.com@archive.test"}, message("second")); code != 250 {
		t.Fatalf("second without a new AUTH: %d %s", code, msg)
	}
	c.must(503, "AUTH PLAIN %s", plain("", auth.SMTPUsername(f.journal.ID), secret))
	if got := f.envelopes("second"); !slices.Equal(got["journal"], []string{"b@example.com"}) {
		t.Errorf("envelopes %v", got)
	}
}

// A login hands over mail only for its own account. A message for two
// accounts gets 250 for the login's address and 451 for the other one, so
// the mail server keeps that copy for the other account's login; nothing
// is stored for the other account until then.
func TestAuthAccountMismatch(t *testing.T) {
	f := newAuthFixture(t, nil)
	other := f.account("other", "other@archive.test")
	secret, _ := f.secret(f.journal, 0)
	otherSecret, _ := f.secret(other, 0)

	c, _ := f.login(auth.SMTPUsername(f.journal.ID), secret)
	rcpts := []string{"archive+alice=example.com@archive.test", "other+alice=example.com@archive.test"}
	code, msg := c.deliver("sender@example.com", rcpts, message("numbat"))
	if code != 451 || !strings.Contains(msg, "not accepted with this login") {
		t.Fatalf("other account's address: %d %s", code, msg)
	}
	if got := f.envelopes("numbat"); len(got) != 1 || !slices.Equal(got["journal"], []string{"alice@example.com"}) {
		t.Fatalf("with the journal login: %v", got)
	}
	f.logHas(fmt.Sprintf(`level=ERROR msg="smtp recipient deferred" code=451 client_ip=127.0.0.1 reason="the address belongs to another smtp account than the login" login_account_id=%d recipient_account_id=%d`,
		f.journal.ID, other.ID))
	f.logLacks("other+alice", "other@archive.test")

	// The mail server retries the other copy with the other login.
	c2, _ := f.login(auth.SMTPUsername(other.ID), otherSecret)
	if code, msg := c2.deliver("sender@example.com", rcpts[1:], message("numbat")); code != 250 {
		t.Fatalf("with the other login: %d %s", code, msg)
	}
	if got := f.envelopes("numbat"); !slices.Equal(got["other"], []string{"alice@example.com"}) {
		t.Fatalf("after the retry: %v", got)
	}
	// An unknown address still answers 550, as without AUTH.
	if code, _ := c2.deliver("sender@example.com", []string{"nobody@archive.test"}, message("koala")); code != 550 {
		t.Errorf("unknown address: %d", code)
	}
}

// The account checks come in order: the mismatch before the disabled
// check, so another account's disabled address answers 4.7.1 and the
// login's own disabled account 4.2.1.
func TestAuthRcptOrder(t *testing.T) {
	f := newAuthFixture(t, nil)
	other := f.account("other", "other@archive.test")
	secret, _ := f.secret(f.journal, 0)
	ctx := context.Background()
	off := false
	for _, a := range []*store.Account{f.journal, other} {
		cur, err := f.st.GetAccount(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.UpdateAccount(ctx, cur.Ref(), store.AccountChange{Enabled: &off}); err != nil {
			t.Fatal(err)
		}
	}
	// A disabled account still logs in: its copies stay queued.
	c, code := f.login(auth.SMTPUsername(f.journal.ID), secret)
	if code != 235 {
		t.Fatalf("AUTH of a disabled account: %d", code)
	}
	c.must(250, "MAIL FROM:<sender@example.com>")
	if msg := c.must(451, "RCPT TO:<other@archive.test>"); !strings.HasPrefix(msg, "4.7.1") {
		t.Errorf("another account, disabled: %s", msg)
	}
	if msg := c.must(451, "RCPT TO:<archive@archive.test>"); !strings.HasPrefix(msg, "4.2.1") {
		t.Errorf("own account, disabled: %s", msg)
	}
	f.logHas(`reason="the address belongs to another smtp account than the login"`, `reason="the smtp account is disabled"`)
}

// Failed logins count per client address: the 21st attempt answers 454
// even with the right secret, without looking it up, until the block ends.
// A database error answers 454 and counts nothing.
func TestAuthRateLimit(t *testing.T) {
	f := newAuthFixture(t, nil)
	secret, _ := f.secret(f.journal, 0)
	user := auth.SMTPUsername(f.journal.ID)
	wrong, _, _, err := auth.NewSMTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	c, _ := f.raw()
	c.startTLS(f.roots)

	f.logins.failNext.Store(true)
	if msg := c.must(454, "AUTH PLAIN %s", plain("", user, secret)); !strings.Contains(msg, "temporary authentication failure") {
		t.Errorf("database error: %s", msg)
	}
	f.logHas(`level=ERROR msg="smtp auth not checked" client_ip=127.0.0.1 mechanism=PLAIN username=` + user + ` err="database unreachable"`)
	// 19 failures leave one: had the database error counted, this would
	// be the 20th and the next login blocked.
	for range 19 {
		c.must(535, "AUTH PLAIN %s", plain("", user, wrong))
	}
	c.must(235, "AUTH PLAIN %s", plain("", user, secret))

	c2, _ := f.raw()
	c2.startTLS(f.roots)
	c2.must(535, "AUTH PLAIN %s", plain("", user, wrong)) // the 20th failure blocks
	before := f.logins.lookups.Load()
	if msg := c2.must(454, "AUTH PLAIN %s", plain("", user, secret)); !strings.Contains(msg, "too many failed logins") {
		t.Errorf("blocked: %s", msg)
	}
	c2.must(454, "AUTH LOGIN")
	if n := f.logins.lookups.Load(); n != before {
		t.Errorf("%d lookups while blocked", n-before)
	}
	f.logHas(`level=WARN msg="smtp auth blocked" client_ip=127.0.0.1 mechanism=PLAIN until=2026-11-12T10:15:00.000Z`,
		`level=WARN msg="smtp auth blocked" client_ip=127.0.0.1 mechanism=LOGIN until=2026-11-12T10:15:00.000Z`)

	f.clock.Add(15 * time.Minute)
	c2.must(235, "AUTH PLAIN %s", plain("", user, secret))
	f.logLacks(secret, wrong)
}

// A connection that has not logged in when its timer fires is closed; a
// login stops the timer.
func TestAuthPreAuthDeadline(t *testing.T) {
	var freed testwait.Signal
	var frees atomic.Int32
	f := newAuthFixture(t, func(s *Server) {
		s.slotFreed = func() { frees.Add(1); freed.Notify() }
	})
	secret, _ := f.secret(f.journal, 0)

	idle, _ := f.raw()
	idle.must(250, "EHLO mx.example.com")
	authed, _ := f.login(auth.SMTPUsername(f.journal.ID), secret)
	if _, stopped := f.timers.timer(t, 1); !stopped {
		t.Error("the login did not stop its timer")
	}
	if !f.timers.fire(t, 0) {
		t.Fatal("the idle connection's timer was stopped")
	}
	// The server closed the idle connection: the next read ends.
	if code, msg := idle.cmd("NOOP"); code != 0 {
		t.Errorf("idle connection still answers: %d %s", code, msg)
	}
	testwait.Until(t, &freed, "the idle connection's slot", func() bool { return frees.Load() >= 1 })
	f.logHas(`level=WARN msg="smtp connection closed" client_ip=127.0.0.1 reason="no login within 1m0s"`)
	if f.timers.fire(t, 1) {
		t.Error("the logged-in connection's timer fired")
	}
	authed.must(250, "NOOP")
}

// One client address gets at most MaxConnsPerAddr connections (default 8);
// more get 421 until one ends.
func TestAuthConnsPerAddr(t *testing.T) {
	var freed testwait.Signal
	var frees atomic.Int32
	f := newAuthFixture(t, func(s *Server) {
		s.slotFreed = func() { frees.Add(1); freed.Notify() }
	})
	var open []*rawClient
	for i := range DefaultMaxConnsPerAddr {
		c, code := f.raw()
		if code != 220 {
			t.Fatalf("connection %d: %d", i+1, code)
		}
		open = append(open, c)
	}
	if _, code := f.raw(); code != 421 {
		t.Fatalf("connection %d: %d, want 421", DefaultMaxConnsPerAddr+1, code)
	}
	f.logHas(`level=WARN msg="smtp connection refused" code=421 client_ip=127.0.0.1 reason="too many connections from this address"`)
	open[0].must(221, "QUIT")
	testwait.Until(t, &freed, "a slot", func() bool { return frees.Load() >= 1 })
	if _, code := f.raw(); code != 220 {
		t.Fatalf("after one ended: %d", code)
	}

	// Without Auth there is no cap per address, only the total.
	g := newFixture(t, func(s *Server) { s.MaxConns = DefaultMaxConnsPerAddr + 2 })
	for i := range DefaultMaxConnsPerAddr + 1 {
		if _, code := g.raw(); code != 220 {
			t.Fatalf("without Auth, connection %d: %d", i+1, code)
		}
	}
}

// With Auth, an allowlist still filters before the greeting.
func TestAuthWithAllowlist(t *testing.T) {
	f := newAuthFixture(t, func(s *Server) { s.Allowed = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")} })
	if _, code := f.raw(); code != 554 {
		t.Errorf("outside the allowlist: %d", code)
	}
	g := newAuthFixture(t, func(s *Server) { s.Allowed = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")} })
	secret, _ := g.secret(g.journal, 0)
	if _, code := g.login(auth.SMTPUsername(g.journal.ID), secret); code != 235 {
		t.Errorf("inside the allowlist: %d", code)
	}
}

// Without Auth (off), AUTH is never offered: 523 before STARTTLS (go-smtp),
// 504 after it, and mail goes through without a login as before.
func TestAuthOff(t *testing.T) {
	serverTLS, roots := tlsPair(t)
	f := newFixture(t, func(s *Server) { s.TLS = serverTLS })
	c, _ := f.raw()
	if ehlo := c.must(250, "EHLO mx.example.com"); strings.Contains(ehlo, "AUTH") {
		t.Errorf("EHLO before STARTTLS:\n%s", ehlo)
	}
	c.must(523, "AUTH PLAIN %s", plain("", "smtp-1", "x"))
	if ehlo := c.startTLS(roots); strings.Contains(ehlo, "AUTH") {
		t.Errorf("EHLO after STARTTLS:\n%s", ehlo)
	}
	c.must(504, "AUTH PLAIN %s", plain("", "smtp-1", "x"))
	if code, msg := c.deliver("sender@example.com", []string{"archive@archive.test"}, message("dingo")); code != 250 {
		t.Errorf("delivery without a login: %d %s", code, msg)
	}
}

// Serve refuses Auth without TLS, and no allowlist without Auth.
func TestServeAuthSettings(t *testing.T) {
	for _, s := range []*Server{{Auth: true}, {Auth: false}} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Serve(context.Background(), l); err == nil {
			t.Errorf("Auth %v: served", s.Auth)
		}
	}
}

// checkRcpt checks TLS and the login again, before the address: a RCPT
// that somehow passed MAIL without them gets 451.
func TestCheckRcptWithoutLogin(t *testing.T) {
	logs := &syncBuffer{}
	ss := &session{s: &Server{Auth: true, Logger: slog.New(slog.NewTextHandler(logs, nil))}, ip: netip.MustParseAddr("192.0.2.1")}
	_, err := ss.checkRcpt("archive@archive.test")
	var se *smtp.SMTPError
	if !errors.As(err, &se) || se.Code != 451 || se.EnhancedCode != (smtp.EnhancedCode{4, 7, 0}) {
		t.Fatalf("without a login: %v", err)
	}
	ss.cred, ss.account = 1, 1
	if _, err := ss.checkRcpt("archive@archive.test"); !errors.As(err, &se) || se.Code != 451 {
		t.Fatalf("with a login, without TLS: %v", err)
	}
}

// loginRefusal names every refusal of the store, and nothing else.
func TestLoginRefusal(t *testing.T) {
	for err, want := range map[error]string{
		store.ErrNotFound:                            "unknown",
		store.ErrSMTPCredentialRevoked:               "revoked",
		store.ErrSMTPCredentialExpired:               "expired",
		store.ErrAccountRemoved:                      "account_removed",
		store.ErrNotSMTP:                             "not_smtp",
		fmt.Errorf("wrapped: %w", store.ErrNotFound): "unknown",
		errors.New("connection refused"):             "",
	} {
		if got := loginRefusal(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
	if got := loginRefusal(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
}

// writePair writes a certificate and key with the given modification time.
func writePair(t *testing.T, dir string, certPEM, keyPEM []byte, mod time.Time) (string, string) {
	t.Helper()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for name, data := range map[string][]byte{cert: certPEM, key: keyPEM} {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	return cert, key
}
