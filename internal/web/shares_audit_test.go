package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

// auditWorld is the seed of the shared-read audit tests (#389, test 10 and
// R2). a shares a/info and c shares c/sales with b, who owns b/mail:
//
//	b/mail INBOX:  S1-S3 "secret", newest; AB "secret", also in a/info
//	a/info INBOX:  A2 "secret", AB
//	a/info Board:  A1 "secret", AC
//	c/sales Board: AC
//	a/info INBOX:  AT, a reply to BT in b/mail INBOX
//
// With limit=3, b's q=secret has S3, S2, S1 on page 1 and A1, A2, AB on
// page 2: AB is dated onto page 2, so "not counted" is not vacuous.
func newAuditWorld(t *testing.T) *shareWorld {
	t.Helper()
	w := newShareWorld(t)
	for _, n := range []string{"a", "b", "c", "u"} {
		w.user(n, false)
	}
	w.user("x", true)
	w.account("a", "info")
	w.account("c", "sales")
	w.account("b", "mail")
	for i, d := range []string{"20", "21", "22"} {
		w.put("b/mail", "INBOX", fmt.Sprintf("S%d", i+1), mail(fmt.Sprintf("s%d", i), "Own "+d, "Tue, "+d+" Oct 2026 10:00:00 +0000", "a secret of b"))
	}
	w.put("a/info", "Board", "A1", mail("a1", "Board minutes", "Mon, 12 Oct 2026 10:00:00 +0000", "the secret plan"))
	w.put("a/info", "INBOX", "A2", mail("a2", "Offer", "Sun, 11 Oct 2026 10:00:00 +0000", "secret offer"))
	ab := mail("ab", "Both", "Sat, 10 Oct 2026 10:00:00 +0000", "a secret both have")
	w.put("a/info", "INBOX", "AB", ab)
	w.put("b/mail", "INBOX", "AB", ab)
	ac := mail("ac", "Quarterly", "Thu, 1 Oct 2026 10:00:00 +0000", "numbers")
	w.put("a/info", "Board", "AC", ac)
	w.put("c/sales", "Board", "AC", ac)
	w.put("b/mail", "Threads", "BT", mail("bt", "Question", "Mon, 7 Sep 2026 10:00:00 +0000", "asking"))
	w.put("a/info", "Threads", "AT", reply("at@x", "bt@x", "Re: Question", "Tue, 8 Sep 2026 10:00:00 +0000"))
	w.share("a/info", "b")
	w.share("c/sales", "b")
	return w
}

// sharedEntries returns the viaShare entries of c's Log.
func (w *shareWorld) sharedEntries(c credential) []auditEntryJSON {
	w.t.Helper()
	all, _ := w.log(c, "limit=500")
	return slices.DeleteFunc(all, func(e auditEntryJSON) bool { return !e.ViaShare })
}

// lastEntry returns the newest stored entry and fails unless exactly one
// was written since n.
func (w *shareWorld) lastEntry(n int, what string) store.AuditRecord {
	w.t.Helper()
	got := newEntries(w.t, w.st, n)
	if len(got) != 1 {
		w.t.Fatalf("%s: %d entries: %+v", what, len(got), got)
	}
	return got[0]
}

