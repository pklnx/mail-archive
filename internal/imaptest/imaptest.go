// Package imaptest runs an in-memory IMAP server for tests.
package imaptest

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// Start serves the users on a random local port until the test ends.
// Connect without TLS.
func Start(t testing.TB, users ...*imapmemserver.User) (host string, port int) {
	t.Helper()
	mem := imapmemserver.New()
	for _, u := range users {
		mem.AddUser(u)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
		Logger:       discard{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// CreateMailboxes creates the named mailboxes for u.
func CreateMailboxes(t testing.TB, u *imapmemserver.User, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := u.Create(n, nil); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
}

// Append stores a raw message in a mailbox of u.
func Append(t testing.TB, u *imapmemserver.User, mailbox string, raw []byte) {
	t.Helper()
	// bytes.Reader implements imap.LiteralReader (Read + Size).
	if _, err := u.Append(mailbox, bytes.NewReader(raw), &imap.AppendOptions{Time: time.Now()}); err != nil {
		t.Fatalf("append to %s: %v", mailbox, err)
	}
}

type discard struct{}

func (discard) Printf(string, ...any) {}
