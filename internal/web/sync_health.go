package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

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
	for id, h := range health {
		switch h.State(s.alertAfter, s.syncInterval(), now) {
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

// syncInterval is the schedule's interval, zero when it is off or the
// server cannot sync.
func (s *Server) syncInterval() time.Duration {
	if s.runner == nil {
		return 0
	}
	return s.runner.Interval
}
