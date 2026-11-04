//go:build !unix

package blobcrypt

// lockFileExclusive is a no-op where flock is missing; the product runs on
// Linux only.
func lockFileExclusive(string) (func(), error) { return func() {}, nil }
