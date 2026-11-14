package smtpd

import (
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keepia/archive/internal/auth"
)

// gate refuses connections before go-smtp sees them: clients outside the
// allowlist get 554, connections over the limits get 421 so that the mail
// server retries later.
type gate struct {
	net.Listener
	// allowed is the allowlist; empty means any address (only with Auth).
	allowed  []netip.Prefix
	slots    chan struct{}
	hostname string
	log      *slog.Logger
	// perAddr caps the connections per client address (auth.LimitKey);
	// 0 means no cap. preAuth closes a connection that has not logged in
	// by then; 0 means no deadline. Both are set with Auth.
	perAddr   int
	preAuth   time.Duration
	afterFunc func(time.Duration, func()) (stop func() bool)
	mu        sync.Mutex
	byAddr    map[string]int
	// slotFreed, if set, is called after a connection's slot is free
	// again, for tests.
	slotFreed func()
}

func newGate(l net.Listener, allowed []netip.Prefix, maxConns int, hostname string, log *slog.Logger) *gate {
	return &gate{Listener: l, allowed: allowed, slots: make(chan struct{}, maxConns), hostname: hostname, log: log,
		byAddr: map[string]int{}}
}

func (g *gate) Accept() (net.Conn, error) {
	for {
		c, err := g.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := remoteIP(c)
		if !g.allows(ip) {
			g.log.Error("smtp connection refused", "code", 554, "client_ip", ip.String(), "reason", "address not in the allowlist")
			go reply(c, "554 5.7.1 "+g.hostname+" access denied")
			continue
		}
		select {
		case g.slots <- struct{}{}:
		default:
			g.log.Warn("smtp connection refused", "code", 421, "client_ip", ip.String(), "reason", "too many connections")
			go reply(c, "421 4.7.0 "+g.hostname+" too many connections, try again later")
			continue
		}
		key := auth.LimitKey(ip)
		if !g.takeAddr(key) {
			<-g.slots // never a connection: no slotFreed
			g.log.Warn("smtp connection refused", "code", 421, "client_ip", ip.String(), "reason", "too many connections from this address")
			go reply(c, "421 4.7.0 "+g.hostname+" too many connections from your address, try again later")
			continue
		}
		gc := &gatedConn{Conn: c, release: func() { g.release(key) }}
		if g.preAuth > 0 {
			gc.stop = g.afterFunc(g.preAuth, func() {
				g.log.Warn("smtp connection closed", "client_ip", ip.String(), "reason", "no login within "+g.preAuth.String())
				_ = gc.Close()
			})
		}
		return gc, nil
	}
}

// takeAddr counts a connection of key; false when key is at the cap.
func (g *gate) takeAddr(key string) bool {
	if g.perAddr <= 0 {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byAddr[key] >= g.perAddr {
		return false
	}
	g.byAddr[key]++
	return true
}

// release frees a connection's slot and its count per address.
func (g *gate) release(key string) {
	if g.perAddr > 0 {
		g.mu.Lock()
		if g.byAddr[key]--; g.byAddr[key] <= 0 {
			delete(g.byAddr, key)
		}
		g.mu.Unlock()
	}
	<-g.slots
	if g.slotFreed != nil {
		g.slotFreed()
	}
}

func (g *gate) allows(ip netip.Addr) bool {
	if len(g.allowed) == 0 {
		return true // Serve refuses an empty allowlist without Auth
	}
	for _, p := range g.allowed {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// reply sends one line and closes the connection.
func reply(c net.Conn, line string) {
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, _ = c.Write([]byte(line + "\r\n"))
	_ = c.Close()
}

func remoteIP(c net.Conn) netip.Addr {
	if a, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return a.AddrPort().Addr().Unmap()
	}
	ap, err := netip.ParseAddrPort(c.RemoteAddr().String())
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// gatedConn frees its slot when closed. With Auth it carries the pre-auth
// timer, which a login stops.
type gatedConn struct {
	net.Conn
	once    sync.Once
	release func()
	// stop stops the pre-auth timer; nil without one. Set before Accept
	// returns the connection.
	stop func() bool
	// refusals counts refused AUTH and MAIL commands (session.refused);
	// closeAfterWrite closes the connection after the next write, the
	// answer to the last one.
	refusals        atomic.Int32
	closeAfterWrite atomic.Bool
}

func (c *gatedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if c.closeAfterWrite.Load() {
		_ = c.Close()
	}
	return n, err
}

func (c *gatedConn) Close() error {
	c.authenticated() // the timer has nothing left to close
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// authenticated stops the pre-auth timer.
func (c *gatedConn) authenticated() {
	if c.stop != nil {
		c.stop()
	}
}
