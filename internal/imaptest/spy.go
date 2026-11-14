package imaptest

import (
	"net"
	"sync"
	"time"

	"github.com/keepia/archive/internal/testwait"
)

// SpyWrap returns a function for imapsync.Config.WrapConn that puts a new
// ConnSpy between the client and its TCP connection, and a channel that
// hands out each spy once the dial has created it.
func SpyWrap() (wrap func(net.Conn) net.Conn, spies <-chan *ConnSpy) {
	ch := make(chan *ConnSpy, 16)
	return func(c net.Conn) net.Conn {
		s := &ConnSpy{Conn: c}
		ch <- s
		return s
	}, ch
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
