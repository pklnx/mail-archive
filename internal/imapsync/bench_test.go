package imapsync_test

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/imaptest"
)

// The benchmarks measure how fast go-imap decodes the responses that cost
// the most CPU per byte, through the metered connection: the figures for
// the CPU bounds in the operations docs (bytes/s is the wire rate).

// benchScript answers LOGIN, an EXAMINE of exists messages if exists > 0,
// and then every command starting with verb with answer.
func benchScript(b *testing.B, exists int, verb, answer string) *imaptest.Script {
	b.Helper()
	return imaptest.StartScript(b, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if exists > 0 {
			if err := c.Examine(exists); err != nil {
				return err
			}
		}
		for {
			tag, line, err := c.Next()
			if err != nil {
				return nil //nolint:nilerr // the client closed
			}
			if !strings.HasPrefix(line, verb) {
				return c.Send("* BYE", tag+" OK bye")
			}
			if _, err := io.WriteString(c, answer+tag+" OK done\r\n"); err != nil {
				return err
			}
		}
	})
}

// BenchmarkDecodeList decodes a LIST of 10,000 folders.
func BenchmarkDecodeList(b *testing.B) {
	const folders = 10_000
	var sb strings.Builder
	for i := range folders {
		fmt.Fprintf(&sb, "* LIST (\\HasNoChildren) \"/\" \"Archive/2026/Folder %05d\"\r\n", i)
	}
	answer := sb.String()
	s := benchScript(b, 0, "LIST", answer)
	conn := dialScript(b, s, nil)
	b.SetBytes(int64(len(answer)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if list, err := conn.ListFolders(); err != nil || len(list) != folders {
			b.Fatalf("%d folders, %v", len(list), err)
		}
	}
}

// BenchmarkDecodeFlags decodes a flag listing of 100,000 messages.
func BenchmarkDecodeFlags(b *testing.B) {
	const messages = 100_000
	var sb strings.Builder
	for i := 1; i <= messages; i++ {
		fmt.Fprintf(&sb, "* %d FETCH (UID %d FLAGS (\\Seen $Label1))\r\n", i, i)
	}
	answer := sb.String()
	s := benchScript(b, messages, "UID FETCH", answer)
	conn := dialScript(b, s, nil)
	if _, err := conn.Examine("INBOX"); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(answer)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := imapsync.ListFlags(conn, 0, func(uint32, []imap.Flag) {}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeNotices decodes 100,000 unsolicited EXPUNGE notices
// during a FETCH on a fresh connection: the traffic a connection may get
// credit for, up to a million notices.
func BenchmarkDecodeNotices(b *testing.B) {
	const notices = 100_000
	var sb strings.Builder
	for range notices {
		sb.WriteString("* 1 EXPUNGE\r\n")
	}
	answer := sb.String()
	s := benchScript(b, notices+1, "UID FETCH", answer)
	b.SetBytes(int64(len(answer)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		conn := dialScript(b, s, nil)
		if _, err := conn.Examine("INBOX"); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := imapsync.FetchAfter(conn, 0, discardSink, func(imapsync.Message[int64]) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}
