package imapsync

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestSpecialUse(t *testing.T) {
	cases := []struct {
		attrs []string
		want  string
	}{
		{[]string{`\HasNoChildren`, `\Trash`}, RoleTrash},
		{[]string{`\junk`}, RoleJunk},
		{[]string{`\Sent`}, RoleSent},
		{[]string{`\All`}, RoleAll},
		{[]string{`\HasChildren`, `\Marked`}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := (Folder{Name: "x", Attrs: c.attrs}).SpecialUse(); got != c.want {
			t.Errorf("SpecialUse(%v) = %q, want %q", c.attrs, got, c.want)
		}
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer for the server's debug output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// listTraffic connects to a test server with the given capabilities, lists
// folders and returns the raw traffic.
func listTraffic(t *testing.T, caps imap.CapSet) string {
	t.Helper()
	user := imapmemserver.NewUser("u", "pw")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem := imapmemserver.New()
	mem.AddUser(user)
	traffic := &syncBuffer{}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         caps,
		InsecureAuth: true,
		DebugWriter:  traffic,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	addr := ln.Addr().(*net.TCPAddr)
	conn, err := Dial(context.Background(), Config{
		Host: addr.IP.String(), Port: addr.Port, TLSMode: TLSModeNone, Username: "u", Password: "pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	folders, err := conn.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Name != "INBOX" {
		t.Fatalf("folders = %+v", folders)
	}
	return traffic.String()
}

func TestListFoldersRequestsSpecialUse(t *testing.T) {
	const request = "RETURN (SPECIAL-USE)"
	withExt := listTraffic(t, imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapListExtended: {}, imap.CapSpecialUse: {}})
	if !strings.Contains(withExt, request) {
		t.Errorf("expected LIST with %s, traffic:\n%s", request, withExt)
	}
	plain := listTraffic(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	if strings.Contains(plain, request) {
		t.Errorf("LIST-EXTENDED not supported, but client sent %s", request)
	}
}

// flagServer serves INBOX with three messages; the second is \Flagged.
// With drop set, FETCH leaves out that UID but EXAMINE still counts it.
func flagServer(t *testing.T, drop imap.UID) (*Conn, *syncBuffer) {
	t.Helper()
	user := imapmemserver.NewUser("u", "pw")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	if err := user.Create("Empty", nil); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		opts := &imap.AppendOptions{}
		if i == 1 {
			opts.Flags = []imap.Flag{imap.FlagFlagged, imap.FlagSeen}
		}
		raw := []byte("Subject: m\r\n\r\nbody\r\n")
		if _, err := user.Append("INBOX", bytes.NewReader(raw), opts); err != nil {
			t.Fatal(err)
		}
	}
	mem := imapmemserver.New()
	mem.AddUser(user)
	traffic := &syncBuffer{}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &dropSession{Session: mem.NewSession(), drop: drop}, nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
		DebugWriter:  traffic,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	addr := ln.Addr().(*net.TCPAddr)
	conn, err := Dial(context.Background(), Config{
		Host: addr.IP.String(), Port: addr.Port, TLSMode: TLSModeNone, Username: "u", Password: "pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, traffic
}

type dropSession struct {
	imapserver.Session
	drop imap.UID
}

func (s *dropSession) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	if s.drop != 0 {
		var set imap.UIDSet
		set.AddRange(1, s.drop-1)
		set.AddRange(s.drop+1, 1000)
		numSet = set
	}
	return s.Session.Fetch(w, numSet, options)
}

func TestListFlags(t *testing.T) {
	conn, traffic := flagServer(t, 0)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	got := map[uint32][]imap.Flag{}
	if err := ListFlags(conn, func(uid uint32, flags []imap.Flag) { got[uid] = flags }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(got[1]) != 0 || len(got[2]) != 2 {
		t.Fatalf("flags = %v", got)
	}
	out := traffic.String()
	if !strings.Contains(out, "UID FETCH 1:* (UID FLAGS)") {
		t.Errorf("want UID FETCH 1:* (UID FLAGS), traffic:\n%s", out)
	}
	if strings.Contains(out, "BODY") || strings.Contains(out, "STORE") {
		t.Errorf("listing flags must not fetch bodies or store flags:\n%s", out)
	}
}

func TestListFlagsIncomplete(t *testing.T) {
	conn, _ := flagServer(t, 2)
	if _, err := conn.Examine("INBOX"); err != nil {
		t.Fatal(err)
	}
	n := 0
	err := ListFlags(conn, func(uint32, []imap.Flag) { n++ })
	if !errors.Is(err, ErrIncomplete) || n != 2 {
		t.Fatalf("err = %v after %d messages, want ErrIncomplete", err, n)
	}
}

func TestListFlagsEmptyFolder(t *testing.T) {
	conn, traffic := flagServer(t, 0)
	if _, err := conn.Examine("Empty"); err != nil {
		t.Fatal(err)
	}
	if err := ListFlags(conn, func(uint32, []imap.Flag) {}); err == nil {
		t.Fatal("ListFlags on an empty folder succeeded")
	}
	if strings.Contains(traffic.String(), "FETCH") {
		t.Errorf("sent FETCH to an empty folder:\n%s", traffic.String())
	}
}
