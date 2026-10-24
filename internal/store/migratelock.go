package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
)

// migrateLockClass is the first key of the advisory lock that lets one
// `migrate` (or `migrate down`) command run at a time ("mh" in ASCII); the
// second key is always 0. It covers the whole command: for `migrate` the
// index rebuild, the migrations, the audit log partitions and the password
// upgrade; for `migrate down` its index rebuild check, the audit log
// partitions and the rollback. goose's own lock (withMigrations) is a
// single bigint key, a different key space, and is always taken inside
// this one, so the order is fixed.
const migrateLockClass = 0x6d68

// LockMigrations takes the migration command lock on a connection of its
// own, outside the pool, and holds it until unlock is called; if the
// process dies, PostgreSQL releases it with the connection. If another
// session holds it, LockMigrations calls onWait (if set) and then waits
// for it without a time limit, because a first migration may rebuild
// indexes for many minutes. Cancelling ctx (Ctrl-C) ends the wait with an
// error.
//
// The connection is not taken from the pool, so a pool of one connection
// (pool_max_conns=1) still serves the command, and closing the store does
// not wait for it. unlock may be called more than once.
func (s *Store) LockMigrations(ctx context.Context, onWait func()) (unlock func(), err error) {
	// Config returns a copy: the pool's host, TLS, credentials and runtime
	// parameters, without the pool-only settings.
	cfg := s.pool.Config().ConnConfig
	// The session idles while it holds the lock; no idle_session_timeout may end it.
	cfg.RuntimeParams["idle_session_timeout"] = "0"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect for the migration lock: %w", err)
	}
	bg := context.WithoutCancel(ctx)
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 0)", migrateLockClass).Scan(&ok); err != nil {
		// The lock may have been taken before the query failed: closing
		// the connection releases it.
		_ = conn.Close(bg)
		return nil, fmt.Errorf("take the migration lock: %w", err)
	}
	if !ok {
		if onWait != nil {
			onWait()
		}
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1, 0)", migrateLockClass); err != nil {
			// A cancelled wait leaves the connection unusable; close it.
			_ = conn.Close(bg)
			return nil, fmt.Errorf("wait for the migration lock: %w", err)
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// The explicit unlock frees the lock before unlock returns. If
			// it fails, closing the connection releases the lock.
			_, _ = conn.Exec(bg, "SELECT pg_advisory_unlock_all()")
			_ = conn.Close(bg)
		})
	}, nil
}
