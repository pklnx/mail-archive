package gobd

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// syncfs and syncByFile are replaced by tests.
var (
	syncfs     = unix.Syncfs
	syncByFile = syncTreeByFile
)

// syncTree puts everything under dir on disk with one syncfs of its file
// system. Where syncfs is not available (an old kernel, or a sandbox that
// refuses it) it syncs every file and directory instead. Any other error,
// such as a failed write-back, is returned: syncing file by file after it
// could report success for data that never reached the disk.
func syncTree(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // the sink's own directory
	if err != nil {
		return err
	}
	err = syncfs(int(f.Fd()))
	_ = f.Close()
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EOPNOTSUPP) {
		return syncByFile(dir)
	}
	return err
}
