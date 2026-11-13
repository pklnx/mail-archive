package quota

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

var th = Thresholds{WarnPercent: 80, OverflowPercent: 10}

func TestLevelAndNext(t *testing.T) {
	const q = 1000 // warn 800, full 1000, overflow 1100, band 10
	for _, tc := range []struct {
		cur   State
		bytes int64
		want  State
	}{
		{OK, 0, OK}, {OK, 799, OK}, {OK, 800, Warn}, {OK, 1000, Full}, {OK, 1100, Overflow}, {OK, 5000, Overflow},
		{Warn, 790, Warn}, {Warn, 789, OK}, {Warn, 999, Warn}, {Warn, 1000, Full},
		{Full, 990, Full}, {Full, 989, Warn}, {Full, 790, Warn}, {Full, 789, OK}, {Full, 1099, Full}, {Full, 1100, Overflow},
		{Overflow, 1090, Overflow}, {Overflow, 1089, Full}, {Overflow, 500, OK},
	} {
		if got := th.Next(tc.cur, q, tc.bytes); got != tc.want {
			t.Errorf("Next(%s, %d) = %s, want %s", tc.cur, tc.bytes, got, tc.want)
		}
	}
	if got := th.Next(Full, 0, 1<<40); got != OK {
		t.Errorf("without a quota: %s, want ok", got)
	}
	// Overflow at 0 %: full and overflow from the quota on.
	if got := (Thresholds{WarnPercent: 80}).Level(q, q); got != Overflow {
		t.Errorf("overflow 0 %%: %s", got)
	}
	// No overflow in the threshold arithmetic near 1 EiB.
	if got := th.Level(1<<60, 1<<60); got != Full {
		t.Errorf("1 EiB: %s", got)
	}
}

// For random quotas, percentages and figures, the state machine never
// changes state inside the hysteresis band, never stops short of or goes
// past a threshold when it moves, and is monotone in the figure.
func TestNextProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 200_000 {
		tt := Thresholds{WarnPercent: 1 + r.IntN(99), OverflowPercent: r.IntN(101)}
		q := 1 + r.Int64N(1<<40)
		if r.IntN(4) == 0 {
			q = 1 + r.Int64N(1000) // small quotas: the band rounds to 0
		}
		cur := State(r.IntN(4))
		u := r.Int64N(q * 2)
		band := q / 100
		got := tt.Next(cur, q, u)
		level := tt.Level(q, u)
		switch {
		case level > cur:
			if got != level {
				t.Fatalf("%+v q=%d cur=%s u=%d: up to %s, want %s", tt, q, cur, u, got, level)
			}
		case cur > OK && u >= tt.threshold(q, cur)-band:
			if got != cur {
				t.Fatalf("%+v q=%d cur=%s u=%d: left inside the band to %s", tt, q, cur, u, got)
			}
		default:
			// Down: to the worst state the figure still holds with the band,
			// never past it.
			if got > cur || got > OK && u < tt.threshold(q, got)-band {
				t.Fatalf("%+v q=%d cur=%s u=%d: down to %s", tt, q, cur, u, got)
			}
			for s := got + 1; s < cur; s++ {
				if u >= tt.threshold(q, s)-band {
					t.Fatalf("%+v q=%d cur=%s u=%d: down to %s skips %s", tt, q, cur, u, got, s)
				}
			}
		}
		if more := tt.Next(cur, q, u+r.Int64N(q)); more < got {
			t.Fatalf("%+v q=%d cur=%s: not monotone at %d", tt, q, cur, u)
		}
	}
}

// fakeDB is a database for gates: figures, persisted states and the row
// lock of TransitionStorageState (its mutex), with every version of the
// figures, so that a gate can read old ones.
type fakeDB struct {
	mu          sync.Mutex
	versions    []store.GateFigures // the figures over time; the last is current
	reads       int
	transitions int
	// onTransition, if set, sees every persisted transition with the
	// figures it was decided on.
	onTransition func(group *int64, from, to string, f store.ScopeFigure)
}

