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