// Test 10, extended (#389, revision 2 R1, decision 13): every page of an
// unnamed search or listing that shows share-only messages is in the
// owner's Log, with the count per account and without b's terms; named
// first pages keep their terms; named cursor pages stay with b.
func TestSharedReadAudit(t *testing.T) {
	w := newAuditWorld(t)
	a, b := w.users["a"], w.creds["b"]

	t.Run("unnamed pages", func(t *testing.T) {
		for _, c := range []struct{ name, query, action1, action2 string }{
			{"search", "q=secret&limit=3", "message.search", "message.page"},
			{"grouped search", "group=1&q=secret&limit=3", "message.search", "message.page"},
			{"listing", "limit=3", "message.list", "message.page"},
		} {
			before := len(w.sharedEntries(w.creds["a"]))
			n := len(auditLog(t, w.st))
			page1, next := w.list(b, c.query)
			if !slices.Equal(page1, []string{"S3", "S2", "S1"}) {
				t.Fatalf("%s: page 1 %v", c.name, page1)
			}
			e := w.lastEntry(n, c.name+" page 1")
			if e.Action != c.action1 || len(viaOwners(t, e)) != 0 {
				t.Errorf("%s: page 1 entry %s %v", c.name, e.Action, details(t, e))
			}
			if got := w.sharedEntries(w.creds["a"]); len(got) != before {
				t.Errorf("%s: page 1 is in a's Log: %+v", c.name, got)
			}
			n = len(auditLog(t, w.st))
			page2, _ := w.list(b, c.query+"&cursor="+url.QueryEscape(next))
			if !slices.Equal(page2, []string{"A1", "A2", "AB"}) {
				t.Fatalf("%s: page 2 %v", c.name, page2)
			}
			e = w.lastEntry(n, c.name+" page 2")
			d := details(t, e)
			if e.Action != c.action2 || !slices.Equal(viaOwners(t, e), []int64{a.ID}) || !slices.Equal(viaKeys(d), []string{"a/info:2"}) {
				t.Errorf("%s: page 2 entry %s %v", c.name, e.Action, d)
			}
			if c.action1 == "message.search" && d["q"] != "secret" {
				t.Errorf("%s: b's entry lost the terms: %v", c.name, d)
			}
			entries, body := w.log(w.creds["a"], "limit=500")
			shared := slices.DeleteFunc(entries, func(e auditEntryJSON) bool { return !e.ViaShare })
			if len(shared) != before+1 {
				t.Fatalf("%s: a's Log has %d shared entries, want %d", c.name, len(shared), before+1)
			}
			got := shared[0]
			gd := detailMap(t, got.Details)
			wantSearch := c.action1 == "message.search"
			if got.Action != "message.page" || got.User != "b" || got.ClientIP != "" || got.UserAgent != "" || got.APIKey != "" ||
				gd["search"] != wantSearch || !slices.Equal(viaKeys(gd), []string{"a/info:2"}) || len(gd) != 2 {
				t.Errorf("%s: a sees %+v %v", c.name, got, gd)
			}
			if strings.Contains(body, "secret") || strings.Contains(body, "cursor") || strings.Contains(body, "via_owners") {
				t.Errorf("%s: a's Log shows b's terms or cursor: %s", c.name, body)
			}
		}
	})

	t.Run("named", func(t *testing.T) {
		before := len(w.sharedEntries(w.creds["a"]))
		n := len(auditLog(t, w.st))
		page1, next := w.list(b, "account=a/info&q=secret&limit=1")
		if !slices.Equal(page1, []string{"A1"}) || next == "" {
			t.Fatalf("named page 1 %v", page1)
		}
		e := w.lastEntry(n, "named page 1")
		if e.Action != "message.search" || !slices.Equal(viaKeys(details(t, e)), []string{"a/info:1"}) {
			t.Errorf("named page 1: %s %v", e.Action, details(t, e))
		}
		got := w.sharedEntries(w.creds["a"])
		if len(got) != before+1 {
			t.Fatalf("named page 1 not in a's Log: %+v", got)
		}
		if gd := detailMap(t, got[0].Details); gd["q"] != "secret" || gd["account"] != "a/info" || gd["limit"] != nil {
			t.Errorf("a's view of the named search: %v", gd)
		}
		n = len(auditLog(t, w.st))
		w.list(b, "account=a/info&q=secret&limit=1&cursor="+url.QueryEscape(next))
		if e := w.lastEntry(n, "named page 2"); e.Action != "message.page" || len(viaOwners(t, e)) != 0 || details(t, e)["q"] != "secret" {
			t.Errorf("named page 2: %s %v", e.Action, details(t, e))
		}
		if got := w.sharedEntries(w.creds["a"]); len(got) != before+1 {
			t.Errorf("named page 2 is in a's Log: %+v", got[0])
		}
	})

	t.Run("thread", func(t *testing.T) {
		var at messageJSON
		w.fetchJSON(b, "/api/messages/"+w.ids["AT"], &at)
		before := len(w.sharedEntries(w.creds["a"]))
		q := "thread=" + url.QueryEscape(at.Thread) + "&limit=1"
		page1, next := w.list(b, q)
		page2, _ := w.list(b, q+"&cursor="+url.QueryEscape(next))
		if !slices.Equal(page1, []string{"AT"}) || !slices.Equal(page2, []string{"BT"}) {
			t.Fatalf("thread pages %v %v", page1, page2)
		}
		got := w.sharedEntries(w.creds["a"])
		// Page 1 shows AT, share-only; page 2 only b's own BT.
		if len(got) != before+1 || got[0].Action != "message.list" {
			t.Fatalf("thread entries in a's Log: %+v", got[:len(got)-before])
		}
		if gd := detailMap(t, got[0].Details); gd["thread"] != nil || !slices.Equal(viaKeys(gd), []string{"a/info:1"}) {
			t.Errorf("a's view of the thread: %v", gd)
		}
	})

	t.Run("conversation", func(t *testing.T) {
		before := len(w.sharedEntries(w.creds["a"]))
		// b's own message with a reply only in a/info.
		n := len(auditLog(t, w.st))
		w.fetchJSON(b, "/api/messages/"+w.ids["BT"]+"/conversation", &conversationResp{})
		e := w.lastEntry(n, "conversation")
		if e.Action != "message.conversation" || e.TargetID != w.ids["BT"] || details(t, e)["members"] != float64(2) ||
			!slices.Equal(viaKeys(details(t, e)), []string{"a/info:1"}) {
			t.Errorf("conversation entry %+v %v", e, details(t, e))
		}
		got := w.sharedEntries(w.creds["a"])
		if len(got) != before+1 || got[0].Action != "message.conversation" {
			t.Fatalf("conversation not in a's Log: %+v", got)
		}
		// a cannot open BT: only its hash would show, so no target.
		if got[0].Target != nil {
			t.Errorf("a sees the target of b's own message: %+v", got[0].Target)
		}
		// A single message, and members all in b's own accounts: b's Log
		// only.
		for _, name := range []string{"S1", "AB"} {
			n := len(auditLog(t, w.st))
			w.fetchJSON(b, "/api/messages/"+w.ids[name]+"/conversation", &conversationResp{})
			if e := w.lastEntry(n, name); e.Action != "message.conversation" || len(viaOwners(t, e)) != 0 {
				t.Errorf("conversation of %s: %v", name, details(t, e))
			}
		}
		if got := w.sharedEntries(w.creds["a"]); len(got) != before+1 {
			t.Errorf("a's Log: %+v", got[0])
		}
	})

	t.Run("own reads stay out", func(t *testing.T) {
		before := len(w.sharedEntries(w.creds["a"]))
		n := len(auditLog(t, w.st))
		// b over b's own account only: as before shares.
		w.list(b, "account=b/mail&q=secret")
		w.list(b, "account=mail")
		w.fetchJSON(b, "/api/messages/"+w.ids["S1"], &messageJSON{})
		// a reads a's own mail.
		w.list(w.creds["a"], "q=secret")
		w.fetchJSON(w.creds["a"], "/api/messages/"+w.ids["A1"], &messageJSON{})
		for _, e := range newEntries(t, w.st, n) {
			if len(viaOwners(t, e)) != 0 {
				t.Errorf("%s by %s: %v", e.Action, e.UserName, details(t, e))
			}
		}
		if got := w.sharedEntries(w.creds["a"]); len(got) != before {
			t.Errorf("a's Log: %+v", got[0])
		}
		if got := w.sharedEntries(w.creds["u"]); len(got) != 0 {
			t.Errorf("u's Log: %+v", got)
		}
	})
}