func newFakeDB(f store.GateFigures) *fakeDB {
	if f.Instance.State == "" {
		f.Instance.State = "ok"
	}
	for i := range f.Groups {
		if f.Groups[i].State == "" {
			f.Groups[i].State = "ok"
		}
	}
	return &fakeDB{versions: []store.GateFigures{f}}
}

// set records new figures (states are carried over from the current).
func (d *fakeDB) set(change func(*store.GateFigures)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.versions[len(d.versions)-1]
	next := cur
	next.Groups = append([]store.GateScope(nil), cur.Groups...)
	change(&next)
	d.versions = append(d.versions, next)
}

func (d *fakeDB) current() *store.GateFigures { return &d.versions[len(d.versions)-1] }

// view is a gate's connection: it reads the figures lag versions old.
type view struct {
	db  *fakeDB
	lag int
}

func (v *view) StorageGateFigures(context.Context) (store.GateFigures, error) {
	v.db.mu.Lock()
	defer v.db.mu.Unlock()
	v.db.reads++
	i := max(0, len(v.db.versions)-1-v.lag)
	f := v.db.versions[i]
	f.Groups = append([]store.GateScope(nil), f.Groups...)
	return f, nil
}

func (v *view) TransitionStorageState(_ context.Context, group *int64,
	decide func(string, store.ScopeFigure) (string, error)) (store.StorageTransition, error) {
	d := v.db
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.current()
	sc := &cur.Instance
	fig := store.ScopeFigure{Bytes: cur.Instance.Bytes, Counted: true}
	if group != nil {
		sc = nil
		for i := range cur.Groups {
			if *cur.Groups[i].Group == *group {
				sc = &cur.Groups[i]
			}
		}
		if sc == nil {
			return store.StorageTransition{}, store.ErrUnknownGroup
		}
		fig = store.ScopeFigure{Bytes: sc.Bytes, Quota: sc.Quota, Counted: cur.Counted}
	}
	next, err := decide(sc.State, fig)
	if err != nil {
		return store.StorageTransition{}, err
	}
	t := store.StorageTransition{State: sc.State, EventSeq: sc.EventSeq, Figure: fig}
	if next != sc.State {
		if d.onTransition != nil {
			d.onTransition(group, sc.State, next, fig)
		}
		sc.State, sc.EventSeq = next, sc.EventSeq+1
		d.transitions++
		t.State, t.EventSeq, t.Changed = next, sc.EventSeq, true
	}
	return t, nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func ptr(v int64) *int64 { return &v }

const gib = int64(1) << 30

func testGate(cfg Config, src Source, free func() (int64, error), c *clock) *Gate {
	if cfg.Thresholds == (Thresholds{}) {
		cfg.Thresholds = th
	}
	cfg.Persist = true
	return New(cfg, src, free, c.now, quiet())
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)} }

