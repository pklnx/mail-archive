package web

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/quota"
	"github.com/pklnx/mail-archive/internal/store"
)

// gateFigures is a storage gate's database: group A's and the instance's
// bytes against quotas of 100.
type gateFigures struct {
	mu                sync.Mutex
	groupA            int64
	aBytes, instBytes int64
	aState, instState string
}

func (g *gateFigures) StorageGateFigures(context.Context) (store.GateFigures, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	q := int64(100)
	return store.GateFigures{Counted: true, Instance: store.GateScope{Bytes: g.instBytes, State: g.instState},
		Groups: []store.GateScope{{Group: &g.groupA, Quota: &q, Bytes: g.aBytes, State: g.aState}}}, nil
}

func (g *gateFigures) TransitionStorageState(_ context.Context, group *int64,
	decide func(string, store.ScopeFigure) (string, error)) (store.StorageTransition, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	q := int64(100)
	f, st := store.ScopeFigure{Bytes: g.instBytes, Counted: true}, &g.instState
	if group != nil {
		f, st = store.ScopeFigure{Bytes: g.aBytes, Quota: &q, Counted: true}, &g.aState
	}
	next, err := decide(*st, f)
	*st = next
	return store.StorageTransition{State: next, Changed: true, Figure: f}, err
}

func (g *gateFigures) set(gate *quota.Gate, aBytes, instBytes int64) {
	g.mu.Lock()
	g.aBytes, g.instBytes = aBytes, instBytes
	g.mu.Unlock()
	gate.Refresh(context.Background())
}

// While group A is full, /healthz/sync lists its enabled accounts as
// paused (503, degraded) and never as stale; group B's are not paused. A
// manual sync of group A answers 409 storage_full, the same answer for a
// full instance; group B's is queued.
func TestStoragePausedSyncs(t *testing.T) {
	db := &gateFigures{aState: "ok", instState: "ok"}
	gate := quota.New(quota.Config{Persist: true, InstanceQuota: 100, Thresholds: quota.Thresholds{WarnPercent: 80, OverflowPercent: 10},
		Interval: 24 * time.Hour}, db, nil, nil, nil)
	f := newAuthFixtureOpts(t, Options{
		Syncer: &archive.Syncer{Storage: gate},
		Runner: &archive.Runner{Schedule: archive.Schedule{Sync: 6 * time.Hour, SyncMin: 5 * time.Minute}},
	})
	ctx := context.Background()
	kim := f.addUser("kim", "correct horse battery", false)
	lee := f.addUser("lee", "correct horse battery", false)
	gb, err := f.st.CreateGroup(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetUserGroup(ctx, lee.ID, gb.ID); err != nil {
		t.Fatal(err)
	}
	db.groupA = kim.GroupID
	a := f.imapAccount("work", kim)
	off := f.imapAccount("off", kim)
	disabled := false
	if err := f.st.UpdateAccount(ctx, off.Ref(), store.AccountChange{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	b := f.imapAccount("other", lee)
	f.runs(a, "ok", 1)
	f.runs(b, "ok", 1)
	db.set(gate, 100, 10) // group A full

	health := func() (int, syncHealthJSON) {
		t.Helper()
		f.clock.Advance(syncHealthTTL)
		status, body := f.syncHealth("")
		var out syncHealthJSON
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return status, out
	}
	if status, out := health(); status != 503 || out.Status != "degraded" || !slices.Equal(out.Paused, []int64{a.ID}) || len(out.Stale) != 0 {
		t.Fatalf("group A full: %d %+v", status, out)
	}
	// 13 hours later: B is stale, A is paused and not stale.
	f.clock.Advance(13 * time.Hour)
	if _, out := health(); !slices.Equal(out.Paused, []int64{a.ID}) || !slices.Equal(out.Stale, []int64{b.ID}) {
		t.Fatalf("13 hours later: %+v", out)
	}

	f.loginOK("kim", "correct horse battery")
	full := f.expect("POST", "/api/accounts/work/sync", nil, 409)
	if full["code"] != "storage_full" {
		t.Fatalf("manual sync: %v", full)
	}
	if out := f.expect("POST", "/api/sync", nil, 409); out["code"] != "storage_full" {
		t.Fatalf("sync all: %v", out)
	}
	// The instance full instead: the same answer, byte for byte.
	db.set(gate, 10, 100)
	if out := f.expect("POST", "/api/accounts/work/sync", nil, 409); !mapsEqual(out, full) {
		t.Fatalf("instance full: %v, group full: %v", out, full)
	}
	g := f.as()
	g.loginOK("lee", "correct horse battery")
	g.expect("POST", "/api/accounts/other/sync", nil, 409) // the instance is everyone's
	db.set(gate, 100, 10)
	g.expect("POST", "/api/accounts/other/sync", nil, 202)
	// Space freed: kim's syncs are queued again.
	db.set(gate, 0, 10)
	f.expect("POST", "/api/accounts/work/sync", nil, 202)
	if _, out := health(); len(out.Paused) != 0 {
		t.Fatalf("after space was freed: %+v", out)
	}
}

func mapsEqual(a, b map[string]any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
