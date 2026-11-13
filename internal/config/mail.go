package config

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"strings"
)

// Outbound mail for alert mail to admins (#397, part 5).
const (
	// EnvMailURL is the relay alert mail is sent through:
	// smtps://user@relay.example:465 (TLS from the start) or
	// smtp://user@relay.example:587 (STARTTLS required). Empty turns
	// alert mail off.
	EnvMailURL = "KEEPIA_ARCHIVE_MAIL_URL"
	// EnvMailPasswordFile holds the relay password; a password in the URL
	// is refused.
	EnvMailPasswordFile = "KEEPIA_ARCHIVE_MAIL_PASSWORD_FILE" //nolint:gosec // variable name, not a credential
	// EnvMailFrom is the sender, such as "Mail Archive <archive@example.com>".
	EnvMailFrom = "KEEPIA_ARCHIVE_MAIL_FROM"
)

// Mail is the relay for alert mail. Password is a secret: never log it.
type Mail struct {
	// Host and Port of the relay; the only host alert mail goes to.
	Host string
	Port string
	// ImplicitTLS is smtps: TLS from the start. Otherwise the relay must
	// offer STARTTLS.
	ImplicitTLS bool
	// Username logs in with AUTH PLAIN; empty sends without a login.
	Username string
	Password string
	// From is the sender's address; FromHeader the From header (with a
	// display name if one was given).
	From       string
	FromHeader string
}

// Addr is host:port.
func (m *Mail) Addr() string { return net.JoinHostPort(m.Host, m.Port) }

// Target names the relay for messages: scheme, host and port, never the
// user or password.
func (m *Mail) Target() string {
	scheme := "smtp"
	if m.ImplicitTLS {
		scheme = "smtps"
	}
	return scheme + "://" + m.Addr()
}

// ParseMail checks the alert mail settings. An empty URL means alert mail
// is off; the other two set without it are refused. The password file
// follows the rules of the key files (a regular file outside dataDir that
// only its owner may access); a trailing line break is dropped.
func ParseMail(rawURL, passwordFile, from, dataDir string) (*Mail, error) {
	rawURL, passwordFile, from = strings.TrimSpace(rawURL), strings.TrimSpace(passwordFile), strings.TrimSpace(from)
	if rawURL == "" {
		for name, v := range map[string]string{EnvMailPasswordFile: passwordFile, EnvMailFrom: from} {
			if v != "" {
				return nil, fmt.Errorf("%s is set, but %s is not", name, EnvMailURL)
			}
		}
		return nil, nil
	}
	bad := func(why string) (*Mail, error) {
		return nil, fmt.Errorf("%s: %s (example: smtps://alerts@relay.example:465)", EnvMailURL, why)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return bad("not a URL")
	}
	m := &Mail{Host: u.Hostname(), Port: u.Port()}
	switch u.Scheme {
	case "smtps":
		m.ImplicitTLS = true
		if m.Port == "" {
			m.Port = "465"
		}
	case "smtp":
		if m.Port == "" {
			m.Port = "587"
		}
	default:
		return bad("want smtps:// or smtp:// (with STARTTLS)")
	}
	switch {
	case m.Host == "":
		return bad("no host")
	case u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return bad("only scheme, user, host and port are allowed")
	}
	if u.User != nil {
		if _, set := u.User.Password(); set {
			return bad("a password in the URL is refused; put it in " + EnvMailPasswordFile)
		}
		m.Username = u.User.Username()
	}
	if m.Username != "" && passwordFile == "" {
		return nil, fmt.Errorf("%s names a user, but %s is not set", EnvMailURL, EnvMailPasswordFile)
	}
	if m.Username == "" && passwordFile != "" {
		return nil, fmt.Errorf("%s is set, but %s names no user", EnvMailPasswordFile, EnvMailURL)
	}
	if passwordFile != "" {
		data, err := readKeyFile(passwordFile, dataDir, "the mail password")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvMailPasswordFile, err)
		}
		m.Password = strings.TrimRight(string(data), "\r\n")
		if m.Password == "" || strings.ContainsAny(m.Password, "\r\n\x00") {
			return nil, fmt.Errorf("%s: the file must hold the password on one line", EnvMailPasswordFile)
		}
	}
	if from == "" {
		return nil, fmt.Errorf("%s is set, but %s is not", EnvMailURL, EnvMailFrom)
	}
	addr, err := ParseMailAddress(from)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvMailFrom, err)
	}
	m.From, m.FromHeader = addr.Address, addr.String()
	return m, nil
}

// MailFromEnv is ParseMail with every input from the environment.
func MailFromEnv() (*Mail, error) {
	return ParseMail(Getenv(EnvMailURL), Getenv(EnvMailPasswordFile), Getenv(EnvMailFrom), DataDirFromEnv())
}

// ParseMailAddress checks one mail address, with or without a display
// name: exactly one address, no control characters, at most 254
// characters for the address.
func ParseMailAddress(s string) (*mail.Address, error) {
	if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, errors.New("contains a control character")
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return nil, errors.New("not one valid mail address")
	}
	if len(a.Address) > 254 || !strings.Contains(a.Address, "@") {
		return nil, errors.New("not one valid mail address")
	}
	return a, nil
}
