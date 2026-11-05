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

// runMa runs the repository's ./ma with a docker that only logs its
// arguments, and returns the exit code, the output and the docker calls.
func runMa(t *testing.T, args ...string) (int, string, []string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	ma, err := filepath.Abs("../../ma")
	if err != nil {
		t.Fatal(err)
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "docker.log")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(shim), 0o700); err != nil { //nolint:gosec // an executable test script
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	cmd := exec.CommandContext(ctx, ma, args...) //nolint:gosec // the repository's own script
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	data, _ := os.ReadFile(log)
	return code, out.String(), strings.Split(strings.TrimSpace(string(data)), "\n")
}

// TestMaRestoreDumpRouting: ./ma restore-dump --reconcile-only, which
// restore-dump prints and the docs give as the way back after a migrate or
// an interrupted restore, reaches the command; only a file goes through
// the restore (review round 2 of #518).
func TestMaRestoreDumpRouting(t *testing.T) {
	code, out, calls := runMa(t, "restore-dump", "--reconcile-only")
	last := calls[len(calls)-1]
	if code != 0 || !strings.Contains(last, " run --rm ") || !strings.HasSuffix(last, "mail-archive restore-dump --reconcile-only") {
		t.Errorf("--reconcile-only: exit %d\n%s\ndocker calls:\n%s", code, out, strings.Join(calls, "\n"))
	}

	code, out, calls = runMa(t, "restore-dump", filepath.Join(t.TempDir(), "missing.dump.enc"))
	if code != 2 || !strings.Contains(out, "no such file") || strings.Contains(strings.Join(calls, "\n"), "restore-dump") {
		t.Errorf("a missing file: exit %d\n%s\ndocker calls:\n%s", code, out, strings.Join(calls, "\n"))
	}

	if code, out, _ = runMa(t, "restore-dump"); code != 2 || !strings.Contains(out, "usage: ./ma restore-dump FILE") {
		t.Errorf("no argument: exit %d\n%s", code, out)
	}
}
