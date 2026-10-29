package web

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
)

// blobLockKey is the class of the blob lock's advisory lock (store).
const blobLockKey = 0x6d62

// TestSyncStateNeverIdle (#187): from the sync request until its run has
// finished, the account shows queued or running, also after the runner
// took it from the queue and before its sync holds the account's lock. It
// shows idle only once the claim has ended, and then with the new run.
func TestSyncStateNeverIdle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var block atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	synced := make(chan struct{}, 4)
	f := newManageFixtureWith(t, manageOptions{
		beforeSync: func(hctx context.Context, _ int64) {
			if !block.Load() {
				return
			}
			entered <- struct{}{}
			select {
			case <-release:
			case <-hctx.Done():
			}
		},
		afterSync: func() { synced <- struct{}{} },
	}, testUser(t))
	f.do("POST", "/api/accounts", f.newAccount("private", "alice", "secret"), http.StatusCreated)
	before := f.waitIdle("private")
	<-synced
	old := before.Sync.LastRun.StartedAt

	block.Store(true)
	f.do("POST", "/api/accounts/private/sync", nil, http.StatusAccepted)
	// Until the runner has claimed it: in the queue.
	for claimed := false; !claimed; {
		select {
		case <-entered:
			claimed = true
		case <-ctx.Done():
			t.Fatal("the runner does not take the account")
		default:
		}
		if a := f.account("private"); a.Sync.State == "idle" {
			t.Fatalf("idle after the sync request, before its run: %+v", a.Sync)
		}
	}
	// Claimed, before the account's lock: waiting, not idle.
	if a := f.account("private"); a.Sync.State != "queued" || !a.Sync.LastRun.StartedAt.Equal(old) {
		t.Fatalf("claimed: state %s, run %v; want queued with the previous run", a.Sync.State, a.Sync.LastRun.StartedAt)
	}

	// From the release until the claim ends, never idle before it: a poll
	// that shows idle comes after the claim and shows the new run.
	close(release)
	for done := false; !done; {
		select {
		case <-synced:
			done = true
		case <-ctx.Done():
			t.Fatal("the sync does not finish")
		default:
		}
		a := f.account("private")
		if a.Sync.State == "idle" && (a.Sync.LastRun.StartedAt.Equal(old) || a.Sync.LastRun.FinishedAt == nil) {
			t.Fatalf("idle before the sync finished: %+v", a.Sync.LastRun)
		}
	}
	a := f.account("private")
	if a.Sync.State != "idle" || a.Sync.LastRun.StartedAt.Equal(old) || a.Sync.LastRun.FinishedAt == nil {
		t.Fatalf("after the sync: state %s, run %+v; want idle with a new finished run", a.Sync.State, a.Sync.LastRun)
	}
}

