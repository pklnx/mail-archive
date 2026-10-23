// Package testwait lets tests wait for a condition that another goroutine
// makes true, woken by a signal instead of polling on a timer. Only tests
// import it.
package testwait

import (
	"sync"
	"testing"
	"time"
)

// Signal is a broadcast: Notify wakes everyone who took C before it. The
// zero value is ready to use.
type Signal struct {
	mu sync.Mutex
	ch chan struct{}
}

// C returns a channel that is closed at the next Notify.
func (s *Signal) C() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

// Notify wakes every waiter that took C before it.
func (s *Signal) Notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch != nil {
		close(s.ch)
		s.ch = nil
	}
}

const (
	// noDeadline bounds the wait of a test that has no deadline.
	noDeadline = 30 * time.Second
	// margin is how long before the test's deadline Until gives up, so its
	// message is reported instead of the timeout's panic.
	margin = 5 * time.Second
)

// limit is how long Until waits at most: until shortly before the test's
// deadline, or noDeadline without one. It is a guard against a hang, not
// a delay: Until returns as soon as the condition holds.
func limit(tb testing.TB) time.Duration {
	d, ok := tb.(interface{ Deadline() (time.Time, bool) })
	if !ok {
		return noDeadline
	}
	deadline, ok := d.Deadline()
	if !ok {
		return noDeadline
	}
	left := time.Until(deadline)
	if left-margin < left/2 {
		return left / 2
	}
	return left - margin
}

// Until returns once cond holds. It checks cond, and again after each
// Notify of sig; whoever changes what cond reads must notify sig after the
// change. cond runs in the calling goroutine, so it may call tb.Fatal. If
// cond does not hold before the test's deadline (see limit), the test
// fails with what.
func Until(tb testing.TB, sig *Signal, what string, cond func() bool) {
	tb.Helper()
	timer := time.NewTimer(limit(tb))
	defer timer.Stop()
	for {
		// C is taken before the check, so a Notify between the check and
		// the wait is not lost.
		woken := sig.C()
		if cond() {
			return
		}
		select {
		case <-woken:
		case <-timer.C:
			tb.Fatalf("timed out waiting for %s", what)
			return
		}
	}
}
