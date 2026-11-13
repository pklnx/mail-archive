package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/quota"
	"github.com/pklnx/mail-archive/internal/store"
)

// usageWorld is a groupWorld (a, b and the admin x in "Default", k in
// "B"; a owns info, k owns kb) with mail in
// info and kb and a usage snapshot.
func newUsageWorld(t *testing.T) *groupWorld {
	t.Helper()
	w := newGroupWorld(t)
	// The invoice is in the fixture's alice and bob too, which have no
	// owner.
	w.put("a/info", "INBOX", "info1", msgInvoice)
	w.put("a/info", "INBOX", "info2", crlf("Message-ID: <info-only@x>\nSubject: Nur in info\n\nInhalt\n"))
	w.put("k/kb", "INBOX", "kb1", crlf("Message-ID: <kb-only@x>\nSubject: Nur in kb\n\nInhalt\n"))
	if _, err := w.st.RefreshUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

// getJSON sends GET path as c, expects want and decodes the body into v.
func (w *groupWorld) getJSON(c credential, path string, want int, v any) []byte {
	w.t.Helper()
	status, body := w.send(c, "GET", path)
	if status != want {
		w.t.Fatalf("GET %s as %s: %d %s", path, c.name, status, body)
	}
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			w.t.Fatalf("GET %s: %v: %s", path, err, body)
		}
	}
	return body
}

// TestUsageViews: an admin gets the instance, every group and every
// account with owner, group and exclusive bytes; a member gets the size of
// their own accounts and the state, nothing of other users' accounts or
// groups; a user with a share of another's account gets no figures of
// it; auditors and API keys are refused.
func TestUsageViews(t *testing.T) {
	w := newUsageWorld(t)
	w.server.storageQuota = 1 << 30

	var admin usageAdminJSON
	w.getJSON(w.creds["x"], "/api/usage", http.StatusOK, &admin)
	if admin.ComputedAt == nil || admin.Instance.State != "ok" || admin.Instance.QuotaBytes == nil || *admin.Instance.QuotaBytes != 1<<30 ||
		admin.Instance.Bytes <= 0 || admin.Instance.Messages != 6 || admin.Instance.WarnPercent != 80 || admin.Instance.Encryption != "off" {
		t.Fatalf("instance %+v, computed %v", admin.Instance, admin.ComputedAt)
	}
	groups := map[string]usageGroupJSON{}
	for _, g := range admin.Groups {
		groups[g.Name] = g
	}
	if len(groups) != 2 || groups["Default"].Messages != 5 || groups["B"].Messages != 1 || groups["B"].State != "ok" {
		t.Fatalf("groups %+v", admin.Groups)
	}
	accounts := map[string]usageAccountJSON{}
	for _, a := range admin.Accounts {
		accounts[a.Owner+"/"+a.Name] = a
	}
	info, kb := accounts["a/info"], accounts["k/kb"]
	if !info.Counted || info.Messages != 2 || info.Own || info.GroupID == nil || *info.GroupID != w.def.ID || info.Kind != "imap" {
		t.Fatalf("info %+v", info)
	}
	// Only info-only is info's alone.
	if info.ExclusiveBytes <= 0 || info.ExclusiveBytes >= info.Bytes {
		t.Fatalf("info exclusive %d of %d", info.ExclusiveBytes, info.Bytes)
	}
	if kb.Messages != 1 || kb.GroupID == nil || *kb.GroupID != w.b.ID || kb.ExclusiveBytes != kb.Bytes {
		t.Fatalf("kb %+v", kb)
	}

	// A member: their own accounts only, by name, without exclusive bytes.
	var member map[string]any
	body := w.getJSON(w.creds["a"], "/api/usage", http.StatusOK, &member)
	var a usageMemberJSON
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, acc := range a.Accounts {
		names = append(names, acc.Name)
		if acc.Name == "info" && (acc.Messages == nil || *acc.Messages != 2 || *acc.Bytes != info.Bytes) {
			t.Fatalf("a's info %+v", acc)
		}
	}
	slices.Sort(names)
	if a.State != "ok" || !slices.Equal(names, []string{"info"}) {
		t.Fatalf("a: %s", body)
	}
	for _, leak := range []string{"kb", `"B"`, "Default", "exclusive", "quota", "group", "computed"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("a's answer contains %s: %s", leak, body)
		}
	}
	// k sees kb only.
	var k usageMemberJSON
	w.getJSON(w.creds["k"], "/api/usage", http.StatusOK, &k)
	if len(k.Accounts) != 1 || k.Accounts[0].Name != "kb" {
		t.Fatalf("k %+v", k)
	}
	// b reads a's info through a share: no figures of it.
	w.share("a/info", "b")
	if body := w.getJSON(w.creds["b"], "/api/usage", http.StatusOK, nil); strings.TrimSpace(string(body)) != `{"state":"ok","accounts":[]}` {
		t.Fatalf("b: %s", body)
	}

	// A group's persisted state and since when: B goes to warn.
	if _, err := w.st.TransitionStorageState(context.Background(), &w.b.ID, func(string, store.ScopeFigure) (string, error) { return "warn", nil }); err != nil {
		t.Fatal(err)
	}
	w.getJSON(w.creds["x"], "/api/usage", http.StatusOK, &admin)
	for _, g := range admin.Groups {
		if g.Name == "B" && (g.State != "warn" || g.StateSince == nil) {
			t.Fatalf("B after warn: %+v", g)
		}
		if g.Name == "Default" && (g.State != "ok" || g.StateSince != nil) {
			t.Fatalf("Default: %+v", g)
		}
	}

	// API keys get no session route.
	if status, _ := w.send(w.creds["a-key"], "GET", "/api/usage"); status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatalf("key: %d", status)
	}
}

