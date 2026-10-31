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
To: Kunde <kunde@example.com>
Cc: =?UTF-8?Q?Buchhaltung_M=C3=BCller?= <buchhaltung@example.com>
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
	t      *testing.T
	st     *store.Store
	blobs  *blobstore.Store
	srv    *httptest.Server
	server *Server
	bare   http.Handler // the server's handler without the fixture's session cookie
	dbURL  string
	cookie string            // session token; empty means the fixture's own user
	ids    map[string]string // name -> sha256
	raw    map[string]string
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	st, dbURL := storetest.NewWithURL(t)
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := &apiFixture{t: t, st: st, blobs: blobs, dbURL: dbURL, ids: map[string]string{}, raw: map[string]string{
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
		// Like a first sync: the folder's UIDVALIDITY is set before its messages.
		if err := st.ResetFolder(ctx, folder.ID, 1); err != nil {
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
			meta, err := archive.BuildMeta(ctx, st, blobs, blob, true)
			if err != nil {
				t.Fatal(err)
			}
			metas = append(metas, meta)
			locs = append(locs, store.Location{FolderID: folder.ID, UIDValidity: 1, UID: uint32(i + 1), Flags: []string{`\Seen`}})
			f.ids[name] = blob.SHA256
		}
		if _, err := st.SaveBatch(ctx, folder.ID, uint32(len(l.msgs)), metas, locs); err != nil {
			t.Fatal(err)
		}
	}
	s := New(st, blobs, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"127.0.0.1"}})
	f.server = s
	f.bare = s.Handler()
	f.srv = httptest.NewServer(loggedIn(t, st, f.bare))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *apiFixture) get(path string) *http.Response {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
	if f.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: f.cookie})
	}
	resp, err := http.DefaultClient.Do(req)
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
		{"after=2026-10-06T00:00:00%2B02:00&before=2026-10-07T00:00:00%2B02:00", []string{"meeting"}}, // local midnight
		{"to=" + url.QueryEscape("Buchhaltung Müller"), []string{"invoice"}},                          // Cc
		{"to=KUNDE@example", []string{"invoice"}},
		{"to=alice@", []string{"rich"}},
		{"q=alice", []string{"meeting"}}, // free text finds the sender, not the recipient
		{"from=stadtwerke", []string{"invoice"}},
		{"from=alice&to=alice", []string{}},
		{"attachment=vertrag", []string{"rich"}},
		{"attachment=logo", []string{}}, // inline image referenced by cid:
		{"q=Vertrag.pdf", []string{}},   // attachment names only through attachment=
		{"has=attachment", []string{"rich"}},
		{"has=attachment&account=bob", []string{}},
		{"to=" + url.QueryEscape("%"), []string{}},
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

	f.getJSON("/api/messages", 200, &r)
	for _, m := range r.Messages {
		if m.HasAttachment != (f.name(m.ID) == "rich") {
			t.Errorf("%s: hasAttachment = %v", f.name(m.ID), m.HasAttachment)
		}
	}
}

// A row that matched by an attachment only names it as matchedAttachment,
// with the snippet from its text; other rows have no such key (#390,
// part 4).
func TestListMatchedAttachment(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	if ok, err := f.st.SaveAttachmentTexts(ctx, f.ids["rich"], []store.AttachmentText{
		{Part: 2, Filename: "Vertrag.pdf", Status: "ok", Text: "Wartungsvertrag mit Kündigungsfrist von drei Monaten"},
	}); !ok || err != nil {
		t.Fatalf("save texts: %v, %v", ok, err)
	}
	for _, group := range []string{"", "&group=1"} {
		var raw struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		f.getJSON("/api/messages?q=Kündigungsfrist"+group, 200, &raw)
		if len(raw.Messages) != 1 {
			t.Fatalf("group %q: %d rows", group, len(raw.Messages))
		}
		if got := string(raw.Messages[0]["matchedAttachment"]); got != `{"part":2,"filename":"Vertrag.pdf"}` {
			t.Errorf("group %q: matchedAttachment = %s", group, got)
		}
		var snippet string
		_ = json.Unmarshal(raw.Messages[0]["snippet"], &snippet)
		if !strings.Contains(snippet, store.HighlightStart+"Kündigungsfrist"+store.HighlightEnd) {
			t.Errorf("group %q: snippet %q", group, snippet)
		}
		// A body match: no key (a fresh value: Unmarshal would merge maps).
		raw.Messages = nil
		f.getJSON("/api/messages?q=Rechnungen"+group, 200, &raw)
		if len(raw.Messages) == 0 {
			t.Fatalf("group %q: no body match", group)
		}
		for _, m := range raw.Messages {
			if _, ok := m["matchedAttachment"]; ok {
				t.Errorf("group %q: body match with matchedAttachment: %s", group, m["matchedAttachment"])
			}
		}
	}
	// The message detail never has it.
	var d map[string]json.RawMessage
	f.getJSON("/api/messages/"+f.ids["rich"], 200, &d)
	if _, ok := d["matchedAttachment"]; ok {
		t.Errorf("detail with matchedAttachment")
	}
}

