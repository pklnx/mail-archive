package imapsync_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/imaptest"
)

// dialScript dials s without TLS, after edit changes the Config.
func dialScript(t testing.TB, s *imaptest.Script, edit func(*imapsync.Config)) *imapsync.Conn {
	t.Helper()
	cfg := scriptConfig(s)
	if edit != nil {
		edit(&cfg)
	}
	conn, err := imapsync.Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// flood writes lines produced by line(i) for i = 1..max, in batches,
// until a write fails. It returns how many it sent and the write error,
// nil if it sent them all.
func flood(c *imaptest.ScriptConn, maxLines int, line func(i int) string) (int, error) {
	var b strings.Builder
	sent := 0
	for i := 1; i <= maxLines; i++ {
		b.WriteString(line(i))
		b.WriteString("\r\n")
		if b.Len() >= 32<<10 || i == maxLines {
			if _, err := io.WriteString(c, b.String()); err != nil {
				return sent, err
			}
			sent = i
			b.Reset()
		}
	}
	return sent, nil
}

// floodRaw writes n bytes of "a" without line breaks.
func floodRaw(c *imaptest.ScriptConn, n int) error {
	buf := bytes.Repeat([]byte("a"), 32<<10)
	for sent := 0; sent < n; sent += len(buf) {
		if _, err := c.Write(buf[:min(len(buf), n-sent)]); err != nil {
			return err
		}
	}
	return nil
}

// floodScript answers LOGIN, waits for verb, and then floods. The result
// of Wait is nil only if the server saw the client close the connection
// before it had sent all lines.
func floodScript(t *testing.T, setup func(*imaptest.ScriptConn) error, verb string, maxLines int, line func(i int) string) *imaptest.Script {
	t.Helper()
	return imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if setup != nil {
			if err := setup(c); err != nil {
				return err
			}
		}
		if _, err := c.Expect(verb); err != nil {
			return err
		}
		sent, err := flood(c, maxLines, line)
		if err == nil {
			return fmt.Errorf("sent all %d lines; the client never closed", sent)
		}
		return nil
	})
}

// wantLimit checks that err is a limit error for what, with limit if not
// 0, and that the connection is broken. It returns the error's UID.
func wantLimit(t *testing.T, conn *imapsync.Conn, err error, what string, limit int64) uint32 {
	t.Helper()
	var lim *imapsync.LimitError
	if imapsync.CategoryOf(err) != imapsync.CategoryLimit || !errors.As(err, &lim) || lim.What != what {
		t.Fatalf("err = %v, want a limit error for %s", err, what)
	}
	if limit != 0 && lim.Limit != limit {
		t.Errorf("limit %d, want %d", lim.Limit, limit)
	}
	if !conn.Broken() {
		t.Error("connection not closed after the limit")
	}
	return lim.UID
}

// wantConfigured checks whether the limit error names the configured or
// caller's limit.
func wantConfigured(t *testing.T, err error, want bool) {
	t.Helper()
	var lim *imapsync.LimitError
	if errors.As(err, &lim) && lim.Configured != want {
		t.Errorf("Configured = %v, want %v", lim.Configured, want)
	}
}

const endless = 1 << 30

// 3,000,000 LIST lines: ListFolders stops at the folder cap,
// and the server sees the close before it has sent them all.
func TestListFoldersCap(t *testing.T) {
	s := floodScript(t, nil, "LIST", 3_000_000, func(i int) string {
		return fmt.Sprintf(`* LIST () "/" "folder%d"`, i)
	})
	conn := dialScript(t, s, nil)
	_, err := conn.ListFolders()
	wantLimit(t, conn, err, imapsync.LimitFolders, imapsync.DefaultMaxFolders)
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
}

// Config.MaxFolders sets the cap, and the error carries it.
func TestListFoldersConfiguredCap(t *testing.T) {
	s := floodScript(t, nil, "LIST", endless, func(i int) string {
		return fmt.Sprintf(`* LIST () "/" "folder%d"`, i)
	})
	conn := dialScript(t, s, func(c *imapsync.Config) { c.MaxFolders = 100 })
	_, err := conn.ListFolders()
	wantLimit(t, conn, err, imapsync.LimitFolders, 100)
	wantConfigured(t, err, true)
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
}

