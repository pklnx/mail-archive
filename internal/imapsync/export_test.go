package imapsync

import (
	"github.com/pklnx/mail-archive/internal/imaptest"
)

// WithSpy makes cfg's connection go through a new ConnSpy, which it
// returns once Dial has created it (through the channel).
func WithSpy(cfg *Config) <-chan *ConnSpy {
	wrap, spies := imaptest.SpyWrap()
	cfg.WrapConn = wrap
	return spies
}

// ConnSpy is imaptest.ConnSpy.
type ConnSpy = imaptest.ConnSpy

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
