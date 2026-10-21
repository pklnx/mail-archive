package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
)

// containerUID and containerGID are the image's user (Dockerfile: USER 65532:65532).
const containerUID, containerGID = 65532, 65532

// exitNotWritable is the exit code of `prepare-data --check` for a data
// directory the process cannot write to.
const exitNotWritable = 3

const linuxHostsURL = "https://pklnx.github.io/mail-archive/guide/getting-started#linux-hosts"

func newPrepareDataCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:    "prepare-data",
		Short:  "Make a fresh data directory writable for the container user (run by ./ma migrate)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := config.DataDirFromEnv()
			if check {
				if err := tryWrite(dir); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), err)
					return &exitError{msg: err.Error(), code: exitNotWritable}
				}
				return nil
			}
			// Never fails: a file permission problem must not block the
			// schema upgrade that ./ma migrate runs next.
			prepareDataDir(dir, cmd.OutOrStdout(), cmd.ErrOrStderr(), systemDataDirEnv())
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check that this process can write to the data directory")
	_ = cmd.Flags().MarkHidden("check")
	return cmd
}

// dataDirEnv is what prepareDataDir needs from the system, replaced in tests.
type dataDirEnv struct {
	isRoot bool
	userns bool // in a user namespace: rootless Docker or Podman
	// canWrite reports whether the container user can write to dir; an
	// error means the check itself failed.
	canWrite func(dir string) (bool, error)
	owner    func(dir string) (uint32, error)
	chown    func(dir string, uid, gid int) error
}

func systemDataDirEnv() dataDirEnv {
	env := dataDirEnv{
		isRoot: os.Getuid() == 0,
		userns: inUserNamespace(),
		owner:  fileOwner,
		chown:  os.Chown,
	}
	if env.isRoot {
		env.canWrite = checkAsContainerUser
	} else {
		env.canWrite = func(dir string) (bool, error) { return tryWrite(dir) == nil, nil }
	}
	return env
}

// prepareDataDir creates dir if needed and, run as root, gives a fresh
// empty directory, as Docker creates a missing bind mount source, to the
// container user. It changes nothing else and only warns: never a
// recursive chown, never an error.
func prepareDataDir(dir string, stdout, stderr io.Writer, env dataDirEnv) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		fmt.Fprintf(stderr, "warning: could not create the data directory %s: %v\n", dir, err)
		return
	}
	ok, err := env.canWrite(dir)
	if err != nil {
		fmt.Fprintf(stderr, "warning: could not check whether the data directory %s is writable for UID %d: %v\n",
			dir, containerUID, err)
		return
	}
	if ok {
		return
	}
	warn := func(reason string) {
		fmt.Fprintf(stderr, "warning: the data directory %s (ARCHIVE_DIR in .env, default ./data) is not writable "+
			"for the container user, UID %d, %s.\n%s", dir, containerUID, reason, ownerHint(env.userns))
	}
	if !env.isRoot {
		warn("and this command does not run as root")
		return
	}
	empty, err := isEmpty(dir)
	if err != nil {
		warn(fmt.Sprintf("and it could not be read (%v)", err))
		return
	}
	if !empty {
		warn("and it is not empty, so its owner was left as it is")
		return
	}
	uid, err := env.owner(dir)
	if err != nil {
		warn(fmt.Sprintf("and its owner could not be read (%v)", err))
		return
	}
	if uid != 0 {
		warn(fmt.Sprintf("and it belongs to UID %d, so its owner was left as it is", uid))
		return
	}
	if err := env.chown(dir, containerUID, containerGID); err != nil {
		warn(fmt.Sprintf("and its owner could not be changed (%v)", err))
		return
	}
	if ok, err := env.canWrite(dir); err != nil || !ok {
		warn("even after changing its owner")
		return
	}
	fmt.Fprintf(stdout, "data directory %s: owner set to UID %d (the container user)\n", dir, containerUID)
}

// ownerHint tells how to give an existing data directory to the container
// user from the host. In a user namespace, UID 65532 in the container is
// another UID on the host, so a plain chown on the host would be wrong.
func ownerHint(userns bool) string {
	if userns {
		return `  Fix it on the host (use your ARCHIVE_DIR instead of data), with rootless Docker:
    docker run --rm --user 0 -v "$PWD/data:/data" alpine chown -R 65532:65532 /data
  or with Podman:
    podman unshare chown -R 65532:65532 data
`
	}
	return `  Fix it on the host (use your ARCHIVE_DIR instead of data):
    sudo chown -R 65532:65532 data
`
}

// inUserNamespace reports whether UID 0 here is not root on the host.
func inUserNamespace() bool {
	b, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return false
	}
	return strings.Join(strings.Fields(string(b)), " ") != "0 0 4294967295"
}

// checkAsContainerUser runs `prepare-data --check` as the container user:
// a real write test, not a guess from mode bits.
func checkAsContainerUser(dir string) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	cmd := exec.Command(exe, "prepare-data", "--check") //nolint:gosec // this binary itself
	cmd.Env = append(os.Environ(), config.EnvDataDir+"="+dir)
	if err := runAs(cmd, containerUID, containerGID); err != nil {
		return false, err
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) && exitErr.ExitCode() == exitNotWritable {
		return false, nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return false, fmt.Errorf("%w: %s", err, msg)
	}
	return false, err
}

// tryWrite creates and removes a file in dir.
func tryWrite(dir string) error {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// isEmpty reports whether dir holds nothing but a filesystem's lost+found.
func isEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Name() != "lost+found" {
			return false, nil
		}
	}
	return true, nil
}

// openBlobs opens the blob store and explains a permission error. A
// parent directory that cannot be opened for its durability barrier is
// not the data directory being read-only.
func openBlobs(dir string) (*blobstore.Store, error) {
	blobs, err := blobstore.New(dir)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return blobs, err
	}
	var ae *blobstore.AncestorError
	if errors.As(err, &ae) {
		return nil, fmt.Errorf("data directory %s: cannot synchronize its parent directory %s, which must be readable for UID %d: %w",
			dir, ae.Dir, os.Getuid(), err)
	}
	return nil, notWritableError(dir, os.Getuid(), commandName(), err)
}

func notWritableError(dir string, uid int, command string, err error) error {
	who := fmt.Sprintf("UID %d", uid)
	if uid == containerUID {
		who += " (the container user)"
	}
	fix := "make it writable for that user"
	if command == "./ma" {
		fix = "run ./ma migrate"
	}
	return fmt.Errorf("data directory %s is not writable for %s: %w; %s, or see %s", dir, who, err, fix, linuxHostsURL)
}
