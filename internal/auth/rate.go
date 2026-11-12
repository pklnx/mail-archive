package auth

import (
	"context"
	"sync"
	"time"
)

// Rate allows a number of events per key in a fixed time window, for
// requests that cost the server something even when they are valid. It
// keeps its counters in memory; a restart resets them.
//
// Allow counts every event before the work. Begin is for limits that count
// only failures, like invalid API keys: it reserves an attempt before the
// work, and only Fail counts it.
type Rate struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	counts  map[string]*rateWindow
	waiting int
	now     func() time.Time
	// timeout starts the bound of one Begin's wait; tests replace it.
	timeout func(time.Duration) (<-chan time.Time, func())
}

// rateWindow is the counter of one key. A new window resets start and n in
// place and keeps pending, so attempts in flight stay counted.
type rateWindow struct {
	start   time.Time
	n       int
	pending int // attempts begun and not yet ended by Fail or Release
	// changed is closed when pending goes down or the entry is removed.
	changed chan struct{}
}

func (w *rateWindow) wake() {
	if w.changed != nil {
		close(w.changed)
		w.changed = nil
	}
}

// NewRate allows limit events per key and window.
func NewRate(limit int, window time.Duration) *Rate {
	return &Rate{limit: limit, window: window, counts: map[string]*rateWindow{}, now: time.Now, timeout: realTimeout}
}

// current returns key's counter for the window at now, created if needed.
// mu is held.
func (r *Rate) current(key string, now time.Time) *rateWindow {
	w := r.counts[key]
	if w == nil {
		if len(r.counts) >= sweepEvery {
			for k, old := range r.counts {
				if old.pending == 0 && now.Sub(old.start) >= r.window {
					old.wake()
					delete(r.counts, k)
				}
			}
		}
		w = &rateWindow{start: now}
		r.counts[key] = w
	}
	r.roll(w, now)
	return w
}

// roll starts a new window if w's has ended. mu is held.
func (r *Rate) roll(w *rateWindow, now time.Time) {
	if now.Sub(w.start) >= r.window {
		w.start, w.n = now, 0
	}
}

// Allow counts an event for key. If the limit is reached, it returns false
// and how long until the window ends.
func (r *Rate) Allow(key string) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w := r.current(key, now)
	if w.n >= r.limit {
		return false, w.start.Add(r.window).Sub(now)
	}
	w.n++
	return true, 0
}

// RateAttempt is an attempt reserved by Rate.Begin. It ends with exactly
// one of Fail or Release; later calls do nothing, and so do calls on a nil
// RateAttempt.
type RateAttempt struct {
	r        *Rate
	key      string
	w        *rateWindow
	finished bool
}

// Begin reserves an attempt for key that counts against the limit only if
// it fails. If limit failures were counted in the window, it returns no
// attempt and how long until the window ends. If the attempts in flight
// could still use up the limit, it waits until one of them ends, at most
// MaxWait (then ErrBusy); if ctx ends first, it returns ctx's error.
// Neither error reserves anything.
//
// A request must never call Begin while it holds a RateAttempt for the
// same key: with one attempt left, it would wait for itself.
func (r *Rate) Begin(ctx context.Context, key string) (*RateAttempt, time.Duration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var expire <-chan time.Time
	stop := func() {}
	defer func() { stop() }()
	for {
		now := r.now()
		w := r.counts[key]
		if w != nil {
			r.roll(w, now)
			if w.n >= r.limit {
				return nil, w.start.Add(r.window).Sub(now), nil
			}
		}
		if w == nil || w.pending == 0 || w.n+w.pending < r.limit {
			w = r.current(key, now)
			w.pending++
			return &RateAttempt{r: r, key: key, w: w}, 0, nil
		}
		if w.changed == nil {
			w.changed = make(chan struct{})
		}
		if expire == nil {
			expire, stop = r.timeout(MaxWait)
		}
		if err := waitUnlocked(ctx, &r.mu, &r.waiting, w.changed, expire); err != nil {
			return nil, 0, err
		}
	}
}

// Waiting returns how many calls of Begin wait for attempts in flight.
// Tests use it to wait for that state.
func (r *Rate) Waiting() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waiting
}

// end ends the reservation; it reports false if it already ended. mu is
// held.
func (a *RateAttempt) end() bool {
	if a == nil || a.finished {
		return false
	}
	a.finished = true
	a.w.pending--
	a.w.wake()
	return true
}

// Fail counts the attempt as a failure, in the window current now.
func (a *RateAttempt) Fail() {
	if a == nil {
		return
	}
	r := a.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if !a.end() {
		return
	}
	r.roll(a.w, r.now())
	a.w.n++
}

// Release ends the attempt without counting it.
func (a *RateAttempt) Release() {
	if a == nil {
		return
	}
	r := a.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if !a.end() {
		return
	}
	// A key without failures and attempts keeps no entry.
	if a.w.n == 0 && a.w.pending == 0 && r.counts[a.key] == a.w {
		delete(r.counts, a.key)
	}
}
