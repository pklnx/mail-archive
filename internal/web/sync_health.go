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

// handleSyncHealth answers 200 when every enabled IMAP account syncs, and
// 503 with the IDs of failing and stale accounts otherwise. It needs no
// login, so it names no accounts, users or mail.
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
	now := s.currentTime()
	var busy busyState
	if err == nil {
		if busy, err = s.loadBusy(r.Context()); err != nil {
			s.log.Error("sync health", "err", err)
			out, status = syncHealthJSON{Status: "unavailable"}, http.StatusServiceUnavailable
			health = nil
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
// runs or waits makes no account stale (see store.SyncHealth.State).
type busyState struct {
	running map[int64]bool // the sync lock is held, also by the CLI
	queued  map[int64]bool // waiting in this server's queue
	runs    map[int64]store.LastRun
}

func (s *Server) loadBusy(ctx context.Context) (busyState, error) {
	b := busyState{}
	var err error
	if b.running, err = s.store.SyncingAccounts(ctx); err != nil {
		return b, err
	}
	if b.runs, err = s.store.LastRuns(ctx); err != nil {
		return b, err
	}
	if s.runner != nil {
		b.queued = s.runner.Queued()
	}
	return b, nil
}

// since is when the account became busy: the start of its running sync,
// or now while it waits; nil when it is idle.
func (b busyState) since(id int64, now time.Time) *time.Time {
	switch {
	case b.running[id]:
		if run, ok := b.runs[id]; ok && run.FinishedAt == nil {
			return &run.StartedAt
		}
		return &now // the run's row is not written yet
	case b.queued[id]:
		return &now
	}
	return nil
}

// healthState is the account's health on this server's schedule.
func (s *Server) healthState(h *store.SyncHealth, busy busyState, now time.Time) string {
	return h.State(s.alertAfter, s.schedule().Every(h.SyncInterval), busy.since(h.AccountID, now), now)
}
