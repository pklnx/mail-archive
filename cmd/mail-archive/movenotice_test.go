package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/testwait"
)

// moveNoticeArgsEnv makes the test binary run main with the arguments in
// it (separated by spaces), as the mail-archive binary would.
const moveNoticeArgsEnv = "MA_TEST_MOVE_NOTICE_ARGS"

// TestMoveNoticeMain is the child process of TestMoveNotice: it runs main,
// which exits the process itself.
func TestMoveNoticeMain(t *testing.T) {
	args := os.Getenv(moveNoticeArgsEnv)
	if args == "" {
		t.Skip("child process of TestMoveNotice")
	}
	os.Args = append([]string{"mail-archive"}, strings.Fields(args)...)
	main()
	os.Exit(0)
}

// runMain runs main in a child process with args and only the environment
// in env, and returns its exit code, stdout and stderr.
func runMain(t *testing.T, env []string, args string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMoveNoticeMain$") //nolint:gosec // the test binary itself
	cmd.Env = append([]string{moveNoticeArgsEnv + "=" + args, "PATH=" + os.Getenv("PATH")}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

// TestMoveNotice: serve and worker log the move once at start, as one WARN
// line on stderr; other commands stay silent (#585, part 8). serve stops at
// the missing secret key and worker at MAIL_ARCHIVE_JOB_WORKERS=0, both
// before any database connection.
func TestMoveNotice(t *testing.T) {
	dir := t.TempDir()
	env := []string{
		// Port 1 refuses at once; only migrate status gets that far.
		"MAIL_ARCHIVE_DATABASE_URL=postgres://none@127.0.0.1:1/none?sslmode=disable&connect_timeout=5",
		"MAIL_ARCHIVE_DATA_DIR=" + filepath.Join(dir, "data"),
		"MAIL_ARCHIVE_JOB_WORKERS=0",
		"HOME=" + dir,
	}
	for _, tc := range []struct {
		args     string
		wantCode int
		notice   bool
	}{
		{"serve", exitStartRefused, true},
		{"worker", 1, true},
		{"keygen", 0, false},
		{"--version", 0, false},
		{"migrate status", 1, false},
	} {
		t.Run(tc.args, func(t *testing.T) {
			code, stdout, stderr := runMain(t, env, tc.args)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d; stderr:\n%s", code, tc.wantCode, stderr)
			}
			if strings.Contains(stdout, "keepia") {
				t.Errorf("notice on stdout:\n%s", stdout)
			}
			n := strings.Count(stderr, "keepia Archive")
			if !tc.notice {
				if n != 0 {
					t.Errorf("notice in a command other than serve and worker:\n%s", stderr)
				}
				return
			}
			if n != 1 {
				t.Fatalf("notice %d times, want once; stderr:\n%s", n, stderr)
			}
			var line string
			for _, l := range strings.Split(stderr, "\n") {
				if strings.Contains(l, "keepia Archive") {
					line = l
				}
			}
			for _, want := range []string{
				"level=WARN",
				"ghcr.io/keepia/archive",
				"github.com/keepia/archive",
				"no releases after 1.6.1",
				"https://github.com/pklnx/mail-archive/releases/tag/v1.6.1",
			} {
				if !strings.Contains(line, want) {
					t.Errorf("notice lacks %q: %s", want, line)
				}
			}
			if strings.Contains(stderr, "127.0.0.1:1") || strings.Contains(stderr, dir) {
				t.Errorf("stderr shows the configuration:\n%s", stderr)
			}
		})
	}
}
