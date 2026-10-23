package smtpd

import (
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// gate refuses connections before go-smtp sees them: clients outside the
// allowlist get 554, connections over the limit get 421 so that the mail
// server retries later.
type gate struct {
	net.Listener
	allowed  []netip.Prefix
	slots    chan struct{}
	hostname string
	log      *slog.Logger
	// slotFreed, if set, is called after a connection's slot is free
	// again, for tests.
	slotFreed func()
}

func newGate(l net.Listener, allowed []netip.Prefix, maxConns int, hostname string, log *slog.Logger) *gate {
	return &gate{Listener: l, allowed: allowed, slots: make(chan struct{}, maxConns), hostname: hostname, log: log}
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
			return &gatedConn{Conn: c, release: g.release}, nil
		default:
			g.log.Warn("smtp connection refused", "code", 421, "client_ip", ip.String(), "reason", "too many connections")
			go reply(c, "421 4.7.0 "+g.hostname+" too many connections, try again later")
		}
	}
}

// release frees a connection's slot.
func (g *gate) release() {
	<-g.slots
	if g.slotFreed != nil {
		g.slotFreed()
	}
}

func (g *gate) allows(ip netip.Addr) bool {
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

// gatedConn frees its slot when closed.
type gatedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *gatedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
