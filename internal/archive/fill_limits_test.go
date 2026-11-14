package archive_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/imapsync"
	"github.com/keepia/archive/internal/imaptest"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/testwait"
)

// Tests of the run limits for fills: a fresh dial after a broken
// connection, the listing caps, the run time limit, a message over the
// size cap.

// countDials makes filler count its dials.
func countDials(filler *archive.Filler) *atomic.Int32 {
	var n atomic.Int32
	filler.Dial = func(ctx context.Context, cfg imapsync.Config) (*imapsync.Conn, error) {
		n.Add(1)
		return imapsync.Dial(ctx, cfg)
	}
	return &n
}

// INBOX's listing goes past the fill's limit, which breaks the connection;
// Sent is filled over a new one. The error names the setting, and the
// listing never holds more than the limit.
func TestFillRedialAfterBrokenConnection(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret", "Spam")
	filler := f.filler()
	filler.MaxFolderMessages = 3
	dials := countDials(filler)
	var capacities []int
	archive.SetAfterList(filler, func(_, c int) { capacities = append(capacities, c) })
	got, err := filler.Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	inbox, sent := folderOf(got, "INBOX"), folderOf(got, "Sent")
	if got.Status != store.FillPartial || !strings.Contains(inbox.Error, "the folder has more than 3 messages: ") ||
		!strings.HasSuffix(inbox.Error, "; raise "+config.EnvFillMaxFolderMessages) {
		t.Fatalf("fill %+v, INBOX error %q", got, inbox.Error)
	}
	if sent.Counts.Imported != 1 || !sent.Done || sent.Error != "" {
		t.Errorf("Sent %+v", sent)
	}
	if n := dials.Load(); n != 2 {
		t.Errorf("%d dials, want 2", n)
	}
	if len(capacities) != 2 || capacities[0] != 3 || capacities[1] != 3 {
		t.Errorf("listing capacities %v, want [3 3]", capacities)
	}
}

// Every folder breaks the connection: after 5 new connections the run
// stops, and the folders after that are never listed.
func TestFillRedialCap(t *testing.T) {
	u := imapmemserver.NewUser("alice@example.com", "secret")
	var names []string
	for i := range 8 {
		name := fmt.Sprintf("F%d", i)
		names = append(names, name)
		imaptest.CreateMailboxes(t, u, name)
		imaptest.AppendAt(t, u, name, fillMessage(name+"a"), fillCutoff.Add(-48*time.Hour))
		imaptest.AppendAt(t, u, name, fillMessage(name+"b"), fillCutoff.Add(-48*time.Hour))
	}
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret")
	filler := f.filler()
	filler.MaxFolderMessages = 1
	dials := countDials(filler)
	got, err := filler.Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FillFailed || !strings.Contains(got.Error, "after 5 new connections") {
		t.Fatalf("fill %+v", got)
	}
	if n := dials.Load(); n != 1+archive.MaxRedials {
		t.Errorf("%d dials, want %d", n, 1+archive.MaxRedials)
	}
	for _, name := range names[1+archive.MaxRedials:] {
		if ff := folderOf(got, name); ff.Error != "" || ff.Counts.Scanned != 0 {
			t.Errorf("%s was listed: %+v", name, ff)
		}
	}
}

// longIDUser has an INBOX with two messages whose Message-IDs take 100 B
// each.
func longIDUser(t *testing.T, idLen int, n int) (*imapmemserver.User, []string) {
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX")
	var ids []string
	for i := range n {
		id := fmt.Sprintf("%d", i) + strings.Repeat("x", idLen-len("@example.com")-1)
		ids = append(ids, id)
		imaptest.AppendAt(t, u, "INBOX", fillMessage(id), fillCutoff.Add(-48*time.Hour))
	}
	return u, ids
}

