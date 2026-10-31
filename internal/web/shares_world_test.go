package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// shareWorld is an API fixture with users who own, share and read
// accounts (#389). Users and accounts are named; an account is addressed
// as "owner/name".
type shareWorld struct {
	*apiFixture
	users    map[string]*store.User
	creds    map[string]credential
	accounts map[string]*store.Account
}

func newShareWorld(t *testing.T) *shareWorld {
	t.Helper()
	return &shareWorld{apiFixture: newAPIFixture(t), users: map[string]*store.User{}, creds: map[string]credential{}, accounts: map[string]*store.Account{}}
}

// user creates a user with a session (credential name) and a read key
// (credential name+"-key"). An admin gets two-factor authentication
// marked as set up, which admins need for every request.
func (w *shareWorld) user(name string, admin bool) *store.User {
	w.t.Helper()
	ctx := context.Background()
	u, err := w.st.CreateUser(ctx, name, "unused", admin)
	if err != nil {
		w.t.Fatal(err)
	}
	if admin {
		w.sql("UPDATE users SET two_factor_enabled = true, two_factor_secret = '\\x01' WHERE id = $1", u.ID)
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.st.CreateSession(ctx, hash, u.ID, time.Now().Add(time.Hour), ""); err != nil {
		w.t.Fatal(err)
	}
	w.users[name] = u
	w.creds[name] = credential{name: name, cookie: token}
	w.creds[name+"-key"] = credential{name: name + "'s key", key: readKey(w.t, w.st, u)}
	return u
}

func (w *shareWorld) sql(query string, args ...any) {
	w.t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.dbURL)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, query, args...); err != nil {
		w.t.Fatal(err)
	}
}

// account creates an IMAP account of owner.
func (w *shareWorld) account(owner, name string) *store.Account {
	w.t.Helper()
	id := w.users[owner].ID
	a := &store.Account{Name: name, OwnerID: &id, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true}
	if err := w.st.CreateAccount(context.Background(), a); err != nil {
		w.t.Fatal(err)
	}
	w.accounts[owner+"/"+name] = a
	return a
}

// put stores raw under name in the account's folder, like a sync.
func (w *shareWorld) put(key, folder, name, raw string) {
	w.t.Helper()
	ctx := context.Background()
	fo, err := w.st.GetOrCreateFolder(ctx, w.accounts[key].ID, folder)
	if err != nil {
		w.t.Fatal(err)
	}
	if fo.UIDValidity == 0 {
		if err := w.st.ResetFolder(ctx, fo.ID, 1); err != nil {
			w.t.Fatal(err)
		}
		fo.UIDValidity = 1
	}
	blob, _, err := w.blobs.Put(strings.NewReader(raw))
	if err != nil {
		w.t.Fatal(err)
	}
	meta, err := archive.BuildMeta(ctx, w.st, w.blobs, blob, true)
	if err != nil {
		w.t.Fatal(err)
	}
	uid := fo.LastUID + 1
	loc := store.Location{FolderID: fo.ID, UIDValidity: fo.UIDValidity, UID: uid}
	if _, err := w.st.SaveBatch(ctx, fo.ID, uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		w.t.Fatal(err)
	}
	w.ids[name] = blob.SHA256
}

// current returns the stored state of an account, for its version.
func (w *shareWorld) current(key string) *store.Account {
	w.t.Helper()
	a, err := w.st.GetAccount(context.Background(), w.accounts[key].ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return a
}

func (w *shareWorld) share(key, user string) {
	w.t.Helper()
	if _, err := w.st.ShareAccount(context.Background(), w.current(key).Ref(), w.users[user].ID); err != nil {
		w.t.Fatal(err)
	}
}

func (w *shareWorld) unshare(key, user string) {
	w.t.Helper()
	if err := w.st.UnshareAccount(context.Background(), w.current(key).Ref(), w.users[user].ID); err != nil {
		w.t.Fatal(err)
	}
}

// send sends a request with c like the UI does (JSON, same origin) and
// returns the status and the body.
func (w *shareWorld) send(c credential, method, path string) (int, []byte) {
	w.t.Helper()
	req, _ := http.NewRequest(method, w.srv.URL+path, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", w.srv.URL)
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// list returns the names on one page of GET /api/messages as c, and the
// next cursor.
func (w *shareWorld) list(c credential, query string) ([]string, string) {
	w.t.Helper()
	var r listResp
	w.fetchJSON(c, "/api/messages?"+query, &r)
	names := []string{}
	for _, m := range r.Messages {
		names = append(names, w.name(m.ID))
	}
	next := ""
	if r.Next != nil {
		next = *r.Next
	}
	return names, next
}

// log returns c's Log page (GET /api/audit) and its raw body.
func (w *shareWorld) log(c credential, query string) ([]auditEntryJSON, string) {
	w.t.Helper()
	var p auditPageJSON
	body := w.fetchJSON(c, "/api/audit?"+query, &p)
	return p.Entries, string(body)
}

// detailMap decodes an entry's details.
func detailMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// viaOwners returns the via_owners of a stored entry as user IDs.
func viaOwners(t *testing.T, r store.AuditRecord) []int64 {
	t.Helper()
	v, _ := details(t, r)[store.DetailViaOwners].([]any)
	out := []int64{}
	for _, x := range v {
		out = append(out, int64(x.(float64)))
	}
	return out
}

// viaKeys returns "key:n" (or "key" without n) for each via_accounts
// element of details.
func viaKeys(d map[string]any) []string {
	out := []string{}
	v, _ := d[store.DetailViaAccounts].([]any)
	for _, x := range v {
		m := x.(map[string]any)
		s := fmt.Sprint(m["key"])
		if n, ok := m["n"]; ok {
			s += fmt.Sprintf(":%v", n)
		}
		out = append(out, s)
	}
	return out
}

// mail builds a plain message.
func mail(id, subject, date, body string, headers ...string) string {
	h := ""
	for _, x := range headers {
		h += x + "\n"
	}
	return crlf(fmt.Sprintf("Message-ID: <%s@x>\nFrom: Sender <s@example.com>\nTo: r@example.com\nSubject: %s\nDate: %s\n%sContent-Type: text/plain; charset=utf-8\n\n%s\n",
		id, subject, date, h, body))
}
