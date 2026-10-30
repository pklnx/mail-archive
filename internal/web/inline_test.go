package web

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/mime"
	"github.com/pklnx/mail-archive/internal/store"
)

// inlinePart is one leaf part of a test message built by cidMessage.
type inlinePart struct {
	contentType, contentID string
	body                   string // raw bytes; sent base64-encoded
}

// cidMessage builds a multipart/related message with the HTML body and the
// parts after it (part indexes 1, 2, ...).
func cidMessage(html string, parts ...inlinePart) string {
	var b strings.Builder
	b.WriteString("Message-ID: <cid-test@x>\r\nFrom: a@example.com\r\nSubject: cid\r\n" +
		"Date: Fri, 9 Oct 2026 10:00:00 +0000\r\nContent-Type: multipart/related; boundary=b\r\n\r\n")
	b.WriteString("--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + html + "\r\n")
	for _, p := range parts {
		fmt.Fprintf(&b, "--b\r\nContent-Type: %s\r\nContent-ID: <%s>\r\nContent-Disposition: inline\r\n"+
			"Content-Transfer-Encoding: base64\r\n\r\n%s\r\n", p.contentType, p.contentID, base64.StdEncoding.EncodeToString([]byte(p.body)))
	}
	b.WriteString("--b--\r\n")
	return b.String()
}

func dataURL(contentType, body string) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString([]byte(body))
}

// htmlBody fetches /html of the named message and returns the body after
// the <base> line, and the message.html audit entry it wrote.
func (f *apiFixture) htmlBody(name string) (string, store.AuditRecord) {
	f.t.Helper()
	n := len(auditLog(f.t, f.st))
	resp := f.get("/api/messages/" + f.ids[name] + "/html")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("html of %s: %d %s", name, resp.StatusCode, body)
	}
	got := newEntries(f.t, f.st, n)
	if len(got) != 1 || got[0].Action != "message.html" {
		f.t.Fatalf("html of %s: audit %+v", name, got)
	}
	rest, ok := strings.CutPrefix(string(body), htmlPreamble)
	if !ok {
		f.t.Fatalf("html of %s: no <base>: %s", name, body)
	}
	return strings.TrimSuffix(rest, "\r\n"), got[0]
}

