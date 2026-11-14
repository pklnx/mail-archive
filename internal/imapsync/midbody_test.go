package imapsync_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/keepia/archive/internal/imapsync"
	"github.com/keepia/archive/internal/imaptest"
	"github.com/keepia/archive/internal/testwait"
)

// partialBody serves one message whose body literal announces size bytes
// but stops after sent; then end runs.
func partialBody(t *testing.T, size, sent int, end func(*imaptest.ScriptConn) error) (*imaptest.Script, <-chan struct{}) {
	t.Helper()
	wrote := make(chan struct{})
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(1); err != nil {
			return err
		}
		if _, err := c.Expect("UID FETCH"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(c, "* 1 FETCH (UID 5 FLAGS () BODY[] {%d}\r\n%s", size, strings.Repeat("x", sent)); err != nil {
			return err
		}
		close(wrote)
		return end(c)
	})
	return s, wrote
}

// fetchMidBody runs FetchAfter in the background with a sink that reads
// the whole body; it returns the result channel and closes inSink once
// the sink runs. handle must never be called.
func fetchMidBody(t *testing.T, conn *imapsync.Conn, inSink chan struct{}) <-chan error {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		errc <- imapsync.FetchAfter(conn, 0, func(r io.Reader) (int64, error) {
			close(inSink)
			return io.Copy(io.Discard, r)
		}, func(imapsync.Message[int64]) error {
			t.Error("a message with a cut-off body was handled")
			return nil
		})
	}()
	return errc
}

func waitErr(t *testing.T, errc <-chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(testwait.Limit(t)):
		t.Fatal("FetchAfter still blocked")
		return nil
	}
}

// The server goes silent in the middle of a body: the idle deadline ends
// the fetch with a timeout, not with an uncategorised sink error.
func TestMidBodySilence(t *testing.T) {
	// Inside a literal go-imap arms its own 5 min deadline; an idle
	// deadline below it is the one in force.
	const idle = 3 * time.Minute
	s, wrote := partialBody(t, 1000, 3, drain)
	conn, spy := dialSpyIdle(t, s, idle)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	inSink := make(chan struct{})
	errc := fetchMidBody(t, conn, inSink)
	<-wrote
	<-inSink
	waitIdleArmed(t, spy, idle)
	spy.Expire()
	err := waitErr(t, errc)
	wantCategory(t, err, imapsync.CategoryTimeout, true)
	if !strings.Contains(err.Error(), "UID FETCH") || !conn.Broken() {
		t.Errorf("err = %v, broken %v", err, conn.Broken())
	}
}

// MaxRead runs out while the sink reads a body: a limit error.
func TestMidBodyMaxRead(t *testing.T) {
	s, _ := partialBody(t, 200_000, 200_000, func(c *imaptest.ScriptConn) error {
		_ = c.Send(")", "T9 OK done")
		return drain(c)
	})
	conn := dialScript(t, s, func(c *imapsync.Config) { c.MaxRead = 64 << 10 })
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := waitErr(t, fetchMidBody(t, conn, make(chan struct{})))
	wantLimit(t, conn, err, imapsync.LimitRead, 64<<10)
}

// The server closes the connection in the middle of a body: a protocol
// error, and the cut-off body never reaches handle.
func TestMidBodyEOF(t *testing.T) {
	s, _ := partialBody(t, 1000, 10, func(*imaptest.ScriptConn) error { return nil })
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := waitErr(t, fetchMidBody(t, conn, make(chan struct{})))
	wantCategory(t, err, imapsync.CategoryProtocol, true)
	if !conn.Broken() {
		t.Error("connection not broken")
	}
}

// The same in a header literal of ListHeaders.
func TestMidHeaderEOF(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(1); err != nil {
			return err
		}
		if _, err := c.Expect("UID FETCH"); err != nil {
			return err
		}
		_, err := io.WriteString(c, "* 1 FETCH (UID 2 RFC822.SIZE 10 BODY[HEADER.FIELDS (MESSAGE-ID)] {500}\r\nMessage-ID: <a")
		return err
	})
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := imapsync.ListHeaders(conn, 0, 0, func(imapsync.Header) error {
		t.Error("cut-off header handled")
		return nil
	})
	wantCategory(t, err, imapsync.CategoryProtocol, true)
}

// noticeScript answers EXAMINE with exists messages, the probe for the
// newest UID with newest (no probe expected if 0), and the UID FETCH with
// notices produced by notice(i) for i = 1..n, then one new message.
func noticeScript(t *testing.T, exists, n int, newest uint32, notice func(i int) string) *imaptest.Script {
	t.Helper()
	return imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(exists); err != nil {
			return err
		}
		if newest != 0 {
			if err := c.Newest(newest); err != nil {
				return err
			}
		}
		tag, err := c.Expect("UID FETCH")
		if err != nil {
			return err
		}
		if _, err := flood(c, n, notice); err != nil {
			return nil //nolint:nilerr // the client closed: the budget ran dry
		}
		if err := c.Send(fmt.Sprintf("* 1 FETCH (UID %d BODY[] {3}", exists+1), "abc)", tag+" OK done"); err != nil {
			return err
		}
		return c.Serve()
	})
}

