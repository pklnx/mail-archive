package imapsync

import (
	"bytes"
	"context"
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
