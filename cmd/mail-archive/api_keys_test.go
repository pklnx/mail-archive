package main

import (
	"context"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestUserAPIKeyCommands(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	add := func(name string) string {
		key, hash, hint, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateAPIKey(ctx, u.ID, store.NewAPIKey{Hash: hash, Hint: hint, Name: name, Scopes: []string{"read", "sync"}}); err != nil {
			t.Fatal(err)
		}
		return key
	}
	run := func(args ...string) string {
		t.Helper()
		var err error
		out := captureStdout(t, func() { err = runUser(t, "", args...) })
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}

	if out := run("api-keys", "alice"); !strings.Contains(out, "no API keys") {
		t.Fatalf("no keys: %q", out)
	}
	key := add("backup")
	if out := run("list"); !strings.Contains(out, "API KEYS") || !strings.Contains(out, "alice") {
		t.Fatalf("list: %q", out)
	}
	out := run("api-keys", "alice")
	if !strings.Contains(out, "backup") || !strings.Contains(out, "ma_…"+key[51:]) || !strings.Contains(out, "read,sync") ||
		!strings.Contains(out, "active") || strings.Contains(out, key) {
		t.Fatalf("api-keys: %q", out)
	}

	// reset-password --keep-api-keys keeps them; a plain reset revokes them.
	if out := run("reset-password", "alice", "--keep-api-keys"); strings.Contains(out, "revoked") {
		t.Fatalf("keep: %q", out)
	}
	if counts, _ := st.APIKeyCounts(ctx); counts[u.ID] != 1 {
		t.Fatalf("after keep: %v", counts)
	}
	if out := run("reset-password", "alice"); !strings.Contains(out, `1 API key(s) of "alice" revoked`) {
		t.Fatalf("reset: %q", out)
	}
	if out := run("api-keys", "alice"); !strings.Contains(out, "revoked") {
		t.Fatalf("state after reset: %q", out)
	}

	add("one")
	add("two")
	if out := run("revoke-api-keys", "alice"); !strings.Contains(out, `2 API key(s) of "alice" revoked`) {
		t.Fatalf("revoke: %q", out)
	}
	if counts, _ := st.APIKeyCounts(ctx); counts[u.ID] != 0 {
		t.Fatalf("after revoke: %v", counts)
	}
	entries := 0
	if err := st.ExportAudit(ctx, store.AuditFilter{Actions: []string{"api_keys"}, Since: u.CreatedAt.AddDate(0, 0, -1), Until: u.CreatedAt.AddDate(0, 0, 1)},
		func(r store.AuditRecord) error {
			if r.Action != "api_key.revoke" || r.Actor != store.ActorCLI || !strings.Contains(string(r.Details), `"api_keys_revoked": 2`) {
				t.Errorf("entry %+v %s", r, r.Details)
			}
			entries++
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Fatalf("%d revoke entries", entries)
	}
}
