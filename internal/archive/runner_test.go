package archive_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
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
	if _, err := f.store.DeleteOrRemoveAccount(f.ctx, gone.ID); err != nil {
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
	off, _ := f.account("off")
	if err := f.store.SetAccountEnabled(f.ctx, off.ID, false); err != nil {
		t.Fatal(err)
	}

	r := &archive.Runner{Syncer: f.syncer, Interval: time.Hour, CheckEvery: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// The schedule syncs the enabled account once: its next run is due in
	// an hour.
	on, _ := f.account("on")
	waitForRun(t, f, on.ID, "ok")
	first, _ := f.store.LastRuns(f.ctx)
	time.Sleep(100 * time.Millisecond)
	again, _ := f.store.LastRuns(f.ctx)
	if !again[on.ID].StartedAt.Equal(first[on.ID].StartedAt) {
		t.Fatal("account synced again before its interval")
	}
	if _, ok := again[off.ID]; ok {
		t.Fatal("disabled account synced by the schedule")
	}
	if r := first[on.ID]; r.MessagesFetched != 3 || r.MessagesNew != 3 {
		t.Fatalf("run counters: %+v", r)
	}

	// A request syncs even a disabled account.
	r.Enqueue(off.ID)
	waitForRun(t, f, off.ID, "ok")
}

func waitForRun(t *testing.T, f *fixture, accountID int64, status string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := f.store.LastRuns(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if r, ok := runs[accountID]; ok && r.FinishedAt != nil {
			if r.Status != status {
				t.Fatalf("run status = %s (%s), want %s", r.Status, r.Error, status)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no finished run for account %d", accountID)
}
