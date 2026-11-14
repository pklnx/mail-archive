package auth

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keepia/archive/internal/testwait"
)

// spinUntil returns once cond holds: Waiting is a state that nothing
// signals, so it checks again right away, as waitForRow does in the store
// tests, bounded by testwait.Limit.
func spinUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testwait.Limit(t))
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
	}
}

type beginResult struct {
	a    *Attempt
	wait time.Duration
	err  error
}

// beginAll starts Begin for each (user, addr) in its own goroutine and
// returns the channel of their results.
func beginAll(ctx context.Context, l *Limiter, users, addrs []string) <-chan beginResult {
	out := make(chan beginResult, len(users))
	for i := range users {
		go func() {
			a, wait, err := l.Begin(ctx, users[i], addrs[i])
			out <- beginResult{a, wait, err}
		}()
	}
	return out
}

// admitted collects results until n attempts are admitted and the others
// wait in Begin, then returns the admitted ones.
func admitted(t *testing.T, l *Limiter, results <-chan beginResult, n, waiting int) []*Attempt {
	t.Helper()
	var got []*Attempt
	spinUntil(t, fmt.Sprintf("%d admitted, %d waiting", n, waiting), func() bool {
		for {
			select {
			case r := <-results:
				if r.a == nil {
					t.Fatalf("refused while deciding: %v %v", r.wait, r.err)
				}
				got = append(got, r.a)
				continue
			default:
			}
			if len(got) > n {
				t.Fatalf("%d attempts admitted, want %d", len(got), n)
			}
			return len(got) == n && l.Waiting() == waiting
		}
	})
	return got
}

