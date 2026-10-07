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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := &cobra.Command{
		Use:           "mail-archive",
		Short:         "Archive IMAP mailboxes (read-only copy) into one deduplicated archive",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newMigrateCmd(),
		newKeygenCmd(),
		newAccountCmd(),
		newUserCmd(),
		newSyncCmd(),
		newStatusCmd(),
		newReindexCmd(),
		newVerifyCmd(),
		newExportCmd(),
		newImportCmd(),
		newBackupCmd(),
		newServeCmd(),
	)
	if err := root.ExecuteContext(ctx); err != nil {
		var exitErr *exitError
		if !errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		os.Exit(exitErr.exitCode())
	}
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
