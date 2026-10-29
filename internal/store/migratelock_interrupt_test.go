package store_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"testing"

	"github.com/pressly/goose/v3/lock"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// lockChildEnv makes the test binary act as the child of an interrupt
// test (lockInterruptChild). Its value is the URL of the database the
// parent created; lockChildKindEnv says which lock the child waits for.
const (
	lockChildEnv     = "MAIL_ARCHIVE_TEST_LOCK_CHILD_URL"
	lockChildKindEnv = "MAIL_ARCHIVE_TEST_LOCK_CHILD_KIND"
)

// The locks a child waits for.
const (
	childWaitsForCommandLock = "command" // LockMigrations
	childWaitsForGooseLock   = "goose"   // goose's lock in Migrate
)

// Marker lines the child prints on stdout.
const (
	childWaiting    = "waiting"      // onWait was called
	childReturned   = "returned: "   // the wait returned context.Canceled
	childUnexpected = "unexpected: " // the wait returned anything else
)

// lockInterruptChild is the child process of the interrupt tests. It
// models the CLI: it waits for a lock (LockMigrations, or goose's lock in
// Migrate) with a context that Ctrl-C cancels, and when the wait returns it
// ends at once. It never returns.
//
// It ends by killing itself, without os.Exit and without deferred
// cleanup: under the race detector, os.Exit gives the background cancel of
// pgx enough time to run and hides the leftover waiter of #255.
func lockInterruptChild(dbURL, kind string) {
	// One P: the background goroutine of pgx runs only when this one
	// blocks, so the child's end does not depend on how many idle cores
	// the machine has.
	runtime.GOMAXPROCS(1)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		fmt.Printf("%sopen the store: %v\n", childUnexpected, err)
	} else {
		onWait := func() { fmt.Println(childWaiting) }
		if kind == childWaitsForGooseLock {
			st.SetGooseLockWait(func(store.GooseLockHolder) { onWait() })
			_, err = st.Migrate(ctx)
		} else {
			_, err = st.LockMigrations(ctx, onWait)
		}
		if errors.Is(err, context.Canceled) {
			fmt.Printf("%s%v\n", childReturned, err)
		} else {
			fmt.Printf("%s%v\n", childUnexpected, err)
		}
	}
	p, err := os.FindProcess(os.Getpid())
	if err == nil {
		err = p.Kill()
	}
	if err != nil {
		fmt.Printf("%scannot kill itself: %v\n", childUnexpected, err)
		os.Exit(3)
	}
	select {} // until the kill arrives
}

// runLockChildIfAsked runs the test binary as lockInterruptChild when the
// parent started it as one. It must come before any storetest call: the
// child creates no database.
func runLockChildIfAsked() {
	if dbURL := os.Getenv(lockChildEnv); dbURL != "" {
		lockInterruptChild(dbURL, os.Getenv(lockChildKindEnv))
	}
}

// TestLockMigrationsInterruptLeavesNoWaiter (#255): when Ctrl-C ends the
// wait for the migration lock and the process exits right after the error,
// no backend is left queued for the lock. Without closeAndWaitCleanup the
// command exits before pgx has sent the cancel request, and the backend,
// which does not notice the closed socket, keeps waiting.
//
// The test starts itself as a child process (see lockInterruptChild) that
// waits for a lock the test holds, interrupts it, and then waits for the
// child to be gone and for the waiter to leave the queue. The test's own
// lock stays held, so a leftover waiter cannot disappear by getting the
// lock.
func TestLockMigrationsInterruptLeavesNoWaiter(t *testing.T) {
	runLockChildIfAsked()

	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	observer := connect(t, dbURL)

	unlock, err := st.LockMigrations(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)

	interruptLockChild(t, ctx, cancel, dbURL, childWaitsForCommandLock, func() (int, error) {
		return countMigrateLockWaiters(ctx, observer)
	})
}

