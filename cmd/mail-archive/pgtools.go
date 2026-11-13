package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/config"
)

// The connection of pg_dump and pg_restore without Docker Compose (#540).
//
// The tools get their whole environment from this process (config.ChildEnv),
// so no PG* variable of the process reaches them. Host, port, user, database
// and password come from the app's own parse of MAIL_ARCHIVE_DATABASE_URL
// (pgconn.ParseConfig, which reads PGHOST, PGPORT, PGUSER, PGDATABASE,
// PGPASSWORD and the passfile as the app's connection does). The TLS and
// authentication settings come from the connection string only. Where the
// process environment would give the app a setting the tools cannot get,
// the commands refuse, and a cross-check proves that what the tools get
// yields the same connection attempts as the app's connection.

// pgToolTLSKeys are the TLS and authentication keys of the connection
// string that the tools get, as libpq variables. Their order is the order
// of the variables in the tools' environment.
var pgToolTLSKeys = []struct{ key, env string }{
	{"sslmode", "PGSSLMODE"},
	{"sslrootcert", "PGSSLROOTCERT"},
	{"sslcert", "PGSSLCERT"},
	{"sslkey", "PGSSLKEY"},
	{"sslsni", "PGSSLSNI"},
	{"sslnegotiation", "PGSSLNEGOTIATION"},
	{"target_session_attrs", "PGTARGETSESSIONATTRS"},
	{"channel_binding", "PGCHANNELBINDING"},
	{"require_auth", "PGREQUIREAUTH"},
}

// pgToolConnKeys are the other keys the tools get, as libpq variables.
var pgToolConnKeys = []struct{ key, env string }{
	{"host", "PGHOST"},
	{"port", "PGPORT"},
	{"user", "PGUSER"},
	{"dbname", "PGDATABASE"},
	{"password", "PGPASSWORD"},
}

// pgToolFixedKeys are settings the tools always get, whatever the
// process or the connection string says. gssencmode=disable: libpq built
// with GSSAPI prefers GSSAPI encryption to TLS, whatever sslmode says,
// where Kerberos credentials and the server allow it; pgx, the app's
// driver, never uses it. It is not in the cross-check string, which pgx
// would send as a runtime parameter.
var pgToolFixedKeys = []struct{ key, env, value string }{
	{"gssencmode", "PGGSSENCMODE", "disable"},
}

// pgToolRefusedEnv are variables pgx reads that cannot reach the tools at
// all: a service and its file (the tools would read other settings than the
// app), and the client key's password (libpq has no variable for it, and
// the command line is ruled out).
var pgToolRefusedEnv = []string{"PGSERVICE", "PGSERVICEFILE", "PGSSLPASSWORD"}

// pgToolRefusedKeys are the connection-string keys refused for the same
// reasons.
var pgToolRefusedKeys = []string{"service", "servicefile", "sslpassword"}

// pgToolDocs points the operator at the section that explains the inputs.
const pgToolDocs = `see "Backups without Docker" in the operations guide`

// pgToolConn is the connection of pg_dump or pg_restore: one map of libpq
// keywords, from which both the tools' environment (env) and the string
// of the cross-check (checkString) are built: the tools get exactly the
// settings the cross-check compares. It holds host, port, user, dbname,
// the password when there is one, every TLS key with its value (the
// connection string's, else the default pgx takes, the certificate files
// under HOME/.postgresql named as paths), and gssencmode.
type pgToolConn struct {
	settings map[string]string
}

