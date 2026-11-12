package auth

import (
	"context"
	"errors"
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

	// invalidUserKey is the one counter of all names that fail
	// ValidateUserName. No valid name can equal it, and such names can never
	// log in, so sharing a counter only throttles garbage while keeping the
	// map's keys bounded.
	invalidUserKey = "\x00invalid"
)

// MaxWait is how long Begin waits at most for attempts in flight. After
// that it gives up with ErrBusy.
const MaxWait = 60 * time.Second

// ErrBusy means that an attempt waited MaxWait for the attempts in flight
// ahead of it without being decided.
var ErrBusy = errors.New("auth: waited too long for attempts in flight")

type failures struct {
	times        []time.Time // within failureWindow
	blockedUntil time.Time
	blocks       int
	// pending counts the attempts in flight: begun and not yet ended by
	// Fail, Succeed or Release. An entry with pending > 0 is never removed
	// from its map, so an Attempt's pointer stays the live entry.
	pending int
	// changed is closed when pending goes down or the entry is removed;
	// Begin waits on it. Nil until a waiter needs it.
	changed chan struct{}
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

// stale reports whether the entry can be removed: quiet and no attempt in
// flight.
func (f *failures) stale(now time.Time) bool {
	f.prune(now)
	return f.pending == 0 && f.quiet(now)
}

// fits reports whether one more attempt can start without the attempts in
// flight being able to push the entry over limit: decided one after
// another, every one of them could fail. After a block every failure blocks
// again, so only one attempt at a time. With nothing in flight, an attempt
// that is not blocked always fits, as before attempts were counted.
func (f *failures) fits(limit int) bool {
	if f == nil || f.pending == 0 {
		return true
	}
	return f.blocks == 0 && len(f.times)+f.pending < limit
}

// wait returns the channel that is closed at the entry's next change.
func (f *failures) wait() <-chan struct{} {
	if f.changed == nil {
		f.changed = make(chan struct{})
	}
	return f.changed
}

// wake wakes the attempts waiting on the entry.
func (f *failures) wake() {
	if f.changed != nil {
		close(f.changed)
		f.changed = nil
	}
}

// Limiter slows down password guessing per user name and per client
// address. It keeps its counters in memory; a restart resets them.
//
// An attempt is reserved with Begin before the expensive work and ends with
// Fail, Succeed or Release. Attempts in flight hold their place in the
// limits, so parallel attempts are decided as if they came one after
// another.
type Limiter struct {
	mu      sync.Mutex
	users   map[string]*failures
	addrs   map[string]*failures
	added   int
	waiting int
	now     func() time.Time
	// timeout starts the bound of one Begin's wait; tests replace it.
	timeout func(time.Duration) (<-chan time.Time, func())
}

// NewLimiter creates an empty Limiter.
func NewLimiter() *Limiter {
	return &Limiter{users: map[string]*failures{}, addrs: map[string]*failures{}, now: time.Now, timeout: realTimeout}
}

func realTimeout(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// userKey is the counter key of a normalized user name.
func userKey(user string) string {
	if ValidateUserName(user) != nil {
		return invalidUserKey
	}
	return user
}

// Attempt is a login attempt reserved by Begin or BeginAddr. It ends with
// exactly one of Fail, Succeed or Release; later calls do nothing, and so
// do calls on a nil Attempt. Callers end it before they answer the request
// and defer Release as a safety net.
type Attempt struct {
	l        *Limiter
	user     *failures // nil for BeginAddr
	userKey  string
	addr     *failures
	addrKey  string
	finished bool
}

// Begin reserves a login attempt for user (a normalized name) from addr.
//
// If the name or the address is blocked, it returns no attempt and how long
// the block lasts. If the attempts in flight for the name or address could
// still use up its budget, it waits until one of them ends and decides
// again; it waits at most MaxWait and then returns ErrBusy. If ctx ends
// first, it returns ctx's error. Neither error reserves anything.
//
// A request must never call Begin while it holds an Attempt for the same
// name or address: with one attempt left, it would wait for itself.
func (l *Limiter) Begin(ctx context.Context, user, addr string) (*Attempt, time.Duration, error) {
	return l.begin(ctx, userKey(user), true, addr)
}

// BeginAddr is Begin for a login without a user name, like a passkey: only
// the address counts.
func (l *Limiter) BeginAddr(ctx context.Context, addr string) (*Attempt, time.Duration, error) {
	return l.begin(ctx, "", false, addr)
}

func (l *Limiter) begin(ctx context.Context, ukey string, withUser bool, addr string) (*Attempt, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var expire <-chan time.Time
	stop := func() {}
	defer func() { stop() }()
	for {
		now := l.now()
		var u *failures
		if withUser {
			u = l.users[ukey]
		}
		a := l.addrs[addr]
		var wait time.Duration
		for _, f := range []*failures{u, a} {
			if f == nil {
				continue
			}
			f.prune(now)
			if f.blockedUntil.After(now) {
				wait = max(wait, f.blockedUntil.Sub(now))
			}
		}
		if wait > 0 {
			return nil, wait, nil
		}
		var changed <-chan struct{}
		switch {
		case !u.fits(userFailures):
			changed = u.wait()
		case !a.fits(addrFailures):
			changed = a.wait()
		default:
			at := &Attempt{l: l, addrKey: addr, addr: reserve(l.addrs, addr)}
			if withUser {
				at.userKey, at.user = ukey, reserve(l.users, ukey)
			}
			return at, 0, nil
		}
		if expire == nil {
			expire, stop = l.timeout(MaxWait)
		}
		if err := waitUnlocked(ctx, &l.mu, &l.waiting, changed, expire); err != nil {
			return nil, 0, err
		}
	}
}

// waitUnlocked waits, with mu released, until changed is closed, expire
// delivers or ctx ends. It counts the waiter in *waiting. mu is held on
// entry and on return.
func waitUnlocked(ctx context.Context, mu *sync.Mutex, waiting *int, changed <-chan struct{}, expire <-chan time.Time) error {
	*waiting++
	mu.Unlock()
	var err error
	select {
	case <-changed:
	case <-expire:
		err = ErrBusy
	case <-ctx.Done():
		err = ctx.Err()
	}
	mu.Lock()
	*waiting--
	return err
}

// reserve returns the entry for key, created if needed, with one more
// attempt in flight.
func reserve(m map[string]*failures, key string) *failures {
	f := m[key]
	if f == nil {
		f = &failures{}
		m[key] = f
	}
	f.pending++
	return f
}

// Waiting returns how many calls of Begin and BeginAddr wait for attempts
// in flight. Tests use it to wait for that state.
func (l *Limiter) Waiting() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waiting
}

// AddrBlocked returns how long logins from addr must wait, without
// reserving anything; zero means none. It is only for a step that records
// no failure itself, like the start of a passkey login: it must never be
// paired with recording a failure, which is what Begin is for.
func (l *Limiter) AddrBlocked(addr string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f, now := l.addrs[addr], l.now(); f != nil && f.blockedUntil.After(now) {
		return f.blockedUntil.Sub(now)
	}
	return 0
}

// end ends the attempt's reservation; it reports false if it already ended.
// mu is held.
func (a *Attempt) end() bool {
	if a == nil || a.finished {
		return false
	}
	a.finished = true
	for _, f := range []*failures{a.user, a.addr} {
		if f != nil {
			f.pending--
			f.wake()
		}
	}
	return true
}

// Fail records a failed login for the attempt's name and address.
func (a *Attempt) Fail() {
	if a == nil {
		return
	}
	l := a.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if !a.end() {
		return
	}
	now := l.now()
	if u := a.user; u != nil {
		record(u, now)
		// Once blocked, every further failure blocks again until the name
		// has been quiet for a whole window after its last block.
		if len(u.times) >= userFailures || u.blocks > 0 {
			u.blockedUntil = now.Add(min(userBlockMin<<u.blocks, userBlockMax))
			u.blocks = min(u.blocks+1, 8)
		}
	}
	record(a.addr, now)
	if len(a.addr.times) >= addrFailures {
		a.addr.blockedUntil = now.Add(addrBlockTime)
	}

	if l.added++; l.added >= sweepEvery {
		l.added = 0
		for _, m := range []map[string]*failures{l.users, l.addrs} {
			for k, f := range m {
				if f.stale(now) {
					f.wake()
					delete(m, k)
				}
			}
		}
	}
}

func record(f *failures, now time.Time) {
	f.prune(now)
	f.times = append(f.times, now)
}

// Succeed ends the attempt after a successful login and forgets the
// failures of its name. The address counter stays, so that one valid
// account does not reset a guessing run against others.
func (a *Attempt) Succeed() {
	if a == nil {
		return
	}
	l := a.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if !a.end() {
		return
	}
	if a.user != nil {
		l.forget(a.userKey)
	}
	l.dropIfStale(l.addrs, a.addrKey, a.addr)
}

// Release ends the attempt without recording anything: the attempt was not
// decided, for example after an internal error.
func (a *Attempt) Release() {
	if a == nil {
		return
	}
	l := a.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if !a.end() {
		return
	}
	if a.user != nil {
		l.dropIfStale(l.users, a.userKey, a.user)
	}
	l.dropIfStale(l.addrs, a.addrKey, a.addr)
}

// dropIfStale removes f from m if it holds nothing worth keeping, so that a
// released attempt leaves no entry behind. mu is held.
func (l *Limiter) dropIfStale(m map[string]*failures, key string, f *failures) {
	if m[key] == f && f.stale(l.now()) {
		f.wake()
		delete(m, key)
	}
}

// Succeed forgets the failures of a user name after a successful login
// that had no Attempt for the name, like a passkey login (BeginAddr).
func (l *Limiter) Succeed(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forget(userKey(user))
}

// forget clears the failures of key. An entry with attempts in flight is
// cleared in place, so that their outcome still lands on it. mu is held.
func (l *Limiter) forget(key string) {
	f := l.users[key]
	if f == nil {
		return
	}
	f.wake()
	if f.pending > 0 {
		f.times, f.blocks, f.blockedUntil = nil, 0, time.Time{}
		return
	}
	delete(l.users, key)
}
