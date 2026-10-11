package attest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// File names in the attestation directory (<data dir>/attest).
const (
	DefaultKeyFile  = "signing.key"
	PublicKeyFile   = "signing.pub"
	RetiredKeysFile = "retired-keys.pem"
	ChainFile       = "attestations.jsonl"
	LeavesDir       = "leaves"
)

// Keys is a set of trusted public keys, by EncodeKey.
type Keys map[string]bool

// EncodeKey is how a public key appears in an attestation: base64 of the
// raw 32 bytes.
func EncodeKey(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

// DecodeKey reverses EncodeKey.
func DecodeKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("malformed public key")
	}
	return ed25519.PublicKey(b), nil
}

// MarshalPrivateKey encodes a key as PKCS#8 PEM.
func MarshalPrivateKey(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ParsePrivateKey decodes a PKCS#8 PEM Ed25519 key.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an Ed25519 key")
	}
	return key, nil
}

// MarshalPublicKey encodes a public key as SPKI PEM.
func MarshalPublicKey(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// ParsePublicKeys decodes every SPKI PEM Ed25519 key in data. Anything
// else in data is an error.
func ParsePublicKeys(data []byte) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("unexpected PEM block %q", block.Type)
		}
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := k.(ed25519.PublicKey)
		if !ok {
			return nil, errors.New("not an Ed25519 key")
		}
		keys = append(keys, pub)
	}
	if len(bytes.TrimSpace(data)) > 0 {
		return nil, errors.New("data that is not a PEM public key")
	}
	return keys, nil
}

// LoadPrivateKey reads a key file. Like OpenSSH, it refuses a file that
// the group or others can read.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s can be read by others (mode %04o); run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	data, err := os.ReadFile(path) //nolint:gosec // path from configuration
	if err != nil {
		return nil, err
	}
	key, err := ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}

// LoadPublicKey reads a file with exactly one public key.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path from configuration
	if err != nil {
		return nil, err
	}
	keys, err := ParsePublicKeys(data)
	if err == nil && len(keys) != 1 {
		err = fmt.Errorf("want one public key, found %d", len(keys))
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return keys[0], nil
}

// LoadTrusted returns signing.pub and the retired keys of dir. Missing
// files are no error; the set is then smaller, possibly empty.
func LoadTrusted(dir string) (Keys, error) {
	keys := Keys{}
	pub, err := LoadPublicKey(filepath.Join(dir, PublicKeyFile))
	switch {
	case err == nil:
		keys[EncodeKey(pub)] = true
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, RetiredKeysFile)) //nolint:gosec // fixed name in the data directory
	switch {
	case err == nil:
		retired, err := ParsePublicKeys(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, RetiredKeysFile), err)
		}
		for _, k := range retired {
			keys[EncodeKey(k)] = true
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return keys, nil
}

// LoadSigner reads the signing key and checks that signing.pub in dir is
// its public key, so that what it signs is trusted. A missing key file
// returns an error that wraps fs.ErrNotExist.
func LoadSigner(keyFile, dir string) (ed25519.PrivateKey, error) {
	key, err := LoadPrivateKey(keyFile)
	if err != nil {
		return nil, err
	}
	pubPath := filepath.Join(dir, PublicKeyFile)
	pub, err := LoadPublicKey(pubPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s is missing; it is written by attest keygen together with the key", pubPath)
	}
	if err != nil {
		return nil, err
	}
	if !pub.Equal(key.Public()) {
		return nil, fmt.Errorf("%s does not match %s; run attest keygen --rotate", pubPath, keyFile)
	}
	return key, nil
}

// Keygen creates a key file and signing.pub in dir. It refuses if either
// exists.
func Keygen(keyFile, dir string) (ed25519.PublicKey, error) {
	pubPath := filepath.Join(dir, PublicKeyFile)
	for _, p := range []string{keyFile, pubPath} {
		if _, err := os.Lstat(p); err == nil {
			return nil, fmt.Errorf("%s exists; use attest keygen --rotate to replace the key", p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := writeKeys(keyFile, dir, key, false); err != nil {
		return nil, err
	}
	return pub, nil
}

// Rotate replaces the key: the current public key (signing.pub, or the
// key file's if signing.pub is missing) is appended to retired-keys.pem,
// which keeps older attestations trusted, and a new key and signing.pub
// replace the old ones. retired is nil if there was no key at all.
func Rotate(keyFile, dir string) (pub, retired ed25519.PublicKey, err error) {
	pubPath := filepath.Join(dir, PublicKeyFile)
	retired, err = LoadPublicKey(pubPath)
	if errors.Is(err, fs.ErrNotExist) {
		var key ed25519.PrivateKey
		key, err = LoadPrivateKey(keyFile)
		if err == nil {
			retired = key.Public().(ed25519.PublicKey)
		}
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	if retired == nil {
		return nil, nil, errors.New("no key to rotate; run attest keygen")
	}
	if err := ensureDir(dir); err != nil {
		return nil, nil, err
	}
	pemData, err := MarshalPublicKey(retired)
	if err != nil {
		return nil, nil, err
	}
	if err := appendSync(filepath.Join(dir, RetiredKeysFile), pemData); err != nil {
		return nil, nil, err
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	if err := writeKeys(keyFile, dir, key, true); err != nil {
		return nil, nil, err
	}
	return pub, retired, nil
}

// writeKeys writes the key file and signing.pub, new files with O_EXCL or,
// with replace, through a temp file and rename.
func writeKeys(keyFile, dir string, key ed25519.PrivateKey, replace bool) error {
	if err := ensureDir(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return err
	}
	keyPEM, err := MarshalPrivateKey(key)
	if err != nil {
		return err
	}
	pubPEM, err := MarshalPublicKey(key.Public().(ed25519.PublicKey))
	if err != nil {
		return err
	}
	write := createSync
	if replace {
		write = replaceSync
	}
	if err := write(keyFile, keyPEM); err != nil {
		return err
	}
	return write(filepath.Join(dir, PublicKeyFile), pubPEM)
}
