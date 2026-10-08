package mime

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

var alternative = crlf(`From: =?UTF-8?Q?J=C3=BCrgen?= <j@example.com>
To: a@example.com, "B, C" <b@example.com>
Subject: =?UTF-8?Q?Rechnung_f=C3=BCr_Oktober?=
Date: Mon, 5 Oct 2026 10:00:00 +0200
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="b1"

--b1
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: quoted-printable

Hallo, anbei die Rechnung f=C3=BCr Oktober.
--b1
Content-Type: text/html; charset=utf-8

<p>Hallo, anbei die <b>Rechnung</b></p>
--b1--
`)

func TestParseAlternative(t *testing.T) {
	m := Parse(strings.NewReader(alternative))
	if m.Subject != "Rechnung für Oktober" {
		t.Errorf("Subject = %q", m.Subject)
	}
	if !strings.Contains(m.From, "Jürgen") || !strings.Contains(m.From, "j@example.com") {
		t.Errorf("From = %q", m.From)
	}
	if !strings.HasPrefix(m.To, "a@example.com, ") || !strings.Contains(m.To, "b@example.com") {
		t.Errorf("To = %q", m.To)
	}
	if m.Text != "Hallo, anbei die Rechnung für Oktober." {
		t.Errorf("Text = %q", m.Text)
	}
	if !strings.Contains(m.HTML, "<b>Rechnung</b>") {
		t.Errorf("HTML = %q", m.HTML)
	}
	if len(m.Parts) != 2 || m.Parts[0].Attachment || m.Parts[1].Attachment {
		t.Errorf("Parts = %+v", m.Parts)
	}
	if got := Index(strings.NewReader(alternative)); got.Text != m.Text || got.HasAttachment || got.AttachmentNames != nil {
		t.Errorf("Index = %+v", got)
	}
}

var htmlOnlyLatin1 = crlf(`Subject: Newsletter
Content-Type: text/html; charset=iso-8859-1
Content-Transfer-Encoding: base64

PGh0bWw+PGhlYWQ+PHRpdGxlPlRpdGVsPC90aXRsZT48c3R5bGU+cHtjb2xvcjpyZWR9PC9zdHlsZT48L2hlYWQ+PGJvZHk+PHA+R3L832UgYXVzIEv2bG48L3A+PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0PjxkaXY+WndlaXRlJm5ic3A7WmVpbGU8L2Rpdj48L2JvZHk+PC9odG1sPg==
`)

func TestIndexFromLatin1HTML(t *testing.T) {
	got := Index(strings.NewReader(htmlOnlyLatin1)).Text
	want := "Grüße aus Köln\n\nZweite Zeile"
	if got != want {
		t.Errorf("Index().Text = %q, want %q", got, want)
	}
}

func TestWindows1252(t *testing.T) {
	raw := "Subject: x\r\nContent-Type: text/plain; charset=windows-1252\r\n\r\nPreis: 5 \x80\r\n"
	if got := Parse(strings.NewReader(raw)).Text; got != "Preis: 5 €\r\n" {
		t.Errorf("Text = %q", got)
	}
}

var mixed = crlf(`Subject: Mit Anhang
Content-Type: multipart/mixed; boundary="outer"

--outer
Content-Type: multipart/related; boundary="inner"

--inner
Content-Type: text/html; charset=utf-8

<p>Logo: <img src="cid:logo@x"></p>
--inner
Content-Type: image/png
Content-ID: <logo@x>
Content-Disposition: inline
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--inner--
--outer
Content-Type: application/pdf; name="Rechnung 10/2026.pdf"
Content-Disposition: attachment; filename="Rechnung 10/2026.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjQK
--outer--
`)

