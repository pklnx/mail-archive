package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/mime"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

var (
	msgInvoice = crlf(`Message-ID: <invoice@x>
From: Stadtwerke <rechnung@stadtwerke.example>
Subject: =?UTF-8?Q?Rechnung_f=C3=BCr_Oktober?=
Date: Mon, 5 Oct 2026 10:00:00 +0000
Content-Type: text/plain; charset=utf-8

Guten Tag, Ihre Rechnung liegt bei. Bitte zahlen Sie bis Ende des Monats.
`)
	msgMeeting = crlf(`Message-ID: <meeting@x>
From: Alice <alice@example.com>
Subject: Team sync
Date: Tue, 6 Oct 2026 10:00:00 +0000
Content-Type: text/plain; charset=utf-8

We are meeting tomorrow to discuss the roadmap.
`)
	msgNewsletter = crlf(`Message-ID: <news@x>
From: News <news@shop.example>
Subject: Newsletter 2026/Q3
Date: Wed, 7 Oct 2026 10:00:00 +0000
Content-Type: text/plain; charset=utf-8

Angebote der Woche.
`)
	msgRich = crlf(`Message-ID: <rich@x>
From: Bob <bob@example.com>
To: alice@example.com
Subject: Fotos und Vertrag
Date: Thu, 8 Oct 2026 10:00:00 +0000
Content-Type: multipart/mixed; boundary="outer"

--outer
Content-Type: multipart/related; boundary="inner"

--inner
Content-Type: text/html; charset=utf-8

<p>Hier das Logo: <img src="cid:logo@x"> und ein <img src="https://tracker.example/p.gif"></p>
--inner
Content-Type: image/png
Content-ID: <logo@x>
Content-Disposition: inline
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--inner--
--outer
Content-Type: text/html; name="evil.html"
Content-Disposition: attachment; filename="evil.html"

<script>alert(1)</script>
--outer
Content-Type: application/pdf; name="Vertrag.pdf"
Content-Disposition: attachment; filename="Vertrag.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjQK
--outer--
`)
)

type apiFixture struct {
	t   *testing.T
	srv *httptest.Server
	ids map[string]string // name -> sha256
	raw map[string]string
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	st := storetest.New(t)
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := &apiFixture{t: t, ids: map[string]string{}, raw: map[string]string{
		"invoice": msgInvoice, "meeting": msgMeeting, "newsletter": msgNewsletter, "rich": msgRich,
	}}

	// alice/INBOX: invoice, meeting, rich; bob/INBOX: invoice; bob/Archive: newsletter
	layout := []struct {
		account, folder string
		msgs            []string
	}{
		{"alice", "INBOX", []string{"invoice", "meeting", "rich"}},
		{"bob", "INBOX", []string{"invoice"}},
		{"bob", "Archive", []string{"newsletter"}},
	}
	accountIDs := map[string]int64{}
	for _, l := range layout {
		id, ok := accountIDs[l.account]
		if !ok {
			acc := &store.Account{Name: l.account, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true}
			if err := st.CreateAccount(ctx, acc); err != nil {
				t.Fatal(err)
			}
			id = acc.ID
			accountIDs[l.account] = id
		}
		folder, err := st.GetOrCreateFolder(ctx, id, l.folder)
		if err != nil {
			t.Fatal(err)
		}
		var metas []store.MessageMeta
		var locs []store.Location
		for i, name := range l.msgs {
			raw := f.raw[name]
			blob, _, err := blobs.Put(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			h := archive.ParseHeaders(strings.NewReader(raw))
			metas = append(metas, store.MessageMeta{
				SHA256: blob.SHA256, Size: blob.Size, StoredPath: blob.Path,
				MessageID: h.MessageID, Subject: h.Subject, From: h.From, SentAt: h.Date,
				BodyText: mime.IndexText(strings.NewReader(raw)),
			})
			locs = append(locs, store.Location{FolderID: folder.ID, UIDValidity: 1, UID: uint32(i + 1), Flags: []string{`\Seen`}})
			f.ids[name] = blob.SHA256
		}
		if _, err := st.SaveBatch(ctx, folder.ID, uint32(len(l.msgs)), metas, locs); err != nil {
			t.Fatal(err)
		}
	}
	s := New(st, blobs, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"127.0.0.1"}})
	f.srv = httptest.NewServer(s.Handler())
	t.Cleanup(f.srv.Close)
	return f
}

