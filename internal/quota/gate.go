package quota

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

// DefaultInterval is how often a gate reads the figures.
const DefaultInterval = 10 * time.Second

// readTimeout bounds one read of the figures, with its transitions.
const readTimeout = 30 * time.Second

// Config holds the instance's limits (config.Storage).
type Config struct {
	// InstanceQuota in message bytes; 0 means none.
	InstanceQuota int64
	Thresholds
	// MinFreeDisk is the disk reserve in bytes; 0 turns it off.
	MinFreeDisk int64
	// Interval between reads (default DefaultInterval).
	Interval time.Duration
	// Persist makes this gate write the transitions its figures call for;
	// without it (tests of a reader) it decides them in memory only. With
	// Writer set, it writes only while it holds the writer lock.
	Persist bool
	// Writer is the storage state writer lock (store.StorageWriter); nil:
	// no lock, the gate writes whenever Persist is set. serve holds the
	// lock for its run (ClaimWriter); a command takes it only for a read
	// that has a transition to write, and only if no serve holds it. So
	// while serve runs, a command with other limits (a newer .env before
	// the web server restarts) decides the states in memory and never
	// flips the persisted state back and forth; without serve, the
	// commands write the transitions.
	Writer WriterLock
	// ClaimWriter makes the gate try to take the writer lock at every
	// read, and keep it (serve).
	ClaimWriter bool
	// OnTransition, if set, is called after this gate persisted a
	// transition (the notifier's Wake).
	OnTransition func()
}

// WriterLock is the storage state writer lock (store.StorageWriter).
type WriterLock interface {
	TryLock(ctx context.Context) (unlock func(), ok bool, err error)
}

// Source is where a gate reads figures and persists transitions: the
// store.
type Source interface {
	StorageGateFigures(ctx context.Context) (store.GateFigures, error)
	TransitionStorageState(ctx context.Context, group *int64,
		decide func(current string, f store.ScopeFigure) (string, error)) (store.StorageTransition, error)
}

// Gate is a process's storage gate. Every Interval, or when asked after
// that time, it reads the figures of every scope with their persisted
// states in one statement, and the free space of the data directory. To
// those figures it adds what this process stored since (Wrote), so a bulk
// import cannot overshoot between reads; the next read contains them.
//
// When a scope's figures call for another state than the persisted one,
// a gate that holds the storage state writer lock (serve for its whole
// run, a command only while no serve runs; Config.Writer) persists the
// transition from the database figures alone (and statfs, for disk_low):
// under the row lock of the scope's state, with the figures read again
// (store.TransitionStorageState). Two gates that see a crossing at once
// make one transition. A gate without the lock decides the same states in
// memory. The local and pending bytes only make this process pause
// earlier; they are never persisted.
//
// The checks themselves (State, Admit) are lookups in memory: their time
// does not depend on other groups' figures.
type Gate struct {
	cfg Config
	src Source
	// free returns the free bytes of the data directory's file system;
	// nil: no disk reserve.
	free func() (int64, error)
	now  func() time.Time
	log  *slog.Logger

	// refresh serializes reads; a check that finds the figures old reads
	// them only if no read runs (the first read is waited for).
	refresh sync.Mutex

	mu      sync.RWMutex
	read    bool
	readAt  time.Time
	counted bool
	// instance and groups are the persisted states and database figures
	// of the last read.
	instance scope
	groups   map[int64]scope
	// freeDisk is the last statfs; -1 unknown.
	freeDisk int64
	// local are the bytes this process stored since the read: instance
	// and per group.
	localInstance int64
	localGroups   map[int64]int64
	// pending are the bytes of message files written whose batch has not
	// committed yet (Pending), so that a sync, fill or import stops within
	// one message of a limit, not one batch.
	pendingInstance int64
	pendingGroups   map[int64]int64
	// diskErr is set while statfs fails, so it is logged once.
	diskErr bool
}

// scope is a scope as the gate last read it.
type scope struct {
	state State
	bytes int64
	quota int64 // 0: none
}