// TestParkedSyncShowsQueued (#187): a sync of the web server that waits
// for the blob lock without the account's lock (parked, as during a
// verify) shows queued, not idle with the previous run, and keeps its
// account from turning stale while it waits.
func TestParkedSyncShowsQueued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	synced := make(chan struct{}, 4)
	f := newManageFixtureWith(t, manageOptions{afterSync: func() { synced <- struct{}{} }}, testUser(t))
	f.do("POST", "/api/accounts", f.newAccount("private", "alice", "secret"), http.StatusCreated)
	before := f.waitIdle("private")
	<-synced
	old := before.Sync.LastRun.StartedAt

	observer, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close(context.Background()) })
	waiting := func() int {
		var n int
		if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND classid = $1 AND objid = 0 AND NOT granted
			  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, blobLockKey).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Stands in for a CLI verify.
	if _, err := observer.Exec(ctx, "SELECT pg_advisory_lock($1, 0)", blobLockKey); err != nil {
		t.Fatal(err)
	}
	// Another writer takes the wait slot and waits in PostgreSQL.
	other := &store.Account{Kind: store.KindImport, Name: "other"}
	if err := f.st.CreateAccount(ctx, other); err != nil {
		t.Fatal(err)
	}
	locked := make(chan error, 1)
	go func() {
		unlock, ok, err := f.st.TryLockSyncForWrite(ctx, other.ID)
		if ok {
			unlock()
		} else if err == nil {
			err = context.Canceled
		}
		locked <- err
	}()
	for waiting() < 1 {
		if ctx.Err() != nil {
			t.Fatal("the other writer does not wait")
		}
	}

	f.do("POST", "/api/accounts/private/sync", nil, http.StatusAccepted)
	for f.st.BlobWaiters() < 1 {
		if ctx.Err() != nil {
			t.Fatal("the sync does not park")
		}
	}
	a := f.account("private")
	if a.Sync.State != "queued" || !a.Sync.LastRun.StartedAt.Equal(old) {
		t.Fatalf("parked: state %s, run %v; want queued with the previous run", a.Sync.State, a.Sync.LastRun.StartedAt)
	}
	// A parked sync waits: it keeps its account from turning stale.
	health, err := f.st.SyncHealth(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	accs, err := f.st.ListAccountsByName(ctx, "private")
	if err != nil || len(accs) != 1 {
		t.Fatal(accs, err)
	}
	id := accs[0].ID
	h := health[id]
	busy, err := f.server.loadBusy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimedAt, ok := busy.claimedAt[id]
	if !ok || claimedAt.Before(old) || claimedAt.After(time.Now()) {
		t.Fatalf("claim time %v (%v), want between the last run %v and now", claimedAt, ok, old)
	}
	// Seen from shortly after the claim, with the last success long ago:
	// the parked sync counts from its claim, so the account is not stale,
	// while an idle one would be.
	every := time.Hour
	longAgo := claimedAt.Add(-48 * time.Hour)
	h.LastSuccessAt, h.CreatedAt = &longAgo, longAgo
	soon := claimedAt.Add(time.Minute)
	if got := h.State(f.server.alertAfter, every, busy.since(id, soon), soon); got == store.HealthStale {
		t.Errorf("recently parked sync: health %s, want not stale", got)
	}
	if got := h.State(f.server.alertAfter, every, busyState{}.since(id, soon), soon); got != store.HealthStale {
		t.Fatalf("idle control: health %s, want stale", got)
	}
	// A sync parked for longer than StaleAfter (a verify that hangs) turns
	// the account stale again.
	stuck := claimedAt.Add(store.StaleAfter(every) + time.Minute)
	if got := h.State(f.server.alertAfter, every, busy.since(id, stuck), stuck); got != store.HealthStale {
		t.Errorf("sync parked for longer than %v: health %s, want stale", store.StaleAfter(every), got)
	}

	if _, err := observer.Exec(ctx, "SELECT pg_advisory_unlock($1, 0)", blobLockKey); err != nil {
		t.Fatal(err)
	}
	if err := <-locked; err != nil {
		t.Fatalf("other writer: %v", err)
	}
	select {
	case <-synced:
	case <-ctx.Done():
		t.Fatal("the parked sync does not finish")
	}
	a = f.account("private")
	if a.Sync.State != "idle" || a.Sync.LastRun.StartedAt.Equal(old) || a.Sync.LastRun.FinishedAt == nil {
		t.Fatalf("after the parked sync: state %s, run %+v; want idle with a new finished run", a.Sync.State, a.Sync.LastRun)
	}
}

// TestClaimedFillShowsQueued (#187): a fill the fill runner took from the
// queue shows its account as queued until the run holds the account's
// lock, and counts as busy for its health: from its claim while it waits,
// from its run's start while it runs.
func TestClaimedFillShowsQueued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entered := make(chan int64, 1)
	release := make(chan struct{})
	ran := make(chan struct{}, 1)
	f, acc := fillFixtureWith(t, manageOptions{
		beforeRun: func(hctx context.Context, id int64) {
			entered <- id
			select {
			case <-release:
			case <-hctx.Done():
			}
		},
		afterRun: func() { ran <- struct{}{} },
	})
	body := f.fillBody("secret")
	body["excludedFolders"] = []string{"Spam"}
	body["confirmFolders"] = true
	f.do("POST", "/api/accounts/journal/fills", body, http.StatusAccepted)
	var fillID int64
	select {
	case fillID = <-entered:
	case <-ctx.Done():
		t.Fatal("the fill runner does not take the fill")
	}
	if a := f.account("journal"); a.Sync.State != "queued" {
		t.Fatalf("claimed fill: state %s, want queued", a.Sync.State)
	}
	busy, err := f.server.loadBusy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !busy.fillsQueued[fillID] || !busy.queued[acc.ID] {
		t.Fatalf("claimed fill not pending: fills %v, accounts %v", busy.fillsQueued, busy.queued)
	}
	now := time.Now()
	started := now.Add(-time.Hour)
	claimedAt, ok := busy.fillsClaimedAt[fillID]
	if !ok || claimedAt.After(now) {
		t.Fatalf("fill claim time %v (%v), want one before now", claimedAt, ok)
	}
	h := &store.FillHealth{FillID: fillID, AccountID: acc.ID}
	if got := busy.fillSince(h, now); got == nil || !got.Equal(claimedAt) {
		t.Errorf("claimed fill before its run: since %v, want its claim %v", got, claimedAt)
	}
	// Claimed for the whole run: a running fill keeps counting from its
	// run's start.
	h.Running, h.RunStartedAt = true, &started
	if got := busy.fillSince(h, now); got == nil || !got.Equal(started) {
		t.Errorf("running fill: since %v, want its start %v", got, started)
	}

	close(release)
	select {
	case <-ran:
	case <-ctx.Done():
		t.Fatal("the fill does not finish")
	}
	if a := f.account("journal"); a.Sync.State != "idle" || a.Fill == nil || a.Fill.Status != store.FillOK {
		t.Fatalf("after the fill: state %s, fill %+v", a.Sync.State, a.Fill)
	}
}

