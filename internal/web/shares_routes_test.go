package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

// msgShared is M of the route matrix: HTML with an attachment, so every
// message route has something to serve.
var msgShared = crlf(`Message-ID: <m@x>
From: Anna <anna@example.com>
To: team@example.com
Subject: Matrix
Date: Mon, 5 Oct 2026 10:00:00 +0000
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/html; charset=utf-8

<p>Shared mail</p>
--b
Content-Type: application/pdf; name="plan.pdf"
Content-Disposition: attachment; filename="plan.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjQK
--b--
`)

// Every read route under GET /api/messages, taken from the route table
// (#389, revision 1 "route matrix", revision 2 R4): with a in "a/info"
// shared with b, b reads M (and its reply M2, both only in a/info) with a
// session and with a read key, and each request writes an entry whose
// via_owners holds a. The only exception are named cursor pages (decision
// 14: the owner has the named first page). u, a user without a share, and
// x, an admin without one, get 404 or empty lists; after the unshare so
// does b. A new read route fails until it is classified here.
func TestSharedReadRouteMatrix(t *testing.T) {
	w := newShareWorld(t)
	a := w.user("a", false)
	w.user("b", false)
	w.user("u", false)
	w.user("x", true)
	w.account("a", "info")
	w.put("a/info", "INBOX", "M", msgShared)
	w.put("a/info", "INBOX", "M2", reply("m2@x", "m@x", "Re: Matrix", "Tue, 6 Oct 2026 10:00:00 +0000"))
	w.share("a/info", "b")
	b := w.creds["b"]

	var m messageJSON
	w.fetchJSON(b, "/api/messages/"+w.ids["M"], &m)
	if m.Thread == "" {
		t.Fatal("M has no thread key")
	}
	_, unnamedCursor := w.list(b, "limit=1")
	_, namedCursor := w.list(b, "account=a/info&limit=1")
	if unnamedCursor == "" || namedCursor == "" {
		t.Fatal("no second page")
	}
	type request struct {
		name, path string
		list       bool
		exception  string // why no via_owners entry is expected
	}
	sha := w.ids["M"]
	variants := map[string][]request{
		"GET /api/messages": {
			{name: "named first page", path: "/api/messages?account=a/info", list: true},
			{name: "unnamed first page", path: "/api/messages", list: true},
			{name: "unnamed cursor page", path: "/api/messages?limit=1&cursor=" + url.QueryEscape(unnamedCursor), list: true},
			{name: "unnamed thread", path: "/api/messages?thread=" + url.QueryEscape(m.Thread), list: true},
			{name: "named cursor page", path: "/api/messages?account=a/info&limit=1&cursor=" + url.QueryEscape(namedCursor), list: true,
				exception: "decision 14: the owner sees the named first page only"},
		},
		"GET /api/messages/{sha}":              {{name: "message", path: "/api/messages/" + sha}},
		"GET /api/messages/{sha}/conversation": {{name: "conversation", path: "/api/messages/" + sha + "/conversation"}},
		"GET /api/messages/{sha}/html":         {{name: "html", path: "/api/messages/" + sha + "/html"}},
		"GET /api/messages/{sha}/raw":          {{name: "raw", path: "/api/messages/" + sha + "/raw"}},
		"GET /api/messages/{sha}/parts/{n}":    {{name: "part", path: "/api/messages/" + sha + "/parts/1"}},
	}
	var requests []request
	for _, rt := range (&Server{}).routes() {
		if rt.access != accessRead || !strings.HasPrefix(rt.pattern, "GET /api/messages") {
			continue
		}
		v, ok := variants[rt.pattern]
		if !ok {
			t.Errorf("read route %s is not in the share route matrix", rt.pattern)
			continue
		}
		requests = append(requests, v...)
		delete(variants, rt.pattern)
	}
	for p := range variants {
		t.Errorf("the matrix lists %s, which is no read route", p)
	}

	// readable checks the answer of c: 200 with M (or M2) when shared,
	// otherwise 404 or an empty list.
	readable := func(stage string, c credential, rq request, shared bool) {
		t.Helper()
		status, body := w.fetch(c, rq.path)
		switch {
		case rq.list && shared:
			if status != http.StatusOK || !strings.Contains(string(body), sha) && !strings.Contains(string(body), w.ids["M2"]) {
				t.Errorf("%s: %s as %s: %d %s", stage, rq.name, c.name, status, body)
			}
		case rq.list:
			if status != http.StatusOK || !strings.Contains(string(body), `"messages":[]`) {
				t.Errorf("%s: %s as %s: %d %s", stage, rq.name, c.name, status, body)
			}
		case shared:
			if status != http.StatusOK {
				t.Errorf("%s: %s as %s: %d %s", stage, rq.name, c.name, status, body)
			}
		default:
			if status != http.StatusNotFound || strings.Contains(string(body), "Matrix") || strings.Contains(string(body), "JVBERi") {
				t.Errorf("%s: %s as %s: %d %s", stage, rq.name, c.name, status, body)
			}
		}
	}
	for _, c := range []credential{b, w.creds["b-key"]} {
		for _, rq := range requests {
			n := len(auditLog(t, w.st))
			readable("shared", c, rq, true)
			entries := newEntries(t, w.st, n)
			via := slices.ContainsFunc(entries, func(e store.AuditRecord) bool { return slices.Contains(viaOwners(t, e), a.ID) })
			switch {
			case rq.exception == "" && !via:
				t.Errorf("%s as %s: no entry with via_owners %d: %+v", rq.name, c.name, a.ID, entries)
			case rq.exception != "" && (via || len(entries) != 1 || entries[0].Action != "message.page"):
				t.Errorf("%s as %s (%s): %+v", rq.name, c.name, rq.exception, entries)
			}
		}
	}
	for _, name := range []string{"u", "u-key", "x"} {
		for _, rq := range requests {
			readable("not shared", w.creds[name], rq, false)
		}
	}
	w.unshare("a/info", "b")
	for _, c := range []credential{b, w.creds["b-key"]} {
		for _, rq := range requests {
			readable("unshared", c, rq, false)
		}
	}
}
