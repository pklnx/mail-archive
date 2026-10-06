package archive

import (
	"errors"
	"strings"
	"unicode"
)

// ValidateAccountName rejects names that would not work in URLs, command
// lines or logs.
func ValidateAccountName(name string) error {
	if name == "" || len(name) > 64 {
		return errors.New("name must be 1 to 64 characters")
	}
	if strings.TrimSpace(name) != name {
		return errors.New("name must not start or end with spaces")
	}
	for _, c := range name {
		if c == '/' || unicode.IsControl(c) {
			return errors.New("name must not contain slashes or control characters")
		}
	}
	return nil
}
