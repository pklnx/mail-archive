package gobd

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

var errNoSys, errIO error = unix.ENOSYS, unix.EIO

// syncfs that is not available falls back to syncing file by file; a
// failed write-back is reported, not hidden by the fallback.
func TestSyncTreeFallback(t *testing.T) {
	dir := t.TempDir()
	byFile := 0
	oldFS, oldFile := syncfs, syncByFile
	t.Cleanup(func() { syncfs, syncByFile = oldFS, oldFile })
	syncByFile = func(d string) error { byFile++; return syncTreeByFile(d) }
	for _, c := range []struct {
		err     error
		byFile  int
		wantErr bool
	}{
		{nil, 0, false},
		{errNoSys, 1, false},
		{errIO, 0, true},
	} {
		byFile = 0
		syncfs = func(int) error { return c.err }
		err := syncTree(dir)
		if (err != nil) != c.wantErr || byFile != c.byFile || (c.wantErr && !errors.Is(err, c.err)) {
			t.Errorf("syncfs %v: err %v, by file %d", c.err, err, byFile)
		}
	}
}