// A folder name of 1,024 bytes is fine; 1,025 trips the cap.
func TestListFoldersNameCap(t *testing.T) {
	ok := strings.Repeat("a", imapsync.MaxFolderName)
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		tag, err := c.Expect("LIST")
		if err != nil {
			return err
		}
		if err := c.Send(`* LIST () "/" "`+ok+`"`, `* LIST () "/" "`+ok+`b"`, tag+" OK done"); err != nil {
			return err
		}
		return drain(c)
	})
	conn := dialScript(t, s, nil)
	_, err := conn.ListFolders()
	wantLimit(t, conn, err, imapsync.LimitFolderName, imapsync.MaxFolderName)
}

// Responses that reach no code of ours spend the credit budget until it
// runs dry: a 2 MiB LIST line, a 10 MB mailbox-name literal, and endless
// untagged OKs. The client reads far less than the server offers, and the
// server sees the connection close.
func TestUnrequestedData(t *testing.T) {
	const twoMiB = 2 << 20
	cases := map[string]func(c *imaptest.ScriptConn) error{
		"2 MiB LIST line": func(c *imaptest.ScriptConn) error {
			if _, err := io.WriteString(c, `* LIST () "/" "`); err != nil {
				return err
			}
			return floodRaw(c, twoMiB)
		},
		"10 MB name literal": func(c *imaptest.ScriptConn) error {
			if err := c.Send(`* LIST () "/" {10000000}`); err != nil {
				return err
			}
			return floodRaw(c, 10_000_000)
		},
		"endless OK": func(c *imaptest.ScriptConn) error {
			_, err := flood(c, endless, func(int) string { return "* OK x" })
			return err
		},
	}
	for name, send := range cases {
		t.Run(name, func(t *testing.T) {
			s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
				if err := c.Greet(""); err != nil {
					return err
				}
				if _, err := c.Expect("LIST"); err != nil {
					return err
				}
				if err := send(c); err != nil {
					return nil //nolint:nilerr // the client closed while the server was sending
				}
				// Everything fit into the socket buffers; the client's
				// read count below shows it stopped early.
				return drain(c)
			})
			conn := dialScript(t, s, nil)
			_, err := conn.ListFolders()
			wantLimit(t, conn, err, imapsync.LimitUnrequested, imapsync.BudgetMax)
			if n := conn.BytesRead(); n > 2*imapsync.BudgetMax {
				t.Errorf("read %d bytes", n)
			}
			if err := s.Wait(t); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Junk while no command is in flight runs the budget dry too; the next
// command reports it.
func TestJunkBetweenCommands(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if _, err := flood(c, endless, func(i int) string { return fmt.Sprintf("* OK junk %d", i) }); err == nil {
			return errors.New("sent everything")
		}
		return nil
	})
	conn := dialScript(t, s, nil)
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
	_, err := conn.ListFolders()
	wantLimit(t, conn, err, imapsync.LimitUnrequested, 0)
}

// EXISTS=1 and then a flood of FETCH responses: ListFlags
// accepts EXISTS + 1,000.
func TestListFlagsResponseCap(t *testing.T) {
	s := floodScript(t, func(c *imaptest.ScriptConn) error { return c.Examine(1) }, "UID FETCH", 3_000_000,
		func(i int) string { return fmt.Sprintf("* %d FETCH (UID %d FLAGS (\\Seen))", i, i) })
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	n := 0
	err := imapsync.ListFlags(conn, 0, func(uint32, []imap.Flag) { n++ })
	wantLimit(t, conn, err, imapsync.LimitFetchResponses, 1001)
	wantConfigured(t, err, false) // the slack over EXISTS tripped
	if n != 1001 {
		t.Errorf("handled %d messages", n)
	}
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
}

// The caller's limit caps ListFlags below EXISTS + 1,000.
func TestListFlagsCallerLimit(t *testing.T) {
	s := floodScript(t, func(c *imaptest.ScriptConn) error { return c.Examine(500) }, "UID FETCH", endless,
		func(i int) string { return fmt.Sprintf("* %d FETCH (UID %d FLAGS ())", i, i) })
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := imapsync.ListFlags(conn, 300, func(uint32, []imap.Flag) {})
	wantLimit(t, conn, err, imapsync.LimitFetchResponses, 300)
	wantConfigured(t, err, true)
}

