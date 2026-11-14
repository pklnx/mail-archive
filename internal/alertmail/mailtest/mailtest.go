// Package mailtest is an in-process SMTP relay for the tests of alert
// mail (#397, part 5): it listens on 127.0.0.1 only, with a self-signed
// certificate made at start, and records what it receives. Tests never
// reach the network.
package mailtest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/keepia/archive/internal/alertmail"
	"github.com/keepia/archive/internal/config"
)

// Modes of the relay.
const (
	// StartTLS offers STARTTLS on a plain connection (smtp://).
	StartTLS = "starttls"
	// Implicit speaks TLS from the start (smtps://).
	Implicit = "implicit"
	// NoTLS offers no STARTTLS: the client must refuse it.
	NoTLS = "none"
)

// Host is the name the certificate is made for.
const Host = "relay.test"

// Login is the AUTH PLAIN user and password the relay accepts; not a
// secret.
const (
	User     = "alerts"
	Password = "relay-test-login"
)

// Mail is one message the relay took.
type Mail struct {
	From string
	To   []string
	Data []byte
}

// Reply is what a hook answers: nil to accept.
type Reply func(arg string) error

// Server is the relay.
type Server struct {
	Mode string
	// Roots trusts the relay's certificate.
	Roots *x509.CertPool
	addr  string

	mu   sync.Mutex
	mail []Mail
	// hooks per phase ("mail", "rcpt", "data"), set with On.
	hooks map[string]Reply
	// commands counts MAIL commands received.
	commands int
}

// Start runs a relay in mode until the test ends.
func Start(t testing.TB, mode string) *Server {
	t.Helper()
	cert, roots := certificate(t)
	s := &Server{Mode: mode, Roots: roots, hooks: map[string]Reply{}}
	srv := smtp.NewServer(&backend{s: s})
	srv.Domain = Host
	srv.ReadTimeout, srv.WriteTimeout = time.Minute, time.Minute
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	switch mode {
	case StartTLS:
		srv.TLSConfig = tlsCfg
	case Implicit:
		l = tls.NewListener(l, tlsCfg)
	case NoTLS:
		srv.AllowInsecureAuth = true
	}
	s.addr = l.Addr().String()
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s
}

// On sets the answer of a phase ("mail", "rcpt", "data"); nil accepts.
func (s *Server) On(phase string, r Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks[phase] = r
}

// Mails returns what the relay took.
func (s *Server) Mails() []Mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.mail...)
}

// MailCommands counts the MAIL commands the relay received.
func (s *Server) MailCommands() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commands
}

// Config is the relay as config.Mail, with the login and from.
func (s *Server) Config() *config.Mail {
	_, port, _ := net.SplitHostPort(s.addr)
	return &config.Mail{
		Host: Host, Port: port, ImplicitTLS: s.Mode == Implicit, Username: User, Password: Password,
		From: "archive@relay.test", FromHeader: "Mail Archive <archive@relay.test>",
	}
}

// Sender sends to the relay: Host resolves to its address, and its
// certificate is trusted.
func (s *Server) Sender() *alertmail.Sender {
	return &alertmail.Sender{Cfg: s.Config(), TLS: &tls.Config{RootCAs: s.Roots}, Dial: s.Dial}
}

// Dial connects to the relay whatever address is asked for.
func (s *Server) Dial(ctx context.Context, network, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, s.addr)
}

// Error is an SMTP reply for a hook, such as Error(550, 5, 1, 1, "...").
func Error(code, a, b, c int, text string) error {
	return &smtp.SMTPError{Code: code, EnhancedCode: smtp.EnhancedCode{a, b, c}, Message: text}
}

type backend struct{ s *Server }

func (b *backend) NewSession(*smtp.Conn) (smtp.Session, error) { return &session{s: b.s}, nil }

type session struct {
	s    *Server
	from string
	to   []string
	auth bool
}

func (x *session) hook(phase, arg string) error {
	x.s.mu.Lock()
	h := x.s.hooks[phase]
	x.s.mu.Unlock()
	if h == nil {
		return nil
	}
	return h(arg)
}

func (x *session) AuthMechanisms() []string { return []string{sasl.Plain} }

func (x *session) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, user, pass string) error {
		if user != User || pass != Password {
			return errors.New("bad login")
		}
		x.auth = true
		return nil
	}), nil
}

func (x *session) Mail(from string, _ *smtp.MailOptions) error {
	x.s.mu.Lock()
	x.s.commands++
	x.s.mu.Unlock()
	if !x.auth {
		return Error(530, 5, 7, 0, "authentication required")
	}
	if err := x.hook("mail", from); err != nil {
		return err
	}
	x.from = from
	return nil
}

func (x *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if err := x.hook("rcpt", to); err != nil {
		return err
	}
	x.to = append(x.to, to)
	return nil
}

func (x *session) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if err := x.hook("data", string(data)); err != nil {
		return err
	}
	x.s.mu.Lock()
	x.s.mail = append(x.s.mail, Mail{From: x.from, To: x.to, Data: bytes.Clone(data)})
	x.s.mu.Unlock()
	return nil
}

func (x *session) Reset()        { x.from, x.to = "", nil }
func (x *session) Logout() error { return nil }

// certificate makes a self-signed certificate for Host and 127.0.0.1.
func certificate(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: Host},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		DNSNames: []string{Host}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
}

// Port is the relay's port.
func (s *Server) Port() int {
	_, p, _ := net.SplitHostPort(s.addr)
	n, _ := strconv.Atoi(p)
	return n
}
