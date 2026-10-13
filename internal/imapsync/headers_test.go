package imapsync_test

import (
	"context"
	"fmt"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/imaptest"
)

func dial(t *testing.T, srv *imaptest.Server) *imapsync.Conn {
	t.Helper()
	conn, err := imapsync.Dial(context.Background(), imapsync.Config{
		Host: srv.Host, Port: srv.Port, TLSMode: imapsync.TLSModeNone, Username: "u", Password: "pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func msg(id string) []byte {
	return fmt.Appendf(nil, "From: a@example.com\r\nMessage-ID: <%s>\r\nSubject: s\r\n\r\nbody %s\r\n", id, id)
}

func TestListHeaders(t *testing.T) {
	u := imapmemserver.NewUser("u", "pw")
	imaptest.CreateMailboxes(t, u, "INBOX", "Empty")
	berlin := time.FixedZone("CEST", 2*3600)
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, berlin)
	for i := range 3 {
		imaptest.AppendAt(t, u, "INBOX", msg(fmt.Sprintf("m%d@example.com", i+1)), at.Add(time.Duration(i)*time.Hour))
	}
	srv := imaptest.StartServer(t, u)
	conn := dial(t, srv)

	if _, err := conn.Examine("Empty"); err != nil {
		t.Fatal(err)
	}
	srv.ResetCommands()
	if err := imapsync.ListHeaders(conn, 0, func(imapsync.Header) error { t.Error("header in an empty folder"); return nil }); err != nil {
		t.Fatal(err)
	}
	if c := srv.Commands(); len(c) != 0 {
		t.Errorf("commands on an empty folder: %v", c)
	}

	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	srv.ResetCommands()
	var got []imapsync.Header
	if err := imapsync.ListHeaders(conn, 1, func(h imapsync.Header) error { got = append(got, h); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].UID != 2 || got[1].UID != 3 {
		t.Fatalf("headers %+v", got)
	}
	if string(got[0].MessageID) != "Message-ID: <m2@example.com>\r\n\r\n" {
		t.Errorf("message id %q", got[0].MessageID)
	}
	if !got[0].InternalDate.Equal(at.Add(time.Hour)) {
		t.Errorf("internal date %v, want %v", got[0].InternalDate, at.Add(time.Hour))
	}
	if c := srv.Commands(); !slices.Equal(c, []string{"UID FETCH HEADER"}) || len(srv.BodyFetches()) != 0 {
		t.Errorf("commands %v, body fetches %v", c, srv.BodyFetches())
	}

	// "4:*" answers the last message (UID 3); it is not listed.
	got = nil
	if err := imapsync.ListHeaders(conn, 3, func(h imapsync.Header) error { got = append(got, h); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("after the last UID: %+v", got)
	}
}

func TestFetchUIDs(t *testing.T) {
	u := imapmemserver.NewUser("u", "pw")
	imaptest.CreateMailboxes(t, u, "INBOX")
	for i := range 4 {
		imaptest.Append(t, u, "INBOX", msg(fmt.Sprintf("m%d@example.com", i+1)))
	}
	srv := imaptest.StartServer(t, u)
	conn := dial(t, srv)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	srv.ResetCommands()
	sink := func(r io.Reader) (string, error) {
		b, err := io.ReadAll(r)
		return string(b), err
	}
	var uids []uint32
	err := imapsync.FetchUIDs(conn, []uint32{2, 4, 9}, sink, func(m imapsync.Message[string]) error {
		uids = append(uids, m.UID)
		if m.InternalDate.IsZero() || len(m.Body) == 0 {
			t.Errorf("message %+v", m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(uids, []uint32{2, 4}) {
		t.Errorf("fetched %v", uids)
	}
	if f := srv.BodyFetches(); !slices.Equal(f, []string{"2,4,9"}) {
		t.Errorf("body fetches %v", f)
	}
	if err := imapsync.FetchUIDs(conn, nil, sink, func(imapsync.Message[string]) error { return nil }); err != nil {
		t.Errorf("no UIDs: %v", err)
	}
}
