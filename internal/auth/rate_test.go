package auth

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := NewRate(3, time.Minute)
	r.now = func() time.Time { return now }
	for i := range 3 {
		if ok, _ := r.Allow("a"); !ok {
			t.Fatalf("event %d refused", i+1)
		}
	}
	ok, wait := r.Allow("a")
	if ok || wait != time.Minute {
		t.Fatalf("4th event: %v %v", ok, wait)
	}
	if ok, _ := r.Allow("b"); !ok {
		t.Fatal("other key refused")
	}
	now = now.Add(time.Minute)
	if ok, _ := r.Allow("a"); !ok {
		t.Fatal("refused in the next window")
	}
}

// Sequential Begin counts only failures: Release leaves the window as it
// is, and the limit refuses with the time until the window ends.
func TestRateBegin(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_000_000, 0)
	r := NewRate(2, time.Minute)
	r.now = func() time.Time { return now }
	begin := func() (*RateAttempt, time.Duration) {
		t.Helper()
		a, wait, err := r.Begin(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		return a, wait
	}
	for range 5 {
		a, wait := begin()
		if a == nil || wait != 0 {
			t.Fatalf("refused without failures: %v", wait)
		}
		a.Release()
	}
	a, _ := begin()
	a.Fail()
	a, wait := begin()
	if a == nil || wait != 0 {
		t.Fatalf("refused below the limit: %v", wait)
	}
	a.Fail()
	now = now.Add(20 * time.Second)
	if a, wait := begin(); a != nil || wait != 40*time.Second {
		t.Fatalf("at the limit: %v %v", a, wait)
	}
	if a, wait := begin(); a != nil || wait != 40*time.Second {
		t.Fatal("a refused Begin counted")
	}
	now = now.Add(40 * time.Second)
	if a, wait := begin(); a == nil || wait != 0 {
		t.Fatal("refused in the next window")
	}
}

func TestLimiterAddressOnly(t *testing.T) {
	ctx := context.Background()
	l := NewLimiter()
	for range addrFailures {
		if l.AddrBlocked("1.2.3.4") > 0 {
			t.Fatal("blocked too early")
		}
		a, wait, err := l.BeginAddr(ctx, "1.2.3.4")
		if a == nil {
			t.Fatalf("refused: %v %v", wait, err)
		}
		a.Fail()
	}
	if _, wait, _ := l.BeginAddr(ctx, "1.2.3.4"); wait == 0 {
		t.Fatal("address not blocked for BeginAddr")
	}
	if l.AddrBlocked("1.2.3.4") == 0 || blocked(t, l, "alice", "1.2.3.4") == 0 {
		t.Fatal("address not blocked")
	}
	if blocked(t, l, "alice", "5.6.7.8") > 0 {
		t.Fatal("a name was blocked by address-only failures")
	}
}

type rateResult struct {
	a    *RateAttempt
	wait time.Duration
	err  error
}

// rateAdmitted starts n Begins for key and collects results until want
// are admitted and the others wait.
func rateAdmitted(t *testing.T, r *Rate, results <-chan rateResult, want, waiting int) []*RateAttempt {
	t.Helper()
	var got []*RateAttempt
	spinUntil(t, fmt.Sprintf("%d admitted, %d waiting", want, waiting), func() bool {
		for {
			select {
			case res := <-results:
				if res.a == nil {
					t.Fatalf("refused while deciding: %+v", res)
				}
				got = append(got, res.a)
				continue
			default:
			}
			if len(got) > want {
				t.Fatalf("%d admitted, want %d", len(got), want)
			}
			return len(got) == want && r.Waiting() == waiting
		}
	})
	return got
}

func rateBeginAll(r *Rate, key string, n int) <-chan rateResult {
	out := make(chan rateResult, n)
	for range n {
		go func() {
			a, wait, err := r.Begin(context.Background(), key)
			out <- rateResult{a, wait, err}
		}()
	}
	return out
}

// Parallel attempts on one key: only as many run as failures are left,
// the rest get the limit once those failed.
func TestRateParallel(t *testing.T) {
	const n, limit = 100, 30
	r := NewRate(limit, time.Minute)
	results := rateBeginAll(r, "a", n)
	for _, a := range rateAdmitted(t, r, results, limit, n-limit) {
		a.Fail()
	}
	for range n - limit {
		if res := <-results; res.a != nil || res.err != nil || res.wait <= 0 {
			t.Fatalf("waiter after the limit: %+v", res)
		}
	}
}

// Attempts that are released, like valid API keys, never use up the
// budget: afterwards the full limit of failures is still admitted.
func TestRateReleaseKeepsBudget(t *testing.T) {
	const n, limit = 100, 30
	r := NewRate(limit, time.Minute)
	results := rateBeginAll(r, "a", n)
	left := n
	for left > 0 {
		batch := rateAdmitted(t, r, results, min(limit, left), max(0, left-limit))
		for _, a := range batch {
			a.Release()
		}
		left -= len(batch)
	}
	if len(r.counts) != 0 {
		t.Fatalf("entries left after releases: %d", len(r.counts))
	}
	for i := range limit {
		a, wait, err := r.Begin(context.Background(), "a")
		if a == nil {
			t.Fatalf("failure %d refused: %v %v", i+1, wait, err)
		}
		a.Fail()
	}
	if a, wait, _ := r.Begin(context.Background(), "a"); a != nil || wait == 0 {
		t.Fatal("not limited after the limit of failures")
	}
}

// A new window keeps the attempts in flight: their Release does not drive
// pending below zero or change the new window's count, and their Fail
// counts in the new window.
func TestRateWindowRollover(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_000_000, 0)
	r := NewRate(3, time.Minute)
	r.now = func() time.Time { return now }
	for range 2 {
		a, _, _ := r.Begin(ctx, "a")
		a.Fail()
	}
	inFlight, _, _ := r.Begin(ctx, "a")
	now = now.Add(time.Minute)
	second, wait, _ := r.Begin(ctx, "a")
	if second == nil {
		t.Fatalf("refused in the new window: %v", wait)
	}
	w := r.counts["a"]
	if w.n != 0 || w.pending != 2 {
		t.Fatalf("new window: n %d, pending %d", w.n, w.pending)
	}
	inFlight.Release()
	inFlight.Release()
	if w.n != 0 || w.pending != 1 {
		t.Fatalf("after Release: n %d, pending %d", w.n, w.pending)
	}
	second.Fail()
	if w.n != 1 || w.pending != 0 || r.counts["a"] != w {
		t.Fatalf("after Fail: n %d, pending %d", w.n, w.pending)
	}

	// The sweep keeps entries in flight.
	held, _, _ := r.Begin(ctx, "held")
	now = now.Add(time.Hour)
	for i := range sweepEvery {
		r.Allow(fmt.Sprintf("k%d", i))
	}
	if r.counts["held"] == nil {
		t.Fatal("the sweep removed an entry in flight")
	}
	held.Fail()
	if r.counts["held"].n != 1 {
		t.Fatal("failure lost")
	}
}
