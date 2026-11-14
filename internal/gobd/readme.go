package gobd

import (
	"fmt"
	"strings"
	"time"

	"github.com/keepia/archive/internal/attest"
)

// Readme is README.txt of an export: German, then English, with CRLF line
// ends. It names the customer group, the contents, the columns, how to
// verify the export and what it does not show. signed says whether the
// statement will be signed (the README is written before it).
func Readme(st *attest.Statement, signed bool) string {
	var b strings.Builder
	created := st.Created.UTC().Format(time.RFC3339)
	groupDE, groupEN := "keine (Konten ohne Eigentümer)", "none (accounts without an owner)"
	if st.Group != nil {
		// The name is one line in the statement (JSON); here control
		// characters become spaces so the README stays readable.
		name := strings.Map(func(r rune) rune {
			if r < ' ' || r == 0x7f {
				return ' '
			}
			return r
		}, st.Group.Name)
		groupDE = fmt.Sprintf("%s (Nr. %d)", name, st.Group.ID)
		groupEN = fmt.Sprintf("%s (no. %d)", name, st.Group.ID)
	}
	signedDE := "Die Erklärung ist mit dem Attestierungsschlüssel des Archivs signiert (Ed25519)."
	signedEN := "The statement is signed with the archive's attestation key (Ed25519)."
	if !signed {
		signedDE = "Die Erklärung ist NICHT signiert: das Archiv hatte keinen Attestierungsschlüssel."
		signedEN = "The statement is NOT signed: the archive had no attestation key."
	}
	fmt.Fprintf(&b, `mail-archive – GoBD-Export (Format v1)
=========================================

Deutsch (English below)
-----------------------

Erstellt: %s (UTC), Export-Nr. %s, Software %s.
Kundengruppe: %s, wie vom Betreiber zum Zeitpunkt des Exports benannt.
Die Nummer der Gruppe bleibt gleich, wenn die Gruppe später umbenannt wird,
und wird nie an eine andere Gruppe vergeben; der Name ist der zum Zeitpunkt
des Exports.
Zeitzone für Datum und Uhrzeit: %s.

Inhalt
- mail/JJJJ/MM/<sha256>.eml: die archivierten E-Mails in den Originalbytes,
  wie sie empfangen wurden (GoBD Rz. 131). Sie sind die Dokumente; die
  Tabellen sind eine Hilfe. JJJJ/MM ist der Monat des Datums der Nachricht.
- messages.csv: eine Zeile je exportierter Nachricht (Schlüssel sha256).
- locations.csv: eine Zeile je ausgewähltem Ablageort (Konto, Ordner) einer
  Nachricht (Fremdschlüssel sha256).
- missing.csv: Nachrichten der Auswahl ohne gültige Datei. Sie stehen nur
  hier, nicht in messages.csv. Ist die Tabelle nicht leer, sagt die Erklärung
  "complete no".
- index.xml: Beschreibung der Tabellen nach dem Beschreibungsstandard
  (Anhang der GoBD, Rz. 178).
- attest/: die Attestierung, die die Erklärung nennt, und die öffentlichen
  Schlüssel des Archivs.
- SHA256SUMS: SHA-256 jeder Datei oben.
- STATEMENT.txt: Exporterklärung mit Auswahl, Kundengruppe, Eigentümern,
  Anzahlen und dem SHA-256 von SHA256SUMS. %s

Tabellen
- UTF-8 ohne BOM, Zeilenende CRLF, Trennzeichen ";", jeder Text in ",
  mit Kopfzeile.
- In einem Wert wird " zu ', und Zeilenumbrüche, Tabulatoren und andere
  Steuerzeichen werden zu Leerzeichen. Ein Wert, der mit = + - @ beginnt,
  erhält ein führendes ', damit Tabellenprogramme ihn nicht als Formel
  lesen; in IDEA lässt es sich mit einem berechneten Feld entfernen. Werte
  über 32.768 Bytes werden gekürzt. Maßgeblich ist immer die .eml-Datei.
- Datum dreifach: date (TT.MM.JJJJ) und time (HH:MM:SS) in der Zeitzone
  oben, date_utc in UTC (ISO 8601). Weitere Zeitpunkte in UTC (ISO 8601).
- Bcc-Empfänger stehen nicht in den Kopfzeilen und werden nicht gespeichert;
  bei Journaling (SMTP) stehen alle Empfänger in locations.csv, envelope_to.
- archive_seq, attestation und die Nummer der Kundengruppe sind Nummern der
  ganzen Instanz. Die eine Attestierung in attest/ enthält Summen der ganzen
  Instanz (Total und die Nachrichten und Löschungen ihres Zeitraums), weil sie
  Teil des signierten Texts sind.
- holds ist nur in Exporten nach Aufbewahrungssperre (legal hold) gefüllt,
  sonst leer, weil nur Administratoren Sperren sehen. Sperren, die mehrere
  Kunden betreffen, werden nicht aufgeführt.

Prüfen
- Mit mail-archive:  mail-archive export verify <Export> --public-key <Ihre Kopie von signing.pub>
  Geben Sie die Kopie des öffentlichen Schlüssels an, die Sie außerhalb des
  Archivservers aufbewahren, nicht attest/signing.pub aus diesem Export: wer
  den Export verändert, kann auch diese Datei ersetzen.
  (Mit --attestations <Ihre Kopie von attestations.jsonl> gegen die eigene
  Kopie, mit --expect-group "<Name>" gegen die erwartete Kundengruppe.)
- Ohne mail-archive:  sha256sum -c SHA256SUMS  (Linux, macOS: shasum -a 256 -c)
  oder in PowerShell: Get-FileHash -Algorithm SHA256 <Datei>.
  Das prüft die Dateien, nicht die Signatur der Erklärung.
- Reihenfolge beim Schreiben: erst die .eml-Dateien, dann die Tabellen,
  zuletzt SHA256SUMS und STATEMENT.txt.

Was der Export zeigt und was nicht
- Er zeigt, dass er jede Nachricht enthält, die das Archiv für die Auswahl
  zum Zeitpunkt des Exports hatte, dass die Dateien unverändert sind, dass
  sie attestiert wurden, und zu welcher Kundengruppe und welchen Eigentümern
  sie gehören.
- Er zeigt nicht, dass jede E-Mail das Archiv erreicht hat.
- Keine Rechtsberatung. Auswahl, Klassifizierung und die
  Verfahrensdokumentation bleiben Aufgabe des Kunden.

English
-------

Created: %s (UTC), export id %s, software %s.
Customer group: %s, as named by the operator at the time of the export.
The group's number stays the same if the group is renamed later and is never
given to another group; the name is the one at the time of the export.
Time zone of dates and times: %s.

Contents
- mail/YYYY/MM/<sha256>.eml: the archived mail in its original bytes, as
  received (GoBD Rz. 131). These are the documents; the tables are an aid.
  YYYY/MM is the month of the message's date.
- messages.csv: one row per exported message (key sha256).
- locations.csv: one row per selected place (account, folder) of a message
  (foreign key sha256).
- missing.csv: messages of the selection without a valid file. They appear
  only here, not in messages.csv. If it has rows, the statement says
  "complete no".
- index.xml: description of the tables in the description standard (GoBD
  annex, Rz. 178).
- attest/: the attestation the statement names, and the archive's public
  keys.
- SHA256SUMS: the SHA-256 of every file above.
- STATEMENT.txt: the export statement with the selection, customer group,
  owners, counts and the SHA-256 of SHA256SUMS. %s

Tables
- UTF-8 without BOM, CRLF line ends, ";" as separator, every text in ",
  with a header row.
- Inside a value, " becomes ', and line breaks, tabs and other control
  characters become a space. A value that starts with = + - @ gets a leading
  ' so that spreadsheets do not read it as a formula; in IDEA a computed
  field removes it. Values over 32,768 bytes are cut. The .eml file is
  always authoritative.
- Dates three ways: date (DD.MM.YYYY) and time (HH:MM:SS) in the time zone
  above, date_utc in UTC (ISO 8601). Other times are UTC (ISO 8601).
- Bcc recipients are not in the headers and are not stored; with journaling
  (SMTP), locations.csv lists every recipient in envelope_to.
- archive_seq, attestation and the customer group's number are numbers of
  the whole instance. The one attestation in attest/ carries totals of the
  whole instance (Total, and the messages and deletions of its interval),
  because they are part of its signed text.
- holds is filled only in exports by legal hold, empty otherwise, because
  only administrators see holds. Holds that cover several customers are not
  listed.

Verifying
- With mail-archive:  mail-archive export verify <export> --public-key <your copy of signing.pub>
  Pass the copy of the public key that you keep off the archive's host,
  not attest/signing.pub from this export: whoever changed the export could
  have replaced that file too.
  (With --attestations <your copy of attestations.jsonl> against your own
  copy, with --expect-group "<name>" against the customer group you expect.)
- Without it:  sha256sum -c SHA256SUMS  (macOS: shasum -a 256 -c), or in
  PowerShell: Get-FileHash -Algorithm SHA256 <file>. This checks the files,
  not the statement's signature.
- Write order: first the .eml files, then the tables, last SHA256SUMS and
  STATEMENT.txt.

What the export shows, and what it does not
- It shows that it holds every message the archive had for the selection at
  the time of the export, that the files are unchanged, that they were
  attested, and which customer group and owners they belong to.
- It does not show that every mail reached the archive.
- Not legal advice. The selection, classification and the procedure
  documentation (Verfahrensdokumentation) stay the customer's job.
`,
		created, st.ID, st.Software, groupDE, st.TimeZone, signedDE,
		created, st.ID, st.Software, groupEN, st.TimeZone, signedEN)
	return strings.ReplaceAll(b.String(), "\n", "\r\n")
}
