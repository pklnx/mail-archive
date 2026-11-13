//go:build unix

package quota

import "golang.org/x/sys/unix"

// FreeSpace returns the bytes an unprivileged user may still write in
// dir's file system.
func FreeSpace(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	free := uint64(st.Bavail) * uint64(st.Bsize) //nolint:gosec,unconvert // block counts and sizes are positive; their types differ per system
	if free > 1<<62 {
		free = 1 << 62
	}
	return int64(free), nil
}