// Another client empties a large folder, or marks it read, while we fetch:
// an honest server reports every change. Those notices earn a little
// credit, up to the folder's message count, so they pass.
func TestUnilateralNoticesPass(t *testing.T) {
	const n = 100_000
	cases := map[string]func(i int) string{
		"EXPUNGE":     func(int) string { return "* 1 EXPUNGE" },
		"FETCH FLAGS": func(i int) string { return fmt.Sprintf("* %d FETCH (UID %d FLAGS (\\Seen))", i, i) },
	}
	for name, notice := range cases {
		t.Run(name, func(t *testing.T) {
			s := noticeScript(t, n+1, n, n+1, notice)
			conn := dialScript(t, s, nil)
			if _, err := conn.Examine("INBOX"); err != nil {
				t.Fatal(err)
			}
			got := 0
			before := conn.Budget()
			// after = n: the flagged UIDs 1..n are not requested, so they
			// arrive as unsolicited notices.
			if err := imapsync.FetchAfter(conn, n, discardSink, func(imapsync.Message[int64]) error { got++; return nil }); err != nil {
				t.Fatal(err)
			}
			if got != 1 {
				t.Errorf("handled %d messages", got)
			}
			// The notices earned no more than they cost: the budget grew
			// by at most the command's and the one message's credit.
			// The probe for the newest UID earns 1 KiB.
			if gain := conn.Budget() - before; gain > 16<<10+1<<10+1<<10 {
				t.Errorf("budget grew by %d during the notices", gain)
			}
		})
	}
}

// Notices beyond the folder's message count (plus 1,000) earn nothing:
// an unbounded flood still runs the budget dry.
func TestUnilateralNoticeFlood(t *testing.T) {
	s := noticeScript(t, 1, endless, 0, func(int) string { return "* 1 EXPUNGE" })
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := imapsync.FetchAfter(conn, 0, discardSink, func(imapsync.Message[int64]) error { return nil })
	wantLimit(t, conn, err, imapsync.LimitUnrequested, 0)
	// 1,001 credited notices of 13 bytes, then what the budget held.
	if n, most := conn.BytesRead(), int64(1001*13+2*imapsync.BudgetMax); n > most {
		t.Errorf("read %d bytes, more than %d", n, most)
	}
}

// When go-imap's own deadline is the earlier one (5 min within a literal,
// here below a 10 min idle deadline), the error does not claim the idle
// deadline ran out.
func TestTimeoutNamesLibraryDeadline(t *testing.T) {
	s, wrote := partialBody(t, 1000, 3, drain)
	conn, spy := dialSpyIdle(t, s, 10*time.Minute)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	inSink := make(chan struct{})
	errc := fetchMidBody(t, conn, inSink)
	<-wrote
	<-inSink
	testwait.Until(t, &spy.Changed, "go-imap's literal deadline armed", func() bool {
		d, at := spy.Armed()
		return d.Sub(at) > 4*time.Minute && d.Sub(at) <= 5*time.Minute
	})
	spy.Expire()
	err := waitErr(t, errc)
	var te *imapsync.TimeoutError
	if !errors.As(err, &te) || te.Idle != 0 || !strings.Contains(err.Error(), "within the read deadline during UID FETCH") {
		t.Fatalf("err = %v", err)
	}
}

// A server that claims 4,000,000,000 messages and then floods EXPUNGE
// notices: each notice earns at most its own size, never more, and only
// a million per connection earn anything (lowered here to 10,000). The
// flood trips after them.
func TestUnilateralNoticeHugeExists(t *testing.T) {
	const notice, capped = "* 1 EXPUNGE", 10_000
	if imapsync.NoticesMax != 1_000_000 {
		t.Errorf("NoticesMax = %d", imapsync.NoticesMax)
	}
	s := noticeScript(t, 4_000_000_000, endless, 0, func(int) string { return notice })
	conn := dialScript(t, s, func(c *imapsync.Config) { imapsync.SetNoticesMax(c, capped) })
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	start := conn.Budget()
	err := imapsync.FetchAfter(conn, 0, discardSink, func(imapsync.Message[int64]) error { return nil })
	wantLimit(t, conn, err, imapsync.LimitUnrequested, 0)
	// The credited notices of 13 bytes, then what the budget held.
	wire := int64(len(notice) + 2)
	if n, most := conn.BytesRead(), capped*wire+2*imapsync.BudgetMax; n > most {
		t.Errorf("read %d bytes, more than %d", n, most)
	}
	if start > imapsync.BudgetMax {
		t.Errorf("budget %d", start)
	}
}

