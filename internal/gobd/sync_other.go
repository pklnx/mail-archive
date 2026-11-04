//go:build !linux

package gobd

// syncTree syncs every file and directory under dir: other systems have
// no syncfs.
func syncTree(dir string) error { return syncTreeByFile(dir) }