// The effective state is the worst of the group's, the instance's and the
// disk's; the scope names the limit, the group's first.
func TestGateWorstOfThree(t *testing.T) {
	g1, g2 := int64(1), int64(2)
	db := newFakeDB(store.GateFigures{Counted: true,
		Instance: store.GateScope{Bytes: 50 * gib},
		Groups: []store.GateScope{
			{Group: &g1, Quota: ptr(10 * gib), Bytes: 10 * gib}, // full
			{Group: &g2, Quota: ptr(10 * gib), Bytes: 1 * gib},  // ok
		}})
	free := 100 * gib
	c := newClock()
	g := testGate(Config{InstanceQuota: 100 * gib, MinFreeDisk: gib}, &view{db: db}, func() (int64, error) { return free, nil }, c)
	check := func(group *int64, want State, wantScope Scope) {
		t.Helper()
		st, sc := g.effective(group)
		if st != want || sc != wantScope {
			t.Fatalf("group %v: %s/%q, want %s/%q", group, st, sc, want, wantScope)
		}
		err := g.Admit(group)
		var fe *FullError
		if want.Paused() != (err != nil) || err != nil && (!errors.As(err, &fe) || !errors.Is(err, ErrStorageFull) || fe.Scope != wantScope) {
			t.Fatalf("group %v: Admit = %v", group, err)
		}
	}
	check(&g1, Full, ScopeGroup)
	check(&g2, OK, "")
	check(nil, OK, "")

	db.set(func(f *store.GateFigures) { f.Instance.Bytes = 111 * gib }) // instance overflow
	c.add(DefaultInterval)
	check(&g1, Overflow, ScopeInstance)
	check(&g2, Overflow, ScopeInstance)
	check(nil, Overflow, ScopeInstance)

	free = gib - 1
	c.add(DefaultInterval)
	check(&g2, DiskLow, ScopeDisk)
	check(nil, DiskLow, ScopeDisk)
}

// A group without a quota and an account without an owner follow the
// instance only; so does every group before the first snapshot, and
// nothing is persisted for it.
func TestGateInstanceOnly(t *testing.T) {
	g1, g2 := int64(1), int64(2)
	db := newFakeDB(store.GateFigures{Counted: false,
		Instance: store.GateScope{Bytes: 85 * gib},
		Groups: []store.GateScope{
			{Group: &g1, Quota: ptr(gib), Bytes: 50 * gib},
			{Group: &g2, Bytes: 80 * gib},
		}})
	c := newClock()
	g := testGate(Config{InstanceQuota: 100 * gib}, &view{db: db}, nil, c)
	for _, grp := range []*int64{&g1, &g2, nil} {
		if st := g.State(grp); st != Warn {
			t.Fatalf("before the first snapshot, group %v: %s, want the instance's warn", grp, st)
		}
	}
	if db.current().Groups[0].State != "ok" || db.transitions != 1 {
		t.Fatalf("persisted: group %s, %d transitions (want only the instance's)", db.current().Groups[0].State, db.transitions)
	}
	db.set(func(f *store.GateFigures) { f.Counted = true })
	c.add(DefaultInterval)
	if st := g.State(&g1); st != Overflow {
		t.Fatalf("after the snapshot: %s", st)
	}
	if st := g.State(&g2); st != Warn {
		t.Fatalf("group without a quota: %s", st)
	}
	if st := g.State(nil); st != Warn {
		t.Fatalf("without an owner: %s", st)
	}
	// A quota removed: the group goes back to ok, persisted.
	db.set(func(f *store.GateFigures) { f.Groups[0].Quota = nil })
	c.add(DefaultInterval)
	if st := g.State(&g1); st != Warn || db.current().Groups[0].State != "ok" {
		t.Fatalf("quota removed: %s, persisted %s", st, db.current().Groups[0].State)
	}
}

// The bytes this process stored count until the next read, which then
// holds them; they are never persisted.
func TestGateLocalBytes(t *testing.T) {
	g1 := int64(1)
	db := newFakeDB(store.GateFigures{Counted: true,
		Instance: store.GateScope{Bytes: 0},
		Groups:   []store.GateScope{{Group: &g1, Quota: ptr(10 * gib), Bytes: 9 * gib}}})
	c := newClock()
	g := testGate(Config{}, &view{db: db}, nil, c)
	if st := g.State(&g1); st != Warn {
		t.Fatalf("start: %s", st)
	}
	g.Wrote(&g1, gib)
	if st := g.State(&g1); st != Full {
		t.Fatalf("after storing a GiB locally: %s, want full", st)
	}
	if db.current().Groups[0].State != "warn" {
		t.Fatalf("local bytes persisted: %s", db.current().Groups[0].State)
	}
	// The next read has them; local bytes are dropped, not counted twice.
	db.set(func(f *store.GateFigures) { f.Groups[0].Bytes = 10 * gib })
	c.add(DefaultInterval)
	if st := g.State(&g1); st != Full || db.current().Groups[0].State != "full" {
		t.Fatalf("after the read: %s, persisted %s", st, db.current().Groups[0].State)
	}
	g.mu.RLock()
	local := g.localGroups[g1] + g.localInstance
	g.mu.RUnlock()
	if local != 0 {
		t.Fatalf("local bytes kept after the read: %d", local)
	}
	// An account without an owner counts for the instance only.
	g.Wrote(nil, 5)
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.localInstance != 5 || len(g.localGroups) != 0 {
		t.Fatalf("ownerless write: instance %d, groups %v", g.localInstance, g.localGroups)
	}
}

