# Retention and legal hold

mail-archive keeps mail for ever unless you tell it otherwise. Retention
rules say how long mail must be kept; once that period has ended, a
reviewed deletion run removes it. Legal holds stop deletion while they are
active. This page explains the rules, the legal background as far as
software can help with it, and what you have to document yourself.

::: warning Not legal advice
Which period applies to which mail, and when deletion is required, are
legal questions. The notes below are a starting point for a conversation
with your tax adviser or data protection officer, not an answer.
:::

## In short

- **No rule, no deletion.** Installing mail-archive or adding a user deletes
  nothing. Only an admin adds rules, for an account or one of its folders.
- **The longest period wins.** A message is deleted only when **every** place
  it was found, in every user's accounts, has a rule whose period has ended.
  One unruled copy keeps it.
- **Deletion is reviewed.** `retention preview` (or *New deletion run* in the
  web UI) lists what may go; nothing happens until an admin confirms with the
  number of messages. Every message is checked again when it is deleted.
- **Holds win over rules.** A legal hold stops deletion of what it covers.
- **Everything is recorded:** rules, holds and runs in the
  [audit log](./audit-log), every deleted message as a deletion record
  (hash, size, dates, no subject or sender), and in the next signed
  [attestation](./tamper-evidence).

## Retention periods under GoBD

German tax law (§147 AO) and commercial law (§257 HGB) set the same
periods:

| Documents | Period |
|---|---|
| Received business letters and copies of sent ones (Handels- und Geschäftsbriefe) | 6 years |
| Accounting vouchers (Buchungsbelege), for example an invoice sent by mail | 8 years since 2025 (before: 10; check the transition rules with your tax adviser) |
| Books, inventories, annual statements, working instructions and organisational documents | 10 years |

The period starts at the **end of the calendar year** in which the letter was
received or sent (§147 Abs. 4 AO, §257 Abs. 5 HGB). A letter received on
5 March 2019 with 6 years may be deleted from 1 January 2026.

The period does not end while the documents still matter for taxes whose
assessment period is open, for example during a tax audit (Ablaufhemmung).
mail-archive cannot know this: put a [legal hold](#legal-holds) on the
affected mail.

Which period applies to a mail is a legal classification. A mail that only
carries an invoice is a transport; the invoice is the voucher. Spam,
newsletters and private mail have no retention duty. mail-archive cannot
classify mail and keeps a message only as a whole, so the usual setup is one
rule per business account with the longest period its mail can need: 8
years if invoices arrive by mail, 10 if organisational documents do. Check
this with your tax adviser.

**GoBD does not require deletion.** Keeping mail longer than the period is
not a GoBD violation.

## GDPR

Personal data must be deleted once it is no longer needed (Art. 5 Abs. 1
lit. e and Art. 17 GDPR). During the retention period, the legal obligation
is such a need. After it, deletion is required without undue delay. A
yearly, reviewed deletion run fits this and the year-end rule: every
January, one run deletes the mail whose period ended on 31 December. Agree
the cadence with your data protection officer.

The deletion records keep the SHA-256 of every deleted message. A hash
reveals nothing readable, but it is a pseudonymous identifier. It is needed:
the attestation chain proves with it that a message was deleted, not lost,
and the archive uses it to avoid archiving a deleted message again.

## Which date counts

The period of a message in a folder counts from:

- the **later** of the Date header and the date the server received it
  (for IMAP, the internal date; for SMTP, when the archive received it; for
  imports, the mbox `From ` line, the Maildir file time or the Date header),
- but **never later** than when the archive first stored the message.

So spam dated 2001 counts from when it really arrived, a message dated in
the future counts from when it was archived, and an mbox from 2015 imported
in 2026 counts from 2015. The year ends in the rule's time zone (default
`Europe/Berlin`). The message view shows when a message's period ends, or
"no rule: kept".

## Rules

```sh
./ma retention rule add --account work --years 8 --note "§147 AO, invoices by mail"
./ma retention rule add --account work --folder Newsletter --years 1 --force
./ma retention rule list
```

![Retention rules in the web UI](/screenshots/retention.png)

Or in the web UI: **Retention → Rules**. A rule covers an account, or one of
its folders. Rules under 6 years need a confirmation. Because the longest
period wins, a folder rule cannot shorten an account rule: to keep a
newsletter folder shorter, give the account no account-wide rule and give
each folder its own.

Rules are added and removed, not changed. Removing a rule keeps its mail for
ever again, unless another rule covers it.

### smtp accounts with a fill