// New returns a gate. free returns the free bytes of the file system that
// holds the archive (FreeSpace of the data directory); nil turns the disk
// reserve off. now is the clock (nil: time.Now).
func New(cfg Config, src Source, free func() (int64, error), now func() time.Time, log *slog.Logger) *Gate {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Gate{cfg: cfg, src: src, free: free, now: now, log: log, freeDisk: -1,
		groups: map[int64]scope{}, localGroups: map[int64]int64{}, pendingGroups: map[int64]int64{}}
}

// Config returns the gate's limits.
func (g *Gate) Config() Config { return g.cfg }

// Wrote adds bytes a committed save stored for a group (nil: an account
// without an owner) to this process's figures until the next read
// (store.SetWriteObserver).
func (g *Gate) Wrote(group *int64, bytes int64) {
	if g == nil || bytes <= 0 {
		return
	}
	g.mu.Lock()
	g.localInstance += bytes
	if group != nil {
		g.localGroups[*group] += bytes
	}
	g.mu.Unlock()
}

// Run reads the figures every Interval until ctx ends, so transitions
// are persisted also while nothing is stored (serve).
func (g *Gate) Run(ctx context.Context) {
	t := time.NewTicker(g.cfg.Interval)
	defer t.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, readTimeout)
		g.Refresh(rctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Refresh reads the figures now and persists the transitions they call
// for. A failed read keeps the last figures; it is logged.
func (g *Gate) Refresh(ctx context.Context) {
	g.refresh.Lock()
	defer g.refresh.Unlock()
	g.refreshLocked(ctx)
}

// fresh reads the figures if the last read is Interval old: it waits for
// the first read, and later ones only if no other read runs.
func (g *Gate) fresh() {
	g.mu.RLock()
	read, age := g.read, g.now().Sub(g.readAt)
	g.mu.RUnlock()
	if read && age < g.cfg.Interval && age >= 0 {
		return
	}
	if read {
		if !g.refresh.TryLock() {
			return
		}
	} else {
		g.refresh.Lock()
	}
	defer g.refresh.Unlock()
	g.mu.RLock()
	read, age = g.read, g.now().Sub(g.readAt)
	g.mu.RUnlock()
	if read && age < g.cfg.Interval && age >= 0 {
		return // another check read meanwhile
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	g.refreshLocked(ctx)
}

// refreshLocked is Refresh; the caller holds g.refresh.
func (g *Gate) refreshLocked(ctx context.Context) {
	at := g.now()
	// The local bytes up to now are in the figures read below: they were
	// committed before. What is stored during the read is kept, and may be
	// counted twice until the next read (an upper bound).
	g.mu.Lock()
	takenInstance, takenGroups := g.localInstance, make(map[int64]int64, len(g.localGroups))
	for id, n := range g.localGroups {
		takenGroups[id] = n
	}
	g.mu.Unlock()

	figs, err := g.src.StorageGateFigures(ctx)
	if err != nil {
		g.log.Error("storage gate: read the figures", "err", err)
		g.mu.Lock()
		// Try again after an interval, keeping the last figures (or none:
		// an unread gate lets everything through, as storing fails anyway
		// without the database).
		g.readAt, g.read = at, true
		g.mu.Unlock()
		return
	}
	free := g.statfs()
	w := g.writer(ctx)
	defer w.done()
	if g.cfg.ClaimWriter {
		w.may()
	}
	inst := scope{state: parseOr(figs.Instance.State, g.log), bytes: figs.Instance.Bytes, quota: g.cfg.InstanceQuota}
	if next := g.decideInstance(inst.state, inst.bytes, free); next != inst.state && w.may() {
		inst = g.transition(ctx, nil, inst)
	}
	groups := make(map[int64]scope, len(figs.Groups))
	for _, fg := range figs.Groups {
		sc := scope{state: parseOr(fg.State, g.log), bytes: fg.Bytes, quota: deref(fg.Quota)}
		if next := g.decideGroup(sc.state, sc.bytes, sc.quota, figs.Counted); next != sc.state && w.may() {
			sc = g.transition(ctx, fg.Group, sc)
		}
		groups[*fg.Group] = sc
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.read, g.readAt, g.counted = true, at, figs.Counted
	g.instance, g.groups, g.freeDisk = inst, groups, free
	g.localInstance -= takenInstance
	for id, n := range takenGroups {
		if g.localGroups[id] -= n; g.localGroups[id] <= 0 {
			delete(g.localGroups, id)
		}
	}
}

// readWriter decides, once per read and only when asked, whether this
// gate may write transitions: Persist, and the writer lock if there is
// one.
type readWriter struct {
	g       *Gate
	ctx     context.Context
	decided bool
	ok      bool
	unlock  func()
}

func (g *Gate) writer(ctx context.Context) *readWriter { return &readWriter{g: g, ctx: ctx} }

func (w *readWriter) may() bool {
	if w.decided {
		return w.ok
	}
	w.decided = true
	switch {
	case !w.g.cfg.Persist:
	case w.g.cfg.Writer == nil:
		w.ok = true
	default:
		unlock, ok, err := w.g.cfg.Writer.TryLock(w.ctx)
		if err != nil {
			w.g.log.Error("storage gate: take the state writer lock", "err", err)
		}
		w.ok, w.unlock = ok, unlock
		if !ok && err == nil {
			w.g.log.Debug("storage gate: another process writes the states (serve); deciding in memory")
		}
	}
	return w.ok
}

// done frees a writer lock taken for this read.
func (w *readWriter) done() {
	if w.unlock != nil {
		w.unlock()
	}
}

// transition persists a scope's transition under its row lock, from the
// figures read again there, and returns the scope as persisted. On an
// error the scope stays as read.
func (g *Gate) transition(ctx context.Context, group *int64, sc scope) scope {
	var reread int64 = -1
	t, err := g.src.TransitionStorageState(ctx, group, func(current string, f store.ScopeFigure) (string, error) {
		cur, err := ParseState(current)
		if err != nil {
			return current, err
		}
		if group == nil {
			// statfs again too, under the lock (revision 5, point 8).
			reread = g.statfs()
			return g.decideInstance(cur, f.Bytes, reread).String(), nil
		}
		return g.decideGroup(cur, f.Bytes, deref(f.Quota), f.Counted).String(), nil
	})
	if errors.Is(err, store.ErrUnknownGroup) {
		return scope{}
	}
	if err != nil {
		g.log.Error("storage gate: persist a transition", "scope", scopeName(group), "err", err)
		return sc
	}
	from := sc.state
	sc.state, sc.bytes = parseOr(t.State, g.log), t.Figure.Bytes
	if group != nil {
		sc.quota = deref(t.Figure.Quota)
	}
	if t.Changed {
		attrs := []any{"scope", scopeName(group), "from", from.String(), "to", sc.state.String(),
			"bytes", sc.bytes, "quota_bytes", sc.quota, "event", store.StorageEventID(group, t.EventSeq)}
		if group == nil {
			attrs = append(attrs, "free_disk_bytes", reread, "min_free_disk_bytes", g.cfg.MinFreeDisk)
		}
		if sc.state > from && sc.state >= Full {
			g.log.Warn("storage state changed", attrs...)
		} else {
			g.log.Info("storage state changed", attrs...)
		}
		if g.cfg.OnTransition != nil {
			g.cfg.OnTransition()
		}
	}
	return sc
}

// decideInstance is the instance's next persisted state after cur for
// its exact bytes and the free disk space (-1: unknown).
func (g *Gate) decideInstance(cur State, bytes, free int64) State {
	if free >= 0 && diskLow(cur, g.cfg.MinFreeDisk, free) {
		return DiskLow
	}
	base := cur
	if base == DiskLow {
		base = OK // the quota state is not kept while disk_low: from scratch
	}
	return g.cfg.Next(base, g.cfg.InstanceQuota, bytes)
}

// decideGroup is a group's next persisted state after cur: ok without a
// quota and before the first snapshot.
func (g *Gate) decideGroup(cur State, bytes, quota int64, counted bool) State {
	if !counted {
		return OK
	}
	if cur > Overflow {
		cur = Overflow
	}
	return g.cfg.Next(cur, quota, bytes)
}

// statfs returns the free bytes, -1 without a reserve or when it fails.
func (g *Gate) statfs() int64 {
	if g.free == nil || g.cfg.MinFreeDisk <= 0 {
		return -1
	}
	n, err := g.free()
	g.mu.Lock()
	logIt := err != nil && !g.diskErr
	g.diskErr = err != nil
	g.mu.Unlock()
	if logIt {
		g.log.Error("storage gate: free disk space unknown; the disk reserve is not checked", "err", err)
	}
	if err != nil {
		return -1
	}
	return n
}

// State is the effective state of an account of the group (nil: without
// an owner): the worst of the group's (with a quota, after the first
// snapshot), the instance's and the disk's, each with what this process
// stored since the last read.
func (g *Gate) State(group *int64) State {
	st, _ := g.effective(group)
	return st
}

// Admit returns nil while syncs, fills and imports of the group may store
// new mail (below full), else a *FullError.
func (g *Gate) Admit(group *int64) error {
	st, sc := g.effective(group)
	if !st.Paused() {
		return nil
	}
	return &FullError{State: st, Scope: sc, Group: group}
}

func (g *Gate) effective(group *int64) (State, Scope) {
	if g == nil {
		return OK, ""
	}
	g.fresh()
	g.mu.RLock()
	defer g.mu.RUnlock()
	worst, why := OK, Scope("")
	if group != nil && g.counted {
		if sc, ok := g.groups[*group]; ok {
			worst = g.cfg.Next(min(sc.state, Overflow), sc.quota, sc.bytes+g.localGroups[*group]+g.pendingGroups[*group])
			why = ScopeGroup
		}
	}
	base := g.instance.state
	if base == DiskLow {
		base = OK
	}
	added := g.localInstance + g.pendingInstance
	if st := g.cfg.Next(base, g.cfg.InstanceQuota, g.instance.bytes+added); st > worst {
		worst, why = st, ScopeInstance
	}
	if g.freeDisk >= 0 && diskLow(g.instance.state, g.cfg.MinFreeDisk, g.freeDisk-added) {
		worst, why = DiskLow, ScopeDisk
	}
	if worst == OK {
		why = ""
	}
	return worst, why
}

// Pending counts the message files one writer (a sync's folder, a fill's
// batch, an import) has written for a group whose batch has not committed
// yet. The gate adds them to the group's and the instance's figures, so
// that the writer's next check sees them: a running sync, fill or import
// stops within one message of a limit. When the batch commits, the store
// tells the gate what it really added (Wrote) and the writer settles its
// pending bytes; a batch that fails or is dropped settles them too. Not
// safe for concurrent use; one per writer.
type Pending struct {
	g     *Gate
	group *int64
	bytes int64
}

// Pending returns a writer's pending counter for the group (nil: an
// account without an owner). A nil gate gives a counter that counts
// nothing.
func (g *Gate) Pending(group *int64) *Pending {
	return &Pending{g: g, group: group}
}

// Add counts a message file of n bytes that was written.
func (p *Pending) Add(n int64) {
	if p == nil || p.g == nil || n <= 0 {
		return
	}
	g := p.g
	g.mu.Lock()
	g.pendingInstance += n
	if p.group != nil {
		g.pendingGroups[*p.group] += n
	}
	g.mu.Unlock()
	p.bytes += n
}

// Bytes returns what is pending.
func (p *Pending) Bytes() int64 {
	if p == nil {
		return 0
	}
	return p.bytes
}

// Settle drops the pending bytes: the batch committed (the store told the
// gate what it added) or was dropped.
func (p *Pending) Settle() {
	if p == nil || p.g == nil || p.bytes == 0 {
		return
	}
	g := p.g
	g.mu.Lock()
	g.pendingInstance -= p.bytes
	if p.group != nil {
		if g.pendingGroups[*p.group] -= p.bytes; g.pendingGroups[*p.group] <= 0 {
			delete(g.pendingGroups, *p.group)
		}
	}
	g.mu.Unlock()
	p.bytes = 0
}

func parseOr(name string, log *slog.Logger) State {
	s, err := ParseState(name)
	if err != nil {
		log.Error("storage gate", "err", err)
	}
	return s
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func scopeName(group *int64) any {
	if group == nil {
		return "instance"
	}
	return *group
}