// TestBusySinceUsesClaimTime (#187): a sync of this server counts from its
// run's start once the row is written; before that, and while it waits for
// the locks, from when the runner claimed it; work that only waits in the
// queue, and the CLI before its row, count from now.
func TestBusySinceUsesClaimTime(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	claimed, started := now.Add(-3*time.Hour), now.Add(-2*time.Hour)
	finished := now.Add(-time.Hour)
	for _, c := range []struct {
		name string
		b    busyState
		want *time.Time
	}{
		{"idle", busyState{}, nil},
		{"queued", busyState{queued: map[int64]bool{1: true}}, &now},
		{"claimed or parked", busyState{queued: map[int64]bool{1: true}, claimedAt: map[int64]time.Time{1: claimed}}, &claimed},
		{"running before its row", busyState{running: map[int64]bool{1: true}, claimedAt: map[int64]time.Time{1: claimed},
			runs: map[int64]store.LastRun{1: {StartedAt: now.Add(-5 * time.Hour), FinishedAt: &finished}}}, &claimed},
		{"running", busyState{running: map[int64]bool{1: true}, claimedAt: map[int64]time.Time{1: claimed},
			runs: map[int64]store.LastRun{1: {StartedAt: started}}}, &started},
		{"CLI before its row", busyState{running: map[int64]bool{1: true}}, &now},
	} {
		got := c.b.since(1, now)
		if (got == nil) != (c.want == nil) || got != nil && !got.Equal(*c.want) {
			t.Errorf("%s: since %v, want %v", c.name, got, c.want)
		}
	}
	// The same for a recurring fill between runs.
	h := &store.FillHealth{FillID: 7, AccountID: 1}
	b := busyState{fillsQueued: map[int64]bool{7: true}, fillsClaimedAt: map[int64]time.Time{7: claimed}}
	if got := b.fillSince(h, now); got == nil || !got.Equal(claimed) {
		t.Errorf("claimed fill: since %v, want %v", got, claimed)
	}
	b.fillsClaimedAt = nil
	if got := b.fillSince(h, now); got == nil || !got.Equal(now) {
		t.Errorf("queued fill: since %v, want now", got)
	}
}

// TestKeepEarliestClaim (#193): when the sync runner and the fill runner
// both claim one account, the earliest claim wins, whichever loadBusy reads
// first. This tests the rule, not the loops in loadBusy that apply it.
func TestKeepEarliestClaim(t *testing.T) {
	early := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	late := early.Add(time.Minute)
	for _, c := range []struct {
		name   string
		claims []time.Time
		want   time.Time // zero: no claim
	}{
		{"first claim", []time.Time{late}, late},
		{"later claim ignored", []time.Time{early, late}, early},
		{"earlier claim replaces", []time.Time{late, early}, early},
		{"zero time ignored", []time.Time{late, {}}, late},
		{"zero time alone", []time.Time{{}}, time.Time{}},
		{"zero time first, then a claim", []time.Time{{}, late}, late},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := map[int64]time.Time{}
			for _, at := range c.claims {
				keepEarliestClaim(m, 7, at)
			}
			got, ok := m[7]
			if c.want.IsZero() {
				if ok {
					t.Fatalf("claim %v, want none", got)
				}
				return
			}
			if !ok || !got.Equal(c.want) {
				t.Fatalf("claim %v (%v), want %v", got, ok, c.want)
			}
			if len(m) != 1 {
				t.Errorf("%d accounts claimed, want 1", len(m))
			}
		})
	}

	// Both runners' Pending() for the same accounts, read in both orders:
	// account 1 was claimed first by the sync runner, account 2 by the fill
	// runner, account 3 only by the fill runner.
	syncPending := map[int64]time.Time{1: early, 2: late}
	fillPending := map[int64]time.Time{1: late, 2: early, 3: late}
	want := map[int64]time.Time{1: early, 2: early, 3: late}
	for name, order := range map[string][]map[int64]time.Time{
		"sync runner first": {syncPending, fillPending},
		"fill runner first": {fillPending, syncPending},
	} {
		t.Run(name, func(t *testing.T) {
			m := map[int64]time.Time{}
			for _, pending := range order {
				for id, at := range pending {
					keepEarliestClaim(m, id, at)
				}
			}
			if len(m) != len(want) {
				t.Fatalf("claims %v, want %v", m, want)
			}
			for id, at := range want {
				if !m[id].Equal(at) {
					t.Errorf("account %d: claim %v, want %v", id, m[id], at)
				}
			}
		})
	}
}
