package auth

import (
	"strconv"
	"strings"
)

// SMTP credentials (#395) let a mail server log in to the SMTP server of
// an smtp account. The user name is "smtp-" and the account's ID: not a
// secret, unique and never changing. The secret is "mas_" and 32 random
// bytes in lower-case base32, built and stored like an API key (apikey.go):
// only its SHA-256 and its last 4 characters are kept, and it is shown once.
// The prefix differs from API keys, so neither is taken for the other.

// SMTPSecretPrefix starts every SMTP secret.
const SMTPSecretPrefix = "mas_"

// SMTPUsernamePrefix starts every SMTP user name.
const SMTPUsernamePrefix = "smtp-"

// NewSMTPSecret returns a new secret, its hash for the database and its
// hint.
func NewSMTPSecret() (secret string, hash []byte, hint string, err error) {
	return newPrefixedSecret(SMTPSecretPrefix)
}

// HashSMTPSecret returns the database key of an SMTP secret.
func HashSMTPSecret(secret string) []byte { return hashSecret(secret) }

// ValidSMTPSecretFormat reports whether secret looks like an SMTP secret:
// the prefix and 52 characters from a-z and 2-7, case-sensitive.
func ValidSMTPSecretFormat(secret string) bool { return validPrefixedSecret(secret, SMTPSecretPrefix) }

// SMTPUsername returns the SMTP user name of the account with ID id.
func SMTPUsername(accountID int64) string {
	return SMTPUsernamePrefix + strconv.FormatInt(accountID, 10)
}

// ParseSMTPUsername returns the account ID of an SMTP user name. Only the
// form SMTPUsername writes is accepted: "smtp-" and 1 to 19 digits without
// a leading zero, at most the largest int64, so every accepted name maps to
// one ID and back.
func ParseSMTPUsername(name string) (int64, bool) {
	digits, ok := strings.CutPrefix(name, SMTPUsernamePrefix)
	if !ok || len(digits) == 0 || len(digits) > 19 || digits[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}
