package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// TestCheckEnv (#585): old variable names in use give one WARN line that
// names them with their new names and no value, silent at log level error
// (under either name); none in use, no line; a variable under both names
// with two values is refused before anything is logged.
func TestCheckEnv(t *testing.T) {
	const secret = "c2VjcmV0LXZhbHVlLW5ldmVyLWxvZ2dlZA=="
	for _, tc := range []struct {
		desc    string
		environ []string
		want    string // the line, "" for none
		refused bool
	}{
		{"old names", []string{"MAIL_ARCHIVE_SECRET_KEY=" + secret, "MAIL_ARCHIVE_DATA_DIR=/srv", "PATH=/bin"},
			`level=WARN msg="old environment variable names in use: MAIL_ARCHIVE_DATA_DIR (now KEEPIA_ARCHIVE_DATA_DIR), ` +
				`MAIL_ARCHIVE_SECRET_KEY (now KEEPIA_ARCHIVE_SECRET_KEY); rename them to the new names. ` +
				`The old names keep working in 1.x and will be removed in a later major release"`, false},
		{"old and equal new name", []string{"MAIL_ARCHIVE_SECRET_KEY=" + secret, "KEEPIA_ARCHIVE_SECRET_KEY=" + secret},
			`MAIL_ARCHIVE_SECRET_KEY (now KEEPIA_ARCHIVE_SECRET_KEY); rename`, false},
		{"new names", []string{"KEEPIA_ARCHIVE_SECRET_KEY=" + secret, "MAIL_ARCHIVE_SECRET_KEY=", "MAIL_ARCHIVE_BENCH=1"}, "", false},
		{"error level, new name", []string{"MAIL_ARCHIVE_SECRET_KEY=" + secret, "KEEPIA_ARCHIVE_LOG_LEVEL=error"}, "", false},
		{"error level, old name", []string{"MAIL_ARCHIVE_SECRET_KEY=" + secret, "MAIL_ARCHIVE_LOG_LEVEL=error"}, "", false},
		{"warn level", []string{"MAIL_ARCHIVE_SECRET_KEY=" + secret, "KEEPIA_ARCHIVE_LOG_LEVEL=warn"}, `level=WARN msg="old environment variable names`, false},
		{"conflict", []string{"MAIL_ARCHIVE_SECRET_KEY=" + secret, "KEEPIA_ARCHIVE_SECRET_KEY=other", "MAIL_ARCHIVE_DATA_DIR=/srv"}, "", true},
	} {
		var stderr bytes.Buffer
		err := checkEnv(tc.environ, &stderr)
		var conflict *config.EnvConflictError
		if errors.As(err, &conflict) != tc.refused || (err != nil && !tc.refused) {
			t.Errorf("%s: error %v", tc.desc, err)
		}
		out := stderr.String()
		if tc.want == "" && out != "" || tc.want != "" && !strings.Contains(out, tc.want) {
			t.Errorf("%s: logged %q, want %q", tc.desc, out, tc.want)
		}
		if strings.Count(out, "\n") > 1 {
			t.Errorf("%s: more than one line: %q", tc.desc, out)
		}
		if strings.Contains(out, secret) || err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("%s: a value is shown", tc.desc)
		}
	}
	// The conflict comes out as a start refusal with its message.
	_, err := config.ScanEnv([]string{"MAIL_ARCHIVE_DATA_DIR=/a", "KEEPIA_ARCHIVE_DATA_DIR=/b"})
	if code, msg := exitStatus(context.Background(), err); code != exitStartRefused || !strings.Contains(msg, "remove MAIL_ARCHIVE_DATA_DIR") {
		t.Errorf("exit status %d %q", code, msg)
	}
}

// mainChildEnv makes the test binary run main() with the arguments after
// "--" as the child of TestMainEnvScan.
const mainChildEnv = "KEEPIA_ARCHIVE_TEST_MAIN_CHILD"

// TestMainEnvScan (#585): main scans the environment before cobra runs a
// command. With old names, `--version` writes the version to stdout and the
// warning once to stderr; with both names set to different values the
// process ends with exit code 4 before the command runs (migrate would
// otherwise fail on the missing database URL with exit code 1).
func TestMainEnvScan(t *testing.T) {
	if args, ok := strings.CutPrefix(os.Getenv(mainChildEnv), "run:"); ok {
		os.Args = append([]string{"mail-archive"}, strings.Fields(args)...)
		main()
		os.Exit(0)
	}
	run := func(args string, env ...string) (stdout, stderr string, code int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainEnvScan$") //nolint:gosec // the test binary itself
		cmd.Env = append(config.ChildEnv(os.Environ(), []string{"PATH", "HOME", "TMPDIR"}), mainChildEnv+"=run:"+args)
		cmd.Env = append(cmd.Env, env...)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			t.Fatal(err)
		}
		return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
	}

	stdout, stderr, code := run("--version", "MAIL_ARCHIVE_SYNC_INTERVAL=2h")
	if code != 0 || !strings.HasPrefix(stdout, "mail-archive version ") || strings.Contains(stdout, "MAIL_ARCHIVE") {
		t.Errorf("--version: %d\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	if strings.Count(stderr, "old environment variable names in use: MAIL_ARCHIVE_SYNC_INTERVAL (now KEEPIA_ARCHIVE_SYNC_INTERVAL)") != 1 {
		t.Errorf("warning not once on stderr: %q", stderr)
	}

	stdout, stderr, code = run("migrate", "MAIL_ARCHIVE_SYNC_INTERVAL=2h", "KEEPIA_ARCHIVE_SYNC_INTERVAL=3h")
	if code != exitStartRefused || stdout != "" || stderr != "error: KEEPIA_ARCHIVE_SYNC_INTERVAL and its old name MAIL_ARCHIVE_SYNC_INTERVAL are both set, "+
		"to different values: remove MAIL_ARCHIVE_SYNC_INTERVAL\n" {
		t.Errorf("conflict: %d\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	// Control: without the conflict, migrate runs and fails on the missing
	// database URL.
	_, stderr, code = run("migrate", "MAIL_ARCHIVE_SYNC_INTERVAL=2h")
	if code != 1 || !strings.Contains(stderr, "KEEPIA_ARCHIVE_DATABASE_URL is not set") {
		t.Errorf("control: %d %q", code, stderr)
	}
}
