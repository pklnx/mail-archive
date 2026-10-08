package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func finishRun(t *testing.T, st *store.Store, accountID int64, status string, effect store.HealthEffect) {
	t.Helper()
	ctx := context.Background()
	run, err := st.StartSyncRun(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Health = status, effect
	if status == "failed" {
		run.Error = "login failed"
	}
	if err := st.FinishSyncRun(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func healthOf(t *testing.T, st *store.Store, accountID int64) *store.SyncHealth {
	t.Helper()
	all, err := st.SyncHealth(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := all[accountID]
	if h == nil {
		t.Fatalf("no health for account %d", accountID)
	}
	return h
}

func TestSyncHealthStreak(t *testing.T) {
	st := storetest.New(t)
	a := createAccount(t, st, "work")

	if h := healthOf(t, st, a.ID); h.FailureStreak != 0 || h.FailingSince != nil || h.LastSuccessAt != nil {
		t.Fatalf("new account: %+v", h)
	}
	finishRun(t, st, a.ID, "failed", store.HealthFailure)
	finishRun(t, st, a.ID, "failed", store.HealthFailure)
	h := healthOf(t, st, a.ID)
	if h.FailureStreak != 2 || h.FailingSince == nil {
		t.Fatalf("after two failures: %+v", h)
	}
	since := *h.FailingSince
	// Cancelled runs leave the streak alone.
	finishRun(t, st, a.ID, "failed", store.HealthUnchanged)
	if h := healthOf(t, st, a.ID); h.FailureStreak != 2 || !h.FailingSince.Equal(since) {
		t.Fatalf("after a cancelled run: %+v", h)
	}
	// So do runs closed as interrupted by the next start.
	if _, err := st.StartSyncRun(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	finishRun(t, st, a.ID, "failed", store.HealthFailure)
	if h := healthOf(t, st, a.ID); h.FailureStreak != 3 {
		t.Fatalf("after an interrupted run: %+v", h)
	}
	// partial and ok reset it.
	finishRun(t, st, a.ID, "partial", store.HealthSuccess)
	h = healthOf(t, st, a.ID)
	if h.FailureStreak != 0 || h.FailingSince != nil || h.LastSuccessAt == nil {
		t.Fatalf("after partial: %+v", h)
	}
	finishRun(t, st, a.ID, "failed", store.HealthFailure)
	finishRun(t, st, a.ID, "ok", store.HealthSuccess)
	if h := healthOf(t, st, a.ID); h.FailureStreak != 0 {
		t.Fatalf("after ok: %+v", h)
	}
}

func TestSyncHealthState(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	created := now.Add(-48 * time.Hour)
	recent := now.Add(-time.Hour)
	old := now.Add(-13 * time.Hour)
	for _, c := range []struct {
		name string
		h    store.SyncHealth
		want string
	}{
		{"ok", store.SyncHealth{Enabled: true, CreatedAt: created, LastSuccessAt: &recent}, store.HealthOK},
		{"below threshold", store.SyncHealth{Enabled: true, CreatedAt: created, LastSuccessAt: &recent, FailureStreak: 2}, store.HealthOK},
		{"failing", store.SyncHealth{Enabled: true, CreatedAt: created, LastSuccessAt: &recent, FailureStreak: 3}, store.HealthFailing},
		{"stale", store.SyncHealth{Enabled: true, CreatedAt: created, LastSuccessAt: &old}, store.HealthStale},
		{"never synced", store.SyncHealth{Enabled: true, CreatedAt: created}, store.HealthStale},
		{"new", store.SyncHealth{Enabled: true, CreatedAt: recent}, store.HealthOK},
		{"disabled", store.SyncHealth{CreatedAt: created, FailureStreak: 9}, store.HealthOK},
	} {
		if got := c.h.State(3, 6*time.Hour, now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	never := store.SyncHealth{Enabled: true, CreatedAt: created}
	if got := never.State(3, 0, now); got != store.HealthOK {
		t.Errorf("schedule off: %s", got)
	}
}

func TestSyncHealthIgnoresImportAccounts(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	imp := &store.Account{Name: "old", Kind: store.KindImport}
	if err := st.CreateAccount(ctx, imp); err != nil {
		t.Fatal(err)
	}
	finishRun(t, st, imp.ID, "failed", store.HealthUnchanged)
	all, err := st.SyncHealth(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all[imp.ID]; ok {
		t.Fatal("import account listed")
	}
}

func TestSyncHealthCascade(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := rawConn(t, url)
	ctx := context.Background()
	a := createAccount(t, st, "gone")
	finishRun(t, st, a.ID, "failed", store.HealthFailure)
	if res, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil || res != store.AccountDeleted {
		t.Fatalf("delete: %v %v", res, err)
	}
	if n := countRows(t, conn, "account_sync_health"); n != 0 {
		t.Fatalf("%d health rows left", n)
	}
}

// rawConn opens a connection for SQL the store API does not offer.
func rawConn(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func countRows(t *testing.T, conn *pgx.Conn, table string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNotificationClaims(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := rawConn(t, url)
	ctx := context.Background()
	a := createAccount(t, st, "a")
	b := createAccount(t, st, "b")
	for range 3 {
		finishRun(t, st, a.ID, "failed", store.HealthFailure)
		finishRun(t, st, b.ID, "failed", store.HealthFailure)
	}
	lease1, lease2 := []byte("lease-1"), []byte("lease-2")

	// The threshold applies when reading.
	if got, err := st.ClaimNotifications(ctx, lease1, time.Minute, 4, 50); err != nil || len(got) != 0 {
		t.Fatalf("threshold 4: %v %v", got, err)
	}
	got, err := st.ClaimNotifications(ctx, lease1, time.Minute, 3, 1)
	if err != nil || len(got) != 1 || got[0].AccountID != a.ID {
		t.Fatalf("claim one: %+v %v", got, err)
	}
	if p := got[0]; p.Account != "a" || p.FailureStreak != 3 || p.NotifiedState != store.NotifiedOK || p.LastError != "login failed" || p.FailingSince == nil {
		t.Fatalf("claimed: %+v", p)
	}
	// A second notifier gets only the other account.
	got, err = st.ClaimNotifications(ctx, lease2, time.Minute, 3, 50)
	if err != nil || len(got) != 1 || got[0].AccountID != b.ID {
		t.Fatalf("second claim: %+v %v", got, err)
	}
	// Only the lease holder can finish.
	if err := st.FinishNotification(ctx, a.ID, lease2, store.NotifiedFailing, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("finish with the wrong lease: %v", err)
	}
	if err := st.FinishNotification(ctx, a.ID, lease1, store.NotifiedFailing, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.DeferNotification(ctx, b.ID, lease2, time.Now().Add(time.Hour), "HTTP 500"); err != nil {
		t.Fatal(err)
	}
	// a was announced, b waits for its retry.
	if got, err := st.ClaimNotifications(ctx, lease1, time.Minute, 3, 50); err != nil || len(got) != 0 {
		t.Fatalf("after finish: %+v %v", got, err)
	}
	// A fourth failure sends nothing; a success sends the recovery.
	finishRun(t, st, a.ID, "failed", store.HealthFailure)
	if got, _ := st.ClaimNotifications(ctx, lease1, time.Minute, 3, 50); len(got) != 0 {
		t.Fatalf("fourth failure claimed: %+v", got)
	}
	finishRun(t, st, a.ID, "ok", store.HealthSuccess)
	got, err = st.ClaimNotifications(ctx, lease1, time.Minute, 3, 50)
	if err != nil || len(got) != 1 || got[0].NotifiedState != store.NotifiedFailing || got[0].FailingSince == nil {
		t.Fatalf("recovery: %+v %v", got, err)
	}

	// A success drops b's pending alert and its retry state.
	finishRun(t, st, b.ID, "ok", store.HealthSuccess)
	var attempts int
	var next *time.Time
	if err := conn.QueryRow(ctx, "SELECT notify_attempts, notify_next_at FROM account_sync_health WHERE account_id = $1", b.ID).
		Scan(&attempts, &next); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || next != nil {
		t.Fatalf("retry state kept: %d %v", attempts, next)
	}
}

func TestNotificationLeaseExpires(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createAccount(t, st, "a")
	finishRun(t, st, a.ID, "failed", store.HealthFailure)
	if got, err := st.ClaimNotifications(ctx, []byte("old"), time.Millisecond, 1, 50); err != nil || len(got) != 1 {
		t.Fatalf("claim: %v %v", got, err)
	}
	time.Sleep(20 * time.Millisecond)
	if got, err := st.ClaimNotifications(ctx, []byte("new"), time.Minute, 1, 50); err != nil || len(got) != 1 {
		t.Fatalf("reclaim: %v %v", got, err)
	}
	if err := st.FinishNotification(ctx, a.ID, []byte("old"), store.NotifiedFailing, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old lease wrote: %v", err)
	}
	if err := st.DeferNotification(ctx, a.ID, []byte("old"), time.Now(), "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old lease deferred: %v", err)
	}
}

func TestNotificationClaimsConcurrent(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	var ids []int64
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		a := createAccount(t, st, name)
		finishRun(t, st, a.ID, "failed", store.HealthFailure)
		ids = append(ids, a.ID)
	}
	claimed := make(chan int64, 100)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			got, err := st.ClaimNotifications(ctx, []byte{byte(i)}, time.Minute, 1, 2)
			if err != nil {
				t.Error(err)
			}
			for _, p := range got {
				claimed <- p.AccountID
			}
		})
	}
	wg.Wait()
	close(claimed)
	seen := map[int64]int{}
	for id := range claimed {
		seen[id]++
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Errorf("account %d claimed %d times", id, seen[id])
		}
	}
}

func TestNotificationSkipsDisabledAccounts(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := rawConn(t, url)
	ctx := context.Background()
	a := createAccount(t, st, "a")
	finishRun(t, st, a.ID, "failed", store.HealthFailure)
	if _, err := conn.Exec(ctx, "UPDATE accounts SET enabled = false WHERE id = $1", a.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ClaimNotifications(ctx, []byte("l"), time.Minute, 1, 50); err != nil || len(got) != 0 {
		t.Fatalf("disabled account claimed: %v %v", got, err)
	}
	if h := healthOf(t, st, a.ID); h.FailureStreak != 1 {
		t.Fatalf("state lost: %+v", h)
	}
	counts, err := st.CountFailingByOwner(ctx, 1)
	if err != nil || len(counts) != 0 {
		t.Fatalf("disabled account counted: %v %v", counts, err)
	}
}

func TestSyncHealthBackfill(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := rawConn(t, url)
	ctx := context.Background()
	broken := createAccount(t, st, "broken")
	fine := createAccount(t, st, "fine")
	fresh := createAccount(t, st, "fresh")
	if _, err := st.MigrateDown(ctx); err != nil {
		t.Fatal(err)
	}
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	for _, r := range []struct {
		id     int64
		at     time.Time
		status string
		err    string
	}{
		{broken.ID, day(1), "failed", "x"},
		{broken.ID, day(2), "ok", ""},
		{broken.ID, day(3), "failed", "login failed"},
		{broken.ID, day(4), "failed", "interrupted"},
		{broken.ID, day(5), "failed", "login failed"},
		{fine.ID, day(1), "failed", "x"},
		{fine.ID, day(2), "partial", "INBOX: x"},
	} {
		if _, err := conn.Exec(ctx, `INSERT INTO sync_runs (account_id, started_at, finished_at, status, error)
			VALUES ($1, $2, $2, $3, NULLIF($4, ''))`, r.id, r.at, r.status, r.err); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h := healthOf(t, st, broken.ID)
	if h.FailureStreak != 2 || h.FailingSince == nil || !h.FailingSince.Equal(day(3)) || h.LastSuccessAt == nil || !h.LastSuccessAt.Equal(day(2)) {
		t.Errorf("broken: %+v", h)
	}
	if h.NotifiedState != store.NotifiedOK {
		t.Errorf("broken notified: %s", h.NotifiedState)
	}
	if h := healthOf(t, st, fine.ID); h.FailureStreak != 0 || h.LastSuccessAt == nil {
		t.Errorf("fine: %+v", h)
	}
	if h := healthOf(t, st, fresh.ID); h.FailureStreak != 0 || h.LastSuccessAt != nil {
		t.Errorf("fresh: %+v", h)
	}
	if n := countRows(t, conn, "account_sync_health"); n != 3 {
		t.Errorf("%d rows, want 3", n)
	}
}
