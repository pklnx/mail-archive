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
| `migrate` (or `migrate up`) | Create or upgrade the database schema. With `./ma`, it also creates the data directory and makes it writable for the container. On a database created by the `postgres:18-alpine` image (26.10-6 and earlier), it first rebuilds all database indexes once; until then the other commands that use the database (`serve` included, `migrate status` excepted) refuse to run. This index rebuild is not [`reindex`](#archive), which extracts message text for search. Waits for another running `migrate`, and after a killed `migrate` for goose's migration lock; see below. |
| `migrate status` | Show applied and pending migrations. Does not wait for a running migration. |
| `migrate down --yes` | Roll back the latest migration. Usually deletes data; for development. Waits for another running `migrate`, also during its one-time index rebuild, and, like `migrate`, for goose's migration lock. Refuses, like the other commands, while no `migrate` has done that rebuild. |
| `keygen` | Print a random secret key for `MAIL_ARCHIVE_SECRET_KEY` (not the attestation key, see [`attest keygen`](#attest)). |

### migrate

Two `./ma migrate` (or `migrate down`) runs at the same time wait for each
other: the second prints `another migrate is running; waiting for it` and
starts once the first is done, also after a long index rebuild. Ctrl-C stops
the wait, at once as a rule; if the database server cannot be reached at that
moment, the command needs up to 15 seconds to end, and further Ctrl-C does
nothing meanwhile. The lock covers the whole command: for `migrate`, the
index rebuild, the migrations, the audit log partitions and the password
upgrade; for `migrate down`, its index rebuild check, the audit log
partitions and the rollback. `./ma migrate status` and `./ma backup` do not
wait for a running migration; a backup taken meanwhile records the last
committed migration (see [Backups](../guide/operations#backups)).

If a `migrate` is killed (SIGKILL, a crashed container) during a long
migration statement, the server ends that statement about 10 seconds after
the process died, or about 2 minutes after its host vanished (over a TCP
connection), with PostgreSQL 14 or later on Linux, as in the bundled image.
Until then the statement still holds goose's migration lock, and the next
`migrate` or `migrate down` waits for it and says so:
`waiting for goose's migration lock, held by database backend 12345 (active for 3m12s): ...`.
The wait has no time limit; Ctrl-C stops it (same 15 second limit). If no
`migrate` runs anywhere, the message names the `pg_cancel_backend` and
`pg_terminate_backend` calls that stop the holder. Statements of the killed
command outside goose's lock (the index rebuild, the audit log partitions,
the password upgrade) are not waited for: within those 10 seconds a new
command can start next to such a statement. The web server is not covered:
it neither waits for nor blocks a migration, so stop it before `./ma migrate`
when the release notes say so (see [Upgrades](../guide/operations#upgrades)).

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
| `account fill NAME --host HOST --username USER` | Copy an IMAP mailbox into an smtp account, into its folder `JOURNAL`: the mail from before journaling, once up to a cut-off or, with `--keep-running`, recurring. See below. |
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

### account fill

```sh
./ma account fill journal --host imap.example.com --username alice@example.com --exclude Junk
./ma account fill journal --host imap.example.com --username alice@example.com --exclude Junk \
  --keep-running --interval 12h
./ma account fill journal                    # list the fills
./ma account fill journal --interval 24h     # change the interval of the recurring fill
./ma account fill journal --cancel           # stop the running or recurring fill
./ma account fill journal --resume           # continue or resume it
```

| Flag | Default | |
|---|---|---|
| `--host HOST`, `--username USER` | | The IMAP server and login of a new fill. `--port` and `--tls tls\|starttls\|none` (default `tls`) as for `account add`. |
| `--password-stdin` | asked for | Read the password from stdin. It is kept, encrypted with `MAIL_ARCHIVE_SECRET_KEY`, only while the fill runs; for a recurring fill as long as it keeps running, like the password of an IMAP account, and deleted when the fill is stopped. |
| `--include FOLDER`, `--exclude FOLDER` | all folders | Which folders to copy (repeatable; see [Folder filters](#accounts)). |
| `--confirm-folders` | off | Copy trash and spam folders too. |
| `--before DATE` | now | The cut-off of a one-time fill: RFC 3339, or `YYYY-MM-DD` for midnight UTC. A message is imported when its INTERNALDATE is before the cut-off plus one hour and the account did not receive its Message-ID over SMTP. Not with `--keep-running`. |
| `--keep-running` | off | A recurring fill: no cut-off, and after the first run here the web server runs it every `--interval`. On an existing fill, switches an ended fill on: the password is asked for again; the first run scans the fill's IMAP folders again from the start and imports what the cut-off left out, what the fill imported before is recognised without downloading it again. A fill from before `JOURNAL` first copies the messages of its own folders into `JOURNAL`, as locations, without storing any message file again; its old folders stay. |
| `--interval D` | `default` | How often a recurring fill runs: `default` (`MAIL_ARCHIVE_SYNC_INTERVAL`, 6 hours unless set) or a duration of whole minutes from `5m` to `720h`, not below `MAIL_ARCHIVE_SYNC_INTERVAL_MIN`. Alone, it changes the interval of a recurring fill. The next run is due that long after the last one started. With the schedule off (`MAIL_ARCHIVE_SYNC_INTERVAL=0`) there are no scheduled runs, and no fill is stale; a fill just started, switched on, resumed or interrupted still finishes its current run. The fills of all accounts share one queue in the web server, one at a time; a long one-time fill delays the recurring runs behind it. |
| `--cancel` | | Stop a running or recurring fill (after its current batch if it runs) and delete its password. What it imported stays. |
| `--resume` | | Continue a failed, partial, cancelled or interrupted fill, or make a stopped recurring one keep running again; asks for the password. A new password for a fill that fails at login works the same way: `--cancel`, then `--resume` with the new password. |
| `--fill ID` | see text | Which fill. Without it, `--cancel` and `--interval` take the only fill that runs or keeps running (and list the IDs if there are several), `--resume` and `--keep-running` the newest fill. |
| `--prefix` | | No longer accepted; fills started before still finish in their `PREFIX/<IMAP folder>` folders. |

Without flags it lists the fills, with the schedule, health and waiting
messages of recurring ones. The first run happens in the foreground and
prints the first message the account received over SMTP and then the
progress per folder; exit code `1` when some folders failed. The web UI
offers the same on the account's page; see
[Journaling](../guide/journaling#bring-in-the-mail-from-before-journaling)
and [Keep importing](../guide/journaling#keep-importing-recurring-fill). A
recurring fill behaves as follows.

**Which messages.** A recurring fill has no cut-off. A message is imported
when the account did not receive its Message-ID over SMTP **and** its
INTERNALDATE is at least **two hours** old. A younger message waits for the
next run: its journal copy may still be in the mail server's queue. A message
without a Message-ID cannot be matched with a journal copy; it is imported
once it is two hours old (a duplicate beats a gap). A date more than a day in
the future, from a server with a wrong clock, counts as old.

**After a restart.** The web server starts no run of a recurring fill in the
first 70 minutes after it started: while it was down, Postfix kept the
journal copies queued and retries them at least every 4000 seconds, and they
must arrive before the fill sees the mailbox copies. The account's page shows
such a run as waiting. Re-enabling a disabled smtp account starts no such
hold: the copies Postfix kept queued while the account answered `451` arrive
within about an hour, but an overdue recurring fill runs within a minute and
may import their mailbox copies first. To avoid that, stop the fill before
you enable the account and resume it an hour later.

**Incremental.** Each run continues every IMAP folder after the last UID the
previous run decided, like a sync. It stops at the first message that is
still waiting, so that one and everything after it are looked at again by
the next run: journaled ones cost only their header, imported ones are
recognised without downloading them. When a folder's `UIDVALIDITY` changes,
it is scanned again from the start.

**Moves and deletions are ignored.** A message moved from `INBOX` to
`Clients/Miller` keeps its Message-ID, INTERNALDATE and size; the fill knows
it imported it and does not download it again, and `JOURNAL` holds it once.
A message deleted in the mailbox stays in the archive; nothing is marked
*only in archive*, and no loss alert is sent. A folder deleted in the mailbox
is simply not visited; a renamed one is scanned like a new folder, without
downloading what the fill imported before.

**New folders** are picked up, subject to the fill's include and exclude
lists. A trash or spam folder that appears later is skipped, because nobody
confirmed it: the account's page lists it as such, and the command line shows
it as *not confirmed*. It is filled only if it was confirmed when the fill
started or was switched on, or if the fill's include list names it.

**Counts.** The account's page and the list show how often the fill runs,
when next, how the last run ended and its counts: imported and *waiting*
(younger than two hours). The counts are those of the last run, not of all
runs. Runs are not written to the [audit log](../guide/audit-log), like
scheduled syncs; starting, changing, stopping and resuming a fill are.

**Health and alerts.** A recurring fill that keeps failing, for example
because the mailbox's password changed, is noticed like a sync: after
`MAIL_ARCHIVE_ALERT_AFTER_FAILURES` failed runs in a row it is *failing*,
after two intervals without a successful run *stale*. The banner names the
account with the mailbox, such as "journal (alice@example.com)",
[`/healthz/sync`](../guide/operations#healthz-sync) lists the smtp account's
ID, and the [webhook alert](../guide/operations#webhook-alerts) names the
fill and its mailbox. Fills of a disabled account do not run, not even a run
that was about to start, and count as healthy; they run again once the
account is enabled.

**Moving the account** to another user (`account move`) keeps the fill
running: it goes on importing the same mailbox, now into the new owner's
account. The command warns about it; stop the fill if that is not wanted.

**Retention.** A journal copy that retention deleted no longer counts as
journaled. If the fill sees the mailbox copy of such a message again (after a
`UIDVALIDITY` change or a move into a new folder), it imports it once. Its
retention has usually ended too, so the next deletion run lists it.

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

