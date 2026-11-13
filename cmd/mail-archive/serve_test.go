package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
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
