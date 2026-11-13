package store

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
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
// The lock is a session-level advisory lock on a pool connection of its
// own; a process that dies frees it with its connection.
type StorageWriter struct {
	s    *Store
	hold bool

	mu   sync.Mutex
	conn *pgxpool.Conn // held lock (hold), nil otherwise
}

// StorageWriter returns the writer lock: hold keeps it once taken (serve);
// otherwise each TryLock takes it until its unlock (a command).
func (s *Store) StorageWriter(hold bool) *StorageWriter {
	return &StorageWriter{s: s, hold: hold}
}

// TryLock takes the lock if it is free and returns whether this process
// may write transitions now. unlock is never nil: for a held lock it does
// nothing; otherwise it frees the lock. It never waits for the lock.
func (w *StorageWriter) TryLock(ctx context.Context) (unlock func(), ok bool, err error) {
	nothing := func() {}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn != nil {
		return nothing, true, nil
	}
	conn, err := w.s.pool.Acquire(ctx)
	if err != nil {
		return nothing, false, err
	}
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 0)", storageWriterClass).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nothing, false, err
	}
	if w.hold {
		w.conn = conn
		return nothing, true, nil
	}
	return func() { releaseStorageWriter(ctx, conn) }, true, nil
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

// releaseStorageWriter frees the writer lock and gives the connection back; a failed
// unlock closes the connection, which frees the lock too.
func releaseStorageWriter(ctx context.Context, conn *pgxpool.Conn) {
	bg := context.WithoutCancel(ctx)
	if _, err := conn.Exec(bg, "SELECT pg_advisory_unlock($1, 0)", storageWriterClass); err != nil {
		_ = conn.Conn().Close(bg)
	}
	conn.Release()
}
