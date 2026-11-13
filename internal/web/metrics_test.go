package web

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

// metricsGet requests /api/metrics with key (or f's session when key is
// empty) and returns the status and the body.
func (f *authFixture) metricsGet(key string) (int, string, http.Header) {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/metrics", nil)
	client := f.client
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

// TestMetricsRouteAccess (#397, part 3): an admin's session and an
// admin's metrics key read /api/metrics; the metrics key opens no other
// route; read and sync keys, members and a demoted owner get 403; a
// locked owner's key is refused like every key of a locked user (401);
// invalid keys count in the limiter.
func TestMetricsRouteAccess(t *testing.T) {
	f, _ := newKeyFixture(t)
	f.addUser("admin", keyPassword, true)
	f.addUser("helper", keyPassword, true)
	f.addUser("kim", keyPassword, false)
	f.loginOK("admin", keyPassword)
	metrics := f.createKey("scrape", []string{"metrics"}, f.code(f.adminSecrets["admin"]))
	adminKeys := map[string]string{
		"read": f.createKey("r", []string{"read"}, f.code(f.adminSecrets["admin"])),
		"sync": f.createKey("s", []string{"sync"}, f.code(f.adminSecrets["admin"])),
		"both": f.createKey("b", []string{"read", "sync"}, f.code(f.adminSecrets["admin"])),
	}

	status, body, h := f.metricsGet("")
	if status != 200 || h.Get("Content-Type") != metricsContentType || h.Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, "# TYPE mail_archive_storage_bytes gauge\n") {
		t.Fatalf("admin session: %d %v\n%s", status, h, body)
	}
	if status, body, _ := f.metricsGet(metrics); status != 200 || !strings.Contains(body, `mail_archive_users{kind="admin",state="active"} 2`) {
		t.Fatalf("metrics key: %d\n%s", status, body)
	}
	for kind, key := range adminKeys {
		if status, _, _ := f.metricsGet(key); status != 403 {
			t.Errorf("an admin's %s key: %d, want 403", kind, status)
		}
	}

	// The metrics key reaches nothing else.
	params := strings.NewReplacer("{sha}", strings.Repeat("0", 64), "{name}", "nothing", "{id}", "999999", "{n}", "1", "{user}", "nobody")
	for _, rt := range (&Server{}).routes() {
		if rt.ui || rt.access == accessPublic || rt.pattern == "GET /api/metrics" {
			continue
		}
		method, path, ok := strings.Cut(rt.pattern, " ")
		if !ok {
			method, path = "GET", rt.pattern+"nope"
		}
		if got := f.keyStatus(metrics, method, params.Replace(path)); got != 403 {
			t.Errorf("%s with the metrics key: %d, want 403", rt.pattern, got)
		}
	}

	// A member's session and keys.
	k := f.as()
	k.loginOK("kim", keyPassword)
	if status, _, _ := k.metricsGet(""); status != 403 {
		t.Errorf("member's session: %d", status)
	}
	kimRead := k.createKey("r", []string{"read"}, "")
	if status, _, _ := k.metricsGet(kimRead); status != 403 {
		t.Errorf("member's read key: %d", status)
	}

	// The owner's role is read on every request.
	f.exec("UPDATE users SET is_admin = false WHERE name = 'admin'")
	if status, body, _ := f.metricsGet(metrics); status != 403 || !strings.Contains(body, adminOnlyMetrics) {
		t.Errorf("demoted owner's key: %d %s", status, body)
	}
	if status, _, _ := f.metricsGet(""); status != 403 {
		t.Errorf("demoted admin's session: %d", status)
	}
	f.exec("UPDATE users SET is_admin = true WHERE name = 'admin'")
	if status, _, _ := f.metricsGet(metrics); status != 200 {
		t.Errorf("admin again: %d", status)
	}
	// Invalid keys on the metrics route count in the limiter.
	bad := "ma_" + strings.Repeat("c", 52)
	for i := range badAPIKeyLimit {
		if s, _, _ := f.metricsGet(bad); s != 401 {
			t.Fatalf("invalid key %d: %d", i+1, s)
		}
	}
	if s, _, _ := f.metricsGet(bad); s != 429 {
		t.Fatalf("over the invalid-key rate: %d", s)
	}
}