// The Message-IDs a listing keeps are capped at 96 B per message of the
// limit; beyond, the folder fails with an error naming the setting.
func TestFillMessageIDBudget(t *testing.T) {
	u, _ := longIDUser(t, 100, 2)
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret")
	filler := f.filler()
	filler.MaxFolderMessages = 2 // 192 B of Message-IDs
	got, err := filler.Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	inbox := folderOf(got, "INBOX")
	if want := "the folder's Message-IDs take more than 192 bytes; raise " + config.EnvFillMaxFolderMessages; inbox.Error != want {
		t.Fatalf("INBOX error %q, want %q", inbox.Error, want)
	}
}

// A Message-ID longer than 998 B is not kept: the message is imported,
// never left out as journaled, even when the account received a copy.
func TestFillLongMessageIDNotKept(t *testing.T) {
	u, ids := longIDUser(t, 1000, 1)
	f := newFixture(t, u)
	acc := f.smtpAccount()
	f.journal(acc, ids[0])
	fill := f.createFill(acc, "secret")
	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c := folderOf(got, "INBOX").Counts; c.Imported != 1 || c.Journaled != 0 {
		t.Errorf("counts %+v, want imported", c)
	}
}

// At its time limit, a fill run that committed a batch stays running and
// returns ErrRunTimeLimit; the next run finishes it.
func TestFillTimeLimitWithProgress(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret", "Spam")
	filler := f.filler()
	var limit runLimit
	archive.SetFillLimitRun(filler, limit.seam)
	filler.Progress = func(store.FillFolder) { limit.end() }
	got, err := filler.Run(f.ctx, fill.ID)
	if !errors.Is(err, archive.ErrRunTimeLimit) || got.Status != store.FillRunning {
		t.Fatalf("fill %+v, err %v", got, err)
	}
	got, err = f.filler().Run(f.ctx, fill.ID)
	if err != nil || got.Status != store.FillOK || archive.FillTotals(got).Imported != 6 {
		t.Fatalf("after the next run %+v, %v", got, err)
	}
}

// At its time limit, a recurring fill's run without progress failed and
// counts as a failure; a shutdown leaves the fill running and records no
// health.
func TestFillTimeLimitWithoutProgress(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown=%v", shutdown), func(t *testing.T) {
			c := &clock{now: time.Now()}
			f := newFixture(t, recurringUser(t, c.Now()))
			acc := f.smtpAccount()
			fill := f.createRecurringFill(acc, "secret")
			filler := f.recurringFiller(c)
			parent, stop := context.WithCancel(f.ctx)
			defer stop()
			var limit runLimit
			archive.SetFillLimitRun(filler, func(ctx context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := limit.seam(ctx)
				if shutdown {
					stop()
				} else {
					limit.end()
				}
				return ctx, cancel
			})
			got, err := filler.Run(parent, fill.ID)
			h, _ := f.store.FillHealth(f.ctx, nil)
			switch {
			case shutdown && (!errors.Is(err, context.Canceled) || got.Status != store.FillRunning || h[fill.ID].FailureStreak != 0):
				t.Errorf("shutdown: fill %+v, err %v, health %+v", got, err, h[fill.ID])
			case !shutdown && (err != nil || got.Status != store.FillFailed || !strings.Contains(got.Error, "time limit") ||
				h[fill.ID].FailureStreak != 1):
				t.Errorf("time limit: fill %+v, err %v, health %+v", got, err, h[fill.ID])
			}
		})
	}
}

