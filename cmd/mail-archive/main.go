// Command mail-archive copies messages from IMAP accounts into a local,
// deduplicated archive.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	sig, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A writer that must stop (lost writers lock, encryption at rest
	// changed) cancels this with a cause and exits non-zero (#392).
	ctx, cancel := context.WithCancelCause(sig)
	defer cancel(nil)
	stopProcess = cancel

	root := &cobra.Command{
		Use:           "mail-archive",
		Short:         "Archive IMAP mailboxes (read-only copy) into one deduplicated archive",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newMigrateCmd(),
		newPrepareDataCmd(),
		newKeygenCmd(),
		newAccountCmd(),
		newUserCmd(),
		newGroupCmd(),
		newAuditorCmd(),
		newSyncCmd(),
		newStatusCmd(),
		newReindexCmd(),
		newVerifyCmd(),
		newExportCmd(),
		newImportCmd(),
		newBackupCmd(),
		newRestoreDumpCmd(),
		newAttestCmd(),
		newRetentionCmd(),
		newHoldCmd(),
		newAuditCmd(),
		newNotifyCmd(),
		newServeCmd(),
		newWorkerCmd(),
		newJobsCmd(),
		newEncryptionCmd(),
		newDecryptCmd(),
		newSealStreamCmd(),
		newUnsealStreamCmd(),
	)
	code, msg := exitStatus(ctx, root.ExecuteContext(ctx))
	if msg != "" {
		fmt.Fprintln(os.Stderr, "error:", msg)
	}
	if code != 0 {
		os.Exit(code)
	}
}

// exitStatus is the exit code of a command that returned err, and the
// message to print. A writer stopped through the root context (#392) exits
// 1 also when its command returned no error, as serve does on a cancelled
// context.
func exitStatus(ctx context.Context, err error) (int, string) {
	var stopped *writerStopped
	if errors.As(context.Cause(ctx), &stopped) {
		return 1, stopped.Error()
	}
	if err == nil {
		return 0, ""
	}
	var exitErr *exitError
	if !errors.As(err, &exitErr) {
		return 1, err.Error()
	}
	return exitErr.exitCode(), ""
}

// exitError signals a failure that was already reported to the user. The
// process exits with code, or 1 when it is 0.
type exitError struct {
	msg  string
	code int
}

func (e *exitError) Error() string { return e.msg }

func (e *exitError) exitCode() int {
	if e.code == 0 {
		return 1
	}
	return e.code
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
