package archive_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
)

// lossFixture: account "mail" with 20 messages in INBOX and an empty Zz
// (listed after INBOX), synced once.
type lossFixture struct {
	*fixture
	user *imapmemserver.User
	acc  *store.Account
}

func newLossFixture(t *testing.T, reconciled bool) *lossFixture {
	u := imapmemserver.NewUser("alice", "pw")
	createMailboxes(t, u, "INBOX", "Zz")
	for i := range 20 {
		appendMsg(t, u, "INBOX", rawMessage(fmt.Sprintf("m%d", i), fmt.Sprintf("Message %d", i)))
	}
	f := newFixture(t, u)
	f.addAccount("mail", "alice", "pw")
	if _, err := f.store.CreateUser(f.ctx, "alice", "h", false); err != nil {
		t.Fatal(err)
	}
	acc, err := f.account("mail")
	if err != nil {
		t.Fatal(err)
	}
	lf := &lossFixture{fixture: f, user: u, acc: acc}
	if r := lf.run(archive.SyncOptions{Reconcile: reconciled}); r.Err != nil {
		t.Fatal(r.Err)
	}
	return lf
}

func (f *lossFixture) run(opts archive.SyncOptions) archive.AccountResult {
	f.t.Helper()
	return f.runCtx(f.ctx, opts)
}

func (f *lossFixture) runCtx(ctx context.Context, opts archive.SyncOptions) archive.AccountResult {
	f.t.Helper()
	acc, err := f.store.GetAccount(f.ctx, f.acc.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.syncer.SyncAccountWith(ctx, acc, opts)
}

func (f *lossFixture) expunge(mailbox string, from, to uint32) {
	f.t.Helper()
	var uids []uint32
	for u := from; u <= to; u++ {
		uids = append(uids, u)
	}
	imaptest.Expunge(f.t, f.host, f.port, "alice", "pw", mailbox, uids...)
}

// alerts returns the pending loss alerts, claimed with a throwaway lease.
func (f *lossFixture) alerts() []store.PendingLossAlert {
	f.t.Helper()
	got, err := f.store.ClaimLossAlerts(f.ctx, []byte(time.Now().String()), time.Millisecond, store.LossAlertMaxAge, 50)
	if err != nil {
		f.t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond) // let the lease run out
	return got
}

func TestLossAlertWhenMessagesAreDeleted(t *testing.T) {
	f := newLossFixture(t, true)
	f.expunge("INBOX", 1, 12)
	if r := f.run(archive.SyncOptions{Reconcile: true}); r.Err != nil || r.Reconcile.Gone != 12 {
		t.Fatalf("reconcile: %+v", r)
	}
	got := f.alerts()
	if len(got) != 1 || got[0].Lost != 12 || got[0].PresentBefore != 20 || len(got[0].Folders) != 1 ||
		got[0].Folders[0] != (store.FolderLoss{Name: "INBOX", Lost: 12}) {
		t.Fatalf("alerts = %+v", got)
	}
	if !strings.Contains(f.logs.String(), `msg="messages deleted on the server" account=mail lost=12 before=20`) {
		t.Errorf("no warning:\n%s", f.logs.String())
	}
}

func TestNoLossAlertForMovedMessages(t *testing.T) {
	f := newLossFixture(t, true)
	c := imaptest.Client(t, f.host, f.port, "alice", "pw")
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	var set imap.UIDSet
	set.AddRange(1, 15)
	if _, err := c.Copy(set, "Zz").Wait(); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	f.expunge("INBOX", 1, 15)
	if r := f.run(archive.SyncOptions{Reconcile: true}); r.Err != nil || r.Reconcile.Gone != 15 || r.New != 0 {
		t.Fatalf("reconcile: %+v", r)
	}
	if got := f.alerts(); len(got) != 0 {
		t.Fatalf("alert for moved messages: %+v", got)
	}
}

// The first reconcile after the upgrade finds what was deleted long ago.
func TestNoLossAlertOnFirstReconcile(t *testing.T) {
	f := newLossFixture(t, false)
	f.expunge("INBOX", 1, 12)
	if r := f.run(archive.SyncOptions{Reconcile: true}); r.Err != nil || r.Reconcile.Gone != 12 {
		t.Fatalf("reconcile: %+v", r)
	}
	if got := f.alerts(); len(got) != 0 {
		t.Fatalf("alert on the first reconcile: %+v", got)
	}
}

func TestLossAlertForVanishedFolder(t *testing.T) {
	f := newLossFixture(t, true)
	if err := f.user.Delete("INBOX"); err != nil {
		t.Fatal(err)
	}
	if r := f.run(archive.SyncOptions{Reconcile: true}); r.Err != nil || r.Reconcile.Gone != 20 {
		t.Fatalf("reconcile: %+v", r)
	}
	got := f.alerts()
	if len(got) != 1 || got[0].Lost != 20 || got[0].Folders[0].Name != "INBOX" {
		t.Fatalf("alerts = %+v", got)
	}
}

// cancelOn cancels a context when a log record with the message arrives.
type cancelOn struct {
	slog.Handler
	msg    string
	cancel context.CancelFunc
}

func (h cancelOn) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}

func (h cancelOn) WithGroup(name string) slog.Handler {
	h.Handler = h.Handler.WithGroup(name)
	return h
}

func (h cancelOn) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == h.msg {
		h.cancel()
	}
	return h.Handler.Handle(ctx, r)
}

// A run stopped by a shutdown after INBOX was reconciled still stores the
// alert: the next run would not see these losses again.
func TestLossAlertAfterCancelledRun(t *testing.T) {
	f := newLossFixture(t, true)
	f.expunge("INBOX", 1, 12)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.syncer.Logger = slog.New(cancelOn{Handler: f.syncer.Logger.Handler(), msg: "many messages no longer on the server", cancel: cancel})
	r := f.runCtx(ctx, archive.SyncOptions{Reconcile: true})
	if r.Err == nil || r.Reconcile.Gone != 12 {
		t.Fatalf("cancelled run: %+v", r)
	}
	runs, _ := f.store.LastRuns(f.ctx)
	if s := runs[f.acc.ID].Status; s != "failed" {
		t.Errorf("status = %s, want failed", s)
	}
	if got := f.alerts(); len(got) != 1 || got[0].Lost != 12 {
		t.Fatalf("alerts = %+v", got)
	}
}
