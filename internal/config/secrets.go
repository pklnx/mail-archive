package config

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pklnx/mail-archive/internal/crypto"
)

// EnvSecretKeyFile is the file holding the secret key (#396); preferred
// over EnvSecretKey. Both set is refused.
const EnvSecretKeyFile = "MAIL_ARCHIVE_SECRET_KEY_FILE" //nolint:gosec // variable name, not a credential

// SecretKeyError is a refused secret key configuration: both the file and
// the variable set, a file that breaks the key file rules, a key that is
// not 32 bytes of base64, no key where one is required, or a key that
// does not match the instance (#396). A command that gets it does not
// start (exit code 4). Its message never contains the key.
type SecretKeyError struct{ msg string }

func (e *SecretKeyError) Error() string { return e.msg }

// NewSecretKeyError returns a SecretKeyError with the message msg, which
// must not contain the key.
func NewSecretKeyError(msg string) *SecretKeyError { return &SecretKeyError{msg: msg} }

// LoadSecretKey reads the secret key from the key file (preferred) or the
// value of EnvSecretKey. Both empty means not configured (nil key, no
// error). The file follows the rules of the archive key file: a regular
// file that neither group nor others may access, outside the data
// directory. Refused with a SecretKeyError: both set, a file that breaks
// these rules or cannot be read, a key that is not 32 bytes of base64.
// source names where the key came from, for messages.
func LoadSecretKey(file, value, dataDir string) (key []byte, source string, err error) {
	file = strings.TrimSpace(file)
	switch {
	case file != "" && value != "":
		return nil, "", NewSecretKeyError(fmt.Sprintf("both %s and %s are set; set only one (the file is preferred)", EnvSecretKeyFile, EnvSecretKey))
	case file != "":
		data, err := readKeyFile(file, dataDir, "the secret key")
		if err == nil {
			key, err = crypto.ParseKey(strings.TrimSpace(string(data)))
		}
		if err != nil {
			return nil, "", NewSecretKeyError(fmt.Sprintf("%s: %v", EnvSecretKeyFile, err))
		}
		return key, "file " + file, nil
	case value != "":
		key, err = crypto.ParseKey(value)
		if err != nil {
			return nil, "", NewSecretKeyError(fmt.Sprintf("%s: %v", EnvSecretKey, err))
		}
		return key, EnvSecretKey, nil
	}
	return nil, "", nil
}

// SecretKeyFromEnv is LoadSecretKey with every input from the environment.
func SecretKeyFromEnv() (key []byte, source string, err error) {
	return LoadSecretKey(os.Getenv(EnvSecretKeyFile), os.Getenv(EnvSecretKey), DataDirFromEnv())
}

// maxKeyFile is the largest key file read; a key is 44 characters.
const maxKeyFile = 4 << 10

// readKeyFile reads a key file of what (for messages): a regular file
// outside the data directory (KeyInDataDir) that neither group nor others
// may access, at most maxKeyFile bytes. Errors name the path and the
// reason, never the content.
func readKeyFile(path, dataDir, what string) ([]byte, error) {
	inside, err := KeyInDataDir(path, dataDir)
	if err != nil {
		return nil, err
	}
	if inside {
		return nil, fmt.Errorf("%s is inside the data directory %s; keep %s apart from the data it protects", path, dataDir, what)
	}
	f, err := os.Open(path) //nolint:gosec // the configured key file
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("%s has mode %04o: group or others may access it; allow only its owner (chmod 0400)", path, perm)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxKeyFile {
		return nil, fmt.Errorf("%s is too large for a key file", path)
	}
	return data, nil
}

// ChildEnv builds the whole environment of a child process (#396): the
// variables of environ (NAME=value, as os.Environ returns them) whose
// names are in keep, then extra. Nothing else of environ is passed on. A
// MAIL_ARCHIVE_* or PG* variable of environ is never passed on, even when
// keep names it: those hold the secret key, the archive key, the database
// URL and its password. What a child needs of them (the data directory,
// the libpq variables of the database URL) comes in extra. Of a name that
// environ holds twice, the first counts, as os.Getenv does.
func ChildEnv(environ, keep []string, extra ...string) []string {
	wanted := make(map[string]bool, len(keep))
	for _, name := range keep {
		wanted[name] = true
	}
	seen := map[string]bool{}
	var env []string
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		if wanted[name] && !strings.HasPrefix(name, "MAIL_ARCHIVE_") && !strings.HasPrefix(name, "PG") {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}
