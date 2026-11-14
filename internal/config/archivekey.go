package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/keepia/archive/internal/blobcrypt"
)

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
// environment, for commands that run without a database. The secret key
// (SecretKeyFromEnv, its file or its variable) is read only to refuse an
// archive key equal to it.
func ArchiveKeyFromEnv() (kek []byte, source string, err error) {
	secret, _, err := SecretKeyFromEnv()
	if err != nil {
		return nil, "", err
	}
	return LoadArchiveKey(Getenv(EnvArchiveKeyFile), Getenv(EnvArchiveKey), DataDirFromEnv(), secret)
}

func readArchiveKeyFile(path, dataDir string) ([]byte, error) {
	data, err := readKeyFile(path, dataDir, "the archive key")
	if err != nil {
		return nil, err
	}
	return blobcrypt.ParseKEK(string(data))
}

// KeyInDataDir reports whether the key file lies in the data directory:
// by its path (WithinDir), or because the key file's directory or one of
// its ancestors is the data directory or one of its subdirectories reached
// under another path. That is the Compose layout with ARCHIVE_KEY_DIR inside
// ARCHIVE_DIR: /keys is then a bind mount of a directory below /data.
func KeyInDataDir(keyFile, dataDir string) (bool, error) {
	return keyInDataDir(keyFile, dataDir, os.Stat)
}

func keyInDataDir(keyFile, dataDir string, stat func(string) (fs.FileInfo, error)) (bool, error) {
	if in, err := WithinDir(keyFile, dataDir); err != nil || in {
		return in, err
	}
	data, err := stat(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	targets := []fs.FileInfo{data}
	entries, err := os.ReadDir(dataDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() {
			if fi, err := stat(filepath.Join(dataDir, e.Name())); err == nil {
				targets = append(targets, fi)
			}
		}
	}
	abs, err := resolve(keyFile)
	if err != nil {
		return false, err
	}
	for d := filepath.Dir(abs); ; {
		if fi, err := stat(d); err == nil {
			for _, t := range targets {
				if os.SameFile(fi, t) {
					return true, nil
				}
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false, nil
		}
		d = parent
	}
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
