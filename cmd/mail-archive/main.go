// Command mail-archive copies messages from IMAP accounts into a local,
// deduplicated archive.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/keepia/archive/internal/config"
)

var version = "dev"

// defaultDataDir is the data directory when KEEPIA_ARCHIVE_DATA_DIR is not
// set. The image sets /data at build time (-X main.defaultDataDir=/data),
// so that it names no variable that `docker run -e` could conflict with
// (#585).
var defaultDataDir = "./data"

func main() {
	config.SetDefaultDataDir(defaultDataDir)
	// Before any command: refuse a variable set under its new and its old
	// name to different values, and warn once about old names (#585).
	if err := checkEnv(os.Environ(), os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitStartRefused)
	}
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
		newSecretKeyCmd(),
		newSetupCmd(),
		newAccountCmd(),
		newUserCmd(),
		newGroupCmd(),
		newAuditorCmd(),
		newSyncCmd(),
		newStatusCmd(),
		newUsageCmd(),
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
	// A failed migration or a refused start-up check (#396).
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code, err.Error()
	}
	// A refused secret key (#396): its configuration or its check.
	var keyErr *config.SecretKeyError
	if errors.As(err, &keyErr) {
		return exitStartRefused, err.Error()
	}
	// A variable set under both names to different values (#585).
	var conflictErr *config.EnvConflictError
	if errors.As(err, &conflictErr) {
		return exitStartRefused, err.Error()
	}
	// A refused setup token (#396, part 3).
	var tokenErr *config.SetupTokenError
	if errors.As(err, &tokenErr) {
		return exitStartRefused, err.Error()
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

func newLogger(level string) *slog.Logger { return newLoggerTo(os.Stderr, level) }

// newLoggerTo is newLogger writing to w.
func newLoggerTo(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l}))
}

// checkEnv scans environ once against the variables' old names (#585),
// before cobra runs a command, so that it does not depend on when a
// command reads its configuration: a variable set under its new and its
// old name to different values is refused (*config.EnvConflictError);
// old names in use get one WARN line on stderr through the logger, at the
// log level the environment sets (silent at error). Never values.
func checkEnv(environ []string, stderr io.Writer) error {
	inUse, err := config.ScanEnv(environ)
	if err != nil {
		return err
	}
	if len(inUse) > 0 {
		level := config.EnvValue(environ, config.EnvLogLevel)
		newLoggerTo(stderr, level).Warn(config.DeprecationWarning(inUse))
	}
	return nil
}