// TestMetricsKeyLockedOwner: a locked owner's metrics key is refused like
// every key of a locked user (401), and works again once unlocked.
func TestMetricsKeyLockedOwner(t *testing.T) {
	f, _ := newKeyFixture(t)
	f.addUser("admin", keyPassword, true)
	f.loginOK("admin", keyPassword)
	metrics := f.createKey("scrape", []string{"metrics"}, f.code(f.adminSecrets["admin"]))
	f.exec("UPDATE users SET locked_at = now() WHERE name = 'admin'")
	if status, _, _ := f.metricsGet(metrics); status != 401 {
		t.Errorf("locked owner's key: %d", status)
	}
	f.exec("UPDATE users SET locked_at = NULL WHERE name = 'admin'")
	if status, _, _ := f.metricsGet(metrics); status != 200 {
		t.Errorf("unlocked owner's key: %d", status)
	}
}

// TestMetricsKeyCreation: only admins create a metrics key, and only
// alone in its key; the database check refuses the rest too (store test).
func TestMetricsKeyCreation(t *testing.T) {
	f, _ := newKeyFixture(t)
	f.addUser("admin", keyPassword, true)
	f.addUser("kim", keyPassword, false)
	k := f.as()
	k.loginOK("kim", keyPassword)
	n := len(auditLog(t, f.st))
	out := k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "m", "scopes": []string{"metrics"}, "currentPassword": keyPassword}, 403)
	if !strings.Contains(toJSON(out), "only admins") {
		t.Errorf("member: %v", out)
	}
	// Refused before the password is checked: nothing is recorded.
	k.expect("POST", "/api/profile/api-keys", map[string]any{"name": "m", "scopes": []string{"metrics"}, "currentPassword": "wrong wrong wrong"}, 403)
	if entries := newEntries(t, f.st, n); len(entries) != 0 {
		t.Errorf("entries for a refused metrics key: %+v", entries)
	}

	f.loginOK("admin", keyPassword)
	for _, scopes := range [][]string{{"metrics", "read"}, {"sync", "metrics"}} {
		out := f.expect("POST", "/api/profile/api-keys", map[string]any{"name": "m", "scopes": scopes, "currentPassword": keyPassword,
			"code": f.code(f.adminSecrets["admin"])}, 400)
		if !strings.Contains(toJSON(out), "only scope") {
			t.Errorf("%v: %v", scopes, out)
		}
	}
	f.createKey("m", []string{"metrics"}, f.code(f.adminSecrets["admin"]))
	list := toJSON(f.expect("GET", "/api/profile/api-keys", nil, 200))
	if !strings.Contains(list, `"scopes":["metrics"]`) {
		t.Errorf("list %s", list)
	}
	r := f.lastEntry("api_key.create")
	if d := details(t, r); toJSON(d["scopes"]) != `["metrics"]` {
		t.Errorf("audit entry %v", d)
	}
}

// metricsMarkers are put into every name, host, folder, subject, address
// and error text the database holds.
var metricsMarkers = []string{
	"zqgroupzq", "zqdefaultzq", "zqadminzq", "zqmemberzq", "zqaccountzq", "zqhost.example", "zqimapuserzq",
	"zqfolderzq", "zqsubjectzq", "zqfrom@zq.example", "zqto@zq.example", "zqsmtpzq", "zqsmtp@zq.example", "zqerrorzq",
}