func TestPartsAndOpenPart(t *testing.T) {
	m := Parse(strings.NewReader(mixed))
	if len(m.Parts) != 3 {
		t.Fatalf("Parts = %+v", m.Parts)
	}
	img, pdf := m.Parts[1], m.Parts[2]
	if img.ContentType != "image/png" || img.ContentID != "logo@x" || !img.Inline || img.Size != 8 {
		t.Errorf("inline image = %+v", img)
	}
	if !pdf.Attachment || pdf.Filename != "Rechnung 10_2026.pdf" || pdf.Size != 9 {
		t.Errorf("attachment = %+v", pdf)
	}

	p, data, err := OpenPart(strings.NewReader(mixed), 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if p.Index != 2 || !bytes.Equal(data, []byte("%PDF-1.4\n")) {
		t.Errorf("OpenPart = %+v %q", p, data)
	}
	if _, _, err := OpenPart(strings.NewReader(mixed), 7, 1<<20); !errors.Is(err, ErrNoPart) {
		t.Errorf("missing part: err = %v", err)
	}
	if _, _, err := OpenPart(strings.NewReader(mixed), 2, 4); err == nil {
		t.Error("expected size limit error")
	}
}

func TestBrokenMessageIsLenient(t *testing.T) {
	raw := crlf(`Subject: kaputt
Content-Type: multipart/mixed; boundary="nope"

--other
Content-Type: text/plain

Text ohne passende Grenze
`)
	m := Parse(strings.NewReader(raw))
	if m.Subject != "kaputt" {
		t.Errorf("Subject = %q", m.Subject)
	}
	_ = Index(strings.NewReader(raw)) // must not panic
	if got := Parse(strings.NewReader("not a mail at all")); got == nil {
		t.Error("nil result")
	}
}

func TestIndexTruncatesText(t *testing.T) {
	raw := "Subject: big\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("ä", MaxIndexText)
	got := Index(strings.NewReader(raw)).Text
	if len(got) > MaxIndexText || !strings.HasPrefix(got, "ää") || strings.ContainsRune(got, '�') {
		t.Errorf("len = %d, invalid rune present = %v", len(got), strings.ContainsRune(got, '�'))
	}
}

func TestHTMLToText(t *testing.T) {
	got := HTMLToText(`<ul><li>Eins</li><li>Zwei &amp; drei</li></ul><script>x()</script><br>Ende`)
	if got != "Eins\n\nZwei & drei\n\nEnde" {
		t.Errorf("HTMLToText = %q", got)
	}
}

var attachmentKinds = crlf(`Subject: Anhaenge
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="m"

--m
Content-Type: multipart/related; boundary="r"

--r
Content-Type: text/html; charset=utf-8

<p>Logo: <img src="cid:logo@x"></p>
--r
Content-Type: image/png; name="logo.png"
Content-Disposition: inline; filename="logo.png"
Content-ID: <logo@x>

iVBORw0KGgo=
--r--
--m
Content-Type: application/pdf; name="Rechnung 2024.pdf"
Content-Disposition: inline; filename="Rechnung 2024.pdf"

JVBERi0=
--m
Content-Type: application/zip
Content-Disposition: attachment; filename="daten.zip"

UEs=
--m
Content-Type: text/plain; name="notiz.txt"

Eine Notiz, die als Text mitgeschickt wurde.
--m
Content-Type: text/csv
Content-Disposition: attachment; filename="liste.csv"

a;b
--m--
`)

func TestAttachmentRules(t *testing.T) {
	m := Parse(strings.NewReader(attachmentKinds))
	got := map[string]bool{}
	for _, p := range m.Parts {
		if p.Filename != "" {
			got[p.Filename] = p.Attachment
		}
	}
	want := map[string]bool{
		"logo.png":          false, // inline image referenced by cid:
		"Rechnung 2024.pdf": true,  // Apple Mail: named inline PDF without Content-ID
		"daten.zip":         true,
		"notiz.txt":         false, // a named text part without disposition is a body
		"liste.csv":         true,
	}
	for name, w := range want {
		if a, ok := got[name]; !ok || a != w {
			t.Errorf("%s: Attachment = %v (found %v), want %v", name, a, ok, w)
		}
	}

	idx := Index(strings.NewReader(attachmentKinds))
	if !idx.HasAttachment {
		t.Error("HasAttachment = false")
	}
	if names := strings.Join(idx.AttachmentNames, "|"); names != "Rechnung 2024.pdf|daten.zip|liste.csv" {
		t.Errorf("AttachmentNames = %q", names)
	}
}

func TestIndexWithoutAttachments(t *testing.T) {
	raw := crlf(`Subject: Nur Bild
MIME-Version: 1.0
Content-Type: multipart/related; boundary="r"

--r
Content-Type: text/html

<img src="cid:a@x">
--r
Content-Type: image/gif; name="a.gif"
Content-Disposition: inline
Content-ID: <a@x>

R0lG
--r--
`)
	if idx := Index(strings.NewReader(raw)); idx.HasAttachment || len(idx.AttachmentNames) != 0 {
		t.Errorf("Index = %+v", idx)
	}
}

func TestIndexAttachmentNameCaps(t *testing.T) {
	part := func(name string) string {
		return "--m\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"" +
			name + "\"\r\n\r\nx\r\n"
	}
	build := func(names []string) string {
		var b strings.Builder
		b.WriteString("Subject: viele\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"m\"\r\n\r\n")
		for _, n := range names {
			b.WriteString(part(n))
		}
		b.WriteString("--m--\r\n")
		return b.String()
	}

	var many []string
	for i := range MaxAttachmentNames + 20 {
		many = append(many, "f"+strings.Repeat("0", 3)+string(rune('a'+i%26))+".bin")
	}
	idx := Index(strings.NewReader(build(many)))
	if len(idx.AttachmentNames) != MaxAttachmentNames || !idx.HasAttachment {
		t.Errorf("got %d names, want %d", len(idx.AttachmentNames), MaxAttachmentNames)
	}

	// Names are sanitized to 255 bytes; 70 of them exceed 16 KB together.
	var long []string
	for i := range 70 {
		long = append(long, strings.Repeat(string(rune('a'+i%26)), 300))
	}
	idx = Index(strings.NewReader(build(long)))
	joined := strings.Join(idx.AttachmentNames, "\n")
	if len(joined) > MaxAttachmentNameBytes || len(idx.AttachmentNames) != MaxAttachmentNameBytes/256 {
		t.Errorf("got %d names, %d bytes", len(idx.AttachmentNames), len(joined))
	}
	for _, n := range idx.AttachmentNames {
		if len(n) != 255 {
			t.Errorf("name of %d bytes", len(n))
		}
	}
}
