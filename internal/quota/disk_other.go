//go:build !unix

package quota

import "errors"

// FreeSpace is not known here: the disk reserve is then not checked.
func FreeSpace(string) (int64, error) { return 0, errors.New("free disk space is not known on this system") }
