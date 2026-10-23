package archive_test

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
)

// runRunner runs r until the test ends; stop cancels it and waits.
func runRunner(t *testing.T, ctx context.Context, run func(context.Context)) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { run(ctx); close(done) }()
	stop = func() { cancel(); <-done }
	t.Cleanup(stop)
	return stop
}

func claimUser(t *testing.T) *imapmemserver.User {
	u := imapmemserver.NewUser("u", "pw")
	createMailboxes(t, u, "INBOX")
	appendMsg(t, u, "INBOX", rawMessage("m1", "Hello"))
	return u
}

// TestRunnerClaimsUntilSyncReturns (#187): an account the runner took from
// the queue stays pending until its sync has returned, and the claim has
// ended when AfterSync runs. An account queued again during its sync keeps
// exactly one queue entry.
func TestRunnerClaimsUntilSyncReturns(t *testing.T) {
	f := newFixture(t, claimUser(t))
	f.addAccount("acc", "u", "pw")
	acc, _ := f.account("acc")

	entered := make(chan map[int64]bool, 2)
	release := make(chan struct{})
	afterSync := make(chan map[int64]bool, 2)
	r := &archive.Runner{Syncer: f.syncer, CheckEvery: time.Hour}
	r.BeforeSync = func(ctx context.Context, id int64) {
		entered <- r.Pending()
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	r.AfterSync = func() { afterSync <- r.Pending() }
	r.Enqueue(acc.ID)
	runRunner(t, f.ctx, r.Run)

	if p := <-entered; !maps.Equal(p, map[int64]bool{acc.ID: true}) {
		t.Fatalf("pending in BeforeSync = %v, want the claimed account", p)
	}
	if p := r.Pending(); !p[acc.ID] {
		t.Fatalf("pending while claimed = %v", p)
	}
	// Queued again while it is claimed: one more sync, not two.
	r.Enqueue(acc.ID)
	r.Enqueue(acc.ID)
	release <- struct{}{}
	if p := <-afterSync; !maps.Equal(p, map[int64]bool{acc.ID: true}) {
		t.Fatalf("pending after the first sync = %v, want the queued account", p)
	}
	<-entered
	release <- struct{}{}
	// The claim has ended before AfterSync, and nothing else is queued.
	if p := <-afterSync; len(p) != 0 {
		t.Fatalf("pending in AfterSync = %v, want none", p)
	}
}

// TestRunnerReleasesClaimOfRemovedAccount (#187): an account removed while
// it was queued ends its claim without a sync.
func TestRunnerReleasesClaimOfRemovedAccount(t *testing.T) {
	f := newFixture(t, claimUser(t))
	f.addAccount("gone", "u", "pw")
	f.addAccount("acc", "u", "pw")
	gone, _ := f.account("gone")
	acc, _ := f.account("acc")

	entered := make(chan map[int64]bool, 2)
	afterSync := make(chan map[int64]bool, 2)
	r := &archive.Runner{Syncer: f.syncer, CheckEvery: time.Hour}
	r.BeforeSync = func(_ context.Context, id int64) {
		if id == acc.ID {
			entered <- r.Pending()
		}
	}
	r.AfterSync = func() { afterSync <- r.Pending() }
	r.Enqueue(gone.ID, acc.ID)
	if _, err := f.store.DeleteOrRemoveAccount(f.ctx, gone.Ref()); err != nil {
		t.Fatal(err)
	}
	runRunner(t, f.ctx, r.Run)

	if p := <-entered; !maps.Equal(p, map[int64]bool{acc.ID: true}) {
		t.Fatalf("pending when the next account is claimed = %v, want only %d", p, acc.ID)
	}
	if p := <-afterSync; len(p) != 0 {
		t.Fatalf("pending after the sync = %v, want none", p)
	}
}

// TestRunnerReleasesClaimOnCancel (#187): a claimed account whose sync is
// cancelled ends its claim, and Run takes no further account off the
// queue once ctx is cancelled, so none is claimed and lost.
func TestRunnerReleasesClaimOnCancel(t *testing.T) {
	f := newFixture(t, claimUser(t))
	f.addAccount("first", "u", "pw")
	f.addAccount("second", "u", "pw")
	first, _ := f.account("first")
	second, _ := f.account("second")

	entered := make(chan int64, 2)
	afterSync := make(chan map[int64]bool, 2)
	r := &archive.Runner{Syncer: f.syncer, CheckEvery: time.Hour}
	r.BeforeSync = func(ctx context.Context, id int64) {
		entered <- id
		if id == first.ID {
			<-ctx.Done()
		}
	}
	r.AfterSync = func() { afterSync <- r.Pending() }
	r.Enqueue(first.ID, second.ID)
	stop := runRunner(t, f.ctx, r.Run)
	if id := <-entered; id != first.ID {
		t.Fatalf("first claimed %d, want %d", id, first.ID)
	}
	stop()
	if p := r.Pending(); !maps.Equal(p, map[int64]bool{second.ID: true}) {
		t.Fatalf("pending after cancel = %v, want only the queued %d", p, second.ID)
	}
	if len(afterSync) != 0 {
		t.Fatal("a sync ran after cancel")
	}

	// The account still queued syncs when Run starts again.
	runRunner(t, f.ctx, r.Run)
	if id := <-entered; id != second.ID {
		t.Fatalf("claimed %d after the restart, want %d", id, second.ID)
	}
	if p := <-afterSync; len(p) != 0 {
		t.Fatalf("pending after the sync = %v, want none", p)
	}
}

// TestFillRunnerClaimsUntilRunReturns (#187): a fill the runner took from
// the queue stays pending, by account and by fill, until its run has
// returned; the claim has ended when AfterRun runs.
func TestFillRunnerClaimsUntilRunReturns(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret", "Spam")

	entered := make(chan [2]map[int64]bool, 1)
	release := make(chan struct{})
	afterRun := make(chan [2]map[int64]bool, 1)
	r := &archive.FillRunner{Filler: f.filler(), CheckEvery: time.Hour}
	r.BeforeRun = func(ctx context.Context, id int64) {
		entered <- [2]map[int64]bool{r.Pending(), r.PendingFills()}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	r.AfterRun = func() { afterRun <- [2]map[int64]bool{r.Pending(), r.PendingFills()} }
	runRunner(t, f.ctx, r.Run) // picks up the running fill

	p := <-entered
	if !maps.Equal(p[0], map[int64]bool{acc.ID: true}) || !maps.Equal(p[1], map[int64]bool{fill.ID: true}) {
		t.Fatalf("pending in BeforeRun = %v, want account %d and fill %d", p, acc.ID, fill.ID)
	}
	close(release)
	if p := <-afterRun; len(p[0])+len(p[1]) != 0 {
		t.Fatalf("pending in AfterRun = %v, want none", p)
	}
}

// TestFillRunnerReleasesClaim (#187): a fill skipped by its run (it no
// longer exists) and a fill whose run is cancelled both end their claim.
func TestFillRunnerReleasesClaim(t *testing.T) {
	t.Run("skipped", func(t *testing.T) {
		f := newFixture(t)
		acc := f.smtpAccount()
		afterRun := make(chan map[int64]bool, 1)
		r := &archive.FillRunner{Filler: f.filler(), CheckEvery: time.Hour}
		r.AfterRun = func() { afterRun <- r.PendingFills() }
		r.Enqueue(999999, acc.ID)
		runRunner(t, f.ctx, r.Run)
		if p := <-afterRun; len(p) != 0 {
			t.Fatalf("pending fills after the skipped run = %v", p)
		}
		if p := r.Pending(); len(p) != 0 {
			t.Fatalf("pending accounts after the skipped run = %v", p)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		f := newFixture(t, fillUser(t))
		acc := f.smtpAccount()
		f.createFill(acc, "secret", "Spam")
		entered := make(chan struct{}, 1)
		r := &archive.FillRunner{Filler: f.filler(), CheckEvery: time.Hour}
		r.BeforeRun = func(ctx context.Context, _ int64) {
			entered <- struct{}{}
			<-ctx.Done()
		}
		stop := runRunner(t, f.ctx, r.Run)
		<-entered
		stop()
		if p, q := r.Pending(), r.PendingFills(); len(p)+len(q) != 0 {
			t.Fatalf("pending after cancel = %v %v, want none", p, q)
		}
	})
}
