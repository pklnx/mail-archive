package gobd

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxSumsLine bounds a SHA256SUMS line: a hash, two spaces and a path of
// format v1 fit with room to spare.
const maxSumsLine = 512

// sumsLine is one line of SHA256SUMS in the GNU coreutils format that
// "sha256sum -c" reads: the hex digest, two spaces, the path.
func sumsLine(sum, path string) string { return sum + "  " + path + "\n" }

// ParseSums reads SHA256SUMS from r line by line and calls fn with each
// path and digest in order. It accepts only lines as sumsLine writes them
// (lowercase hex, two spaces, a path without spaces, backslashes or
// control characters, "\n" at the end) and returns the file's SHA-256.
// Checking the paths themselves is up to fn.
func ParseSums(r io.Reader, fn func(path, sum string) error) (string, error) {
	h := sha256.New()
	br := bufio.NewReaderSize(io.TeeReader(r, h), maxSumsLine)
	for n := 1; ; n++ {
		line, err := br.ReadSlice('\n')
		switch {
		case errors.Is(err, io.EOF) && len(line) == 0:
			return hex.EncodeToString(h.Sum(nil)), nil
		case errors.Is(err, io.EOF):
			return "", fmt.Errorf("line %d is incomplete (no newline)", n)
		case errors.Is(err, bufio.ErrBufferFull):
			return "", fmt.Errorf("line %d is too long", n)
		case err != nil:
			return "", err
		}
		s := string(line[:len(line)-1])
		sum, path, ok := strings.Cut(s, "  ")
		if !ok || !isSHA(sum) || path == "" || strings.ContainsFunc(path, func(r rune) bool {
			return r <= ' ' || r == '\\' || r >= 0x7f
		}) {
			return "", fmt.Errorf("line %d is not \"<sha256>  <path>\"", n)
		}
		if err := fn(path, sum); err != nil {
			return "", err
		}
	}
}