// Checks read the figures at most once per interval of the gate's clock.
func TestGateInterval(t *testing.T) {
	db := newFakeDB(store.GateFigures{Counted: true})
	c := newClock()
	g := testGate(Config{InstanceQuota: gib}, &view{db: db}, nil, c)
	for range 5 {
		g.State(nil)
	}
	if db.reads != 1 {
		t.Fatalf("reads = %d, want 1", db.reads)
	}
	c.add(DefaultInterval - time.Nanosecond)
	g.State(nil)
	if db.reads != 1 {
		t.Fatalf("reads before the interval = %d", db.reads)
	}
	c.add(time.Nanosecond)
	g.State(nil)
	g.State(nil)
	if db.reads != 2 {
		t.Fatalf("reads after the interval = %d, want 2", db.reads)
	}
	// A clock that went back reads again rather than trusting the cache.
	c.add(-time.Hour)
	g.State(nil)
	if db.reads != 3 {
		t.Fatalf("reads after the clock went back = %d", db.reads)
	}
}

// disk_low through the injected free space: entered below the reserve,
// left 1 % of the reserve above it, decided again under the row lock with
// a fresh statfs.
func TestGateDiskLow(t *testing.T) {
	db := newFakeDB(store.GateFigures{Counted: true})
	c := newClock()
	free := 100 * gib
	calls := 0
	g := testGate(Config{MinFreeDisk: 100 * gib}, &view{db: db}, func() (int64, error) { calls++; return free, nil }, c)
	step := func(f int64, want State, persisted string) {
		t.Helper()
		free = f
		c.add(DefaultInterval)
		if st := g.State(nil); st != want || db.current().Instance.State != persisted {
			t.Fatalf("free %d: %s, persisted %s; want %s, %s", f, st, db.current().Instance.State, want, persisted)
		}
	}
	step(100*gib, OK, "ok")
	step(100*gib-1, DiskLow, "disk_low")
	step(100*gib+gib-1, DiskLow, "disk_low") // inside the band of 1 GiB
	step(101*gib, OK, "ok")
	// The free space at the read is low, but under the lock it is not
	// any more: nothing is persisted.
	calls = 0
	free = 50 * gib
	g2 := testGate(Config{MinFreeDisk: 100 * gib}, &view{db: db}, func() (int64, error) {
		calls++
		if calls > 1 {
			return 200 * gib, nil
		}
		return free, nil
	}, c)
	g2.Refresh(context.Background())
	if calls != 2 || db.current().Instance.State != "ok" {
		t.Fatalf("statfs calls %d, persisted %s; want 2 and ok", calls, db.current().Instance.State)
	}
	// A failing statfs leaves the reserve unchecked.
	g3 := testGate(Config{MinFreeDisk: 100 * gib}, &view{db: db}, func() (int64, error) { return 0, errors.New("statfs failed") }, c)
	if st := g3.State(nil); st != OK {
		t.Fatalf("statfs failing: %s", st)
	}
}