// ListHeaders accepts EXISTS + 1,000 at most, and at most the caller's
// limit.
func TestListHeadersResponseCap(t *testing.T) {
	for _, c := range []struct {
		limit uint32
		want  int64
	}{{0, 1002}, {10, 10}} {
		s := floodScript(t, func(c *imaptest.ScriptConn) error { return c.Examine(2) }, "UID FETCH", endless,
			func(i int) string {
				return fmt.Sprintf("* %d FETCH (UID %d RFC822.SIZE 10 BODY[HEADER.FIELDS (MESSAGE-ID)] {2}\r\n\r\n)", i, i)
			})
		conn := dialScript(t, s, nil)
		if _, err := conn.Examine("INBOX"); err != nil {
			t.Fatal(err)
		}
		err := imapsync.ListHeaders(conn, 0, c.limit, func(imapsync.Header) error { return nil })
		wantLimit(t, conn, err, imapsync.LimitFetchResponses, c.want)
	}
}

// FetchAfter's cap follows the live EXISTS, but FETCH responses without
// a body earn no credit: endless EXISTS plus FETCH runs the budget dry.
func TestFetchAfterEndlessExists(t *testing.T) {
	s := floodScript(t, func(c *imaptest.ScriptConn) error { return c.Examine(1) }, "UID FETCH", endless,
		func(i int) string { return fmt.Sprintf("* %d EXISTS\r\n* %d FETCH (UID %d FLAGS ())", i+1, i, i) })
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := imapsync.FetchAfter(conn, 0, discardSink, func(imapsync.Message[int64]) error {
		t.Error("message without body handled")
		return nil
	})
	wantLimit(t, conn, err, imapsync.LimitUnrequested, 0)
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
}

// FetchAfter accepts the live EXISTS + 1,000 responses with bodies.
func TestFetchAfterResponseCap(t *testing.T) {
	s := floodScript(t, func(c *imaptest.ScriptConn) error { return c.Examine(1) }, "UID FETCH", endless,
		func(i int) string { return fmt.Sprintf("* %d FETCH (UID %d BODY[] {3}\r\nabc)", i, i) })
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	n := 0
	err := imapsync.FetchAfter(conn, 0, discardSink, func(imapsync.Message[int64]) error { n++; return nil })
	wantLimit(t, conn, err, imapsync.LimitFetchResponses, 1001)
	if n != 1001 {
		t.Errorf("handled %d", n)
	}
}

// FetchUIDs asks for given UIDs: go-imap drops responses for others, so a
// flood of them runs the budget dry.
func TestFetchUIDsFlood(t *testing.T) {
	s := floodScript(t, func(c *imaptest.ScriptConn) error { return c.Examine(3) }, "UID FETCH", endless,
		func(i int) string { return fmt.Sprintf("* %d FETCH (UID %d BODY[] {3}\r\nabc)", i, i+10) })
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := imapsync.FetchUIDs(conn, []uint32{1, 2, 3}, discardSink, func(imapsync.Message[int64]) error {
		t.Error("unrequested message handled")
		return nil
	})
	wantLimit(t, conn, err, imapsync.LimitUnrequested, 0)
}

func flagList(n int) string {
	f := make([]string, n)
	for i := range f {
		f[i] = fmt.Sprintf("k%d", i)
	}
	return strings.Join(f, " ")
}

