package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mailPasswordFile writes a relay password file with mode; not a secret.
func mailPasswordFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mail.password")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseMail(t *testing.T) {
	data := t.TempDir()
	pw := mailPasswordFile(t, "relay-test-login\n", 0o400)

	if m, err := ParseMail("", "", "", data); m != nil || err != nil {
		t.Fatalf("off: %v %v", m, err)
	}
	m, err := ParseMail("smtps://alerts@relay.example", pw, "Mail Archive <archive@example.com>", data)
	if err != nil || !m.ImplicitTLS || m.Port != "465" || m.Username != "alerts" || m.Password != "relay-test-login" ||
		m.From != "archive@example.com" || m.FromHeader != `"Mail Archive" <archive@example.com>` || m.Target() != "smtps://relay.example:465" {
		t.Fatalf("smtps: %+v %v", m, err)
	}
	m, err = ParseMail("smtp://relay.example", "", "archive@example.com", data)
	if err != nil || m.ImplicitTLS || m.Port != "587" || m.Username != "" || m.Addr() != "relay.example:587" {
		t.Fatalf("smtp without login: %+v %v", m, err)
	}
	if strings.Contains(m.Target(), "alerts") {
		t.Errorf("target names the user: %s", m.Target())
	}

	for _, c := range []struct{ url, file, from, want string }{
		{"", pw, "", EnvMailURL},
		{"", "", "archive@example.com", EnvMailURL},
		{"http://relay.example", "", "a@example.com", "smtps://"},
		{"smtps://alerts:inline@relay.example", pw, "a@example.com", "password in the URL"},
		{"smtps://alerts@relay.example", "", "a@example.com", EnvMailPasswordFile},
		{"smtps://relay.example", pw, "a@example.com", "names no user"},
		{"smtps://relay.example/path", "", "a@example.com", "only scheme"},
		{"smtps://relay.example?x=1", "", "a@example.com", "only scheme"},
		{"smtps://:465", "", "a@example.com", "no host"},
		{"smtps://relay.example", "", "", EnvMailFrom},
		{"smtps://relay.example", "", "a@example.com, b@example.com", "one valid"},
		{"smtps://relay.example", "", "a@example.com\r\nBcc: b@example.com", "control"},
		{"smtps://alerts@relay.example", mailPasswordFile(t, "relay-test-login", 0o644), "a@example.com", "0644"},
		{"smtps://alerts@relay.example", filepath.Join(data, "pw"), "a@example.com", ""},
		{"smtps://alerts@relay.example", mailPasswordFile(t, "two\nlines", 0o400), "a@example.com", "one line"},
	} {
		if c.file == filepath.Join(data, "pw") {
			if err := os.WriteFile(c.file, []byte("x"), 0o400); err != nil {
				t.Fatal(err)
			}
			c.want = "inside the data directory"
		}
		_, err := ParseMail(c.url, c.file, c.from, data)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q %q %q: %v, want %q", c.url, c.file, c.from, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "relay-test-login") {
			t.Errorf("error shows the password: %v", err)
		}
	}
}
