package imapsync_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/keepia/archive/internal/imapsync"
	"github.com/keepia/archive/internal/imaptest"
)

// wantCategory checks err's category and whether LOGIN had been sent.
func wantCategory(t *testing.T, err error, cat imapsync.Category, loginSent bool) {
	t.Helper()
	var ie *imapsync.Error
	if !errors.As(err, &ie) || ie.Category != cat {
		t.Fatalf("err = %v (category %q), want %q", err, imapsync.CategoryOf(err), cat)
	}
	if ie.LoginSent != loginSent {
		t.Errorf("LoginSent = %v, want %v", ie.LoginSent, loginSent)
	}
}

func TestCategoryConnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close() // nothing listens there now
	_, err = imapsync.Dial(context.Background(), imapsync.Config{
		Host: addr.IP.String(), Port: addr.Port, TLSMode: imapsync.TLSModeNone, Username: "u", Password: "pw",
	})
	wantCategory(t, err, imapsync.CategoryConnect, false)
}

func TestCategoryTLS(t *testing.T) {
	serverTLS, _ := imaptest.TLSConfigs(t)
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		_ = c.StartTLS(serverTLS) // the client rejects the certificate
		return nil
	})
	cfg := scriptConfig(s)
	cfg.TLSMode = imapsync.TLSModeTLS // default roots: the certificate is unknown
	_, err := imapsync.Dial(context.Background(), cfg)
	wantCategory(t, err, imapsync.CategoryTLS, false)
}

func TestCategoryLogin(t *testing.T) {
	const marker = "upstream-text-7c1f"
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Send("* OK [CAPABILITY IMAP4rev1] ready"); err != nil {
			return err
		}
		tag, err := c.Expect("LOGIN")
		if err != nil {
			return err
		}
		return c.Send(tag + " NO [AUTHENTICATIONFAILED] " + marker)
	})
	_, err := imapsync.Dial(context.Background(), scriptConfig(s))
	wantCategory(t, err, imapsync.CategoryLogin, true)
	// The raw text stays in the error for the server log.
	if !strings.Contains(err.Error(), marker) {
		t.Errorf("err = %v", err)
	}
}

// A server that drops the connection after LOGIN was sent: not a login
// failure, but LoginSent tells part 3's target limit to count it.
func TestCategoryLoginDropped(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Send("* OK [CAPABILITY IMAP4rev1] ready"); err != nil {
			return err
		}
		_, err := c.Expect("LOGIN")
		return err
	})
	_, err := imapsync.Dial(context.Background(), scriptConfig(s))
	wantCategory(t, err, imapsync.CategoryProtocol, true)
}

func TestCategoryServer(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		tag, err := c.Expect("EXAMINE")
		if err != nil {
			return err
		}
		if err := c.Send(tag + " NO no such folder"); err != nil {
			return err
		}
		return c.Serve()
	})
	conn := dialScript(t, s, nil)
	_, err := conn.Examine("Gone")
	wantCategory(t, err, imapsync.CategoryServer, true)
	if conn.Broken() {
		t.Error("a NO broke the connection")
	}
}

func TestCategoryProtocol(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if _, err := c.Expect("LIST"); err != nil {
			return err
		}
		if err := c.Send(`* LIST garbage ((("`); err != nil {
			return err
		}
		return drain(c)
	})
	conn := dialScript(t, s, nil)
	_, err := conn.ListFolders()
	wantCategory(t, err, imapsync.CategoryProtocol, true)
}

// A context deadline is a timeout, not the protocol error go-imap sees
// when the connection closes under it.
func TestCategoryContextDeadline(t *testing.T) {
	gotList := make(chan struct{})
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if _, err := c.Expect("LIST"); err != nil {
			return err
		}
		close(gotList)
		return drain(c)
	})
	ctx, cancel := context.WithCancelCause(context.Background())
	conn, err := imapsync.Dial(ctx, scriptConfig(s))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	errc := make(chan error, 1)
	go func() {
		_, err := conn.ListFolders()
		errc <- err
	}()
	<-gotList
	cancel(context.DeadlineExceeded)
	err = <-errc
	wantCategory(t, err, imapsync.CategoryTimeout, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v does not wrap the cause", err)
	}
}

// Errors from the caller's callbacks pass through unchanged, and the
// connection stays usable.
func TestCallbackErrorPassesThrough(t *testing.T) {
	errMine := errors.New("mine")
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(3); err != nil {
			return err
		}
		for range 2 {
			tag, err := c.Expect("UID FETCH")
			if err != nil {
				return err
			}
			if err := c.Send("* 1 FETCH (UID 1 BODY[] {3}", "abc)", "* 2 FETCH (UID 2 BODY[] {3}", "def)",
				"* 3 FETCH (UID 3 BODY[] {3}", "ghi)", tag+" OK done"); err != nil {
				return err
			}
		}
		return c.Serve()
	})
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := imapsync.FetchAfter(conn, 0, discardSink, func(imapsync.Message[int64]) error { return errMine })
	if err != errMine { //nolint:errorlint // unchanged means the same value
		t.Fatalf("err = %v, want the handler's own error", err)
	}
	sinkErr := errors.New("disk full")
	err = imapsync.FetchAfter(conn, 0, func(r io.Reader) (int64, error) { return 0, sinkErr }, func(imapsync.Message[int64]) error { return nil })
	if !errors.Is(err, sinkErr) || imapsync.CategoryOf(err) != "" {
		t.Fatalf("err = %v, want the sink's error without a category", err)
	}
	if conn.Broken() {
		t.Error("a callback error broke the connection")
	}
}

// STARTTLS still works through the metered connection.
func TestStartTLS(t *testing.T) {
	serverTLS, clientTLS := imaptest.TLSConfigs(t)
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Send("* OK [CAPABILITY IMAP4rev1 STARTTLS] ready"); err != nil {
			return err
		}
		tag, err := c.Expect("STARTTLS")
		if err != nil {
			return err
		}
		if err := c.Send(tag + " OK begin TLS"); err != nil {
			return err
		}
		if err := c.StartTLS(serverTLS); err != nil {
			return err
		}
		if tag, err = c.Expect("CAPABILITY"); err != nil {
			return err
		}
		if err := c.Send("* CAPABILITY IMAP4rev1", tag+" OK done"); err != nil {
			return err
		}
		if tag, err = c.Expect("LOGIN"); err != nil {
			return err
		}
		if err := c.Send(tag + " OK [CAPABILITY IMAP4rev1] in"); err != nil {
			return err
		}
		if tag, err = c.Expect("LIST"); err != nil {
			return err
		}
		if err := c.Send(`* LIST () "/" "INBOX"`, tag+" OK done"); err != nil {
			return err
		}
		return c.Serve()
	})
	conn := dialScript(t, s, func(c *imapsync.Config) {
		c.TLSMode = imapsync.TLSModeSTARTTLS
		c.TLSConfig = clientTLS
	})
	list, err := conn.ListFolders()
	if err != nil || len(list) != 1 || list[0].Name != "INBOX" {
		t.Fatalf("folders %+v, %v", list, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
}
