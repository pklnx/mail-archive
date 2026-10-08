package store

import (
	"context"
	"fmt"
	"math"
)

// syncLockClass is the first key of the advisory locks that guard account
// syncs ("ma" in ASCII); the second key is the account ID.
const syncLockClass = 0x6d61

// blobLockClass is the first key of the advisory lock that guards the blob
// store ("mb" in ASCII); the second key is always 0. Writers of new blobs
// hold it shared, verify holds it exclusive while it looks for orphans.
const blobLockClass = 0x6d62

// reindexLockClass is the first key of the advisory lock that lets only one
// reindex run at a time ("mc" in ASCII); the second key is always 0.
const reindexLockClass = 0x6d63

// TryLockSync takes the sync lock of an account, so that the CLI, the web
// server and the schedule never sync the same account at the same time. It
// returns false if another session holds the lock. The lock is held on a
// dedicated connection until unlock is called; if the process dies,
// PostgreSQL releases it with the connection.
func (s *Store) TryLockSync(ctx context.Context, accountID int64) (unlock func(), ok bool, err error) {
	return s.tryLockSync(ctx, accountID, false)
}

// TryLockSyncForWrite is TryLockSync for a sync or import that writes
// blobs: once it has the account's lock, it also waits for the shared blob
// lock on the same connection, so that verify never sees a blob without its
// row as an orphan.
func (s *Store) TryLockSyncForWrite(ctx context.Context, accountID int64) (unlock func(), ok bool, err error) {
	return s.tryLockSync(ctx, accountID, true)
}

func (s *Store) tryLockSync(ctx context.Context, accountID int64, blobs bool) (unlock func(), ok bool, err error) {
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
	unlock = func() {
		// Unlock even if ctx was cancelled; on failure, drop the connection
		// so the lock cannot leak into the pool.
		bg := context.WithoutCancel(ctx)
		_, err := conn.Exec(bg, "SELECT pg_advisory_unlock($1, $2)", syncLockClass, key)
		if err == nil && blobs {
			_, err = conn.Exec(bg, "SELECT pg_advisory_unlock_shared($1, 0)", blobLockClass)
		}
		if err != nil {
			_ = conn.Conn().Close(bg)
		}
		conn.Release()
	}
	if blobs {
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock_shared($1, 0)", blobLockClass); err != nil {
			// A cancelled wait leaves the connection unusable; close it,
			// which also releases the account lock.
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
			conn.Release()
			return nil, false, fmt.Errorf("wait for the blob lock: %w", err)
		}
	}
	return unlock, true, nil
}

// TryLockBlobs takes the blob lock exclusively, which waits for no one: it
// returns false while a sync or import writes blobs. Until unlock is
// called, new writers wait.
func (s *Store) TryLockBlobs(ctx context.Context) (unlock func(), ok bool, err error) {
	return s.tryLockClass(ctx, blobLockClass)
}

// TryLockReindex takes the reindex lock. It returns false if another
// session runs a reindex.
func (s *Store) TryLockReindex(ctx context.Context) (unlock func(), ok bool, err error) {
	return s.tryLockClass(ctx, reindexLockClass)
}

// tryLockClass takes the session lock (class, 0) on a dedicated connection.
func (s *Store) tryLockClass(ctx context.Context, class int32) (unlock func(), ok bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 0)", class).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		bg := context.WithoutCancel(ctx)
		if _, err := conn.Exec(bg, "SELECT pg_advisory_unlock($1, 0)", class); err != nil {
			_ = conn.Conn().Close(bg)
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
