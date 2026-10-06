package store

import (
	"context"
	"fmt"
	"math"
)

// syncLockClass is the first key of the advisory locks that guard account
// syncs ("ma" in ASCII); the second key is the account ID.
const syncLockClass = 0x6d61

// TryLockSync takes the sync lock of an account, so that the CLI, the web
// server and the schedule never sync the same account at the same time. It
// returns false if another session holds the lock. The lock is held on a
// dedicated connection until unlock is called; if the process dies,
// PostgreSQL releases it with the connection.
func (s *Store) TryLockSync(ctx context.Context, accountID int64) (unlock func(), ok bool, err error) {
	if accountID < 1 || accountID > math.MaxInt32 {
		return nil, false, fmt.Errorf("account id %d out of range for the sync lock", accountID)
	}
	key := int32(accountID) //nolint:gosec // range checked above
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", syncLockClass, key).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		// Unlock even if ctx was cancelled; on failure, drop the connection
		// so the lock cannot leak into the pool.
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1, $2)", syncLockClass, key); err != nil {
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
		conn.Release()
	}, true, nil
}

// SyncingAccounts returns the IDs of accounts whose sync lock is held by any
// session, in any process.
func (s *Store) SyncingAccounts(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT objid FROM pg_locks
		WHERE locktype = 'advisory' AND classid = $1 AND objsubid = 2 AND granted
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, syncLockClass)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id uint32
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[int64(id)] = true
	}
	return out, rows.Err()
}
