# Command line

The command line is the operator's tool. It runs on the instance's host,
which users do not reach; users ask the operator for anything on this page
and do the rest in the web UI. The commands that change
users or accounts (`user …`, `account …`, including fills and purges),
retention rules, holds and deletion runs, `jobs retry`, and `import`,
`export`, `backup` and `audit` leave an entry in the
[audit log](../guide/audit-log#the-fields); `migrate`, `reindex`,
`attest` and `attest keygen`, `sync`, `verify`, `status`, `notify test` and
the list commands do not.

With Docker Compose, run every command through the wrapper, for example
`./ma status`. The binary itself is called `mail-archive`. Every command has
`--help`.

## Setup

| Command | Purpose |
|---|---|
| `migrate` (or `migrate up`) | Create or upgrade the database schema. With `./ma`, it also creates the data directory and makes it writable for the container. On a database created by the `postgres:18-alpine` image (26.10-6 and earlier), it first rebuilds all database indexes once; until then the other commands that use the database (`serve` included, `migrate status` excepted) refuse to run. This index rebuild is not [`reindex`](#archive), which extracts message text for search. See [One-time steps](../guide/operations#one-time-steps). Waits for another running `migrate` (see [Upgrades](../guide/operations#upgrades)). After a killed `migrate`, it may wait for goose's migration lock while the killed command's statement ends, and says so: `waiting for goose's migration lock, held by database backend …`. |
| `migrate status` | Show applied and pending migrations. Does not wait for a running migration. |
| `migrate down --yes` | Roll back the latest migration. Usually deletes data; for development. Waits for another running `migrate`, also during its one-time index rebuild, and, like `migrate`, for goose's migration lock. Refuses, like the other commands, while no `migrate` has done that rebuild. |
| `keygen` | Print a random secret key for `MAIL_ARCHIVE_SECRET_KEY` (not the attestation key, see [`attest keygen`](#attest)). |

## Users

Users log in to the web UI. Names are lowercase (`a-z`, `0-9`, `.`, `-`,
`_`); `Patrick` and `patrick` are the same user. Passwords need at least 12
characters and are prompted twice, or read once from stdin with
`--password-stdin`.

| Command | Purpose |
|---|---|
| `user add NAME` | Create a user. `--admin` for an admin (admins manage users in the web UI; they do not see other users' mail, but they see everyone's [audit log](../guide/audit-log)). |
| `user list` | List users with role, state (active, locked, must change password), number of accounts, passkeys and active API keys, and last login. |
| `user set-password NAME` | Set a new password. Ends the user's sessions. |
| `user reset-password NAME` | Generate a password, print it once; the user must change it at the next login. Ends the user's sessions, removes their passkeys and revokes their [API keys](./api#api-keys) (`--keep-passkeys` and `--keep-api-keys` keep them). |
| `user remove-passkeys NAME` | Remove all passkeys of a user, for example after a lost device. Ends the user's sessions. |
| `user api-keys NAME` | List a user's API keys: name, last characters, scopes, created, expires, last used, state. Keys are created in the web UI's profile, never on the command line: that needs the second factor. |
| `user revoke-api-keys NAME` | Revoke all API keys of a user, for example when no admin can log in. |
| `user reset-2fa NAME` | Turn off the user's 2FA, for example after a lost phone. Ends the user's sessions; admins set it up again at the next login. |
| `user promote NAME`, `user demote NAME` | Grant or take away the admin role. |
| `user lock NAME`, `user unlock NAME` | Stop a user from logging in, or allow it again. Locking ends the user's sessions. |
| `user remove NAME` | Delete the user. Refused while the user owns accounts, also removed ones; hand them over with `account move` first. |

The last admin who can log in cannot be locked, removed or demoted. Admins
can do the same in the web UI; see [Users](../guide/users).

## Accounts

| Command | Purpose |
|---|---|
| `account add NAME` | Add an account. Flags: `--host` and `--username` (required), `--port`, `--tls tls\|starttls\|none` (default `tls`), `--include FOLDER`, `--exclude FOLDER` (repeatable), `--password-stdin`, `--skip-check`. The login is checked before saving unless `--skip-check` is set. |
| `account add-smtp NAME --address ADDRESS` | Add an smtp account that receives copies of every message from a mail server (`--address` repeatable, at least one). An address has no `+`: the part after `+` encodes the original recipient. See [Journaling with Postfix](../guide/journaling). |
| `account smtp-addresses NAME` | Show the addresses of an smtp account; `--add ADDRESS` and `--remove ADDRESS` (repeatable) change them. An address belongs to one account; the account keeps at least one. |
| `account fill NAME --host HOST --username USER` | Copy an IMAP mailbox into an smtp account, into its folder `JOURNAL`: the mail from before journaling. A message is imported when its INTERNALDATE is before the cut-off plus one hour and the account did not receive its Message-ID over SMTP. `--port`, `--tls`, `--include`/`--exclude` (repeatable), `--before` (the cut-off, RFC 3339 or `YYYY-MM-DD` for midnight UTC; default now), `--confirm-folders` (copy trash and spam too), `--password-stdin`. With `--keep-running` the fill is recurring: no cut-off, a message is imported once it is two hours old unless journaled, and after the first run here the web server runs it every `--interval` (`default`, the server's `MAIL_ARCHIVE_SYNC_INTERVAL`, or a duration of whole minutes from `5m` to `720h`, not below `MAIL_ARCHIVE_SYNC_INTERVAL_MIN`); `--before` and `--keep-running` exclude each other. On an existing fill: `--keep-running` switches an ended fill on (asks for the password; its first run scans the fill's folders again and imports what the cut-off left out, after copying the folders of a fill from before `JOURNAL` into it), `--interval D` alone changes the interval of a recurring fill, `--cancel` stops a running or recurring fill, `--resume` continues a failed, partial, cancelled or interrupted fill or resumes a stopped recurring one. `--fill ID` chooses the fill; without it, `--cancel` and `--interval` take the only fill that runs or keeps running (and list the IDs if there are several), `--resume` and `--keep-running` the newest fill. Without flags it lists the fills, with the schedule, health and waiting messages of recurring ones. `--prefix` is no longer accepted; fills started before still finish in their `PREFIX/<IMAP folder>` folders. Exit code `1` when some folders failed. See [Journaling](../guide/journaling#bring-in-the-mail-from-before-journaling) and [Keep importing](../guide/journaling#keep-importing-recurring-fill). |
| `account list` | List accounts with owner, server, login, state (enabled, disabled, removed, removed (being deleted), import) and folder filters. For smtp accounts, SERVER is `smtp` and LOGIN lists the addresses. |
| `account folders NAME` | Connect and show which folders will be archived, with their role (trash, junk, sent, …). |
| `account set-folders NAME` | Replace the folder filters with `--include` and `--exclude`. No flags: archive all folders. |
| `account set-schedule NAME --sync-interval V` | How often the web server syncs the account: `default` (`MAIL_ARCHIVE_SYNC_INTERVAL`), `manual` (not on the schedule; `sync` still syncs it) or a duration of whole minutes from `5m` to `720h`, such as `30m`. A new value below `MAIL_ARCHIVE_SYNC_INTERVAL_MIN` is refused. Prints the interval and the next sync. |
| `account rename NAME NEW-NAME` | Rename the account; its mail moves with it. |
| `account set-password NAME` | Replace the stored password (`--password-stdin` to read it from stdin). |
| `account enable NAME`, `account disable NAME` | Include or exclude the account from automatic syncs. Archived mail is kept. A disabled smtp account answers `451`: the mail server keeps the copies queued for `maximal_queue_lifetime` (5 days by default) and then drops them. |
| `account remove NAME` | Remove the account. Archived mail is kept; see [Disabling and removing](../guide/accounts#disabling-and-removing). Refused while the account is being synced or imported into. Prints how to delete it completely with `account purge`. |
| `account purge NAME` | Delete a removed account completely: its locations, folders and the account; the name is free again. Messages also in another account stay there; messages whose last location is in this account are deleted like a retention deletion (record, attestation entry, file removed after `MAIL_ARCHIVE_RETENTION_FILE_GRACE`). Refused, naming the reason, for an account that is not removed, has a retention rule, is named by a legal hold (also a released one) or has mail covered by an active hold, is being synced, filled or imported into, or while another purge is open. Shows the locations to remove, the messages to delete and those kept (by owner and account), then asks for the number of messages to delete; `--count N` gives it instead and must match, `--dry-run` stops after the preview. Runs in batches (`--lock-timeout`, default 10 minutes, per batch); an interrupted purge resumes with the same command or when the web server starts. `--cancel` cancels an open purge: what it deleted stays deleted, the account stays removed. See [Deleting a removed account completely](../guide/accounts#deleting-a-removed-account-completely). |
| `account move NAME --to USER` | Hand the account and its archived mail to another user. Also for removed accounts; refused if the user already has an account with that name. Warns when the account has a recurring fill: it goes on importing the same mailbox, now for the new owner. |

Account changes work while the account is being synced and apply from the
next sync; only `account remove` waits for the sync to finish. A command
that finds the account changed in the meantime (by the web UI or another
command) stops without changing anything; run it again.

Accounts belong to users, and names are unique per user. Every `account`
command takes `--user USER`; it is needed when several users have an account
with that name, and for `account add` when more than one user exists (with
exactly one user, new accounts are theirs; with none yet, the first user
created gets them). `account list` and `status` show all users' accounts
unless `--user` is given.

Folder filters: with `--include`, only those folders are archived; `--exclude`
then removes folders from what is left. Names are matched case-insensitively.

## Archive

| Command | Purpose |
|---|---|
| `sync` | Copy new messages from all enabled accounts. Exits non-zero if any account failed. Accounts that are being synced elsewhere are skipped. With `MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL` set, sends the alerts that became due at the end (for up to 15 seconds; failed deliveries are retried by the next sync or the web server). |
| `sync --account NAME` | Only these accounts (repeatable), also when disabled. `--user USER` limits to one user's accounts. |
| `sync --reconcile` | Also compare every selected folder with the server now, not only those due by `MAIL_ARCHIVE_RECONCILE_INTERVAL`. A plain `sync` reconciles due folders. The output adds `gone=N back=N flags=N` for accounts that were reconciled. See [Reconcile](../guide/syncing#reconcile). |
| `status` | Distinct messages per account with its ID, SOURCE, owner and the last sync result (`--user USER` for one user). SOURCE is `snapshot (IMAP)`, `complete (SMTP)` or `import`. Import accounts show `import` instead of enabled. `GONE` counts the account's messages that are no longer on its server (`-` for import and smtp accounts). For smtp accounts, LAST SYNC is when the account last received a message. `EVERY` is how often the web server's schedule syncs an IMAP account (`6h (default)`, `30m`, `15m (min)` when the floor raised it, `manual`, or `off` when the schedule is off), `NEXT SYNC` when it does next (`due` when that has passed, `-` when disabled or not scheduled), and `HEALTH` is `ok`, `failing` or `stale` as in `/healthz/sync`; this command cannot see the web server's queue, so an account waiting there may show `stale`. `FAILED` shows `-`, or how many syncs in a row failed and since when (`4x since 2026-10-01 12:00`). The ID is what [`/healthz/sync`](../guide/operations#healthz-sync) and alerts name. A line shows the newest [attestation](#attest), another the number of [retention rules and legal holds](#retention), `off` without a key, or `overdue` when it is older than twice `MAIL_ARCHIVE_ATTEST_INTERVAL`. The last line counts the [background jobs](../guide/operations#background-jobs) per kind: `jobs: attachment_text 12 queued (oldest 3m), 1 running, 2 failed`, also `retrying` for jobs that wait for another attempt, or `jobs: none`. When the PDF reader could not be prepared in the process that ran them, a further line says `attachment text: PDF extractor unavailable for N message(s)`: the cause is in the log of the web server or worker; after fixing it and a restart, `jobs retry attachment_text`. |
| `notify test` | Send a test message to `MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL`. Prints the HTTP status (never the URL) and exits non-zero if the receiver did not accept it. See [Monitoring and alerts](../guide/operations#monitoring-and-alerts). |
| `import NAME --from PATH --format mbox\|maildir\|eml` | Import mbox files, a Maildir or a directory of `.eml` files into the import account `NAME`, created if needed, or with `--journal` into the folder `JOURNAL` of the existing smtp account `NAME`. See below. |
| `reindex` | Extract body text, To and Cc recipients, attachment names and the links between replies from messages archived by an older version, so search, its filters and conversations find them. Needed once after an upgrade that says so; see [Upgrades](../guide/operations#one-time-steps). Then it extracts the text of PDF and text attachments of every message that has none yet, in the foreground, newest first, until none is left, and prints `updated N message(s), extracted text of M attachment(s)`; `MAIL_ARCHIVE_JOB_WORKERS` (default 1; `0`, as set for a web server that leaves jobs to the worker, counts as 1 here) sets how many messages run at a time, PDFs one at a time. The web server or the worker does the same in the background; installations that run only `sync` or `import` from cron get attachment text only from `reindex`, so run it after them, for example in the same cron job. Messages that another process is extracting, or that wait for a retry, are left to it, and it says how many. Safe to interrupt and rerun, and to run alongside the web server, the worker and syncs; a second `reindex` at the same time exits with `reindex already running`. |
| `verify` | Check the whole archive: the attestations, every message file is present and matches its hash, and no files lie around without a database row. See below. |
| `export --format mbox\|maildir --out DIR` | Write archived mail for a mail client, for `--user USER`, or `--account NAME` with an optional `--folder NAME`. See below. |
| `backup DIR` | Dump the database into `DIR` with `pg_dump`, next to `BACKUP-NOTE.txt`. With Docker Compose: `./ma backup DIR`. See [Backups](../guide/operations#backups). |
| `audit --since DATE --format csv\|jsonl` | Write the [audit log](../guide/audit-log) of a period to stdout or `--out FILE`, oldest first. `DATE` is `YYYY-MM-DD`, a day in UTC, or RFC 3339 with an offset for another zone (`2026-10-01T00:00:00+02:00`). `--until DATE` (exclusive; a plain date includes the day), `--user NAME`, and `--action` with a group (`login`, `search`, `read`, `export`, `accounts`, `users`, `profile`, `api_keys`, `sync`, `retention`) or an action, repeatable. See [Writing it out](../guide/audit-log#writing-it-out). |
| `attest` | Write a signed attestation over the archived messages now. See below. |
| `attest keygen [--rotate]` | Create the key that signs attestations, or replace it. See below. |
| `attest public-key` | Print the public key attestations are verified with. |
| `retention rule add\|list\|remove` | Retention rules: how long the mail of an account or folder must be kept. See below. |
| `hold add\|list\|release` | Legal holds: no deletion of what they cover. See below. |
| `retention preview\|show\|confirm\|cancel\|runs\|sweep` | Delete mail whose retention period has ended, in a reviewed run. See below. |
| `attest check [--file FILE] [--public-key FILE] [--offline]` | Check only the attestations, fast. See below. |
| `serve [--listen ADDR]` | Run the web server: UI, JSON API, sync schedule, attestations and, unless `MAIL_ARCHIVE_JOB_WORKERS=0`, the [background jobs](../guide/operations#background-jobs). Default `127.0.0.1:8080`. |
| `worker` | Run only the background jobs, `MAIL_ARCHIVE_JOB_WORKERS` (1 to 8) at a time: no web server, SMTP, syncs, fills, attestations or retention. For a separate process with its own memory limit, with `MAIL_ARCHIVE_JOB_WORKERS=0` for `serve`; with Compose, the `worker` service. Stops on SIGTERM and hands running jobs back to the queue. Refuses `MAIL_ARCHIVE_JOB_WORKERS=0`. |
| `jobs retry KIND` | Queue the failed background jobs of `KIND` (for example `attachment_text`) again, with their attempts reset, and print how many. `status` shows how many failed. Recorded in the audit log as `jobs.retry`. |

Commands that change users or accounts, `jobs retry`, and `import`, `export`, `backup`
and `audit`, write an entry to the [audit log](../guide/audit-log) first;
if it cannot be written they do nothing. `sync`, `status`, `verify` and the
list commands are not recorded.

### import

```sh
./ma import old-laptop --from /import/thunderbird --format mbox
./ma import takeout --from "/import/All mail Including Spam and Trash.mbox" --format mbox --folder Gmail
./ma import old-server --from /import/Maildir --format maildir --dry-run
./ma import piler --from /import/piler-export --format eml --folder piler
./ma import journal --journal --from /import/piler-alice --format eml --label piler
```

| Flag | Default | |
|---|---|---|
| `--from PATH` | | A file or directory. With Docker Compose under `/import` (`./import` on the host, read-only). |
| `--format` | | `mbox`, `maildir` or `eml` (only files ending in `.eml`). |
| `--folder NAME` | from the files | The folder name of a single-folder source, or a prefix (`NAME/…`) for several folders. Required for `.eml` files directly in `--from`. Not with `--journal`. |
| `--user USER` | the only user | The owner of the account. |
| `--journal` | off | Import into the folder `JOURNAL` of the existing smtp account `NAME`, like a fill: no envelope, and messages whose Message-ID the account received over SMTP are left out (counted as `JOURNALED`). Source folders are merged. |
| `--label TEXT` | the `--from` path; a longer one needs `--label` | With `--journal`: the origin the message view shows ("Imported from files: TEXT"), 1 to 200 characters. |
| `--max-message-size SIZE` | `256MiB` | Larger messages are skipped; the import then exits `1` (`partial`). |
| `--dry-run` | off | List folders and message counts, store nothing. |

Running the same import again adds only what is missing. Progress goes to
stderr after every batch, a summary per folder to stdout. Exit codes: `0`
done, `1` messages skipped or an error. Import accounts are never synced:
`sync --account NAME` refuses them, and `account enable`, `disable`,
`set-password`, `set-folders` and `folders` do not apply. `rename`, `move`
and `remove` work. Without `--journal`, an smtp account is refused; with it,
anything but an existing smtp account is, and nothing is created. See
[Importing mail](../guide/importing) and
[Into an smtp account](../guide/importing#into-an-smtp-account).

### verify

`verify` first checks the [attestations](#attest), then reads every
archived message from disk and hashes it again, then checks every file in
the data directory's `messages/` and `tmp/` against the database. It
changes no database row and no archived file. Without an attestation key
and without attestations it prints `note: tamper evidence off` and works as
before.

| Finding | Meaning |
|---|---|
| `missing` | The database has the message, the file is gone. Listed with user, account, folder and UID. |
| `corrupt` | The file's content or size no longer matches its SHA-256. Listed like `missing`. |
| `bad-path` | The database points to a different path than the hash gives. |
| `orphan` | A message file without a database row, for example after an interrupted sync or a restore from an older dump. |
| `stale-temp` | A file in `tmp/` left by an interrupted sync, untouched for an hour. Younger files may belong to a delivery in progress and are skipped. |
| `unexpected` | Anything else in `messages/` or `tmp/`: symlinks, other names. Never moved. |
| `io-error` | A file that could not be read. |
| `deleted` | The file of a message deleted by [retention](#deletion) or an [account purge](#accounts) that is not scheduled for removal. `--fix-orphans` schedules it for the next sweep instead of moving it aside. Files waiting out their grace period are only a note. |
| `removed` | An attested message is gone and no deletion was recorded. Listed with its hash and `attested by #N on DATE`. |
| `unattested` | A row inside an attested range that the attestation does not list (inserted afterwards). |
| `attestation` | An attestation no longer matches the database: another message at the same number, changed deletion records, or only the counts when its leaves file is gone. |
| `attest-file` | The attestation file differs from the database or does not parse, or a leaves file is missing or does not match its signed digest. |
| `chain` | A broken chain, a bad hash or signature, or an untrusted key. |
| `attest-stale` | A key exists and the newest attestation is older than twice `MAIL_ARCHIVE_ATTEST_INTERVAL`. |

Lines starting with `note:` are no findings: the attestation file lags
behind the database, deletions not yet signed, a key rotation, a message
archived again after a restore (`re-archived`). See [Tamper
evidence](../guide/tamper-evidence#findings).

| Flag | Default | |
|---|---|---|
| `--jobs N` | 4 | Files hashed in parallel, 1 to 16. |
| `--lock-timeout D` | `1m` | The orphan check waits this long while a sync writes files, then is skipped. |
| `--fix-orphans` | off | Move orphans and `stale-temp` files to `orphans/<UTC time>/` in the data directory. Delete that directory yourself once you are sure. |
| `--public-key FILE` | | Trust only the attestation public keys in this PEM file, for example an off-host copy of `signing.pub` (repeatable). Default: `attest/signing.pub` and `attest/retired-keys.pem`. |

Exit codes: `0` nothing found, `1` findings, `2` the check is incomplete
(orphan check skipped, unreadable files, attestations but no trusted public
key, no database connection, wrong flags). Progress goes to stderr every 10,000 messages.

While the orphan check runs, it holds the blob lock, and syncs, fills,
imports and SMTP deliveries wait for it; the web UI keeps working. An SMTP
delivery that waits longer than 2 minutes gets `451`, and the mail server
retries it later.

### attest

An attestation records which messages the archive held, so that a message
removed afterwards can be noticed. Each one covers the messages archived
since the previous one, is chained to it by its hash and signed with an
Ed25519 key. It is stored in the database and appended to
`MAIL_ARCHIVE_DATA_DIR/attest/attestations.jsonl`, with a leaves file in
`attest/leaves/N.txt` that lists the SHA-256 of every message it covers. The
chain is tamper-evident, not tamper-proof: it does not stop anyone with
access to the database or the data directory from deleting mail.

```sh
./ma attest keygen        # once: creates attest/signing.key and attest/signing.pub
./ma attest               # write one now; the web server writes one every 24h
./ma attest public-key    # print the public key, for a copy off the host
```

`attest keygen` refuses if a key exists. Copy `attest/signing.pub` off the
host (password manager, printout, your auditor). `attest keygen --rotate`
moves the current public key to `attest/retired-keys.pem`, where it stays
trusted for older attestations, and creates a new key; use it when the key
was lost or may be known to others.

The web server writes an attestation every `MAIL_ARCHIVE_ATTEST_INTERVAL`
(default 24h), also when nothing changed: the daily entry shows that the
archive was checked that day. Without a key, attestations are off; the web
server still starts and logs `attestations off`.

The database and the file must agree. Entries that only the database has
(after a crash between the two writes) are copied to the file by the next
run. Entries that only the file has (a database restored from a backup older
than the file) are checked and imported, and messages archived since the
restore inside an imported range get new numbers; the web server does this
at its start too. See [Restore](../guide/tamper-evidence#restore-from-a-backup).
If they disagree in any other way, for example a changed line, `attest`
writes nothing and fails.

`attest check` runs only the attestation part of `verify`, without reading
message files:

| Flag | |
|---|---|
| `--public-key FILE` | Trust only the public keys in this PEM file (repeatable), such as the copy kept off the host. Pass the retired keys too after a rotation. |
| `--file FILE` | Compare another copy of `attestations.jsonl`, such as the off-host one, with the database. It must be a prefix of the database chain. Its leaves are read from `attest/leaves/`. |
| `--offline` | No database: check `--file` (default `attest/attestations.jsonl`) alone: chain, signatures, totals, and every leaves file in the `leaves/` directory next to it. Without `--public-key`, `signing.pub` and `retired-keys.pem` next to the file are trusted. |

Exit codes as for `verify`. See [Tamper evidence](../guide/tamper-evidence).

### retention

A retention rule keeps the mail of an account, or of one folder of it, for a
number of years after the end of the year it was received or sent in. The
date is the later of the Date header and the received date, but never later
than when the archive stored the message, so an mbox from 2015 imported in
2026 counts from 2015. Without a rule, mail is kept for ever. When several
rules match a message, the longest wins, also across other users' accounts
that hold the same message. **Rules alone delete nothing**; deletion is a
separate, reviewed step, see [Deletion](#deletion). The background, GoBD and
a template for the procedure documentation are in
[Retention and legal hold](../guide/retention).

```sh
./ma retention rule add --account work --years 8 --note "§147 AO, invoices by mail"
./ma retention rule add --account work --folder Newsletter --years 1 --force
./ma retention rule list
./ma retention rule remove 2
```

| Flag of `rule add` | Default | |
|---|---|---|
| `--account NAME` | | The account; `--user` when several users have one with that name. Removed accounts work too. |
| `--folder NAME` | all folders | One folder, matched exactly. An account has one rule for all folders and at most one per folder. |
| `--years N` | | 1 to 100. Under 6 (business letters, §147 AO) only with `--force`. |
| `--time-zone TZ` | `Europe/Berlin` | Where the year ends. |
| `--note TEXT` | | For example the legal basis. |

Rules are not changed: remove one and add another. Which period applies is a
legal question (GoBD: 6 years for business letters, 8 for accounting
vouchers such as invoices, 10 for books); check with your tax adviser. The
message view in the web UI shows when a message's period ends. `status`
shows the number of rules and active holds.

### hold

A legal hold stops the deletion of the mail it covers while it is active,
whatever the rules say: during a tax audit, a lawsuit or an investigation.

```sh
./ma hold add "Tax audit 2026" --reason "Betriebsprüfung, letter of 2026-09-01" --account work --until 2024-01-01
./ma hold add "Falcon dispute" --reason "lawsuit 12 O 345/26" --query falcon
./ma hold list [--released]
./ma hold release 1 --note "audit closed on 2026-12-01"
```

Every condition given must match: `--user`, `--account`, `--since` and
`--until` (the Date header or received date; mail without dates counts as
inside), and a search filter (`--query`, `--sender`, `--recipient`,
`--attachment`, `--has-attachment`). A filter is matched by search, as the
mail's owner sees it, when the hold is added and again before every
deletion; matches are only ever added. Until the first match, a filter hold
covers its whole scope. Without any condition the hold covers the whole
archive and needs `--all`.

Holds are released with a note, never deleted. A user named by a hold can
therefore not be removed (lock them instead), an account whose owner is on
hold cannot be moved to another user, and an empty account named by a hold
is kept as removed instead of being deleted.

### deletion

Deletion is a reviewed run, never a silent job:

```sh
./ma retention preview          # create run 3: what may be deleted now, by account, folder and year
./ma retention show 3           # every message: date, sender, subject, places, end of its period
./ma retention show 3 --format csv > run-3.csv
./ma retention confirm 3        # asks for the number of messages, then deletes
./ma retention cancel 3         # or drop the run; nothing is deleted
./ma retention runs             # past runs and their counts
```

A message is a candidate only if **every** location of it, in every user's
accounts, has a retention rule whose period has ended and no active legal
hold. A location without a rule keeps the message for ever. Only one run can
be open at a time. To drop the locations of a removed account without rules,
use [`account purge`](#accounts).

`confirm` deletes in batches of 500. Each batch waits for the blob lock
(`--lock-timeout`, default 10 minutes, while syncs and imports write files)
and checks every candidate again: a rule removed, a legal hold added or a new
location since the preview keeps it (`kept`). For every deleted message it
writes a record (hash, size, dates, number of locations; no subject, sender
or recipients) and a deletion entry that the next
[attestation](#attest) signs, and deletes its locations and row. An
interrupted run resumes with `confirm` again, or when the web server starts.

The message files stay for `MAIL_ARCHIVE_RETENTION_FILE_GRACE` (default 7
days), so a backup made meanwhile has every file its dump needs and
restoring the last dump undoes a wrong deletion. The web server removes due
files every hour; `retention sweep` does it by hand. A file whose message
was archived again stays.

A deleted message is not archived again where it would already be expired:
a sync after a `UIDVALIDITY` change, a copy in another selected folder, an
import or an SMTP delivery skips it (`sync` prints `deleted=N`, `import` a
line, and SMTP answers 250). In an account without a rule, or with a longer
one, it is archived again, as if that location had existed at deletion.

After restoring a dump older than a run, its messages come back as rows;
once their files are removed, `verify` reports them as `missing` and
`export` skips them as "deleted by retention". Run `retention preview`
again.

### export

```sh
./ma export --user anna --format mbox --out /data/export/anna
./ma export --account private --folder INBOX --format maildir --out /data/export/inbox
```

Writes one mbox file (`DIR/<account>/<folder>.mbox`, mboxrd) or one Maildir
(`DIR/<account>/<folder>/{cur,new,tmp}`) per folder. Each message is written
once per folder, byte for byte; the mbox adds a final newline where one is
missing. Removed accounts are included, mail synced during the export is not.
`DIR` must not exist; it appears when the export is complete. Characters that
cannot be part of a file name (`/`, `\`, `%`, control characters, a leading
`.`) are written as `%XX`.

One export never mixes users: give `--user`, or `--account` (with `--user`
when several users have an account with that name). With Docker Compose,
`DIR` must be under `/data`, which is `./data` on the host. Exits `1` if
messages were skipped because their file is missing or corrupt (run `verify`).
See [Exporting mail](../guide/operations#exporting-mail).

