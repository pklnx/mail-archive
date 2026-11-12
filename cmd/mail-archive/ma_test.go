package main

import (
	"bytes"
	"context"
	"encoding/base64"
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
	// The holder's call also logs the keyring copy ./ma passes to it.
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n" +
		"case \"$*\" in *' --hold '*) printf 'keyring-copy=%s\\n' \"${MAIL_ARCHIVE_KEYRING_COPY:-}\" >> '" + log + "' ;; esac\nexit 0\n"
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

	if code, out, _ = runMa(t, "restore-dump"); code != 2 || !strings.Contains(out, "usage: ./ma restore-dump [--plaintext] FILE") {
		t.Errorf("no argument: exit %d\n%s", code, out)
	}
}

// TestMaRestoreDumpFacts: ./ma restore-dump decides nothing about a dump
// without encryption; it passes the facts to the holder, which refuses
// before "ready" (TestRestoreDumpHoldRefusesPlaintext): the dump's name,
// its header as read, --plaintext before or after FILE, and the keyring
// copy next to it, for a dump without encryption too
// (GHSA-w6jg-ffvw-q4f5). The shim's holder never says "ready", so no
// pg_restore follows.
func TestMaRestoreDumpFacts(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	plain := write("mailarchive-T.dump.enc", "PGDMP not encrypted")
	write("mailarchive-T.keyring", "the keyring copy")
	sealed := write("s.dump.enc", "MA-ENC1 encrypted")
	copyB64 := base64.StdEncoding.EncodeToString([]byte("the keyring copy"))

	for _, c := range []struct {
		name    string
		args    []string
		want    string // the holder's arguments
		keyring string // the keyring copy passed to it
	}{
		{"plain named .dump.enc with a keyring copy", []string{plain},
			"restore-dump --hold --dump-name=mailarchive-T.dump.enc --dump-header=plain", copyB64},
		{"sealed", []string{sealed}, "restore-dump --hold --dump-name=s.dump.enc --dump-header=sealed", ""},
		{"--plaintext before FILE", []string{"--plaintext", plain},
			"restore-dump --hold --dump-name=mailarchive-T.dump.enc --dump-header=plain --plaintext", copyB64},
		{"--plaintext after FILE", []string{plain, "--plaintext"},
			"restore-dump --hold --dump-name=mailarchive-T.dump.enc --dump-header=plain --plaintext", copyB64},
	} {
		code, out, calls := runMa(t, append([]string{"restore-dump"}, c.args...)...)
		all := strings.Join(calls, "\n")
		hold := -1
		for i, call := range calls {
			if strings.Contains(call, " restore-dump --hold ") {
				hold = i
			}
		}
		if code == 0 || hold < 0 || hold+1 >= len(calls) {
			t.Errorf("%s: exit %d\n%s\ndocker calls:\n%s", c.name, code, out, all)
			continue
		}
		if !strings.HasSuffix(calls[hold], c.want) {
			t.Errorf("%s: the holder got %q, want the suffix %q", c.name, calls[hold], c.want)
		}
		if strings.Contains(c.want, "--plaintext") != strings.Contains(calls[hold], "--plaintext") {
			t.Errorf("%s: --plaintext in %q", c.name, calls[hold])
		}
		if got := calls[hold+1]; got != "keyring-copy="+c.keyring {
			t.Errorf("%s: %s, want keyring-copy=%s", c.name, got, c.keyring)
		}
		if strings.Contains(all, "pg_restore") || strings.Contains(all, "unseal-stream") {
			t.Errorf("%s: a restore ran without \"ready\":\n%s", c.name, all)
		}
	}

	// Any other option goes to the plain command, as before.
	code, out, calls := runMa(t, "restore-dump", "--plaintext", "--reconcile-only")
	if last := calls[len(calls)-1]; code != 0 || !strings.HasSuffix(last, "mail-archive restore-dump --plaintext --reconcile-only") {
		t.Errorf("--plaintext --reconcile-only: exit %d\n%s\ndocker calls:\n%s", code, out, strings.Join(calls, "\n"))
	}
	if code, out, _ := runMa(t, "restore-dump", "--plaintext"); code != 2 || !strings.Contains(out, "usage: ./ma restore-dump [--plaintext] FILE") {
		t.Errorf("--plaintext without FILE: exit %d\n%s", code, out)
	}
}
