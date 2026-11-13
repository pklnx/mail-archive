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

- **No rule, no deletion.** A new instance or a new user deletes nothing.
  Only an admin (the operator) adds rules, for an account or one of its
  folders.
- **The longest period wins.** A message is deleted only when **every** place
  it was found, in every user's accounts, has a rule whose period has ended.
  One unruled copy keeps it. To drop the copies of a removed account
  deliberately, [delete the account
  completely](./accounts#deleting-a-removed-account-completely) with
  `./ma account purge NAME`; its rules must be removed first.
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

Admins add rules in the web UI: **Retention → Rules**, a table with **Add
rule** above it; the form is a page of its own. Each rule has a page too,
from the link in its row, and **Remove** is in the row's menu (⋯) and in
the page's.

![Retention rules in the web UI](/screenshots/retention.png)

Or the operator, on the command line:

```sh
./ma retention rule add --account work --years 8 --note "§147 AO, invoices by mail"
./ma retention rule add --account work --folder Newsletter --years 1 --force
./ma retention rule list
```

A rule covers an account, or one of its folders. Rules under 6 years need a
confirmation. Because the longest period wins, a folder rule cannot shorten
an account rule: to keep a newsletter folder shorter, give the account no
account-wide rule and give each folder its own.

Rules are added and removed, not changed. Removing a rule keeps its mail for
ever again, unless another rule covers it.

With two or more [customer groups](./users#customer-groups), the rules table
has a **Customer group** column (the group of the account's owner) and a
**Customer group** filter above it, and **Add rule** lists the accounts under
a heading per group. An account without an owner is in no group.

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
  again where it would already be expired, in the accounts and customer
  groups that held it, while no account of that group holds it (see
  [Deletion](../reference/cli#deletion)).

## Legal holds

A hold stops the deletion of the mail it covers, whatever the rules say:
during a tax audit, a lawsuit or an investigation. Admins add one under
**Retention → Legal holds** → **Add hold**, with the period and, over their
own mail, the filter in the search syntax. Or the operator, on the command
line:

```sh
./ma hold add "Tax audit 2026" --reason "Betriebsprüfung, letter of 2026-09-01" --account work --until 2024-01-01
./ma hold add "Falcon dispute" --reason "lawsuit 12 O 345/26" --query falcon
./ma hold add "Meier audit" --reason "Betriebsprüfung Meier GmbH" --group "Steuerbüro Meier"
```
The table lists the active holds; the filter button (funnel icon) in
its toolbar shows released holds too (**Show released holds**). Each hold
has its page with the reason, and **Release** is in its menu (⋯). Every
condition must match: a user, an account, a period, a search filter. A
filter is matched by search when the hold is added and before every
deletion, and its matches are only ever added; until then the hold covers
its whole scope. A search filter also matches the text extracted from
attachments, once that text is searchable for the user whose mail it
matches (see [Attachments](./web-ui#attachments)): a hold matches as each
user it covers, through that user's own accounts. After the upgrade that brings
attachment search, older messages are extracted in the background newest
first, so the oldest mail, which retention deletes first, comes last: ask
the operator to run `./ma reindex` once before you rely on a filter hold
over attachment text. Only admins see holds.

A filter searches the headers and the text, never SMTP envelopes: a
recipient filter (`to:`, `--recipient`) matches the To and Cc headers only,
not envelope (BCC) recipients. To keep journal mail that reached someone
only as a BCC recipient, hold the smtp account itself, with a period if
needed. Matches a filter stored before version 1.5.3 through an envelope
stay held until the hold is released.

Holds are released with a note and never deleted. A user named by a hold
can therefore not be removed (lock them instead), and an account whose owner
is on hold cannot move to another user.

### Who adds which hold

A search filter decides which of the covered mail a hold keeps, and so which
messages a deletion run lists. In the web UI, an admin adds a hold with a
search filter only over their own mail: the hold names them as its user, or
names no user and one of their own accounts. A filter over other users' mail,
over a customer group or over the whole archive is the operator's, with
`./ma hold add --query` (or `--sender`, `--recipient`, `--attachment`,
`--has-attachment`). For such a scope the form takes only a period
(`after:`, `before:`) and says so, and the server refuses a filter. Holds
without a filter (a user, an account, a group, a period, the whole archive)
are added in the web UI as before.

A filter hold an admin adds on one of their own accounts also names the admin
as its user. It covers the account only while they own it, and the account
cannot move to another user while the hold is active. Like every hold that
names a user, it keeps that user: the admin's user can no longer be removed,
only locked, even after the hold is released.

Before version 1.6.0, admins could add filter holds over other users' mail in
the web UI. Those holds stay in force: the upgrade releases none of them,
since a hold ends only when a person decides. `./ma hold list` shows who added each
hold in the column **CREATED BY**: the name of the admin who added it in the
web UI (marked "(removed)" if that user is gone), `cli` for the command line,
or `unknown` when the hold's audit entry is missing (its audit partition was
detached or dropped, or the row was written by hand). Review the filter holds
added in the web UI over other users' mail, and the `unknown` ones like them,
and release those you do not want to keep with `./ma hold release`.

### Match counts

The number of messages a filter matched is shown only for a hold on you or
on one of your own accounts. For any other hold the table says that it is
matched, without the number: admins see no mail except their own (see
[Security](../reference/security)). The operator's `./ma hold list` shows
every number.

### Holds per customer group

With two or more [customer groups](./users#customer-groups), a hold can name
a group (**Customer group** on **Add hold**, `--group` on the command line).
It then covers every account whose owner is in that group, also the accounts
of users who join the group after the hold was added. Like the other
conditions it only narrows the hold: a user or an account it names must be
in the group (the form offers only those), and the hold covers nothing
outside it.

- A user cannot move out of a group whose active hold covers the accounts
  they own, and an account cannot move to a user of another group while such
  a hold covers it: release the hold first. A user without accounts moves.
- A group that a hold names, active or released, cannot be removed: the hold
  is a record and keeps it.
- The holds table has a **Customer group** column: the group the hold names,
  or the group of its user or account; **All groups** for a hold that covers
  every group. The **Customer group** filter above the table shows the holds
  of one group together with those of all groups, since both keep that
  group's mail.
- The audit log records a hold that names only a group, and its release,
  under that group; a hold without user, account or group is instance-level
  and has no group.

## Deletion runs

An admin starts a run under **Retention → Deletion runs** → **New deletion
run**, which opens the run's page. It shows the counts by owner, account,
folder and year, and the hashes, not subjects (admins do not read other
users' mail there); with two or more customer groups, each line also shows
the owner's group, and a **Customer group** filter narrows the lines. A run
always covers every group: a message that several groups received is stored
once and deleted only when none of its places is kept. **Delete N messages** asks for the number of messages,
typed in, in a dialog; **Cancel run** is in the page's menu (⋯). The web
server then deletes in the background; the page shows the progress.

The operator's commands, which also list every candidate with its subject
(`show`), for a review on paper:

```sh
./ma retention preview         # what may go now, by account, folder and year
./ma retention show 3          # every message with date, sender, subject and places
./ma retention confirm 3       # type the number of messages, then it deletes
```

![A deletion run's page with the Delete dialog](/screenshots/retention-run.png)

Every message is checked again when it is deleted: a rule removed, a hold
added or a new place since the preview keeps it. An interrupted run resumes.
A deleted message's rows go with it in the same transaction, the text
extracted from its attachments and its queued background jobs included.
The batches, the lock they wait for (up to 10 minutes each), the file grace
period (`KEEPIA_ARCHIVE_RETENTION_FILE_GRACE`, default 7 days), why a deleted
message does not come back where it would already be expired (in the
accounts and customer groups it was deleted from), and what
happens after restoring a dump older than a run are in
[Deletion](../reference/cli#deletion).

A long first sync of a large mailbox, and a fill, hold the lock that
deletion waits for; a batch waits up to 10 minutes, and when the wait ends
without the lock, the run stops and resumes with `confirm` again or when the
web server starts. Each running sync and fill also keeps one database
connection.

The operator keeps the grace period longer than the time between the dump
and the copy of the data directory: a backup made within it has every file
its dump needs, and restoring the last dump undoes a wrong deletion.

## Tamper-evident, not tamper-proof

The database refuses to delete a place whose period has not ended or that is
on hold, and a message row without its deletion record. The deletion records
are append-only, and the next attestation signs them. Someone with owner
rights on PostgreSQL, in the hosted instance the operator, can still remove all
of this; your off-host copy of the attestations then shows it. See
[Tamper evidence](./tamper-evidence).

## Data access in a tax audit (Z1/Z2/Z3)

In a tax audit (Außenprüfung) the tax office may access the archived mail
in three ways (§147 Abs. 6 AO). This is how mail-archive maps them; whether
it is enough in your case is a question for your tax adviser.

- **Z1, unmittelbarer Datenzugriff:** the auditor reads the data in the
  system itself, read-only. The owner gives the auditor's login access to
  the accounts, folders and period the audit covers, for a limited time
  ([Auditor access](./users#auditor-access)). The auditor uses the archive's
  own search and filters, sees nothing outside the grant and changes
  nothing. The grant documents what the taxpayer made available, and every
  search and read is in the owner's [Log](./audit-log#auditors).
- **Z2, mittelbarer Datenzugriff:** the owner or their tax advisor searches
  as the auditor asks, and shows the results. This needs nothing extra; the
  searches are in the Log as the owner's own.
- **Z3, Datenüberlassung (formerly Datenträgerüberlassung):** the data
  handed over in a machine-readable form. Auditors do not export in the web
  UI; the owner asks the operator for a [GoBD export](./gobd-export) on the
  command line. `./ma export --format gobd --grant ID` exports exactly a
  grant's scope: its accounts, folders and message period, so the data
  handed over matches what the auditor reads.

**Before the audit, ask the operator for a [legal hold](#legal-holds)** over
the period, by account, owner or [customer group](#holds-per-customer-group).
A grant creates no hold: a deletion run may still remove mail whose period
ended, and that mail then leaves the auditor's view as well.

## Procedure documentation (Verfahrensdokumentation)

GoBD expects you to document how mail is captured, stored, protected and
deleted. mail-archive cannot write it, but this template covers its part.
Copy it and fill in the brackets; in the hosted instance the operator is the
party that runs it under your contract. Add what is yours.

Mandantentrennung is logical, enforced by the application, not by separate
databases. Deleting a group's mail (retention, account purge) removes that
group's locations; the message file stays only while another group has its
own location of it. The audit log records each entry's group from 1.4.0 on;
earlier entries have none. If physical separation is required for a customer,
that customer gets an individual instance (see
[Customer groups](./users#customer-groups)).

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
- Each message is stored unchanged in a file named by the SHA-256 of its
  content, plus its metadata in PostgreSQL. With encryption at rest the file
  is encrypted (.enc); decrypted, it is the unchanged message.
- [With encryption at rest:] the archive key is kept at [place] by [person],
  the recovery key at [offline place].
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
- Tax audits: the auditor [name, tax office] reads [accounts, folders,
  period] through an auditor grant from [date] to [date], granted by
  [person]; a legal hold over the period was added on [date].

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

## 8. Data access (Datenzugriff)
- Z3, Datenüberlassung: on a request of the tax office, [person] asks the
  operator for a GoBD export (`export --format gobd`) of [the whole
  customer group / these users], period [..]. One export covers one
  customer group; its STATEMENT.txt names the group and lists the owners,
  which [person] compares with our users.
- Before the audit, a legal hold for [the customer group / accounts] keeps
  the period from deletion; the export can follow that hold (`--hold`).
- An auditor who reads in the web UI gets an auditor grant from [owner] for
  [accounts, folders, period, until]; its scope can also be exported
  (`--grant`).
- On receipt, [person] runs `export verify` with our copy of signing.pub
  (and attestations.jsonl), or `sha256sum -c SHA256SUMS`, and compares the
  SHA-256 of SHA256SUMS with the digest the operator sent separately and
  with the `data.export_done` entry of the audit log.
- The digest is kept at [place]; the export goes to the tax office on
  [medium, agreed with the auditor]; our copies are deleted on [occasion],
  the operator's when we confirm receipt.
```

Keep the documentation with the other GoBD documents and update it when
the setup changes. Section 8 follows [GoBD export](./gobd-export).
