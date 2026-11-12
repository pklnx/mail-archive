package imaptest

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/testwait"
)

// Script is a raw-TCP IMAP server whose every byte a test writes by hand,
// for answers no well-behaved server gives: silence, floods, huge
// literals. It listens on the loopback interface only.
type Script struct {
	Host string
	Port int

	results chan error
}

// StartScript serves each accepted connection with serve, in its own
// goroutine, until the test ends. serve's result is handed to Wait.
func StartScript(t testing.TB, serve func(*ScriptConn) error) *Script {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Script{results: make(chan error, 16)}
	addr := ln.Addr().(*net.TCPAddr)
	s.Host, s.Port = addr.IP.String(), addr.Port
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		_ = ln.Close()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc := newScriptConn(c)
				err := serve(sc)
				_ = sc.Close()
				select {
				case s.results <- err:
				case <-done:
				}
			}()
		}
	}()
	return s
}

// Wait returns the result of the next serve call to end, failing the test
// if none ends within testwait.Limit.
func (s *Script) Wait(t testing.TB) error {
	t.Helper()
	select {
	case err := <-s.results:
		return err
	case <-time.After(testwait.Limit(t)):
		t.Fatal("scripted server did not finish")
		return nil
	}
}

// ScriptConn is one connection of a Script.
type ScriptConn struct {
	net.Conn
	r *bufio.Reader
}

func newScriptConn(c net.Conn) *ScriptConn {
	return &ScriptConn{Conn: c, r: bufio.NewReader(c)}
}

// StartTLS switches the connection to TLS as the server, with cfg.
func (c *ScriptConn) StartTLS(cfg *tls.Config) error {
	tc := tls.Server(c.Conn, cfg)
	if err := tc.HandshakeContext(context.Background()); err != nil {
		return err
	}
	c.Conn, c.r = tc, bufio.NewReader(tc)
	return nil
}

// Send writes each line followed by CRLF.
func (c *ScriptConn) Send(lines ...string) error {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	_, err := io.WriteString(c.Conn, b.String())
	return err
}

// Next reads the next command and returns its tag and the rest of the
// line, like "T1" and "LOGIN \"u\" \"pw\"".
func (c *ScriptConn) Next() (tag, line string, err error) {
	l, err := c.r.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	l = strings.TrimRight(l, "\r\n")
	tag, line, _ = strings.Cut(l, " ")
	return tag, line, nil
}

// Expect reads the next command and fails unless it starts with verb.
func (c *ScriptConn) Expect(verb string) (tag string, err error) {
	tag, line, err := c.Next()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(strings.ToUpper(line), verb) {
		return "", fmt.Errorf("got %q, want %s", line, verb)
	}
	return tag, nil
}

// Greet sends a greeting with the capabilities and answers LOGIN with
// OK, so that the client asks for nothing else. caps defaults to
// IMAP4rev1.
func (c *ScriptConn) Greet(caps string) error {
	if caps == "" {
		caps = "IMAP4rev1"
	}
	if err := c.Send("* OK [CAPABILITY " + caps + "] ready"); err != nil {
		return err
	}
	tag, err := c.Expect("LOGIN")
	if err != nil {
		return err
	}
	return c.Send(tag + " OK [CAPABILITY " + caps + "] logged in")
}

// Examine answers the next command, which must be EXAMINE, with a folder
// of exists messages.
func (c *ScriptConn) Examine(exists int) error {
	tag, err := c.Expect("EXAMINE")
	if err != nil {
		return err
	}
	return c.Send(
		fmt.Sprintf("* %d EXISTS", exists),
		"* OK [UIDVALIDITY 1] ok",
		fmt.Sprintf("* OK [UIDNEXT %d] ok", exists+1),
		tag+" OK [READ-ONLY] done",
	)
}

// Serve answers LOGOUT and returns nil once the client logs out or
// closes the connection; any other command fails.
func (c *ScriptConn) Serve() error {
	tag, line, err := c.Next()
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.ToUpper(line), "LOGOUT") {
		return fmt.Errorf("unexpected command %q", line)
	}
	return c.Send("* BYE", tag+" OK bye")
}

// TLSConfigs returns a server configuration with a fresh self-signed
// certificate for 127.0.0.1 and a client configuration that trusts it.
func TLSConfigs(t testing.TB) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "imaptest"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	server = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
	client = &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	return server, client
}
