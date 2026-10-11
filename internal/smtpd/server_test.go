package smtpd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestDecode(t *testing.T) {
	for _, tc := range []struct{ in, base, original string }{
		{"archive@archive.local", "archive@archive.local", "archive@archive.local"},
		{"archive+alice=example.com@archive.local", "archive@archive.local", "alice@example.com"},
		{"archive+alice+tag=example.com@archive.local", "archive@archive.local", "alice+tag@example.com"},
		{"archive+a=b=example.com@archive.local", "archive@archive.local", "a=b@example.com"},
		{"archive+noequals@archive.local", "archive@archive.local", "archive+noequals@archive.local"},
		{"archive+=example.com@archive.local", "archive@archive.local", "archive+=example.com@archive.local"},
		{"archive+alice=@archive.local", "archive@archive.local", "archive+alice=@archive.local"},
		{"no-at-sign", "no-at-sign", "no-at-sign"},
	} {
		base, original := Decode(tc.in)
		if base != tc.base || original != tc.original {
			t.Errorf("Decode(%q) = %q, %q; want %q, %q", tc.in, base, original, tc.base, tc.original)
		}
	}
}

// syncBuffer is a log sink for concurrent writers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type fixture struct {
	t       *testing.T
	st      *store.Store
	srv     *Server
	addr    string
	logs    *syncBuffer
	owner   int64
	journal *store.Account
}

func newFixture(t *testing.T, configure func(*Server)) *fixture {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	srv := &Server{
		Store: st, Blobs: blobs, Logger: slog.New(slog.NewTextHandler(logs, nil)),
		Allowed: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, Hostname: "archive.test",
	}
	if configure != nil {
		configure(srv)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(sctx, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	f := &fixture{t: t, st: st, srv: srv, addr: l.Addr().String(), logs: logs, owner: u.ID}
	f.journal = f.account("journal", "archive@archive.test")
	return f
}

func (f *fixture) account(name string, addrs ...string) *store.Account {
	f.t.Helper()
	a := &store.Account{Name: name, Enabled: true, OwnerID: &f.owner}
	if err := f.st.CreateSMTPAccount(context.Background(), a, addrs); err != nil {
		f.t.Fatal(err)
	}
	return a
}

func (f *fixture) dial() *smtp.Client {
	f.t.Helper()
	c, err := smtp.Dial(f.addr)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = c.Close() })
	if err := c.Hello("mx.example.com"); err != nil {
		f.t.Fatal(err)
	}
	return c
}

