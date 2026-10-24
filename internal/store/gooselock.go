package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3/lock"
)

// GooseLockHolder is the session that holds goose's lock while Migrate or
// MigrateDown waits for it.
type GooseLockHolder struct {
	// PID is the holder's backend process ID, 0 if the lock was released
	// before it could be looked up.
	PID int64
	// State is the holder's state in pg_stat_activity ("active", "idle",
	// ...), empty if this role may not see it (a session of another role).
	State string
	// ActiveFor is how long the holder's current statement has run; it is
	// set only when State is "active".
	ActiveFor time.Duration
}

// gooseLocker is goose's session lock (lock.DefaultLockID, a single bigint
// advisory lock) on the connection that runs the migrations, the same lock
// as goose's own Postgres session locker, so it excludes older binaries
// too. Unlike goose's, which retries silently for 5 minutes and then fails,
// it calls onWait once with the holder and then waits without a time limit;
// cancelling the context (Ctrl-C) ends the wait.
//
// Usually the holder is a statement of an earlier migrate that was killed:
// the server runs it to its end, or until it notices that the client is
// gone (see ForMigrations), and holds the lock until then. The command
// lock (LockMigrations) does not cover this, because PostgreSQL released
// it at once with the killed command's own connection.
//
// Known limit: statements of a killed migrate outside goose's lock (the
// index rebuild, the audit log partitions, the password upgrade) are not
// waited for. Once the command lock is free, a new command can run next to
// such a statement; ForMigrations shortens that window to about 10 s.
type gooseLocker struct {
	onWait func(GooseLockHolder)
}

var _ lock.SessionLocker = gooseLocker{}

// gooseLockHolderQuery finds the session that holds the advisory lock $1
// (a single bigint key: classid holds its upper and objid its lower 32
// bits, objsubid is 1) in the current database. state and query_start are
// NULL for a session of another role (and query is "<insufficient
// privilege>", which is never read). It is a variable for a test only.
var gooseLockHolderQuery = `SELECT l.pid, a.state,
		EXTRACT(epoch FROM clock_timestamp() - a.query_start)::float8
	FROM pg_locks l LEFT JOIN pg_stat_activity a ON a.pid = l.pid
	WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
	  AND l.classid::bigint = $1::bigint >> 32 AND l.objid::bigint = $1::bigint & 4294967295
	  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
	LIMIT 1`

// SessionLock takes goose's lock on conn. If another session holds it, it
// calls onWait with that session and waits for the lock. The lookup of the
// holder only feeds the message: if it fails, onWait gets an empty holder
// and the wait goes on, unless ctx has ended.
//
// On an error it closes the connection instead of giving it back to the
// pool: the lock may have been taken before the error, and a cancelled
// wait must not leave a backend queued for the lock after the command
// exits (closeAndWaitCleanup, #255).
func (l gooseLocker) SessionLock(ctx context.Context, conn *sql.Conn) error {
	var ok bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lock.DefaultLockID).Scan(&ok); err != nil {
		discardConn(context.WithoutCancel(ctx), conn)
		return fmt.Errorf("take goose's migration lock: %w", err)
	}
	if ok {
		return nil
	}
	if l.onWait != nil {
		h, err := gooseLockHolder(ctx, conn)
		if err != nil && ctx.Err() != nil {
			// Cancelled (Ctrl-C): end here, with the cleanup of a
			// cancelled query.
			discardConn(context.WithoutCancel(ctx), conn)
			return fmt.Errorf("look up the holder of goose's migration lock: %w", err)
		}
		if err != nil {
			h = GooseLockHolder{} // wait anyway, without the holder's details
		}
		l.onWait(h)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
		discardConn(context.WithoutCancel(ctx), conn)
		return fmt.Errorf("wait for goose's migration lock: %w", err)
	}
	return nil
}

// SessionUnlock releases goose's lock on conn. If that fails, it closes
// the connection, which releases the lock, instead of giving it back to
// the pool with the lock.
func (gooseLocker) SessionUnlock(ctx context.Context, conn *sql.Conn) error {
	var ok bool
	err := conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", lock.DefaultLockID).Scan(&ok)
	if err == nil && !ok {
		err = errors.New("the lock was not held")
	}
	if err != nil {
		discardConn(context.WithoutCancel(ctx), conn)
		return fmt.Errorf("release goose's migration lock: %w", err)
	}
	return nil
}

// gooseLockHolder looks up the session that holds goose's lock.
func gooseLockHolder(ctx context.Context, conn *sql.Conn) (GooseLockHolder, error) {
	var (
		h      GooseLockHolder
		state  sql.NullString
		active sql.NullFloat64
	)
	err := conn.QueryRowContext(ctx, gooseLockHolderQuery, lock.DefaultLockID).Scan(&h.PID, &state, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return GooseLockHolder{}, nil // released meanwhile
	}
	if err != nil {
		return GooseLockHolder{}, err
	}
	h.State = state.String
	if h.State == "active" && active.Valid {
		h.ActiveFor = time.Duration(active.Float64 * float64(time.Second))
	}
	return h, nil
}

// discardConn closes the pool connection behind conn and waits until pgx
// has finished closing it (closeAndWaitCleanup). The pool drops a closed
// connection when goose gives it back.
//
// It does nothing when database/sql has already closed conn itself: the
// stdlib driver returns driver.ErrBadConn for a query on a pgx connection
// that is already closed (for example after a cancelled query), and Raw
// then returns sql.ErrConnDone. That is fine: such a connection holds no
// lock any more once the server sees it closed, pgx has already started
// its own close (cancel request included) in the background, and the
// pool's destructor waits for that cleanup when the store is closed. A
// cancelled wait for the lock itself never gets here with a closed conn:
// the failing query is the one that cancelled it, and pgx returns the
// context's error for it, not driver.ErrBadConn.
func discardConn(ctx context.Context, conn *sql.Conn) {
	_ = conn.Raw(func(driverConn any) error {
		if c, ok := driverConn.(*stdlib.Conn); ok {
			closeAndWaitCleanup(ctx, c.Conn())
		}
		return nil
	})
}