// More than 1,000 flags on a message trip the cap, in ListFlags and in a
// body fetch; 1,000 are fine.
func TestFlagsCap(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(2); err != nil {
			return err
		}
		tag, err := c.Expect("UID FETCH")
		if err != nil {
			return err
		}
		if err := c.Send(
			"* 1 FETCH (UID 1 FLAGS ("+flagList(imapsync.MaxFlags)+"))",
			"* 2 FETCH (UID 2 FLAGS ("+flagList(imapsync.MaxFlags+1)+"))",
			tag+" OK done"); err != nil {
			return err
		}
		return drain(c)
	})
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	var uids []uint32
	err := imapsync.ListFlags(conn, 0, func(uid uint32, _ []imap.Flag) { uids = append(uids, uid) })
	if uid := wantLimit(t, conn, err, imapsync.LimitFlags, imapsync.MaxFlags); uid != 2 || len(uids) != 1 {
		t.Errorf("UID %d, handled %v", uid, uids)
	}

	s = imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(1); err != nil {
			return err
		}
		tag, err := c.Expect("UID FETCH")
		if err != nil {
			return err
		}
		if err := c.Send("* 1 FETCH (UID 4 FLAGS ("+flagList(imapsync.MaxFlags+1)+") BODY[] {3}", "abc)", tag+" OK done"); err != nil {
			return err
		}
		return drain(c)
	})
	conn = dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err = imapsync.FetchAfter(conn, 0, discardSink, func(imapsync.Message[int64]) error { return nil })
	if uid := wantLimit(t, conn, err, imapsync.LimitFlags, imapsync.MaxFlags); uid != 4 {
		t.Errorf("UID %d, want 4", uid)
	}
}

// A body over 256 MiB stops the fetch before the sink runs; the error
// names the message's UID, and the client reads almost none of it.
func TestMessageSizeCap(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(2); err != nil {
			return err
		}
		if _, err := c.Expect("UID FETCH"); err != nil {
			return err
		}
		if err := c.Send("* 1 FETCH (UID 7 BODY[] {3}", "abc)",
			fmt.Sprintf("* 2 FETCH (UID 9 FLAGS () BODY[] {%d}", imapsync.MaxMessageSize+1)); err != nil {
			return err
		}
		if _, err := flood(c, endless, func(int) string { return strings.Repeat("x", 1000) }); err == nil {
			return errors.New("sent everything")
		}
		return nil
	})
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	var sunk []int64
	var handled []uint32
	err := imapsync.FetchAfter(conn, 0, func(r io.Reader) (int64, error) {
		n, err := io.Copy(io.Discard, r)
		sunk = append(sunk, n)
		return n, err
	}, func(m imapsync.Message[int64]) error {
		handled = append(handled, m.UID)
		return nil
	})
	if uid := wantLimit(t, conn, err, imapsync.LimitMessageSize, imapsync.MaxMessageSize); uid != 9 {
		t.Errorf("UID %d, want 9", uid)
	}
	if len(sunk) != 1 || len(handled) != 1 || handled[0] != 7 {
		t.Errorf("sink saw %v, handled %v; want only UID 7", sunk, handled)
	}
	if n := conn.BytesRead(); n > 1<<20 {
		t.Errorf("read %d bytes of the large message", n)
	}
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
}

// ListHeaders refuses a header section over 128 KiB.
func TestHeaderSizeCap(t *testing.T) {
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
		if err := c.Send(fmt.Sprintf("* 1 FETCH (UID 3 RFC822.SIZE 10 BODY[HEADER.FIELDS (MESSAGE-ID)] {%d}", imapsync.MaxHeaderSize+1)); err != nil {
			return err
		}
		if _, err := flood(c, endless, func(int) string { return strings.Repeat("x", 1000) }); err == nil {
			return errors.New("sent everything")
		}
		return nil
	})
	conn := dialScript(t, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	err := imapsync.ListHeaders(conn, 0, 0, func(imapsync.Header) error { t.Error("header handled"); return nil })
	if uid := wantLimit(t, conn, err, imapsync.LimitHeaderSize, imapsync.MaxHeaderSize); uid != 3 {
		t.Errorf("UID %d", uid)
	}
}

// Config.MaxRead caps what a connection reads in total, even of data our
// code uses.
func TestMaxRead(t *testing.T) {
	s := floodScript(t, nil, "LIST", endless, func(i int) string {
		return fmt.Sprintf(`* LIST () "/" "folder%d"`, i)
	})
	conn := dialScript(t, s, func(c *imapsync.Config) { c.MaxRead = 64 << 10 })
	_, err := conn.ListFolders()
	wantLimit(t, conn, err, imapsync.LimitRead, 64<<10)
	if n := conn.BytesRead(); n != 64<<10 {
		t.Errorf("read %d bytes", n)
	}
}

func discardSink(r io.Reader) (int64, error) { return io.Copy(io.Discard, r) }

