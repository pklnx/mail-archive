//go:build !unix

package archive

import "math"

// freeSpace is not known here; the check then always passes.
func freeSpace(string) (uint64, error) { return math.MaxUint64, nil }
