package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestUsers(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()

	if n, err := st.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("count: %d, %v", n, err)
	}
	admin, err := st.CreateUser(ctx, "admin", "h1", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "admin", "h2", false); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	// The database only takes normalized names.
	if _, err := st.CreateUser(ctx, "Admin", "h2", false); err == nil {
		t.Fatal("uppercase name stored")
	}
	alice, err := st.CreateUser(ctx, "alice", "h3", false)
	if err != nil {
		t.Fatal(err)
	}

	// The last unlocked admin can be neither locked nor removed.
	if err := st.SetUserLocked(ctx, admin.ID, true); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("lock last admin: %v", err)
	}
	if err := st.DeleteUser(ctx, admin.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("delete last admin: %v", err)
	}
	second, _ := st.CreateUser(ctx, "second", "h4", true)
	if err := st.SetUserLocked(ctx, admin.ID, true); err != nil {
		t.Fatalf("lock with a second admin: %v", err)
	}
	if err := st.DeleteUser(ctx, second.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("delete the only unlocked admin: %v", err)
	}
	if err := st.SetUserLocked(ctx, admin.ID, false); err != nil {
		t.Fatal(err)
	}

	// Removing a user removes their sessions.
	sess := make([]byte, 32)
	sess[0] = 1
	if err := st.CreateSession(ctx, sess, alice.ID, time.Now().Add(time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, sess, time.Now().Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session of removed user: %v", err)
	}
	if err := st.DeleteUser(ctx, alice.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}

	users, err := st.ListUsers(ctx)
	if err != nil || len(users) != 2 || users[0].Name != "admin" || users[1].Name != "second" {
		t.Fatalf("list: %v, %v", users, err)
	}
}

func TestUserSettings(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	if alice.Language != "auto" || alice.Locale != "auto" {
		t.Fatalf("new user: %q %q", alice.Language, alice.Locale)
	}
	change := func(c store.ProfileChange) (bool, error) {
		e := &store.AuditEntry{Actor: store.ActorSession, Action: "profile.change"}
		return st.UpdateProfile(store.WithAudit(ctx, e), alice.ID, c)
	}
	de, deAT := "de", "de-AT"

	// One setting changes, the other stays.
	if ok, err := change(store.ProfileChange{Language: &de}); !ok || err != nil {
		t.Fatalf("language: %v %v", ok, err)
	}
	if ok, err := change(store.ProfileChange{Locale: &deAT}); !ok || err != nil {
		t.Fatalf("locale: %v %v", ok, err)
	}
	sess := make([]byte, 32)
	if err := st.CreateSession(ctx, sess, alice.ID, time.Now().Add(time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	s, err := st.GetSession(ctx, sess, time.Now().Add(-time.Hour))
	if err != nil || s.Language != "de" || s.Locale != "de-AT" {
		t.Fatalf("session: %+v %v", s, err)
	}
	if u, err := st.GetUserByID(ctx, alice.ID); err != nil || u.Language != "de" || u.Locale != "de-AT" {
		t.Fatalf("user: %+v %v", u, err)
	}

	// The same values change nothing and write no entry.
	if ok, err := change(store.ProfileChange{Language: &de, Locale: &deAT}); ok || err != nil {
		t.Fatalf("unchanged: %v %v", ok, err)
	}
	rows := auditRows(t, conn)
	if len(rows) != 2 {
		t.Fatalf("entries: %+v", rows)
	}
	if d := rows[0].Details; fmt.Sprint(d["fields"]) != "[language]" || fmt.Sprint(d["language"]) != "map[from:auto to:de]" || d["locale"] != nil {
		t.Fatalf("first entry: %v", d)
	}
	if d := rows[1].Details; fmt.Sprint(d["fields"]) != "[locale]" || fmt.Sprint(d["locale"]) != "map[from:auto to:de-AT]" {
		t.Fatalf("second entry: %v", d)
	}
	if _, err := st.UpdateProfile(ctx, alice.ID+1000, store.ProfileChange{Language: &de}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}

	// The database checks the values.
	for _, q := range []string{
		`UPDATE users SET language = 'DE'`,
		`UPDATE users SET language = 'german'`,
		`UPDATE users SET locale = 'de_AT'`,
		`UPDATE users SET locale = ''`,
	} {
		if _, err := conn.Exec(ctx, q); err == nil {
			t.Errorf("%s: accepted", q)
		}
	}
	if _, err := conn.Exec(ctx, `UPDATE users SET language = 'pt-BR', locale = 'zh-Hant-TW'`); err != nil {
		t.Errorf("valid tags: %v", err)
	}

	// Concurrent changes record a chain of old values.
	auto := "auto"
	if _, err := change(store.ProfileChange{Language: &auto}); err != nil {
		t.Fatal(err)
	}
	before := len(auditRows(t, conn))
	var wg sync.WaitGroup
	for _, l := range []string{"de", "en"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := change(store.ProfileChange{Language: &l}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rows = auditRows(t, conn)[before:]
	if len(rows) != 2 {
		t.Fatalf("concurrent entries: %+v", rows)
	}
	// The entry time is the transaction start, so the order of the rows
	// says nothing; the chain must hold one way round.
	first, _ := rows[0].Details["language"].(map[string]any)
	second, _ := rows[1].Details["language"].(map[string]any)
	if first["from"] != "auto" {
		first, second = second, first
	}
	if first["from"] != "auto" || second["from"] != first["to"] {
		t.Fatalf("chain: %v then %v", first, second)
	}
}