// R2: an owner's Log names only the owner's own accounts. AC is in a/info
// and c/sales, neither b's own.
func TestSharedReadAuditOwnersApart(t *testing.T) {
	w := newAuditWorld(t)
	b := w.creds["b-key"]
	w.fetchJSON(b, "/api/messages/"+w.ids["AC"], &messageJSON{})
	names, _ := w.list(b, "folder=Board")
	if !slices.Equal(names, []string{"A1", "AC"}) {
		t.Fatalf("Board %v", names)
	}
	for _, c := range []struct{ owner, key, other string }{{"a", "a/info", "sales"}, {"c", "c/sales", "info"}} {
		entries, body := w.log(w.creds[c.owner], "limit=500")
		shared := slices.DeleteFunc(entries, func(e auditEntryJSON) bool { return !e.ViaShare })
		if len(shared) != 2 {
			t.Fatalf("%s: %d shared entries: %s", c.owner, len(shared), body)
		}
		wantList := []string{c.key + ":2"}
		if c.owner == "c" {
			wantList = []string{c.key + ":1"}
		}
		list, open := detailMap(t, shared[0].Details), detailMap(t, shared[1].Details)
		if shared[0].Action != "message.list" || !slices.Equal(viaKeys(list), wantList) || list["search"] != false {
			t.Errorf("%s: list %v", c.owner, list)
		}
		if shared[1].Action != "message.open" || !slices.Equal(viaKeys(open), []string{c.key}) || shared[1].Target.Subject != "Quarterly" ||
			shared[1].APIKey != "" {
			t.Errorf("%s: open %+v %v", c.owner, shared[1], open)
		}
		otherOwner := map[string]string{"a": `"c"`, "c": `"a"`}[c.owner]
		if strings.Contains(body, "via_owners") || strings.Contains(body, c.other) || strings.Contains(body, `"owner":`+itoa(w.users[strings.Trim(otherOwner, `"`)].ID)) {
			t.Errorf("%s sees the other owner's share: %s", c.owner, body)
		}
	}
	// b's own Log and the admin view keep both.
	for _, c := range []credential{w.creds["b"], w.creds["x"]} {
		entries, _ := w.log(c, "limit=500")
		var open *auditEntryJSON
		for i := range entries {
			if entries[i].Action == "message.open" && entries[i].User == "b" {
				open = &entries[i]
			}
		}
		if open == nil || open.ViaShare {
			t.Fatalf("%s: no plain open entry", c.name)
		}
		d := detailMap(t, open.Details)
		if !slices.Equal(viaKeys(d), []string{"a/info", "c/sales"}) || d["via_owners"] == nil {
			t.Errorf("%s sees %v", c.name, d)
		}
		// Subjects come from the viewer's scope: b reads AC, x does not.
		if want := map[string]string{"b": "Quarterly", "x": ""}[c.name]; open.Target == nil || open.Target.Subject != want {
			t.Errorf("%s sees the target %+v", c.name, open.Target)
		}
	}
	// The reads stay in the owner's Log after the unshare; b's Log loses
	// the subject of a's mail.
	w.unshare("a/info", "b")
	w.unshare("c/sales", "b")
	if got := w.sharedEntries(w.creds["a"]); len(got) != 2 {
		t.Errorf("after the unshare a's Log has %+v", got)
	}
	entries, _ := w.log(w.creds["b"], "limit=500")
	for _, e := range entries {
		if e.Target != nil && e.Target.Subject != "" {
			t.Errorf("b still sees the subject %q", e.Target.Subject)
		}
	}
}

