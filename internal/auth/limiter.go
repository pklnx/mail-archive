package auth

import (
	"sync"
	"time"
)

// Limits for failed logins.
const (
	failureWindow = 15 * time.Minute

	// Per user name: from the 5th failure within the window, the name is
	// blocked; the block starts at 1 minute and doubles up to 15 minutes.
	userFailures  = 5
	userBlockMin  = time.Minute
	userBlockMax  = 15 * time.Minute
	addrFailures  = 20 // per client address within the window
	addrBlockTime = 15 * time.Minute

	// sweepEvery is how many recorded failures trigger removing stale
	// entries, which keeps memory bounded.
	sweepEvery = 1000
)

type failures struct {
	times        []time.Time // within failureWindow
	blockedUntil time.Time
	blocks       int
}

func (f *failures) prune(now time.Time) {
	i := 0
	for i < len(f.times) && now.Sub(f.times[i]) >= failureWindow {
		i++
	}
	f.times = f.times[i:]
	if f.quiet(now) {
		f.blocks = 0
	}
}

// quiet reports whether there was no failure and no block for a whole
// window.
func (f *failures) quiet(now time.Time) bool {
	return len(f.times) == 0 && now.Sub(f.blockedUntil) >= failureWindow
}

func (f *failures) stale(now time.Time) bool {
	f.prune(now)
	return f.quiet(now)
}

// Limiter slows down password guessing per user name and per client
// address. It keeps its counters in memory; a restart resets them.
type Limiter struct {
	mu    sync.Mutex
	users map[string]*failures
	addrs map[string]*failures
	added int
	now   func() time.Time
}

// NewLimiter creates an empty Limiter.
func NewLimiter() *Limiter {
	return &Limiter{users: map[string]*failures{}, addrs: map[string]*failures{}, now: time.Now}
}

// Blocked returns how long logins for user from addr must wait; zero means
// the attempt may proceed.
func (l *Limiter) Blocked(user, addr string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	for _, f := range []*failures{l.users[user], l.addrs[addr]} {
		if f != nil && f.blockedUntil.After(now) {
			wait = max(wait, f.blockedUntil.Sub(now))
		}
	}
	return wait
}

// Fail records a failed login.
func (l *Limiter) Fail(user, addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	u := entry(l.users, user, now)
	// Once blocked, every further failure blocks again until the name has
	// been quiet for a whole window after its last block.
	if len(u.times) >= userFailures || u.blocks > 0 {
		u.blockedUntil = now.Add(min(userBlockMin<<u.blocks, userBlockMax))
		u.blocks = min(u.blocks+1, 8)
	}
	a := entry(l.addrs, addr, now)
	if len(a.times) >= addrFailures {
		a.blockedUntil = now.Add(addrBlockTime)
	}

	if l.added++; l.added >= sweepEvery {
		l.added = 0
		for _, m := range []map[string]*failures{l.users, l.addrs} {
			for k, f := range m {
				if f.stale(now) {
					delete(m, k)
				}
			}
		}
	}
}

func entry(m map[string]*failures, key string, now time.Time) *failures {
	f := m[key]
	if f == nil {
		f = &failures{}
		m[key] = f
	}
	f.prune(now)
	f.times = append(f.times, now)
	return f
}

// Succeed forgets the failures of a user name after a successful login.
// The address counter stays, so that one valid account does not reset a
// guessing run against others.
func (l *Limiter) Succeed(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.users, user)
}
