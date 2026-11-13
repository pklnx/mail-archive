package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
)

// storageWriterClass is the advisory lock of the storage state writer
// ("ms" in ASCII); the second key is always 0.
const storageWriterClass = 0x6d73

// StorageWriter is the lock that decides which process writes the
// transitions of the quota states (#397): serve holds it for its whole
// run (hold), and a command takes it for one read only if no serve runs.
// So while serve runs, a command with other limits (a newer .env before
// the web server restarts) never writes a state; without serve, the
// commands write them, and ./ma sync sends the storage alerts at its end.
//
// The lock is a session-level advisory lock on a connection of its own,
// outside the pool, as LockMigrations does: serve holds it for its whole
// run, and a pool connection kept for that long would leave the rest of
// serve one connection short (the default pool has 4 on 2 cores). A
// process that dies frees it with its connection. serve checks the held
// lock at every TryLock and takes it again when its connection ended (a
// PostgreSQL restart, a terminated backend).
type StorageWriter struct {
	s    *Store
	hold bool

	mu   sync.Mutex
	conn *pgx.Conn // the held lock (hold), nil otherwise
	// lost counts the times a held lock was found gone (for tests and
	// the log).
	lost int
}

// StorageWriter returns the writer lock: hold keeps it once taken (serve);
// otherwise each TryLock takes it until its unlock (a command).
func (s *Store) StorageWriter(hold bool) *StorageWriter {
	return &StorageWriter{s: s, hold: hold}
}

// TryLock takes the lock if it is free and returns whether this process
// may write transitions now. unlock is never nil: for a held lock it does
// nothing; otherwise it frees the lock. It never waits for the lock. A
// held lock is checked first; if its session is gone, it is taken again
// (lost reports that).
func (w *StorageWriter) TryLock(ctx context.Context) (unlock func(), ok bool, err error) {
	nothing := func() {}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn != nil {
		if w.stillHeld(ctx) {
			return nothing, true, nil
		}
		w.lost++
		closeAndWaitCleanup(context.WithoutCancel(ctx), w.conn)
		w.conn = nil
	}
	cfg := w.s.pool.Config().ConnConfig
	// The session idles while it holds the lock; no idle_session_timeout
	// may end it.
	cfg.RuntimeParams["idle_session_timeout"] = "0"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nothing, false, fmt.Errorf("connect for the storage state writer lock: %w", err)
	}
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 0)", storageWriterClass).Scan(&ok); err != nil {
		// The lock may have been taken before the query failed: closing
		// the connection releases it.
		closeAndWaitCleanup(context.WithoutCancel(ctx), conn)
		return nothing, false, fmt.Errorf("take the storage state writer lock: %w", err)
	}
	if !ok {
		_ = conn.Close(context.WithoutCancel(ctx))
		return nothing, false, nil
	}
	if w.hold {
		w.conn = conn
		return nothing, true, nil
	}
	var once sync.Once
	return func() { once.Do(func() { releaseStorageWriter(ctx, conn) }) }, true, nil
}

// stillHeld reports whether the held lock's session still holds it.
func (w *StorageWriter) stillHeld(ctx context.Context) bool {
	if w.conn.IsClosed() {
		return false
	}
	var held bool
	err := w.conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks
		WHERE pid = pg_backend_pid() AND locktype = 'advisory' AND granted
		  AND classid = $1 AND objid = 0 AND objsubid = 2)`, storageWriterClass).Scan(&held)
	return err == nil && held
}

// Lost returns how often a held lock was found gone and taken again.
func (w *StorageWriter) Lost() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lost
}

// Close frees a held lock.
func (w *StorageWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn != nil {
		releaseStorageWriter(context.Background(), w.conn)
		w.conn = nil
	}
}

// releaseStorageWriter frees the writer lock and closes its connection;
// closing frees the lock too if the unlock fails.
func releaseStorageWriter(ctx context.Context, conn *pgx.Conn) {
	bg := context.WithoutCancel(ctx)
	_, _ = conn.Exec(bg, "SELECT pg_advisory_unlock_all()")
	_ = conn.Close(bg)
}
