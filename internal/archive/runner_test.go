package archive_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/testwait"
)

func TestSyncSkipsLockedAndRemovedAccounts(t *testing.T) {
	u := imapmemserver.NewUser("u", "pw")
	createMailboxes(t, u, "INBOX")
	appendMsg(t, u, "INBOX", rawMessage("m1", "Hello"))
	f := newFixture(t, u)
	f.addAccount("busy", "u", "pw")
	f.addAccount("gone", "u", "pw")

	busy, _ := f.account("busy")
	unlock, ok, err := f.store.TryLockSync(f.ctx, busy.ID)
	if err != nil || !ok {
		t.Fatal("lock", ok, err)
	}
	if res := f.syncer.SyncAccount(f.ctx, busy); !errors.Is(res.Err, archive.ErrSyncRunning) {
		t.Fatalf("locked account: %v", res.Err)
	}
	unlock()

	// "gone" is removed after its first sync; later syncs skip it.
	f.sync()
	gone, _ := f.account("gone")
	if _, err := f.store.DeleteOrRemoveAccount(f.ctx, gone.Ref()); err != nil {
		t.Fatal(err)
	}
	if res := f.sync(); len(res) != 1 || res["busy"].Err != nil {
		t.Fatalf("sync after removal: %+v", res)
	}
	gone, _ = f.account("gone")
	if res := f.syncer.SyncAccount(f.ctx, gone); !errors.Is(res.Err, archive.ErrAccountRemoved) {
		t.Fatalf("removed account: %v", res.Err)
	}
}

