package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestDeleteAccountSmallPool (#176): deleting an account holds its sync
// lock and runs the delete on one connection. With a separate lock
// connection, a pool of one connection made the delete wait for ever.
func TestDeleteAccountSmallPool(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Created before any user, so they belong to the first one (tester).
	var ids []int64
	for _, name := range []string{"empty", "busy"} {
		a := &store.Account{Name: name, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true}
		if err := st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	small, err := store.Open(ctx, storetest.WithMaxConns(t, url, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(small.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	syncer := &archive.Syncer{Store: small, Logger: log}
	s := New(small, nil, log, Options{AllowedHosts: []string{"127.0.0.1"}, Syncer: syncer, Runner: &archive.Runner{Syncer: syncer}})
	srv := httptest.NewServer(loggedIn(t, st, s.Handler()))
	t.Cleanup(srv.Close)

	del := func(name string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "DELETE", srv.URL+"/api/accounts/"+name, http.NoBody)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", srv.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE %s: %v (ctx %v)", name, err, ctx.Err())
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}

	var res struct {
		Result string `json:"result"`
	}
	if code, out := del("empty"); code != http.StatusOK || json.Unmarshal(out, &res) != nil || res.Result != "deleted" {
		t.Fatalf("delete: %d %s", code, out)
	}

	obs, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = obs.Close(context.Background()) }()
	var n int
	if err := obs.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'account.remove' AND details->>'result' = 'deleted'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit entries %d %v", n, err)
	}
	const locks = `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`
	if err := obs.QueryRow(ctx, locks).Scan(&n); err != nil || n != 0 {
		t.Fatalf("advisory locks left: %d %v", n, err)
	}

	// Busy: another session holds the account's sync lock.
	if _, err := obs.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", 0x6d61, ids[1]); err != nil {
		t.Fatal(err)
	}
	if code, out := del("busy"); code != http.StatusConflict {
		t.Fatalf("delete while synced: %d %s", code, out)
	}
	if _, err := st.GetAccount(ctx, ids[1]); err != nil {
		t.Fatalf("busy account: %v", err)
	}
}
