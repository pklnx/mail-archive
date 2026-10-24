package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

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

// AcquiredConnsForTest returns how many connections of the pool are in use.
func (s *Store) AcquiredConnsForTest() int32 {
	return s.pool.Stat().AcquiredConns()
}

// ForMigrationsWithCheckInterval is ForMigrations with another client
// connection check interval.
var ForMigrationsWithCheckInterval = withCheckInterval