// The web UI sends after/before as the instant a local day starts, with the
// browser's offset (#404). A mail from the first local hours of a day east of
// UTC is in that day, not in the UTC day before.
func TestSearchLocalDayBounds(t *testing.T) {
	f := newAPIFixture(t)
	f.addMessage("alice", "INBOX", "late", crlf(`Message-ID: <late@x>
From: Carol <carol@example.com>
Subject: Shortly after midnight in Berlin
Date: Tue, 6 Oct 2026 22:30:00 +0000
Content-Type: text/plain; charset=utf-8

Gute Nacht.
`), 10)
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"after=2026-10-07T00:00:00%2B02:00&before=2026-10-08T00:00:00%2B02:00", []string{"newsletter", "late"}},
		{"after=2026-10-07&before=2026-10-08", []string{"newsletter"}}, // UTC day
		{"before=2026-10-07T00:00:00%2B02:00&after=2026-10-06T00:00:00%2B02:00", []string{"meeting"}},
		{"before=2026-10-07&after=2026-10-06", []string{"late", "meeting"}},
	} {
		if got := f.list(c.query); !equal(got, c.want) {
			t.Errorf("list(%q) = %v, want %v", c.query, got, c.want)
		}
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
		"/api/messages?limit=0":                                         400,
		"/api/messages?limit=1000":                                      400,
		"/api/messages?after=yesterday":                                 400,
		"/api/messages?after=2026-10-08&before=2026-10-07":              400,
		"/api/messages?after=2026-10-07&before=2026-10-07":              200, // empty range
		"/api/messages?after=2026-10-07T00:00:00%2B02:00":               200, // offset, '+' encoded
		"/api/messages?after=2026-10-07T00:00:00+02:00":                 400, // a raw '+' is a space
		"/api/messages?has=pdf":                                         400,
		"/api/messages?to=" + strings.Repeat("a", 201):                  400,
		"/api/messages?from=" + strings.Repeat("a", 201):                400,
		"/api/messages?attachment=" + strings.Repeat("a", 201):          400,
		"/api/messages?to=" + url.QueryEscape(strings.Repeat("ä", 200)): 200, // characters, not bytes
		"/api/messages?cursor=garbage":                                  400,
		"/api/messages/not-a-sha":                                       400,
		"/api/messages/" + strings.Repeat("0", 64):                      404,
		"/api/messages/" + f.ids["rich"] + "/parts/x":                   400,
		"/api/messages/" + f.ids["rich"] + "/parts/9":                   404,
		"/api/messages/" + f.ids["invoice"] + "/html":                   404, // plain text only
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
	if len(m.Locations) != 2 || m.Locations[0].Account != "alice" || m.Locations[1].Account != "bob" ||
		m.Locations[0].Superseded || m.Locations[1].Superseded {
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
	// Exactly: no scripts (also explicitly), no forms, no remote content; the
	// sandbox allows the same origin (the app measures the frame) and popups
	// (links), never scripts.
	const want = "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data:" +
		"; font-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'self'" +
		"; sandbox allow-same-origin allow-popups allow-popups-to-escape-sandbox"
	if csp != want {
		t.Errorf("CSP\n got %q\nwant %q", csp, want)
	}
	// Remote images are blocked: no bare scheme sources like "https:".
	if strings.Contains(csp, "allow-scripts") || bareScheme.MatchString(csp) {
		t.Errorf("CSP too permissive: %q", csp)
	}
	if got := cspDirective(csp, "sandbox"); got != "allow-same-origin allow-popups allow-popups-to-escape-sandbox" {
		t.Errorf("sandbox = %q", got)
	}
	// The preamble: links in a new tab, and the height reset; then the mail.
	// The cid: logo is embedded; the frame makes no request for it.
	if !bytes.HasPrefix(body, []byte(`<base target="_blank"><style>html,body{height:auto!important;min-height:0!important}</style>`+"\n")) ||
		!bytes.Contains(body, []byte(`<img src="data:image/png;base64,iVBORw0KGgo=">`)) ||
		bytes.Contains(body, []byte("parts/")) || bytes.Contains(body, []byte("cid:")) {
		t.Errorf("body = %s", body)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q", got)
	}

	resp = f.get("/api/messages/" + f.ids["rich"] + "/html?images=1")
	csp = resp.Header.Get("Content-Security-Policy")
	if want := strings.Replace(want, "img-src data:", "img-src data: https: http:", 1); csp != want {
		t.Errorf("images=1 CSP\n got %q\nwant %q", csp, want)
	}
	if strings.Contains(csp, "allow-scripts") || cspDirective(csp, "script-src") != "'none'" {
		t.Errorf("images=1 CSP = %q", csp)
	}
}

// A mail with a doctype gets the preamble after it, so it renders in
// standards mode (#206); the CSP and the sandbox are the same as without.
func TestMessageHTMLDoctype(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	raw := crlf(`Message-ID: <doctype@x>
From: Bob <bob@example.com>
To: carol@example.com
Subject: With doctype
Date: Thu, 8 Oct 2026 10:00:00 +0000
Content-Type: text/html; charset=utf-8

<!DOCTYPE html>
<html><body><p>Hi</p></body></html>
`)
	accs, err := f.st.ListAccountsByName(ctx, "alice")
	if err != nil || len(accs) != 1 {
		t.Fatalf("alice: %v %v", accs, err)
	}
	folder, err := f.st.GetOrCreateFolder(ctx, accs[0].ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blob, _, err := f.blobs.Put(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := archive.BuildMeta(ctx, f.st, f.blobs, blob, true)
	if err != nil {
		t.Fatal(err)
	}
	loc := store.Location{FolderID: folder.ID, UIDValidity: 1, UID: 10, Flags: []string{`\Seen`}}
	if _, err := f.st.SaveBatch(ctx, folder.ID, 10, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		t.Fatal(err)
	}

	resp := f.get("/api/messages/" + blob.SHA256 + "/html")
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "<!DOCTYPE html>"+htmlPreamble) || !strings.Contains(string(body), "<html><body><p>Hi</p>") {
		t.Errorf("body = %q", body)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if got := cspDirective(csp, "sandbox"); got != "allow-same-origin allow-popups allow-popups-to-escape-sandbox" {
		t.Errorf("sandbox = %q", got)
	}
	if got := cspDirective(csp, "base-uri"); got != "'none'" {
		t.Errorf("base-uri = %q", got)
	}
}

// Declarative shadow roots and srcdoc frames are neutralized in the served
// mail (#209): their attributes get the prefix "data-". The cid: image inside
// the template is still inlined, and the preamble still goes after the
// doctype; every other byte is the mail's.
func TestMessageHTMLNeutralized(t *testing.T) {
	const png = "\x89PNG\r\n\x1a\n-n"
	mail := `<!DOCTYPE html>` + "\n" + `<html><body><div id="host"><template shadowrootmode="closed">` +
		`<img src="cid:a@x"><a href="/api/openapi.json" target="_blank">in a closed root</a></template></div>` +
		`<iframe SrcDoc="&lt;a href=/api/openapi.json target=_blank&gt;in a srcdoc frame&lt;/a&gt;"></iframe></body></html>`
	f := newAPIFixture(t)
	f.addMessage("alice", "INBOX", "neutralized", cidMessage(mail, inlinePart{"image/png", "a@x", png}), 10)

	resp := f.get("/api/messages/" + f.ids["neutralized"] + "/html")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	want := `<!DOCTYPE html>` + htmlPreamble + "\n" + `<html><body><div id="host"><template data-shadowrootmode="closed">` +
		`<img src="` + dataURL("image/png", png) + `"><a data-href="/api/openapi.json" target="_blank">in a closed root</a></template></div>` +
		`<iframe data-SrcDoc="&lt;a href=/api/openapi.json target=_blank&gt;in a srcdoc frame&lt;/a&gt;"></iframe></body></html>`
	if string(body) != want {
		t.Errorf("body\n got %q\nwant %q", body, want)
	}
	if s := neutralizeSurvivors(string(body)); len(s) > 0 {
		t.Errorf("served body keeps %v", s)
	}
}

// Inlining replaces a cid: URL together with the bytes after it up to a
// quote, white space, ")", "<" or ">", "=" and "/" included. Before the
// rename, that could turn an attribute value into attribute names: in
// <template cid:q= shadowrootmode=closed>, "shadowrootmode=closed" is a
// value until "cid:q=" becomes "data:,". So the rename runs on the served
// bytes, after inlining (#209). Unknown references become "data:,"; the
// known ones end without base64 padding, one with "/" in the payload.
func TestMessageHTMLNeutralizedAfterInlining(t *testing.T) {
	const (
		png = "\x89PNG\r\n\x1a\n\xff" // iVBORw0KGgr/
		gif = "GIF89a\xff\xff\xff"    // R0lGODlh////
	)
	mail := `<div><template cid:q= shadowrootmode=closed><a href="/api/openapi.json">u</a></template></div>` +
		`<iframe cid:q= srcdoc=&lt;a&#32;href=/api/openapi.json&#32;target=_blank&gt;v&lt;/a&gt;></iframe>` +
		`<div><template cid:a@x= shadowrootmode=closed>w</template></div>` +
		`<iframe cid:b@x= srcdoc=&lt;p&gt;y&lt;/p&gt;></iframe>`
	f := newAPIFixture(t)
	f.addMessage("alice", "INBOX", "after-inlining", cidMessage(mail,
		inlinePart{"image/png", "a@x=", png}, inlinePart{"image/gif", "b@x=", gif}), 10)

	body, _ := f.htmlBody("after-inlining")
	want := `<div><template data:, data-shadowrootmode=closed><a data-href="/api/openapi.json">u</a></template></div>` +
		`<iframe data:, data-srcdoc=&lt;a&#32;href=/api/openapi.json&#32;target=_blank&gt;v&lt;/a&gt;></iframe>` +
		`<div><template ` + dataURL("image/png", png) + ` data-shadowrootmode=closed>w</template></div>` +
		`<iframe ` + dataURL("image/gif", gif) + ` data-srcdoc=&lt;p&gt;y&lt;/p&gt;></iframe>`
	if body != want {
		t.Errorf("body\n got %q\nwant %q", body, want)
	}
	// The parse oracle on what the handler serves.
	if s := neutralizeSurvivors(htmlPreamble + body); len(s) > 0 {
		t.Errorf("served body keeps %v", s)
	}
}

// linkServer is the fixture's server through the real route table, with
// archive.example as a further allowed host (a public URL's host, as from
// MAIL_ARCHIVE_PUBLIC_URL) next to 127.0.0.1.
func (f *apiFixture) linkServer() *httptest.Server {
	f.t.Helper()
	f.server.allowedHosts = append(f.server.allowedHosts, "archive.example")
	return f.srv
}

// rawGet sends a GET with this Host header and the Fetch Metadata of a frame
// navigation, as the browser does for the mail frame, and returns the
// response with its body.
func rawGet(t *testing.T, srv *httptest.Server, host, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Host = host
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

// linkMail is an HTML mail with links into the app (relative, by the
// request's host and port, by an allowed host) and an external one, for
// TestMessageHTMLLinks and TestMessageHTMLRewriteLeavesStoredBytes.
func linkMail(port string) string {
	return `<p><a href="/api/openapi.json?link=root">a</a> <a href="api/x">b</a> ` +
		`<a href="http://127.0.0.1:` + port + `/api/openapi.json?link=port">c</a> ` +
		`<a href="https://archive.example/api/openapi.json?link=public">d</a> ` +
		`<a href="https://links.example/">e</a></p>`
}

// The served HTML has no link into the app (#459): relative links, links
// to the request's own host and port and links to an allowed host are
// renamed; the external link is kept. The same for either Host the server
// accepts.
func TestMessageHTMLLinks(t *testing.T) {
	f := newAPIFixture(t)
	srv := f.linkServer()
	u, _ := url.Parse(srv.URL)
	f.addMessage("alice", "INBOX", "links", "Message-ID: <links@x>\r\nFrom: a@example.com\r\nSubject: links\r\n"+
		"Date: Fri, 9 Oct 2026 10:00:00 +0000\r\nContent-Type: text/html; charset=utf-8\r\n\r\n"+linkMail(u.Port())+"\r\n", 10)
	want := htmlPreamble + `<p><a data-href="/api/openapi.json?link=root">a</a> <a data-href="api/x">b</a> ` +
		`<a data-href="http://127.0.0.1:` + u.Port() + `/api/openapi.json?link=port">c</a> ` +
		`<a data-href="https://archive.example/api/openapi.json?link=public">d</a> ` +
		`<a href="https://links.example/">e</a></p>`
	for _, host := range []string{u.Host, "archive.example"} {
		resp, body := rawGet(t, srv, host, "/api/messages/"+f.ids["links"]+"/html")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Host %s: status %d: %s", host, resp.StatusCode, body)
		}
		if body = strings.TrimSuffix(body, "\r\n"); body != want {
			t.Errorf("Host %s: body\n got %q\nwant %q", host, body, want)
		}
	}
	// Another host is refused before anything is served.
	if resp, _ := rawGet(t, srv, "evil.example", "/api/messages/"+f.ids["links"]+"/html"); resp.StatusCode == http.StatusOK {
		t.Errorf("Host evil.example: status %d", resp.StatusCode)
	}
}

// Only the rendering changes (#459): /raw, the stored blob and a part keep
// the archived bytes, links and all, while /html is rewritten.
func TestMessageHTMLRewriteLeavesStoredBytes(t *testing.T) {
	f := newAPIFixture(t)
	srv := f.linkServer()
	u, _ := url.Parse(srv.URL)
	mail := linkMail(u.Port())
	attachment := `<html><body>` + mail + `</body></html>`
	raw := "Message-ID: <links-stored@x>\r\nFrom: a@example.com\r\nSubject: links\r\n" +
		"Date: Fri, 9 Oct 2026 10:00:00 +0000\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + mail + "\r\n" +
		"--b\r\nContent-Type: text/html; charset=utf-8\r\nContent-Disposition: attachment; filename=\"links.html\"\r\n\r\n" +
		attachment + "\r\n--b--\r\n"
	f.addMessage("alice", "INBOX", "stored", raw, 10)
	sha := f.ids["stored"]

	resp, body := rawGet(t, srv, u.Host, "/api/messages/"+sha+"/html")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<a data-href="/api/openapi.json?link=root">`) {
		t.Fatalf("html: %d %q", resp.StatusCode, body)
	}
	if resp, body = rawGet(t, srv, u.Host, "/api/messages/"+sha+"/raw"); resp.StatusCode != http.StatusOK || body != raw {
		t.Errorf("raw: %d %q", resp.StatusCode, body)
	}
	// The stored path is the blob store's for the hash (archive.BuildMeta).
	rc, err := f.blobs.Open(blobstore.RelPath(sha))
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(stored) != raw {
		t.Errorf("stored blob %q", stored)
	}
	if resp, body = rawGet(t, srv, u.Host, "/api/messages/"+sha+"/parts/1"); resp.StatusCode != http.StatusOK || body != attachment {
		t.Errorf("part 1: %d %q", resp.StatusCode, body)
	}
}

// cspDirective returns the sources of one directive of a CSP header.
func cspDirective(csp, name string) string {
	for _, d := range strings.Split(csp, ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(d), name+" "); ok {
			return v
		}
	}
	return ""
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
