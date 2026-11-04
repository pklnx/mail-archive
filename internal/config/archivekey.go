package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/crypto"
)

// maxArchiveKeyFile is the largest key file read; a key is 44 characters.
const maxArchiveKeyFile = 4 << 10

// LoadArchiveKey reads the archive key (KEK) from the key file (preferred)
// or the value of EnvArchiveKey. Both empty means not configured (nil key,
// no error). Refused: both set; a key file that is not a regular file, is
// readable or writable by group or others, or lies inside the data
// directory; a key that is not 32 bytes of base64; a key equal to the
// secret key. Errors never contain the key. source names where the key
// came from, for messages.
func LoadArchiveKey(file, value, dataDir string, secretKey []byte) (kek []byte, source string, err error) {
	file = strings.TrimSpace(file)
	value = strings.TrimSpace(value)
	switch {
	case file != "" && value != "":
		return nil, "", fmt.Errorf("both %s and %s are set; set only one (the file is preferred)", EnvArchiveKeyFile, EnvArchiveKey)
	case file != "":
		source = "file " + file
		kek, err = readArchiveKeyFile(file, dataDir)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", EnvArchiveKeyFile, err)
		}
	case value != "":
		source = EnvArchiveKey
		kek, err = blobcrypt.ParseKEK(value)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", EnvArchiveKey, err)
		}
	default:
		return nil, "", nil
	}
	if secretKey != nil && blobcrypt.SameKEK(kek, secretKey) {
		return nil, "", fmt.Errorf("the archive key (%s) equals %s; they must differ: losing the secret key costs the stored passwords, losing the archive key all mail", source, EnvSecretKey)
	}
	return kek, source, nil
}

// ArchiveKeyFromEnv is LoadArchiveKey with every input from the
// environment, for commands that run without a database.
func ArchiveKeyFromEnv() (kek []byte, source string, err error) {
	var secret []byte
	if raw := os.Getenv(EnvSecretKey); raw != "" {
		if secret, err = crypto.ParseKey(raw); err != nil {
			return nil, "", errors.New(EnvSecretKey + ": " + err.Error())
		}
	}
	return LoadArchiveKey(os.Getenv(EnvArchiveKeyFile), os.Getenv(EnvArchiveKey), DataDirFromEnv(), secret)
}

func readArchiveKeyFile(path, dataDir string) ([]byte, error) {
	inside, err := WithinDir(path, dataDir)
	if err != nil {
		return nil, err
	}
	if inside {
		return nil, fmt.Errorf("%s is inside the data directory %s; keep the archive key apart from the data it protects", path, dataDir)
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
	data, err := io.ReadAll(io.LimitReader(f, maxArchiveKeyFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxArchiveKeyFile {
		return nil, fmt.Errorf("%s is too large for a key file", path)
	}
	return blobcrypt.ParseKEK(string(data))
}

// WithinDir reports whether path is dir or lies below it, after making
// both absolute and resolving symlinks as far as they exist.
func WithinDir(path, dir string) (bool, error) {
	p, err := resolve(path)
	if err != nil {
		return false, err
	}
	d, err := resolve(dir)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(d, p)
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

// resolve makes p absolute and resolves the symlinks of its longest
// existing prefix; the missing rest is appended as is.
func resolve(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	for cur := abs; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
