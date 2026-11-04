# Backups and upgrades

This page is for the operator, who runs the instance and its host. Users do
not run any of these commands. What they see of it: an
[export](#exporting-mail) they asked for, the [banner](./web-ui#sync-problems)
in the web UI when an account keeps failing, the
[GoBD export](#gobd-export) for a tax audit, and the
[one-time steps](#one-time-steps) an upgrade may bring (for example
attachment text of older mail that becomes searchable only after a while).

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

Without [encryption at rest](#encryption-at-rest) the message files
(`.eml`) are **not** encrypted on disk; with it they are (`.enc`), and the
keyring in `data/keys/` is wrapped under the archive keys. The dumps are not
encrypted yet. Keep both on an encrypted volume; FileVault on macOS does
this by default.

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

Exports run only on the command line, so the operator runs them, for example
at a user's request. The web UI has no export. `export` writes archived mail
in a format mail clients import:

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
Delete them once imported. `verify` ignores the `export/` directory. Every
export is in the [audit log](./audit-log) as `data.export`.

## GoBD export

For a tax audit, a customer asks the operator for their records in a
machine-readable form ("Datenüberlassung", GoBD Rz. 167). `export --format
gobd` writes them: the original messages, CSV tables with an `index.xml`
description, a checksum manifest and a statement signed with the
attestation key. The format, the flags and the exit codes are in the
[command line reference](../reference/cli#export-format-gobd); what the
customer receives and checks is in [GoBD export](./gobd-export). The export
runs only on the command line; there is no download in the web UI.

**One export per customer.** Every export covers one customer group, and
the operator names it: with two or more groups, `--group NAME` is required,
and the export refuses a selection outside it. To find a customer's users,
`./ma user list` shows each user's GROUP, `./ma group list` the groups.

```sh
# The whole customer, one year:
./ma export --format gobd --group "Meier GmbH" --since 2025-01-01 --until 2025-12-31 \
  --out /export/meier-2025 --requested-by "Huber (Steuerberater), mail of 2026-11-02"
# Some users, as a ZIP:
./ma export --format gobd --user anna --user ben --group "Meier GmbH" --out /export/meier-anna-ben.zip
# What a legal hold for the audit covers:
./ma export --format gobd --hold 3 --group "Meier GmbH" --out /export/meier-hold-3
```

- **A mixed selection is refused**, never filtered: `the selection spans
  customer groups: anna (Meier GmbH), kim (Schulz KG); one export covers one
  customer group`. Split the request into one export per customer.
- **Name groups after the customer's legal entity** ("Meier GmbH"), never
  with internal notes: the group's name and id are in the signed
  `STATEMENT.txt` and in `README.txt`, which the customer and the tax office
  read.
- **A legal hold export** needs a hold that names a user, an account or a
  group; a hold over every customer is refused. Only `--hold` exports fill
  the `holds` column, and only with holds that name the customer's users,
  accounts or group; holds that cover several customers are not listed.
- **Exit 3** means a selected user or account moved to another group while
  the export ran. The export is complete as of its snapshot, before the
  move; check with the customer before handing it over.
- **Interrupted?** Run the same command with `--resume`; the directory
  `OUT.partial` keeps what was written. It refuses when the selection's
  group changed since the export started: start a new export then. A
  `.partial` left over without a resume blocks a new export to the same
  path; remove it with `rm -r`.
- **Files missing (exit 1):** the export lists them in `missing.csv`, says
  `complete no`, and the missing messages are named on stderr. Run
  [`verify`](#checking-the-archive) to see where they were.

**Where it is written.** With Docker Compose, `/export` in the container is
`EXPORT_DIR` on the host (default `./export`), outside the data directory,
so the export never lands in backups of `data/` or under its encryption at
rest. `./ma export` makes a fresh `EXPORT_DIR` writable for the container
user, as `migrate` does for `data/`. On Linux the files belong to UID 65532:
`sudo chown -R "$USER" export/meier-2025` makes them yours, for example to
copy them onto a medium.

**The hosted path, honestly.** The operator handles the customer's mail in
plaintext while exporting: the export is not encrypted, and whoever runs
the command can read it. So:

- Export only on a request of the customer (its admin), and record who
  asked with `--requested-by`. Both audit entries (`data.export`,
  `data.export_done` with the SHA-256 of `SHA256SUMS`) are recorded under
  the customer's group, so `./ma audit --group "Meier GmbH"` and the Log's
  Group filter show the customer's exports.
- Send the export only to the customer's admin (for `--grant`: the grant's
  owner), over a channel you agreed with them: an encrypted archive with the
  password on another channel, or a medium handed over in person. Give them
  the `manifest_sha256` from the output (`SHA256SUMS ...`) separately, so
  they can check what arrived.
- Delete your copy when the customer confirms it arrived:
  `rm -r export/meier-2025`.
- The shell on the host is not a customer interface: customers never get
  access to it or to the `export` directory.

**While it runs** the export reads in one database snapshot. It takes no
locks, so users, logins and group moves go on; but PostgreSQL's vacuum
cannot clean up rows that changed after the snapshot began until the export
ends, which matters only for exports of several hours on a busy instance.
Mail archived during the export is not in it. Before writing, it checks the
free space (the messages, about 1 KiB of tables per message, plus 2 %).

**Budget on the 4 GB / 2 core target.** One process, one core, one database
connection; the Go heap's soft limit is 256 MiB (`GOMEMLIMIT` overrides it).
Measured with 4,000 messages of 50 KB (200 MB) on 2 cores, the files in the
page cache:

| Output | Messages per second | Mail per second | Peak memory (RSS) |
|---|---|---|---|
| Directory | about 3,700 | about 180 MB/s | 34 MB |
| ZIP (Deflate) | about 1,700 | about 85 MB/s | 43 MB |

Heap growth over 10,000 messages is about 1.5 MiB (a test checks it stays
below 64 MiB). A ZIP keeps about 300 bytes per entry until it is closed, so
it holds at most 200,000 messages (about 200 MB of memory then); larger
selections go into a directory or are split by year. From disk instead of
the page cache, and on network storage, expect 2 to 4 times slower: about
5 to 15 minutes per 100,000 messages.

## Encryption at rest

With encryption at rest, every message file is stored encrypted
(`messages/ab/cd/<sha256>.enc`) under an archive key the operator holds,
apart from the data. What it protects, and what it does not, is in
[Security](../reference/security#the-archive-key). The database stays
unencrypted in this version.

**Key custody is the operator's job, and these steps are mandatory.** The
archive key and every recovery key are the only way to read the encrypted
mail, backups included: nobody, not the app's developers either, can
recover it without them.

- Create the key with `./ma encryption keygen` and set
  `MAIL_ARCHIVE_ARCHIVE_KEY_FILE` (see [Getting started](./getting-started#the-archive-key)).
- Keep the recovery key it prints offline, apart from the host and the
  backups (a password manager, or paper in a safe). `encryption add-key`
  adds another one.
- Back up the key file apart from `data/` and its backups, and check after
  every change that `./ma encryption status` shows the configured key
  opening the keyring.

### Turning it on

A **new instance** turns it on before it archives anything:
`./ma encryption enable` on the empty archive only records the state (see
[Getting started](./getting-started#the-archive-key)).

An **existing archive** is converted offline, in one maintenance window:
nothing may write message files while it runs.

1. Back up the database and the data directory ([Backups](#backups)).
2. Create the key (above), if not done yet.
3. Stop everything that writes: `docker compose stop web worker`, and pause
   cron jobs that run `./ma sync`, `import`, `account fill`, `reindex`,
   `verify` or the retention commands.
4. Run `./ma encryption enable`. It prints the percentage, the files per
   second and the time left.
5. Start again: `docker compose up -d`.

While it runs, the web UI, the IMAP syncs and the SMTP receiver are down.
IMAP syncs catch up afterwards; mail servers retry an SMTP delivery for
days, so journaled mail arrives once the receiver is back. On a shared
instance this is **one maintenance window for all customers at once**:
announce it to all of them. The window cannot be split: until the command
has finished, the web server and the worker refuse to start.

The command refuses to start while a writer runs and names it (for example
`"mail-archive writer serve" (pid 812)`), and while verify, a retention
sweep or a purge holds the blob lock. It needs a direct connection to
PostgreSQL or a session-mode pool: a transaction-mode pooler (PgBouncer
`pool_mode=transaction`) breaks the locks it and the writers rely on.

**Interrupted** (Ctrl-C, a lost connection, a crash, a reboot), it resumes:
run `./ma encryption enable` again. It records its position after every
batch, and `./ma status`, from another terminal or after an interruption,
shows the unfinished change, its progress and the command that finishes it.
Running `./ma encryption disable` instead turns back.

How it converts, in batches of 200 files or 256 MiB:

- Each file of the batch is encrypted into `tmp/`, synchronized to disk,
  read back in full and checked against its SHA-256, then linked under its
  `.enc` name. Only after the batch's directories are synchronized are the
  `.eml` files unlinked. A crash at any point leaves every message readable,
  and the rerun completes the batch.
- Files that verify moved to `orphans/` are encrypted too, so no plaintext
  of a message is left in `data/`. Nothing writes while the command runs,
  so every file in `tmp/` is left by an interrupted write, whatever its
  age: it is moved to `orphans/<time>/tmp/` and encrypted there. A later
  `enable`, with encryption already on, does the same for files left in
  `tmp/` since.
- A plaintext file whose content does not match its name is never
  encrypted: it stays as it is, in `messages/` or in `orphans/`, the
  command lists it, and `verify` reports the one in `messages/`. Restore a
  good copy from a backup, or delete it once checked.
- Encryption is on only after a full pass from the start has found nothing
  left to convert; the end of a resumed run makes that pass too. It only
  reads directories, so it is quick.

**Downtime and budget.** Measured with the test `TestConvertGate` in
`internal/archive` (50 KB files; the gate needs at least 50 files/s), on
the development container limited to 2 cores, local SSD shared with other
jobs:

| Files | Downtime (measured rate) | Downtime (at the gate's 50 files/s) |
|---|---|---|
| 10,000 | about 15 s to 1 min | about 3.5 min |
| 100,000 | about 2.5–9 min | about 35 min |
| 1,000,000 | about 25–90 min | about 5.5 h |

The rate was 186–733 files/s; the disk's `fsync` sets it, not the CPU.
Network block storage is slower: plan 1–3 hours per 1,000,000 files there.
Only distinct files count; mail that several accounts or customers received
is stored once. The command converts on one goroutine, so it uses at most
one core (0.2–0.8 measured; the gate bounds the CPU time at 5 ms per 50 KB
file, measured about 0.5 ms, 1.1 ms with the race detector), and at most
32 MiB of memory (measured +3–7 MiB over its start, up to 17 MiB on a
very fast disk); it needs free disk space for one batch, at most 256 MiB.

### Turning it off

`./ma encryption disable` decrypts every file back to `.eml`, under the
same rules: writers stopped, resumable, and off only after a full pass.
Run it before going back to a version below 1.5.0, which cannot read
encrypted files. An encrypted file that cannot be decrypted (damaged, or
under a data key the keyring lacks) is moved to `orphans/<time>/` and
listed; its message then shows up as `missing` in `verify`. To recover one
once the keyring that opens it is back, move it to its place in `messages/`
(the path below `orphans/<time>/`) and run `./ma encryption disable` again.
Files left in `tmp/` are moved to `orphans/<time>/tmp/` and kept as they
are. The keyring and the key stay; keep them as long as backups made with
encryption on exist.

**Old plaintext.** After turning it on, earlier plaintext can survive in
free space of the disk, in older snapshots and in backups made before. For
a clean state, restore onto a new volume and destroy the old one (see
[Security](../reference/security#the-archive-key)).

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

Two `./ma migrate` runs at the same time wait for each other, and after a
killed `migrate` the next one may wait for the database server to end the
killed statement; see [`migrate`](../reference/cli#migrate) for the messages
and time limits.

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

The tags and images of releases before 1.0.0 stay available.

Pull requests that change the database schema carry the label
`db-migration`. Back up the database before you upgrade past one of them.

`./ma migrate status` lists applied and pending migrations.

### One-time steps

Upgrading from a version before 1.0.0: the
[release notes](https://github.com/pklnx/mail-archive/releases) of the
releases after yours up to 1.0.0 name their breaking changes and database
migrations; the one-time steps of those versions are in the
[docs of version 1.0.0](https://github.com/pklnx/mail-archive/blob/v1.0.0/docs/guide/operations.md#one-time-steps).

| When upgrading from | Run |
|---|---|
| A version without customer groups | Nothing to run. The migration puts every user, and with them every account, into one group, `Default`; existing shares stay. It adds a column and an index on the audit log, which every existing entry leaves empty: like the shared accounts index below, building it reads every partition and holds back audit log writes until it is done, about 0.1 seconds per 1,000,000 entries with the log in memory. With a very large audit log, stop the web server before `./ma migrate` (`docker compose stop web`). For a shared instance, create a group per customer afterwards and move its users (see [Customer groups](./users#customer-groups)). Rolling back the migration refuses while more than one group exists. |
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
watching for a database created by the `postgres:18-alpine` image of versions
before 1.0.0 (see [`migrate`](../reference/cli#migrate)). The first
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

- **The web UI** shows users a banner above the mail when one of their
  accounts keeps failing or has not synced for a while; see
  [Web UI](./web-ui#sync-problems).
- **An alert** through a webhook, set up by the operator: one message when an
  account's syncs keep failing, one when they work again. See
  [Syncing](./syncing#alerts) for what counts. The same goes for the runs of a
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
alerts name the accounts and their owners. Use a long random topic, or
better a protected topic with an access token:

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
