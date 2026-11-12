package auth

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type quotaClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *quotaClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *quotaClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestQuota(ttl time.Duration, total int) (*Quota, *quotaClock) {
	c := &quotaClock{now: time.Unix(1_700_000_000, 0)}
	return NewQuota(ttl, total, c.Now), c
}

func mustBegin(t *testing.T, q *Quota, keys ...QuotaKey) *QuotaSlot {
	t.Helper()
	s, wait, full := q.Begin(keys...)
	if s == nil {
		t.Fatalf("Begin(%v) refused: wait %v, full %v", keys, wait, full)
	}
	return s
}

func refused(t *testing.T, q *Quota, want time.Duration, wantFull bool, keys ...QuotaKey) {
	t.Helper()
	s, wait, full := q.Begin(keys...)
	if s != nil || wait != want || full != wantFull {
		t.Fatalf("Begin(%v) = %v, %v, %v; want refused, %v, %v", keys, s != nil, wait, full, want, wantFull)
	}
}

// The wait of a refused key is until its own oldest slot expires, even when
// other keys hold older slots.
func TestQuotaPerKeyCapAndExactWait(t *testing.T) {
	q, c := newTestQuota(5*time.Minute, 1000)
	a := QuotaKey{Key: "a", Cap: 3}
	b := QuotaKey{Key: "b", Cap: 3}
	mustBegin(t, q, b) // the oldest slot overall
	c.Advance(time.Minute)
	for range 3 {
		mustBegin(t, q, a)
		c.Advance(10 * time.Second)
	}
	// a's oldest slot started at 1:00 and expires at 6:00; now is 1:30.
	refused(t, q, 4*time.Minute+30*time.Second, false, a)
	mustBegin(t, q, b)
}

// A released slot at the head of a key's queue is skipped: the wait is
// until the key's oldest live slot expires.
func TestQuotaWaitSkipsReleased(t *testing.T) {
	q, c := newTestQuota(5*time.Minute, 1000)
	a := QuotaKey{Key: "a", Cap: 3}
	first := mustBegin(t, q, a)
	c.Advance(time.Minute)
	mustBegin(t, q, a)
	mustBegin(t, q, a)
	first.Release()
	mustBegin(t, q, a)
	refused(t, q, 5*time.Minute, false, a)
	c.Advance(time.Minute)
	refused(t, q, 4*time.Minute, false, a)
}

// The /48 key caps across many /64 keys.
func TestQuotaSharedKey(t *testing.T) {
	q, _ := newTestQuota(5*time.Minute, 1000)
	net := func(i int) []QuotaKey {
		return []QuotaKey{{Key: fmt.Sprintf("2001:db8:1:%x::/64", i), Cap: 3}, {Key: "2001:db8:1::/48", Cap: 7}}
	}
	for i := range 7 {
		mustBegin(t, q, net(i%3)...)
	}
	refused(t, q, 5*time.Minute, false, net(5)...)
	mustBegin(t, q, QuotaKey{Key: "2001:db8:2::/64", Cap: 3}, QuotaKey{Key: "2001:db8:2::/48", Cap: 7})
	// /64 number 0 holds 3 and is refused on its own key too.
	refused(t, q, 5*time.Minute, false, net(0)...)
}

func TestQuotaTotal(t *testing.T) {
	q, c := newTestQuota(5*time.Minute, 4)
	for i := range 4 {
		mustBegin(t, q, QuotaKey{Key: fmt.Sprint(i), Cap: 10})
		c.Advance(time.Second)
	}
	// The oldest slot started 4 seconds ago.
	refused(t, q, 5*time.Minute-4*time.Second, true, QuotaKey{Key: "new", Cap: 10})
	c.Advance(5*time.Minute - 4*time.Second)
	mustBegin(t, q, QuotaKey{Key: "new", Cap: 10})
	refused(t, q, time.Second, true, QuotaKey{Key: "other", Cap: 10})
}

func TestQuotaExpiry(t *testing.T) {
	q, c := newTestQuota(5*time.Minute, 100)
	a := QuotaKey{Key: "a", Cap: 2}
	mustBegin(t, q, a).Succeed()
	mustBegin(t, q, a)
	c.Advance(5*time.Minute - time.Nanosecond)
	refused(t, q, time.Nanosecond, false, a)
	c.Advance(time.Nanosecond)
	mustBegin(t, q, a)
	mustBegin(t, q, a)
	if live, entries, keys := q.Len(); live != 2 || entries != 2 || keys != 1 {
		t.Fatalf("Len = %d, %d, %d; want 2, 2, 1", live, entries, keys)
	}
	c.Advance(5 * time.Minute)
	mustBegin(t, q, QuotaKey{Key: "b", Cap: 2})
	if live, entries, keys := q.Len(); live != 1 || entries != 1 || keys != 1 {
		t.Fatalf("after expiry Len = %d, %d, %d; want 1, 1, 1 (key a dropped)", live, entries, keys)
	}
}