// TestUsageAuditorRefused: an auditor's session gets 403 on both usage
// routes.
func TestUsageAuditorRefused(t *testing.T) {
	w := newAuditorWorld(t)
	for _, rt := range []struct{ method, path string }{{"GET", "/api/usage"}, {"POST", "/api/usage/refresh"}} {
		if status, body := w.send(w.creds["auda"], rt.method, rt.path); status != http.StatusForbidden {
			t.Fatalf("%s %s: %d %s", rt.method, rt.path, status, body)
		}
	}
}

// TestUsageMemberFields: a member's answer has exactly the allowlisted
// fields, by reflection over its types and in the encoded answer: no group
// id, no limit, no cause, no exclusive bytes.
func TestUsageMemberFields(t *testing.T) {
	allowed := map[reflect.Type][]string{
		reflect.TypeFor[usageMemberJSON]():     {"accounts", "state"},
		reflect.TypeFor[usageOwnAccountJSON](): {"bytes", "messages", "name"},
	}
	for typ, want := range allowed {
		var got []string
		for f := range typ.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			got = append(got, name)
			switch f.Type.Kind() {
			case reflect.String, reflect.Slice:
			case reflect.Pointer:
				if f.Type.Elem().Kind() != reflect.Int64 {
					t.Errorf("%s.%s: %s", typ, f.Name, f.Type)
				}
			default:
				t.Errorf("%s.%s: %s", typ, f.Name, f.Type)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s fields %v, want %v", typ, got, want)
		}
	}
	w := newUsageWorld(t)
	var raw map[string]json.RawMessage
	w.getJSON(w.creds["a"], "/api/usage", http.StatusOK, &raw)
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range raw {
			if !yield(k) {
				return
			}
		}
	})
	if !slices.Equal(keys, []string{"accounts", "state"}) {
		t.Fatalf("keys %v", keys)
	}
	var accounts []map[string]any
	if err := json.Unmarshal(raw["accounts"], &accounts); err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		if len(a) != 3 {
			t.Fatalf("account %v", a)
		}
	}
	// The enum: ok, full and overflow only.
	for st, want := range map[quota.State]string{quota.OK: "ok", quota.Warn: "ok", quota.Full: "full", quota.Overflow: "overflow", quota.DiskLow: "overflow"} {
		if got := memberState(st); got != want {
			t.Errorf("memberState(%s) = %s, want %s", st, got, want)
		}
	}
}

