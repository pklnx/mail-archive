package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/pklnx/mail-archive/internal/crypto"
)

// The setup token of a new instance (#396, part 3): the first admin is
// created once with the link PUBLIC_URL/#setup=<token>.
const (
	// EnvSetupTokenFile is the file holding the setup token; preferred over
	// EnvSetupToken. Both set is refused.
	EnvSetupTokenFile = "KEEPIA_ARCHIVE_SETUP_TOKEN_FILE" //nolint:gosec // variable name, not a credential
	// EnvSetupToken is the setup token itself.
	EnvSetupToken = "KEEPIA_ARCHIVE_SETUP_TOKEN" //nolint:gosec // variable name, not a credential
	// EnvEncryptionAtSetup ("on" or "off", default on): a new instance
	// turns encryption at rest on before it archives anything, and refuses
	// to start without an archive key. "off" is for the operator's test
	// instances only (CI, the e2e demo, the Compose smoke test).
	EnvEncryptionAtSetup = "KEEPIA_ARCHIVE_ENCRYPTION_AT_SETUP"
)

// Lengths of a setup token, in characters of printable ASCII.
const (
	MinSetupToken = 32
	MaxSetupToken = 256
)

// SetupTokenError is a refused setup token configuration: both the file
// and the variable set, a file that breaks the key file rules, or a token
// that is too short, too long, not printable ASCII, or one of the keys. A
// command that gets it does not start (exit code 4). Its message never
// contains the token.
type SetupTokenError struct{ msg string }

func (e *SetupTokenError) Error() string { return e.msg }

// LoadSetupToken reads the setup token from its file (preferred) or the
// value of EnvSetupToken; surrounding white space is trimmed. Both empty
// means none (no error). The file follows the rules of the key files: a
// regular file that neither group nor others may access, outside the data
// directory. The token must have MinSetupToken to MaxSetupToken printable
// ASCII characters and must not be one of keys (the secret key and the
// archive key, in any base64 form): a token is shown to whoever sets up
// the instance, a key never is.
func LoadSetupToken(file, value, dataDir string, keys ...[]byte) (string, error) {
	file, value = strings.TrimSpace(file), strings.TrimSpace(value)
	var token, source string
	switch {
	case file != "" && value != "":
		return "", &SetupTokenError{fmt.Sprintf("both %s and %s are set; set only one (the file is preferred)", EnvSetupTokenFile, EnvSetupToken)}
	case file != "":
		data, err := readKeyFile(file, dataDir, "the setup token")
		if err != nil {
			return "", &SetupTokenError{fmt.Sprintf("%s: %v", EnvSetupTokenFile, err)}
		}
		token, source = strings.TrimSpace(string(data)), EnvSetupTokenFile
	case value != "":
		token, source = value, EnvSetupToken
	default:
		return "", nil
	}
	if err := checkSetupToken(token, keys); err != nil {
		return "", &SetupTokenError{fmt.Sprintf("%s: %v (create one with `mail-archive setup token`)", source, err)}
	}
	return token, nil
}

// SetupTokenFromEnv is LoadSetupToken with the token from the environment
// and the keys of cfg.
func SetupTokenFromEnv(cfg *Config) (string, error) {
	return LoadSetupToken(Getenv(EnvSetupTokenFile), Getenv(EnvSetupToken), cfg.DataDir, cfg.SecretKey, cfg.ArchiveKey)
}

// checkSetupToken checks the form of a setup token. Its errors never
// contain the token.
func checkSetupToken(token string, keys [][]byte) error {
	if n := len(token); n < MinSetupToken || n > MaxSetupToken {
		return fmt.Errorf("the setup token needs %d to %d characters, it has %d", MinSetupToken, MaxSetupToken, n)
	}
	for i := 0; i < len(token); i++ {
		if c := token[i]; c < 0x21 || c > 0x7e {
			return fmt.Errorf("the setup token may hold only printable ASCII characters without spaces (character %d is not)", i+1)
		}
	}
	if k, err := crypto.ParseKey(token); err == nil {
		for _, key := range keys {
			if key != nil && bytes.Equal(k, key) {
				return fmt.Errorf("the setup token is one of the instance's keys; create a token of its own")
			}
		}
	}
	return nil
}

// NewSetupToken returns a new setup token: 256 random bits, base64url
// without padding (43 characters).
func NewSetupToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ParseEncryptionAtSetup reads EnvEncryptionAtSetup: on (the default) or
// off.
func ParseEncryptionAtSetup(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, fmt.Errorf("%s: want on or off, got %q", EnvEncryptionAtSetup, v)
}
