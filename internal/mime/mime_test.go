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
	if !strings.Contains(m.To, "b@example.com") {
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
	if got := IndexText(strings.NewReader(alternative)); got != m.Text {
		t.Errorf("IndexText = %q", got)
	}
}

var htmlOnlyLatin1 = crlf(`Subject: Newsletter
Content-Type: text/html; charset=iso-8859-1
Content-Transfer-Encoding: base64

PGh0bWw+PGhlYWQ+PHRpdGxlPlRpdGVsPC90aXRsZT48c3R5bGU+cHtjb2xvcjpyZWR9PC9zdHlsZT48L2hlYWQ+PGJvZHk+PHA+R3L832UgYXVzIEv2bG48L3A+PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0PjxkaXY+WndlaXRlJm5ic3A7WmVpbGU8L2Rpdj48L2JvZHk+PC9odG1sPg==
`)

func TestIndexTextFromLatin1HTML(t *testing.T) {
	got := IndexText(strings.NewReader(htmlOnlyLatin1))
	want := "Grüße aus Köln\n\nZweite Zeile"
	if got != want {
		t.Errorf("IndexText = %q, want %q", got, want)
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
	_ = IndexText(strings.NewReader(raw)) // must not panic
	if got := Parse(strings.NewReader("not a mail at all")); got == nil {
		t.Error("nil result")
	}
}

func TestIndexTextTruncates(t *testing.T) {
	raw := "Subject: big\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("ä", MaxIndexText)
	got := IndexText(strings.NewReader(raw))
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