// resolvePgToolConn resolves the connection of tool (pg_dump or
// pg_restore) for databaseURL in the process environment environ
// (os.Environ). It refuses where the tool would connect differently from
// the app's own connection. Its errors name variables and keys, never a
// value.
func resolvePgToolConn(tool, databaseURL string, environ []string) (*pgToolConn, error) {
	given, err := connStringSettings(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.EnvDatabaseURL, err)
	}
	// Before the app's parse, which reads a service file.
	if err := refusePgToolInputs(tool, given, environ); err != nil {
		return nil, err
	}
	app, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.EnvDatabaseURL, err)
	}
	conn := &pgToolConn{settings: map[string]string{}}
	hosts, ports := pgAttemptHosts(app)
	conn.settings["host"] = strings.Join(hosts, ",")
	conn.settings["port"] = strings.Join(ports, ",")
	conn.settings["user"] = app.User
	conn.settings["dbname"] = app.Database
	if app.Password != "" {
		conn.settings["password"] = app.Password
	}
	defaults, homeOK := libpqTLSDefaults()
	for _, k := range pgToolTLSKeys {
		// An empty value is the default. Where pgx reads an empty value
		// differently from the default (an empty sslrootcert is no root
		// certificate for pgx), the cross-check refuses.
		v := given[k.key]
		if v == "" {
			v = defaults[k.key]
		}
		conn.settings[k.key] = v
	}
	if usesTLS(app) {
		if err := refuseHomeDefaults(tool, given, homeOK); err != nil {
			return nil, err
		}
	}
	if err := conn.derivePgxMode(tool); err != nil {
		return nil, err
	}
	for _, k := range pgToolFixedKeys {
		conn.settings[k.key] = k.value
	}
	tools, err := pgconn.ParseConfig(conn.checkString())
	if err != nil {
		return nil, fmt.Errorf("the connection settings of %s do not parse (%w); %s", tool, err, pgToolDocs)
	}
	if err := comparePgConfigs(app, tools); err != nil {
		return nil, fmt.Errorf("%s would connect differently from the app: %w; put the TLS settings into %s (%s)", tool, err, config.EnvDatabaseURL, pgToolDocs)
	}
	return conn, nil
}

// usesTLS reports whether any connection attempt of cfg uses TLS.
func usesTLS(cfg *pgconn.Config) bool {
	for _, a := range pgAttempts(cfg) {
		if a.TLSConfig != nil {
			return true
		}
	}
	return false
}

// refuseHomeDefaults refuses default certificate files that the tools
// cannot be given exactly: without HOME pgx looks for none while libpq
// looks in the account's home directory; and a client certificate under
// HOME/.postgresql without its key (or the key without the certificate),
// which pgx ignores and libpq refuses at connect time, after the audit
// entry.
func refuseHomeDefaults(tool string, given map[string]string, homeOK bool) error {
	unset := func(key string) bool { return given[key] == "" }
	if !homeOK && (unset("sslrootcert") || unset("sslcert") || unset("sslkey")) {
		return fmt.Errorf("HOME is not set, so %s would look for its certificate files elsewhere than the app; set HOME or put sslrootcert, sslcert and sslkey into %s (%s)",
			tool, config.EnvDatabaseURL, pgToolDocs)
	}
	if homeOK && unset("sslcert") && unset("sslkey") {
		d, _ := libpqTLSDefaults()
		if fileExists(d["sslcert"]) != fileExists(d["sslkey"]) {
			return fmt.Errorf("HOME/.postgresql holds only one of postgresql.crt and postgresql.key, which %s refuses; add the other or remove it (%s)",
				tool, pgToolDocs)
		}
	}
	return nil
}

// derivePgxMode gives the tools the sslmode pgx derives from other keys,
// which libpq would refuse at connect time, after the audit entry:
// sslnegotiation=direct raises prefer (the default) to require, and
// sslrootcert=system makes every mode verify-full. A direct negotiation
// with allow or disable, which pgx accepts and libpq refuses, is refused
// here.
func (c *pgToolConn) derivePgxMode(tool string) error {
	mode := c.settings["sslmode"]
	if c.settings["sslnegotiation"] == "direct" {
		switch mode {
		case "", "prefer":
			c.settings["sslmode"] = "require"
		case "allow", "disable":
			return fmt.Errorf("%s sets sslnegotiation=direct with a weaker sslmode than require, which %s refuses; use sslmode=require, verify-ca or verify-full (%s)",
				config.EnvDatabaseURL, tool, pgToolDocs)
		}
	}
	if c.settings["sslrootcert"] == "system" {
		c.settings["sslmode"] = "verify-full"
	}
	return nil
}

