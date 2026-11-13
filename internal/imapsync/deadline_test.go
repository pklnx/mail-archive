package imapsync_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// testIdle is an idle deadline no other deadline in play has, so a test
// can tell it apart from go-imap's own 30 s. The spy never lets it pass.
const testIdle = 7 * time.Minute

// scriptConfig is a Config for s, without TLS.
func scriptConfig(s *imaptest.Script) imapsync.Config {
	return imapsync.Config{Host: s.Host, Port: s.Port, TLSMode: imapsync.TLSModeNone, Username: "u", Password: "pw"}
}

// dialSpy dials s through a ConnSpy with testIdle as the idle deadline.
func dialSpy(t *testing.T, s *imaptest.Script) (*imapsync.Conn, *imapsync.ConnSpy) {
	t.Helper()
	return dialSpyIdle(t, s, testIdle)
}

// dialSpyIdle is dialSpy with the given idle deadline.
func dialSpyIdle(t *testing.T, s *imaptest.Script, idle time.Duration) (*imapsync.Conn, *imapsync.ConnSpy) {
	t.Helper()
	cfg := scriptConfig(s)
	cfg.IdleTimeout = idle
	spies := imapsync.WithSpy(&cfg)
	conn, err := imapsync.Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, <-spies
}

// waitIdleArmed waits until the spy has the idle deadline armed.
func waitIdleArmed(t *testing.T, spy *imapsync.ConnSpy, idle time.Duration) {
	t.Helper()
	testwait.Until(t, &spy.Changed, "idle deadline armed", func() bool {
		d, at := spy.Armed()
		return !d.IsZero() && d.Sub(at) > idle-time.Minute && d.Sub(at) <= idle
	})
}

// drain reads until the client closes the connection.
func drain(c *imaptest.ScriptConn) error {
	for {
		if _, _, err := c.Next(); err != nil {
			return nil //nolint:nilerr // the close is what the script waits for
		}
	}
}

// A server that goes silent after LOGIN no longer blocks ListFolders:
// the idle deadline armed for LIST closes the connection.
func TestIdleDeadlineSilentServer(t *testing.T) {
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
	conn, spy := dialSpy(t, s)
	errc := make(chan error, 1)
	go func() {
		_, err := conn.ListFolders()
		errc <- err
	}()
	<-gotList
	waitIdleArmed(t, spy, testIdle)
	spy.Expire()
	var err error
	select {
	case err = <-errc:
	case <-time.After(testwait.Limit(t)):
		t.Fatal("ListFolders still blocked after the deadline expired")
	}
	var te *imapsync.TimeoutError
	if imapsync.CategoryOf(err) != imapsync.CategoryTimeout || !errors.As(err, &te) || te.Command != "LIST" {
		t.Fatalf("err = %v (category %q), want a timeout during LIST", err, imapsync.CategoryOf(err))
	}
	if !strings.Contains(err.Error(), "LIST") {
		t.Errorf("error %q does not name the command", err)
	}
	if !conn.Broken() {
		t.Error("connection not broken after the idle deadline")
	}
	if err := s.Wait(t); err != nil {
		t.Errorf("server: %v", err)
	}
}

// Between commands no deadline is armed, so the caller's own slow work
// never breaks the connection; the next command still works.
func TestNoDeadlineBetweenCommands(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		for range 2 {
			if err := c.Examine(3); err != nil {
				return err
			}
		}
		return c.Serve()
	})
	conn, spy := dialSpy(t, s)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	testwait.Until(t, &spy.Changed, "deadline cleared after EXAMINE", func() bool {
		d, _ := spy.Armed()
		return d.IsZero()
	})
	if st, err := conn.Examine("INBOX"); err != nil || st.Messages != 3 {
		t.Fatalf("second EXAMINE: %+v, %v", st, err)
	}
	if conn.Broken() {
		t.Error("connection broken")
	}
}

// Caps() may send CAPABILITY, so the idle deadline covers it too.
func TestIdleDeadlineCapabilities(t *testing.T) {
	gotCaps := make(chan struct{})
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Send("* OK ready"); err != nil { // no capabilities
			return err
		}
		if _, err := c.Expect("CAPABILITY"); err != nil {
			return err
		}
		close(gotCaps)
		return drain(c)
	})
	cfg := scriptConfig(s)
	cfg.IdleTimeout = testIdle
	spies := imapsync.WithSpy(&cfg)
	errc := make(chan error, 1)
	go func() {
		_, err := imapsync.Dial(context.Background(), cfg)
		errc <- err
	}()
	spy := <-spies
	<-gotCaps
	waitIdleArmed(t, spy, testIdle)
	spy.Expire()
	err := <-errc
	var te *imapsync.TimeoutError
	var ie *imapsync.Error
	if !errors.As(err, &te) || te.Command != "CAPABILITY" || !errors.As(err, &ie) || ie.Category != imapsync.CategoryTimeout {
		t.Fatalf("err = %v, want a timeout during CAPABILITY", err)
	}
	if ie.LoginSent {
		t.Error("LoginSent before LOGIN")
	}
}

// Close allows LOGOUT at most 5 s.
func TestCloseLogoutDeadline(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if _, err := c.Expect("LOGOUT"); err != nil {
			return err
		}
		return drain(c)
	})
	cfg := scriptConfig(s)
	cfg.IdleTimeout = testIdle
	spies := imapsync.WithSpy(&cfg)
	conn, err := imapsync.Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	spy := <-spies
	done := make(chan struct{})
	go func() {
		_ = conn.Close()
		close(done)
	}()
	testwait.Until(t, &spy.Changed, "LOGOUT deadline armed", func() bool {
		d, at := spy.Armed()
		return !d.IsZero() && d.Sub(at) <= 5*time.Second
	})
	spy.Expire()
	select {
	case <-done:
	case <-time.After(testwait.Limit(t)):
		t.Fatal("Close still blocked")
	}
}

// The context bounds connection setup: a server that never greets.
func TestDialContextBoundsGreeting(t *testing.T) {
	accepted := make(chan struct{})
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		close(accepted)
		return drain(c)
	})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := imapsync.Dial(ctx, scriptConfig(s))
		errc <- err
	}()
	<-accepted
	cancel()
	select {
	case err := <-errc:
		var ie *imapsync.Error
		if !errors.As(err, &ie) || ie.Category != imapsync.CategoryTimeout || ie.LoginSent {
			t.Fatalf("err = %v, want timeout before login", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v does not wrap the context's cause", err)
		}
	case <-time.After(testwait.Limit(t)):
		t.Fatal("Dial still blocked after the context ended")
	}
}
