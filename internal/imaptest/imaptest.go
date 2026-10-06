// Package imaptest runs an in-memory IMAP server for tests.
package imaptest

import (
	"bytes"
	"net"
	"sort"
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
	return start(t, nil, users...)
}

// StartWithRoles is like Start, but LIST answers with exactly the given
// mailboxes and their special-use attributes ("" for none), because
// imapmemserver cannot mark mailboxes as trash or junk. Create the same
// mailboxes for the users so that they can be selected.
func StartWithRoles(t testing.TB, mailboxes map[string]imap.MailboxAttr, users ...*imapmemserver.User) (host string, port int) {
	t.Helper()
	return start(t, mailboxes, users...)
}

func start(t testing.TB, roles map[string]imap.MailboxAttr, users ...*imapmemserver.User) (string, int) {
	t.Helper()
	mem := imapmemserver.New()
	for _, u := range users {
		mem.AddUser(u)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			if roles != nil {
				return &rolesSession{Session: mem.NewSession(), roles: roles}, nil, nil
			}
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

// rolesSession answers LIST with a fixed set of mailboxes and attributes.
type rolesSession struct {
	imapserver.Session
	roles map[string]imap.MailboxAttr
}

func (s *rolesSession) List(w *imapserver.ListWriter, _ string, patterns []string, _ *imap.ListOptions) error {
	if len(patterns) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.roles))
	for name := range s.roles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := &imap.ListData{Mailbox: name, Delim: '/'}
		if role := s.roles[name]; role != "" {
			data.Attrs = []imap.MailboxAttr{role}
		}
		if err := w.WriteList(data); err != nil {
			return err
		}
	}
	return nil
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