// refusePgToolInputs refuses what pgx reads for the app's connection but
// the tool cannot get: a TLS or authentication variable of the process
// that the connection string does not override, a service, a client key
// password, and an ssl key that is not the URL alias ssl=true.
func refusePgToolInputs(tool string, given map[string]string, environ []string) error {
	env := map[string]string{}
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if _, seen := env[name]; ok && !seen {
			env[name] = value // the first counts, as for os.Getenv
		}
	}
	for _, k := range pgToolTLSKeys {
		if _, set := given[k.key]; env[k.env] != "" && !set {
			return fmt.Errorf("%s is set in the environment but would not reach %s; put %s into %s instead (%s)",
				k.env, tool, k.key, config.EnvDatabaseURL, pgToolDocs)
		}
	}
	for _, name := range pgToolRefusedEnv {
		if env[name] != "" {
			return fmt.Errorf("%s is set in the environment but cannot reach %s; unset it and put the settings into %s (%s)",
				name, tool, config.EnvDatabaseURL, pgToolDocs)
		}
	}
	for _, key := range pgToolRefusedKeys {
		if _, set := given[key]; set {
			return fmt.Errorf("%s sets %s, which cannot reach %s; put the settings into %s itself (%s)",
				config.EnvDatabaseURL, key, tool, config.EnvDatabaseURL, pgToolDocs)
		}
	}
	if _, set := given["ssl"]; set {
		return fmt.Errorf("%s sets ssl to something other than the URL alias ssl=true, which pgx and libpq read differently; use sslmode (%s)",
			config.EnvDatabaseURL, pgToolDocs)
	}
	return nil
}

// pgAttemptHosts are the hosts and ports of cfg's connection attempts in
// their order: the primary config, then its fallbacks. The repeat of a
// host for the TLS fallback of sslmode prefer or allow is one entry, as
// libpq tries both for each host itself.
func pgAttemptHosts(cfg *pgconn.Config) (hosts, ports []string) {
	var lastHost string
	var lastPort uint16
	for i, a := range pgAttempts(cfg) {
		if i > 0 && a.Host == lastHost && a.Port == lastPort {
			continue
		}
		hosts = append(hosts, a.Host)
		ports = append(ports, strconv.Itoa(int(a.Port)))
		lastHost, lastPort = a.Host, a.Port
	}
	return hosts, ports
}

// pgAttempts is the ordered list of cfg's connection attempts.
func pgAttempts(cfg *pgconn.Config) []*pgconn.FallbackConfig {
	return append([]*pgconn.FallbackConfig{{Host: cfg.Host, Port: cfg.Port, TLSConfig: cfg.TLSConfig}}, cfg.Fallbacks...)
}

// env is the whole environment of the tool: the allowlisted variables of
// environ (config.ChildEnv), then the libpq variables of c, every TLS key
// and PGGSSENCMODE included, so no setting depends on the libpq version's
// own defaults. Only an empty value is left out:
//   - require_auth empty is no restriction, the same in every supported
//     libpq (14 and 15 do not know the key, 16 and newer default to it);
//   - a certificate file is empty only without HOME on a connection
//     without TLS (with TLS, refuseHomeDefaults refuses), where libpq does
//     not read it.
//
// The certificate files are named as paths whether they exist or not:
// libpq of every supported version reads a named root or client
// certificate that does not exist as none (and refuses verify-ca and
// verify-full without a root certificate), as pgx reads the missing
// default file; checkString spells such a path as empty.
func (c *pgToolConn) env(environ []string) []string {
	var libpq []string
	for _, k := range append(append([]struct{ key, env string }{}, pgToolConnKeys...), pgToolTLSKeys...) {
		if v, ok := c.settings[k.key]; ok && v != "" {
			libpq = append(libpq, k.env+"="+v)
		}
	}
	for _, k := range pgToolFixedKeys {
		if v, ok := c.settings[k.key]; ok {
			libpq = append(libpq, k.env+"="+v)
		}
	}
	return config.ChildEnv(environ, pgToolEnvNames, libpq...)
}

// checkString is c as a keyword/value connection string that names every
// key the cross-check compares, with the values env gives the tools; a
// certificate file that does not exist is spelled empty, as pgx reads a
// named missing file as an error but the missing default file as none. As
// the connection string wins over the environment key by key, no variable
// of the process can change its parse. The password is left out; the
// cross-check does not compare it.
func (c *pgToolConn) checkString() string {
	var b strings.Builder
	write := func(key, value string) {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(key + "=" + quoteConnValue(value))
	}
	for _, k := range pgToolConnKeys {
		if k.key != "password" {
			write(k.key, c.settings[k.key])
		}
	}
	for _, k := range pgToolTLSKeys {
		write(k.key, checkValue(k.key, c.settings[k.key]))
	}
	return b.String()
}