func TestRunnerScheduleAndQueue(t *testing.T) {
	u := imapmemserver.NewUser("u", "pw")
	createMailboxes(t, u, "INBOX")
	for _, id := range []string{"m1", "m2", "m3"} {
		appendMsg(t, u, "INBOX", rawMessage(id, id))
	}
	f := newFixture(t, u)
	f.addAccount("on", "u", "pw")
	f.addAccount("off", "u", "pw")
	f.addAccount("manual", "u", "pw")
	f.addAccount("fast", "u", "pw")
	off, _ := f.account("off")
	disabled := false
	if err := f.store.UpdateAccount(f.ctx, off.Ref(), store.AccountChange{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	manual, _ := f.account("manual")
	zero, fifteen := time.Duration(0), 15*time.Minute
	if err := f.store.UpdateAccount(f.ctx, manual.Ref(), store.AccountChange{SyncInterval: &store.OwnInterval{D: &zero}}); err != nil {
		t.Fatal(err)
	}
	fast, _ := f.account("fast")
	if err := f.store.UpdateAccount(f.ctx, fast.Ref(), store.AccountChange{SyncInterval: &store.OwnInterval{D: &fifteen}}); err != nil {
		t.Fatal(err)
	}

	// The clock jumps 20 minutes once the first syncs are done: then the
	// 15-minute account is due again, the hourly one is not. Only the
	// runner reads this clock; each read is counted and signalled.
	var later atomic.Bool
	var reads atomic.Int64
	var read, synced testwait.Signal
	now := func() time.Time {
		reads.Add(1)
		read.Notify()
		if later.Load() {
			return time.Now().Add(20 * time.Minute)
		}
		return time.Now()
	}
	r := &archive.Runner{Syncer: f.syncer, Schedule: archive.Schedule{Sync: time.Hour, SyncMin: 5 * time.Minute},
		CheckEvery: 10 * time.Millisecond, Now: now, AfterSync: synced.Notify}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// The schedule syncs the enabled accounts once: the next run of "on"
	// is due in an hour.
	on, _ := f.account("on")
	waitForRun(t, f, &synced, on.ID, "ok")
	waitForRun(t, f, &synced, fast.ID, "ok")
	first, _ := f.store.LastRuns(f.ctx)
	// Three more clock reads: with both syncs done, the runner reads the
	// clock once per schedule check (dueAccounts) and once per account it
	// takes from the queue. Had a check queued "on" again, its sync would
	// have run before the third read (the next check).
	from := reads.Load()
	testwait.Until(t, &read, "three schedule checks", func() bool { return reads.Load() >= from+3 })
	again, _ := f.store.LastRuns(f.ctx)
	if !again[on.ID].StartedAt.Equal(first[on.ID].StartedAt) {
		t.Fatal("account synced again before its interval")
	}
	if _, ok := again[off.ID]; ok {
		t.Fatal("disabled account synced by the schedule")
	}
	if _, ok := again[manual.ID]; ok {
		t.Fatal("account not on the schedule synced by the schedule")
	}
	later.Store(true)
	testwait.Until(t, &synced, "the 15-minute account's sync after 20 minutes", func() bool {
		runs, err := f.store.LastRuns(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return runs[fast.ID].StartedAt.After(first[fast.ID].StartedAt) && runs[fast.ID].FinishedAt != nil
	})
	if runs, _ := f.store.LastRuns(f.ctx); !runs[on.ID].StartedAt.Equal(first[on.ID].StartedAt) {
		t.Fatal("hourly account synced again after 20 minutes")
	}
	// The other accounts read the same mailbox, so "on" may find the
	// messages already archived.
	if r := first[on.ID]; r.MessagesFetched != 3 {
		t.Fatalf("run counters: %+v", r)
	}

	// A request syncs even a disabled account.
	r.Enqueue(off.ID)
	waitForRun(t, f, &synced, off.ID, "ok")
}

// waitForRun waits until the account has a finished run and checks its
// status. synced is the runner's AfterSync: the run is finished before.
func waitForRun(t *testing.T, f *fixture, synced *testwait.Signal, accountID int64, status string) {
	t.Helper()
	var last store.LastRun
	testwait.Until(t, synced, fmt.Sprintf("a finished run of account %d", accountID), func() bool {
		runs, err := f.store.LastRuns(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		r, ok := runs[accountID]
		last = r
		return ok && r.FinishedAt != nil
	})
	if last.Status != status {
		t.Fatalf("run status = %s (%s), want %s", last.Status, last.Error, status)
	}
}

// A request to reconcile upgrades a queued sync instead of adding another.
func TestRunnerUpgradesQueuedSync(t *testing.T) {
	u := imapmemserver.NewUser("u", "pw")
	createMailboxes(t, u, "INBOX")
	appendMsg(t, u, "INBOX", rawMessage("m1", "Hello"))
	f := newFixture(t, u)
	f.addAccount("acc", "u", "pw")
	acc, _ := f.account("acc")

	var syncs atomic.Int32
	var synced testwait.Signal
	r := &archive.Runner{Syncer: f.syncer, CheckEvery: 10 * time.Millisecond,
		AfterSync: func() { syncs.Add(1); synced.Notify() }}
	r.Enqueue(acc.ID)
	r.EnqueueWith(archive.SyncOptions{Reconcile: true}, acc.ID)
	r.Enqueue(acc.ID) // does not downgrade it
	if q := ids(r.Pending()); len(q) != 1 || !q[acc.ID] {
		t.Fatalf("queued = %v", q)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitForRun(t, f, &synced, acc.ID, "ok")
	runs, _ := f.store.LastRuns(f.ctx)
	if got := runs[acc.ID].Reconcile.Folders; got != 1 {
		t.Fatalf("reconciled folders = %d, want 1", got)
	}
	// Once the runner holds nothing, a second entry for the account would
	// have been queued, claimed or synced: its sync is in LastRuns then.
	testwait.Until(t, &synced, "the end of the sync's claim", func() bool {
		return syncs.Load() >= 1 && len(r.Pending()) == 0
	})
	if again, _ := f.store.LastRuns(f.ctx); !again[acc.ID].StartedAt.Equal(runs[acc.ID].StartedAt) || syncs.Load() != 1 {
		t.Fatalf("queued twice: %d syncs", syncs.Load())
	}
}
