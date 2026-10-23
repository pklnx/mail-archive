# Backups and upgrades

## Backups

A complete backup has three parts, made in this order:

1. **The database:**
   ```sh
   ./ma backup ./backups
   ```
   This writes `backups/mailarchive-<time>.dump` (made with `pg_dump` inside
   the database container, so it always matches the server version) and
   `backups/BACKUP-NOTE.txt` with the restore steps. Without Docker Compose,
   `mail-archive backup DIR` does the same with the `pg_dump` in `PATH`.
2. **The archive directory** (`data/`, or `ARCHIVE_DIR`): the `.eml` files.
   Copy it **after** the dump. Files are only added, so a copy made later
   covers every message in the dump.
3. **The secret key** from `.env` (`MAIL_ARCHIVE_SECRET_KEY`), kept
   separately, for example in a password manager. It is not in the dump or
   the note. Without it, archived mail stays readable, but every IMAP
   password and every 2FA setup has to be entered again.

With an [attestation key](./tamper-evidence), `backup` first writes an
attestation, so that the dump holds the newest one. The data directory, and
so every backup, contains the private signing key in `data/attest/`. The
copies of `attestations.jsonl` and `signing.pub` kept off the host are
separate from the backup; see [Tamper evidence](./tamper-evidence#what-you-do).

Files are only added, with one exception: the files of messages deleted by
[retention](../reference/cli#deletion) are removed after a grace period
(`MAIL_ARCHIVE_RETENTION_FILE_GRACE`, default 7 days). A backup made within
it is complete; keep the period longer than the time between a dump and the
copy of the data directory.

The `.eml` files are **not** encrypted on disk, and neither are the dumps.
Keep both on an encrypted volume; FileVault on macOS does this by default.

The files alone do not record where each message was found or which
accounts exist, so back up both the files and the database.

A nightly backup with cron, which also deletes dumps older than 30 days and
checks the archive once a week:

```sh
30 2 * * * cd /path/to/mail-archive && ./ma backup ./backups && rsync -a --delete ./data/ /backup/mail-archive-data/ && find ./backups -name 'mailarchive-*.dump' -mtime +30 -delete
0 4 * * 0 cd /path/to/mail-archive && ./ma verify > /tmp/mail-archive-verify.log 2>&1 || echo "mail-archive verify: see /tmp/mail-archive-verify.log"
```

On Linux the files in `data/` belong to UID 65532 (the container user), so
copy them as root or with `sudo`.

### Checking the archive

`./ma verify` checks the attestations, hashes every archived file again and
looks for files without a database row. Exit code `0` means all is well; see
the [command reference](../reference/cli#verify) for the findings.

- **`missing` or `corrupt`:** the listed messages are damaged. Restore their
  files from a backup of the data directory; the path is the same there. A
  sync does not fetch them again, because it only fetches messages newer than
  the last one it archived. Without a backup, the message's details and
  search text stay, but it cannot be opened or exported.
- **`orphan` or `stale-temp`:** usually left by an interrupted sync. A later
  sync adopts orphans that are still on the server.
  `./ma verify --fix-orphans` moves the rest to `data/orphans/<time>/`;
  delete that directory once you are sure nothing in it is needed.
- **`removed`, `unattested`, `attestation`, `attest-file`, `chain`:** an
  attested message is gone or the chain was changed. See [Tamper
  evidence](./tamper-evidence#findings). After a restore, `removed` lists
  the messages the restore lost.
- **`attest-stale`:** the web server has not written an attestation for
  twice `MAIL_ARCHIVE_ATTEST_INTERVAL`; look at its log.

### Restore

1. Put `MAIL_ARCHIVE_SECRET_KEY` back into `.env` and copy the data directory
   back to `data/` (or `ARCHIVE_DIR`). On Linux the files must belong to UID
   65532 again: `sudo chown -R 65532:65532 data` (for rootless Docker, see
   [Linux hosts](./getting-started#linux-hosts)).
2. Load the dump into the database, which replaces what is there:
   ```sh
   docker compose up -d --wait postgres
   docker compose exec -T postgres pg_restore --clean --if-exists --no-owner \
     -U mailarchive -d mailarchive < backups/mailarchive-<time>.dump
   ```
3. `./ma migrate`, then `./ma attest` if attestations are on (it imports
   the attestations written after the dump, before anything archives; see
   [Restore](./tamper-evidence#restore-from-a-backup)), then `./ma sync`,
   then `./ma verify`.

Files newer than the dump show up as orphans. The sync adopts those still on
the server. Others came from mail deleted on the server since the dump: they
are the only copy, so look at them before `--fix-orphans` moves them aside.

## Exporting mail

`export` writes archived mail in a format mail clients import:

```sh
./ma export --user anna --format mbox --out /data/export/anna
```

With Docker Compose the output directory must be under `/data`; the example
writes to `./data/export/anna` on the host. On Linux the files belong to
UID 65532: `sudo chown -R "$USER" data/export/anna` makes them yours.

| Format | Result | Import |
|---|---|---|
| `mbox` | One `.mbox` file per folder: `anna/<account>/<folder>.mbox` | Thunderbird: add-on *ImportExportTools NG*, then *Import mbox file*. Apple Mail: *File, Import Mailboxes*, *Files in mbox format*. |
| `maildir` | One Maildir per folder with read, answered and flagged marks | Mail servers (Dovecot) and clients such as mutt or NeoMutt. |

Exports are **not encrypted**; the files are readable only by their owner.
Delete them once imported. `verify` ignores the `export/` directory.

## Upgrades

Back up first (see [Backups](#backups)) and read the release notes of every
version you skip: they list database migrations and manual steps. Then, in
the directory that holds your `mail-archive/` directory:

```sh
curl -fsSL https://github.com/pklnx/mail-archive/releases/latest/download/mail-archive.tar.gz | tar -xz
cd mail-archive
./ma migrate              # apply new database migrations with the new image
docker compose up -d web  # restart the web server with the new image
./ma --version            # the version you are running
```

The new bundle replaces `docker-compose.yml`, `./ma`, `.env.example` and
`README.txt` and keeps `.env` and `data/`. Compare `.env.example` with your
`.env` for new settings. A specific version instead of the latest:
`https://github.com/pklnx/mail-archive/releases/download/v26.10-1/mail-archive.tar.gz`.

With a clone of the repository (see [From source](./getting-started#install)),
`git pull` replaces the download, and `./ma migrate` rebuilds the image for
every service, so the web server and a cron job that uses `docker compose
run` both run the new version afterwards.

Two `./ma migrate` (or `migrate down`) runs at the same time wait for each
other: the second prints `another migrate is running; waiting for it` and
starts once the first is done, also after a long index rebuild. Ctrl-C stops
the wait. The lock covers the whole command (the index rebuild, the
migrations, the audit log partitions and the password upgrade).
`./ma migrate status` and `./ma backup` do not wait for a running migration.
If a `migrate` is killed during a long statement (an index rebuild, for
example), PostgreSQL keeps its locks until that statement ends, so the next
`migrate` waits until then.
The web server is not covered: it neither waits for nor blocks a migration,
so stop it before `./ma migrate` when the release notes say so.

### Versions

Versions are named after the year and month of the release, plus a build
number that starts at 1 each month: `26.10-1` is the first release of
October 2026, `26.10-2` the second, `26.11-1` the first of November. They
are tagged `v26.10-1` in the repository. Each release publishes
`ghcr.io/pklnx/mail-archive` for `linux/amd64` and `linux/arm64` with three
tags:

| Tag | Points to |
|---|---|
| `26.10-1` | Exactly this build. The bundle's `docker-compose.yml` uses it. |
| `26.10` | The newest build of that month. |
| `latest` | The newest release. |

Set `MAIL_ARCHIVE_IMAGE` in `.env` to use another tag or your own build;
pinning an exact version is safer than `latest`, because migrations only run
with `./ma migrate`. The images carry a provenance attestation and an SBOM.

Pull requests that change the database schema carry the label
`db-migration`. Back up the database before you upgrade past one of them.

`./ma migrate status` lists applied and pending migrations.

### One-time steps

| When upgrading from | Run |
|---|---|
| A version without full-text search | `./ma reindex` once, so older messages become searchable. |
| A version without recipient and attachment search | `./ma reindex` once, so `to:`, `attachment:` and `has:attachment` find older messages. The migration itself is quick. Reindex reads every stored message file and updates every message row, so it takes a while on large archives and logs its progress; it can run while the web server and syncs run, and can be interrupted and run again. The table needs about twice its space until autovacuum has cleaned up; on large archives, `docker compose exec postgres psql -U mailarchive -c "VACUUM (ANALYZE) messages"` afterwards makes the space reusable right away. `./ma migrate` and the web server point out when messages are still pending. |
| A version without conversations | `./ma reindex` once, also if you ran it for recipient and attachment search. Until then older messages are not grouped and show no conversation. Reindex extracts everything again and rewrites every message row, the full-text index included: about 6 ms per message (10,000 messages of 8 KB text took 62 seconds in a test container with PostgreSQL 16), so roughly 10 minutes per 100,000 messages. The same notes as above apply: it can run alongside the web server and syncs, can be interrupted, and `VACUUM (ANALYZE) messages` afterwards helps large archives. |
| A version without login | `./ma user add NAME --admin` once; until then the web UI only shows how to do it. This first user gets all existing accounts. |
| A version without separate users | Nothing. Existing accounts belong to the oldest admin; hand some to other users with `./ma account move NAME --to USER`. `./ma migrate` also encrypts the stored IMAP passwords again, bound to the account ID instead of the name (needs `MAIL_ARCHIVE_SECRET_KEY`; the web server does the same when it starts). Older versions cannot read these passwords: going back means entering them again with `account set-password`. |
| A version without 2FA | Nothing to run. Every admin must set up TOTP at the next web login and needs an authenticator app for it. Recovery codes and TOTP secrets depend on `MAIL_ARCHIVE_SECRET_KEY`: after changing the key nobody with 2FA can log in until `./ma user reset-2fa NAME` resets it. |
| A version without sync alerts | Nothing to run. The migration counts each account's failed syncs since its last success. With `MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL` set, accounts that are already failing are announced once after the upgrade. |
| A version without reconcile | Nothing to run. The migration only adds columns and an index. The first sync after the upgrade compares every folder with the server (one UID and flag listing per folder, no bodies; see [Reconcile](./syncing#reconcile)). Messages deleted on the server before the upgrade are found by it too. |
| A version without loss alerts | Nothing to run. The migration adds an empty table. |
| A version with the journal folder `INBOX` | Stop the web server before `./ma migrate` (`docker compose stop web`): the migration renames the journal folder of every smtp account from `INBOX` to `JOURNAL` and records for every location of a fill which fill it came from, which locks the message locations table while it runs. Postfix keeps the copies queued meanwhile and delivers them when the web server is back; a web server of the old version that still runs after the migration answers them with `451` too. Retention rules on `INBOX` of smtp accounts are renamed with the folder; a rule on `JOURNAL`, which matched nothing, is removed and logged. Afterwards, links and scripts that name the folder `INBOX` of an smtp account (`./ma export --folder INBOX`, the API's `folder`, saved URLs) need `JOURNAL`, and `./ma account fill --prefix` and the API's `folderPrefix` are refused: fills write into `JOURNAL`. See [Journaling](./journaling#bring-in-the-mail-from-before-journaling). |
| A version without recurring fills | Nothing to run. The migration adds columns and an empty table; existing fills stay one-time fills. Switch one on in the web UI or with `./ma account fill NAME --keep-running` (the password is asked for again); see [Keep importing](./journaling#keep-importing-recurring-fill). Rolling the migration back stops recurring fills and deletes their passwords; what they imported stays. |
| A version without imports into smtp accounts | Nothing to run; the migration adds a table, a column and an index and reads every message location once. See [Into an smtp account](./importing#into-an-smtp-account). |
| A version with `postgres:18-alpine` in `docker-compose.yml` (26.10-6 and earlier) | `./ma migrate` rebuilds the database indexes once, before it applies the migrations; stop the web server first (`docker compose stop web`) and pause cron jobs that run `./ma`, then `./ma migrate` and `docker compose up -d web`. It prints `rebuilding all database indexes once` and takes about 15 seconds per 30,000 messages. This index rebuild is not `./ma reindex`, which extracts message text for search. Until it has run, the other `./ma` commands that use the database (except `./ma migrate status`) and the web server refuse to start with `the database needs a one-time index rebuild`. That includes `./ma backup`, so back up with the old bundle before you extract the new one. Installations that already ran the `REINDEX` by hand get one more rebuild; it is harmless. The bundle now uses the Debian-based `postgres:18` image, whose full-text search is more than twice as fast for less frequent words. It reads the existing volume as is, but sorts text differently: both images say `en_US.utf8`, yet the Alpine image (musl) sorts by code point and the Debian image (glibc) by language rules. Indexes on text columns built by the old image are in the wrong order for the new one, so lookups can miss rows and unique checks can miss duplicates until they are rebuilt. PostgreSQL does not warn about it, because the Alpine image records no collation version; `./ma migrate` recognizes such a database by exactly that (no recorded collation version, while the server reports one) and records the rebuild in the database setting `mail_archive.collation_reindexed`. If the collation version changes later (a newer glibc in a later `postgres:18` image), `./ma migrate` rebuilds again. If the rebuild stops with a duplicate key error, something wrote to the database before it ran: the message names the index and the key; restore the backup from before the upgrade (see [Restore](#restore)). Nothing is migrated then. If the database user is no superuser (the bundle's `mailarchive` is one), the rebuild works but recording it fails; the message then gives the `ALTER DATABASE ... SET mail_archive.collation_reindexed = '...'` command to run as a superuser, and the other commands refuse until it has run. For reference, the rebuild by hand, as a superuser: `docker compose exec -T postgres psql -U mailarchive -d mailarchive -c "REINDEX DATABASE mailarchive"` (without the `ALTER DATABASE` from the message, `./ma migrate` does not know it ran and rebuilds once more). `ALTER DATABASE ... REFRESH COLLATION VERSION` is not needed and fails with `invalid collation version change`; only a restore from a dump into a new volume records the collation version, so that PostgreSQL warns after later glibc changes. |
| A version without passkeys | Nothing to run. To use passkeys, set `MAIL_ARCHIVE_PUBLIC_URL` to the address you open in the browser (Compose defaults to `http://localhost:WEB_PORT`). Passkeys are bound to its host name: after changing it, remove the passkeys (`./ma user remove-passkeys NAME`) and register them again. |

## Monitoring and alerts

An account whose syncs fail (a changed password, a provider that blocks the
login, an expired app password) loses mail until someone notices. Three
things make it visible:

- **The web UI** shows a banner above the mail when one of your accounts
  keeps failing or has not synced for a while; see [Web UI](./web-ui#sync-problems).
- **An alert** through a webhook: one message when an account's syncs keep
  failing, one when they work again. See [Syncing](./syncing#alerts) for
  what counts. The same goes for the runs of a
  [recurring fill](./journaling#keep-importing-recurring-fill) of an smtp
  account. Another alert reports mail deleted on the server in bulk;
  see [Reconcile](./syncing#alert-for-mail-deleted-on-the-server).
- **`/healthz/sync`** for a monitoring tool such as Uptime Kuma.

### Webhook alerts

Set the URL in `.env` and restart the web server
(`docker compose up -d web`). Alerts are sent by the web server, and at the
end of `./ma sync`, so a cron setup without the web server gets them too.
Check the settings with:

```sh
./ma notify test
```

It sends a test message, prints the HTTP status and exits non-zero if the
receiver refused it.

**ntfy** (app for Android and iOS, or self-hosted):

```sh
MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL=https://ntfy.sh/mail-archive-k3v9q2x7w1
MAIL_ARCHIVE_NOTIFY_WEBHOOK_FORMAT=ntfy
```

Anyone who knows a topic on the public ntfy.sh server can read it, and the
alerts name your accounts. Use a long random topic, or better a protected
topic with an access token:

```sh
MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION=Bearer tk_...
```

A self-hosted ntfy on the LAN works too (`http://192.168.1.5:2586/alerts`);
plain `http` to another machine logs a warning at start, because the alert
and the token travel unencrypted.

**Gotify:** `https://gotify.example.org/message` with the format `json` and
`MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION=Bearer <app token>` (or
`?token=<app token>` in the URL).

**Slack, Mattermost, Discord:** create an incoming webhook and use its URL
with the format `json`. They show the `text` (Slack, Mattermost) or `content`
(Discord) field.

**Anything else:** the `json` format posts:

```json
{
  "id": "3-1759320000-failing",
  "event": "failing",
  "title": "Mail archive: sync of work keeps failing",
  "message": "Account \"work\" (owner anna, ID 3) failed 3 syncs in a row since 2026-10-01 12:00 UTC.\nLast error: …",
  "text": "…", "content": "…",
  "accounts": [{"id": "3-1759320000-failing", "event": "failing", "accountId": 3, "account": "work",
                "owner": "anna", "failureStreak": 3, "failingSince": "2026-10-01T12:00:00Z", "lastError": "…"}]
}
```

`event` is `failing`, `recovered`, `mixed` (alerts and recoveries in one
message), `gone` (mail deleted on the server) or `test`.

A [recurring fill](./journaling#keep-importing-recurring-fill) whose runs
keep failing, for example because the mailbox's password changed, is
announced the same way, after `MAIL_ARCHIVE_ALERT_AFTER_FAILURES` failed
runs, and again when a run succeeds. Its entry names the smtp account and
adds `fillId` and `mailbox`, the IMAP user name it imports from:

```json
{
  "id": "fill-7-1759320000-failing",
  "event": "failing",
  "title": "Mail archive: import of ak@example.com into journal keeps failing",
  "message": "Import of ak@example.com into account \"journal\" (owner anna, fill 7) failed 3 runs in a row since 2026-10-01 12:00 UTC.\nLast error: …",
  "text": "…", "content": "…",
  "accounts": [{"id": "fill-7-1759320000-failing", "event": "failing", "accountId": 4, "account": "journal",
                "owner": "anna", "failureStreak": 3, "failingSince": "2026-10-01T12:00:00Z", "lastError": "…",
                "fillId": 7, "mailbox": "ak@example.com"}]
}
```

Its recovery has the title "Mail archive: ak@example.com imports into
journal again". Syncs and fills never share a message.

A `gone` message looks like this:

```json
{
  "id": "gone-812",
  "event": "gone",
  "title": "Mail archive: 1,234 messages deleted on the server (personal)",
  "message": "Account \"personal\" (owner anna, ID 3): 1,234 of 5,000 messages are no longer on the server. They stay in the archive.\nFolders: INBOX 1,200, Archive 34.",
  "text": "…", "content": "…",
  "accounts": [{"id": "gone-812", "event": "gone", "accountId": 3, "account": "personal", "owner": "anna",
                "failureStreak": 0, "failingSince": null, "lost": 1234, "presentBefore": 5000,
                "folders": [{"name": "INBOX", "lost": 1200}, {"name": "Archive", "lost": 34}]}]
}
```

Its `id` names the sync run that found the loss. `folders` lists at most 20
folders, most first; `moreFolders` counts the rest. With ntfy it has the
priority `high` and the tag `wastebasket`. Sync alerts and `gone` alerts
never share a message. Several accounts that change at the same time go into one
message (up to 50 per request). The `id` is also sent as the
`X-Mail-Archive-Id` header.

Alerts are delivered **at least once**: if the server stops right after the
receiver accepted a message, it is sent again with the same `id`. ntfy, Gotify
and Slack do not drop such repeats; a receiver of your own can use the `id`.
A receiver that is down is retried after 30 seconds, then with growing
pauses up to an hour, and given up after 24 hours (logged as
`notification given up`; a `gone` alert 24 hours after its run). An answer like `400` or `404` usually means a wrong
URL, token or format: it is logged as an error and retried hourly. Redirects
are not followed.

The log records each alert (`alert sent`, `recovery sent`, `loss alert sent`)
with the account and its owner. It never contains the webhook URL, the authorization or the
message; failed attempts name only the scheme and host.

### /healthz/sync

`GET /healthz/sync` needs no login and answers:

| Status | Body | Meaning |
|---|---|---|
| `200` | `{"status":"ok"}` | Every enabled account syncs, and every recurring fill runs. |
| `503` | `{"status":"degraded","failing":[3,7],"stale":[5]}` | Account IDs whose last `MAIL_ARCHIVE_ALERT_AFTER_FAILURES` syncs failed, or that had no successful sync within two of their sync intervals (at least one plus an hour); see [Alerts](./syncing#alerts). An smtp account is listed when one of its recurring fills fails or is stale the same way. |
| `503` | `{"status":"unavailable"}` | The database cannot be reached. |

Disabled, removed and import accounts are left out, as are fills that were
stopped; with the schedule off (`MAIL_ARCHIVE_SYNC_INTERVAL=0`) nothing counts
as stale, nor does an account not on the schedule, or one that is syncing
or waiting in the queue, nor a recurring fill during a run or while it
waits for one. A running sync or fill counts from its start, and one the
web server has taken from the queue from then, also while it waits for a
verify or a deletion to finish: one that hangs, or waits behind a verify
that hangs, turns stale after the same time. A recurring fill counts as
stale from when it was switched on until its first successful run. The
answer is cached for 15 seconds. `./ma status` shows each account's ID next
to its name, in `HEALTH` the same state, and in `FAILED` how many syncs in a
row failed.

The request must use an allowed host name (`MAIL_ARCHIVE_ALLOWED_HOSTS`),
like every request. For Uptime Kuma, add an HTTP monitor for
`http://localhost:8080/healthz/sync`, or from another machine with a
host name you allowed. With curl:

```sh
curl -fsS http://localhost:8080/healthz/sync || echo "mail-archive: syncs failing"
```

Do not use `/healthz/sync` as the container's healthcheck: Docker would
restart the web server whenever an IMAP account fails, which fixes nothing.
`/healthz` only says whether the server is up and is the right check there.

## Logs

```sh
docker compose logs web --tail 100
```

Set `MAIL_ARCHIVE_LOG_LEVEL=debug` in `.env` for more detail.
