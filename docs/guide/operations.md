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

A backup taken while `./ma migrate` runs is consistent, but holds only the
migrations committed when the dump started. `BACKUP-NOTE.txt` names the last
migration committed when its text was made, before the dump started, so the
dump can hold later migrations than the note names. While a migration runs,
the dump can also wait for it or fail on its table locks. After a restore,
`./ma migrate` applies the rest. Back up before `./ma migrate`, not while it
runs.

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
  sync does not fetch them again, because it only fetches messages newer
  than the last one it archived. Without a backup, the message's details and
  search text stay, but it cannot be opened or exported.
- **`orphan` or `stale-temp`:** usually left by an interrupted sync. A later
  sync adopts orphans that are still on the server.
  `./ma verify --fix-orphans` moves the rest to `data/orphans/<time>/`;
  delete that directory once you are sure nothing in it is needed.
- **`removed`, `unattested`, `attestation`, `attest-file`, `chain`:** an
  attested message is gone or the chain was changed. See
  [Tamper evidence](./tamper-evidence#findings). After a restore, `removed`
  lists the messages the restore lost.
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
version you skip: they list database migrations and manual steps. Also check
`.env` for a `MAIL_ARCHIVE_IMAGE` line (see below). Then, in the directory
that holds your `mail-archive/` directory:

```sh
curl -fsSL https://github.com/pklnx/mail-archive/releases/latest/download/mail-archive.tar.gz | tar -xz
cd mail-archive
./ma migrate              # apply new database migrations with the new image
docker compose up -d web  # restart the web server with the new image
./ma --version            # the version you are running
```

With the [worker service](#background-jobs) on (`COMPOSE_PROFILES=worker`),
restart it too: `docker compose up -d web worker` instead of
`docker compose up -d web`, here and in the steps below.

`docker compose up -d web`, `docker compose stop` and `docker compose restart`
give the web server up to 45 seconds to shut down. It ends syncs and fills
right away and records how far they got, so they resume at the next run.
Open HTTP requests get 10 seconds and SMTP connections (journaling) 30
seconds; mail still in transfer is stored or, if it is cut off, retried by
the sending mail server. Compose commands that recreate or restart `postgres`
together with `web` (`docker compose up -d`, `docker compose restart postgres`)
restart `web` too, so its start-up checks run again; `./ma`
starts only `postgres`.

The new bundle replaces `docker-compose.yml`, `./ma`, `.env.example` and
`README.txt` and keeps `.env` and `data/`. Compare `.env.example` with your
`.env` for new settings. A specific version instead of the latest:
`https://github.com/pklnx/mail-archive/releases/download/v1.0.0/mail-archive.tar.gz`.

A `MAIL_ARCHIVE_IMAGE` line in the kept `.env` (see [Versions](#versions))
overrides the new bundle's image: the web server and the CLI (`./ma`) go on
using the image it names, and `./ma --version` shows that image's version.
Check it before `./ma migrate`:

- Keep the line if it names a newer build of your own than the new release.
- Otherwise, if it names an `X.Y` tag of `ghcr.io/pklnx/mail-archive`, set it to the
  new release's `X.Y` tag, for example `ghcr.io/pklnx/mail-archive:1.2`, and
  run `docker compose pull web` (it restarts nothing; migrate before starting
  web): a tag that is already on the machine is not pulled again.
- In any other case, remove the line or set it to the new release's exact
  image, for example `ghcr.io/pklnx/mail-archive:X.Y.Z`.

If you ran the steps above with the line in place, change it as described
and repeat the upgrade from `./ma migrate` on, with the manual steps from
the release notes and the [One-time steps](#one-time-steps) in their order,
then run `docker compose up -d web`.

With a clone of the repository (see [From source](./getting-started#install)),
`git pull` replaces the download, and `./ma migrate` rebuilds the image for
every service, so the web server and a cron job that uses `docker compose
run` both run the new version afterwards.

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
committed migration (see [Backups](#backups)).

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
when the release notes say so.

### Versions

Versions follow [semantic versioning](https://semver.org/), `X.Y.Z`, from
`1.0.0` on:

- a **major** release (`2.0.0`) has a breaking change that needs manual steps
  when upgrading (see the release notes);
- a **minor** release (`1.5.0`) adds features and may change the database
  schema; then it needs `./ma migrate`;
- a **patch** release (`1.4.3`) normally has fixes only, without a migration
  or a breaking change.

The maintainer can release with a lower version than the labels propose, so
read the release notes: they list database migrations and breaking changes
under their own headings.

Releases are tagged `v1.0.0` in the repository. Each release publishes
`ghcr.io/pklnx/mail-archive` for `linux/amd64` and `linux/arm64` with four
tags:

| Tag | Points to | Brings |
|---|---|---|
| `1.4.2` | Exactly this build. The bundle's `docker-compose.yml` uses it. | Nothing new. |
| `1.4` | The newest `1.4.x` release. | Patch updates: normally no migration and no breaking change. |
| `1` | The newest `1.x.y` release. | Features, and migrations when there are any, so run `./ma migrate` after each update; normally no breaking change. |
| `latest` | The newest release. | Everything, breaking changes included. |

Set `MAIL_ARCHIVE_IMAGE` in `.env` to use another tag or your own build.
Pinning an exact version is safer than `latest`, because migrations only run
with `./ma migrate`. **Do not update images automatically** (Watchtower,
Renovate, Diun, or a floating tag such as `latest` or a major tag like `1`):
the web server does not run migrations, so after an automatic update it runs
against a database that lacks them, and what needs them fails, until
`./ma migrate` runs. A tool on `latest` also installs breaking changes
without their upgrade steps. If a tool updates the image anyway, pin an exact
version or an `X.Y` tag such as `1.4`, which gets patch releases only. The
images carry a provenance attestation and an SBOM.

Releases before 1.0.0 were named after the year and month, plus a build
number (`26.10-1` to `26.10-11`). Their tags and images stay. The image tag
`26.10` stays on `26.10-11` and gets no more updates. Tools that update images
automatically may rank `26.10` above `1.0.0`: switch them once to an exact
version or `1.0` (patch releases, which normally have no migration). `1.0`
gets nothing new once a newer minor or major release
is out. Then move the tag to the newest `X.Y` tag (for example `1.1`, or
`2.0` after the major release's upgrade steps) after reading that release's
notes: back up, set `MAIL_ARCHIVE_IMAGE=ghcr.io/pklnx/mail-archive:1.1` in
`.env`, run `docker compose pull web`, run `./ma migrate` as in
[Upgrades](#upgrades), then `docker compose up -d web`. The setting stays in
`.env` when you extract a newer bundle: move it to the new release's `X.Y`
tag as described in [Upgrades](#upgrades).

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
| A version without imports into smtp accounts | Nothing to run; the migration adds a table, a column and an index and reads every message location once. See [Into an smtp account](./importing#into-an-smtp-account). On very large archives, stop the web server before `./ma migrate` (`docker compose stop web`) as for the journal folder `INBOX` above: the migration locks the message locations table while it reads every location, and deliveries wait until it is done. Postfix keeps the copies queued meanwhile. |
| A version with `postgres:18-alpine` in `docker-compose.yml` (26.10-6 and earlier) | `./ma migrate` rebuilds the database indexes once, before it applies the migrations; stop the web server first (`docker compose stop web`) and pause cron jobs that run `./ma`, then `./ma migrate` and `docker compose up -d web`. It prints `rebuilding all database indexes once` and takes about 15 seconds per 30,000 messages. This index rebuild is not `./ma reindex`, which extracts message text for search. Until it has run, the other `./ma` commands that use the database (except `./ma migrate status`) and the web server refuse to start with `the database needs a one-time index rebuild`. That includes `./ma backup`, so back up with the old bundle before you extract the new one. Installations that already ran the `REINDEX` by hand get one more rebuild; it is harmless. The bundle now uses the Debian-based `postgres:18` image, whose full-text search is more than twice as fast for less frequent words. It reads the existing volume as is, but sorts text differently: both images say `en_US.utf8`, yet the Alpine image (musl) sorts by code point and the Debian image (glibc) by language rules. Indexes on text columns built by the old image are in the wrong order for the new one, so lookups can miss rows and unique checks can miss duplicates until they are rebuilt. PostgreSQL does not warn about it, because the Alpine image records no collation version; `./ma migrate` recognizes such a database by exactly that (no recorded collation version, while the server reports one) and records the rebuild in the database setting `mail_archive.collation_reindexed`. If the collation version changes later (a newer glibc in a later `postgres:18` image), `./ma migrate` rebuilds again. If the rebuild stops with a duplicate key error, something wrote to the database before it ran: the message names the index and the key; restore the backup from before the upgrade (see [Restore](#restore)). Nothing is migrated then. If the database user is no superuser (the bundle's `mailarchive` is one), the rebuild works but recording it fails; the message then gives the `ALTER DATABASE ... SET mail_archive.collation_reindexed = '...'` command to run as a superuser, and the other commands refuse until it has run. For reference, the rebuild by hand, as a superuser: `docker compose exec -T postgres psql -U mailarchive -d mailarchive -c "REINDEX DATABASE mailarchive"` (without the `ALTER DATABASE` from the message, `./ma migrate` does not know it ran and rebuilds once more). `ALTER DATABASE ... REFRESH COLLATION VERSION` is not needed and fails with `invalid collation version change`; only a restore from a dump into a new volume starts a database that no longer depends on the C library, because a new volume gets the builtin locale (see [Database locale](#database-locale)). |
| A version that believes `X-Forwarded-Proto` from any client (26.10-11 and earlier) | Behind a proxy that does not connect from loopback, set `MAIL_ARCHIVE_TRUSTED_PROXIES` to its address before or right after the update. With Docker Compose and Caddy or `tailscale serve` on the host, that is the gateway of the Compose network (see [Behind a reverse proxy](./audit-log#behind-a-reverse-proxy)). Otherwise the session cookie is no longer `__Host-` and `Secure`, and HSTS is not sent; logging in keeps working, and the server logs a warning once that names the proxy's address. Sessions: when a request counts as HTTPS, only the `__Host-` cookie logs in; over plain HTTP (which includes a proxy that does not count) only the plain one. With the variable set at the update, existing `__Host-` sessions keep working. Without it, requests count as plain HTTP, so users log in once more and get the plain cookie; once the variable is set, requests are secure again, that plain cookie is ignored, and users log in once more. The login limits, the invalid-key limit and the passkey-start limit now count IPv6 clients per `/64`. |
| A version without passkeys | Nothing to run. To use passkeys, set `MAIL_ARCHIVE_PUBLIC_URL` to the address you open in the browser (Compose defaults to `http://localhost:WEB_PORT`). Passkeys are bound to its host name: after changing it, remove the passkeys (`./ma user remove-passkeys NAME`) and register them again. |
| A version without attachment search | Nothing to run. The migration only adds two empty tables and is quick; it neither reads nor rewrites the message rows, so deliveries and syncs go on (they wait at most a moment while it adds its foreign keys). Afterwards the web server, or the worker service if you run it, extracts the text of PDF and text attachments of older messages in the background, one message at a time and newest first, and logs at start how many are pending; new mail becomes searchable shortly after it is stored. About 17 ms per message with a typical PDF invoice (26 ms on average over a test corpus of 1,412 varied PDFs), and about 52 MB of database space per 10,000 PDFs with 3.5 KB of text each. `./ma reindex` does the same at once in the foreground; it can run while the web server and syncs run, and can be interrupted and run again. Installations without the web server, which run only `./ma sync` or `./ma import` from cron, get attachment text only from `./ma reindex` or from the worker service: run it after those commands, for example in the same cron job. While it extracts, the process needs up to 224 MiB more memory per worker (see [Hardware and budgets](#hardware-and-budgets)), and the first PDF after a start takes about 3 seconds once (see [Security](../reference/security#the-smtp-server)). Message rows are not rewritten, so no `VACUUM` is needed. With [legal holds](./retention#legal-holds) that use a search filter, run `./ma reindex` before relying on them over attachment text: the background extraction goes newest first, so the oldest mail, which retention deletes first, is extracted last. |
| A version without shared accounts | Nothing to run. The migration adds an empty table and an index on the audit log. Building the index reads every partition of the audit log and holds back audit log writes until it is done, and with them the web server's reads and logins: about 0.1 seconds for 1,000,000 entries (253 MB) on PostgreSQL 18 with the log in memory, longer when it has to be read from disk. With a very large audit log, stop the web server before `./ma migrate` (`docker compose stop web`). |

### Database locale

New installations create the database with PostgreSQL's builtin `C.UTF-8`
locale (`POSTGRES_INITDB_ARGS` in `docker-compose.yml`). How text sorts then
does not depend on the C library of the `postgres` image, so an image update
cannot invalidate the indexes. Names in lists that PostgreSQL sorts
(accounts, users, retention overview) sort by code point: capital letters
before lower case, umlauts last. The folder tree is sorted in the browser
and is not affected. Full-text search, trigram matching and
case-insensitive matching behave as before.

The setting is read only when a new database is created. An existing
database keeps its locale and its sort order, and `./ma migrate` keeps
watching for the Alpine case described in the table above. The first
`docker compose up -d` after an upgrade recreates the `postgres` container
once, because its environment changed; the volume and the locale stay. A
restore into a new volume (after `docker compose down -v`) gets the builtin
locale; `pg_restore` rebuilds every index, so that is safe.

## Background jobs

Some work runs in the background, after the message that caused it is
stored: the first is extracting the text of attachments for search. Syncs,
imports and journaling never wait for it. The jobs are kept in the database,
so a restart loses none: a job that was running is handed back at shutdown,
and one whose process crashed is retried about a minute later (its lease
expires), with a growing pause between attempts. After a kind's attempts
(three for attachment text) the job is given up as **failed**, so a document
that crashes the process every time cannot keep it busy; the message stays
archived and searchable by everything else.

**By default the web server runs them**, one at a time
(`MAIL_ARCHIVE_JOB_WORKERS=1`); nothing needs to be set up. `./ma status`
shows the queue in its last line, for example
`jobs: attachment_text 12 queued (oldest 3m), 1 running, 2 failed`, or
`jobs: none`. Failed jobs are queued again with
`./ma jobs retry attachment_text`, for example after an upgrade that fixes
the cause (written to the [audit log](./audit-log) as `jobs.retry`).

**A separate worker** runs them in a container of its own, with its own
memory limit, so a large document can never take memory from the web
server. Hosted instances should use it. In `.env`:

```sh
COMPOSE_PROFILES=worker   # start the worker service with the others
WEB_JOB_WORKERS=0         # the web server runs no jobs
#WORKER_JOB_WORKERS=1     # jobs the worker runs at a time (1-8)
```

Then `docker compose up -d web worker`. The web server logs
`jobs off in this process`, the worker `worker on`. Set both variables: with
the profile on and `WEB_JOB_WORKERS` left at 1, both run jobs, which is
correct, but the web server then needs the extraction memory too. With
`WEB_JOB_WORKERS=0` and no worker running, nothing is extracted, and
[`/healthz/jobs`](#healthz-jobs) turns `degraded` after 30 minutes. The
worker can also run outside Compose (`mail-archive worker`); several
workers, on several hosts, share the queue safely.

### Hardware and budgets

The smallest target is a host with **4 GB of RAM and 2 cores** for
PostgreSQL, the web server and the worker together. The memory is split
like this:

| Share | Budget | Basis |
|---|---|---|
| Kernel, Docker, other processes | 512 MiB | reserved |
| PostgreSQL (its processes, not the page cache) | 1024 MiB | the defaults of the `postgres:18` image: `shared_buffers` 128 MiB, about 15 connections (web server, worker and CLI) of 10-20 MiB each, `work_mem` 4 MiB per sort, autovacuum; about 550 MiB, with headroom for index maintenance |
| Web server without jobs | 768 MiB | syncs, fills, SMTP, the web UI |
| Background jobs (one worker, in the web server or separate) | 768 MiB | the `mem_limit` of the worker service, below |
| Page cache | 1024 MiB, not reserved | everything not used; search indexes are read from it |

Per job at a time (a worker slot), extracting a document takes up to
224 MiB: up to 96 MiB of WebAssembly memory for the PDF reader (outside the
Go heap; the budget keeps the 128 MiB the design allows), the attachment and
its text twice over for the garbage collector (52 MiB), and 32 MiB for the
reader's instance. The process needs 96 MiB when idle, and once after each
start 300 MiB while it compiles the PDF reader. So the peak with c workers
is the larger of 96 + c × 224 and 96 + 300 + (c − 1) × 224 MiB: 396 MiB with
one, 620 MiB with two, 844 MiB with three. 768 MiB holds up to **two
workers**.

**One PDF at a time per process.** A process reads only one PDF at a time,
whatever `MAIL_ARCHIVE_JOB_WORKERS` says; a second worker slot in the same
process extracts text, CSV and HTML attachments beside it. This keeps two
documents, possibly of different users, out of the PDF reader at the same
time (see [Security](../reference/security#the-smtp-server)). For more PDF
throughput, run a second worker *process*: another worker service or
`mail-archive worker` with its own memory limit, not `WORKER_JOB_WORKERS=2`.

- **The worker service** has `mem_limit: 768m` and `cpus: 1.0`: PostgreSQL
  and the web server keep the other core. A container's memory limit also
  counts the page cache of the files it reads, which the kernel reclaims
  before it would end the process.
- **`GOMEMLIMIT`** (Go's soft heap limit) is `mem_limit` − c × 128 MiB − 64 MiB:
  576 MiB for one worker (the default, `WORKER_GOMEMLIMIT`), 448 MiB for two.
  With two, the compile right after a start goes above it for a moment; the
  limit is soft, so that only costs some garbage collection then.
- **More workers** only on hosts with at least 4 cores: each needs a core
  and 224 MiB more. Raise `mem_limit` and `WORKER_GOMEMLIMIT` with them. For
  PDFs, a second worker process helps; a second slot in one process does
  not.
- **The web server** sets no memory limit by default, as before. Hosts that
  want one and run the jobs in the web server give it 1536 MiB with
  `GOMEMLIMIT=1344MiB`.
- **Waiting time:** with one worker, new mail waits behind at most one
  running job of older mail, which ends within its time limit.

## Monitoring and alerts

An account whose syncs fail (a changed password, a provider that blocks the
login, an expired app password) loses mail until someone notices. These
things make it visible:

- **The web UI** shows a banner above the mail when one of your accounts
  keeps failing or has not synced for a while; see
  [Web UI](./web-ui#sync-problems).
- **An alert** through a webhook: one message when an account's syncs keep
  failing, one when they work again. See [Syncing](./syncing#alerts) for
  what counts. The same goes for the runs of a
  [recurring fill](./journaling#keep-importing-recurring-fill) of an smtp
  account. Another alert reports mail deleted on the server in bulk; see
  [Reconcile](./syncing#alert-for-mail-deleted-on-the-server).
- **`/healthz/sync`** for a monitoring tool such as Uptime Kuma.
- **`/healthz/jobs`** says whether the [background jobs](#background-jobs)
  of new mail get done.

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
never share a message. Several accounts that change at the same time go into
one message (up to 50 per request). The `id` is also sent as the
`X-Mail-Archive-Id` header.

Alerts are delivered **at least once**: if the server stops right after the
receiver accepted a message, it is sent again with the same `id`. ntfy,
Gotify and Slack do not drop such repeats; a receiver of your own can use
the `id`. A receiver that is down is retried after 30 seconds, then with
growing pauses up to an hour, and given up after 24 hours (logged as
`notification given up`; a `gone` alert 24 hours after its run). An answer
like `400` or `404` usually means a wrong URL, token or format: it is logged
as an error and retried hourly. Redirects are not followed.

The log records each alert (`alert sent`, `recovery sent`, `loss alert sent`)
with the account and its owner. It never contains the webhook URL, the
authorization or the message; failed attempts name only the scheme and host.

### /healthz/sync

`GET /healthz/sync` needs no login and answers:

| Status | Body | Meaning |
|---|---|---|
| `200` | `{"status":"ok"}` | Every enabled account syncs, and every recurring fill runs. |
| `503` | `{"status":"degraded","failing":[3,7],"stale":[5]}` | Account IDs whose last `MAIL_ARCHIVE_ALERT_AFTER_FAILURES` syncs failed, or that had no successful sync within two of their sync intervals (at least one plus an hour); see [Alerts](./syncing#alerts). An smtp account is listed when one of its recurring fills fails or is stale the same way. |
| `503` | `{"status":"unavailable"}` | The database cannot be reached. |

Disabled, removed and import accounts are left out, as are fills that were
stopped; with the schedule off (`MAIL_ARCHIVE_SYNC_INTERVAL=0`) nothing
counts as stale, nor does an account not on the schedule, or one that is
syncing or waiting in the queue, nor a recurring fill during a run or while
it waits for one. A running sync or fill counts from its start, and one the
web server has taken from the queue from then, also while it waits for a
verify or a deletion to finish: one that hangs, or waits behind a verify
that hangs, turns stale after the same time. A recurring fill counts as
stale from when it was switched on until its first successful run. The
answer is cached for 15 seconds. `./ma status` shows each account's ID next
to its name, in `HEALTH` the same state, and in `FAILED` how many syncs in a
row failed.

The request must use an allowed host name (`MAIL_ARCHIVE_ALLOWED_HOSTS`),
like every request. For Uptime Kuma, add an HTTP monitor for
`http://localhost:8080/healthz/sync`, or from another machine with a host
name you allowed. With curl:

```sh
curl -fsS http://localhost:8080/healthz/sync || echo "mail-archive: syncs failing"
```

Do not use `/healthz/sync` as the container's healthcheck: Docker would
restart the web server whenever an IMAP account fails, which fixes nothing.
`/healthz` only says whether the server is up and is the right check there.

### /healthz/jobs

`GET /healthz/jobs` needs no login either and gives only a status, no counts:

| Status | Body | Meaning |
|---|---|---|
| `200` | `{"status":"ok"}` | Background work for new mail gets done. |
| `503` | `{"status":"degraded","stale":["attachment_text"]}` | Work for new mail of these job kinds has been due for more than 30 minutes: no worker runs (`WEB_JOB_WORKERS=0` without the worker service), or it hangs. |
| `503` | `{"status":"unavailable"}` | The database cannot be reached. |

Backfills of mail archived before (which may take days on a large archive)
and failed jobs do not make it `degraded`; `./ma status` and, for admins,
`GET /api/status` show them with counts. The answer is cached for 15
seconds. `/healthz` stays `ok` while the process runs.

## Logs

```sh
docker compose logs web --tail 100
```

Set `MAIL_ARCHIVE_LOG_LEVEL=debug` in `.env` for more detail.
