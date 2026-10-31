package attachtext

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/attachtext/pdftest"
	"github.com/pklnx/mail-archive/internal/mime"
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
