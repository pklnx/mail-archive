# Test data

`invoice.pdf` (15,809 bytes) is a real-world PDF: LibreOffice 24.2
(`soffice --headless --convert-to pdf:writer_web_pdf_Export`) converted a
small HTML page set in DejaVu Sans to it. It embeds a subset of DejaVu Sans
as a TrueType font with a ToUnicode map; DejaVu fonts are under the
Bitstream Vera license with public-domain changes, which allows
redistribution. The text is made up and contains no personal data:

```text
Rechnung Nr. 2026-10
Müller & Söhne GmbH, Straße 5, 80331 München
Gesamtbetrag: 1.234,56 €
Leistung: Proﬁl-Prüfung, Größe XL
```

"Proﬁl" is written with the ligature U+FB01, which extraction normalises
to "fi". Every other PDF the tests use is built at test time by
`../pdftest`.