// send delivers one message and returns the first error with its stage,
// also a refused connection, so that callers can retry.
func (f *fixture) send(from string, rcpts []string, body string) error {
	f.t.Helper()
	c, err := smtp.Dial(f.addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("mx.example.com"); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	defer func() { _ = c.Quit() }()
	if err := c.Mail(from, nil); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r, nil); err != nil {
			return fmt.Errorf("rcpt %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("end of data: %w", err)
	}
	return nil
}

func code(err error) int {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

func message(subject string) string {
	return "From: sender@example.com\r\nTo: alice@example.com\r\nSubject: " + subject +
		"\r\nMessage-ID: <" + subject + "@example.com>\r\n\r\nHello.\r\n"
}

// envelopes returns the recipients per account of the message with the subject.
func (f *fixture) envelopes(subject string) map[string][]string {
	f.t.Helper()
	ctx := context.Background()
	res, err := f.st.SearchMessages(ctx, store.SearchFilter{Owner: f.owner, Query: subject})
	if err != nil {
		f.t.Fatal(err)
	}
	if len(res) != 1 {
		f.t.Fatalf("%d messages with subject %q", len(res), subject)
	}
	out := map[string][]string{}
	for _, m := range res {
		d, err := f.st.GetMessageDetail(ctx, f.owner, m.SHA256)
		if err != nil {
			f.t.Fatal(err)
		}
		for _, l := range d.Locations {
			if l.Envelope == nil {
				continue
			}
			if _, dup := out[l.Account]; dup {
				f.t.Errorf("account %s has two locations of %q", l.Account, subject)
			}
			out[l.Account] = l.Envelope.RcptTo
		}
	}
	return out
}

func TestReceive(t *testing.T) {
	f := newFixture(t, nil)
	f.account("other", "other@archive.test")

	err := f.send("sender@example.com", []string{
		"archive+alice=example.com@archive.test",
		"ARCHIVE+bob=example.com@Archive.Test",
		"other@archive.test",
	}, message("quokka"))
	if err != nil {
		t.Fatal(err)
	}
	got := f.envelopes("quokka")
	if !slices.Equal(got["journal"], []string{"alice@example.com", "bob@example.com"}) || !slices.Equal(got["other"], []string{"other@archive.test"}) {
		t.Fatalf("envelopes %v", got)
	}

	// Postfix splits more than 50 recipients into transactions with the
	// same bytes, and retries the recipients that got 451: one location
	// with all recipients.
	if err := f.send("sender@example.com", []string{"archive+carol=example.com@archive.test"}, message("quokka")); err != nil {
		t.Fatal(err)
	}
	if err := f.send("sender@example.com", []string{"archive+alice=example.com@archive.test"}, message("quokka")); err != nil {
		t.Fatal(err)
	}
	got = f.envelopes("quokka")
	if !slices.Equal(got["journal"], []string{"alice@example.com", "bob@example.com", "carol@example.com"}) {
		t.Fatalf("merged envelope %v", got["journal"])
	}

	// always_bcc: the archive address itself is the recipient.
	if err := f.send("", []string{"archive@archive.test"}, message("wombat")); err != nil {
		t.Fatal(err)
	}
	if got := f.envelopes("wombat"); !slices.Equal(got["journal"], []string{"archive@archive.test"}) {
		t.Fatalf("plain copy %v", got)
	}
	if strings.Contains(f.logs.String(), "level=ERROR") {
		t.Errorf("errors logged:\n%s", f.logs)
	}
}

func TestReceiveRefusals(t *testing.T) {
	f := newFixture(t, func(s *Server) { s.MaxSize = 1000 })
	ctx := context.Background()

	err := f.send("sender@example.com", []string{"nobody@archive.test"}, message("koala"))
	if code(err) != 550 {
		t.Errorf("unknown address: %v", err)
	}
	err = f.send("sender@example.com", []string{"archive@archive.test"}, message("dingo")+strings.Repeat("xxxxxxxxx\r\n", 200))
	if code(err) != 552 {
		t.Errorf("too large: %v", err)
	}

	off := false
	if err := f.st.UpdateAccount(ctx, f.journal.Ref(), store.AccountChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	err = f.send("sender@example.com", []string{"archive@archive.test"}, message("bilby"))
	if code(err) != 451 {
		t.Errorf("disabled account: %v", err)
	}

	logs := f.logs.String()
	for _, want := range []string{
		`level=ERROR msg="smtp recipient refused" code=550 client_ip=127.0.0.1 sender=sender@example.com recipients=[nobody@archive.test]`,
		`level=ERROR msg="smtp message refused" client_ip=127.0.0.1 sender=sender@example.com recipients=[archive@archive.test] code=552`,
		`level=WARN msg="smtp recipient deferred" code=451`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "Hello.") {
		t.Error("message content logged")
	}
}

func TestReceiveRefusesClients(t *testing.T) {
	f := newFixture(t, func(s *Server) {
		s.MaxConns = 1
	})
	first := f.dial()
	_ = first

	c, err := smtp.Dial(f.addr)
	if err == nil {
		err = c.Hello("mx.example.com")
		_ = c.Close()
	}
	if code(err) != 421 {
		t.Errorf("over the connection limit: %v", err)
	}
	_ = first.Quit()
	// The slot is free again.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := f.send("sender@example.com", []string{"archive@archive.test"}, message("emu"))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the first connection ended: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	g := newFixture(t, func(s *Server) {
		s.Allowed = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	})
	c, err = smtp.Dial(g.addr)
	if err == nil {
		err = c.Hello("mx.example.com")
		_ = c.Close()
	}
	if code(err) != 554 {
		t.Errorf("client outside the allowlist: %v", err)
	}
	if !strings.Contains(g.logs.String(), `level=ERROR msg="smtp connection refused" code=554 client_ip=127.0.0.1`) {
		t.Errorf("refusal not logged:\n%s", g.logs)
	}
}

func TestReceiveCommitFailure(t *testing.T) {
	var fail sync.Map // account ID -> true
	f := newFixture(t, func(s *Server) {
		s.receive = func(ctx context.Context, account int64, meta store.MessageMeta, env store.Envelope, fileExists func() bool) (store.ReceiveResult, error) {
			if _, ok := fail.Load(account); ok {
				return store.ReceiveResult{}, errors.New("commit failed")
			}
			return s.Store.ReceiveMessage(ctx, account, meta, env, fileExists)
		}
	})
	other := f.account("other", "other@archive.test")

	// The first account commits, the second fails: 451 for the whole
	// message, and the retry stores it once for each account.
	fail.Store(other.ID, true)
	rcpts := []string{"archive+alice=example.com@archive.test", "other@archive.test"}
	if err := f.send("sender@example.com", rcpts, message("numbat")); code(err) != 451 {
		t.Fatalf("commit failure: %v", err)
	}
	fail.Delete(other.ID)
	if err := f.send("sender@example.com", rcpts, message("numbat")); err != nil {
		t.Fatal(err)
	}
	got := f.envelopes("numbat")
	if !slices.Equal(got["journal"], []string{"alice@example.com"}) || !slices.Equal(got["other"], []string{"other@archive.test"}) {
		t.Fatalf("after retry %v", got)
	}
	if !strings.Contains(f.logs.String(), `msg="smtp message not stored"`) {
		t.Errorf("failure not logged:\n%s", f.logs)
	}
}

// verify --fix-orphans runs between Put and the commit: it finds the new
// file without a row and moves it. The delivery must not acknowledge a
// message whose file is gone; the retry stores it once, with its file.
func TestReceiveVerifyBetweenPutAndCommit(t *testing.T) {
	var verified atomic.Bool
	f := newFixture(t, func(s *Server) {
		s.receive = func(ctx context.Context, account int64, meta store.MessageMeta, env store.Envelope, fileExists func() bool) (store.ReceiveResult, error) {
			if !verified.Swap(true) {
				res, err := archive.Verify(ctx, s.Store, s.Blobs, archive.VerifyOptions{FixOrphans: true, Logger: s.Logger})
				if err != nil || res.Moved != 1 {
					t.Errorf("verify: %+v, %v", res, err)
				}
			}
			return s.Store.ReceiveMessage(ctx, account, meta, env, fileExists)
		}
	})
	rcpts := []string{"archive+alice=example.com@archive.test"}
	if err := f.send("sender@example.com", rcpts, message("platypus")); code(err) != 451 {
		t.Fatalf("delivery after verify moved the file: %v", err)
	}
	if err := f.send("sender@example.com", rcpts, message("platypus")); err != nil {
		t.Fatal(err)
	}
	if got := f.envelopes("platypus"); !slices.Equal(got["journal"], []string{"alice@example.com"}) {
		t.Fatalf("after retry %v", got)
	}
	f.assertFilesPresent()
}

// verify --fix-orphans running again and again next to deliveries never
// moves the file of a committed message.
func TestReceiveAlongsideVerify(t *testing.T) {
	f := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	// Stop verify before the test's directories go, also on failure.
	defer func() {
		cancel()
		wg.Wait()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		var n atomic.Int64
		for ctx.Err() == nil {
			// verify runs again and again, but not without a pause: back to
			// back, it could catch every retry between Put and commit.
			time.Sleep(20 * time.Millisecond)
			// A new orphans/ directory per run: runs within one second
			// would otherwise share it.
			now := time.Unix(1_800_000_000+n.Add(1), 0)
			_, err := archive.Verify(ctx, f.st, f.srv.Blobs, archive.VerifyOptions{
				FixOrphans: true, LockTimeout: time.Second, Logger: f.srv.Logger, Now: func() time.Time { return now },
			})
			if err != nil && ctx.Err() == nil {
				t.Errorf("verify: %v", err)
				return
			}
		}
	}()
	for i := range 30 {
		subject := fmt.Sprintf("wallaby%d", i)
		for attempt := 0; ; attempt++ {
			err := f.send("sender@example.com", []string{"archive@archive.test"}, message(subject))
			if err == nil {
				break
			}
			// Like Postfix: a 451 is delivered again.
			if code(err) != 451 || attempt == 10 {
				t.Fatalf("delivery %d: %v", i, err)
			}
		}
	}
	cancel()
	wg.Wait()
	f.assertFilesPresent()
}

// assertFilesPresent checks that every message row has its file.
func (f *fixture) assertFilesPresent() {
	f.t.Helper()
	res, err := archive.Verify(context.Background(), f.st, f.srv.Blobs, archive.VerifyOptions{Logger: f.srv.Logger})
	if err != nil {
		f.t.Fatal(err)
	}
	if n := res.Findings[archive.FindingMissing]; n != 0 {
		f.t.Errorf("%d committed messages lost their file: %+v", n, res.Findings)
	}
	if res.Checked == 0 {
		f.t.Error("verify checked no messages")
	}
}