// TestMetricsLeak: with markers in every name, host, IMAP user name,
// folder, subject, address and error text, none appears in /api/metrics,
// while the figures are there by ID.
func TestMetricsLeak(t *testing.T) {
	f, _ := newKeyFixture(t)
	ctx := context.Background()
	f.exec("UPDATE customer_groups SET name = 'zqdefaultzq'")
	admin := f.addUser("zqadminzq", keyPassword, true)
	g, err := f.st.CreateGroup(ctx, "zqgroupzq")
	if err != nil {
		t.Fatal(err)
	}
	h, err := cheapHasher.Hash(ctx, keyPassword)
	if err != nil {
		t.Fatal(err)
	}
	member, err := f.st.CreateUserInGroup(ctx, store.NewUser{Name: "zqmemberzq", PasswordHash: h, GroupID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "zqaccountzq", Host: "zqhost.example", Port: 993, TLSMode: store.TLSModeTLS, Username: "zqimapuserzq",
		PasswordEnc: []byte{1}, Enabled: true, OwnerID: &member.ID}
	if err := f.st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	folder, err := f.st.GetOrCreateFolder(ctx, acc.ID, "zqfolderzq")
	if err != nil {
		t.Fatal(err)
	}
	meta := store.MessageMeta{SHA256: strings.Repeat("e", 64), Size: 1234, StoredPath: "x", Subject: "zqsubjectzq", From: "zqfrom@zq.example",
		IndexData: store.IndexData{To: "zqto@zq.example"}}
	if _, err := f.st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: folder.ID, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		run, err := f.st.StartSyncRun(ctx, acc.ID)
		if err != nil {
			t.Fatal(err)
		}
		run.Status, run.Error, run.Health = "failed", "zqerrorzq", store.HealthFailure
		if err := f.st.FinishSyncRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	smtpAcc := &store.Account{Name: "zqsmtpzq", Enabled: true, OwnerID: &admin.ID}
	if err := f.st.CreateSMTPAccount(ctx, smtpAcc, []string{"zqsmtp@zq.example"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RefreshUsage(ctx); err != nil {
		t.Fatal(err)
	}
	f.exec("UPDATE customer_groups SET quota_bytes = 4096 WHERE id = $1", g.ID)
	f.exec("INSERT INTO storage_states (group_id, state, event_seq) VALUES ($1, 'warn', 1)", g.ID)
	f.server.diskUsage = func() (uint64, uint64, error) { return 1 << 30, 4 << 30, nil }
	f.server.version = "1.6.0"
	f.server.storageQuota = 1 << 20

	f.loginOK("zqadminzq", keyPassword)
	key := f.createKey("scrape", []string{"metrics"}, f.code(f.adminSecrets["zqadminzq"]))
	status, body, _ := f.metricsGet(key)
	if status != 200 {
		t.Fatalf("status %d: %s", status, body)
	}
	for _, m := range metricsMarkers {
		if strings.Contains(body, m) {
			t.Errorf("metrics contain %q", m)
		}
	}
	gid, aid := itoa(g.ID), itoa(acc.ID)
	for _, want := range []string{
		`mail_archive_build_info{version="1.6.0"} 1`,
		`mail_archive_storage_bytes{scope="instance"} 1234`,
		`mail_archive_storage_messages{scope="group",group_id="` + gid + `"} 1`,
		`mail_archive_storage_bytes{scope="group",group_id="` + gid + `"} 1234`,
		`mail_archive_storage_quota_bytes{scope="instance"} 1.048576e+06`,
		`mail_archive_storage_quota_bytes{scope="group",group_id="` + gid + `"} 4096`,
		`mail_archive_storage_state{scope="group",group_id="` + gid + `",state="warn"} 1`,
		`mail_archive_storage_state{scope="instance",state="ok"} 1`,
		`mail_archive_account_bytes{account_id="` + aid + `",group_id="` + gid + `",kind="imap"} 1234`,
		`mail_archive_account_health{account_id="` + aid + `",health="failing"} 1`,
		`mail_archive_disk_free_bytes 1.073741824e+09`,
		`mail_archive_users{kind="member",state="active"} 1`,
		`mail_archive_encryption_state{state="off"} 1`,
		`mail_archive_migrations_pending 0`,
		`mail_archive_smtp_storage_deferrals_total 0`,
		`mail_archive_database_relation_bytes{table="messages"} `,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	// Exclusive bytes are computed across groups: never in the metrics.
	if strings.Contains(body, "exclusive") {
		t.Error("metrics contain exclusive bytes")
	}
}

// TestMetricsCache: the answer is kept for 15 seconds.
func TestMetricsCache(t *testing.T) {
	f, _ := newKeyFixture(t)
	f.addUser("admin", keyPassword, true)
	f.loginOK("admin", keyPassword)
	key := f.createKey("scrape", []string{"metrics"}, f.code(f.adminSecrets["admin"]))
	_, first, _ := f.metricsGet(key)
	f.addUser("kim", keyPassword, false)
	if _, again, _ := f.metricsGet(key); again != first {
		t.Fatal("answer changed within the cache time")
	}
	f.clock.Advance(metricsTTL)
	if _, later, _ := f.metricsGet(key); !strings.Contains(later, `mail_archive_users{kind="member",state="active"} 1`) {
		t.Fatalf("answer after the cache time:\n%s", later)
	}
}

// TestMetricsLabelsChecked: a label value that does not match its label's
// pattern drops the sample, so a name that reached the model would still
// not be written.
func TestMetricsLabelsChecked(t *testing.T) {
	gid := int64(3)
	in := &metricsInput{
		Version: "zq name", Encryption: "off",
		DB: &store.InstanceMetrics{
			State: "zq state", RelationBytes: map[string]int64{"messages": 1, `zq "table"`: 2},
			Groups:   []store.GroupMetrics{{ID: 3, State: "ok"}},
			Accounts: []store.AccountMetrics{{ID: 9, GroupID: &gid, Kind: "zq kind"}},
			Users:    []store.UserCount{{Role: "zq role", N: 1}},
			Jobs:     []store.JobStat{{Kind: "zq job"}, {Kind: "attachment_text", Queued: 4}},
		},
	}
	out := string(writeMetrics(in))
	if strings.Contains(out, "zq") {
		t.Fatalf("a bad label value was written:\n%s", out)
	}
	for _, want := range []string{`mail_archive_database_relation_bytes{table="messages"} 1`, `mail_archive_jobs{job_kind="attachment_text",state="queued"} 4`,
		`mail_archive_account_health{account_id="9",health="ok"} 1`} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %s", want)
		}
	}
	// Every sample line uses known labels only.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexByte(line, '{'); i >= 0 {
			for _, pair := range strings.Split(line[i+1:strings.IndexByte(line, '}')], ",") {
				name, _, _ := strings.Cut(pair, "=")
				if _, ok := metricLabels[name]; !ok {
					t.Errorf("unknown label %q in %s", name, line)
				}
			}
		}
	}
}