// checkValue is the value of key in checkString for the value v the tools
// get: a certificate file that does not exist is empty.
func checkValue(key, v string) string {
	switch key {
	case "sslrootcert", "sslcert", "sslkey":
		if v != "" && v != "system" && !fileExists(v) {
			return ""
		}
	}
	return v
}

// libpqTLSDefaults are the values pgx takes for a TLS key the connection
// string does not set: the fixed defaults, and the certificate files under
// HOME/.postgresql, where pgx looks (os.UserHomeDir), as paths whether
// they exist or not. homeOK is false without HOME; the files are empty
// then.
func libpqTLSDefaults() (d map[string]string, homeOK bool) {
	d = map[string]string{
		"sslmode": "prefer", "sslrootcert": "", "sslcert": "", "sslkey": "", "sslsni": "1",
		"sslnegotiation": "postgres", "target_session_attrs": "any", "channel_binding": "prefer", "require_auth": "",
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return d, false
	}
	dir := filepath.Join(home, ".postgresql")
	d["sslrootcert"] = filepath.Join(dir, "root.crt")
	d["sslcert"], d["sslkey"] = filepath.Join(dir, "postgresql.crt"), filepath.Join(dir, "postgresql.key")
	return d, true
}

// quoteConnValue quotes a value of a keyword/value connection string.
func quoteConnValue(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// minLibpq is the oldest libpq major version that knows every setting of
// c, and the setting that needs it. Older versions ignore these settings
// without an error. The values count, not the keys.
func (c *pgToolConn) minLibpq() (int, string) {
	switch {
	case c.settings["sslnegotiation"] == "direct":
		return 17, "sslnegotiation=direct"
	case c.settings["require_auth"] != "":
		return 16, "require_auth"
	case c.settings["sslrootcert"] == "system":
		return 16, "sslrootcert=system"
	}
	return 0, ""
}

// comparePgConfigs reports the first setting in which the tools' parse
// differs from the app's: the ordered list of connection attempts (host,
// port and TLS of each, so sslmode prefer, which adds a plaintext attempt,
// never passes for require), then the settings of the whole connection.
// The error names the setting, never a value.
func comparePgConfigs(app, tools *pgconn.Config) error {
	a, t := pgAttempts(app), pgAttempts(tools)
	if len(a) != len(t) {
		return fmt.Errorf("the number of connection attempts differs (%d, the app %d)", len(t), len(a))
	}
	for i := range a {
		if a[i].Host != t[i].Host || a[i].Port != t[i].Port {
			return fmt.Errorf("the host or port of connection attempt %d differs", i+1)
		}
		if what := compareTLS(a[i].TLSConfig, t[i].TLSConfig); what != "" {
			return fmt.Errorf("the TLS of connection attempt %d differs (%s)", i+1, what)
		}
	}
	switch {
	case app.User != tools.User:
		return errors.New("the user differs")
	case app.Database != tools.Database:
		return errors.New("the database differs")
	case sslNegotiation(app.SSLNegotiation) != sslNegotiation(tools.SSLNegotiation):
		return errors.New("sslnegotiation differs")
	case app.ChannelBinding != tools.ChannelBinding:
		return errors.New("channel_binding differs")
	case app.RequireAuth != tools.RequireAuth:
		return errors.New("require_auth differs")
	case funcPointer(app.ValidateConnect) != funcPointer(tools.ValidateConnect):
		return errors.New("target_session_attrs differs")
	}
	return nil
}

// compareTLS names what differs between two TLS configs of an attempt.
func compareTLS(a, t *tls.Config) string {
	switch {
	case (a == nil) != (t == nil):
		return "TLS on or off"
	case a == nil:
		return ""
	case a.InsecureSkipVerify != t.InsecureSkipVerify:
		return "certificate verification"
	case (a.VerifyPeerCertificate == nil) != (t.VerifyPeerCertificate == nil):
		return "certificate chain verification"
	case a.ServerName != t.ServerName:
		return "server name"
	case !a.RootCAs.Equal(t.RootCAs):
		return "root certificates"
	case !sameClientCerts(a.Certificates, t.Certificates):
		return "client certificate"
	case !slices.Equal(a.NextProtos, t.NextProtos):
		return "ALPN"
	}
	return ""
}

func sameClientCerts(a, t []tls.Certificate) bool {
	return slices.EqualFunc(a, t, func(x, y tls.Certificate) bool {
		return slices.EqualFunc(x.Certificate, y.Certificate, bytes.Equal)
	})
}

// sslNegotiation reads pgx's empty default as libpq's "postgres".
func sslNegotiation(v string) string {
	if v == "" {
		return "postgres"
	}
	return v
}

// funcPointer identifies a ValidateConnect function: pgx sets one of its
// exported ValidateConnectTargetSessionAttrs* functions, or none.
func funcPointer(f pgconn.ValidateConnectFunc) uintptr {
	if f == nil {
		return 0
	}
	return reflect.ValueOf(f).Pointer()
}

// pgTool is pg_dump or pg_restore, found and checked, with the environment
// it runs with.
type pgTool struct {
	Path     string
	Env      []string
	Database string // for pg_restore --dbname
}

// findPgTool resolves the connection of tool (pg_dump or pg_restore),
// finds it in PATH and checks its version (checkPgTool), before the
// command logs, decrypts or writes anything. composeUse is the ./ma
// command that runs it in the database container.
func findPgTool(ctx context.Context, a *app, tool, composeUse string) (*pgTool, error) {
	conn, err := resolvePgToolConn(tool, a.cfg.DatabaseURL, os.Environ())
	if err != nil {
		return nil, err
	}
	path, err := exec.LookPath(tool)
	if err != nil {
		return nil, fmt.Errorf("%s not found; install the PostgreSQL client tools, or with Docker Compose use %s", tool, composeUse)
	}
	major, err := a.store.ServerMajorVersion(ctx)
	if err != nil {
		return nil, err
	}
	return checkPgTool(ctx, tool, path, conn, major)
}

// checkPgTool runs the tool at path with --version in the environment of
// conn and refuses a tool older than the server or than a setting of conn
// needs.
func checkPgTool(ctx context.Context, tool, path string, conn *pgToolConn, serverMajor int) (*pgTool, error) {
	env := conn.env(os.Environ())
	version, err := pgCommand(ctx, path, env, "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("%s --version: %w", tool, err)
	}
	major, err := pgDumpMajor(string(version))
	if err != nil {
		return nil, err
	}
	if major < serverMajor {
		return nil, fmt.Errorf("%s is version %d, the server %d: install %s %d or newer", tool, major, serverMajor, tool, serverMajor)
	}
	if need, setting := conn.minLibpq(); major < need {
		return nil, fmt.Errorf("%s is version %d, but %s in %s needs %d or newer (older versions ignore it): install %s %d or newer",
			tool, major, setting, config.EnvDatabaseURL, need, tool, need)
	}
	return &pgTool{Path: path, Env: env, Database: conn.settings["dbname"]}, nil
}

// connStringSettings reads the keys of a connection string as pgx and
// libpq do (the last occurrence of a key wins, keyword/value values may be
// quoted and escaped), for the TLS and authentication keys. A URL's ssl=true
// is pgx's alias for sslmode=require where it comes after the last
// sslmode; any other ssl value stays under "ssl". Host, port, user and
// database are not read here: they come from the app's parse.
func connStringSettings(s string) (map[string]string, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return nil, errors.New("forbidden NUL byte in connection string")
	}
	if strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://") {
		return urlQuerySettings(s)
	}
	return keywordValueSettings(s)
}