// Two gates fed the same figures persist one transition and agree on
// every state.
func TestGatesAgree(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	groups := []int64{1, 2, 3}
	db := newFakeDB(store.GateFigures{Counted: true})
	db.set(func(f *store.GateFigures) {
		for i := range groups {
			f.Groups = append(f.Groups, store.GateScope{Group: &groups[i], Quota: ptr(1000), Bytes: 0})
		}
	})
	c := newClock()
	cfg := Config{InstanceQuota: 3000}
	a, b := testGate(cfg, &view{db: db}, nil, c), testGate(cfg, &view{db: db}, nil, c)
	for step := range 2000 {
		db.set(func(f *store.GateFigures) {
			for i := range f.Groups {
				f.Groups[i].Bytes = max(0, f.Groups[i].Bytes+r.Int64N(81)-40)
			}
			f.Instance.Bytes = f.Groups[0].Bytes + f.Groups[1].Bytes + f.Groups[2].Bytes
		})
		c.add(DefaultInterval)
		for i := range groups {
			if sa, sb := a.State(&groups[i]), b.State(&groups[i]); sa != sb {
				t.Fatalf("step %d, group %d: %s and %s", step, groups[i], sa, sb)
			}
		}
		if sa, sb := a.State(nil), b.State(nil); sa != sb {
			t.Fatalf("step %d, instance: %s and %s", step, sa, sb)
		}
	}
}

// Gates that read figures of different ages never persist a state the
// current figures do not call for: every transition is decided on the
// figures read under the lock, from the state persisted there. At the end
// the persisted states are those of a model that saw every figure in
// order.
func TestGatesDifferentAges(t *testing.T) {
	for seed := range uint64(20) {
		r := rand.New(rand.NewPCG(seed, 99))
		g1 := int64(1)
		db := newFakeDB(store.GateFigures{Counted: true,
			Groups: []store.GateScope{{Group: &g1, Quota: ptr(1000), Bytes: 0}}})
		var bad []string
		db.onTransition = func(group *int64, from, to string, f store.ScopeFigure) {
			cur, _ := ParseState(from)
			q := int64(2000)
			if group != nil {
				q = *f.Quota
			}
			if want := th.Next(cur, q, f.Bytes); want.String() != to {
				bad = append(bad, from+"->"+to)
			}
		}
		c := newClock()
		cfg := Config{InstanceQuota: 2000}
		gates := []*Gate{
			testGate(cfg, &view{db: db, lag: 0}, nil, c),
			testGate(cfg, &view{db: db, lag: 3}, nil, c),
			testGate(cfg, &view{db: db, lag: 7}, nil, c),
		}
		// The model walks every version in order.
		model := map[string]State{"i": OK, "g": OK}
		for range 500 {
			db.set(func(f *store.GateFigures) {
				f.Groups[0].Bytes = max(0, f.Groups[0].Bytes+r.Int64N(121)-60)
				f.Instance.Bytes = max(0, f.Instance.Bytes+r.Int64N(241)-120)
			})
			gates[r.IntN(len(gates))].Refresh(context.Background())
		}
		if len(bad) > 0 {
			t.Fatalf("seed %d: transitions not called for by the figures under the lock: %v", seed, bad)
		}
		// A last read with current figures settles every scope as the model
		// does from the persisted state.
		gates[0].Refresh(context.Background())
		cur := db.current()
		pi, _ := ParseState(cur.Instance.State)
		pg, _ := ParseState(cur.Groups[0].State)
		model["i"], model["g"] = th.Next(pi, 2000, cur.Instance.Bytes), th.Next(pg, 1000, cur.Groups[0].Bytes)
		if pi != model["i"] || pg != model["g"] {
			t.Fatalf("seed %d: persisted %s/%s, figures call for %s/%s", seed, pi, pg, model["i"], model["g"])
		}
	}
}

// A nil gate lets everything through: processes without one (tests,
// commands that do not store mail).
func TestNilGate(t *testing.T) {
	var g *Gate
	if g.State(nil) != OK || g.Admit(ptr(1)) != nil {
		t.Fatal("nil gate refuses")
	}
}