func TestInlineCIDImages(t *testing.T) {
	const (
		png  = "\x89PNG\r\n\x1a\n-a"
		gif  = "GIF89a-prefix"
		jpeg = "\xff\xd8\xff-pct"
		webp = "RIFF-webp-first"
	)
	html := `<p><img src="cid:a@x"> <img src="CID:a@x"> <img src='cid:a@xy'> <img src="cid:pct%40x">` +
		` <img src="cid:svg@x"> <img src="cid:html@x"> <img src="cid:nope@x"> <img src="cid:dup@x">` +
		` <img src="cid:%zz@x"> acid:keep and cid:a@x<br></p>`
	f := newAPIFixture(t)
	f.addMessage("alice", "INBOX", "cid", cidMessage(html,
		inlinePart{"image/png", "a@x", png},
		inlinePart{"image/gif", "a@xy", gif},
		inlinePart{"image/jpeg", "pct@x", jpeg},
		inlinePart{"image/svg+xml", "svg@x", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`},
		inlinePart{"text/html", "html@x", "<script>alert(2)</script>"},
		// Duplicate Content-ID: the first part wins.
		inlinePart{"image/webp", "dup@x", webp},
		inlinePart{"image/png", "dup@x", "second"},
		// Not valid percent-encoding: looked up as written.
		inlinePart{"image/png", "%zz@x", png},
	), 10)

	body, e := f.htmlBody("cid")
	want := `<p><img src="` + dataURL("image/png", png) + `"> <img src="` + dataURL("image/png", png) + `">` +
		` <img src='` + dataURL("image/gif", gif) + `'> <img src="` + dataURL("image/jpeg", jpeg) + `">` +
		` <img src="data:,"> <img src="data:,"> <img src="data:,"> <img src="` + dataURL("image/webp", webp) + `">` +
		` <img src="` + dataURL("image/png", png) + `"> acid:keep and ` + dataURL("image/png", png) + `<br></p>`
	if body != want {
		t.Errorf("body\n got %s\nwant %s", body, want)
	}
	for _, bad := range []string{"svg", "script", "cid:", "parts/"} {
		if strings.Contains(strings.ReplaceAll(body, "acid:keep", ""), bad) {
			t.Errorf("body contains %q", bad)
		}
	}
	if d := details(t, e); fmt.Sprint(d) != "map[inline_images:7 inline_omitted:3]" {
		t.Errorf("audit details %v", d)
	}
}

// The replacement holds only [A-Za-z0-9+/=:;,]: whatever the reference
// held, the markup around it stays byte for byte as the mail wrote it.
func TestInlineCIDInjection(t *testing.T) {
	const png = "\x89PNG-inject"
	img := dataURL("image/png", png)
	cases := []struct{ in, want string }{
		{`<img src=cid:a@x onerror=x>`, `<img src=` + img + ` onerror=x>`},
		{`<img src="cid:a@x&quot; onload=x">`, `<img src="data:, onload=x">`},
		{`<div style="background:url(cid:a@x)">`, `<div style="background:url(` + img + `)">`},
		{`<img src='cid:a@x'><script>x</script>`, `<img src='` + img + `'><script>x</script>`},
		{`<img src="cid:a@x"onload="x">`, `<img src="` + img + `"onload="x">`},
		{`<img src="cid:"><img src="cid:a@x\"x>`, `<img src="cid:"><img src="data:,"x>`},
	}
	var html, want strings.Builder
	for _, c := range cases {
		html.WriteString(c.in + "\n")
		want.WriteString(c.want + "\n")
	}
	f := newAPIFixture(t)
	f.addMessage("alice", "INBOX", "inject", cidMessage(html.String(), inlinePart{"image/png", "a@x", png}), 10)
	body, _ := f.htmlBody("inject")
	if body != want.String() {
		t.Errorf("body\n got %q\nwant %q", body, want.String())
	}
}

func TestInlineCIDBudget(t *testing.T) {
	const a, b = "first-image", "second-image-is-longer"
	f := newAPIFixture(t)
	f.addMessage("alice", "INBOX", "budget", cidMessage(
		`<img src="cid:a@x"><img src="cid:b@x"><img src="cid:a@x"><img src="cid:b@x">`,
		inlinePart{"image/png", "a@x", a}, inlinePart{"image/png", "b@x", b}), 10)

	// Room for a twice: b does not fit after the first a, the second a does.
	old := maxInlineBytes
	t.Cleanup(func() { maxInlineBytes = old })
	maxInlineBytes = int64(len(dataURL("image/png", a)) + len(dataURL("image/png", b)) - 1)

	body, e := f.htmlBody("budget")
	want := `<img src="` + dataURL("image/png", a) + `"><img src="data:,"><img src="` + dataURL("image/png", a) +
		`"><img src="data:,">`
	if body != want {
		t.Errorf("body\n got %s\nwant %s", body, want)
	}
	if d := details(t, e); fmt.Sprint(d) != "map[inline_images:2 inline_omitted:2]" {
		t.Errorf("audit details %v", d)
	}

	// Nothing fits: every reference is empty, nothing is read.
	maxInlineBytes = 10
	body, e = f.htmlBody("budget")
	if body != strings.Repeat(`<img src="data:,">`, 4) || fmt.Sprint(details(t, e)) != "map[inline_images:0 inline_omitted:4]" {
		t.Errorf("no budget: %s %v", body, details(t, e))
	}
}

// A part that cannot be decoded is shown as an empty image; the rest of the
// mail and its other images are shown, and the audit entry counts it.
func TestInlineCIDReadError(t *testing.T) {
	const good = "good-image"
	raw := cidMessage(`<img src="cid:bad@x"><img src="cid:good@x">`,
		inlinePart{"image/png", "bad@x", "will be corrupted"}, inlinePart{"image/png", "good@x", good})
	raw = strings.Replace(raw, base64.StdEncoding.EncodeToString([]byte("will be corrupted")), "d2lsbCBi!!!*corrupt", 1)
	f := newAPIFixture(t)
	f.addMessage("alice", "INBOX", "broken", raw, 10)
	body, e := f.htmlBody("broken")
	if want := `<img src="data:,"><img src="` + dataURL("image/png", good) + `">`; body != want {
		t.Errorf("body\n got %s\nwant %s", body, want)
	}
	if d := details(t, e); fmt.Sprint(d) != "map[inline_images:1 inline_omitted:1]" {
		t.Errorf("audit details %v", d)
	}
}

// The audit entry is written before any of the body: without it, no image
// bytes leave the server.
func TestInlineCIDAuditFailClosed(t *testing.T) {
	f := newAPIFixture(t)
	breakAudit(t, f.dbURL)
	resp := f.get("/api/messages/" + f.ids["rich"] + "/html")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(body), "base64") ||
		strings.Contains(string(body), "Logo") {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
}

// The request the frame makes for an image on the app's own origin (after
// "Load remote images", img-src https: matches it): same-origin, no-cors,
// with the SameSite=Strict session cookie, since the frame shares the app's
// origin (#203). The Fetch Metadata guard refuses it before the handler, so
// it writes no audit entry, and the parts endpoint is not opened up: no
// CORP or CORS relaxation.
func TestPartRequestFromFrameStaysBlocked(t *testing.T) {
	f := newAPIFixture(t)
	n := len(auditLog(t, f.st))
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/messages/"+f.ids["rich"]+"/parts/1", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Dest", "image")
	resp, err := http.DefaultClient.Do(req) // f.srv adds the session cookie
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		t.Errorf("status %d, type %q: %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if got := newEntries(t, f.st, n); len(got) != 0 {
		t.Errorf("audit entries %+v", got)
	}

	// The same request as a fetch of the app passes: the cookie is valid.
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("fetch: status %d", resp2.StatusCode)
	}
}

// countingReadCloser counts the bytes read through it.
type countingReadCloser struct {
	r io.Reader
	n *int64
}

func (c countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

func (countingReadCloser) Close() error { return nil }

// The budget readInline passes is exactly what the walk counts down: once
// the last referenced image is read, a large attachment after it is not.
func TestInlineImagesStopsAfterLastImage(t *testing.T) {
	const png = "\x89PNG\r\n\x1a\n-logo"
	raw := cidMessage(`<p><img src="cid:logo@x"> <img src="cid:logo@x"></p>`, inlinePart{"image/png", "logo@x", png})
	end := strings.LastIndex(raw, "--b--")
	raw = raw[:end] + "--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=big.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("P", 1<<20))) +
		"\r\n" + raw[end:]
	m := mime.Parse(strings.NewReader(raw))

	var n int64
	opened := 0
	res, err := inlineImages(m.HTML, m.Parts, func() (io.ReadCloser, error) {
		opened++
		return countingReadCloser{strings.NewReader(raw), &n}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `<p><img src="` + dataURL("image/png", png) + `"> <img src="` + dataURL("image/png", png) + `"></p>`
	if res.HTML != want || res.Inlined != 2 || res.Omitted != 0 {
		t.Errorf("got %+v\nwant HTML %s", res, want)
	}
	if opened != 1 || n >= 64<<10 {
		t.Errorf("opened %d times, read %d bytes of a %d-byte message, want once and < %d", opened, n, len(raw), 64<<10)
	}
}