// The owner's Log pages through own entries and other users' reads
// merged, without gaps or repeats across cursors.
func TestSharedReadAuditPaging(t *testing.T) {
	w := newAuditWorld(t)
	for i := range 4 {
		w.fetchJSON(w.creds["a"], "/api/messages/"+w.ids["A1"], &messageJSON{})
		w.fetchJSON(w.creds["b"], "/api/messages/"+w.ids["A2"], &messageJSON{})
		if i%2 == 0 {
			w.fetchJSON(w.creds["b"], "/api/messages/"+w.ids["S1"], &messageJSON{}) // b's own: not a's
		}
	}
	all, _ := w.log(w.creds["a"], "limit=500")
	if len(all) != 8 {
		t.Fatalf("a's Log has %d entries", len(all))
	}
	var paged []int64
	cursor := ""
	for range 10 {
		var p auditPageJSON
		w.fetchJSON(w.creds["a"], "/api/audit?limit=3&cursor="+url.QueryEscape(cursor), &p)
		for _, e := range p.Entries {
			paged = append(paged, e.ID)
		}
		if cursor = p.NextCursor; cursor == "" {
			break
		}
	}
	var want []int64
	for _, e := range all {
		want = append(want, e.ID)
	}
	if !slices.Equal(paged, want) {
		t.Errorf("paged %v, want %v", paged, want)
	}
}

// Shared reads fail closed: when the share-only lookup or the audit write
// fails, lists, pages and conversations answer 503 and send no rows.
func TestSharedReadAuditFailClosed(t *testing.T) {
	w := newAuditWorld(t)
	b := w.creds["b"]
	_, next := w.list(b, "limit=3")
	paths := []string{
		"/api/messages?q=secret",
		"/api/messages?limit=3&cursor=" + url.QueryEscape(next),
		"/api/messages?account=a/info",
		"/api/messages/" + w.ids["BT"] + "/conversation",
	}
	check := func(stage string) {
		t.Helper()
		for _, p := range paths {
			status, body := w.fetch(b, p)
			if status != http.StatusServiceUnavailable || strings.Contains(string(body), w.ids["A1"]) || strings.Contains(string(body), "Question") {
				t.Errorf("%s: %s: %d %s", stage, p, status, body)
			}
		}
	}
	w.server.shareOnlyFails = func() error { return errors.New("lookup failed") }
	check("lookup fails")
	w.server.shareOnlyFails = nil
	breakAudit(t, w.dbURL)
	check("audit fails")
}
