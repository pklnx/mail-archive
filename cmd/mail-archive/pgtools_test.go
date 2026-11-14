package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/crypto"
)

// The tests here set PG* variables with t.Setenv and are not parallel. No
// test connects anywhere: the hosts are never dialled, and the fake tools
// only record their arguments and environment.

// testPgTool is the tool at path, checked for databaseURL in the
// environment of the test.
func testPgTool(t *testing.T, path, databaseURL string, serverMajor int) *pgTool {
	t.Helper()
	conn, err := resolvePgToolConn(filepath.Base(path), databaseURL, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	tool, err := checkPgTool(context.Background(), filepath.Base(path), path, conn, serverMajor)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// pgToolEnv is the environment the tools get for databaseURL.
func pgToolEnv(t *testing.T, databaseURL string) []string {
	t.Helper()
	conn, err := resolvePgToolConn("pg_dump", databaseURL, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	return conn.env(os.Environ())
}

// pgVars are the PG* entries of env, by name.
func pgVars(env []string) map[string]string {
	vars := map[string]string{}
	for _, kv := range env {
		if name, value, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "PG") {
			vars[name] = value
		}
	}
	return vars
}

// cleanPgEnv unsets every variable pgx reads and gives the test its own
// HOME, so that nothing of the machine's environment counts.
func cleanPgEnv(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE", "PGAPPNAME", "PGCONNECT_TIMEOUT",
		"PGSSLMODE", "PGSSLKEY", "PGSSLCERT", "PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION", "PGTARGETSESSIONATTRS",
		"PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS", "PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION", "PGCHANNELBINDING", "PGREQUIREAUTH"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// testCerts writes a CA certificate and a client certificate signed by it
// with its key (mode 0600) into dir.
func testCerts(t *testing.T, dir, name string) (ca, cert, key string) {
	t.Helper()
	newKey := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	write := func(path, kind string, der []byte, mode os.FileMode) {
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), mode); err != nil {
			t.Fatal(err)
		}
	}
	caKey := newKey()
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name + " CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	clientKey := newKey()
	clientDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "mailarchive"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, caTmpl, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, cert, key = filepath.Join(dir, name+"-ca.pem"), filepath.Join(dir, name+"-client.pem"), filepath.Join(dir, name+"-client.key")
	write(ca, "CERTIFICATE", caDER, 0o644)
	write(cert, "CERTIFICATE", clientDER, 0o644)
	write(key, "PRIVATE KEY", keyDER, 0o600)
	return ca, cert, key
}

// withoutSSLMode is the test database's URL without its sslmode, so that
// a PGSSLMODE of the process decides the app's connection.
func withoutSSLMode(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Del("sslmode")
	u.RawQuery = q.Encode()
	return u.String()
}

// TestBackupRefusesTLSEnv (regression, #540): PGSSLMODE in the process and
// a database URL without sslmode gave the app's connection that mode, but
// pg_dump ran without it and fell back to libpq's prefer. Now backup
// refuses before pg_dump runs and before the data.backup audit entry, and
// the message names the variable. (PGSSLMODE=disable, so that the app's
// own connection reaches the test database; any mode is refused alike.)
func TestBackupRefusesTLSEnv(t *testing.T) {
	s := newArchiveSetup(t)
	secret, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, secret)
	pgDump, logDir := fakePgDump(t, "18.1")
	t.Setenv("PATH", filepath.Dir(pgDump)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(config.EnvDatabaseURL, withoutSSLMode(t, s.url))
	t.Setenv("PGSSLMODE", "disable")
	before := len(auditLog(t, s.st))
	dir := filepath.Join(t.TempDir(), "b")
	err := captureErr(t, func() error { return runCmd(t, newBackupCmd(), "", dir) })
	if err == nil || !strings.Contains(err.Error(), "PGSSLMODE is set in the environment but would not reach pg_dump") ||
		!strings.Contains(err.Error(), "Backups without Docker") {
		t.Fatalf("backup: %v", err)
	}
	if strings.Contains(err.Error(), "disable") {
		t.Errorf("the message holds the value: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "version-env")); !errors.Is(err, os.ErrNotExist) {
		t.Error("pg_dump ran")
	}
	for _, r := range auditLog(t, s.st)[before:] {
		t.Errorf("logged %s", r.Action)
	}
	// --note-only, for ./ma backup, runs no tool and is not refused.
	if err := captureErr(t, func() error { return runCmd(t, newBackupCmd(), "", "--note-only", "mailarchive-x.dump") }); err != nil {
		t.Errorf("--note-only: %v", err)
	}
}

// TestRestoreDumpRefusesTLSEnv (regression, #540): restore-dump refuses
// the same way, before the dump is opened (so before the archive key
// unwraps a data key) and before pg_restore runs.
func TestRestoreDumpRefusesTLSEnv(t *testing.T) {
	newArchiveSetup(t)
	secret, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, secret)
	switchDatabase(t)
	t.Setenv(config.EnvDatabaseURL, withoutSSLMode(t, os.Getenv(config.EnvDatabaseURL)))
	pgRestore, logDir := fakePgRestore(t, "18.0")
	t.Setenv("PATH", filepath.Dir(pgRestore)+string(os.PathListSeparator)+os.Getenv("PATH"))
	opened := false
	t.Cleanup(func() { openBackupStreamFunc = openBackupStream })
	openBackupStreamFunc = func(in io.Reader, keyringPath string) (*blobcrypt.StreamReader, error) {
		opened = true
		return openBackupStream(in, keyringPath)
	}
	dump := filepath.Join(t.TempDir(), "mailarchive-x.dump.enc")
	if err := os.WriteFile(dump, []byte(blobcrypt.Magic+"rest of a dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSSLMODE", "disable")
	_, err := runRestoreDump(t, dump)
	if err == nil || !strings.Contains(err.Error(), "PGSSLMODE is set in the environment but would not reach pg_restore") {
		t.Fatalf("restore-dump: %v", err)
	}
	if opened {
		t.Error("the dump was opened")
	}
	if _, err := os.Stat(filepath.Join(logDir, "version-env")); !errors.Is(err, os.ErrNotExist) {
		t.Error("pg_restore ran")
	}
}

// pgToolDefaultEnv are the TLS variables every tool gets where the URL
// sets nothing, with HOME home: the defaults named explicitly, the
// certificate files under home as paths, and GSSAPI encryption off.
func pgToolDefaultEnv(home string) map[string]string {
	dir := filepath.Join(home, ".postgresql")
	return map[string]string{
		"PGSSLMODE": "prefer", "PGSSLROOTCERT": filepath.Join(dir, "root.crt"), "PGSSLCERT": filepath.Join(dir, "postgresql.crt"),
		"PGSSLKEY": filepath.Join(dir, "postgresql.key"), "PGSSLSNI": "1", "PGSSLNEGOTIATION": "postgres", "PGTARGETSESSIONATTRS": "any",
		"PGCHANNELBINDING": "prefer", "PGGSSENCMODE": "disable",
	}
}

// TestPgToolConn: what the tools get, and what they refuse.
func TestPgToolConn(t *testing.T) {
	home := cleanPgEnv(t)
	certDir := filepath.Join(t.TempDir(), "with space")
	if err := os.Mkdir(certDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ca, cert, key := testCerts(t, certDir, "a")

	for _, c := range []struct {
		name, url string
		env       map[string]string
		want      map[string]string // the PG* variables of the tool that differ from pgToolDefaultEnv
	}{
		{"defaults are left out", "postgres://u:pw@h/db", nil,
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGPASSWORD": "pw"}},
		// pgx and libpq take the last occurrence of a key.
		{"repeated key", "postgres://u@h/db?sslmode=disable&sslmode=verify-full", nil,
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGSSLMODE": "verify-full"}},
		// The URL sets the key, so the process's variable does not count.
		{"URL wins", "postgres://u@h/db?sslmode=verify-full", map[string]string{"PGSSLMODE": "disable"},
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGSSLMODE": "verify-full"}},
		// pgx's URL alias: ssl=true is sslmode=require where it comes
		// after the last sslmode.
		{"ssl=true", "postgres://u@h/db?ssl=true", map[string]string{"PGSSLMODE": "disable"},
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGSSLMODE": "require"}},
		{"ssl=true after sslmode", "postgres://u@h/db?sslmode=disable&ssl=true", nil,
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGSSLMODE": "require"}},
		{"sslmode after ssl=true", "postgres://u@h/db?ssl=true&sslmode=disable", nil,
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGSSLMODE": "disable"}},
		// Host and port lists in their order, the TLS fallback of prefer
		// once per host.
		{"host list", "postgres://u@h1:5432,h2:5433/db?target_session_attrs=read-write", nil,
			map[string]string{"PGHOST": "h1,h2", "PGPORT": "5432,5433", "PGUSER": "u", "PGDATABASE": "db", "PGTARGETSESSIONATTRS": "read-write"}},
		// PGHOST and PGPORT of the process count, as for the app.
		{"PGHOST counts", "postgres://u@/db", map[string]string{"PGHOST": "envhost", "PGPORT": "6000"},
			map[string]string{"PGHOST": "envhost", "PGPORT": "6000", "PGUSER": "u", "PGDATABASE": "db"}},
		// A quoted keyword/value path with a space, and the client
		// certificate.
		{"keyword/value", "host=h user=u dbname=db sslmode=verify-ca sslrootcert='" + ca + "' sslcert='" + cert + "' sslkey=" +
			strings.ReplaceAll(key, " ", `\ `) + " channel_binding=require require_auth=scram-sha-256 sslsni=0", nil,
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGSSLMODE": "verify-ca",
				"PGSSLROOTCERT": ca, "PGSSLCERT": cert, "PGSSLKEY": key, "PGCHANNELBINDING": "require", "PGREQUIREAUTH": "scram-sha-256", "PGSSLSNI": "0"}},
		// pgx raises the default prefer to require for a direct
		// negotiation; libpq would refuse prefer at connect time, after
		// the audit entry.
		{"sslnegotiation", "postgres://u@h/db?sslnegotiation=direct", nil,
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGSSLNEGOTIATION": "direct", "PGSSLMODE": "require"}},
		// pgx makes sslrootcert=system verify-full whatever the mode;
		// libpq would refuse require at connect time.
		{"sslrootcert=system", "postgres://u@h/db?sslrootcert=system&sslmode=require", nil,
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGSSLROOTCERT": "system", "PGSSLMODE": "verify-full"}},
		// GSSAPI encryption stays off: the process cannot turn it on.
		{"PGGSSENCMODE of the process", "postgres://u@h/db?sslmode=verify-full", map[string]string{"PGGSSENCMODE": "require"},
			map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGUSER": "u", "PGDATABASE": "db", "PGSSLMODE": "verify-full"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for name, value := range c.env {
				t.Setenv(name, value)
			}
			env := pgToolEnv(t, c.url)
			got := pgVars(env)
			want := pgToolDefaultEnv(home)
			for name, value := range c.want {
				want[name] = value
			}
			c.want = want
			// Every tool gets GSSAPI encryption off, once.
			n := 0
			for _, kv := range env {
				if strings.HasPrefix(kv, "PGGSSENCMODE=") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("PGGSSENCMODE %d times: %v", n, env)
			}
			if len(got) != len(c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
			for name, value := range c.want {
				if got[name] != value {
					t.Errorf("%s=%q, want %q (all: %v)", name, got[name], value, got)
				}
			}
		})
	}

	for _, c := range []struct {
		name, url string
		env       map[string]string
		want      string
	}{
		{"PGSSLMODE", "postgres://u@h/db", map[string]string{"PGSSLMODE": "verify-full"}, "PGSSLMODE is set in the environment but would not reach pg_dump"},
		{"PGSSLROOTCERT", "postgres://u@h/db?sslmode=verify-full", map[string]string{"PGSSLROOTCERT": ca}, "PGSSLROOTCERT is set"},
		{"PGTARGETSESSIONATTRS", "postgres://u@h/db", map[string]string{"PGTARGETSESSIONATTRS": "read-write"}, "PGTARGETSESSIONATTRS is set"},
		{"PGREQUIREAUTH", "postgres://u@h/db", map[string]string{"PGREQUIREAUTH": "scram-sha-256"}, "PGREQUIREAUTH is set"},
		{"PGSERVICE", "postgres://u@h/db", map[string]string{"PGSERVICE": "svc"}, "PGSERVICE is set in the environment but cannot reach"},
		{"PGSERVICEFILE", "postgres://u@h/db", map[string]string{"PGSERVICEFILE": "/nonexistent"}, "PGSERVICEFILE is set"},
		{"PGSSLPASSWORD", "postgres://u@h/db?sslmode=require", map[string]string{"PGSSLPASSWORD": "pw"}, "PGSSLPASSWORD is set"},
		{"service", "host=h user=u service=svc", nil, "sets service, which cannot reach pg_dump"},
		{"servicefile", "postgres://u@h/db?servicefile=/nonexistent", nil, "sets servicefile"},
		{"sslpassword", "postgres://u@h/db?sslpassword=pw", nil, "sets sslpassword"},
		// Only the literal ssl=true of a URL is pgx's alias.
		{"ssl=false", "postgres://u@h/db?ssl=false", nil, "sets ssl to something other than the URL alias"},
		{"ssl in keyword/value", "host=h user=u ssl=true", nil, "sets ssl to something other than the URL alias"},
		// pgx accepts it, libpq refuses it when the tool connects.
		{"sslnegotiation=direct with allow", "postgres://u@h/db?sslnegotiation=direct&sslmode=allow", nil, "sets sslnegotiation=direct with a weaker sslmode"},
		// pgx reads an empty sslrootcert as none, libpq as the default
		// file: with the default file there, the cross-check refuses.
		{"empty sslrootcert", "postgres://u@h/db?sslmode=require&sslrootcert=", map[string]string{"HOME": filepath.Join(home, "with-root")},
			"pg_dump would connect differently from the app: the TLS of connection attempt 1 differs (certificate chain verification)"},
	} {
		t.Run("refuses "+c.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Join(home, "with-root", ".postgresql"), 0o700); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(ca) //nolint:gosec // the test's own file
			if err := os.WriteFile(filepath.Join(home, "with-root", ".postgresql", "root.crt"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			for name, value := range c.env {
				t.Setenv(name, value)
			}
			_, err := resolvePgToolConn("pg_dump", c.url, os.Environ())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			for _, value := range c.env {
				if strings.Contains(err.Error(), value) && c.name != "empty sslrootcert" {
					t.Errorf("the message holds a value: %v", err)
				}
			}
			if strings.Contains(err.Error(), "pw") {
				t.Errorf("the message holds the password: %v", err)
			}
		})
	}
}

// TestPgToolCheckStringIgnoresEnv: the cross-check parses the tools'
// settings in the process environment. Every key it compares is named in
// the string, so no PG* variable of the process changes the parse.
func TestPgToolCheckStringIgnoresEnv(t *testing.T) {
	cleanPgEnv(t)
	ca, cert, key := testCerts(t, t.TempDir(), "a")
	for _, raw := range []string{
		"postgres://u@h/db",
		"postgres://u@h1,h2:5433/db?sslmode=verify-full&sslrootcert=" + url.QueryEscape(ca) + "&sslcert=" + url.QueryEscape(cert) +
			"&sslkey=" + url.QueryEscape(key) + "&target_session_attrs=read-write&channel_binding=require&require_auth=scram-sha-256",
	} {
		conn, err := resolvePgToolConn("pg_dump", raw, os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		clean, err := pgconn.ParseConfig(conn.checkString())
		if err != nil {
			t.Fatal(err)
		}
		other := t.TempDir()
		oca, ocert, okey := testCerts(t, other, "b")
		for name, value := range map[string]string{
			"PGHOST": "elsewhere", "PGPORT": "1", "PGUSER": "mallory", "PGDATABASE": "other",
			"PGSSLMODE": "allow", "PGSSLROOTCERT": oca, "PGSSLCERT": ocert, "PGSSLKEY": okey, "PGSSLSNI": "0",
			"PGSSLNEGOTIATION": "direct", "PGTARGETSESSIONATTRS": "standby", "PGCHANNELBINDING": "disable", "PGREQUIREAUTH": "password",
		} {
			t.Setenv(name, value)
		}
		got, err := pgconn.ParseConfig(conn.checkString())
		if err != nil {
			t.Fatal(err)
		}
		if err := comparePgConfigs(clean, got); err != nil {
			t.Errorf("%s: the environment changed the parse: %v", raw, err)
		}
		cleanPgEnv(t)
	}
}

// TestComparePgConfigs: parses that differ in any compared setting are
// refused; equal ones pass.
func TestComparePgConfigs(t *testing.T) {
	cleanPgEnv(t)
	dir := t.TempDir()
	ca1, cert1, key1 := testCerts(t, dir, "one")
	ca2, cert2, key2 := testCerts(t, dir, "two")
	parse := func(s string) *pgconn.Config {
		t.Helper()
		cfg, err := pgconn.ParseConfig(s)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	base := "user=u dbname=db host=h1,h2 port=5432,5433 target_session_attrs=read-write sslmode=verify-ca sslrootcert=" + ca1 +
		" sslcert=" + cert1 + " sslkey=" + key1
	if err := comparePgConfigs(parse(base), parse(base)); err != nil {
		t.Errorf("equal: %v", err)
	}
	for _, c := range []struct{ name, a, b, want string }{
		// prefer adds a plaintext attempt to the TLS one of require.
		{"prefer against require", "host=h sslmode=require", "host=h sslmode=prefer", "number of connection attempts"},
		// The same single attempt, server name (SNI) and no root CA: only
		// the verification itself differs.
		{"require against verify-full", "host=db.example sslmode=verify-full", "host=db.example sslmode=require", "certificate verification"},
		{"allow against prefer", "host=h sslmode=prefer", "host=h sslmode=allow", "attempt 1 differs (TLS on or off)"},
		{"mode", "host=h sslmode=verify-full", "host=h sslmode=verify-ca", "attempt 1 differs"},
		{"root CA", base, strings.Replace(base, ca1, ca2, 1), "root certificates"},
		{"client certificate", base, strings.Replace(strings.Replace(base, cert1, cert2, 1), key1, key2, 1), "client certificate"},
		{"host list", base, strings.Replace(strings.Replace(base, "h1,h2", "h1", 1), "5432,5433", "5432", 1), "number of connection attempts"},
		{"host order", base, strings.Replace(strings.Replace(base, "h1,h2", "h2,h1", 1), "5432,5433", "5433,5432", 1), "host or port of connection attempt 1"},
		{"target_session_attrs", base, strings.Replace(base, "read-write", "any", 1), "target_session_attrs"},
		{"sslnegotiation", "host=h sslmode=require", "host=h sslmode=require sslnegotiation=direct", "ALPN"},
		{"channel_binding", "host=h", "host=h channel_binding=require", "channel_binding"},
		{"require_auth", "host=h", "host=h require_auth=password", "require_auth"},
		{"server name", "host=h sslmode=require", "host=h sslmode=require sslsni=0", "server name"},
		{"user", "host=h user=a", "host=h user=b", "user"},
	} {
		if err := comparePgConfigs(parse(c.a), parse(c.b)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	// pgx's empty sslnegotiation is libpq's postgres.
	if err := comparePgConfigs(parse("host=h"), parse("host=h sslnegotiation=postgres")); err != nil {
		t.Errorf("sslnegotiation default: %v", err)
	}
}

// TestPgToolVersionGate: a setting that an older libpq ignores without an
// error refuses a tool of that version after --version, before it runs;
// the values count, not the keys.
func TestPgToolVersionGate(t *testing.T) {
	cleanPgEnv(t)
	for _, c := range []struct {
		url, version string
		want         string // "" accepts
	}{
		{"postgres://u@h/db?sslnegotiation=direct", "16.4", "pg_dump is version 16, but sslnegotiation=direct in KEEPIA_ARCHIVE_DATABASE_URL needs 17 or newer"},
		{"postgres://u@h/db?sslnegotiation=direct", "17.2", ""},
		{"postgres://u@h/db?require_auth=scram-sha-256", "15.8", "require_auth in KEEPIA_ARCHIVE_DATABASE_URL needs 16"},
		{"postgres://u@h/db?require_auth=scram-sha-256", "16.0", ""},
		{"postgres://u@h/db?sslrootcert=system", "15.8", "sslrootcert=system in KEEPIA_ARCHIVE_DATABASE_URL needs 16"},
		{"postgres://u@h/db?sslnegotiation=postgres&sslrootcert=&require_auth=", "14.1", ""},
	} {
		pgDump, logDir := fakePgDump(t, c.version)
		conn, err := resolvePgToolConn("pg_dump", c.url, os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		_, err = checkPgTool(context.Background(), "pg_dump", pgDump, conn, 14)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s with %s: got %v, want %q", c.url, c.version, err, c.want)
		}
		if _, err := os.Stat(filepath.Join(logDir, "version-env")); err != nil {
			t.Errorf("%s: --version did not run", c.url)
		}
		if _, err := os.Stat(filepath.Join(logDir, "args")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: pg_dump ran", c.url)
		}
	}
}

// TestPgRestoreArgv: pg_restore's arguments hold no password, and the
// database name is quoted, so that a name with "=" is not read as a
// connection string.
func TestPgRestoreArgv(t *testing.T) {
	cleanPgEnv(t)
	pgRestore, logDir := fakePgRestore(t, "18.0")
	job := restoreJob{
		PgRestore: testPgTool(t, pgRestore, "postgres://u:"+envMarker+"@h/odd%3Dname%27s?sslmode=disable", 18),
		In:        strings.NewReader("PGDMP"), Name: "x.dump.enc", Sealed: true,
	}
	if err := runRestore(context.Background(), job, io.Discard); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(logDir, "args")) //nolint:gosec // the test's own log
	if strings.Contains(string(args), envMarker) || !strings.Contains(string(args), `--dbname=dbname='odd=name\'s'`+"\n") {
		t.Errorf("args %q", args)
	}
	env, _ := os.ReadFile(filepath.Join(logDir, "env")) //nolint:gosec // the test's own log
	if !strings.Contains(string(env), "PGPASSWORD="+envMarker+"\n") {
		t.Error("the password did not reach pg_restore's environment")
	}
}

// TestConnStringSettings: the parser of the connection string reads keys
// as pgx does.
func TestConnStringSettings(t *testing.T) {
	for in, want := range map[string]map[string]string{
		"postgres://u:p%3F@h/db?sslmode=verify-full&sslrootcert=%2Fca%20dir%2Fca.pem": {"sslmode": "verify-full", "sslrootcert": "/ca dir/ca.pem"},
		"postgresql://u@h?sslmode=require":                                            {"sslmode": "require"},
		"postgres://u@[::1]:5432,h2/db?sslmode=disable":                               {"sslmode": "disable"},
		"postgres://u:pass?word@h/db?sslmode=require":                                 {"sslmode": "require"},
		"postgres://u@h/db?ssl=true&ssl=false":                                        {"ssl": "false"},
		"postgres://u@h/db?ssl=false&ssl=true":                                        {"sslmode": "require"},
		`host=h sslmode = 'verify-ca' sslkey='/k\'s dir/k.pem'`:                       {"host": "h", "sslmode": "verify-ca", "sslkey": "/k's dir/k.pem"},
		`host=h sslcert=/a\ b.pem sslmode=disable sslmode=allow`:                      {"host": "h", "sslcert": "/a b.pem", "sslmode": "allow"},
	} {
		got, err := connStringSettings(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s=%q, want %q (all: %v)", in, k, got[k], v, got)
			}
		}
		if _, ok := got["ssl"]; ok != (want["ssl"] != "") {
			t.Errorf("%s: ssl %v", in, got)
		}
	}
	for _, in := range []string{"postgres://u@h/db?sslmode", "postgres://u@h/db?sslmode=%zz", "host=h sslmode='open", "host"} {
		if _, err := connStringSettings(in); err == nil {
			t.Errorf("%s: accepted", in)
		}
	}
}

// TestPgToolHomeCertFiles (regression): with the certificate files under
// HOME/.postgresql and a URL that names none, pgx verifies the server with
// root.crt and sends postgresql.crt; the tools get the same files as
// paths, so that a libpq that looks elsewhere for its defaults (libpq 14:
// the account's home directory) uses them too.
func TestPgToolHomeCertFiles(t *testing.T) {
	home := cleanPgEnv(t)
	dir := filepath.Join(home, ".postgresql")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ca, cert, key := testCerts(t, t.TempDir(), "home")
	files := map[string]string{ca: "root.crt", cert: "postgresql.crt", key: "postgresql.key"}
	for from, name := range files {
		data, err := os.ReadFile(from) //nolint:gosec // the test's own file
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := pgVars(pgToolEnv(t, "postgres://u@h/db?sslmode=require"))
	for name, file := range map[string]string{"PGSSLROOTCERT": "root.crt", "PGSSLCERT": "postgresql.crt", "PGSSLKEY": "postgresql.key"} {
		if got[name] != filepath.Join(dir, file) {
			t.Errorf("%s=%q, want %s", name, got[name], filepath.Join(dir, file))
		}
	}

	// A certificate without its key: pgx ignores both, libpq refuses at
	// connect time, so the commands refuse before they start.
	if err := os.Remove(filepath.Join(dir, "postgresql.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePgToolConn("pg_dump", "postgres://u@h/db?sslmode=require", os.Environ()); err == nil ||
		!strings.Contains(err.Error(), "only one of postgresql.crt and postgresql.key") {
		t.Errorf("certificate without key: %v", err)
	}
	if _, err := resolvePgToolConn("pg_dump", "postgres://u@h/db?sslmode=require&sslcert="+url.QueryEscape(cert)+"&sslkey="+url.QueryEscape(key), os.Environ()); err != nil {
		t.Errorf("certificate files in the URL: %v", err)
	}

	// Without HOME, pgx looks for no file and libpq in the account's home
	// directory: refused where TLS is used, unless the URL names the files.
	t.Setenv("HOME", "")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePgToolConn("pg_dump", "postgres://u@h/db?sslmode=require", os.Environ()); err == nil || !strings.Contains(err.Error(), "HOME is not set") {
		t.Errorf("without HOME: %v", err)
	}
	if _, err := resolvePgToolConn("pg_dump", "postgres://u@h/db?sslmode=disable", os.Environ()); err != nil {
		t.Errorf("without HOME, without TLS: %v", err)
	}
	full := "postgres://u@h/db?sslmode=verify-ca&sslrootcert=" + url.QueryEscape(ca) + "&sslcert=" + url.QueryEscape(cert) + "&sslkey=" + url.QueryEscape(key)
	if _, err := resolvePgToolConn("pg_dump", full, os.Environ()); err != nil {
		t.Errorf("without HOME, files in the URL: %v", err)
	}
}

// TestPgToolEnvMatchesCheckString: the tools get exactly the settings the
// cross-check compares. Key by key, the variable equals the value in
// checkString; a certificate file that does not exist is empty there, and
// only an empty value is left out of the environment. A key in one but
// not the other fails.
func TestPgToolEnvMatchesCheckString(t *testing.T) {
	home := cleanPgEnv(t)
	ca, cert, key := testCerts(t, t.TempDir(), "m")
	envName := map[string]string{"database": "PGDATABASE"}
	for _, k := range append(append([]struct{ key, env string }{}, pgToolConnKeys...), pgToolTLSKeys...) {
		envName[k.key] = k.env
	}
	check := func(raw string) {
		t.Helper()
		conn, err := resolvePgToolConn("pg_dump", raw, os.Environ())
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		cs, err := connStringSettings(conn.checkString())
		if err != nil {
			t.Fatal(err)
		}
		vars := pgVars(conn.env(os.Environ()))
		seen := map[string]bool{"PGPASSWORD": true}
		for _, k := range pgToolFixedKeys {
			if vars[k.env] != k.value {
				t.Errorf("%s: %s=%q, want %q", raw, k.env, vars[k.env], k.value)
			}
			seen[k.env] = true
		}
		for key, cv := range cs {
			name, ok := envName[key]
			if !ok || key == "password" {
				t.Errorf("%s: checkString names %s, which the tools do not get", raw, key)
				continue
			}
			seen[name] = true
			ev, inEnv := vars[name]
			switch {
			case !inEnv && cv != "":
				t.Errorf("%s: %s=%q is compared but not passed on", raw, key, cv)
			case inEnv && ev != cv && (cv != "" || fileExists(ev)):
				t.Errorf("%s: the tools get %s=%q, the cross-check compares %q", raw, name, ev, cv)
			}
		}
		for name := range vars {
			if !seen[name] {
				t.Errorf("%s: the tools get %s, which the cross-check does not compare", raw, name)
			}
		}
	}
	urls := []string{
		"postgres://u:pw@h/db",
		"postgres://u@h1,h2:5433/db?sslmode=verify-ca&sslrootcert=" + url.QueryEscape(ca) + "&sslcert=" + url.QueryEscape(cert) + "&sslkey=" +
			url.QueryEscape(key) + "&target_session_attrs=read-write&channel_binding=require&require_auth=scram-sha-256&sslsni=0",
		"postgres://u@h/db?sslnegotiation=direct",
		"postgres://u@h/db?sslrootcert=system",
		"host=h user=u dbname=db sslmode=disable",
	}
	for _, raw := range urls {
		check(raw)
	}
	// And with the default files under HOME.
	dir := filepath.Join(home, ".postgresql")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for from, name := range map[string]string{ca: "root.crt", cert: "postgresql.crt", key: "postgresql.key"} {
		data, _ := os.ReadFile(from) //nolint:gosec // the test's own file
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range append(urls, "postgres://u@h/db?sslmode=require") {
		check(raw)
	}
}