// Shutdown arrives while the sink fails with its own error before it read
// the literal to the end: go-imap's reader waits for that literal, so the
// fetch must release it rather than wait for the reader forever.
func TestSinkErrorAfterCancel(t *testing.T) {
	s, _ := partialBody(t, 1000, 3, drain)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := imapsync.Dial(ctx, scriptConfig(s))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		errc <- imapsync.FetchAfter(conn, 0, func(io.Reader) (int64, error) {
			cancel()
			return 0, errors.New("disk full")
		}, func(imapsync.Message[int64]) error {
			t.Error("message handled")
			return nil
		})
	}()
	err = waitErr(t, errc)
	wantCategory(t, err, imapsync.CategoryTimeout, true)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

// The sink reads message 1 to its end and fails on its own as shutdown
// arrives; go-imap has meanwhile decoded message 2 from its buffer and
// waits for someone to read its body. The fetch must not wait for
// go-imap's reader, but let the drain discard the rest.
func TestSinkErrorAfterFullReadCancel(t *testing.T) {
	body := strings.Repeat("x", 200)
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(5); err != nil {
			return err
		}
		tag, err := c.Expect("UID FETCH")
		if err != nil {
			return err
		}
		var b strings.Builder
		for i := 1; i <= 5; i++ {
			fmt.Fprintf(&b, "* %d FETCH (UID %d BODY[] {%d}\r\n%s)\r\n", i, i, len(body), body)
		}
		b.WriteString(tag + " OK done\r\n")
		if _, err := io.WriteString(c, b.String()); err != nil {
			return err
		}
		return drain(c)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := imapsync.Dial(ctx, scriptConfig(s))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		errc <- imapsync.FetchAfter(conn, 0, func(r io.Reader) (int64, error) {
			if _, err := io.Copy(io.Discard, r); err != nil {
				return 0, err
			}
			cancel()
			return 0, errors.New("disk full")
		}, func(imapsync.Message[int64]) error {
			t.Error("message handled")
			return nil
		})
	}()
	err = waitErr(t, errc)
	wantCategory(t, err, imapsync.CategoryTimeout, true)
	if !errors.Is(err, context.Canceled) || !conn.Broken() {
		t.Errorf("err = %v, broken %v", err, conn.Broken())
	}
}

// Notices ending in a lone LF, which go-imap accepts, earn 1 B more than
// they cost; the per-connection cap still bounds them.
func TestUnilateralNoticeLoneLF(t *testing.T) {
	const capped = 10_000
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(4_000_000_000); err != nil {
			return err
		}
		if _, err := c.Expect("UID FETCH"); err != nil {
			return err
		}
		chunk := strings.Repeat("* 1 EXPUNGE\n", 4096)
		for {
			if _, err := io.WriteString(c, chunk); err != nil {
				return nil //nolint:nilerr // the client closed
			}
		}
	})
	conn := dialScript(t, s, func(c *imapsync.Config) { imapsync.SetNoticesMax(c, capped) })
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := imapsync.FetchAfter(conn, 0, discardSink, func(imapsync.Message[int64]) error { return nil })
	wantLimit(t, conn, err, imapsync.LimitUnrequested, 0)
	if n, most := conn.BytesRead(), int64(capped*12+capped+2*imapsync.BudgetMax); n > most {
		t.Errorf("read %d bytes, more than %d", n, most)
	}
}

// A sink that stops inside the literal, or after exactly its Size() bytes
// without reading EOF, and then fails with the context cancelled: more
// messages are buffered behind it, and FetchAfter still returns. Guards
// connErr's rule (wait for go-imap's reader only after a read error)
// against a go-imap upgrade that changes when a literal's reader is
// released.
func TestSinkErrorAfterPartialReadCancel(t *testing.T) {
	const size = 200
	for _, tc := range []struct {
		name string
		read int
	}{
		{"half", size / 2},
		{"exact size, no EOF", size},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Repeat("x", size)
			s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
				if err := c.Greet(""); err != nil {
					return err
				}
				if err := c.Examine(5); err != nil {
					return err
				}
				tag, err := c.Expect("UID FETCH")
				if err != nil {
					return err
				}
				var b strings.Builder
				for i := 1; i <= 5; i++ {
					fmt.Fprintf(&b, "* %d FETCH (UID %d BODY[] {%d}\r\n%s)\r\n", i, i, len(body), body)
				}
				b.WriteString(tag + " OK done\r\n")
				if _, err := io.WriteString(c, b.String()); err != nil {
					return err
				}
				return drain(c)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn, err := imapsync.Dial(ctx, scriptConfig(s))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Examine("INBOX"); err != nil {
				t.Fatal(err)
			}
			errc := make(chan error, 1)
			go func() {
				errc <- imapsync.FetchAfter(conn, 0, func(r io.Reader) (int64, error) {
					buf := make([]byte, tc.read)
					if _, err := io.ReadFull(r, buf); err != nil {
						return 0, err
					}
					cancel()
					return 0, errors.New("disk full")
				}, func(imapsync.Message[int64]) error {
					t.Error("message handled")
					return nil
				})
			}()
			err = waitErr(t, errc)
			wantCategory(t, err, imapsync.CategoryTimeout, true)
			if !errors.Is(err, context.Canceled) || !conn.Broken() {
				t.Errorf("err = %v, broken %v", err, conn.Broken())
			}
		})
	}
}