func (f *apiFixture) get(path string) *http.Response {
	f.t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (f *apiFixture) getJSON(path string, want int, v any) {
	f.t.Helper()
	resp := f.get(path)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		f.t.Fatalf("GET %s: status %d, want %d: %s", path, resp.StatusCode, want, body)
	}
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			f.t.Fatalf("GET %s: %v: %s", path, err, body)
		}
	}
}

type listResp struct {
	Messages []summaryJSON `json:"messages"`
	Next     *string       `json:"nextCursor"`
}

func (f *apiFixture) list(query string) []string {
	f.t.Helper()
	var r listResp
	f.getJSON("/api/messages?"+query, 200, &r)
	names := make([]string, 0, len(r.Messages))
	for _, m := range r.Messages {
		names = append(names, f.name(m.ID))
	}
	return names
}

func (f *apiFixture) name(sha string) string {
	for n, id := range f.ids {
		if id == sha {
			return n
		}
	}
	return sha
}

func equal(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func TestListAndSearch(t *testing.T) {
	f := newAPIFixture(t)
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"rich", "newsletter", "meeting", "invoice"}},
		{"q=" + url.QueryEscape("Rechnungen"), []string{"invoice"}},       // German stemming
		{"q=meetings", []string{"meeting"}},                               // English stemming
		{"q=sletter", []string{"newsletter"}},                             // substring via trigram/ILIKE
		{"q=" + url.QueryEscape("roadmap -nothing"), []string{"meeting"}}, // websearch syntax
		{"q=" + url.QueryEscape("100%"), []string{}},                      // LIKE wildcards are literal
		{"account=bob", []string{"newsletter", "invoice"}},
		{"account=bob&folder=Archive", []string{"newsletter"}},
		{"account=nobody", []string{}},
		{"after=2026-10-06&before=2026-10-08", []string{"newsletter", "meeting"}},
	}
	for _, c := range cases {
		if got := f.list(c.query); !equal(got, c.want) {
			t.Errorf("list(%q) = %v, want %v", c.query, got, c.want)
		}
	}

	var r listResp
	f.getJSON("/api/messages?q=Rechnungen", 200, &r)
	if !strings.Contains(r.Messages[0].Snippet, "Rechnung") {
		t.Errorf("snippet without highlight: %q", r.Messages[0].Snippet)
	}
	if r.Messages[0].Subject != "Rechnung für Oktober" {
		t.Errorf("subject = %q", r.Messages[0].Subject)
	}
}

func TestPagination(t *testing.T) {
	f := newAPIFixture(t)
	var all []string
	cursor := ""
	for page := 0; page < 10; page++ {
		var r listResp
		f.getJSON("/api/messages?limit=1"+cursor, 200, &r)
		for _, m := range r.Messages {
			all = append(all, f.name(m.ID))
		}
		if r.Next == nil {
			break
		}
		cursor = "&cursor=" + *r.Next
	}
	if want := []string{"rich", "newsletter", "meeting", "invoice"}; !equal(all, want) {
		t.Errorf("pages = %v, want %v", all, want)
	}
}