// metricsStringFields are the string fields the metrics model may have:
// each becomes a label checked against its pattern. Every other field is
// a number, a boolean, a time, or a container of those.
var metricsStringFields = map[string]string{
	"metricsInput.Version": "version", "metricsInput.Encryption": "state", "metricsInput.Health": "health",
	"InstanceMetrics.State": "state", "GroupMetrics.State": "state", "AccountMetrics.Kind": "kind",
	"UserCount.Role": "kind", "JobStat.Kind": "job_kind", "InstanceMetrics.RelationBytes": "table",
}

// TestMetricsModel walks the metrics model by reflection: only numbers,
// booleans, times and the allowlisted enum fields, so a field that could
// hold a name, host, address, folder, subject or error text fails here.
func TestMetricsModel(t *testing.T) {
	timeType := reflect.TypeFor[time.Time]()
	durationType := reflect.TypeFor[time.Duration]()
	var walk func(owner, field string, typ reflect.Type)
	seen := map[string]bool{}
	walk = func(owner, field string, typ reflect.Type) {
		path := owner + "." + field
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		switch {
		case typ == timeType, typ == durationType:
			return
		case typ.Kind() == reflect.Struct:
			for i := range typ.NumField() {
				f := typ.Field(i)
				walk(typ.Name(), f.Name, f.Type)
			}
			return
		case typ.Kind() == reflect.Map:
			if typ.Key().Kind() == reflect.String {
				if _, ok := metricsStringFields[path]; !ok {
					t.Errorf("%s: a map keyed by string, not allowlisted", path)
				}
				seen[path] = true
			}
			walk(owner, field+"[]", typ.Elem())
			return
		case typ.Kind() == reflect.String:
			base := strings.TrimSuffix(path, "[]")
			if _, ok := metricsStringFields[base]; !ok {
				t.Errorf("%s: a string field, not allowlisted", path)
			}
			seen[base] = true
			return
		}
		switch typ.Kind() {
		case reflect.Bool, reflect.Int, reflect.Int32, reflect.Int64, reflect.Uint64, reflect.Float64:
		default:
			t.Errorf("%s: kind %s", path, typ.Kind())
		}
	}
	walk("", "", reflect.TypeFor[metricsInput]())
	for f := range metricsStringFields {
		if !seen[f] {
			t.Errorf("allowlisted %s is not in the model", f)
		}
	}
	for _, l := range metricsStringFields {
		if _, ok := metricLabels[l]; !ok {
			t.Errorf("label %s has no pattern", l)
		}
	}
}