// keywords is a FLAGS list with about 1,000 keywords, about 40 KiB.
var keywords = func() string {
	f := make([]string, 1000)
	for i := range f {
		f[i] = fmt.Sprintf("$Keyword-%030d", i)
	}
	return strings.Join(f, " ")
}()

// Legitimate traffic passes: 50 folders, each EXAMINE with about 1,000
// keywords in FLAGS and PERMANENTFLAGS.
func TestManyKeywordsPass(t *testing.T) {
	const folders = 50
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		tag, err := c.Expect("LIST")
		if err != nil {
			return err
		}
		for i := range folders {
			if err := c.Send(fmt.Sprintf(`* LIST () "/" "f%d"`, i)); err != nil {
				return err
			}
		}
		if err := c.Send(tag + " OK done"); err != nil {
			return err
		}
		for range folders {
			tag, err := c.Expect("EXAMINE")
			if err != nil {
				return err
			}
			if err := c.Send("* FLAGS ("+keywords+")", "* OK [PERMANENTFLAGS ("+keywords+` \*)] ok`,
				"* 5 EXISTS", "* OK [UIDVALIDITY 1] ok", tag+" OK [READ-ONLY] done"); err != nil {
				return err
			}
		}
		return c.Serve()
	})
	conn := dialScript(t, s, nil)
	list, err := conn.ListFolders()
	if err != nil || len(list) != folders {
		t.Fatalf("%d folders, %v", len(list), err)
	}
	for _, f := range list {
		if _, err := conn.Examine(f.Name); err != nil {
			t.Fatalf("examine %s: %v", f.Name, err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(t); err != nil {
		t.Fatal(err)
	}
}

// bodyScript serves one message of size bytes in chunks of chunk bytes,
// with TLS if tlsCfg is set.
func bodyScript(t *testing.T, size, chunk int, start func(*imaptest.ScriptConn) error) *imaptest.Script {
	t.Helper()
	return imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if start != nil {
			if err := start(c); err != nil {
				return err
			}
		}
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := c.Examine(1); err != nil {
			return err
		}
		tag, err := c.Expect("UID FETCH")
		if err != nil {
			return err
		}
		if err := c.Send(fmt.Sprintf("* 1 FETCH (UID 1 FLAGS (\\Seen) BODY[] {%d}", size)); err != nil {
			return err
		}
		buf := bytes.Repeat([]byte("x"), chunk)
		for sent := 0; sent < size; sent += chunk {
			if _, err := c.Write(buf[:min(chunk, size-sent)]); err != nil {
				return err
			}
		}
		if err := c.Send(")", tag+" OK done"); err != nil {
			return err
		}
		return c.Serve()
	})
}

func fetchBody(t *testing.T, conn *imapsync.Conn) int64 {
	t.Helper()
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	var got int64
	err := imapsync.FetchAfter(conn, 0, discardSink, func(m imapsync.Message[int64]) error {
		got = m.Body
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A 32 MiB body crosses the budget's maximum many times and passes.
func TestLargeBodyPasses(t *testing.T) {
	const size = 32 << 20
	s := bodyScript(t, size, 64<<10, nil)
	conn := dialScript(t, s, nil)
	if got := fetchBody(t, conn); got != size {
		t.Fatalf("body %d bytes, want %d", got, size)
	}
}

// Over TLS in 512-byte records, the record overhead is counted below TLS;
// the 5/4 body credit covers it.
func TestLargeBodyTLSSmallRecords(t *testing.T) {
	const size = 32 << 20
	serverTLS, clientTLS := imaptest.TLSConfigs(t)
	s := bodyScript(t, size, 512, func(c *imaptest.ScriptConn) error { return c.StartTLS(serverTLS) })
	conn := dialScript(t, s, func(c *imapsync.Config) {
		c.TLSMode = imapsync.TLSModeTLS
		c.TLSConfig = clientTLS
	})
	if got := fetchBody(t, conn); got != size {
		t.Fatalf("body %d bytes, want %d", got, size)
	}
	if n := conn.BytesRead(); n < size+size/32 {
		t.Errorf("read %d bytes for a %d byte body: records were not small", n, size)
	}
}
