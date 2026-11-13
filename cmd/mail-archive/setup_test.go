package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// TestSetupTokenCommand (#396, part 3): `setup token` prints a new token
// of 256 random bits in base64url, which the configuration accepts.
func TestSetupTokenCommand(t *testing.T) {
	var tokens []string
	for range 2 {
		var out bytes.Buffer
		cmd := newSetupCmd()
		cmd.SetOut(&out)
		if err := runCmd(t, cmd, "", "token"); err != nil {
			t.Fatal(err)
		}
		tok := strings.TrimSuffix(out.String(), "\n")
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil || len(raw) != 32 || len(tok) != 43 {
			t.Fatalf("token %q: %v", tok, err)
		}
		if _, err := config.LoadSetupToken("", tok, t.TempDir()); err != nil {
			t.Errorf("the configuration refuses the token: %v", err)
		}
		tokens = append(tokens, tok)
	}
	if tokens[0] == tokens[1] {
		t.Error("two runs printed the same token")
	}
}

// newKEK is a new archive key and its encoded form.
func newKEK(t *testing.T) ([]byte, string) {
	t.Helper()
	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	return kek, blobcrypt.EncodeKEK(kek)
}

// newInstanceEnv configures a new instance on the migrated database of st:
// a secret key, the archive key kek, no setup token, encryption at setup
// as by default.
func newInstanceEnv(t *testing.T, url string) (kek []byte, dataDir string) {
	t.Helper()
	startEnv(t, url, false)
	useKey(t, newTestKey(t))
	kek, enc := newKEK(t)
	t.Setenv(config.EnvArchiveKeyFile, "")
	t.Setenv(config.EnvArchiveKey, enc)
	t.Setenv(config.EnvEncryptionAtSetup, "")
	t.Setenv(config.EnvSetupTokenFile, "")
	t.Setenv(config.EnvSetupToken, "")
	return kek, os.Getenv(config.EnvDataDir)
}

// prepare runs the start of role with the configuration of the
// environment and returns its log and error.
func prepare(t *testing.T, ctx context.Context, role string) (string, error) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var logs lockedBuffer
	err = prepareInstance(ctx, cfg, slog.New(slog.NewTextHandler(&logs, nil)), role)
	return logs.String(), err
}

// writerState is the state of encryption at rest a writer of the data
// directory starts with, under the archive key kek.
func writerState(t *testing.T, ctx context.Context, st *store.Store, dataDir string, kek []byte) string {
	t.Helper()
	blobs, err := blobstore.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ar, err := archive.OpenAtRest(ctx, st, blobs, archive.AtRestOptions{
		DataDir: dataDir, KEK: kek, Role: archive.RoleWriter, Command: "test", Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("a writer's start: %v", err)
	}
	defer ar.Close()
	return ar.State
}

const turnedOn = "encryption at rest turned on for the new instance"

// TestEncryptionAtSetup (#396, revision 1, step 3b): on a new instance with
// an archive key, the first start under the migration lock turns
// encryption at rest on, whether serve or worker comes first; the other
// finds it on and changes nothing, and every writer starts with state on.
// Started together, exactly one of them turns it on.
func TestEncryptionAtSetup(t *testing.T) {
	for _, order := range [][]string{{"worker", "serve"}, {"serve", "worker"}} {
		t.Run(strings.Join(order, " first, then "), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
			defer cancel()
			st, url := storetest.NewWithURL(t)
			kek, dataDir := newInstanceEnv(t, url)
			for i, role := range order {
				logs, err := prepare(t, ctx, role)
				if err != nil {
					t.Fatalf("%s: %v\n%s", role, err, logs)
				}
				if got := strings.Contains(logs, turnedOn); got != (i == 0) {
					t.Errorf("%s turned it on: %v\n%s", role, got, logs)
				}
				noSecrets(t, role+"'s log", logs, url)
				if strings.Contains(logs, blobcrypt.EncodeKEK(kek)) {
					t.Errorf("%s's log holds the archive key", role)
				}
			}
			row, err := st.BlobEncryption(ctx)
			if err != nil || row == nil || row.State != store.BlobOn {
				t.Fatalf("state: %+v %v", row, err)
			}
			ring, err := blobcrypt.ReadKeyringFile(blobcrypt.KeyringPath(dataDir))
			if err != nil || ring.Open(kek) != nil || ring.ArchiveID() != row.KeyringID {
				t.Fatalf("keyring file: %v", err)
			}
			if s := writerState(t, ctx, st, dataDir, kek); s != store.BlobOn {
				t.Errorf("a writer starts with %q", s)
			}
		})
	}
	t.Run("together", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
		defer cancel()
		st, url := storetest.NewWithURL(t)
		kek, dataDir := newInstanceEnv(t, url)
		unlock, err := openStore(t, url).LockMigrations(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		type started struct {
			err  error
			logs string
		}
		var dones []chan started
		var waits []<-chan error
		for _, role := range []string{"serve", "worker"} {
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			done, ended := make(chan started, 1), make(chan error, 1)
			go func() {
				var logs lockedBuffer
				err := prepareInstance(ctx, cfg, slog.New(slog.NewTextHandler(&logs, nil)), role)
				ended <- err
				done <- started{err: err, logs: logs.String()}
			}()
			dones, waits = append(dones, done), append(waits, ended)
		}
		waitForMigrateLockWaiters(t, ctx, url, 2, waits...)
		unlock()
		on := 0
		for _, d := range dones {
			r := <-d
			if r.err != nil {
				t.Fatalf("start: %v\n%s", r.err, r.logs)
			}
			if strings.Contains(r.logs, turnedOn) {
				on++
			}
		}
		if on != 1 {
			t.Errorf("%d starts turned encryption on, want 1", on)
		}
		if s := writerState(t, ctx, st, dataDir, kek); s != store.BlobOn {
			t.Errorf("a writer starts with %q", s)
		}
	})
}