func TestBadRequests(t *testing.T) {
	f := newAPIFixture(t)
	for path, want := range map[string]int{
		"/api/messages?limit=0":                       400,
		"/api/messages?limit=1000":                    400,
		"/api/messages?after=yesterday":               400,
		"/api/messages?cursor=garbage":                400,
		"/api/messages/not-a-sha":                     400,
		"/api/messages/" + strings.Repeat("0", 64):    404,
		"/api/messages/" + f.ids["rich"] + "/parts/x": 400,
		"/api/messages/" + f.ids["rich"] + "/parts/9": 404,
		"/api/messages/" + f.ids["invoice"] + "/html": 404, // plain text only
	} {
		if resp := f.get(path); resp.StatusCode != want {
			t.Errorf("GET %s: status %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestMessageDetail(t *testing.T) {
	f := newAPIFixture(t)
	var m messageJSON
	f.getJSON("/api/messages/"+f.ids["invoice"], 200, &m)
	if len(m.Locations) != 2 || m.Locations[0].Account != "alice" || m.Locations[1].Account != "bob" {
		t.Errorf("locations = %+v", m.Locations)
	}
	if !strings.Contains(m.Text, "Ihre Rechnung liegt bei") || m.HasHTML || m.MessageID != "invoice@x" {
		t.Errorf("detail = %+v", m)
	}

	f.getJSON("/api/messages/"+f.ids["rich"], 200, &m)
	if !m.HasHTML || !strings.Contains(m.Text, "Hier das Logo") || len(m.Parts) != 4 {
		t.Fatalf("rich detail = %+v", m)
	}
	if m.Parts[3].Filename != "Vertrag.pdf" || !m.Parts[3].Attachment {
		t.Errorf("parts = %+v", m.Parts)
	}
}

var bareScheme = regexp.MustCompile(`(^|\s)https?:(\s|;|$)`)

func TestMessageHTML(t *testing.T) {
	f := newAPIFixture(t)
	resp := f.get("/api/messages/" + f.ids["rich"] + "/html")
	body, _ := io.ReadAll(resp.Body)
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "sandbox", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	// Remote images are blocked: no bare scheme sources like "https:".
	if strings.Contains(csp, "allow-scripts") || bareScheme.MatchString(csp) {
		t.Errorf("CSP too permissive: %q", csp)
	}
	if !bytes.Contains(body, []byte(`src="parts/1"`)) || !bytes.HasPrefix(body, []byte(`<base target="_blank">`)) {
		t.Errorf("body = %s", body)
	}

	resp = f.get("/api/messages/" + f.ids["rich"] + "/html?images=1")
	if csp := resp.Header.Get("Content-Security-Policy"); !bareScheme.MatchString(csp) {
		t.Errorf("images=1 CSP = %q", csp)
	}
}

func TestRawAndParts(t *testing.T) {
	f := newAPIFixture(t)
	resp := f.get("/api/messages/" + f.ids["meeting"] + "/raw")
	body, _ := io.ReadAll(resp.Body)
	if string(body) != msgMeeting || resp.Header.Get("Content-Type") != "message/rfc822" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("raw: %v %q", resp.Header, body)
	}

	check := func(n int, wantType, wantDisp string, wantBody []byte) {
		t.Helper()
		resp := f.get(fmt.Sprintf("/api/messages/%s/parts/%d", f.ids["rich"], n))
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != wantType ||
			!strings.HasPrefix(resp.Header.Get("Content-Disposition"), wantDisp) ||
			(wantBody != nil && !bytes.Equal(body, wantBody)) {
			t.Errorf("part %d: %d %v %q", n, resp.StatusCode, resp.Header, body)
		}
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("part %d without sandbox CSP", n)
		}
	}
	check(1, "image/png", "inline", nil)
	check(2, "application/octet-stream", "attachment", []byte("<script>alert(1)</script>")) // HTML never inline
	check(3, "application/octet-stream", "attachment", []byte("%PDF-1.4\n"))
}

func TestAccountsAndStatus(t *testing.T) {
	f := newAPIFixture(t)
	var a accountsResponse
	f.getJSON("/api/accounts", 200, &a)
	if len(a.Accounts) != 2 || a.Accounts[1].Name != "bob" || len(a.Accounts[1].Folders) != 2 {
		t.Fatalf("accounts = %+v", a)
	}
	var s struct {
		Unique int64 `json:"uniqueMessages"`
	}
	f.getJSON("/api/status", 200, &s)
	if s.Unique != 4 {
		t.Errorf("unique = %d", s.Unique)
	}
}