// A fill run at its time limit with progress is queued again behind the
// fills waiting.
func TestFillRunnerRequeuesAtTimeLimit(t *testing.T) {
	f := newFixture(t, fillUser(t))
	first := f.smtpAccount()
	second := &store.Account{Name: "journal2", Enabled: true}
	if err := f.store.CreateSMTPAccount(f.ctx, second, []string{"archive2@archive.local"}); err != nil {
		t.Fatal(err)
	}
	x := f.createFill(first, "secret", "Spam")
	y := f.createFill(second, "secret", "Spam")
	filler := f.filler()
	var (
		limit runLimit
		runs  atomic.Int32
		ended atomic.Bool
	)
	archive.SetFillLimitRun(filler, func(ctx context.Context) (context.Context, context.CancelFunc) {
		if runs.Add(1) == 1 {
			return limit.seam(ctx)
		}
		return context.WithCancel(ctx)
	})
	filler.Progress = func(store.FillFolder) {
		if !ended.Swap(true) {
			limit.end()
		}
	}
	var (
		mu    sync.Mutex
		order []int64
		after int
		ran   testwait.Signal
		// requeued: x waited in the queue when y started, so it was queued
		// again right away, not found later as a running fill.
		requeued bool
		r        *archive.FillRunner
	)
	r = &archive.FillRunner{
		Filler: filler,
		BeforeRun: func(_ context.Context, id int64) {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			if id == y.ID {
				at, ok := r.PendingFills()[x.ID]
				requeued = ok && at.IsZero()
			}
		},
		AfterRun: func() {
			mu.Lock()
			after++
			mu.Unlock()
			ran.Notify()
		},
	}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	testwait.Until(t, &ran, "three runs", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return after >= 3
	})
	mu.Lock()
	got := append([]int64(nil), order...)
	mu.Unlock()
	if len(got) < 3 || got[0] != x.ID || got[1] != y.ID || got[2] != x.ID || !requeued {
		t.Fatalf("order %v, want [%d %d %d]; queued again before the next fill: %v", got, x.ID, y.ID, x.ID, requeued)
	}
	for _, id := range []int64{x.ID, y.ID} {
		if st, _ := f.store.FillStatus(f.ctx, id); st != store.FillOK {
			t.Errorf("fill %d: %s", id, st)
		}
	}
}

// headerLine is a ListHeaders response for UID uid.
func headerLine(seq int, uid uint32) string {
	h := fmt.Sprintf("Message-ID: <%d@script.example>\r\nDate: Fri, 5 Mar 2010 10:00:00 +0000\r\nFrom: a@example.com\r\nSubject: m%d\r\n\r\n", uid, uid)
	return fmt.Sprintf("* %d FETCH (UID %d INTERNALDATE \"05-Mar-2010 10:00:00 +0000\" RFC822.SIZE 100 "+
		"BODY[HEADER.FIELDS (MESSAGE-ID DATE FROM SUBJECT)] {%d}\r\n%s)\r\n", seq, uid, len(h), h)
}

// A recurring fill stops a folder before a message over the size cap:
// what came before it is imported, the folder's last UID stops before it,
// and the partial run counts as a failure in the fill's health.
func TestFillStopsBeforeLargeMessage(t *testing.T) {
	s := imaptest.StartScript(t, func(c *imaptest.ScriptConn) error {
		if err := c.Greet(""); err != nil {
			return err
		}
		if err := sendList(c, "INBOX"); err != nil {
			return err
		}
		if err := expectExamine(c, "INBOX", 3, 4); err != nil {
			return err
		}
		if err := serveFetch(c, headerLine(1, 1), headerLine(2, 2), headerLine(3, 3)); err != nil {
			return err
		}
		if _, err := c.Expect("UID FETCH"); err != nil {
			return err
		}
		if _, err := io.WriteString(c, fetchLine(1, 1, false)+fetchLine(2, 2, true)); err != nil {
			return err
		}
		return drainScript(c)
	})
	c := &clock{now: time.Now()}
	f := newFixture(t)
	f.host, f.port = s.Host, s.Port
	acc := f.smtpAccount()
	fill := f.createRecurringFill(acc, "secret")
	filler := f.recurringFiller(c)
	filler.BatchSize = 10
	got := runFill(t, filler, fill.ID)
	inbox := folderOf(got, "INBOX")
	if got.Status != store.FillPartial || !strings.Contains(got.Error, "INBOX: a message (UID 2) is larger than 256 MiB") {
		t.Fatalf("fill %+v", got)
	}
	if inbox.LastUID != 1 || inbox.Counts.Imported != 1 || inbox.Done {
		t.Errorf("INBOX %+v, want last UID 1 and one import", inbox)
	}
	h, _ := f.store.FillHealth(f.ctx, nil)
	if h[fill.ID].FailureStreak != 1 {
		t.Errorf("health %+v, want a failure", h[fill.ID])
	}
}
