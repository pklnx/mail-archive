package imapsync

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// The credit budget bounds what go-imap reads ahead of this package's
// code: data that reaches no code of ours (endless untagged responses,
// junk between commands, one huge response) runs it dry, and the
// connection is closed. Every byte read from the network spends budget;
// commands, responses our code uses and body bytes our code reads earn it.
const (
	// budgetStart covers the greeting, a TLS handshake, CAPABILITY and
	// the LOGIN answer.
	budgetStart = 64 << 10
	// budgetMax is the most a connection can save up. The largest
	// legitimate response without a body, a SELECT with about 1,000
	// keywords, is about 40 KiB.
	budgetMax = 256 << 10
	// creditCommand is earned by each command we send.
	creditCommand = 16 << 10
	// creditResponse plus the response's payload is earned by each
	// response our code uses (a LIST entry, a FETCH it keeps, SELECT).
	creditResponse = 512
	// noticeCreditMax caps the credit of one unsolicited EXPUNGE or
	// FETCH. A notice earns at most its own size on the wire (see
	// notice), so it never earns more than it cost.
	noticeCreditMax = 64
	// noticesMax caps the notices a connection earns credit for, however
	// many messages the server claims: an honest mass expunge or mass
	// flag change of up to a million messages passes.
	noticesMax = 1_000_000
)

// creditBody is earned for n body-literal bytes our code read: 5/4 of
// them, because the bytes are counted on the TCP stream below TLS, where
// small TLS records add up to about a fifth.
func creditBody(n int) int64 { return int64(n) + int64(n)/4 }

// meteredConn wraps the raw TCP connection, below TLS and STARTTLS. It
// spends the credit budget on every read, caps the total read, and arms
// the idle deadline while a command is in flight.
type meteredConn struct {
	net.Conn
	idle    time.Duration
	maxRead int64

	mu sync.Mutex
	// lib is the read deadline go-imap or crypto/tls asked for.
	lib time.Time
	// hard is an absolute deadline for the command in flight (LOGOUT).
	hard time.Time
	// cmd is the command in flight, "" between commands.
	cmd     string
	reading bool
	armed   time.Time
	budget  int64
	total   int64
	// cause is the first failure that closed the connection: a
	// *LimitError or a *TimeoutError.
	cause  error
	closed bool
	// notices counts the credited notices of the command in flight, up
	// to noticeMax; noticesTotal those of the connection, up to
	// noticesMax.
	notices, noticeMax, noticesTotal int64
	// noticesCap is noticesMax; tests lower it.
	noticesCap int64
	// idleArmed reports whether the armed deadline is the idle deadline,
	// not go-imap's own or the hard one.
	idleArmed bool
}

func newMeteredConn(raw net.Conn, idle time.Duration, maxRead int64) *meteredConn {
	return &meteredConn{Conn: raw, idle: idle, maxRead: maxRead, budget: budgetStart, noticesCap: noticesMax}
}

// deadline is the read deadline to arm: go-imap's own, shortened to the
// idle deadline while a command is in flight and to the hard deadline.
// Between commands it is go-imap's, which is zero between responses. idle
// reports whether the idle deadline is the one in force.
func (m *meteredConn) deadline() (d time.Time, idle bool) {
	d = m.lib
	if m.cmd != "" {
		if i := time.Now().Add(m.idle); d.IsZero() || i.Before(d) {
			d, idle = i, true
		}
		if !m.hard.IsZero() && (d.IsZero() || m.hard.Before(d)) {
			d, idle = m.hard, false
		}
	}
	return d, idle
}

// arm sets the read deadline on the raw conn if it changed. m.mu is held.
func (m *meteredConn) arm() {
	d, idle := m.deadline()
	m.idleArmed = idle
	if !d.Equal(m.armed) {
		_ = m.Conn.SetReadDeadline(d)
		m.armed = d
	}
}

// begin marks cmd as in flight and earns its credit. A Read that is
// already blocked gets the idle deadline at once. hard, if not zero,
// bounds the whole command. notices is how many unsolicited notices the
// command may earn credit for.
func (m *meteredConn) begin(cmd string, hard time.Time, notices int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cmd, m.hard = cmd, hard
	m.notices, m.noticeMax = 0, notices
	m.budget = min(m.budget+creditCommand, budgetMax)
	if m.reading {
		m.arm()
	}
}

