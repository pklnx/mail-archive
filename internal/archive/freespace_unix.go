//go:build unix

package archive

import "golang.org/x/sys/unix"

// freeSpace returns the bytes an unprivileged user may still write in
// dir's file system.
func freeSpace(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec,unconvert // block counts and sizes are positive; their types differ per system
}

// DiskUsage returns the bytes an unprivileged user may still write in
// dir's file system and the file system's size, for the metrics.
func DiskUsage(dir string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)                                        //nolint:gosec,unconvert // block sizes are positive; their type differs per system
	return uint64(st.Bavail) * bsize, uint64(st.Blocks) * bsize, nil //nolint:unconvert // the block counts' types differ per system
}