// TestEncryptionAtSetupKeyring (#396, part 3): a keyring the operator made
// before the first start (`encryption keygen`) is the one turned on.
func TestEncryptionAtSetupKeyring(t *testing.T) {
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	kek, dataDir := newInstanceEnv(t, url)
	ring, err := blobcrypt.NewKeyring(kek, "archive key", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(dataDir), ring); err != nil {
		t.Fatal(err)
	}
	if logs, err := prepare(t, ctx, "serve"); err != nil || !strings.Contains(logs, turnedOn) {
		t.Fatalf("start: %v\n%s", err, logs)
	}
	row, err := st.BlobEncryption(ctx)
	if err != nil || row == nil || row.State != store.BlobOn || row.KeyringID != ring.ArchiveID() {
		t.Fatalf("state: %+v %v", row, err)
	}
}

// TestEncryptionAtSetupRefusals (#396, revision 1): without an archive key a
// new instance does not start (exit 4), neither serve nor worker; with
// KEEPIA_ARCHIVE_ENCRYPTION_AT_SETUP=off both start with encryption off and
// say so. A command-line writer that holds the writers lock (an import
// before the first user) makes the start refuse with exit 4 and name it,
// changing nothing (plan approval, note 1). An instance with a message is
// not new: it is left alone.
func TestEncryptionAtSetupRefusals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	st, url := storetest.NewWithURL(t)
	kek, dataDir := newInstanceEnv(t, url)
	noState := func(what string) {
		t.Helper()
		if row, err := st.BlobEncryption(ctx); err != nil || row != nil {
			t.Errorf("%s: state %+v %v", what, row, err)
		}
		if _, err := os.Stat(blobcrypt.KeyringPath(dataDir)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: keyring file: %v", what, err)
		}
	}

	t.Setenv(config.EnvArchiveKey, "")
	for _, role := range []string{"serve", "worker"} {
		logs, err := prepare(t, ctx, role)
		code, msg := exitCode(err)
		if code != exitStartRefused || !strings.Contains(msg, "new instances start encrypted") || !strings.Contains(msg, config.EnvArchiveKeyFile) {
			t.Errorf("%s without a key: exit %d: %s\n%s", role, code, msg, logs)
		}
	}
	noState("without a key")

	t.Setenv(config.EnvEncryptionAtSetup, "off")
	for _, role := range []string{"serve", "worker"} {
		logs, err := prepare(t, ctx, role)
		if err != nil || !strings.Contains(logs, config.EnvEncryptionAtSetup+"=off is for test instances only") {
			t.Errorf("%s with encryption at setup off: %v\n%s", role, err, logs)
		}
	}
	noState("with encryption at setup off")
	if s := writerState(t, ctx, st, dataDir, nil); s != store.BlobOff {
		t.Errorf("a writer starts with %q", s)
	}

	t.Setenv(config.EnvEncryptionAtSetup, "")
	t.Setenv(config.EnvArchiveKey, blobcrypt.EncodeKEK(kek))
	held, err := st.HoldWriters(ctx, "import")
	if err != nil {
		t.Fatal(err)
	}
	logs, err := prepare(t, ctx, "serve")
	code, msg := exitCode(err)
	if code != exitStartRefused || !strings.Contains(msg, "import") || !strings.Contains(msg, "start again") {
		t.Errorf("with a writer running: exit %d: %s\n%s", code, msg, logs)
	}
	noState("with a writer running")
	held.Release()
	if logs, err := prepare(t, ctx, "serve"); err != nil || !strings.Contains(logs, turnedOn) {
		t.Errorf("after the writer: %v\n%s", err, logs)
	}

	// An instance with a message, archived before the first user.
	other, otherURL := storetest.NewWithURL(t)
	otherKEK, otherData := newInstanceEnv(t, otherURL)
	blobs, err := blobstore.New(otherData)
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "early", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
	if err := other.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	f, err := other.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := blobs.Put(strings.NewReader("Subject: early\r\n\r\nhi\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.SaveBatch(ctx, f.ID, 1, []store.MessageMeta{{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path}},
		[]store.Location{{FolderID: f.ID, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	// The worker: the made-up password of the account opens with no key.
	if logs, err := prepare(t, ctx, "worker"); err != nil || strings.Contains(logs, turnedOn) {
		t.Errorf("with a message: %v\n%s", err, logs)
	}
	if row, err := other.BlobEncryption(ctx); err != nil || row != nil {
		t.Errorf("with a message: state %+v %v", row, err)
	}
	if s := writerState(t, ctx, other, otherData, otherKEK); s != store.BlobOff {
		t.Errorf("with a message, a writer starts with %q", s)
	}
}

// setupStart is serve's start with steps 5 and 7 and the setup token tok.
func setupStart(t *testing.T, tok string) (instanceStart, *lockedBuffer) {
	t.Helper()
	s, logs := serveStart(t)
	s.setupToken = true
	s.cfg.SetupToken = tok
	return s, logs
}

// TestStartSetupToken (#396, part 3, step 7): a start with a token stores
// its hash and starts its window; the same token keeps the window, another
// starts a new one, and a start without a token revokes it. Two starts
// with a token at once write it once. The log names the state, never the
// token.
func TestStartSetupToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	st, url := storetest.NewWithURL(t)
	startEnv(t, url, false)
	key := newTestKey(t)
	useKey(t, key)
	a, b := setupTokenOf(t), setupTokenOf(t)

	run := func(tok string) string {
		t.Helper()
		s, logs := setupStart(t, tok)
		if err := s.run(ctx); err != nil {
			t.Fatalf("start: %v\n%s", err, logs)
		}
		noSecrets(t, "the start's log", logs.String(), url, key)
		for _, tk := range []string{a, b} {
			if strings.Contains(logs.String(), tk) {
				t.Errorf("the log holds a setup token:\n%s", logs)
			}
		}
		return logs.String()
	}
	if logs := run(a); !strings.Contains(logs, "waiting for the first admin") || !strings.Contains(logs, "new_token=true") {
		t.Errorf("first start:\n%s", logs)
	}
	first, err := st.SetupState(ctx, time.Now())
	if err != nil || first.State != store.SetupToken {
		t.Fatalf("after the first start: %+v %v", first, err)
	}
	if logs := run(a); !strings.Contains(logs, "new_token=false") {
		t.Errorf("same token:\n%s", logs)
	}
	if again, _ := st.SetupState(ctx, time.Now()); !again.TokenSince.Equal(*first.TokenSince) {
		t.Errorf("the same token moved its window: %v, was %v", again.TokenSince, first.TokenSince)
	}
	if err := st.CheckSetupToken(ctx, store.HashSetupToken(a), time.Now()); err != nil {
		t.Errorf("token a: %v", err)
	}
	run(b)
	if err := st.CheckSetupToken(ctx, store.HashSetupToken(a), time.Now()); !errors.Is(err, store.ErrSetupTokenInvalid) {
		t.Errorf("token a after b: %v", err)
	}
	if renewed, _ := st.SetupState(ctx, time.Now()); !renewed.TokenSince.After(*first.TokenSince) {
		t.Errorf("token b kept the old window")
	}
	if logs := run(""); !strings.Contains(logs, "setup token removed") {
		t.Errorf("start without a token:\n%s", logs)
	}
	if s, _ := st.SetupState(ctx, time.Now()); s.State != store.SetupCLI {
		t.Errorf("after a start without a token: %q", s.State)
	}
	if err := st.CheckSetupToken(ctx, store.HashSetupToken(b), time.Now()); !errors.Is(err, store.ErrSetupUnavailable) {
		t.Errorf("revoked token: %v", err)
	}
	is, err := st.InstanceStatus(ctx, version)
	if err != nil {
		t.Fatal(err)
	}
	is.EncryptionAtSetupOff = true
	if line := setupStatus(is); !strings.Contains(line, "user add NAME --admin") || !strings.Contains(line, config.EnvEncryptionAtSetup+"=off") {
		t.Errorf("status: %s", line)
	}

	// Two starts with a token at once: one stores it.
	unlock, err := openStore(t, url).LockMigrations(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	type started struct {
		err  error
		logs *lockedBuffer
	}
	var dones []chan started
	var waits []<-chan error
	for range 2 {
		s, logs := setupStart(t, a)
		done, ended := make(chan started, 1), make(chan error, 1)
		go func() {
			err := s.run(ctx)
			ended <- err
			done <- started{err: err, logs: logs}
		}()
		dones, waits = append(dones, done), append(waits, ended)
	}
	waitForMigrateLockWaiters(t, ctx, url, 2, waits...)
	unlock()
	stored := 0
	for _, d := range dones {
		r := <-d
		if r.err != nil {
			t.Fatalf("start: %v\n%s", r.err, r.logs)
		}
		if strings.Contains(r.logs.String(), "new_token=true") {
			stored++
		}
	}
	if stored != 1 {
		t.Errorf("%d starts stored the token, want 1", stored)
	}
	if s, _ := st.SetupState(ctx, time.Now()); s.State != store.SetupToken {
		t.Errorf("after two starts: %q", s.State)
	}

	// After the setup a token is not used.
	if _, err := st.CreateUser(ctx, "admin", "h", true); err != nil {
		t.Fatal(err)
	}
	if logs := run(b); !strings.Contains(logs, "setup is complete") {
		t.Errorf("start after the setup:\n%s", logs)
	}
	if s, _ := st.SetupState(ctx, time.Now()); s.State != store.SetupComplete || s.TokenExpires != nil {
		t.Errorf("after the setup: %+v", s)
	}
}

// setupTokenOf is a fresh setup token, made at run time.
func setupTokenOf(t *testing.T) string {
	t.Helper()
	tok, err := config.NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestServeSetupTokenRefusals (#396, part 3): serve refuses a setup token
// that is too short, not printable ASCII, set twice, in an unprotected
// file, or one of the instance's keys, with exit code 4, before it listens
// or touches the database, and the message holds no token and no key.
func TestServeSetupTokenRefusals(t *testing.T) {
	const dbURL = "postgres://nobody@127.0.0.1:1/none"
	startEnv(t, dbURL, false)
	k := newTestKey(t)
	useKey(t, k)
	tok := setupTokenOf(t)
	file := func(mode os.FileMode) string {
		p := filepath.Join(t.TempDir(), "setup.token")
		if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct {
		name, file, value, want string
	}{
		{"short", "", tok[:31], "needs 32 to 256 characters, it has 31"},
		{"long", "", strings.Repeat(tok, 6)[:257], "it has 257"},
		{"space", "", tok[:20] + " " + tok[20:], "printable ASCII"},
		{"both", file(0o400), tok, "both " + config.EnvSetupTokenFile + " and " + config.EnvSetupToken},
		{"group-readable", file(0o640), "", "mode 0640"},
		{"the secret key", "", k.enc, "one of the instance's keys"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(config.EnvSetupTokenFile, c.file)
			t.Setenv(config.EnvSetupToken, c.value)
			code, msg := exitCode(runCmd(t, newServeCmd(), "", "--listen", "127.0.0.1:99999"))
			if code != exitStartRefused || !strings.Contains(msg, c.want) {
				t.Fatalf("exit %d: %s", code, msg)
			}
			if strings.Contains(msg, tok[:20]) {
				t.Errorf("the refusal holds the token: %s", msg)
			}
			noSecrets(t, "the refusal", msg, dbURL, k)
		})
	}
}

// TestServeFromConfiguration (#396, Done when): a new instance comes up
// from configuration alone (the database URL, the secret key file, the
// archive key file, the setup token file and start-up migrations): serve
// migrates the empty database, turns encryption at rest on, and the setup
// link's request creates the first admin, who is logged in and must set
// up 2FA. No log line holds a key, the token or the database password.
func TestServeFromConfiguration(t *testing.T) {
	dbURL := storetest.NewUnmigrated(t)
	startEnv(t, dbURL, true)
	t.Setenv(config.EnvAllowedHosts, "archive.lan")
	t.Setenv(config.EnvEncryptionAtSetup, "")
	key := newTestKey(t)
	useKeyFile(t, key)
	kek, kekEnc := newKEK(t)
	keyFile := filepath.Join(t.TempDir(), "archive.key")
	if err := os.WriteFile(keyFile, []byte(kekEnc+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvArchiveKey, "")
	t.Setenv(config.EnvArchiveKeyFile, keyFile)
	tok := setupTokenOf(t)
	tokFile := filepath.Join(t.TempDir(), "setup.token")
	if err := os.WriteFile(tokFile, []byte(tok+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvSetupToken, "")
	t.Setenv(config.EnvSetupTokenFile, tokFile)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SetupToken, err = config.SetupTokenFromEnv(cfg); err != nil || cfg.SetupToken != tok {
		t.Fatalf("the token from its file: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	logs := newSignalLog("msg=ready")
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- runServe(runCtx, cfg, slog.New(slog.NewTextHandler(logs, nil)), ln) }()
	select {
	case <-logs.seen:
	case err := <-done:
		t.Fatalf("serve ended: %v\n%s", err, logs)
	case <-ctx.Done():
		t.Fatalf("serve did not get ready:\n%s", logs)
	}
	if code, _, body := rawGet(t, ctx, addr, "archive.lan", "/api/session"); code != 401 || !strings.Contains(body, `"setup":"token"`) {
		t.Errorf("/api/session: %d %s", code, body)
	}

	post := func(token string) (*http.Response, string) {
		t.Helper()
		in, _ := json.Marshal(map[string]string{"token": token, "username": "admin", "password": "correct horse battery"})
		req, err := http.NewRequestWithContext(ctx, "POST", "http://"+addr+"/api/setup", bytes.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "archive.lan"
		req.Header.Set("Origin", "http://archive.lan")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close() //nolint:errcheck // test
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body)
	}
	if resp, body := post(setupTokenOf(t)); resp.StatusCode != 401 {
		t.Errorf("wrong token: %d %s", resp.StatusCode, body)
	}
	resp, body := post(tok)
	if resp.StatusCode != 201 || !strings.Contains(body, `"admin":true`) || !strings.Contains(body, `"twoFactorRequired":true`) {
		t.Fatalf("setup: %d %s", resp.StatusCode, body)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/api/accounts", nil)
	req.Host = "archive.lan"
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	accounts, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = accounts.Body.Close()
	if accounts.StatusCode != 403 {
		t.Errorf("the admin before the 2FA setup: %d", accounts.StatusCode)
	}
	if resp, body := post(tok); resp.StatusCode != 404 {
		t.Errorf("the link again: %d %s", resp.StatusCode, body)
	}

	stop()
	if err := <-done; err != nil {
		t.Errorf("serve: %v\n%s", err, logs)
	}
	st := openStore(t, dbURL)
	row, err := st.BlobEncryption(ctx)
	if err != nil || row == nil || row.State != store.BlobOn {
		t.Errorf("encryption at rest: %+v %v", row, err)
	}
	if s, err := st.SetupState(ctx, time.Now()); err != nil || s.State != store.SetupComplete {
		t.Errorf("setup: %+v %v", s, err)
	}
	out := logs.String()
	for _, want := range []string{"applied 00039_instance_state.sql", turnedOn, "waiting for the first admin", "first admin created with the setup link", "msg=ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no users yet") {
		t.Errorf("the log points to user add although a setup token is set:\n%s", out)
	}
	noSecrets(t, "serve's log", out, dbURL, key)
	for _, secret := range []string{tok, kekEnc, string(kek)} {
		if strings.Contains(out, secret) {
			t.Errorf("serve's log holds the setup token or the archive key:\n%s", out)
		}
	}
}
