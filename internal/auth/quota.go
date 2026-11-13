package auth

import (
	"sync"
	"time"
)

// Quota caps how many slots are held at once, per key and in total. A slot
// is held from Begin until it expires after the quota's lifetime, or until
// Release. It is for work that anybody can start without a session and
// that occupies the server for a while, like a passkey login in progress:
// each client can use up only its own share, and the total only bounds
// memory. It keeps its counters in memory; a restart resets them.
//
// Every slot is an entry in one queue in the order of Begin, and in one
// queue per key. Expired entries leave from the head of the queue; a
// released entry stays as a tombstone until it reaches the head or the
// queue is compacted. So Begin costs O(1) amortised, and a refusal names
// the exact wait until the oldest live entry that blocks it expires.
type Quota struct {
	mu    sync.Mutex
	ttl   time.Duration
	total int
	now   func() time.Time
	live  int // entries neither expired nor released
	all   quotaQueue
	keys  map[string]*quotaKey
}

// QuotaKey is one key a slot counts against, with that key's cap.
type QuotaKey struct {
	Key string
	Cap int
}

type quotaEntry struct {
	expires time.Time
	keys    []*quotaKey
	done    bool // expired or released
}

type quotaKey struct {
	name string
	n    int // live entries
	q    quotaQueue
}

// quotaQueue is a FIFO of entries. dead counts the done entries it still
// holds.
type quotaQueue struct {
	items []*quotaEntry
	head  int
	dead  int
}

// compactMin is the queue length below which tombstones are left alone.
const compactMin = 32

func (q *quotaQueue) push(e *quotaEntry) {
	if q.head > 0 && q.head*2 >= len(q.items) {
		n := copy(q.items, q.items[q.head:])
		clear(q.items[n:])
		q.items, q.head = q.items[:n], 0
	}
	q.items = append(q.items, e)
}

// front returns the oldest entry that is not done, dropping the done ones
// before it, or nil.
func (q *quotaQueue) front() *quotaEntry {
	for q.head < len(q.items) {
		e := q.items[q.head]
		if !e.done {
			return e
		}
		q.pop()
		q.dead--
	}
	return nil
}

func (q *quotaQueue) pop() {
	q.items[q.head] = nil
	q.head++
	if q.head == len(q.items) {
		q.items, q.head = q.items[:0], 0
	}
}

// buried notes that one entry in the queue is done, and compacts the queue
// once more than half of it are tombstones.
func (q *quotaQueue) buried() {
	q.dead++
	n := len(q.items) - q.head
	if n < compactMin || q.dead*2 <= n {
		return
	}
	kept := q.items[:0]
	for _, e := range q.items[q.head:] {
		if !e.done {
			kept = append(kept, e)
		}
	}
	clear(q.items[len(kept):])
	q.items, q.head, q.dead = kept, 0, 0
}

// NewQuota holds a slot for ttl, at most total slots at once. now is the
// clock; nil means time.Now.
func NewQuota(ttl time.Duration, total int, now func() time.Time) *Quota {
	if now == nil {
		now = time.Now
	}
	return &Quota{ttl: ttl, total: total, now: now, keys: map[string]*quotaKey{}}
}

// QuotaSlot is a slot held by Quota.Begin. It ends with Release, which
// frees it at once, or Succeed, which keeps it until it expires. Later
// calls do nothing, and so do calls on a nil QuotaSlot.
type QuotaSlot struct {
	q     *Quota
	e     *quotaEntry
	ended bool
}

// Begin takes a slot that counts against each of keys (distinct keys; a
// repeated one counts once) and the total. If a key or the total is at its
// cap, it takes nothing and returns how long until the slots that block it
// expire, and full reports whether the total was one of them.
func (q *Quota) Begin(keys ...QuotaKey) (slot *QuotaSlot, wait time.Duration, full bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	q.expire(now)
	until := func(e *quotaEntry) {
		if d := e.expires.Sub(now); d > wait {
			wait = d
		}
	}
	for _, k := range keys {
		if c := q.keys[k.Key]; c != nil && c.n >= k.Cap {
			until(c.q.front())
		} else if c == nil && k.Cap <= 0 {
			wait = max(wait, q.ttl)
		}
	}
	if q.live >= q.total {
		full = true
		if e := q.all.front(); e != nil {
			until(e)
		} else {
			wait = max(wait, q.ttl)
		}
	}
	if full || wait > 0 {
		return nil, max(wait, time.Nanosecond), full
	}
	e := &quotaEntry{expires: now.Add(q.ttl), keys: make([]*quotaKey, 0, len(keys))}
	for _, k := range keys {
		c := q.keys[k.Key]
		if c == nil {
			c = &quotaKey{name: k.Key}
			q.keys[k.Key] = c
		} else if c.n > 0 && c.q.items[len(c.q.items)-1] == e {
			continue // repeated key
		}
		c.n++
		c.q.push(e)
		e.keys = append(e.keys, c)
	}
	q.all.push(e)
	q.live++
	return &QuotaSlot{q: q, e: e}, 0, false
}

// expire ends the entries that expired by now. mu is held.
func (q *Quota) expire(now time.Time) {
	for {
		e := q.all.front()
		if e == nil || e.expires.After(now) {
			return
		}
		q.all.pop()
		q.finish(e)
	}
}

// finish marks e done and takes it off its keys. mu is held.
func (q *Quota) finish(e *quotaEntry) {
	e.done = true
	q.live--
	for _, c := range e.keys {
		c.n--
		if c.n == 0 {
			delete(q.keys, c.name)
			continue
		}
		c.q.buried()
	}
}

// Release frees the slot at once.
func (s *QuotaSlot) Release() {
	if s == nil {
		return
	}
	q := s.q
	q.mu.Lock()
	defer q.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	if s.e.done {
		return // expired already
	}
	q.finish(s.e)
	q.all.buried()
}

// Succeed keeps the slot until it expires.
func (s *QuotaSlot) Succeed() {
	if s == nil {
		return
	}
	s.q.mu.Lock()
	defer s.q.mu.Unlock()
	s.ended = true
}

// Len returns the slots held, the entries stored (tombstones included) and
// the keys. Tests use it to check that memory stays bounded.
func (q *Quota) Len() (live, entries, keys int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.live, len(q.all.items) - q.all.head, len(q.keys)
}
