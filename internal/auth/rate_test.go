package auth

import (
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

func TestRateBlocked(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	r := NewRate(2, time.Minute)
	r.now = func() time.Time { return now }
	if r.Blocked("a") != 0 {
		t.Fatal("blocked without events")
	}
	r.Allow("a")
	if r.Blocked("a") != 0 {
		t.Fatal("blocked below the limit")
	}
	r.Allow("a")
	now = now.Add(20 * time.Second)
	if wait := r.Blocked("a"); wait != 40*time.Second {
		t.Fatalf("wait %v", wait)
	}
	if wait := r.Blocked("a"); wait != 40*time.Second {
		t.Fatal("Blocked counted an event")
	}
	now = now.Add(40 * time.Second)
	if r.Blocked("a") != 0 {
		t.Fatal("blocked in the next window")
	}
}

func TestLimiterAddressOnly(t *testing.T) {
	l := NewLimiter()
	for range addrFailures {
		if l.BlockedAddr("1.2.3.4") > 0 {
			t.Fatal("blocked too early")
		}
		l.FailAddr("1.2.3.4")
	}
	if l.BlockedAddr("1.2.3.4") == 0 || l.Blocked("alice", "1.2.3.4") == 0 {
		t.Fatal("address not blocked")
	}
	if l.Blocked("alice", "5.6.7.8") > 0 {
		t.Fatal("a name was blocked by address-only failures")
	}
}
