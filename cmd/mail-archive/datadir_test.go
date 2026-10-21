package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareDataDir(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name     string
		missing  bool     // the directory does not exist yet
		files    []string // created in the directory
		isRoot   bool
		userns   bool
		writable []bool // answers of canWrite, in order
		checkErr error
		owner    uint32
		chownErr error

		wantChown  bool
		wantStdout string
		wantStderr []string
		notStderr  string
	}{
		{name: "writable", isRoot: true, writable: []bool{true}},
		{
			name: "missing, created by root", missing: true, isRoot: true, writable: []bool{false, true},
			wantChown: true, wantStdout: "owner set to UID 65532",
		},
		{
			name: "empty, owner 0", isRoot: true, writable: []bool{false, true},
			wantChown: true, wantStdout: "owner set to UID 65532",
		},
		{
			name: "only lost+found", files: []string{"lost+found"}, isRoot: true, writable: []bool{false, true},
			wantChown: true, wantStdout: "owner set to UID 65532",
		},
		{
			name: "not empty", files: []string{"messages"}, isRoot: true, writable: []bool{false},
			wantStderr: []string{"not empty", "sudo chown -R 65532:65532 data"}, notStderr: "podman",
		},
		{
			name: "not empty, user namespace", files: []string{"messages"}, isRoot: true, userns: true, writable: []bool{false},
			wantStderr: []string{"not empty", "alpine chown -R 65532:65532 /data", "podman unshare chown -R 65532:65532 data"},
			notStderr:  "sudo",
		},
		{
			name: "owner 1000", isRoot: true, owner: 1000, writable: []bool{false},
			wantStderr: []string{"belongs to UID 1000"},
		},
		{
			name: "not root", writable: []bool{false},
			wantStderr: []string{"does not run as root"},
		},
		{
			name: "check fails", isRoot: true, checkErr: errBoom,
			wantStderr: []string{"could not check", "boom"},
		},
		{
			name: "chown fails", isRoot: true, writable: []bool{false}, chownErr: errBoom,
			wantChown: true, wantStderr: []string{"could not be changed (boom)"},
		},
		{
			name: "still not writable", isRoot: true, writable: []bool{false, false},
			wantChown: true, wantStderr: []string{"even after changing its owner"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			if !tt.missing {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tt.files {
				if err := os.Mkdir(filepath.Join(dir, f), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			chowned := false
			calls := 0
			env := dataDirEnv{
				isRoot: tt.isRoot,
				userns: tt.userns,
				canWrite: func(string) (bool, error) {
					if tt.checkErr != nil {
						return false, tt.checkErr
					}
					calls++
					return tt.writable[calls-1], nil
				},
				owner: func(string) (uint32, error) { return tt.owner, nil },
				chown: func(_ string, uid, gid int) error {
					if uid != 65532 || gid != 65532 {
						t.Errorf("chown to %d:%d", uid, gid)
					}
					chowned = true
					return tt.chownErr
				},
			}
			var stdout, stderr bytes.Buffer
			prepareDataDir(dir, &stdout, &stderr, env)

			if _, err := os.Stat(dir); err != nil {
				t.Errorf("directory: %v", err)
			}
			if chowned != tt.wantChown {
				t.Errorf("chown called: %v, want %v", chowned, tt.wantChown)
			}
			if tt.wantStdout == "" && stdout.Len() > 0 || !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if len(tt.wantStderr) == 0 && stderr.Len() > 0 {
				t.Errorf("stderr = %q, want nothing", stderr.String())
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr = %q, want %q", stderr.String(), want)
				}
			}
			if tt.notStderr != "" && strings.Contains(stderr.String(), tt.notStderr) {
				t.Errorf("stderr = %q, must not contain %q", stderr.String(), tt.notStderr)
			}
		})
	}
}

func TestPrepareDataDirMkdirFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	prepareDataDir(filepath.Join(file, "data"), &stdout, &stderr, dataDirEnv{isRoot: true})
	if !strings.Contains(stderr.String(), "could not create the data directory") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestOpenBlobsNotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write to any directory")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv("MAIL_ARCHIVE_COMMAND", "./ma")
	_, err := openBlobs(dir)
	if err == nil || !strings.Contains(err.Error(), "is not writable for UID") ||
		!strings.Contains(err.Error(), "run ./ma migrate") {
		t.Errorf("err = %v", err)
	}
}

// A parent directory that cannot be opened for the durability barrier is
// named; the data directory itself is writable.
func TestOpenBlobsUnreadableParent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read any directory")
	}
	parent := filepath.Join(t.TempDir(), "parent")
	dir := filepath.Join(parent, "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o300); err != nil { // traversable, not readable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	_, err := openBlobs(dir)
	if err == nil || strings.Contains(err.Error(), "is not writable") ||
		!strings.Contains(err.Error(), "cannot synchronize its parent directory "+parent+", which must be readable") {
		t.Errorf("err = %v", err)
	}
}

func TestNotWritableError(t *testing.T) {
	cause := errors.New("mkdir /data/messages: permission denied")
	got := notWritableError("/data", 65532, "./ma", cause).Error()
	want := "data directory /data is not writable for UID 65532 (the container user): " +
		"mkdir /data/messages: permission denied; run ./ma migrate, or see " + linuxHostsURL
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	got = notWritableError("./data", 1000, "mail-archive", cause).Error()
	if strings.Contains(got, "container user") || strings.Contains(got, "./ma") ||
		!strings.Contains(got, "UID 1000") || !strings.Contains(got, "make it writable for that user") {
		t.Errorf("got %s", got)
	}
	if !errors.Is(notWritableError("/data", 1, "x", cause), cause) {
		t.Error("cause not wrapped")
	}
}
