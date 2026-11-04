# GoBD export

This page is for customers of a hosted instance: how to get your archived
mail for a tax audit, and how to check what you receive. It is no legal
advice; what your records must contain stays your call and your tax
advisor's.

## What the tax office may ask for

The GoBD (BMF-Schreiben of 28.11.2019, amended on 11.03.2024 and
14.07.2025) give the tax office three ways to access electronic records
during an audit:

- **Z1, unmittelbarer Datenzugriff:** the auditor reads the records in your
  system. With auditor access, the owner of the accounts gives the auditor
  a grant for some accounts, folders and a period; the auditor then reads
  in the web UI, and every step is in the [audit log](./audit-log).
- **Z2, mittelbarer Datenzugriff:** you evaluate the records as the auditor
  asks, for example with searches in the web UI.
- **Z3, Datenüberlassung** (formerly Datenträgerüberlassung, Rz. 167): you
  hand the records over "in einem maschinell lesbaren und auswertbaren
  Format", with their metadata and links. The tax office may not download
  them from your system itself; you hand them over. It deletes them once its
  notices are final (Rz. 168, 169).

The **GoBD export** is for Z3. Incoming electronic letters are kept in the
format they arrived in (Rz. 131): the export contains every message's
original `.eml` file, unchanged. The tables next to them are an aid for the
auditor's software; the `.eml` files are the documents.

Sources: [BMF-Schreiben of 11.03.2024](https://www.lfst.bayern.de/fileadmin/RESSOURCEN/INFORMATIONEN/Steuerinfos/Weitere_Themen/Aussenpruefung/2024-03-11-aenderung-gobd.pdf)
(Rz. 167 to 169, 178 and the annex on the description standard),
[2nd amendment of 14.07.2025](https://umwelt-online.de/recht/allgemei/steuer/11032024.htm).

## Requesting an export

Exports are made by the operator of the instance, on request; there is no
export button in the web UI. Ask your operator, from your company's admin
account or the contact you agreed on, and say:

- **what:** your whole company (all users of your customer group), or the
  users or accounts the audit covers, maybe only some folders;
- **which period:** for example the fiscal years under audit;
- **who asks,** for example "Betriebsprüfung, letter of the tax office of
  2026-11-02"; the operator records it with the export;
- **directory or ZIP:** a ZIP is one file (up to 200,000 messages); larger
  exports come as a directory, or one ZIP per year.

If you set a [legal hold](./retention#legal-holds) for the audit (or asked
for one), the operator can export exactly what the hold covers. If you gave
the auditor [access](./users#auditor-access), the operator can export
exactly that grant's scope: its accounts, folders and message period,
nothing else of yours; name the case. This also works before the access
starts and after it ended, not for a revoked grant. The export is then in
your [Log](./audit-log#auditors), shown as the operator's, with the grant.

**One export covers one customer.** An export never mixes companies: if you
ask for users of two companies (customer groups) of the same operator, you
get one export per company.

**How the operator handles it.** While exporting, the operator handles your
mail in plaintext: the export is not encrypted. The operator sends it only
to your admin (for an auditor grant's scope: to the owner of the grant),
over a channel you agree on, for example an encrypted archive with the
password on another channel, and deletes their copy once you confirm that
it arrived. Every export is in the audit log of your company, with the
digest of its manifest.

## What you receive

```
README.txt                 German, then English: contents, columns, how to check
index.xml                  description of the tables (GoBD annex, "Beschreibungsstandard")
messages.csv               one row per message: date, sender, recipients, subject, attachments …
locations.csv              where each message was: user, account, folder, flags, retention …
missing.csv                messages of the selection whose file was missing or damaged
mail/YYYY/MM/<sha256>.eml  the original messages
attest/                    the attestation the statement names, and its public key
SHA256SUMS                 the checksum of every file above
STATEMENT.txt              the signed export statement
```

The tables use `;`, `"` around text and CRLF line ends, as the GoBD annex
describes; an auditor's software (IDEA, for example) imports them with
`index.xml`. Two details: a `"` inside a value becomes `'`, and a value
that starts with `=`, `+`, `-` or `@` gets a leading `'` so that no
spreadsheet runs it as a formula (README.txt explains how to remove it).
Bcc recipients are not in the message headers; for mail captured by
journaling, `locations.csv` has the envelope recipients, Bcc included.

`STATEMENT.txt` says what the export is: its id and time, the selection,
your **customer group** (`group-id 7` and `group "Meier GmbH"`, as the
operator named it then), the list of **owners** (the users whose accounts
are in it), the counts, `complete yes` or `no`, and the checksum of
`SHA256SUMS`. It is signed with the archive's attestation key.

## Checking it

1. **Your company.** `group` in `STATEMENT.txt` names your company; the id
   stays the same if the operator renames the group later. Compare the
   `owners` list with your users: everyone whose mail the audit covers must
   be there.
2. **The files.** With the tool (no database or server needed):
   ```sh
   mail-archive export verify ./export --public-key signing.pub --expect-group "Meier GmbH"
   ```
   Pass the copy of `signing.pub` you keep yourself (see
   [Tamper evidence](./tamper-evidence)), not the one in the export's
   `attest/` directory. With `--attestations attestations.jsonl` it also
   compares the export's attestation with your own copy. It prints every
   problem and ends with `no problems` or a count; `0` means nothing was
   found. See [export verify](../reference/cli#export-verify) for the
   findings. Without the tool, in the export's directory:
   ```sh
   sha256sum -c SHA256SUMS
   ```
   checks every file, but not the signature or the tables. On Windows,
   `Get-FileHash -Algorithm SHA256` gives the same checksums.
3. **The digest.** The operator gives you the SHA-256 of `SHA256SUMS`
   separately. Compute it (`sha256sum SHA256SUMS`) and compare. Keep it with
   your audit documents: it identifies exactly this export later.
4. **Complete?** `complete no` and rows in `missing.csv` mean that some
   messages of the selection had no valid file in the archive. Ask the
   operator before you hand the export over.

## Handing it over

Give the export (or the ZIP) to the auditor in the format and on the medium
you agreed with them. Keep the digest, and delete your own copies when the
audit no longer needs them, as you would paper copies.

## What it shows, and what not

It shows:

- that the export holds every message the archive had for the selection at
  the moment of the export, unchanged, with where it was found;
- which company and which users it belongs to;
- that the archive attested these messages (when an attestation covers
  them; the `attestation` column).

It does not show:

- that every mail reached the archive: an IMAP account is copied at each
  sync, so mail deleted on the server between two syncs is not in it;
- that the selection is right for the request: choosing what the auditor
  gets, leaving out private mail (GDPR), and documenting your procedure
  (Verfahrensdokumentation, see the
  [template](./retention#procedure-documentation-verfahrensdokumentation),
  section 8) stay yours.

The one attestation entry in `attest/` is part of the archive's signed
chain and carries numbers of the whole instance: the total of messages and
the counts of its last interval. The `archive_seq` and `attestation`
columns and the group id are numbers of the whole instance too. They say
nothing about other companies' mail beyond these counts.
