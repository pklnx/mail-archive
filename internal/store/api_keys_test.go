package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func newKey(t *testing.T, st *store.Store, userID int64, name string, scopes []string, expires *time.Time) (string, *store.APIKey) {
	t.Helper()
	key, hash, hint, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	k, err := st.CreateAPIKey(context.Background(), userID, store.NewAPIKey{Hash: hash, Hint: hint, Name: name, Scopes: scopes, ExpiresAt: expires})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return key, k
}

func TestNormalizeScopes(t *testing.T) {
	got, err := store.NormalizeScopes([]string{"sync", "read", "sync"})
	if err != nil || fmt.Sprint(got) != "[read sync]" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range [][]string{nil, {}, {"write"}, {"read", "admin"}} {
		if _, err := store.NormalizeScopes(bad); !errors.Is(err, store.ErrAPIKeyScope) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	// metrics stands alone (#397, part 3).
	if got, err := store.NormalizeScopes([]string{"metrics", "metrics"}); err != nil || fmt.Sprint(got) != "[metrics]" {
		t.Errorf("metrics: %v %v", got, err)
	}
	for _, bad := range [][]string{{"metrics", "read"}, {"sync", "metrics"}, {"read", "sync", "metrics"}} {
		if _, err := store.NormalizeScopes(bad); !errors.Is(err, store.ErrMetricsScopeAlone) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

// TestMetricsKey: only an admin holds a metrics key; the database refuses
// metrics beside another scope and an unknown scope, whoever writes.
func TestMetricsKey(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	admin, err := st.CreateUser(ctx, "admin", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	kim, err := st.CreateUser(ctx, "kim", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	_, hash, hint, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIKey(ctx, kim.ID, store.NewAPIKey{Hash: hash, Hint: hint, Name: "m", Scopes: []string{store.ScopeMetrics}}); !errors.Is(err, store.ErrMetricsKeyAdmin) {
		t.Fatalf("member's metrics key: %v", err)
	}
	key, k := newKey(t, st, admin.ID, "scrape", []string{store.ScopeMetrics}, nil)
	sess, got, err := st.APIKeyLogin(ctx, auth.HashAPIKey(key))
	if err != nil || !sess.IsAdmin || !got.HasScope(store.ScopeMetrics) || got.HasScope(store.ScopeRead) || got.ID != k.ID {
		t.Fatalf("login %+v %+v %v", sess, got, err)
	}
	for _, scopes := range []string{"{metrics,read}", "{sync,metrics}", "{admin}", "{}"} {
		_, err := conn.Exec(ctx, `INSERT INTO api_keys (user_id, key_hash, hint, name, scopes) VALUES ($1, $2, 'abcd', $3, $4::text[])`,
			admin.ID, make([]byte, 32), "raw "+scopes, scopes)
		if state, _ := store.SQLState(err); state != "23514" {
			t.Errorf("%s: %v", scopes, err)
		}
	}
}

// TestMetricsKeyMigration: 00042's Down deletes the metrics keys and
// keeps the others; Up again accepts metrics keys.
func TestMetricsKeyMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	admin, err := st.CreateUser(ctx, "admin", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	newKey(t, st, admin.ID, "scrape", []string{store.ScopeMetrics}, nil)
	readKey, _ := newKey(t, st, admin.ID, "cron", []string{store.ScopeRead, store.ScopeSync}, nil)
	downMetricsKeyScopeMigration(t, st)
	var names []string
	rows, err := conn.Query(ctx, "SELECT name FROM api_keys ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if rows.Err() != nil || fmt.Sprint(names) != "[cron]" {
		t.Fatalf("after Down: %v %v", names, rows.Err())
	}
	if _, err := conn.Exec(ctx, `INSERT INTO api_keys (user_id, key_hash, hint, name, scopes) VALUES ($1, $2, 'abcd', 'm', '{metrics}')`,
		admin.ID, make([]byte, 32)); err == nil {
		t.Fatal("the old check accepted a metrics key")
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(readKey)); err != nil {
		t.Fatalf("read key after re-Up: %v", err)
	}
	newKey(t, st, admin.ID, "scrape", []string{store.ScopeMetrics}, nil)
}

// downMetricsKeyScopeMigration rolls back 00042_metrics_key_scope.
func downMetricsKeyScopeMigration(t *testing.T, st *store.Store) {
	t.Helper()
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00042_metrics_key_scope") {
		t.Fatalf("down: %q %v", m, err)
	}
}

func TestAPIKeys(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", "h", true)
	if err != nil {
		t.Fatal(err)
	}

	key, k := newKey(t, st, alice.ID, "backup", []string{store.ScopeRead}, nil)
	if k.UserName != "alice" || k.Hint != key[len(key)-4:] {
		t.Fatalf("created %+v", k)
	}
	sess, got, err := st.APIKeyLogin(ctx, auth.HashAPIKey(key))
	if err != nil || sess.UserID != alice.ID || sess.UserName != "alice" || got.ID != k.ID || !got.HasScope(store.ScopeRead) || got.HasScope(store.ScopeSync) {
		t.Fatalf("login %+v %+v %v", sess, got, err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(key+"x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown key: %v", err)
	}

	// Names are unique among the keys that are not revoked.
	h := auth.HashAPIKey("other")
	if _, err := st.CreateAPIKey(ctx, alice.ID, store.NewAPIKey{Hash: h, Hint: "abcd", Name: "backup", Scopes: []string{"read"}}); !errors.Is(err, store.ErrAPIKeyName) {
		t.Fatalf("duplicate name: %v", err)
	}
	newKey(t, st, bob.ID, "backup", []string{"read"}, nil) // another user's name is no conflict

	// A user's key cannot be revoked by another user; an admin (owner nil)
	// can revoke any.
	if _, err := st.RevokeAPIKey(ctx, k.ID, &bob.ID, &bob.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke by another user: %v", err)
	}
	r, err := st.RevokeAPIKey(ctx, k.ID, &alice.ID, &alice.ID)
	if err != nil || r.UserName != "alice" || r.Name != "backup" {
		t.Fatalf("revoke: %+v %v", r, err)
	}
	if _, err := st.RevokeAPIKey(ctx, k.ID, nil, &bob.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(key)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked key: %v", err)
	}
	if ref, err := st.FindRefusedAPIKey(ctx, auth.HashAPIKey(key)); err != nil || ref.Reason != "revoked" || ref.Name != "backup" || ref.UserName != "alice" {
		t.Fatalf("refused: %+v %v", ref, err)
	}
	newKey(t, st, alice.ID, "backup", []string{"read"}, nil) // the name is free again

	// Expired.
	expKey, exp := newKey(t, st, alice.ID, "short", []string{"sync"}, new(time.Now().Add(time.Hour)))
	if _, err := conn.Exec(ctx, "UPDATE api_keys SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 second' WHERE id = $1", exp.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(expKey)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired key: %v", err)
	}
	if ref, _ := st.FindRefusedAPIKey(ctx, auth.HashAPIKey(expKey)); ref == nil || ref.Reason != "expired" {
		t.Fatalf("expired reason: %+v", ref)
	}

	// Locking suspends the keys; unlocking restores them.
	lockedKey, _ := newKey(t, st, alice.ID, "locked", []string{"read"}, nil)
	if err := st.SetUserLocked(ctx, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(lockedKey)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("locked owner: %v", err)
	}
	if ref, _ := st.FindRefusedAPIKey(ctx, auth.HashAPIKey(lockedKey)); ref == nil || ref.Reason != "locked" {
		t.Fatalf("locked reason: %+v", ref)
	}
	if err := st.SetUserLocked(ctx, alice.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.APIKeyLogin(ctx, auth.HashAPIKey(lockedKey)); err != nil {
		t.Fatalf("after unlock: %v", err)
	}

	// Lists and counts: revoked and expired keys are listed, not counted.
	list, err := st.ListAPIKeys(ctx, alice.ID)
	if err != nil || len(list) != 4 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	states := map[string]int{}
	for i := range list {
		states[list[i].State(time.Now())]++
	}
	if states[store.APIKeyActive] != 2 || states[store.APIKeyRevoked] != 1 || states[store.APIKeyExpired] != 1 {
		t.Fatalf("states %v", states)
	}
	if counts, _ := st.APIKeyCounts(ctx); counts[alice.ID] != 2 || counts[bob.ID] != 1 {
		t.Fatalf("counts %v", counts)
	}
	all, err := st.ListAllAPIKeys(ctx)
	if err != nil || len(all) != 5 || all[0].UserName != "alice" || all[4].UserName != "bob" {
		t.Fatalf("all: %d %v", len(all), err)
	}

	// Old revoked and expired keys go after 30 days.
	if _, err := conn.Exec(ctx, "UPDATE api_keys SET revoked_at = now() - interval '31 days' WHERE id = $1", k.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := st.DeleteOldAPIKeys(ctx); err != nil || n != 1 {
		t.Fatalf("delete old: %d %v", n, err)
	}

	// A password reset revokes the keys or keeps them; the user's removal
	// deletes them.
	if err := st.SetUserPassword(ctx, alice.ID, "h2", store.PasswordReset{MustChange: true}); err != nil {
		t.Fatal(err)
	}
	if counts, _ := st.APIKeyCounts(ctx); counts[alice.ID] != 2 {
		t.Fatalf("after reset with keep: %v", counts)
	}
	e := store.TargetedEntry(store.ActorCLI, "user.password_reset")
	if err := st.SetUserPassword(store.WithAudit(ctx, e), alice.ID, "h3", store.PasswordReset{MustChange: true, RevokeAPIKeys: true, By: &bob.ID}); err != nil {
		t.Fatal(err)
	}
	// The expired key is revoked too.
	if counts, _ := st.APIKeyCounts(ctx); counts[alice.ID] != 0 || e.Details["api_keys_revoked"] != int64(3) {
		t.Fatalf("after reset: %v %v", counts, e.Details)
	}
	var revokedBy int64
	if err := conn.QueryRow(ctx, "SELECT revoked_by FROM api_keys WHERE user_id = $1 AND name = 'locked'", alice.ID).Scan(&revokedBy); err != nil || revokedBy != bob.ID {
		t.Fatalf("revoked_by %d %v", revokedBy, err)
	}
	newKey(t, st, alice.ID, "after", []string{"read"}, nil)
	e = store.TargetedEntry(store.ActorCLI, "user.remove")
	if err := st.DeleteUser(store.WithAudit(ctx, e), alice.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM api_keys WHERE user_id = $1", alice.ID).Scan(&left); err != nil || left != 0 || e.Details["api_keys_removed"] != int64(1) {
		t.Fatalf("after removal: %d %v %v", left, err, e.Details)
	}
}

func TestAPIKeyLimitAndTouch(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	var first *store.APIKey
	for i := range store.MaxAPIKeys {
		_, k := newKey(t, st, u.ID, fmt.Sprintf("k%d", i), []string{"read"}, nil)
		if i == 0 {
			first = k
		}
	}
	_, hash, hint, _ := auth.NewAPIKey()
	if _, err := st.CreateAPIKey(ctx, u.ID, store.NewAPIKey{Hash: hash, Hint: hint, Name: "one more", Scopes: []string{"read"}}); !errors.Is(err, store.ErrAPIKeyLimit) {
		t.Fatalf("over the limit: %v", err)
	}
	// A revoked key frees a place.
	if _, err := st.RevokeAPIKey(ctx, first.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIKey(ctx, u.ID, store.NewAPIKey{Hash: hash, Hint: hint, Name: "one more", Scopes: []string{"read"}}); err != nil {
		t.Fatalf("after revoking: %v", err)
	}

	// TouchAPIKey writes at most once a minute.
	_, k := newKeyAfterRevoke(t, st, u.ID)
	if err := st.TouchAPIKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	var used time.Time
	if err := conn.QueryRow(ctx, "SELECT last_used_at FROM api_keys WHERE id = $1", k.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchAPIKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	var again time.Time
	if err := conn.QueryRow(ctx, "SELECT last_used_at FROM api_keys WHERE id = $1", k.ID).Scan(&again); err != nil || !again.Equal(used) {
		t.Fatalf("touched twice within a minute: %v %v", used, again)
	}
}

// newKeyAfterRevoke frees a place and creates a key in it.
func newKeyAfterRevoke(t *testing.T, st *store.Store, userID int64) (string, *store.APIKey) {
	t.Helper()
	list, err := st.ListAPIKeys(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range list {
		if list[i].RevokedAt == nil {
			if _, err := st.RevokeAPIKey(context.Background(), list[i].ID, nil, nil); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	return newKey(t, st, userID, "touched", []string{"read"}, nil)
}