// urlQuerySettings reads the query parameters of a connection URI, which
// start at the first "?" after the user info and the host list, as in
// pgx's parseURLSettings.
func urlQuerySettings(s string) (map[string]string, error) {
	p, ok := strings.CutPrefix(s, "postgresql://")
	if !ok {
		p, _ = strings.CutPrefix(s, "postgres://")
	}
	if i := strings.IndexAny(p, "@/"); i >= 0 && p[i] == '@' {
		p = p[i+1:]
	}
	for {
		if strings.HasPrefix(p, "[") {
			end := strings.IndexByte(p, ']')
			if end < 0 {
				return nil, errors.New("unterminated IPv6 host address in URI")
			}
			p = p[end+1:]
		} else {
			i := strings.IndexAny(p, ":/?,")
			if i < 0 {
				i = len(p)
			}
			p = p[i:]
		}
		if strings.HasPrefix(p, ":") {
			i := strings.IndexAny(p[1:], "/?,")
			if i < 0 {
				i = len(p) - 1
			}
			p = p[1+i:]
		}
		if !strings.HasPrefix(p, ",") {
			break
		}
		p = p[1:]
	}
	if strings.HasPrefix(p, "/") {
		i := strings.IndexByte(p, '?')
		if i < 0 {
			i = len(p)
		}
		p = p[i:]
	}
	settings := map[string]string{}
	query, ok := strings.CutPrefix(p, "?")
	if !ok {
		return settings, nil
	}
	sslWasLast := false
	for _, pair := range strings.Split(query, "&") {
		rawKey, rawValue, found := strings.Cut(pair, "=")
		if !found || strings.IndexByte(rawValue, '=') >= 0 {
			return nil, errors.New("malformed URI query parameter")
		}
		key, err := uriDecode(rawKey)
		if err != nil {
			return nil, err
		}
		value, err := uriDecode(rawValue)
		if err != nil {
			return nil, fmt.Errorf("the value of %s: %w", key, err)
		}
		if key == "dbname" {
			key = "database"
		}
		switch key {
		case "ssl":
			sslWasLast = true
		case "sslmode":
			sslWasLast = false
		}
		settings[key] = value
	}
	if settings["ssl"] == "true" {
		delete(settings, "ssl")
		if sslWasLast {
			settings["sslmode"] = "require"
		}
	}
	return settings, nil
}

