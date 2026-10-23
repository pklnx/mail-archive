package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// syncHealthTTL is how long /healthz/sync answers from memory, so callers
// without a login cannot cause a query per request.
const syncHealthTTL = 15 * time.Second

type syncHealthCache struct {
	mu     sync.Mutex
	at     time.Time
	status int
	body   []byte
}

type syncHealthJSON struct {
	Status  string  `json:"status"`
	Failing []int64 `json:"failing,omitempty"`
	Stale   []int64 `json:"stale,omitempty"`
}

// handleSyncHealth answers 200 when every enabled IMAP account syncs and
// every recurring fill runs, and 503 with the IDs of failing and stale
// accounts otherwise; a failing or stale recurring fill lists its smtp
// account. It needs no login, so it names no accounts, users or mail.
func (s *Server) handleSyncHealth(w http.ResponseWriter, r *http.Request) {
	now := s.currentTime()
	c := &s.healthCache
	c.mu.Lock()
	if c.body == nil || now.Sub(c.at) >= syncHealthTTL || now.Before(c.at) {
		c.status, c.body = s.syncHealthAnswer(r)
		c.at = now
	}
	status, body := c.status, c.body
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) syncHealthAnswer(r *http.Request) (int, []byte) {
	out, status := syncHealthJSON{Status: "ok"}, http.StatusOK
	health, err := s.store.SyncHealth(r.Context(), nil)
	if err != nil {
		s.log.Error("sync health", "err", err)
		out, status = syncHealthJSON{Status: "unavailable"}, http.StatusServiceUnavailable
	}
	var fills map[int64]*store.FillHealth
	if err == nil {
		if fills, err = s.store.FillHealth(r.Context(), nil); err != nil {
			s.log.Error("sync health", "err", err)
			out, status = syncHealthJSON{Status: "unavailable"}, http.StatusServiceUnavailable
			health = nil
		}
	}
	now := s.currentTime()
	var busy busyState
	if err == nil {
		if busy, err = s.loadBusy(r.Context()); err != nil {
			s.log.Error("sync health", "err", err)
			out, status = syncHealthJSON{Status: "unavailable"}, http.StatusServiceUnavailable
			health, fills = nil, nil
		}
	}
	for id, h := range health {
		switch s.healthState(h, busy, now) {
		case store.HealthFailing:
			out.Failing = append(out.Failing, id)
		case store.HealthStale:
			out.Stale = append(out.Stale, id)
		}
	}
	for _, h := range fills {
		switch s.fillHealthState(h, busy, now) {
		case store.HealthFailing:
			if !slices.Contains(out.Failing, h.AccountID) {
				out.Failing = append(out.Failing, h.AccountID)
			}
		case store.HealthStale:
			if !slices.Contains(out.Stale, h.AccountID) {
				out.Stale = append(out.Stale, h.AccountID)
			}
		}
	}
	if len(out.Failing)+len(out.Stale) > 0 {
		out.Status, status = "degraded", http.StatusServiceUnavailable
		slices.Sort(out.Failing)
		slices.Sort(out.Stale)
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(out)
	return status, buf.Bytes()
}

// schedule is the web server's sync schedule; the zero Schedule (off) when
// the server cannot sync.
func (s *Server) schedule() archive.Schedule {
	if s.runner == nil {
		return archive.Schedule{}
	}
	return s.runner.Schedule
}

// busyState tells which accounts are syncing or waiting to: a sync that
// runs or waits makes no account stale (see store.SyncHealth.State). So
// does a run of a recurring fill.
type busyState struct {
	running map[int64]bool // the sync lock is held, also by the CLI
	// queued: this server's runners work on the account (Pending) and it
	// does not hold the sync lock: it waits in the queue, for the
	// account's lock or, parked, for the blob lock.
	queued map[int64]bool
	// claimedAt: when this server's runners took the account's sync or
	// fill from the queue, for the claimed ones (also while they run).
	claimedAt map[int64]time.Time
	runs      map[int64]store.LastRun
	// fillsQueued: fills this server's fill runner works on (waiting,
	// claimed or running), by fill ID; fillsClaimedAt: when it took the
	// claimed ones from the queue.
	fillsQueued    map[int64]bool
	fillsClaimedAt map[int64]time.Time
	// holdUntil: until then recurring fills wait after this server's
	// start (archive.RecurringStartHold).
	holdUntil time.Time
}

// loadBusy reads the runners' pending sets first, then the held sync
// locks, then the last runs. A runner claims an account in the same step
// that takes it from the queue and ends the claim only after the sync
// returned (lock released, run finished), so an account read as not
// pending has no sync of this server left to show, and the runs read
// after that already hold its finished run: it never shows idle with the
// previous run while its sync is still to come. The other way round is
// harmless: between the release of the lock and the end of the claim a
// poll sees the finished run as queued, so a sync can show running, then
// queued, then idle.
func (s *Server) loadBusy(ctx context.Context) (busyState, error) {
	b := busyState{claimedAt: map[int64]time.Time{}, fillsClaimedAt: map[int64]time.Time{}}
	pending := map[int64]bool{}
	// claim keeps the earliest claim of an account.
	claim := func(m map[int64]time.Time, id int64, at time.Time) {
		if cur, ok := m[id]; !at.IsZero() && (!ok || at.Before(cur)) {
			m[id] = at
		}
	}
	if s.runner != nil {
		for id, at := range s.runner.Pending() {
			pending[id] = true
			claim(b.claimedAt, id, at)
		}
	}
	if s.fills != nil {
		// A pending fill shows as a sync of its smtp account.
		for id, at := range s.fills.Pending() {
			pending[id] = true
			claim(b.claimedAt, id, at)
		}
		fills := s.fills.PendingFills()
		b.fillsQueued = make(map[int64]bool, len(fills))
		for id, at := range fills {
			b.fillsQueued[id] = true
			claim(b.fillsClaimedAt, id, at)
		}
		b.holdUntil = s.fills.HoldUntil
	}
	var err error
	if b.running, err = s.store.SyncingAccounts(ctx); err != nil {
		return b, err
	}
	if b.runs, err = s.store.LastRuns(ctx); err != nil {
		return b, err
	}
	b.queued = map[int64]bool{}
	for id := range pending {
		if !b.running[id] {
			b.queued[id] = true
		}
	}
	return b, nil
}

// claimedSince is when a runner took the work from the queue, or now while
// it only waits in it. So a claimed sync or fill that waits for the blob
// lock for ever (a hung verify) still turns stale once it has waited longer
// than StaleAfter; the work queued behind it counts from now, since the
// claimed one is reported.
func claimedSince(claimedAt map[int64]time.Time, id int64, now time.Time) *time.Time {
	if at, ok := claimedAt[id]; ok {
		return &at
	}
	return &now
}

// fillSince is when a recurring fill became busy: the start of its run
// while one is in progress (or waits to start, or to be continued); when
// the fill runner took it from the queue while it waits for the locks
// (claimedSince), now while it waits in the queue or behind another run of
// its account (the account's sync lock is held); the server's start during
// the hold after it, when no recurring fill runs; nil otherwise.
func (b busyState) fillSince(h *store.FillHealth, now time.Time) *time.Time {
	switch {
	case h.Running && h.RunStartedAt != nil && !now.Before(b.holdUntil):
		return h.RunStartedAt
	case b.fillsQueued[h.FillID]:
		return claimedSince(b.fillsClaimedAt, h.FillID, now)
	case b.running[h.AccountID]:
		return &now
	case now.Before(b.holdUntil):
		start := b.holdUntil.Add(-archive.RecurringStartHold)
		if h.Running && h.RunStartedAt != nil && h.RunStartedAt.After(start) {
			return h.RunStartedAt
		}
		return &start
	case h.Running:
		return &now
	}
	return nil
}

// fillHealthState is a recurring fill's health on this server's schedule.
func (s *Server) fillHealthState(h *store.FillHealth, busy busyState, now time.Time) string {
	return h.State(s.alertAfter, s.schedule().Every(h.Interval), busy.fillSince(h, now), now)
}

// since is when the account became busy: the start of its running sync;
// before its run's row is written, or while it waits for the locks, when
// this server's runner took it from the queue (claimedSince); now while it
// waits in the queue, or while the CLI holds its lock before writing the
// row; nil when it is idle.
func (b busyState) since(id int64, now time.Time) *time.Time {
	switch {
	case b.running[id]:
		if run, ok := b.runs[id]; ok && run.FinishedAt == nil {
			return &run.StartedAt
		}
		return claimedSince(b.claimedAt, id, now) // the run's row is not written yet
	case b.queued[id]:
		return claimedSince(b.claimedAt, id, now)
	}
	return nil
}

// healthState is the account's health on this server's schedule.
func (s *Server) healthState(h *store.SyncHealth, busy busyState, now time.Time) string {
	return h.State(s.alertAfter, s.schedule().Every(h.SyncInterval), busy.since(h.AccountID, now), now)
}
