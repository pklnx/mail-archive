package imapsync_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/keepia/archive/internal/imapsync"
	"github.com/keepia/archive/internal/imaptest"
)

// A sink that stops the fetch (the storage gate, #397) ends it at once:
// the connection is closed instead of downloading and dropping the rest of
// the folder, and the fetch returns the sink's error as it is. An ordinary
// sink error still drains the rest and keeps the connection.
func TestFetchStop(t *testing.T) {
	const n, size = 20, 200 << 10
	u := imapmemserver.NewUser("u", "pw")
	imaptest.CreateMailboxes(t, u, "INBOX")
	for i := range n {
		body := append(msg(fmt.Sprintf("m%d@example.com", i)), bytes.Repeat([]byte("x"), size)...)
		imaptest.Append(t, u, "INBOX", body)
	}
	srv := imaptest.StartServer(t, u)
	errFull := errors.New("storage full")
	for _, tc := range []struct {
		name   string
		wrap   func(error) error
		broken bool
	}{
		{"stop", imapsync.Stop, true},
		{"ordinary error", func(err error) error { return err }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := dial(t, srv)
			if _, err := conn.Examine("INBOX"); err != nil {
				t.Fatal(err)
			}
			calls := 0
			sink := func(r io.Reader) (int, error) {
				calls++
				if calls == 2 {
					return 0, tc.wrap(errFull)
				}
				b, err := io.ReadAll(r)
				return len(b), err
			}
			before := conn.BytesRead()
			err := imapsync.FetchAfter(conn, 0, sink, func(imapsync.Message[int]) error { return nil })
			read := conn.BytesRead() - before
			if !errors.Is(err, errFull) {
				t.Fatalf("err = %v", err)
			}
			if conn.Broken() != tc.broken {
				t.Fatalf("broken = %v", conn.Broken())
			}
			if tc.broken {
				if err != errFull { //nolint:errorlint // returned as it is
					t.Fatalf("err %v is wrapped", err)
				}
				if read > 8*size {
					t.Fatalf("read %d bytes after the stop; the folder has %d", read, n*size)
				}
			} else if read < n*size {
				t.Fatalf("an ordinary error read only %d bytes; it drains the rest", read)
			}
		})
	}
}
