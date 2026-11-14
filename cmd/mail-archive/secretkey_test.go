package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/crypto"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/store/storetest"
	"github.com/keepia/archive/internal/testwait"
)

// testKey is a secret key of a test: its base64 form, as configured, and
// the raw key.
type testKey struct {
	enc string
	raw []byte
}

func newTestKey(t *testing.T) testKey {
	t.Helper()
	enc, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := crypto.ParseKey(enc)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{enc: enc, raw: raw}
}

// fingerprint is the fingerprint of the key's check value.
func (k testKey) fingerprint() string { return crypto.KeyCheckFingerprint(crypto.KeyCheck(k.raw)) }

// forms are the ways the key could show up in an output.
func (k testKey) forms() []string {
	return []string{k.enc, base64.RawStdEncoding.EncodeToString(k.raw), base64.URLEncoding.EncodeToString(k.raw),
		base64.RawURLEncoding.EncodeToString(k.raw), hex.EncodeToString(k.raw), string(k.raw)}
}

// useKey configures k through KEEPIA_ARCHIVE_SECRET_KEY.
func useKey(t *testing.T, k testKey) {
	t.Helper()
	t.Setenv(config.EnvSecretKeyFile, "")
	t.Setenv(config.EnvSecretKey, k.enc)
}

// useKeyFile configures k through a key file and returns its path.
func useKeyFile(t *testing.T, k testKey) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(p, []byte(k.enc+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvSecretKey, "")
	t.Setenv(config.EnvSecretKeyFile, p)
	return p
}