// end marks the command as done: no idle deadline is armed until the next
// command.
func (m *meteredConn) end() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cmd, m.hard = "", time.Time{}
	if m.reading {
		m.arm()
	}
}

// credit earns n bytes of budget, up to budgetMax.
func (m *meteredConn) credit(n int64) {
	m.mu.Lock()
	m.budget = min(m.budget+n, budgetMax)
	m.mu.Unlock()
}

// fail records cause unless the connection already failed, and closes it.
// It returns the recorded cause. m.mu is held.
func (m *meteredConn) fail(cause error) error {
	if m.cause == nil {
		m.cause = cause
	}
	m.closed = true
	_ = m.Conn.Close()
	return m.cause
}

// trip closes the connection because the server exceeded a cap.
func (m *meteredConn) trip(lim *LimitError) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fail(lim)
}

// failure returns what closed the connection, nil if nothing did (or it
// was closed normally).
func (m *meteredConn) failure() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cause
}

// broken reports whether the connection is closed.
func (m *meteredConn) broken() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *meteredConn) Read(b []byte) (int, error) {
	m.mu.Lock()
	if m.cause != nil {
		err := m.cause
		m.mu.Unlock()
		return 0, err
	}
	allow := m.budget
	if m.maxRead > 0 {
		allow = min(allow, m.maxRead-m.total)
	}
	if allow <= 0 {
		lim := &LimitError{What: LimitUnrequested, Limit: budgetMax}
		if m.maxRead > 0 && m.total >= m.maxRead {
			lim = &LimitError{What: LimitRead, Limit: m.maxRead}
		}
		err := m.fail(lim)
		m.mu.Unlock()
		return 0, err
	}
	if int64(len(b)) > allow {
		b = b[:allow]
	}
	m.arm()
	m.reading = true
	m.mu.Unlock()

	n, err := m.Conn.Read(b)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.reading = false
	m.budget -= int64(n)
	m.total += int64(n)
	if err != nil && errors.Is(err, os.ErrDeadlineExceeded) {
		te := &TimeoutError{Command: m.cmd}
		if m.idleArmed {
			te.Idle = m.idle
		}
		return n, m.fail(te)
	}
	if err != nil {
		m.closed = true
	}
	return n, err
}

func (m *meteredConn) SetReadDeadline(t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lib = t
	m.arm()
	return nil
}

func (m *meteredConn) SetDeadline(t time.Time) error {
	if err := m.SetReadDeadline(t); err != nil {
		return err
	}
	return m.SetWriteDeadline(t)
}

func (m *meteredConn) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return m.Conn.Close()
}

// creditReader reads a body literal for our code and earns its credit.
// It records the first read error other than io.EOF: after one, go-imap's
// reader goroutine has taken the connection back, so the literal must not
// be read again. A literal that ends before its announced size (the
// connection closed) is io.ErrUnexpectedEOF, never a clean EOF: go-imap
// reports it as io.EOF, which would hand a truncated body to the sink.
type creditReader struct {
	r    io.Reader
	m    *meteredConn
	left int64
	err  error
	eof  bool
}

func newCreditReader(lit interface {
	io.Reader
	Size() int64
}, m *meteredConn) *creditReader {
	return &creditReader{r: lit, m: m, left: lit.Size()}
}

func (c *creditReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.eof {
		return 0, io.EOF
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if n > 0 {
		c.m.credit(creditBody(n))
	}
	if errors.Is(err, io.EOF) && c.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	switch {
	case errors.Is(err, io.EOF):
		c.eof = true
	case err != nil:
		c.err = err
	}
	return n, err
}

// notice earns credit for an unsolicited EXPUNGE or FETCH that arrives
// while a command is in flight: another client expunging or flagging
// messages in the folder. wire is the notice's smallest possible size on
// the wire, so the credit never exceeds what the notice cost the budget;
// it is capped at noticeCreditMax. At most noticeMax notices per command
// and noticesMax per connection earn it.
func (m *meteredConn) notice(wire int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd == "" || m.notices >= m.noticeMax || m.noticesTotal >= m.noticesCap {
		return
	}
	m.notices++
	m.noticesTotal++
	m.budget = min(m.budget+int64(min(wire, noticeCreditMax)), budgetMax)
}
