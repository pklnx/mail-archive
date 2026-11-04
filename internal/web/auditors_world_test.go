package web

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// auditorWorld is a groupWorld (a, b and the admin x in "Default", k in
// "B"; a owns info, k owns kb) with the auditors auda (Default) and audb
// (B), and a's second account (#393, part 2):
//
//	a/info INBOX:   M (msgShared, 2026-10-05), Old (2021-06-01)
//	a/info Private: P (2026-10-05)
//	a/second INBOX: S (2026-10-05)
//	k/kb INBOX:     K (2026-10-05)
//
// grant is a's grant to auda of a/info's INBOX for 2026; auda's session
// (credential "auda") is bound to it.
type auditorWorld struct {
	*groupWorld
	grant int64
}

const auditorTestPassword = "correct horse battery"

func newAuditorWorld(t *testing.T) *auditorWorld {
	t.Helper()
	w := &auditorWorld{groupWorld: newGroupWorld(t)}
	w.auditorUser("auda", w.def)
	w.auditorUser("audb", w.b)
	w.account("a", "second")
	w.put("a/info", "INBOX", "M", msgShared)
	w.put("a/info", "INBOX", "Old", mail("old", "Steuer 2021", "Tue, 1 Jun 2021 10:00:00 +0000", "old mail"))
	w.put("a/info", "Private", "P", mail("p", "Privat", "Mon, 5 Oct 2026 11:00:00 +0000", "private mail"))
	w.put("a/second", "INBOX", "S", mail("s", "Second", "Mon, 5 Oct 2026 12:00:00 +0000", "second mail"))
	w.put("k/kb", "INBOX", "K", mail("k", "Kunde B", "Mon, 5 Oct 2026 13:00:00 +0000", "group b mail"))
	// Owners who create grants over the API need a password (step-up).
	hash, err := cheapHasher.Hash(context.Background(), auditorTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	w.sql("UPDATE users SET password_hash = $1", hash)
	w.grant = w.makeGrant("a", "auda", "info", []string{"INBOX"})
	w.creds["auda"] = w.auditorSession("auda", w.grant)
	return w
}

// auditorUser creates an auditor of group g with 2FA marked as set up
// (auditors need it, as admins do) and no session yet.
func (w *auditorWorld) auditorUser(name string, g *store.Group) *store.User {
	w.t.Helper()
	u, err := w.st.CreateUserInGroup(context.Background(), store.NewUser{Name: name, PasswordHash: "unused", GroupID: g.ID, Auditor: true})
	if err != nil {
		w.t.Fatal(err)
	}
	w.sql("UPDATE users SET two_factor_enabled = true, two_factor_secret = '\\x01' WHERE id = $1", u.ID)
	w.users[name] = u
	return u
}

// auditorSession starts a session of the auditor, bound to grant unless
// it is 0.
func (w *auditorWorld) auditorSession(name string, grant int64) credential {
	w.t.Helper()
	ctx := context.Background()
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		w.t.Fatal(err)
	}
	u := w.users[name]
	if err := w.st.CreateSession(ctx, hash, u.ID, time.Now().Add(time.Hour), ""); err != nil {
		w.t.Fatal(err)
	}
	if grant != 0 {
		if err := w.st.SetSessionGrant(ctx, hash, u.ID, grant); err != nil {
			w.t.Fatal(err)
		}
	}
	return credential{name: name, cookie: token}
}

// period2026 is the grants' message period.
var period2026From, period2026Until = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

// makeGrant stores a grant of owner's account to auditor, for 2026 and 28
// days, with the named folders (nil: all).
func (w *auditorWorld) makeGrant(owner, auditor, account string, folders []string) int64 {
	w.t.Helper()
	ctx := context.Background()
	acc := w.accounts[owner+"/"+account]
	ga := store.GrantAccount{AccountID: acc.ID}
	if folders != nil {
		states, err := w.st.ListFolderStates(ctx, acc.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		ga.Folders = []int64{}
		for _, f := range states {
			if slices.Contains(folders, f.Name) {
				ga.Folders = append(ga.Folders, f.ID)
			}
		}
	}
	from, until := period2026From, period2026Until
	id, err := w.st.CreateGrant(ctx, store.NewGrant{
		OwnerID: w.users[owner].ID, AuditorID: w.users[auditor].ID, Label: "Betriebsprüfung " + owner,
		Accounts: []store.GrantAccount{ga}, PeriodFrom: &from, PeriodUntil: &until, EndsAt: time.Now().AddDate(0, 0, 28),
	})
	if err != nil {
		w.t.Fatalf("grant %s/%s to %s: %v", owner, account, auditor, err)
	}
	return id
}

// auditorEntry checks that e is an entry of auditor's session through
// grant: the grant and its owner, which put it into the owner's Log.
func (w *auditorWorld) auditorEntry(e store.AuditRecord, auditor string, grant int64, owner string) error {
	d := details(w.t, e)
	if e.UserID == nil || *e.UserID != w.users[auditor].ID {
		return fmt.Errorf("%s: user %v, want %s", e.Action, e.UserID, auditor)
	}
	if g, _ := d[store.DetailAuditorGrant].(float64); int64(g) != grant {
		return fmt.Errorf("%s: auditor_grant %v, want %d", e.Action, d[store.DetailAuditorGrant], grant)
	}
	if via := viaOwners(w.t, e); !slices.Equal(via, []int64{w.users[owner].ID}) {
		return fmt.Errorf("%s: via_owners %v, want [%d]", e.Action, via, w.users[owner].ID)
	}
	return nil
}

// fingerprint counts the rows that an auditor's refused request must leave
// alone.
func (w *auditorWorld) fingerprint() string {
	w.t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.dbURL)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var out string
	err = conn.QueryRow(ctx, `SELECT concat_ws(',',
		(SELECT count(*) FROM users), (SELECT count(*) FROM accounts), (SELECT count(*) FROM account_shares),
		(SELECT count(*) FROM auditor_grants), (SELECT count(*) FROM auditor_grants WHERE revoked_at IS NOT NULL),
		(SELECT max(ends_at)::text FROM auditor_grants), (SELECT count(*) FROM api_keys), (SELECT count(*) FROM customer_groups),
		(SELECT count(*) FROM retention_rules), (SELECT count(*) FROM legal_holds), (SELECT count(*) FROM deletion_runs),
		(SELECT count(*) FROM sessions), (SELECT count(*) FROM passkeys), (SELECT count(*) FROM smtp_fills),
		(SELECT count(*) FROM jobs), (SELECT count(*) FROM account_purges),
		(SELECT string_agg(name || ':' || is_admin || ':' || (locked_at IS NULL) || ':' || group_id || ':' || language, ';' ORDER BY id) FROM users),
		(SELECT string_agg(name || ':' || enabled || ':' || (removed_at IS NULL), ';' ORDER BY id) FROM accounts))`).Scan(&out)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// queryRow scans the one row of query into dest.
func (w *auditorWorld) queryRow(dest any, query string, args ...any) {
	w.t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.dbURL)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := conn.QueryRow(ctx, query, args...).Scan(dest); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
}

// shiftGrant moves a grant's window by d (storetest.ShiftGrant).
func (w *auditorWorld) shiftGrant(id int64, d time.Duration) {
	w.t.Helper()
	storetest.ShiftGrant(w.t, w.dbURL, id, d)
}

// decodeSession decodes an answer of GET /api/session.
func decodeSession(t *testing.T, body []byte) sessionJSON {
	t.Helper()
	var s sessionJSON
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return s
}
