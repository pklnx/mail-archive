//go:build !unix

package archive

import (
	"errors"
	"math"
)

// freeSpace is not known here; the check then always passes.
func freeSpace(string) (uint64, error) { return math.MaxUint64, nil }

// DiskUsage is not known here.
func DiskUsage(string) (free, total uint64, err error) {
	return 0, 0, errors.New("disk usage is not measured on this system")
}
