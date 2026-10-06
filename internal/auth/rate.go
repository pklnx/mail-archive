package auth

import (
	"sync"
	"time"
)

// Rate allows a number of events per key in a fixed time window, for
// requests that cost the server something even when they are valid. It
// keeps its counters in memory; a restart resets them.
type Rate struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	counts map[string]*rateWindow
	now    func() time.Time
}

type rateWindow struct {
	start time.Time
	n     int
}

// NewRate allows limit events per key and window.
func NewRate(limit int, window time.Duration) *Rate {
	return &Rate{limit: limit, window: window, counts: map[string]*rateWindow{}, now: time.Now}
}

// Allow counts an event for key. If the limit is reached, it returns false
// and how long until the window ends.
func (r *Rate) Allow(key string) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w := r.counts[key]
	if w == nil || now.Sub(w.start) >= r.window {
		if len(r.counts) >= sweepEvery {
			for k, old := range r.counts {
				if now.Sub(old.start) >= r.window {
					delete(r.counts, k)
				}
			}
		}
		w = &rateWindow{start: now}
		r.counts[key] = w
	}
	if w.n >= r.limit {
		return false, w.start.Add(r.window).Sub(now)
	}
	w.n++
	return true, 0
}