An smtp account has one folder, `JOURNAL`: what arrives over SMTP, what a
[fill](./journaling#bring-in-the-mail-from-before-journaling) imports from
IMAP and what [`import --journal`](./importing#into-an-smtp-account) imports
from files all go there. A rule on `JOURNAL`, or an account-wide one, covers
them all.

- A rule matches the folder name exactly. Folders of fills started before
  this layout (`alice@example.com/INBOX`) are covered by an account-wide rule
  or by rules of their own, not by a rule on `JOURNAL`.
- Older versions called the journal folder `INBOX`. The upgrade renames it
  to `JOURNAL` together with its rules, so retention does not change. A rule
  on `JOURNAL` that existed before matched no folder, so its mail was kept
  for ever; the upgrade deletes it, with an entry in the audit log, rather
  than let it shorten retention.
- A journal copy that retention deleted no longer counts as received over
  SMTP, so a later fill or `import --journal` stores the other copy of it.
  Its retention period has ended too, so the next deletion run lists it
  again; nothing is lost. A message retention deleted itself is not stored
  again.

## Legal holds

A hold stops the deletion of the mail it covers, whatever the rules say:
during a tax audit, a lawsuit or an investigation.

```sh
./ma hold add "Tax audit 2026" --reason "Betriebsprüfung, letter of 2026-09-01" --account work --until 2024-01-01
./ma hold add "Falcon dispute" --reason "lawsuit 12 O 345/26" --query falcon
```

Or **Retention → Legal holds**, with the filter in the search syntax. Every
condition must match: a user, an account, a period, a search filter. A
filter is matched by search when the hold is added and before every
deletion, and its matches are only ever added; until then the hold covers
its whole scope. Only admins see holds.

Holds are released with a note and never deleted. A user named by a hold
can therefore not be removed (lock them instead), and an account whose owner
is on hold cannot move to another user.

## Deletion runs

```sh
./ma retention preview         # what may go now, by account, folder and year
./ma retention show 3          # every message with date, sender, subject and places
./ma retention confirm 3       # type the number of messages, then it deletes
```

Or **Retention → Deletion runs**: the web UI shows the counts and the hashes,
not subjects (admins do not read other users' mail there). Confirming needs
the number of messages typed in. The web server then deletes in the
background; the page shows the progress.

Every message is checked again when it is deleted: a rule removed, a hold
added or a new place since the preview keeps it. An interrupted run resumes.
See [Deletion](../reference/cli#deletion) for the details.

A long first sync of a large mailbox, and a fill, hold a lock that deletion
waits for; each batch waits up to 10 minutes and the run goes on afterwards.
Each running sync and fill also keeps one database connection.

**The files stay for a grace period** (`MAIL_ARCHIVE_RETENTION_FILE_GRACE`,
default 7 days) after their rows are gone. A backup made meanwhile has every
file its dump needs, and restoring the last dump undoes a wrong deletion.
Keep the grace longer than the time between your dump and the copy of the
data directory.

**A deleted message does not come back** where it would already be expired:
after a renumbered IMAP folder, a copy in another folder, an import or an
SMTP redelivery. In an account without a rule, or with a longer one, it is
archived again, as if that place had existed at deletion.

After restoring a dump older than a run, its messages come back as rows
without files. Run `retention preview` again.

## Tamper-evident, not tamper-proof

The database refuses to delete a place whose period has not ended or that is
on hold, and a message row without its deletion record. The deletion records
are append-only, and the next attestation signs them. Someone with owner
rights on PostgreSQL can still remove all of this; the off-host copy of the
attestations then shows it. See [Tamper evidence](./tamper-evidence).

## Procedure documentation (Verfahrensdokumentation)

GoBD expects you to document how mail is captured, stored, protected and
deleted. mail-archive cannot write it, but this template covers its part.
Copy it, fill in the brackets and add what is yours.

```md
# Procedure documentation: e-mail archive

## 1. Purpose and scope
- Archive: mail-archive [version], operated by [name, role].
- Covered mailboxes: [accounts, users], business purpose: [..].
- Not covered: [private mailboxes, ..].

## 2. Capture
- IMAP accounts, synced every [6h]; mail deleted on the server between two
  syncs is not captured.
- SMTP journaling from [mail server] for [addresses]; every delivered copy is
  captured.
- Imports of [mbox/Maildir/eml] from [source] on [date].

## 3. Storage and integrity
- Each message is stored unchanged as an .eml file named by its SHA-256, plus
  its metadata in PostgreSQL.
- `verify` checks every file weekly ([cron line]); findings go to [person].
- Signed attestations every [24h]; copies of attestations.jsonl and
  signing.pub are kept at [off-host place], checked with `attest check`
  [how often] by [person].

## 4. Access
- Users see only the mail of their own accounts. Admins manage users,
  accounts, rules and holds; they read no other user's mail in the web UI.
- Administrative access to the server, PostgreSQL and the data directory:
  [persons]. Every search, read, export and change is in the audit log,
  exported [how often] to [place].

## 5. Retention and deletion
- Rules: [account/folder → years, legal basis], added on [date] by [person].
- Deletion: one reviewed run per year in [January] by [person], confirmed
  by [person]; the run IDs and counts are in the audit log.
- Legal holds are set by [person] on [occasion: tax audit, lawsuit] and
  released with a note.

## 6. Backup and recovery
- Nightly backup (dump, then data directory) to [place], kept [how long];
  restore tested on [date].
- The secret key is kept at [place].

## 7. Changes
- [date]: [what changed, who].
```

Keep the documentation with the other GoBD documents and update it when
the setup changes.
