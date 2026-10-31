package attachtext

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/attachtext/pdftest"
	"github.com/pklnx/mail-archive/internal/mime"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// part is one MIME part of a test message.
type part struct {
	header string // Content-Type and further header lines
	body   []byte
	raw    bool // body is written as is, not base64
}

func message(parts ...part) string {
	var b strings.Builder
	b.WriteString("Subject: attachments\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n")
	for _, p := range parts {
		b.WriteString("--b\r\n" + p.header + "\r\n")
		if p.raw {
			b.WriteString("\r\n")
			b.Write(p.body)
			b.WriteString("\r\n")
			continue
		}
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString(p.body)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	b.WriteString("--b--\r\n")
	return b.String()
}

func attach(ctype, name string, body []byte) part {
	return part{header: fmt.Sprintf("Content-Type: %s\r\nContent-Disposition: attachment; filename=%q", ctype, name), body: body}
}

func extractMessage(t *testing.T, raw string) []Result {
	t.Helper()
	res, err := shared.Message(context.Background(), strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMessageTypes(t *testing.T) {
	raw := message(
		part{header: "Content-Type: text/plain", body: []byte("the body"), raw: true},
		attach("application/pdf", "rechnung.pdf", pdftest.Text("Rechnung PDF")),
		attach("application/octet-stream", "scan.PDF", pdftest.Text("octet stream PDF")),
		attach("application/octet-stream", "fake.pdf", []byte("not a pdf at all")),
		attach("application/pdf", "broken.pdf", []byte("no signature")),
		attach("image/jpeg", "photo.jpg", []byte("\xff\xd8%PDF-")),
		part{header: "Content-Type: image/png\r\nContent-ID: <img@x>\r\nContent-Disposition: inline; filename=\"i.png\"", body: []byte("png")},
		attach("text/csv; charset=iso-8859-1", "export.csv", []byte("Gr\xf6\xdfe;Preis")),
		attach("text/tab-separated-values", "t.tsv", []byte("a\tb")),
		attach("text/markdown", "notes.md", []byte("# Notiz")),
		attach("text/html", "page.html", []byte("<p>HTML <b>Anhang</b></p>")),
		attach("text/calendar", "invite.ics", []byte("BEGIN:VCALENDAR")),
	)
	got := extractMessage(t, raw)
	var lines []string
	for _, r := range got {
		lines = append(lines, fmt.Sprintf("%d %s %s %q", r.Part, r.Filename, r.Status, r.Text))
	}
	want := []string{
		`1 rechnung.pdf ok "Rechnung PDF"`,
		`2 scan.PDF ok "octet stream PDF"`,
		`4 broken.pdf failed ""`,
		`7 export.csv ok "Größe;Preis"`,
		`8 t.tsv ok "a\tb"`,
		`9 notes.md ok "# Notiz"`,
		`10 page.html ok "HTML Anhang"`,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	// The part indexes are those of mime.Parse (and the parts API).
	m := mime.Parse(strings.NewReader(raw))
	for _, r := range got {
		if m.Parts[r.Part].Filename != r.Filename {
			t.Errorf("part %d is %q in Parse", r.Part, m.Parts[r.Part].Filename)
		}
	}
}

func TestMessageLimits(t *testing.T) {
	raw := message(
		attach("text/plain", "big.txt", []byte(strings.Repeat("x", MaxTextSize+1))),
		attach("application/pdf", "big.pdf", pdftest.Large(MaxPDFSize+1, "big")),
		part{header: "Content-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"corrupt.pdf\"\r\nContent-Transfer-Encoding: base64", body: []byte("%%%% not base64 !!!!"), raw: true},
		attach("text/plain", "after.txt", []byte("still read")),
	)
	got := extractMessage(t, raw)
	var lines []string
	for _, r := range got {
		lines = append(lines, fmt.Sprintf("%s %s", r.Filename, r.Status))
	}
	if strings.Join(lines, ",") != "big.txt too_large,big.pdf too_large,corrupt.pdf failed,after.txt ok" {
		t.Errorf("got %s", strings.Join(lines, ","))
	}
}

func TestMessageAttachmentCount(t *testing.T) {
	var parts []part
	for i := range 25 {
		parts = append(parts, attach("text/plain", fmt.Sprintf("f%d.txt", i), []byte("text")))
	}
	if got := extractMessage(t, message(parts...)); len(got) != MaxAttachments || got[19].Filename != "f19.txt" {
		t.Errorf("%d results", len(got))
	}
}

func TestMessageTextLimit(t *testing.T) {
	var parts []part
	for i := range 6 {
		parts = append(parts, attach("text/plain", fmt.Sprintf("f%d.txt", i), []byte(strings.Repeat("y", 200<<10))))
	}
	got := extractMessage(t, message(parts...))
	total := 0
	for _, r := range got {
		total += len(r.Text)
	}
	last := got[len(got)-1]
	if total != MaxMessageText || len(got) != 6 || last.Status != StatusTruncated || got[0].Status != StatusOK {
		t.Errorf("%d results, %d bytes, last %s", len(got), total, last.Status)
	}
}

func TestMessageCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw := message(attach("text/plain", "a.txt", []byte("a")))
	if res, err := shared.Message(ctx, strings.NewReader(raw)); !errors.Is(err, context.Canceled) || res != nil {
		t.Errorf("got %v, %v", res, err)
	}
	// A cancel while a PDF runs (shutdown) is an error too, not a status.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	setHooks(t, pdfHooks{start: cancel})
	raw = message(attach("application/pdf", "a.pdf", pdftest.Text("a")))
	if res, err := shared.Message(ctx, strings.NewReader(raw)); !errors.Is(err, context.Canceled) || res != nil {
		t.Errorf("during a PDF: %v, %v", res, err)
	}
}

// Attachments that the message's deadline never reached get a row with
// StatusTimeout, so that the message is recorded as done (#416); size and
// decoding still decide where they can without reading.
func TestMessageDeadlineUnreached(t *testing.T) {
	ctx := newDeadlineCtx()
	setHooks(t, pdfHooks{start: ctx.expire})
	raw := message(
		attach("application/pdf", "first.pdf", pdftest.Text("first")),
		attach("text/plain", "later.txt", []byte("later")),
		attach("application/pdf", "second.pdf", pdftest.Text("second")),
		attach("application/octet-stream", "fake.pdf", []byte("not a pdf at all")),
		attach("text/plain", "big.txt", []byte(strings.Repeat("x", MaxTextSize+1))),
	)
	got, err := shared.Message(ctx, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, r := range got {
		lines = append(lines, fmt.Sprintf("%d %s %s %q", r.Part, r.Filename, r.Status, r.Text))
	}
	want := `0 first.pdf timeout "",1 later.txt timeout "",2 second.pdf timeout "",4 big.txt too_large ""`
	if strings.Join(lines, ",") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(lines, ","), want)
	}
}

// A module that cannot be compiled is reported once, as an error, and every
// PDF then fails with ErrUnavailable, also inside a message; text
// attachments are not affected.
func TestPDFCompileFailureCached(t *testing.T) {
	var logs bytes.Buffer
	e := New(slog.New(slog.NewTextHandler(&logs, nil)))
	calls := 0
	e.pdf.hooks.compile = func() error { calls++; return errors.New("injected: out of memory") }
	for range 3 {
		if _, err := e.PDF(context.Background(), pdftest.Text("a")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("PDF: %v", err)
		}
	}
	raw := message(attach("text/plain", "a.txt", []byte("a")), attach("application/pdf", "a.pdf", pdftest.Text("a")))
	if res, err := e.Message(context.Background(), strings.NewReader(raw)); !errors.Is(err, ErrUnavailable) || res != nil {
		t.Fatalf("Message: %v, %v", res, err)
	}
	if res, err := e.Message(context.Background(), strings.NewReader(message(attach("text/plain", "a.txt", []byte("a"))))); err != nil || len(res) != 1 {
		t.Fatalf("text only: %v, %v", res, err)
	}
	if calls != 1 {
		t.Errorf("compiled %d times", calls)
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 || strings.Count(logs.String(), "cannot compile") != 1 {
		t.Errorf("logged %d errors:\n%s", n, logs.String())
	}
}

// One PDF at a time in the process, also across Extractors (two job
// workers share one; reindex and the web server do not share a process):
// a second PDF waits while the first runs (maintainer decision 12, #390).
func TestPDFOneAtATimePerProcess(t *testing.T) {
	other := New(nil)
	t.Cleanup(func() { _ = other.Close() })
	var (
		inside, waited testwait.Signal
		mu             sync.Mutex
		running, most  int
	)
	enter := func() {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
	}
	leave := func() {
		mu.Lock()
		running--
		mu.Unlock()
	}
	release := make(chan struct{})
	firstIn := inside.C()
	setHooks(t, pdfHooks{start: func() {
		enter()
		inside.Notify()
		select {
		case <-release:
		case <-time.After(testwait.Limit(t)):
			t.Error("not released")
		}
	}})
	other.pdf.hooks.start = func() { enter() }
	waitC := waited.C()
	onPDFWait = waited.Notify
	t.Cleanup(func() { onPDFWait = nil })

	done := make(chan Result, 2)
	go func() {
		res, _ := shared.PDF(context.Background(), pdftest.Text("first"))
		leave()
		done <- res
	}()
	select {
	case <-firstIn:
	case <-time.After(testwait.Limit(t)):
		t.Fatal("the first PDF did not start")
	}
	secondIn := make(chan struct{})
	other.pdf.hooks.start = func() { enter(); close(secondIn) }
	go func() {
		res, _ := other.PDF(context.Background(), pdftest.Text("second"))
		leave()
		done <- res
	}()
	select {
	case <-waitC:
	case <-secondIn:
		t.Fatal("the second PDF ran while the first was open")
	case <-time.After(testwait.Limit(t)):
		t.Fatal("the second PDF neither waited nor ran")
	}
	close(release)
	for range 2 {
		if res := <-done; res.Status != StatusOK {
			t.Errorf("status %s", res.Status)
		}
	}
	if most != 1 {
		t.Errorf("%d PDFs at once", most)
	}
}

// A PDF that waits for another one in the process until its caller's
// deadline passes was never read: Message returns ErrBusy (to be tried
// again), not a final StatusTimeout, and the next try reads it (#438
// review). Waiting does not count against the message's own time.
func TestMessageWaitForPDFIsNotATimeout(t *testing.T) {
	other := New(nil)
	t.Cleanup(func() { _ = other.Close() })
	var inside testwait.Signal
	firstIn := inside.C()
	release := make(chan struct{})
	setHooks(t, pdfHooks{start: func() {
		inside.Notify()
		select {
		case <-release:
		case <-time.After(testwait.Limit(t)):
			t.Error("not released")
		}
	}})
	firstDone := make(chan Result, 1)
	go func() {
		res, _ := shared.PDF(context.Background(), pdftest.Text("first"))
		firstDone <- res
	}()
	select {
	case <-firstIn:
	case <-time.After(testwait.Limit(t)):
		t.Fatal("the first PDF did not start")
	}
	// The second message's deadline passes while it waits for its turn.
	ctx := newDeadlineCtx()
	onPDFWait = ctx.expire
	t.Cleanup(func() { onPDFWait = nil })
	raw := message(attach("application/pdf", "b.pdf", pdftest.Text("second")))
	res, err := other.Message(ctx, strings.NewReader(raw))
	if !errors.Is(err, ErrBusy) || res != nil {
		t.Errorf("got %+v, %v; want ErrBusy and no results", res, err)
	}
	close(release)
	if r := <-firstDone; r.Status != StatusOK {
		t.Errorf("first: %s", r.Status)
	}
	onPDFWait = nil
	got, err := other.Message(context.Background(), strings.NewReader(raw))
	if err != nil || len(got) != 1 || got[0].Status != StatusOK || got[0].Text != "second" {
		t.Errorf("next try: %+v, %v", got, err)
	}
}
