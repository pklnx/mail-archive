package store

import (
	"context"
	"net"

	"github.com/jackc/pgx/v5"
)

// WithDialForTest is an Open option that replaces the dialer of every
// connection.
func WithDialForTest(d func(ctx context.Context, network, addr string) (net.Conn, error)) Option {
	return func(c *openConfig) { c.dial = d }
}

// QueryRowForTest runs a query on a connection of the store's pool.
func (s *Store) QueryRowForTest(ctx context.Context, sql string, args ...any) pgx.Row {
	return s.pool.QueryRow(ctx, sql, args...)
}

// SetMaxEnvelopeMatches changes how many envelope matches a to: filter
// takes as a list, until the returned function restores it.
func SetMaxEnvelopeMatches(n int) (restore func()) {
	old := maxEnvelopeMatches
	maxEnvelopeMatches = n
	return func() { maxEnvelopeMatches = old }
}

// RebuildError builds the error of a failed index rebuild.
var RebuildError = rebuildError

// MarkerError builds the error of a rebuild whose marker could not be set.
var MarkerError = markerError

// HoldBlobWaitSlotForTest takes a wait slot of the blob lock until release
// is called.
func (s *Store) HoldBlobWaitSlotForTest(ctx context.Context) (release func(), err error) {
	return s.blobWaitSlot(ctx)
}

// TryBlobWaitSlotForTest takes a wait slot of the blob lock if one is free
// right now, without waiting: ok is false while every slot is held.
func (s *Store) TryBlobWaitSlotForTest() (release func(), ok bool) {
	select {
	case s.blobWait <- struct{}{}:
		return func() { <-s.blobWait }, true
	default:
		return nil, false
	}
}

// AcquiredConnsForTest returns how many connections of the pool are in use.
func (s *Store) AcquiredConnsForTest() int32 {
	return s.pool.Stat().AcquiredConns()
}

// ForMigrationsWithCheckInterval is ForMigrations with another client
// connection check interval.
var ForMigrationsWithCheckInterval = withCheckInterval

// SetGooseLockHolderQuery replaces the query that looks up the holder of
// goose's lock until the returned function restores it.
func SetGooseLockHolderQuery(q string) (restore func()) {
	old := gooseLockHolderQuery
	gooseLockHolderQuery = q
	return func() { gooseLockHolderQuery = old }
}

// WithSessionSettings is an Open option that runs the given statements on
// every new connection, as ForMigrations runs its SETs.
func WithSessionSettings(stmts ...string) Option {
	return func(c *openConfig) { c.settings = append(c.settings, stmts...) }
}

// WithQueryStartHook is an Open option that calls f with the SQL of every
// query when pgx starts it, before pgx checks the query's context and
// sends it (a pgx query tracer).
func WithQueryStartHook(f func(sql string)) Option {
	return func(c *openConfig) { c.tracer = queryStartTracer(f) }
}

// WithQueryStartConnHook is like WithQueryStartHook, but f also gets the
// backend process ID of the connection that starts the query.
func WithQueryStartConnHook(f func(pid uint32, sql string)) Option {
	return func(c *openConfig) { c.tracer = queryStartConnTracer(f) }
}

type queryStartConnTracer func(pid uint32, sql string)

func (f queryStartConnTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	f(conn.PgConn().PID(), d.SQL)
	return ctx
}

func (queryStartConnTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type queryStartTracer func(sql string)

func (f queryStartTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	f(d.SQL)
	return ctx
}

func (queryStartTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// ScopeIDs and ScopeEnvelopeIDs are the array parameters a scope gives the
// read queries, for plan tests.
func ScopeIDs(sc Scope) []int64         { return sc.ids() }
func ScopeEnvelopeIDs(sc Scope) []int64 { return sc.envelopeIDs() }

// ShareView is shareView: what an owner sees of another user's read.
var ShareView = shareView
