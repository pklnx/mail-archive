package imapsync

import (
	"net"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/testwait"
)

// WithSpy makes cfg's connection go through a new ConnSpy, which it
// returns once Dial has created it (through the channel).
func WithSpy(cfg *Config) <-chan *ConnSpy {
	ch := make(chan *ConnSpy, 1)
	cfg.wrapConn = func(c net.Conn) net.Conn {
		s := &ConnSpy{Conn: c}
		ch <- s
		return s
	}
	return ch
}

// ConnSpy sits between the metered conn and the TCP conn. It records the
// read deadlines it is asked to arm but never lets real time expire them;
// Expire does, at once.
type ConnSpy struct {
	net.Conn
	// Changed is notified after every SetReadDeadline.
	Changed testwait.Signal

	mu      sync.Mutex
	armed   time.Time
	armedAt time.Time
	history []time.Time
	expired bool
}

// SetReadDeadline records t. Before Expire it arms no deadline on the TCP
// conn; after Expire it arms one in the past whenever t is not zero.
func (s *ConnSpy) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.armed, s.armedAt = t, time.Now()
	s.history = append(s.history, t)
	expired := s.expired
	s.mu.Unlock()
	s.Changed.Notify()
	if expired && !t.IsZero() {
		return s.Conn.SetReadDeadline(time.Unix(1, 0))
	}
	return s.Conn.SetReadDeadline(time.Time{})
}

// Armed returns the read deadline in force, zero if none, and when it was
// set.
func (s *ConnSpy) Armed() (deadline, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.armed, s.armedAt
}

// Expire makes the armed deadline pass now: a blocked Read returns
// os.ErrDeadlineExceeded.
func (s *ConnSpy) Expire() {
	s.mu.Lock()
	s.expired = true
	armed := !s.armed.IsZero()
	s.mu.Unlock()
	if armed {
		_ = s.Conn.SetReadDeadline(time.Unix(1, 0))
	}
}

// Budget returns the connection's credit budget.
func (conn *Conn) Budget() int64 {
	conn.m.mu.Lock()
	defer conn.m.mu.Unlock()
	return conn.m.budget
}

// BytesRead returns the bytes the connection read from the network.
func (conn *Conn) BytesRead() int64 {
	conn.m.mu.Lock()
	defer conn.m.mu.Unlock()
	return conn.m.total
}

// SetNoticesMax lowers the notices a connection earns credit for.
func SetNoticesMax(cfg *Config, n int64) { cfg.noticesMax = n }

const (
	NoticesMax  = noticesMax
	BudgetStart = budgetStart
	BudgetMax   = budgetMax
)
