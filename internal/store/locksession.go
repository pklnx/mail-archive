package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Lock sessions (#165, #166) hold session advisory locks on one dedicated
// connection and run their transactions on that connection too. A job that
// holds the blob lock exclusively must never need a second connection:
// writers waiting for the blob lock (SMTP deliveries, syncs, fills) each
// hold a pool connection while they wait, and there can be as many of them
// as the pool has connections. A job that waited for one of them would wait
// for ever.
//
// A session method runs its store calls with withConn(ctx, conn). Such a
// ctx must stay inside the method: it is never handed to a callback (an
// audit hook, a progress function) or to a goroutine, because a pgx
// connection must not be used by two callers at once. The session's
// methods are not safe for concurrent use either.

// lockConn is the dedicated connection of a lock session and its locks.
type lockConn struct {
	s    *Store
	conn *pgxpool.Conn
}

// newLockConn acquires a dedicated connection.
func (s *Store) newLockConn(ctx context.Context) (*lockConn, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &lockConn{s: s, conn: conn}, nil
}

// startLockConn acquires a dedicated connection and takes the session lock
// (class, 0) on it. It returns false, and no connection, if another session
// holds the lock.
func (s *Store) startLockConn(ctx context.Context, class int32) (*lockConn, bool, error) {
	l, err := s.newLockConn(ctx)
	if err != nil {
		return nil, false, err
	}
	ok, err := l.tryLock(ctx, class, 0)
	if err != nil {
		// A cancelled query leaves the connection unusable.
		_ = l.conn.Conn().Close(context.WithoutCancel(ctx))
		l.conn.Release()
		return nil, false, err
	}
	if !ok {
		l.conn.Release()
		return nil, false, nil
	}
	return l, true, nil
}

// tryLock takes the session lock (class, key) on the connection, without
// waiting.
func (l *lockConn) tryLock(ctx context.Context, class, key int32) (bool, error) {
	var ok bool
	err := l.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", class, key).Scan(&ok)
	return ok, err
}

// on returns ctx for the store calls of a session method: they run on the
// session's connection. It must not leave the method (see above).
func (l *lockConn) on(ctx context.Context) context.Context {
	return withConn(ctx, l.conn)
}

// TryLockBlobs takes the blob lock exclusively on the session's
// connection, without waiting. It returns false while a writer holds it.
func (l *lockConn) TryLockBlobs(ctx context.Context) (bool, error) {
	return l.tryLock(ctx, blobLockClass, 0)
}

// UnlockBlobs releases the blob lock taken by TryLockBlobs.
func (l *lockConn) UnlockBlobs(ctx context.Context) error {
	_, err := l.conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1, 0)", blobLockClass)
	return err
}

// Close releases every lock of the session and its connection. If the
// locks cannot be released, the connection is closed, which releases
// them.
func (l *lockConn) Close(ctx context.Context) {
	bg := context.WithoutCancel(ctx)
	if _, err := l.conn.Exec(bg, "SELECT pg_advisory_unlock_all()"); err != nil {
		_ = l.conn.Conn().Close(bg)
	}
	l.conn.Release()
}

// connKey marks a context whose store calls run on a dedicated connection
// instead of the pool.
type connKey struct{}

func withConn(ctx context.Context, c *pgxpool.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

func connFrom(ctx context.Context) *pgxpool.Conn {
	c, _ := ctx.Value(connKey{}).(*pgxpool.Conn)
	return c
}

// qFor returns the queries for a call outside a transaction: on the
// dedicated connection of ctx (withConn), or on the pool.
func (s *Store) qFor(ctx context.Context) *db.Queries {
	if c := connFrom(ctx); c != nil {
		return db.New(c)
	}
	return s.q
}
