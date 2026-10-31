package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/store"
)

type jobsHealthCache struct {
	mu     sync.Mutex
	at     time.Time
	status int
	body   []byte
}

type jobsHealthJSON struct {
	Status string   `json:"status"`
	Stale  []string `json:"stale,omitempty"`
}

// handleJobsHealth answers 200 while the background jobs of new mail get
// done, and 503 with the kinds whose work for new mail has been due for
// more than jobs.StaleAfter (no worker runs, or it hangs). Backfills and
// failed jobs do not count. It needs no login, so it gives no counts and
// names no messages, accounts or users; the answer is cached for
// syncHealthTTL like /healthz/sync.
func (s *Server) handleJobsHealth(w http.ResponseWriter, r *http.Request) {
	now := s.currentTime()
	c := &s.jobsHealthCache
	c.mu.Lock()
	if c.body == nil || now.Sub(c.at) >= syncHealthTTL || now.Before(c.at) {
		c.status, c.body = s.jobsHealthAnswer(r)
		c.at = now
	}
	status, body := c.status, c.body
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) jobsHealthAnswer(r *http.Request) (int, []byte) {
	out, status := jobsHealthJSON{Status: "ok"}, http.StatusOK
	stats, err := s.store.JobStats(r.Context())
	if err != nil {
		s.log.Error("jobs health", "err", err)
		out, status = jobsHealthJSON{Status: "unavailable"}, http.StatusServiceUnavailable
	}
	for _, st := range stats {
		if st.OldestNew > jobs.StaleAfter {
			out.Stale = append(out.Stale, st.Kind)
		}
	}
	if len(out.Stale) > 0 {
		out.Status, status = "degraded", http.StatusServiceUnavailable
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(out)
	return status, buf.Bytes()
}

// statusJob is a kind's queue in GET /api/status, for admins.
type statusJob struct {
	Kind             string  `json:"kind"`
	Queued           int64   `json:"queued"`
	Retrying         int64   `json:"retrying"`
	Running          int64   `json:"running"`
	Failed           int64   `json:"failed"`
	OldestDueSeconds float64 `json:"oldestDueSeconds"`
	OldestNewSeconds float64 `json:"oldestNewSeconds"`
}

func toStatusJobs(stats []store.JobStat) []statusJob {
	out := make([]statusJob, 0, len(stats))
	for _, st := range stats {
		out = append(out, statusJob{
			Kind: st.Kind, Queued: st.Queued, Retrying: st.Retrying, Running: st.Running, Failed: st.Failed,
			OldestDueSeconds: st.OldestDue.Round(time.Second).Seconds(), OldestNewSeconds: st.OldestNew.Round(time.Second).Seconds(),
		})
	}
	return out
}