// TestUsageMemberSameAnswer (#539): a member's answer is the same, byte
// for byte, whether their group, the instance or the disk reached the
// limit; a warn of either is not shown.
func TestUsageMemberSameAnswer(t *testing.T) {
	w := newUsageWorld(t)
	var free atomic.Int64
	free.Store(1 << 40)
	db := &gateFigures{aState: "ok", instState: "ok", groupA: w.def.ID}
	gate := quota.New(quota.Config{Persist: true, InstanceQuota: 100, MinFreeDisk: 1000,
		Thresholds: quota.Thresholds{WarnPercent: 80, OverflowPercent: 10}, Interval: 24 * time.Hour},
		db, func() (int64, error) { return free.Load(), nil }, nil, nil)
	w.server.syncer = &archive.Syncer{Storage: gate}
	answer := func(aBytes, instBytes, freeBytes int64) string {
		t.Helper()
		free.Store(freeBytes)
		db.set(gate, aBytes, instBytes)
		return string(w.getJSON(w.creds["a"], "/api/usage", http.StatusOK, nil))
	}
	ok := answer(0, 0, 1<<40)
	if !strings.Contains(ok, `"state":"ok"`) {
		t.Fatalf("ok: %s", ok)
	}
	for _, c := range []struct {
		name                    string
		aBytes, instBytes, free int64
		want                    string
	}{
		{"group warn", 85, 10, 1 << 40, ok},
		{"instance warn", 10, 85, 1 << 40, ok},
	} {
		if got := answer(c.aBytes, c.instBytes, c.free); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	groupFull := answer(100, 10, 1<<40)
	if !strings.Contains(groupFull, `"state":"full"`) {
		t.Fatalf("group full: %s", groupFull)
	}
	if got := answer(10, 100, 1<<40); got != groupFull {
		t.Errorf("instance full: %s, group full: %s", got, groupFull)
	}
	groupOver := answer(110, 10, 1<<40)
	if !strings.Contains(groupOver, `"state":"overflow"`) {
		t.Fatalf("group overflow: %s", groupOver)
	}
	if got := answer(10, 110, 1<<40); got != groupOver {
		t.Errorf("instance overflow: %s, group overflow: %s", got, groupOver)
	}
	if got := answer(10, 10, 10); got != groupOver {
		t.Errorf("disk low: %s, group overflow: %s", got, groupOver)
	}
	// k's group B has no quota in the fake: group A's full state is not
	// k's.
	free.Store(1 << 40)
	db.set(gate, 100, 10)
	if body := string(w.getJSON(w.creds["k"], "/api/usage", http.StatusOK, nil)); !strings.Contains(body, `"state":"ok"`) {
		t.Fatalf("k while A is full: %s", body)
	}
}

// TestUsageRefresh: only admins request a snapshot (202, the job is
// queued); a member's request queues nothing.
func TestUsageRefresh(t *testing.T) {
	w := newGroupWorld(t)
	// The group moves of the set-up requested one already.
	w.sql(`DELETE FROM jobs WHERE kind = 'usage_refresh'`)
	queued := func() int {
		t.Helper()
		var n int
		w.queryRow(&n, `SELECT count(*) FROM jobs WHERE kind = 'usage_refresh' AND state = 'queued'`)
		return n
	}
	if status, body := w.send(w.creds["a"], "POST", "/api/usage/refresh"); status != http.StatusForbidden || queued() != 0 {
		t.Fatalf("member: %d %s, queued %d", status, body, queued())
	}
	if status, body := w.send(w.creds["x"], "POST", "/api/usage/refresh"); status != http.StatusAccepted || queued() != 1 {
		t.Fatalf("admin: %d %s, queued %d", status, body, queued())
	}
}

// TestGroupQuotaAPI: PATCH /api/groups/{id} with quotaBytes sets, changes
// and removes a group's quota, audited as group.quota under the group;
// non-admins get the same 403 for an existing and an unknown id; a quota
// above the instance quota, a bad value and a body with both or neither
// field get 400; GET /api/groups shows the quota, the usage and the state.
func TestGroupQuotaAPI(t *testing.T) {
	w := newUsageWorld(t)
	w.server.storageQuota = 1 << 30
	path := "/api/groups/" + itoa(w.b.ID)

	n := len(auditLog(t, w.st))
	s1, existing := w.sendJSON(w.creds["a"], "PATCH", path, map[string]any{"quotaBytes": 1000})
	s2, unknown := w.sendJSON(w.creds["a"], "PATCH", "/api/groups/999999", map[string]any{"quotaBytes": 1000})
	if s1 != http.StatusForbidden || s2 != http.StatusForbidden || !bytes.Equal(existing, unknown) {
		t.Fatalf("member: %d %s / %d %s", s1, existing, s2, unknown)
	}
	for _, body := range []any{
		map[string]any{"quotaBytes": (1 << 30) + 1},
		map[string]any{"quotaBytes": 0},
		map[string]any{"quotaBytes": -5},
		map[string]any{"quotaBytes": 1.5},
		map[string]any{"quotaBytes": "1GiB"},
		map[string]any{"quotaBytes": 1000, "name": "C"},
		map[string]any{},
		map[string]any{"quota": 1000},
	} {
		if status, out := w.sendJSON(w.creds["x"], "PATCH", path, body); status != http.StatusBadRequest {
			t.Errorf("%v: %d %s", body, status, out)
		}
	}
	if got := newEntries(t, w.st, n); len(got) != 0 {
		t.Fatalf("logged %+v", got)
	}
	if status, out := w.sendJSON(w.creds["x"], "PATCH", "/api/groups/999999", map[string]any{"quotaBytes": 1000}); status != http.StatusNotFound {
		t.Fatalf("unknown: %d %s", status, out)
	}

	quotaOf := func() (*int64, groupJSON) {
		t.Helper()
		var list struct{ Groups []groupJSON }
		w.getJSON(w.creds["x"], "/api/groups", http.StatusOK, &list)
		for _, g := range list.Groups {
			if g.ID == w.b.ID {
				return g.QuotaBytes, g
			}
		}
		t.Fatal("no group B")
		return nil, groupJSON{}
	}
	if q, _ := quotaOf(); q != nil {
		t.Fatalf("before: %v", *q)
	}
	if status, out := w.sendJSON(w.creds["x"], "PATCH", path, map[string]any{"quotaBytes": 1 << 30}); status != http.StatusNoContent {
		t.Fatalf("set: %d %s", status, out)
	}
	q, g := quotaOf()
	if q == nil || *q != 1<<30 || g.Bytes <= 0 || g.State != "ok" {
		t.Fatalf("after set: %+v", g)
	}
	entries := newEntries(t, w.st, n)
	if len(entries) != 1 || entries[0].Action != "group.quota" || detailMap(t, entries[0].Details)["group"] != "B" {
		t.Fatalf("entries %+v", entries)
	}
	if d := detailMap(t, entries[0].Details); d["from"] != nil || d["to"] != float64(1<<30) {
		t.Fatalf("details %v", d)
	}
	if col := w.groupColumn("group.quota"); col != itoa(w.b.ID) {
		t.Fatalf("group column %s", col)
	}
	// null removes it.
	if status, out := w.sendJSON(w.creds["x"], "PATCH", path, map[string]any{"quotaBytes": nil}); status != http.StatusNoContent {
		t.Fatalf("remove: %d %s", status, out)
	}
	if q, _ := quotaOf(); q != nil {
		t.Fatalf("after remove: %d", *q)
	}
	// Without an instance quota, any positive quota.
	w.server.storageQuota = 0
	if status, out := w.sendJSON(w.creds["x"], "PATCH", path, map[string]any{"quotaBytes": int64(1) << 50}); status != http.StatusNoContent {
		t.Fatalf("no instance quota: %d %s", status, out)
	}
	// The rename still works, with name alone.
	if status, out := w.sendJSON(w.creds["x"], "PATCH", path, map[string]any{"name": "Kunde B"}); status != http.StatusNoContent {
		t.Fatalf("rename: %d %s", status, out)
	}
	if _, g := quotaOf(); g.Name != "Kunde B" || g.QuotaBytes == nil {
		t.Fatalf("after rename: %+v", g)
	}
}

// queryRow scans one row of query into dst.
func (w *groupWorld) queryRow(dst any, query string, args ...any) {
	w.t.Helper()
	conn, err := pgx.Connect(context.Background(), w.dbURL)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if err := conn.QueryRow(context.Background(), query, args...).Scan(dst); err != nil {
		w.t.Fatal(err)
	}
}