func TestQuotaRelease(t *testing.T) {
	q, _ := newTestQuota(5*time.Minute, 2)
	a := QuotaKey{Key: "a", Cap: 1}
	s := mustBegin(t, q, a)
	refused(t, q, 5*time.Minute, false, a)
	s.Release()
	s2 := mustBegin(t, q, a)
	// Repeated calls do nothing: the second Release must not free s2's slot.
	s.Release()
	s.Succeed()
	refused(t, q, 5*time.Minute, false, a)
	s2.Succeed()
	s2.Release()
	refused(t, q, 5*time.Minute, false, a)
	var none *QuotaSlot
	none.Release()
	none.Succeed()
	// The total counts released slots no longer.
	mustBegin(t, q, QuotaKey{Key: "b", Cap: 1})
	refused(t, q, 5*time.Minute, true, QuotaKey{Key: "c", Cap: 1})
}

// A slot released after it expired frees nothing else.
func TestQuotaReleaseAfterExpiry(t *testing.T) {
	q, c := newTestQuota(time.Minute, 10)
	a := QuotaKey{Key: "a", Cap: 1}
	s := mustBegin(t, q, a)
	c.Advance(time.Minute)
	mustBegin(t, q, a)
	s.Release()
	refused(t, q, time.Minute, false, a)
	if live, _, keys := q.Len(); live != 1 || keys != 1 {
		t.Fatalf("Len = %d held, %d keys; want 1, 1", live, keys)
	}
}

// Memory stays bounded: distinct keys over several lifetimes, with and
// without releases, never hold more than total entries (twice that with
// tombstones) and keys.
func TestQuotaBounded(t *testing.T) {
	const total = 500
	q, c := newTestQuota(time.Minute, total)
	maxEntries, maxKeys := 0, 0
	for i := range 20 * total {
		s, _, _ := q.Begin(QuotaKey{Key: fmt.Sprintf("k%d", i), Cap: 1}, QuotaKey{Key: fmt.Sprintf("n%d", i%7), Cap: total})
		if s != nil && i%3 == 0 {
			s.Release()
		}
		_, entries, keys := q.Len()
		maxEntries, maxKeys = max(maxEntries, entries), max(maxKeys, keys)
		if i%50 == 0 {
			c.Advance(time.Second)
		}
	}
	if maxEntries > 2*total || maxKeys > total+7 {
		t.Fatalf("held up to %d entries and %d keys; total %d", maxEntries, maxKeys, total)
	}
	q2, c2 := newTestQuota(time.Minute, total)
	for i := range 20 * total {
		q2.Begin(QuotaKey{Key: fmt.Sprintf("k%d", i), Cap: 1})
		if _, entries, keys := q2.Len(); entries > total || keys > total {
			t.Fatalf("held %d entries and %d keys; total %d", entries, keys, total)
		}
		if i%50 == 0 {
			c2.Advance(time.Second)
		}
	}
}

// Parallel Begins never exceed a cap.
func TestQuotaParallel(t *testing.T) {
	parallel := func(q *Quota, keys func(i int) []QuotaKey) int32 {
		var got atomic.Int32
		var wg sync.WaitGroup
		for i := range 100 {
			wg.Go(func() {
				if s, _, _ := q.Begin(keys(i)...); s != nil {
					got.Add(1)
				}
			})
		}
		wg.Wait()
		return got.Load()
	}
	q, _ := newTestQuota(time.Minute, 1000)
	if n := parallel(q, func(int) []QuotaKey { return []QuotaKey{{Key: "a", Cap: 10}, {Key: "net", Cap: 25}} }); n != 10 {
		t.Fatalf("granted %d on a key with cap 10", n)
	}
	if n := parallel(q, func(i int) []QuotaKey { return []QuotaKey{{Key: fmt.Sprint("b", i), Cap: 10}, {Key: "net", Cap: 25}} }); n != 15 {
		t.Fatalf("granted %d more on a shared key with cap 25 and 10 used", n)
	}
	q, _ = newTestQuota(time.Minute, 30)
	if n := parallel(q, func(i int) []QuotaKey { return []QuotaKey{{Key: fmt.Sprint("o", i), Cap: 1}} }); n != 30 {
		t.Fatalf("granted %d with a total of 30", n)
	}
}

// A key given twice in one Begin counts once.
func TestQuotaRepeatedKey(t *testing.T) {
	q, _ := newTestQuota(time.Minute, 10)
	a := QuotaKey{Key: "a", Cap: 2}
	s := mustBegin(t, q, a, a)
	mustBegin(t, q, a)
	refused(t, q, time.Minute, false, a)
	s.Release()
	mustBegin(t, q, a)
}

// Released slots behind a held one, and a queue that never empties, keep
// the stored entries and their arrays bounded.
func TestQuotaQueuesStayCompact(t *testing.T) {
	q, c := newTestQuota(5*time.Second, 10)
	k := QuotaKey{Key: "k", Cap: 3}
	mustBegin(t, q, k) // held at the head of both queues
	for range 10_000 {
		mustBegin(t, q, k).Release()
	}
	key := q.keys["k"]
	if _, entries, _ := q.Len(); entries > 2*compactMin || len(key.q.items)-key.q.head > 2*compactMin {
		t.Fatalf("stored %d entries, %d for the key; want tombstones compacted", entries, len(key.q.items)-key.q.head)
	}
	for range 10_000 {
		c.Advance(time.Second)
		mustBegin(t, q, QuotaKey{Key: "r", Cap: 10})
	}
	if n := cap(q.all.items); n > 4*compactMin {
		t.Fatalf("queue array grew to %d", n)
	}
}
