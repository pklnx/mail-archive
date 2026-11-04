package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// writersLockClass is the first key of the writers lock ("mw" in ASCII);
// the second key is always 0. Every process that can write message files
// (serve, worker, sync, import, fill, reindex) holds it shared for its
// whole run; the offline conversions of encryption at rest (`encryption
// enable`, `disable`, `rekey`) take it exclusively with a try-lock, so
// they run only while no writer runs (#392, maintainer decision 5).
const writersLockClass = 0x6d77

// WriterAppName is the application_name of a writer's lock connection, so
// that a refused encryption command can name the writers it found.
func WriterAppName(command string) string { return "mail-archive writer " + command }

// encryptionAppName is the application_name of an encryption command's
// lock connection.
func encryptionAppName(command string) string { return "mail-archive encryption " + command }

// ErrWritersExcluded means an encryption command holds the writers lock
// exclusively: no writer may start until it has finished.
var ErrWritersExcluded = errors.New("an encryption change is running; start again when it has finished")

// ErrWritersRunning means writers hold the writers lock, so an encryption
// command cannot take it.
var ErrWritersRunning = errors.New("writers are running; stop the web server and the worker first (`docker compose stop web worker`)")

// ErrWritersLockLost means the writers lock's connection failed: the lock
// may be gone, so the process must stop writing.
var ErrWritersLockLost = errors.New("lost the connection that holds the writers lock")

// WritersLock is a session advisory lock on a dedicated connection outside
// the pool (as the migration lock), held shared by a writer or exclusively
// by an encryption command. A session lock ends with its session, not with
// the process, so a writer watches its connection (Watch) and stops when
// it fails (revision 5, B1 (a), (b)).
type WritersLock struct {
	conn *pgx.Conn

	mu       sync.Mutex // serializes the use of conn
	closed   bool
	lost     chan struct{}
	lostOnce sync.Once
	lostErr  error
	stop     chan struct{}
	watching sync.WaitGroup
}

// writersConn opens the dedicated connection of a writers lock: the pool's
// settings, no idle_session_timeout, the ForMigrations keepalives and
// connection check, and application_name.
func (s *Store) writersConn(ctx context.Context, appName string) (*pgx.Conn, error) {
	cfg := s.pool.Config().ConnConfig
	cfg.RuntimeParams["idle_session_timeout"] = "0"
	cfg.RuntimeParams["application_name"] = appName
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect for the writers lock: %w", err)
	}
	var oc openConfig
	ForMigrations()(&oc)
	for _, set := range oc.settings {
		// Best effort, as in Open.
		if _, err := conn.Exec(ctx, set); err != nil && conn.IsClosed() {
			return nil, fmt.Errorf("connect for the writers lock: %w", err)
		}
	}
	return conn, nil
}

// HoldWriters takes the writers lock shared for the writer command (its
// name goes into application_name). It fails with ErrWritersExcluded,
// naming the holder, while an encryption command holds it.
func (s *Store) HoldWriters(ctx context.Context, command string) (*WritersLock, error) {
	return s.takeWriters(ctx, WriterAppName(command), true)
}

// TryLockWritersExclusive takes the writers lock exclusively for an
// encryption command, without waiting. While writers hold it, it fails
// with ErrWritersRunning and names them by application_name.
func (s *Store) TryLockWritersExclusive(ctx context.Context, command string) (*WritersLock, error) {
	return s.takeWriters(ctx, encryptionAppName(command), false)
}

func (s *Store) takeWriters(ctx context.Context, appName string, shared bool) (*WritersLock, error) {
	conn, err := s.writersConn(ctx, appName)
	if err != nil {
		return nil, err
	}
	bg := context.WithoutCancel(ctx)
	fn := "pg_try_advisory_lock"
	if shared {
		fn = "pg_try_advisory_lock_shared"
	}
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT "+fn+"($1, 0)", writersLockClass).Scan(&ok); err != nil {
		closeAndWaitCleanup(bg, conn)
		return nil, fmt.Errorf("take the writers lock: %w", err)
	}
	if !ok {
		holders, herr := writersHolders(ctx, conn)
		closeAndWaitCleanup(bg, conn)
		if herr != nil {
			return nil, herr
		}
		if shared {
			return nil, fmt.Errorf("%w (held by %s)", ErrWritersExcluded, strings.Join(holders, ", "))
		}
		return nil, fmt.Errorf("%w (held by %s)", ErrWritersRunning, strings.Join(holders, ", "))
	}
	return &WritersLock{conn: conn, lost: make(chan struct{}), stop: make(chan struct{})}, nil
}