// noSecrets reports every key form and the database password in out.
func noSecrets(t *testing.T, what, out, dbURL string, keys ...testKey) {
	t.Helper()
	for _, k := range keys {
		for _, f := range k.forms() {
			if strings.Contains(out, f) {
				t.Errorf("%s holds a secret key:\n%s", what, out)
			}
		}
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	if strings.Contains(out, dbURL) || (pw != "" && strings.Contains(out, ":"+pw+"@")) {
		t.Errorf("%s holds the database URL or password:\n%s", what, out)
	}
}

// serveStart is the start of serve, with step 5, under the configuration
// of the environment.
func serveStart(t *testing.T, opts ...store.Option) (instanceStart, *lockedBuffer) {
	t.Helper()
	s, logs := newStart(t, "serve", opts...)
	s.secretKey = true
	return s, logs
}

// TestStartSecretKey (#396): serve's start records the check value of its
// first key, starts again with it, and refuses another key with exit
// code 4 and both fingerprints, never a key. The worker does not check.
// `secret-key accept --yes` makes the other key the instance's.
func TestStartSecretKey(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	startEnv(t, dbURL, false)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	a, b := newTestKey(t), newTestKey(t)

	// Without a key: refused before anything else.
	t.Setenv(config.EnvSecretKey, "")
	s, _ := serveStart(t)
	if code, msg := exitCode(s.run(ctx)); code != exitStartRefused || !strings.Contains(msg, config.EnvSecretKeyFile) {
		t.Fatalf("without a key: exit %d: %s", code, msg)
	}

	useKey(t, a)
	s, logs := serveStart(t)
	if err := s.run(ctx); err != nil {
		t.Fatalf("first start: %v\n%s", err, logs)
	}
	if !strings.Contains(logs.String(), `msg="secret key check recorded" fingerprint=`+a.fingerprint()) {
		t.Errorf("first start log:\n%s", logs)
	}
	if got, err := st.SecretKeyCheck(ctx); err != nil || !bytes.Equal(got, crypto.KeyCheck(a.raw)) {
		t.Fatalf("check value %x %v", got, err)
	}
	s, logs = serveStart(t)
	if err := s.run(ctx); err != nil || !strings.Contains(logs.String(), `msg="secret key checked"`) {
		t.Fatalf("second start: %v\n%s", err, logs)
	}

	useKeyFile(t, b)
	s, logs = serveStart(t)
	code, msg := exitCode(s.run(ctx))
	if code != exitStartRefused || !strings.Contains(msg, "fingerprint "+b.fingerprint()+") is not the key of this instance (fingerprint "+a.fingerprint()) ||
		!strings.Contains(msg, "secret-key accept --yes") || !strings.Contains(msg, "file ") {
		t.Fatalf("another key: exit %d: %s", code, msg)
	}
	noSecrets(t, "the refusal", msg+logs.String(), dbURL, a, b)
	// The worker never checks the key (and Compose gives it none). A test
	// instance: no encryption at setup.
	t.Setenv(config.EnvEncryptionAtSetup, "off")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var workerLog lockedBuffer
	if err := prepareInstance(ctx, cfg, slog.New(slog.NewTextHandler(&workerLog, nil)), "worker"); err != nil {
		t.Fatalf("worker with another key: %v\n%s", err, workerLog.String())
	}

	// accept: without --yes nothing changes; with it, b is the key.
	var out bytes.Buffer
	cmd := newSecretKeyCmd()
	cmd.SetOut(&out)
	if err := runCmd(t, cmd, "", "accept"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("accept without --yes: %v", err)
	}
	if got, _ := st.SecretKeyCheck(ctx); !bytes.Equal(got, crypto.KeyCheck(a.raw)) {
		t.Fatal("accept without --yes changed the check value")
	}
	cmd = newSecretKeyCmd()
	cmd.SetOut(&out)
	if err := runCmd(t, cmd, "", "accept", "--yes"); err != nil {
		t.Fatalf("accept: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "fingerprint "+b.fingerprint()+": 0 of 0 stored") || !strings.Contains(out.String(), "accepted") {
		t.Errorf("accept said:\n%s", out.String())
	}
	noSecrets(t, "accept", out.String(), dbURL, a, b)
	if got, _ := st.SecretKeyCheck(ctx); !bytes.Equal(got, crypto.KeyCheck(b.raw)) {
		t.Fatal("accept did not record the key")
	}
	if n := countAudit(t, ctx, st, "instance.secret_key_accepted"); n != 1 {
		t.Errorf("%d audit entries", n)
	}
	s, logs = serveStart(t)
	if err := s.run(ctx); err != nil {
		t.Fatalf("start after accept: %v\n%s", err, logs)
	}
}

// countAudit counts the audit entries of action in the last hour.
func countAudit(t *testing.T, ctx context.Context, st *store.Store, action string) int {
	t.Helper()
	n := 0
	f := store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Actions: []string{action}}
	if err := st.ExportAudit(ctx, f, func(store.AuditRecord) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestStartSecretKeySealed (#396): on the first start of 1.6.0 there is no
// check value; stored passwords sealed under key A refuse a start with key
// B and record nothing, and A starts and is recorded.
func TestStartSecretKeySealed(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	startEnv(t, dbURL, false)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	a, b := newTestKey(t), newTestKey(t)
	sealer, err := crypto.NewSealer(a.raw)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, "anna", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAccountSealed(ctx, &store.Account{Name: "mail", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", OwnerID: &u.ID},
		func(id int64) ([]byte, error) { return archive.SealPassword(sealer, id, "imap-password-marker") }); err != nil {
		t.Fatal(err)
	}

	useKeyFile(t, b)
	s, logs := serveStart(t)
	code, msg := exitCode(s.run(ctx))
	if code != exitStartRefused || !strings.Contains(msg, "fingerprint "+b.fingerprint()+") opens none of the 1 stored IMAP passwords") {
		t.Fatalf("key B: exit %d: %s", code, msg)
	}
	noSecrets(t, "the refusal", msg+logs.String(), dbURL, a, b)
	if strings.Contains(msg+logs.String(), "imap-password-marker") {
		t.Error("the refusal names the stored password")
	}
	if got, err := st.SecretKeyCheck(ctx); err != nil || got != nil {
		t.Fatalf("a refused key was recorded: %x %v", got, err)
	}

	useKey(t, a)
	s, logs = serveStart(t)
	if err := s.run(ctx); err != nil {
		t.Fatalf("key A: %v\n%s", err, logs)
	}
	if !strings.Contains(logs.String(), "sealed_values_opened=1") {
		t.Errorf("log:\n%s", logs)
	}
	if got, err := st.SecretKeyCheck(ctx); err != nil || !bytes.Equal(got, crypto.KeyCheck(a.raw)) {
		t.Fatalf("check value %x %v", got, err)
	}
}

// TestServeSecretKeyRefusals (#396): serve refuses a missing, doubled,
// unprotected or malformed secret key with exit code 4, before it listens
// (the listen address here could not be bound) and before it touches the
// database, and the message names no key.
func TestServeSecretKeyRefusals(t *testing.T) {
	startEnv(t, "postgres://nobody@127.0.0.1:1/none", false)
	k := newTestKey(t)
	loose := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(loose, []byte(k.enc), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o640); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(protected, []byte(k.enc), 0o400); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, file, value, want string
	}{
		{"missing", "", "", "serve needs the secret key: set " + config.EnvSecretKeyFile},
		{"both", protected, k.enc, "both " + config.EnvSecretKeyFile + " and " + config.EnvSecretKey},
		{"group-readable", loose, "", "mode 0640"},
		{"malformed", "", k.enc[:20], config.EnvSecretKey + ": secret key must be 32 bytes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(config.EnvSecretKeyFile, c.file)
			t.Setenv(config.EnvSecretKey, c.value)
			code, msg := exitCode(runCmd(t, newServeCmd(), "", "--listen", "127.0.0.1:99999"))
			if code != exitStartRefused || !strings.Contains(msg, c.want) {
				t.Fatalf("exit %d: %s", code, msg)
			}
			noSecrets(t, "the refusal", msg, "postgres://nobody@127.0.0.1:1/none", k)
		})
	}
}

// TestContainerCheckEnv (#396): the write check that prepare-data runs as
// the container user gets the data directory and a few harmless
// variables, never a secret of this process.
func TestContainerCheckEnv(t *testing.T) {
	setEnvMarkers(t)
	t.Setenv(config.EnvSecretKeyFile, envMarker+"-file")
	t.Setenv("PATH", "/usr/bin")
	cmd := containerCheckCmd("/bin/mail-archive", "/data", os.Environ())
	if got := strings.Join(cmd.Args, " "); got != "/bin/mail-archive prepare-data --check" {
		t.Errorf("args %q", got)
	}
	allowed := map[string]bool{config.EnvDataDir: true}
	for _, name := range containerCheckEnvNames {
		allowed[name] = true
	}
	have := map[string]bool{}
	for _, kv := range cmd.Env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.Contains(kv, envMarker) || !allowed[name] {
			t.Errorf("passed on: %s", kv)
		}
		have[kv] = true
	}
	if !have[config.EnvDataDir+"=/data"] || !have["PATH=/usr/bin"] {
		t.Errorf("env %q", cmd.Env)
	}
}

// TestComposeSecretKey (#396, revision 1, point 4): in docker-compose.yml
// web and the CLI service take the secret key from the variable or a file
// in the read-only /secret-key mount, neither of them required; the
// worker gets neither the key nor the mount.
func TestComposeSecretKey(t *testing.T) {
	data, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Services map[string]struct {
			Environment map[string]any `yaml:"environment"`
			Volumes     []string       `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	const mount = "${SECRET_KEY_DIR:-./secret-key}:/secret-key:ro"
	for _, name := range []string{"web", "mail-archive"} {
		svc := file.Services[name]
		for _, v := range []string{config.EnvSecretKey, config.EnvSecretKeyFile} {
			checkComposeAlias(t, name, svc.Environment, v)
		}
		if !slices.Contains(svc.Volumes, mount) {
			t.Errorf("%s volumes %v", name, svc.Volumes)
		}
	}
	worker := file.Services["worker"]
	if len(worker.Environment) == 0 {
		t.Fatal("no worker environment")
	}
	for name := range worker.Environment {
		if strings.HasPrefix(name, config.EnvSecretKey) || strings.HasPrefix(name, config.OldName(config.EnvSecretKey)) {
			t.Errorf("the worker gets %s", name)
		}
	}
	for _, v := range worker.Volumes {
		if strings.Contains(v, "secret-key") {
			t.Errorf("the worker mounts %s", v)
		}
	}
}

// TestComposeSetupToken (#396, part 3): the setup token goes to the web
// server only, from the read-only /secret-key mount or the variable, neither
// required; the worker and the command line never get it. Encryption at
// setup reaches web and worker, which both run it, and the command line for
// `status`.
func TestComposeSetupToken(t *testing.T) {
	data, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Services map[string]struct {
			Environment map[string]any `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	web := file.Services["web"].Environment
	checkComposeAlias(t, "web", web, config.EnvSetupTokenFile)
	checkComposeAlias(t, "web", web, config.EnvSetupToken)
	for name, svc := range file.Services {
		if name == "web" {
			continue
		}
		for v := range svc.Environment {
			if strings.HasPrefix(v, config.EnvSetupToken) || strings.HasPrefix(v, config.OldName(config.EnvSetupToken)) {
				t.Errorf("%s gets %s", name, v)
			}
		}
	}
	for _, name := range []string{"web", "worker", "mail-archive"} {
		checkComposeAlias(t, name, file.Services[name].Environment, config.EnvEncryptionAtSetup)
	}
}

// checkComposeAlias: the service passes the variable name of .env, without
// a default, under its new name with the old one as fallback, and under its
// old name raw, so that the app warns about it (#585).
func checkComposeAlias(t *testing.T, service string, env map[string]any, name string) {
	t.Helper()
	old := config.OldName(name)
	if got, want := env[name], "${"+name+":-${"+old+":-}}"; got != want {
		t.Errorf("%s: %s = %v, want %s", service, name, got, want)
	}
	if got, want := env[old], "${"+old+":-}"; got != want {
		t.Errorf("%s: %s = %v, want %s", service, old, got, want)
	}
}

// TestStartNoSecretsInOutput (#396, leaks): the log and the message of a
// fresh start with start-up migrations, of a refused key and of a failed
// migration name no secret key and no database password.
func TestStartNoSecretsInOutput(t *testing.T) {
	dbURL := storetest.NewUnmigrated(t)
	startEnv(t, dbURL, true)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	a, b := newTestKey(t), newTestKey(t)
	useKeyFile(t, a)

	s, logs := serveStart(t)
	if err := s.run(ctx); err != nil {
		t.Fatalf("fresh start: %v\n%s", err, logs)
	}
	if !strings.Contains(logs.String(), "applied 00039_instance_state.sql") {
		t.Errorf("log:\n%s", logs)
	}
	noSecrets(t, "the fresh start", logs.String(), dbURL, a, b)

	useKey(t, b)
	s, logs = serveStart(t)
	code, msg := exitCode(s.run(ctx))
	if code != exitStartRefused {
		t.Fatalf("another key: exit %d: %s", code, msg)
	}
	noSecrets(t, "the refused key", msg+logs.String(), dbURL, a, b)

	useKeyFile(t, a)
	failing := startMigrations(t, map[string]string{"90001_start_good.sql": goodMigration, "90002_start_bad.sql": badMigration})
	s, logs = serveStart(t, store.WithMigrationsFS(failing))
	code, msg = exitCode(s.run(ctx))
	if code != exitMigrationFailed {
		t.Fatalf("failed migration: exit %d: %s", code, msg)
	}
	noSecrets(t, "the failed migration", msg+logs.String(), dbURL, a, b)
}