// TestMigrateInterruptLeavesNoGooseWaiter (#253): the same as
// TestLockMigrationsInterruptLeavesNoWaiter for the wait for goose's lock
// in Migrate. Without discardConn in gooseLocker the command exits before
// pgx has sent the cancel request, and the backend keeps waiting for
// goose's lock.
func TestMigrateInterruptLeavesNoGooseWaiter(t *testing.T) {
	runLockChildIfAsked()

	// Unmigrated: goose takes its lock only when something is pending.
	dbURL := storetest.NewUnmigrated(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	observer := connect(t, dbURL)
	holder := connect(t, dbURL)
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}

	interruptLockChild(t, ctx, cancel, dbURL, childWaitsForGooseLock, func() (int, error) {
		return countGooseLockWaiters(ctx, observer)
	})
}

// interruptLockChild starts the test binary as a child that waits for the
// lock of the given kind, which the test holds; waiters counts the
// backends queued for that lock. Once the child's backend waits, it sends
// Ctrl-C, checks that the child's wait ended with the cancellation and
// that the child killed itself, and then waits for the waiter to leave the
// queue. cancel ends ctx; the test's cleanup calls it to stop the child.
func interruptLockChild(t *testing.T, ctx context.Context, cancel context.CancelFunc, dbURL, kind string, waiters func() (int, error)) {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), lockChildEnv+"="+dbURL, lockChildKindEnv+"="+kind)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// lines closes at the end of the child's output; exited then gets the
	// result of Wait (which must not run before the output is read).
	lines := make(chan string, 256)
	exited := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			default: // a flood of output: the markers come first
			}
		}
		close(lines)
		exited <- cmd.Wait()
	}()
	collected := false
	t.Cleanup(func() {
		cancel() // kills the child if it still runs
		if !collected {
			<-exited
		}
	})
	var seen []string
	// childFailed ends the test with everything the child printed.
	childFailed := func(format string, args ...any) {
		t.Helper()
		if !collected {
			select {
			case err := <-exited:
				collected = true
				seen = append(seen, fmt.Sprintf("(child ended: %v)", err))
			default:
			}
		}
		// stderr may be read only once the child has ended.
		errText := "(the child still runs)"
		if collected {
			errText = stderr.String()
		}
		t.Fatalf("%s\nchild stdout: %q\nchild stderr: %q", fmt.Sprintf(format, args...), strings.Join(seen, "\n"), errText)
	}
	// expect returns the next line that starts with one of the prefixes.
	expect := func(what string, prefixes ...string) string {
		t.Helper()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					<-exited // the output ended with the child
					collected = true
					seen = append(seen, "(child ended)")
					childFailed("the child ended before %s", what)
				}
				seen = append(seen, line)
				for _, p := range prefixes {
					if strings.HasPrefix(line, p) {
						return line
					}
				}
			case <-ctx.Done():
				childFailed("no %s before the deadline: %v", what, ctx.Err())
			}
		}
	}

	if line := expect("the child's wait", childWaiting, childUnexpected); line != childWaiting {
		childFailed("the child did not wait: %s", line)
	}
	// The child's backend is queued for the lock.
	for {
		n, err := waiters()
		if err != nil {
			childFailed("count the waiters for the lock: %v", err)
		}
		if n > 0 {
			break
		}
		select {
		case err := <-exited:
			collected = true
			seen = append(seen, fmt.Sprintf("(child ended: %v)", err))
			childFailed("the child ended before its backend waited")
		default:
		}
	}

	// Ctrl-C.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if line := expect("the end of the child's wait", childReturned, childUnexpected); !strings.HasPrefix(line, childReturned) {
		childFailed("the child's wait did not end with a cancellation: %s", line)
	}
	select {
	case err := <-exited:
		collected = true
		var exit *exec.ExitError
		// Killed by itself, not by the end of ctx: that would have no
		// marker line before it.
		if !errors.As(err, &exit) || exit.ExitCode() != -1 {
			t.Fatalf("the child did not end by its own kill: %v", err)
		}
	case <-ctx.Done():
		childFailed("the child did not end: %v", ctx.Err())
	}

	// The test still holds the lock, so a waiter that is left would stay.
	for {
		n, err := waiters()
		if ctx.Err() != nil {
			t.Fatalf("a backend still waits for the lock after its process exited: %v", ctx.Err())
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
}
