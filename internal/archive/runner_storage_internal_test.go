package archive

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keepia/archive/internal/quota"
	"github.com/keepia/archive/internal/store"
)

// figures is a storage gate's database: group 1's bytes are set by the
// test, against a quota of 100.
type figures struct {
	mu    sync.Mutex
	bytes int64
	state string
}

func (f *figures) StorageGateFigures(context.Context) (store.GateFigures, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g1, g2, q := int64(1), int64(2), int64(100)
	return store.GateFigures{Counted: true, Instance: store.GateScope{State: "ok"}, Groups: []store.GateScope{
		{Group: &g1, Quota: &q, Bytes: f.bytes, State: f.state}, {Group: &g2, Quota: &q, State: "ok"},
	}}, nil
}

func (f *figures) TransitionStorageState(_ context.Context, group *int64,
	decide func(string, store.ScopeFigure) (string, error)) (store.StorageTransition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := int64(100)
	if group == nil {
		next, err := decide("ok", store.ScopeFigure{Counted: true})
		return store.StorageTransition{State: next}, err
	}
	fig := store.ScopeFigure{Bytes: f.bytes, Quota: &q, Counted: true}
	next, err := decide(f.state, fig)
	f.state = next
	return store.StorageTransition{State: next, Changed: true, Figure: fig}, err
}

// The schedule leaves out the accounts of a full group (group 1), not
// those of another group or without an owner, and logs each pause and
// its end once.
func TestScheduleSkipsPausedAccounts(t *testing.T) {
	db := &figures{bytes: 100, state: "ok"}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	gate := quota.New(quota.Config{Persist: true, Thresholds: quota.Thresholds{WarnPercent: 80, OverflowPercent: 10}}, db, nil,
		func() time.Time { return now }, nil)
	var logs bytes.Buffer
	r := &Runner{Syncer: &Syncer{Storage: gate, Logger: slog.New(slog.NewTextHandler(&logs, nil))}}
	g1, g2 := int64(1), int64(2)
	accounts := []*store.Account{{ID: 10, Name: "full", GroupID: &g1}, {ID: 11, Name: "other", GroupID: &g2}, {ID: 12, Name: "ownerless"}}
	due := []int64{10, 11, 12}
	for range 2 {
		if got := r.skipPaused(accounts, due); !slices.Equal(got, []int64{11, 12}) {
			t.Fatalf("due while group 1 is full: %v", got)
		}
	}
	if n := strings.Count(logs.String(), `msg="scheduled syncs paused: storage limit reached" account=full`); n != 1 {
		t.Fatalf("pause logged %d times:\n%s", n, &logs)
	}
	db.mu.Lock()
	db.bytes = 0
	db.mu.Unlock()
	now = now.Add(quota.DefaultInterval)
	if got := r.skipPaused(accounts, due); !slices.Equal(got, due) {
		t.Fatalf("due after space was freed: %v", got)
	}
	if !strings.Contains(logs.String(), `msg="scheduled syncs resumed" account=full`) {
		t.Fatalf("no resume logged:\n%s", &logs)
	}
}
