package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// cheap keeps the tests fast; the format is the same as with DefaultParams.
var cheap = Params{Memory: 64, Time: 1, Threads: 1}

func TestHashVerify(t *testing.T) {
	ctx := context.Background()
	h := NewHasher(cheap)
	hash, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("hash = %q", hash)
	}
	if ok, rehash, err := h.Verify(ctx, hash, "correct horse battery"); !ok || rehash || err != nil {
		t.Fatalf("right password: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, err := h.Verify(ctx, hash, "wrong horse battery"); ok || err != nil {
		t.Fatalf("wrong password: ok=%v err=%v", ok, err)
	}
	other, _ := h.Hash(ctx, "correct horse battery")
	if other == hash {
		t.Fatal("same salt twice")
	}

	// A hasher with other parameters still verifies and asks for a new hash.
	newer := NewHasher(Params{Memory: 128, Time: 2, Threads: 1})
	if ok, rehash, err := newer.Verify(ctx, hash, "correct horse battery"); !ok || !rehash || err != nil {
		t.Fatalf("old params: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if _, rehash, _ := newer.Verify(ctx, hash, "nope nope nope"); rehash {
		t.Fatal("rehash after a wrong password")
	}
}

func TestMalformedHash(t *testing.T) {
	h := NewHasher(cheap)
	for _, s := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=64,t=0,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$!!$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5",
	} {
		if _, _, err := h.Verify(context.Background(), s, "x"); !errors.Is(err, ErrMalformedHash) {
			t.Errorf("%q: err = %v", s, err)
		}
	}
	h.VerifyDummy(context.Background(), "anything") // must not panic
}

func TestHashHonoursContext(t *testing.T) {
	h := NewHasher(cheap)
	h.slots <- struct{}{}
	h.slots <- struct{}{} // both slots busy
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidate(t *testing.T) {
	if err := ValidatePassword("elevenchars"); err == nil {
		t.Error("11 characters accepted")
	}
	if err := ValidatePassword("twelve chars"); err != nil {
		t.Error(err)
	}
	if err := ValidatePassword("äöüäöüäöüäöü"); err != nil {
		t.Errorf("12 umlauts: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("x", MaxPasswordLength+1)); err == nil {
		t.Error("overlong password accepted")
	}
	if got := NormalizeUserName("  Patrick "); got != "patrick" {
		t.Errorf("normalize = %q", got)
	}
	for _, ok := range []string{"patrick", "p.klein", "a_b-1"} {
		if err := ValidateUserName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Patrick", "a b", "ä", "a@b", strings.Repeat("a", 65)} {
		if err := ValidateUserName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSessionToken(t *testing.T) {
	tok, hash, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 || len(hash) != 32 || string(HashSessionToken(tok)) != string(hash) {
		t.Fatalf("token %q hash %x", tok, hash)
	}
	tok2, _, _ := NewSessionToken()
	if tok == tok2 {
		t.Fatal("same token twice")
	}
}

func TestLimiterPerUser(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.now = func() time.Time { return now }

	for i := range 4 {
		fail(t, l, "alice", "10.0.0.1")
		if d := blocked(t, l, "alice", "10.0.0.1"); d != 0 {
			t.Fatalf("blocked after %d failures: %v", i+1, d)
		}
	}
	fail(t, l, "alice", "10.0.0.1")
	if d := blocked(t, l, "alice", "10.0.0.9"); d != time.Minute {
		t.Fatalf("after 5 failures: %v", d)
	}
	if d := blocked(t, l, "bob", "10.0.0.1"); d != 0 {
		t.Fatalf("other user blocked: %v", d)
	}

	// The block doubles with each further failure, up to 15 minutes.
	want := []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for _, w := range want {
		now = now.Add(blocked(t, l, "alice", "x"))
		fail(t, l, "alice", "10.0.0.2")
		if d := blocked(t, l, "alice", "x"); d != w {
			t.Fatalf("block = %v, want %v", d, w)
		}
	}

	// A success forgets the user's failures.
	now = now.Add(15 * time.Minute)
	succeed(t, l, "alice", "10.0.0.3")
	fail(t, l, "alice", "10.0.0.3")
	if d := blocked(t, l, "alice", "x"); d != 0 {
		t.Fatalf("blocked after success: %v", d)
	}

	// Failures older than the window do not count.
	for range 4 {
		fail(t, l, "carol", "10.0.0.4")
	}
	now = now.Add(16 * time.Minute)
	fail(t, l, "carol", "10.0.0.4")
	if d := blocked(t, l, "carol", "x"); d != 0 {
		t.Fatalf("old failures counted: %v", d)
	}
}

func TestLimiterPerAddress(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	for i := range 20 {
		if d := blocked(t, l, "x", "10.0.0.1"); d != 0 {
			t.Fatalf("blocked after %d failures", i)
		}
		fail(t, l, strings.Repeat("u", i+1), "10.0.0.1") // a new name each time
	}
	if d := blocked(t, l, "new", "10.0.0.1"); d != 15*time.Minute {
		t.Fatalf("address block = %v", d)
	}
	if d := blocked(t, l, "new", "10.0.0.2"); d != 0 {
		t.Fatalf("other address blocked: %v", d)
	}
	now = now.Add(15 * time.Minute)
	if d := blocked(t, l, "new", "10.0.0.1"); d != 0 {
		t.Fatalf("still blocked: %v", d)
	}
}

func TestLimiterSweeps(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	for i := range sweepEvery - 1 {
		// Recorded without Begin: the address is blocked after 20.
		forceFail(l, fmt.Sprintf("u%d", i), "10.0.0.1")
	}
	now = now.Add(time.Hour)
	forceFail(l, "last", "10.0.0.2")
	if len(l.users) != 1 || len(l.addrs) != 1 {
		t.Fatalf("after sweep: %d users, %d addrs", len(l.users), len(l.addrs))
	}
}

// blocked returns how long Begin refuses user from addr; zero means it
// admits an attempt, which is released.
func blocked(t *testing.T, l *Limiter, user, addr string) time.Duration {
	t.Helper()
	a, wait, err := l.Begin(context.Background(), user, addr)
	if err != nil {
		t.Fatal(err)
	}
	a.Release()
	return wait
}

// fail records a failed attempt of user from addr, which must be admitted.
func fail(t *testing.T, l *Limiter, user, addr string) {
	t.Helper()
	a, wait, err := l.Begin(context.Background(), user, addr)
	if a == nil {
		t.Fatalf("attempt of %q from %s refused: %v %v", user, addr, wait, err)
	}
	a.Fail()
}

// succeed records a successful attempt of user from addr.
func succeed(t *testing.T, l *Limiter, user, addr string) {
	t.Helper()
	a, wait, err := l.Begin(context.Background(), user, addr)
	if a == nil {
		t.Fatalf("attempt of %q from %s refused: %v %v", user, addr, wait, err)
	}
	a.Succeed()
}

// forceFail records a failure even while user or addr is blocked.
func forceFail(l *Limiter, user, addr string) {
	l.mu.Lock()
	k := userKey(user)
	a := &Attempt{l: l, userKey: k, user: reserve(l.users, k), addrKey: addr, addr: reserve(l.addrs, addr)}
	l.mu.Unlock()
	a.Fail()
}

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) != 23 || strings.Count(pw, "-") != 3 || ValidatePassword(pw) != nil {
			t.Fatalf("password %q", pw)
		}
		if strings.ContainsAny(pw, "01lo") {
			t.Fatalf("lookalike character in %q", pw)
		}
		if seen[pw] {
			t.Fatalf("%q generated twice", pw)
		}
		seen[pw] = true
	}
}
