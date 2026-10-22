package store

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
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

// attestFenceClass is the first key of the attestation fence ("md" in
// ASCII); the second key is always 0. Every transaction that inserts into
// messages or message_deletions holds it shared until it ends (inWriteTx);
// an attestation takes it exclusively while its snapshot starts, so the
// snapshot sees no writer in progress. Lock order: the blob lock first,
// then the fence, then row locks.
const attestFenceClass = 0x6d64

// attestWriterClass is the first key of the advisory lock that lets only
// one attestation be written at a time ("me" in ASCII); the second key is
// always 0.
const attestWriterClass = 0x6d65

// TryLockSync takes the sync lock of an account, so that the CLI, the web
// server and the schedule never sync the same account at the same time. It
// returns false if another session holds the lock. The lock is held on a
// dedicated connection until unlock is called; if the process dies,
// PostgreSQL releases it with the connection.
func (s *Store) TryLockSync(ctx context.Context, accountID int64) (unlock func(), ok bool, err error) {
	return s.tryLockSync(ctx, accountID, false)
}

// TryLockSyncForWrite is TryLockSync for a sync or import that writes
// blobs: once it has the account's lock, it also takes the shared blob
// lock on the same connection, so that verify never sees a blob without
// its row as an orphan. While the blob lock is held exclusively, it waits
// for it without the account's lock (blobWaitSlot) and then takes the
// account's lock again: ok is false if another session took it meanwhile.
func (s *Store) TryLockSyncForWrite(ctx context.Context, accountID int64) (unlock func(), ok bool, err error) {
	return s.tryLockSync(ctx, accountID, true)
}

// blobWaiters is how many writers of one Store may wait for the blob lock
// in PostgreSQL at a time (blobWaitSlot).
const blobWaiters = 1

// blobWaitSlot waits for one of the blobWaiters wait slots, or for the end
// of ctx. A writer that waits for the blob lock keeps a connection; at
// most blobWaiters may, so reads keep connections while the lock is held
// exclusively, also by another process (a CLI verify or deletion). So a
// writer first tries the lock; only if that fails does it give its
// connection back, take a slot and wait in PostgreSQL with the blocking
// call. It calls release as soon as the lock is granted (or the wait
// failed): when the exclusive holder lets go, the next slot holder's call
// returns at once. PostgreSQL's queue stays the only source of truth for
// the lock; the slots only limit how many wait in it. release may be
// called more than once.
func (s *Store) blobWaitSlot(ctx context.Context) (release func(), err error) {
	s.blobParked.Add(1)
	select {
	case s.blobWait <- struct{}{}:
		// Counted as parked until here, then only in pg_locks.
		s.blobParked.Add(-1)
	case <-ctx.Done():
		s.blobParked.Add(-1)
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(func() { <-s.blobWait }) }, nil
}

// BlobWaiters returns how many writers wait for a wait slot of the blob
// lock (blobWaitSlot), without a connection. The one with the slot waits
// in PostgreSQL and is not counted. For tests and diagnostics.
func (s *Store) BlobWaiters() int {
	return int(s.blobParked.Load())
}

func (s *Store) tryLockSync(ctx context.Context, accountID int64, blobs bool) (unlock func(), ok bool, err error) {
	if accountID < 1 || accountID > math.MaxInt32 {
		return nil, false, fmt.Errorf("account id %d out of range for the sync lock", accountID)
	}
	key := int32(accountID) //nolint:gosec // range checked above
	conn, ok, err := s.lockAccount(ctx, key)
	if err != nil || !ok {
		return nil, false, err
	}
	if blobs {
		var got bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock_shared($1, 0)", blobLockClass).Scan(&got); err != nil {
			// A cancelled query leaves the connection unusable; close it,
			// which also releases the account lock.
			closeConn(ctx, conn)
			return nil, false, fmt.Errorf("try the blob lock: %w", err)
		}
		if !got {
			// Held exclusively: wait without the account lock and without
			// the connection, then start over with the blocking call.
			unlockSync(ctx, conn, key, false)
			release, err := s.blobWaitSlot(ctx)
			if err != nil {
				return nil, false, fmt.Errorf("wait for the blob lock: %w", err)
			}
			defer release()
			if conn, ok, err = s.lockAccount(ctx, key); err != nil || !ok {
				return nil, false, err
			}
			_, err = conn.Exec(ctx, "SELECT pg_advisory_lock_shared($1, 0)", blobLockClass)
			release()
			if err != nil {
				// A cancelled wait leaves the connection unusable; close it,
				// which also releases the account lock.
				closeConn(ctx, conn)
				return nil, false, fmt.Errorf("wait for the blob lock: %w", err)
			}
		}
	}
	return func() { unlockSync(ctx, conn, key, blobs) }, true, nil
}

// lockAccount acquires a connection and takes the account's sync lock on
// it. It returns false, and no connection, if another session holds it.
func (s *Store) lockAccount(ctx context.Context, key int32) (*pgxpool.Conn, bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", syncLockClass, key).Scan(&ok); err != nil {
		// The lock may have been taken before the query failed: close the
		// connection, so it cannot leak into the pool.
		closeConn(ctx, conn)
		return nil, false, err
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	return conn, true, nil
}

// unlockSync releases the account's sync lock, with blobs also the shared
// blob lock, and the connection. It unlocks even if ctx was cancelled; on
// failure it closes the connection, so no lock can leak into the pool.
func unlockSync(ctx context.Context, conn *pgxpool.Conn, key int32, blobs bool) {
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

// closeConn closes and releases a connection whose session state is
// unknown, which releases its session locks.
func closeConn(ctx context.Context, conn *pgxpool.Conn) {
	_ = conn.Conn().Close(context.WithoutCancel(ctx))
	conn.Release()
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

// TryLockAttest takes the attestation writer lock. It returns false if
// another session writes an attestation.
func (s *Store) TryLockAttest(ctx context.Context) (unlock func(), ok bool, err error) {
	return s.tryLockClass(ctx, attestWriterClass)
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