// writersHolders names the other sessions that hold the writers lock, by
// application_name and backend pid. A session that holds it without being
// a writer (an idle connection of a crashed host, until its keepalives
// end it) shows up here too.
func writersHolders(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT coalesce(nullif(a.application_name, ''), '(no application_name)'), l.pid, l.mode
		FROM pg_locks l LEFT JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.locktype = 'advisory' AND l.granted AND l.classid = $1 AND l.objid = 0 AND l.objsubid = 2
		  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND l.pid <> pg_backend_pid()
		ORDER BY l.pid`, writersLockClass)
	if err != nil {
		return nil, fmt.Errorf("list the holders of the writers lock: %w", err)
	}
	var out []string
	for rows.Next() {
		var name, mode string
		var pid int32
		if err := rows.Scan(&name, &pid, &mode); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%q (pid %d)", name, pid))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		// Released between the try and the list.
		out = []string{"a session that let go meanwhile; try again"}
	}
	return out, nil
}

// WritersPing is how often a writer checks its lock connection.
const WritersPing = 10 * time.Second

// Watch checks the lock's connection at every tick (a time.Ticker's C in
// production, WritersPing) and, when the check fails, closes Lost and
// calls onLost once with an error wrapping ErrWritersLockLost. It returns
// at once; the check runs in a goroutine until Release.
func (w *WritersLock) Watch(tick <-chan time.Time, onLost func(error)) {
	w.watching.Go(func() {
		for {
			select {
			case <-w.stop:
				return
			case <-tick:
			}
			if err := w.ping(); err != nil {
				w.markLost(err, onLost)
				return
			}
		}
	})
}

func (w *WritersLock) ping() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var held bool
	// The lock lives as long as this session; the query also proves that
	// the session still holds it.
	err := w.conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND granted
		AND pid = pg_backend_pid() AND classid = $1 AND objid = 0 AND objsubid = 2)`, writersLockClass).Scan(&held)
	if err == nil && !held {
		err = errors.New("the session no longer holds the lock")
	}
	return err
}

func (w *WritersLock) markLost(err error, onLost func(error)) {
	w.lostOnce.Do(func() {
		w.lostErr = fmt.Errorf("%w: %w", ErrWritersLockLost, err)
		close(w.lost)
		if onLost != nil {
			onLost(w.lostErr)
		}
	})
}

// Lost is closed once Watch found the lock's connection failed.
func (w *WritersLock) Lost() <-chan struct{} { return w.lost }

// Release stops the watch and releases the lock; closing the connection
// releases it in any case. It may be called more than once.
func (w *WritersLock) Release() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.stop)
	w.mu.Unlock()
	w.watching.Wait()
	bg := context.Background()
	_, _ = w.conn.Exec(bg, "SELECT pg_advisory_unlock_all()")
	_ = w.conn.Close(bg)
}

// BackendPID is the PostgreSQL backend pid of the lock's connection, as
// pg_stat_activity shows it.
func (w *WritersLock) BackendPID() uint32 { return w.conn.PgConn().PID() }

// TryBlobLock takes the blob lock exclusively on the writers lock's own
// connection, without waiting, for an encryption command that holds the
// writers lock exclusively (#392, part 3): verify, the sweep, purges and
// retention then cannot run alongside it. It returns false while another
// session holds the blob lock. Release releases it with the writers lock.
// No pool connection is held for it, so the pool stays free for the
// command's own queries.
func (w *WritersLock) TryBlobLock(ctx context.Context) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false, errors.New("the writers lock is released")
	}
	var ok bool
	if err := w.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 0)", blobLockClass).Scan(&ok); err != nil {
		return false, fmt.Errorf("take the blob lock: %w", err)
	}
	return ok, nil
}
