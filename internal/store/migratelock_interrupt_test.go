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

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// lockChildEnv makes the test binary act as the child of
// TestLockMigrationsInterruptLeavesNoWaiter. Its value is the URL of the
// database the parent created.
const lockChildEnv = "MAIL_ARCHIVE_TEST_LOCK_CHILD_URL"

// Marker lines the child prints on stdout.
const (
	childWaiting    = "waiting"      // onWait was called
	childReturned   = "returned: "   // LockMigrations returned context.Canceled
	childUnexpected = "unexpected: " // LockMigrations returned anything else
)

// lockInterruptChild is the child process of
// TestLockMigrationsInterruptLeavesNoWaiter. It models the CLI: it waits
// for the migration lock with a context that Ctrl-C cancels, and when
// LockMigrations returns it ends at once. It never returns.
//
// It ends by killing itself, without os.Exit and without deferred
// cleanup: under the race detector, os.Exit gives the background cancel of
// pgx enough time to run and hides the leftover waiter of #255.
func lockInterruptChild(dbURL string) {
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
		_, err = st.LockMigrations(ctx, func() { fmt.Println(childWaiting) })
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
	if dbURL := os.Getenv(lockChildEnv); dbURL != "" {
		lockInterruptChild(dbURL) // before any storetest call: it creates no database
	}

	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	observer := connect(t, dbURL)

	unlock, err := st.LockMigrations(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockMigrationsInterruptLeavesNoWaiter$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), lockChildEnv+"="+dbURL)
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
		t.Fatalf("%s\nchild stdout: %q\nchild stderr: %q", fmt.Sprintf(format, args...), strings.Join(seen, "\n"), stderr.String())
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
	for waitingForMigrateLock(t, ctx, observer) < 1 {
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
		n, err := countMigrateLockWaiters(ctx, observer)
		if ctx.Err() != nil {
			t.Fatalf("a backend still waits for the migration lock after its process exited: %v", ctx.Err())
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
}