// uriDecode percent-decodes a URI component as libpq does: leading and
// trailing spaces are skipped, a space inside is an error, and so is %00.
// Its errors never quote the component.
func uriDecode(raw string) (string, error) {
	t := strings.Trim(raw, " ")
	if strings.IndexByte(t, ' ') >= 0 {
		return "", errors.New("unexpected space in URI component")
	}
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] != '%' {
			b.WriteByte(t[i])
			continue
		}
		if i+2 >= len(t) {
			return "", errors.New("invalid percent-encoding in URI component")
		}
		c, err := strconv.ParseUint(t[i+1:i+3], 16, 8)
		if err != nil || c == 0 {
			return "", errors.New("invalid percent-encoding in URI component")
		}
		b.WriteByte(byte(c))
		i += 2
	}
	return b.String(), nil
}

// keywordValueSettings reads a keyword/value connection string as pgx's
// parseKeywordValueSettings does: values may be single-quoted, a backslash
// takes the next character literally, and the last occurrence wins.
func keywordValueSettings(s string) (map[string]string, error) {
	const spaces = " \t\n\r\v\f"
	settings := map[string]string{}
	s = strings.TrimLeft(s, spaces)
	for s != "" {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, errors.New("invalid keyword/value connection string")
		}
		key := strings.Trim(s[:eq], spaces)
		if key == "" || strings.ContainsAny(key, spaces) {
			return nil, errors.New("invalid keyword/value connection string")
		}
		s = strings.TrimLeft(s[eq+1:], spaces)
		var val strings.Builder
		if strings.HasPrefix(s, "'") {
			i := 1
			for ; i < len(s) && s[i] != '\''; i++ {
				if s[i] == '\\' {
					i++
					if i == len(s) {
						break
					}
				}
				val.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("unterminated quoted string in connection string")
			}
			s = s[i+1:]
		} else {
			i := 0
			for ; i < len(s) && !strings.ContainsRune(spaces, rune(s[i])); i++ {
				if s[i] == '\\' {
					i++
					if i == len(s) {
						break
					}
				}
				val.WriteByte(s[i])
			}
			s = s[min(i, len(s)):]
		}
		s = strings.TrimLeft(s, spaces)
		if key == "dbname" {
			key = "database"
		}
		if key == "user" && val.Len() == 0 {
			continue
		}
		settings[key] = val.String()
	}
	return settings, nil
}
