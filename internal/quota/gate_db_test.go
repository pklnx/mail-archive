package quota_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/quota"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

const gib = int64(1) << 30

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func exec(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// waitForLockWaits returns once n statements of the test's database wait
// for a lock; it fails if done delivers first.
func waitForLockWaits(t *testing.T, conn *pgx.Conn, n int, done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	for {
		var waiting int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatalf("waiting for %d lock waits: %v", n, err)
		}
		if waiting >= n {
			return
		}
		select {
		case <-done:
			t.Fatalf("a gate finished before %d statements waited for the lock", n)
		default:
		}
	}
}

func newGate(st *store.Store, cfg quota.Config) *quota.Gate {
	cfg.Thresholds = quota.Thresholds{WarnPercent: 80, OverflowPercent: 10}
	return quota.New(cfg, st, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Two gates (two processes) see the instance cross its quota at once:
// both wait for the row lock of its state, and exactly one transition is
// persisted (one event_seq step); both then report full.
func TestTwoGatesCrossAtOnce(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	admin := connect(t, url)
	exec(t, admin, `UPDATE storage_counter_base SET bytes = $1`, 10*gib)
	cfg := quota.Config{InstanceQuota: 10 * gib}
	a, b := newGate(st, cfg), newGate(st, cfg)

	holder := connect(t, url)
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM storage_states WHERE group_id IS NULL FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	finished := make(chan struct{}, 2)
	for _, g := range []*quota.Gate{a, b} {
		go func() {
			g.Refresh(ctx)
			finished <- struct{}{}
		}()
	}
	go func() { <-finished; <-finished; close(done) }()
	waitForLockWaits(t, admin, 2, done)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	var state string
	var seq int64
	if err := admin.QueryRow(ctx, `SELECT state, event_seq FROM storage_states WHERE group_id IS NULL`).Scan(&state, &seq); err != nil {
		t.Fatal(err)
	}
	if state != "full" || seq != 1 {
		t.Fatalf("persisted %s with event_seq %d, want full after one transition", state, seq)
	}
	if a.State(nil) != quota.Full || b.State(nil) != quota.Full {
		t.Fatalf("gates: %s, %s", a.State(nil), b.State(nil))
	}
}

// A group's state row comes with its first transition; transitions read
// the group's figures (snapshot, folded and open deltas) and quota under
// the lock; a removed group is ErrUnknownGroup. Event IDs carry the
// sequence, so warn, ok, warn are three events.
func TestGroupTransitions(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	admin := connect(t, url)
	g, err := st.CreateGroup(ctx, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetGroupQuota(ctx, g.ID, ptr(100)); err != nil {
		t.Fatal(err)
	}
	exec(t, admin, `UPDATE storage_snapshot SET computed_at = now()`)
	exec(t, admin, `INSERT INTO storage_usage_groups (group_id, messages, bytes, added_messages, added_bytes, computed_at)
		VALUES ($1, 1, 50, 1, 20, now())`, g.ID)
	exec(t, admin, `INSERT INTO storage_group_deltas (group_id, messages, bytes) VALUES ($1, 1, 10)`, g.ID)
	gate := newGate(st, quota.Config{})
	var events []string
	for _, step := range []struct {
		delta int64
		want  quota.State
	}{{0, quota.Warn}, {-11, quota.OK}, {11, quota.Warn}, {30, quota.Overflow}} {
		if step.delta != 0 {
			exec(t, admin, `INSERT INTO storage_group_deltas (group_id, messages, bytes) VALUES ($1, 0, $2)`, g.ID, step.delta)
		}
		gate.Refresh(ctx)
		if got := gate.State(&g.ID); got != step.want {
			t.Fatalf("after %+d: %s, want %s", step.delta, got, step.want)
		}
		var state string
		var seq int64
		if err := admin.QueryRow(ctx, `SELECT state, event_seq FROM storage_states WHERE group_id = $1`, g.ID).Scan(&state, &seq); err != nil {
			t.Fatal(err)
		}
		if state != step.want.String() {
			t.Fatalf("persisted %s, want %s", state, step.want)
		}
		events = append(events, store.StorageEventID(&g.ID, seq))
	}
	if events[0] == events[2] || events[0] != store.StorageEventID(&g.ID, 1) || store.StorageEventID(nil, 3) != "storage:instance:3" {
		t.Fatalf("event IDs %v", events)
	}
	if err := st.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	_, err = st.TransitionStorageState(ctx, &g.ID, func(string, store.ScopeFigure) (string, error) { return "full", nil })
	if !errors.Is(err, store.ErrUnknownGroup) {
		t.Fatalf("removed group: %v", err)
	}
}

func ptr(v int64) *int64 { return &v }