// fixedLimiter is a Limiter whose clock stands still.
func fixedLimiter() *Limiter {
	l := NewLimiter()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	return l
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func numbered(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return out
}

// Parallel attempts for one name are decided as if they came one after
// another: only as many run as failures are left before the block, and the
// rest get the block once those failed.
func TestLimiterParallelName(t *testing.T) {
	const n = 50
	l := fixedLimiter()
	results := beginAll(context.Background(), l, repeat("alice", n), numbered("10.0.0.", n))
	run := admitted(t, l, results, userFailures, n-userFailures)
	for _, a := range run {
		a.Fail()
	}
	for range n - userFailures {
		r := <-results
		if r.a != nil || r.err != nil || r.wait != userBlockMin {
			t.Fatalf("waiter after the block: %+v", r)
		}
	}
}

func TestLimiterParallelAddress(t *testing.T) {
	const n = 50
	l := fixedLimiter()
	results := beginAll(context.Background(), l, numbered("user", n), repeat("10.0.0.1", n))
	run := admitted(t, l, results, addrFailures, n-addrFailures)
	for _, a := range run {
		a.Fail()
	}
	for range n - addrFailures {
		r := <-results
		if r.a != nil || r.err != nil || r.wait != addrBlockTime {
			t.Fatalf("waiter after the block: %+v", r)
		}
	}
}

// After a block, every failure blocks again until the name was quiet for a
// whole window after the block: one attempt at a time, also once the
// failures that led to the block are older than the window.
func TestLimiterOneAtATimeAfterBlock(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	for range userFailures {
		fail(t, l, "alice", "10.0.0.1")
	}
	now = now.Add(failureWindow)
	results := beginAll(context.Background(), l, repeat("alice", 3), numbered("10.0.1.", 3))
	first := admitted(t, l, results, 1, 2)
	first[0].Release()
	second := admitted(t, l, results, 1, 1)
	second[0].Fail()
	if r := <-results; r.a != nil || r.wait != 2*userBlockMin {
		t.Fatalf("after the second block: %+v", r)
	}
}

// A success frees the name's budget for the waiters; a success while
// another attempt is in flight keeps that attempt's failure.
func TestLimiterSucceedWhileInFlight(t *testing.T) {
	ctx := context.Background()
	l := NewLimiter()
	for range userFailures - 1 {
		fail(t, l, "alice", "10.0.0.1")
	}
	results := beginAll(ctx, l, repeat("alice", 3), numbered("10.0.1.", 3))
	first := admitted(t, l, results, 1, 2)
	first[0].Succeed()
	rest := admitted(t, l, results, 2, 0)
	rest[0].Succeed()
	rest[1].Fail()
	l.mu.Lock()
	u := l.users["alice"]
	if u == nil || len(u.times) != 1 || u.pending != 0 || u.blocks != 0 {
		l.mu.Unlock()
		t.Fatalf("after a success and a failure in flight: %+v", u)
	}
	l.mu.Unlock()
}

// Release frees the budget without counting; a released attempt without
// failures leaves no entry; Fail, Succeed and Release act once.
func TestLimiterRelease(t *testing.T) {
	ctx := context.Background()
	l := NewLimiter()
	results := beginAll(ctx, l, repeat("alice", userFailures+1), numbered("10.0.0.", userFailures+1))
	run := admitted(t, l, results, userFailures, 1)
	run[0].Release()
	last := admitted(t, l, results, 1, 0)
	for _, a := range append(run[1:], last...) {
		a.Release()
		a.Fail()
		a.Succeed()
	}
	var nilAttempt *Attempt
	nilAttempt.Fail()
	nilAttempt.Succeed()
	nilAttempt.Release()
	if len(l.users) != 0 || len(l.addrs) != 0 {
		t.Fatalf("entries left: %d users, %d addrs", len(l.users), len(l.addrs))
	}
}

// The sweep never removes an entry with an attempt in flight, so the
// attempt's failure still counts.
func TestLimiterSweepKeepsPending(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	a, _, _ := l.Begin(context.Background(), "alice", "10.0.0.1")
	now = now.Add(time.Hour)
	l.added = sweepEvery - 1
	forceFail(l, "bob", "10.0.0.2")
	if l.added != 0 {
		t.Fatal("no sweep")
	}
	if l.users["alice"] == nil || l.addrs["10.0.0.1"] == nil {
		t.Fatal("the sweep removed an entry in flight")
	}
	a.Fail()
	if u := l.users["alice"]; len(u.times) != 1 || u.pending != 0 {
		t.Fatalf("failure lost: %+v", u)
	}
}

// Names that fail ValidateUserName share one counter, and a refused Begin
// creates no entry: the memory stays bounded for any name.
func TestLimiterInvalidNamesBounded(t *testing.T) {
	ctx := context.Background()
	l := NewLimiter()
	for i := range 10_000 {
		name := fmt.Sprintf("%d %s", i, strings.Repeat("x", 1024))
		a, wait, err := l.Begin(ctx, name, fmt.Sprintf("10.%d.%d.%d", i>>16, (i>>8)&255, i&255))
		if err != nil {
			t.Fatal(err)
		}
		if i < userFailures && a == nil {
			t.Fatalf("attempt %d refused: %v", i, wait)
		}
		if i >= userFailures && (a != nil || wait == 0) {
			t.Fatalf("attempt %d admitted after the shared counter blocked", i)
		}
		a.Fail()
	}
	if len(l.users) != 1 || l.users[invalidUserKey] == nil {
		t.Fatalf("%d name entries, want the shared one", len(l.users))
	}
	if len(l.addrs) != userFailures {
		t.Fatalf("%d address entries, want %d: a refused Begin added one", len(l.addrs), userFailures)
	}
	// A valid name has its own counter.
	if blocked(t, l, "alice", "10.0.0.1") != 0 {
		t.Fatal("a valid name shares the invalid names' block")
	}
}

// A waiter whose context ends returns without a reservation; a waiter that
// waits MaxWait gets ErrBusy.
func TestLimiterWaitEnds(t *testing.T) {
	l := NewLimiter()
	expire := make(chan time.Time)
	l.timeout = func(d time.Duration) (<-chan time.Time, func()) {
		if d != MaxWait {
			t.Errorf("wait bound %v", d)
		}
		return expire, func() {}
	}
	results := beginAll(context.Background(), l, repeat("alice", userFailures), numbered("10.0.0.", userFailures))
	run := admitted(t, l, results, userFailures, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := beginAll(ctx, l, []string{"alice"}, []string{"10.0.1.1"})
	spinUntil(t, "a waiter", func() bool { return l.Waiting() == 1 })
	cancel()
	if r := <-cancelled; r.a != nil || !errors.Is(r.err, context.Canceled) {
		t.Fatalf("cancelled waiter: %+v", r)
	}

	busy := beginAll(context.Background(), l, []string{"alice"}, []string{"10.0.1.2"})
	spinUntil(t, "a waiter", func() bool { return l.Waiting() == 1 })
	close(expire)
	if r := <-busy; r.a != nil || !errors.Is(r.err, ErrBusy) {
		t.Fatalf("waiter past MaxWait: %+v", r)
	}
	if u := l.users["alice"]; u.pending != userFailures {
		t.Fatalf("pending %d, want %d", u.pending, userFailures)
	}
	for _, a := range run {
		a.Release()
	}
	if len(l.users) != 0 {
		t.Fatal("entries left")
	}
}

// Under any mix of outcomes and repeated calls, no count goes below zero
// and nothing stays in flight.
func TestLimiterPendingNeverNegative(t *testing.T) {
	l := NewLimiter()
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			a, _, err := l.Begin(context.Background(), []string{"alice", "bob", "x y"}[i%3], fmt.Sprintf("10.0.0.%d", i%4))
			if err != nil {
				t.Error(err)
				return
			}
			defer a.Release()
			switch rand.IntN(3) {
			case 0:
				a.Fail()
			case 1:
				a.Succeed()
			}
			a.Release()
			a.Fail()
		})
	}
	wg.Wait()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range []map[string]*failures{l.users, l.addrs} {
		for k, f := range m {
			if f.pending != 0 {
				t.Errorf("%q: pending %d", k, f.pending)
			}
		}
	}
	if l.waiting != 0 {
		t.Errorf("waiting %d", l.waiting)
	}
}

// LimitKey groups IPv6 per /64 and keeps IPv4 (also IPv4-mapped) per
// address, without a zone.
func TestLimitKey(t *testing.T) {
	for _, c := range []struct{ addr, want string }{
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"192.0.2.1", "192.0.2.1"},
		{"::ffff:192.0.2.1", "192.0.2.1"},
	} {
		if got := LimitKey(netip.MustParseAddr(c.addr)); got != c.want {
			t.Errorf("%s: %s, want %s", c.addr, got, c.want)
		}
	}
}

// WithClock moves the limiter's window with the given clock: a block ends
// when the clock passes it.
func TestLimiterWithClock(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	l := NewLimiter().WithClock(func() time.Time { return now })
	for range addrFailures {
		a, wait, err := l.BeginAddr(context.Background(), "192.0.2.1")
		if a == nil || wait != 0 || err != nil {
			t.Fatalf("begin: %v %v %v", a, wait, err)
		}
		a.Fail()
	}
	if wait := l.AddrBlocked("192.0.2.1"); wait != addrBlockTime {
		t.Fatalf("blocked for %v, want %v", wait, addrBlockTime)
	}
	now = now.Add(addrBlockTime)
	if wait := l.AddrBlocked("192.0.2.1"); wait != 0 {
		t.Fatalf("still blocked for %v", wait)
	}
}
