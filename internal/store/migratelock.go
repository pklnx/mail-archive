package store

import (
	"context"
	"fmt"
	"sync"
)

// migrateLockClass is the first key of the advisory lock that lets one
// `migrate` (or `migrate down`) command run at a time ("mh" in ASCII); the
// second key is always 0. It covers the whole command: the index rebuild,
// the migrations, the audit log partitions and the password upgrade.
// goose's own lock (withMigrations) is a single bigint key, a different
// key space, and is always taken inside this one, so the order is fixed.
const migrateLockClass = 0x6d68

// LockMigrations takes the migration command lock on a dedicated
// connection and holds it until unlock is called; if the process dies,
// PostgreSQL releases it with the connection. If another session holds
// it, LockMigrations calls onWait (if set) and then waits for it without
// a time limit, because a first migration may rebuild indexes for many
// minutes. Cancelling ctx (Ctrl-C) ends the wait with an error.
//
// unlock must be called before the store is closed: closing the pool waits
// for the held connection. It may be called more than once.
func (s *Store) LockMigrations(ctx context.Context, onWait func()) (unlock func(), err error) {
	l, err := s.newLockConn(ctx)
	if err != nil {
		return nil, err
	}
	ok, err := l.tryLock(ctx, migrateLockClass, 0)
	if err != nil {
		// The lock may have been taken before the query failed: close the
		// connection, so it cannot leak into the pool.
		closeConn(ctx, l.conn)
		return nil, fmt.Errorf("take the migration lock: %w", err)
	}
	if !ok {
		if onWait != nil {
			onWait()
		}
		if _, err := l.conn.Exec(ctx, "SELECT pg_advisory_lock($1, 0)", migrateLockClass); err != nil {
			// A cancelled wait leaves the connection unusable; close it.
			closeConn(ctx, l.conn)
			return nil, fmt.Errorf("wait for the migration lock: %w", err)
		}
	}
	var once sync.Once
	return func() { once.Do(func() { l.Close(ctx) }) }, nil
}
