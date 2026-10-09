// Package imaptest runs an in-memory IMAP server for tests.
package imaptest

import (
	"bytes"
	"errors"
	"math"
	"net"
	"slices"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// Start serves the users on a random local port until the test ends.
// Connect without TLS.
func Start(t testing.TB, users ...*imapmemserver.User) (host string, port int) {
	t.Helper()
	return start(t, nil, users...)
}

// Server is a test server that records the commands it receives and can
// hide messages, so that a message can disappear and come back under the
// same UID (a real server never reuses a UID).
type Server struct {
	Host string
	Port int

	mu       sync.Mutex
	commands []string
	hidden   map[string][]imap.UID // mailbox -> sorted UIDs
	failFlag map[string]bool
}

// StartServer serves the users like Start and returns the Server.
func StartServer(t testing.TB, users ...*imapmemserver.User) *Server {
	t.Helper()
	s := &Server{hidden: map[string][]imap.UID{}, failFlag: map[string]bool{}}
	s.Host, s.Port = serve(t, func(mem *imapmemserver.Server) imapserver.Session {
		return &recordingSession{Session: mem.NewSession(), srv: s}
	}, users...)
	return s
}

// Hide makes the server leave out a message: EXAMINE counts one less and
// FETCH skips its UID. Hide only existing messages, and not the one with
// the highest UID.
func (s *Server) Hide(mailbox string, uid uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hidden[mailbox]
	if !slices.Contains(h, imap.UID(uid)) {
		h = append(h, imap.UID(uid))
		slices.Sort(h)
		s.hidden[mailbox] = h
	}
}

// Unhide shows a hidden message again.
func (s *Server) Unhide(mailbox string, uid uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hidden[mailbox] = slices.DeleteFunc(s.hidden[mailbox], func(u imap.UID) bool { return u == imap.UID(uid) })
}

// FailFlagFetch makes a FETCH without body in the mailbox answer the first
// message and then fail, like a connection that breaks mid-stream.
func (s *Server) FailFlagFetch(mailbox string, fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failFlag[mailbox] = fail
}

// ResetCommands forgets the commands received so far.
func (s *Server) ResetCommands() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = nil
}

// Commands returns the commands received so far, like "EXAMINE INBOX",
// "UID FETCH FLAGS" or "UID FETCH BODY.PEEK".
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commands)
}

func (s *Server) record(cmd string) {
	s.mu.Lock()
	s.commands = append(s.commands, cmd)
	s.mu.Unlock()
}

func (s *Server) hiddenIn(mailbox string) []imap.UID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.hidden[mailbox])
}

// recordingSession logs commands and applies the hidden messages.
type recordingSession struct {
	imapserver.Session
	srv      *Server
	selected string
}

func (s *recordingSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	verb := "SELECT"
	if options != nil && options.ReadOnly {
		verb = "EXAMINE"
	}
	s.srv.record(verb + " " + mailbox)
	data, err := s.Session.Select(mailbox, options)
	if err != nil {
		return nil, err
	}
	s.selected = mailbox
	hidden := uint32(len(s.srv.hiddenIn(mailbox))) //nolint:gosec // a few test UIDs
	data.NumMessages -= min(hidden, data.NumMessages)
	return data, nil
}

func (s *recordingSession) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	what := "FLAGS"
	for _, b := range options.BodySection {
		what = "BODY"
		if b.Peek {
			what = "BODY.PEEK"
		}
	}
	uids, isUID := numSet.(imap.UIDSet)
	if isUID {
		s.srv.record("UID FETCH " + what)
		numSet = without(uids, s.srv.hiddenIn(s.selected))
	} else {
		s.srv.record("FETCH " + what)
	}
	s.srv.mu.Lock()
	fail := what == "FLAGS" && s.srv.failFlag[s.selected]
	s.srv.mu.Unlock()
	if fail {
		_ = s.Session.Fetch(w, imap.SeqSetNum(1), options)
		return errors.New("simulated failure")
	}
	return s.Session.Fetch(w, numSet, options)
}

func (s *recordingSession) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	s.srv.record("STORE")
	return s.Session.Store(w, numSet, flags, options)
}

func (s *recordingSession) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	s.srv.record("EXPUNGE")
	return s.Session.Expunge(w, uids)
}

func (s *recordingSession) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	s.srv.record("COPY")
	return s.Session.Copy(numSet, dest)
}

func (s *recordingSession) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	s.srv.record("APPEND " + mailbox)
	return s.Session.Append(mailbox, r, options)
}

// without removes hidden UIDs from a UID set. "*" becomes the highest
// possible UID, so the server does not add its last message (RFC 3501
// makes "n:*" include it even if its UID is below n).
func without(set imap.UIDSet, hidden []imap.UID) imap.UIDSet {
	var out imap.UIDSet
	for _, r := range set {
		start, stop := r.Start, r.Stop
		if stop == 0 {
			stop = math.MaxUint32
		}
		if start == 0 {
			start = math.MaxUint32
		}
		if start > stop {
			start, stop = stop, start
		}
		for _, h := range hidden {
			if h < start || h > stop {
				continue
			}
			if h > start {
				out.AddRange(start, h-1)
			}
			start = h + 1
		}
		if start <= stop && start != 0 {
			out.AddRange(start, stop)
		}
	}
	return out
}

// Client logs in as a separate client, to change the server like a mail
// program would. Close it when done.
func Client(t testing.TB, host string, port int, user, password string) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialInsecure(net.JoinHostPort(host, strconv.Itoa(port)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(user, password).Wait(); err != nil {
		t.Fatal(err)
	}
	return c
}

// Expunge deletes the messages with the given UIDs from a mailbox through
// a separate client connection.
func Expunge(t testing.TB, host string, port int, user, password, mailbox string, uids ...uint32) {
	t.Helper()
	c := Client(t, host, port, user, password)
	defer func() { _ = c.Close() }()
	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	set := uidSet(uids)
	store := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
	if err := c.Store(set, store, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.UIDExpunge(set).Close(); err != nil {
		t.Fatal(err)
	}
}

// SetFlags replaces the flags of one message through a separate client
// connection.
func SetFlags(t testing.TB, host string, port int, user, password, mailbox string, uid uint32, flags ...imap.Flag) {
	t.Helper()
	c := Client(t, host, port, user, password)
	defer func() { _ = c.Close() }()
	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	store := &imap.StoreFlags{Op: imap.StoreFlagsSet, Silent: true, Flags: flags}
	if err := c.Store(uidSet([]uint32{uid}), store, nil).Close(); err != nil {
		t.Fatalf("store flags: %v", err)
	}
}

func uidSet(uids []uint32) imap.UIDSet {
	var set imap.UIDSet
	for _, u := range uids {
		set.AddNum(imap.UID(u))
	}
	return set
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
	return serve(t, func(mem *imapmemserver.Server) imapserver.Session {
		if roles != nil {
			return &rolesSession{Session: mem.NewSession(), roles: roles}
		}
		return mem.NewSession()
	}, users...)
}

func serve(t testing.TB, session func(*imapmemserver.Server) imapserver.Session, users ...*imapmemserver.User) (string, int) {
	t.Helper()
	mem := imapmemserver.New()
	for _, u := range users {
		mem.AddUser(u)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return session(mem), nil, nil
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
