package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// signalLog is a log that closes seen once a line with want arrives.
type signalLog struct {
	lockedBuffer
	want string
	once sync.Once
	seen chan struct{}
}

func newSignalLog(want string) *signalLog { return &signalLog{want: want, seen: make(chan struct{})} }

func (l *signalLog) Write(p []byte) (int, error) {
	n, err := l.lockedBuffer.Write(p)
	if strings.Contains(string(p), l.want) {
		l.once.Do(func() { close(l.seen) })
	}
	return n, err
}

// rawGet sends GET path with Host host to addr and returns the status,
// the Retry-After header and the body.
func rawGet(t *testing.T, ctx context.Context, addr, host, path string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Retry-After"), string(body)
}

// TestServeStarting (#396, decision 13): serve listens while it waits for
// the migration lock and answers with the starting handler: /healthz 503
// {"status":"starting"}, another path 503 with Retry-After, a wrong Host
// 403. Once the lock is free it swaps to the app on the same listener:
// /healthz 200 ok and the real routes. Its log names no secret key and
// no database password. Raw requests against the real route table.
func TestServeStarting(t *testing.T) {
	_, dbURL := storetest.NewWithURL(t)
	startEnv(t, dbURL, false)
	t.Setenv(config.EnvAllowedHosts, "archive.lan")
	// A test instance: it starts new, without encryption at setup.
	t.Setenv(config.EnvEncryptionAtSetup, "off")
	k := newTestKey(t)
	useKeyFile(t, k)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	unlock, err := openStore(t, dbURL).LockMigrations(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
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
	waitForMigrateLockWaiters(t, ctx, dbURL, 1, done)

	code, retry, body := rawGet(t, ctx, addr, "archive.lan", "/healthz")
	var health map[string]string
	if code != 503 || retry != "5" || json.Unmarshal([]byte(body), &health) != nil || health["status"] != "starting" {
		t.Errorf("/healthz while starting: %d %q %s", code, retry, body)
	}
	if code, retry, body := rawGet(t, ctx, addr, "archive.lan", "/api/session"); code != 503 || retry != "5" || !strings.Contains(body, "starting") {
		t.Errorf("/api/session while starting: %d %q %s", code, retry, body)
	}
	if code, _, _ := rawGet(t, ctx, addr, "evil.example", "/healthz"); code != 403 {
		t.Errorf("wrong host while starting: %d", code)
	}

	unlock()
	select {
	case <-logs.seen:
	case err := <-done:
		t.Fatalf("serve ended: %v\n%s", err, logs)
	case <-ctx.Done():
		t.Fatalf("serve did not get ready:\n%s", logs)
	}
	if code, retry, body := rawGet(t, ctx, addr, "archive.lan", "/healthz"); code != 200 || retry != "" || body != "ok\n" {
		t.Errorf("/healthz when ready: %d %q %q", code, retry, body)
	}
	if code, _, body := rawGet(t, ctx, addr, "archive.lan", "/api/session"); code != 401 || !strings.Contains(body, `"setupRequired":true`) {
		t.Errorf("/api/session when ready: %d %s", code, body)
	}
	if code, _, _ := rawGet(t, ctx, addr, "evil.example", "/healthz"); code != 403 {
		t.Errorf("wrong host when ready: %d", code)
	}

	stop()
	if err := <-done; err != nil {
		t.Errorf("serve: %v\n%s", err, logs)
	}
	out := logs.String()
	for _, want := range []string{`msg="listening; starting"`, `msg="secret key check recorded"`, "msg=ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	noSecrets(t, "serve's log", out, dbURL, k)
}

// TestServeStartFailureEnds (#550): a start that fails after the sync and
// fill runners have started ends serve with its error instead of hanging
// in the deferred waits for those runners. runServe returns only after
// its deferred waits for the runners, so its return is the signal that
// they ended. Its context comes from the hang guard: without the fix the
// guard cancels the runners, so serve still returns, and the test fails
// cleanly instead of leaking a serve that holds the store.
func TestServeStartFailureEnds(t *testing.T) {
	cases := []struct {
		name string
		env  func(t *testing.T)
		want string
	}{{
		// The SMTP listen port is busy: net.Listen fails in startSMTP.
		name: "busy SMTP port",
		env: func(t *testing.T) {
			busy, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = busy.Close() })
			t.Setenv(config.EnvSMTPListen, busy.Addr().String())
			t.Setenv(config.EnvSMTPAuth, config.SMTPAuthOff)
			t.Setenv(config.EnvSMTPAllowedNetworks, "127.0.0.1/32")
		},
		want: config.EnvSMTPListen,
	}, {
		// Certificate files that exist but do not parse: the certificate
		// load fails in startSMTP, before it listens.
		name: "broken SMTP certificate",
		env: func(t *testing.T) {
			dir := t.TempDir()
			cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
			for _, p := range []string{cert, key} {
				if err := os.WriteFile(p, []byte("not a PEM file\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(config.EnvSMTPListen, "127.0.0.1:0")
			t.Setenv(config.EnvSMTPAuth, config.SMTPAuthRequired)
			t.Setenv(config.EnvSMTPAllowedNetworks, "")
			t.Setenv(config.EnvSMTPTLSCert, cert)
			t.Setenv(config.EnvSMTPTLSKey, key)
		},
		want: config.EnvSMTPTLSCert,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, dbURL := storetest.NewWithURL(t)
			startEnv(t, dbURL, false)
			t.Setenv(config.EnvAllowedHosts, "archive.lan")
			t.Setenv(config.EnvEncryptionAtSetup, "off")
			useKeyFile(t, newTestKey(t))
			tc.env(t)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
			defer cancel()
			logs := &lockedBuffer{}
			done := make(chan error, 1)
			go func() { done <- runServe(ctx, cfg, slog.New(slog.NewTextHandler(logs, nil)), ln) }()
			var serveErr error
			select {
			case serveErr = <-done:
			case <-ctx.Done():
				// The guard cancelled serve's context: let it return so
				// that nothing of it outlives the test.
				<-done
				t.Fatalf("serve did not end after its start failed:\n%s", logs)
			}
			// Both can be ready at once: serve that ended only because the
			// guard cancelled it hung as well.
			if ctx.Err() != nil {
				t.Fatalf("serve ended only when the hang guard cancelled it: %v\n%s", serveErr, logs)
			}
			if serveErr == nil || !strings.Contains(serveErr.Error(), tc.want) {
				t.Fatalf("serve: %v, want an error naming %s\n%s", serveErr, tc.want, logs)
			}
			if code, msg := exitStatus(context.Background(), serveErr); code != 1 || !strings.Contains(msg, tc.want) {
				t.Errorf("exit status: %d %q, want 1 and a message naming %s", code, msg, tc.want)
			}
			if strings.Contains(logs.String(), "msg=ready") {
				t.Errorf("serve got ready although its start failed:\n%s", logs)
			}
		})
	}
}
