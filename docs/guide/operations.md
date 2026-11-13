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
   `backups/BACKUP-NOTE.txt` with the restore steps. With
   [encryption at rest](#encryption-at-rest) on, the dump is encrypted
   instead, see [Encrypted backups](#encrypted-backups). Without Docker
   Compose, `mail-archive backup DIR` does the same with the `pg_dump` in
   `PATH`; see [Backups without Docker](#backups-without-docker) for what
   it gets.
2. **The archive directory** (`data/`, or `ARCHIVE_DIR`): the message files.
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
(`.eml`) and the dumps are **not** encrypted on disk: keep both on an
encrypted volume; FileVault on macOS does this by default. With it, both
are encrypted (`.enc`, `.dump.enc`), and the keyring in `data/keys/` is
wrapped under the archive keys.

The files alone do not record where each message was found or which
accounts exist, so back up both the files and the database.

A nightly backup with cron, which also deletes dumps older than 30 days and
checks the archive once a week:

```sh
30 2 * * * cd /path/to/mail-archive && ./ma backup ./backups && rsync -a --delete ./data/ /backup/mail-archive-data/ && find ./backups -name 'mailarchive-*' -mtime +30 -delete
0 4 * * 0 cd /path/to/mail-archive && ./ma verify > /tmp/mail-archive-verify.log 2>&1 || echo "mail-archive verify: see /tmp/mail-archive-verify.log"
```

On Linux the files in `data/` belong to UID 65532 (the container user), so
copy them as root or with `sudo`.

### Backups without Docker

Without Docker Compose, `mail-archive backup DIR` runs the `pg_dump` in
`PATH`, and `mail-archive restore-dump FILE` the `pg_restore` in `PATH`,
each at least of the server's major version. They get exactly this, so no
secret of the app reaches them, and they get the TLS and authentication
settings of the app's connection, with GSSAPI encryption off, or the
command refuses to run (see below):

- **Host, port, user, database and password** as the app's own connection
  resolves them: from `MAIL_ARCHIVE_DATABASE_URL`, and for what it leaves
  out from `PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE` and `PGPASSWORD` of
  the process, the password also from `PGPASSFILE` (by default
  `~/.pgpass`). A list of hosts keeps its order. The password is passed in
  the environment, never on the command line.
- **The TLS and authentication settings, from `MAIL_ARCHIVE_DATABASE_URL`
  only:** `sslmode` (also as the URL alias `ssl=true`), `sslrootcert`,
  `sslcert`, `sslkey`, `sslsni`, `sslnegotiation`, `target_session_attrs`,
  `channel_binding` and `require_auth`. A key given twice counts with its
  last value, as for the app. Where the app's driver derives the mode from
  another key, the tools get that mode too: `sslnegotiation=direct` without
  `sslmode` is `require`, and `sslrootcert=system` is always `verify-full`.
  What the URL leaves out, the tools get named explicitly with the value
  the app takes, so that it does not depend on the libpq version's own
  defaults: `prefer`, `sslsni=1`, and for `sslrootcert`, `sslcert` and
  `sslkey` the files the app looks at, `$HOME/.postgresql/root.crt`,
  `postgresql.crt` and `postgresql.key` (a file that does not exist is
  not used, by the app or the tools). Only an empty `require_auth` is
  left out, which means the same in every libpq version.
- **`PGGSSENCMODE=disable`,** always: libpq built with GSSAPI would
  otherwise prefer GSSAPI encryption to TLS, whatever `sslmode` says,
  where Kerberos credentials and the server allow it. The app never uses
  it, and a `PGGSSENCMODE` of the process cannot turn it on.
- **These variables of the process,** when set: `PATH`, `HOME`, `TMPDIR`,
  `TZ`, `LANG`, `LC_ALL`, `LC_CTYPE`, `LC_MESSAGES`, `LD_LIBRARY_PATH`,
  `SSL_CERT_FILE`, `SSL_CERT_DIR`, `OPENSSL_CONF`, `KRB5CCNAME` and
  `KRB5_CONFIG`.

Nothing else is passed on: no `MAIL_ARCHIVE_*` variable, no other `PG*`
variable of the process (such as `PGAPPNAME` or `PGCONNECT_TIMEOUT`), and
no other parameter of the URL (such as `sslcrl` or `connect_timeout`). The
tools apply these settings with libpq and OpenSSL (`OPENSSL_CONF`
included), the app with Go's TLS: the settings are the same, the TLS
libraries are not.

Both commands refuse before `backup` writes its audit entry or anything
into `DIR`, before `restore-dump` decrypts the dump, and before either runs
`pg_dump` or `pg_restore`, when:

- the process sets `PGSSLMODE`, `PGSSLROOTCERT`, `PGSSLCERT`, `PGSSLKEY`,
  `PGSSLSNI`, `PGSSLNEGOTIATION`, `PGTARGETSESSIONATTRS`,
  `PGCHANNELBINDING` or `PGREQUIREAUTH`, and the URL does not set the same
  key: the app would use it, the tools would not get it. Move it into the
  URL, for example
  `postgres://mailarchive@db.example/mailarchive?sslmode=verify-full&sslrootcert=/etc/ssl/db-ca.pem`;
- `PGSERVICE`, `PGSERVICEFILE` or `PGSSLPASSWORD` is set, or the URL sets
  `service`, `servicefile` or `sslpassword`: a service would give the tools
  other settings than the app, and the password of a client key could only
  reach them on the command line. Put the settings into the URL, and use a
  client key without a password;
- `HOME` is not set, the connection uses TLS and the URL does not name
  `sslrootcert`, `sslcert` and `sslkey`: the app then looks for no
  certificate file, libpq in the home directory of the user account;
- `$HOME/.postgresql` holds `postgresql.crt` without `postgresql.key` or
  the other way round, which the app ignores and libpq refuses;
- the URL sets `ssl` to anything but `true`, or a keyword/value string
  (`host=… sslmode=…`) sets `ssl` at all: use `sslmode`;
- the URL sets `sslnegotiation=direct` with `sslmode=allow` or `disable`,
  which libpq refuses;
- the settings the tools would get do not give the same connection
  attempts as the app's connection (hosts, ports, TLS and its certificates,
  `target_session_attrs`, `channel_binding`, `require_auth`). An example is
  an empty `sslrootcert=` while `~/.postgresql/root.crt` exists: the app
  reads it as no root certificate, libpq as that file;
- `pg_dump` or `pg_restore` is too old for a setting, which it would ignore
  without an error: `sslnegotiation=direct` needs 17, `require_auth` and
  `sslrootcert=system` need 16.

The error names the variable or key, never its value. With Docker Compose
none of this applies: `./ma` runs the tools in the database container.

### Encrypted backups

While encryption at rest is on (or an `encryption` change is unfinished),
`./ma backup` encrypts the dump while `pg_dump` writes it; the plaintext
dump only passes through a pipe and never reaches a disk. It writes:

- `mailarchive-<time>.dump.enc`, in the format of the message files, under
  the keyring's current data key;
- `mailarchive-<time>.keyring`, a copy of the keyring the dump opens with,
  wrapped under the archive keys like the one in `data/keys/`: safe to keep
  with the backup, useless without a key;
- `BACKUP-NOTE.txt`, which names the dump, the keyring copy and the
  fingerprints of the keys that open it (archive key and recovery keys),
  never a key.

Without the archive key, `backup` refuses: a dump is never written in
plaintext while encryption is on. If `pg_dump` fails, nothing is written.
Only an encrypted dump is authenticated, so on an archive with encryption at
rest `restore-dump` refuses a dump without encryption unless you pass
`--plaintext` (see [Restore](#restore), step 2).
To read a dump the **archive key that was current when it was made**, or a
recovery key of that time, is needed: a key removed later still opens it,
and a key added later does not. Keep the old keys as long as such backups
exist (see [Key custody](#encryption-at-rest)).

By hand, without `restore-dump`, an encrypted dump is read with
`mail-archive unseal-stream --keyring mailarchive-<time>.keyring <
mailarchive-<time>.dump.enc`, which writes the plaintext dump to standard
output, for example into `pg_restore --list`. Exports are not encrypted; an
export written inside the data directory gets a warning, see
[Exporting mail](#exporting-mail).

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

1. Put `MAIL_ARCHIVE_SECRET_KEY` back into `.env` and, with encryption at
   rest, the archive key file into the keys directory
   (`MAIL_ARCHIVE_ARCHIVE_KEY_FILE`). The key opens the dump, and it is
   also what tells `restore-dump` that this archive uses encryption at rest,
   so that a dump without encryption put in place of the encrypted one is
   refused (step 2). Copy the data directory back to `data/`
   (or `ARCHIVE_DIR`). On Linux the files must belong to UID 65532 again:
   `sudo chown -R 65532:65532 data` (for rootless Docker, see
   [Linux hosts](./getting-started#linux-hosts)). Stop the web server and
   the worker: `docker compose stop web worker`.
2. Load the dump into a new, empty database:
   ```sh
   docker compose exec -T postgres dropdb -U mailarchive --force mailarchive
   docker compose exec -T postgres createdb -U mailarchive mailarchive
   ./ma restore-dump backups/mailarchive-<time>.dump.enc
   ```
   (or the `.dump` of an archive without encryption). It runs `pg_restore`
   in the database container in one transaction; an encrypted dump is
   decrypted on its way there with the keyring copy next to it. A damaged or
   cut dump is refused, and the database stays empty.

   Only an encrypted dump is authenticated: whoever can write the backup
   location decides what a dump without encryption holds, and `pg_restore`
   runs it as the database superuser. So `restore-dump` refuses a dump
   without encryption, before anything is restored, when the archive shows
   any sign of encryption at rest: an archive key is configured, the data
   directory has a keyring (`data/keys/keyring`), the file name ends in
   `.enc`, or a keyring copy lies next to the dump. The message lists the
   signs it found. Nothing in the backup location can lift this. If the
   dump is one you expect without encryption (a backup from before
   `encryption enable` or after `encryption disable`), check where it came
   from first, then pass `--plaintext`:
   `./ma restore-dump --plaintext backups/mailarchive-<time>.dump`.
   `--plaintext` on an encrypted dump is refused. On an archive without any
   of these signs, a `.dump` is restored without the flag, as before; keep
   such backups where only you can write. `restore-dump`
   refuses a database that holds anything: `pg_restore --clean` cannot
   replace this schema (it cannot drop the partitions of the audit log), so
   a restore over the old database would fail half-way or mix both. Without
   Docker Compose: `mail-archive restore-dump FILE`, with `pg_restore` in
   `PATH`, into a database made with `dropdb --force` and `createdb`. It
   refuses while a writer runs, and holds the writers lock and the
   migration lock until it has reconciled: a web server or worker that
   starts meanwhile waits for the restore instead of migrating the empty
   database with [start-up migrations](#start-up-migrations) on, and then
   checks the restored schema (and migrates a dump older than this version
   when they are on); a cron `./ma sync` that starts meanwhile refuses to
   start and is retried by the next cron run.
3. `./ma migrate`, then `./ma attest` if attestations are on (it imports
   the attestations written after the dump, before anything archives; see
   [Restore](./tamper-evidence#restore-from-a-backup)), then `./ma sync`,
   then `./ma verify`.

**Interrupted** (Ctrl-C, a lost terminal): `pg_restore` in the database
container may still finish after `./ma` ended, without the reconciliation.
Run `./ma status`. If the database holds the dump, run
`./ma restore-dump --reconcile-only`; otherwise recreate the empty database
and start the restore again.

Files newer than the dump show up as orphans. The sync adopts those still on
the server. Others came from mail deleted on the server since the dump: they
are the only copy, so look at them before `--fix-orphans` moves them aside.

**What `restore-dump` reconciles.** The dump and the data directory each
hold a copy of the keyring, the dump also the state of encryption at rest,
and the two can be of different ages. After loading the dump, `restore-dump` brings them
together and prints each step:

- The newer keyring (by its generation) wins and is written to the other
  copy: a recovery key added with `encryption add-key` after the backup is
  in the newer keyring file and stays. The data keys of both copies are
  kept, so files encrypted under a key only one copy knows stay readable.
  A keyring of another archive is refused before anything is restored when
  a keyring copy lies next to the dump, encrypted or not; for a dump
  without encryption and without a copy next to it, only after loading it,
  since its keyring copy is in the dump. Restore that dump into a new
  database again with the right data directory.
- A dump that says off next to a data directory with encrypted files (a
  backup from before `encryption enable`, restored with `--plaintext`)
  gives the state `encrypting`: run `./ma encryption enable` before
  anything else starts; it finishes in one quick pass.
- A missing keyring file is rebuilt from the dump.

For a dump older than the schema of this version, it says to run
`./ma migrate` and then `./ma restore-dump --reconcile-only`; the web
server does the same reconciliation at its start.

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
Delete them once imported. With encryption at rest on, an export inside the
data directory would put plaintext where everything else is encrypted, so
`export` warns about it; write it to `/export` instead (`EXPORT_DIR` on the
host, default `./export`). `verify` ignores the `export/` directory. Every
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
# Exactly what an auditor grant covers (the ID from ./ma auditor list):
./ma export --format gobd --grant 12 --group "Meier GmbH" --out /export/meier-grant-12
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
- **An auditor grant export** covers exactly the grant's accounts, folders
  and message period, also of a grant that has not started or has ended;
  a revoked one is refused. Both audit entries carry the grant and its
  owner, so the owner sees the export in their Log, as the operator's.
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
  adds another one; [Changing keys](#changing-keys) covers replacing them.
- Back up the key file apart from `data/` and its backups, and check after
  every change that `./ma encryption status` shows the configured key
  opening the keyring.

### Turning it on

A **new instance** turns it on before it archives anything. Started from
configuration, the web server or the worker does it at its first start,
with the configured archive key, and refuses to start without one (see
[Start from configuration](./getting-started#start-from-configuration));
`MAIL_ARCHIVE_ENCRYPTION_AT_SETUP=off` is for test instances only. Set up
from the command line, `./ma encryption enable` on the empty archive only
records the state (see [Getting started](./getting-started#the-archive-key)).

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
(the path below `orphans/<time>/`) and run `./ma encryption disable` again;
on an archive that is off, it first looks for encrypted files in
`messages/`.
Files left in `tmp/` are moved to `orphans/<time>/tmp/` and kept as they
are. The keyring and the key stay; keep them as long as backups made with
encryption on exist. While they stay, the dumps that `./ma backup` writes
after `disable` (without encryption) restore only with
`./ma restore-dump --plaintext`, and their `BACKUP-NOTE.txt` says so (see
[Restore](#restore)).

**Old plaintext.** After turning it on, earlier plaintext can survive in
free space of the disk, in older snapshots and in backups made before. For
a clean state, restore onto a new volume and destroy the old one (see
[Security](../reference/security#the-archive-key)).

### Where the key lives

`./ma encryption keygen` writes the archive key to `keys/archive.key`. The
default keys directory (`ARCHIVE_KEY_DIR`, `./keys`) lies in the Compose
project directory, next to `./data` and on the same disk: it protects copies
and backups of `data/`, not snapshots or a theft of that disk. For real
separation, move `ARCHIVE_KEY_DIR` to another disk, or feed
`MAIL_ARCHIVE_ARCHIVE_KEY_FILE` from a secret store (see
[Configuration](../reference/configuration)).

### Changing keys

| Task | Command | Downtime |
|---|---|---|
| Add a recovery key | `./ma encryption add-key` | none |
| Remove a key that is no longer needed | `./ma encryption remove-key FINGERPRINT` | none |
| Replace the archive key (routine) | `./ma encryption rotate-key --new-key-file /keys/archive-2.key` | a restart |
| The archive key file is lost | configure a recovery key, then `./ma encryption rotate-key --new-key-file /keys/archive-2.key --replace FINGERPRINT` (below) | a restart |
| A key leaked | remove or replace it, then `./ma encryption rekey` | as for `enable` |

`./ma encryption status` lists the slots with their fingerprints. The
commands that change the keyring first reconcile `data/keys/keyring` with
its copy in the database, so they need the database, and a key added to a
keyring file restored from a backup keeps its slot. `add-key`, `remove-key`
and `rotate-key` may run while the web server and the worker run; they
refuse while `enable`, `disable` or `rekey` runs.

**rotate-key** writes a new archive key and replaces exactly one slot with
a slot for it, labelled `archive key`: by default the configured key's, or
the one named by `--replace FINGERPRINT`. Without `--replace` it refuses,
and changes nothing, when the configured key is not labelled
`archive key` (a recovery key is configured): it names the slots, and you
pass `--replace` to choose, also with the configured key's own fingerprint.
Only that slot goes; every other slot stays, and no message file is
touched. The command prints the slots that exist afterwards, and if no
recovery key remains, it says to add one with `./ma encryption add-key`. Then set
`MAIL_ARCHIVE_ARCHIVE_KEY_FILE=/keys/archive-2.key` in `.env` and restart
(`docker compose up -d`); the replaced key no longer opens the keyring.

**The archive key file is lost.** A recovery key opens the keyring
instead:

1. Look up the lost key's fingerprint: `./ma encryption status` lists the
   slots, the lost one labelled `archive key`.
2. Configure the recovery key: put it in `MAIL_ARCHIVE_ARCHIVE_KEY` in
   `.env` (and remove `MAIL_ARCHIVE_ARCHIVE_KEY_FILE`).
3. Run, with the lost key's fingerprint:
   `./ma encryption rotate-key --new-key-file /keys/archive-2.key --replace FINGERPRINT`.
   Without `--replace` it refuses, since the configured key is a recovery
   key.
4. Set `MAIL_ARCHIVE_ARCHIVE_KEY_FILE=/keys/archive-2.key`, remove
   `MAIL_ARCHIVE_ARCHIVE_KEY` again, and restart.

The recovery key keeps its slot. If the lost key may have been stolen
rather than misplaced, run `./ma encryption rekey` afterwards, as after a
leak (below).

**rotate-key alone does not help after a key leaked.** The old key,
together with any older copy of the keyring (every backup and every copy
of `data/` holds one), still opens the data keys of that time, and so every
file encrypted with them. After a leak of any key:

1. Remove the leaked key's slot: for a recovery key
   `./ma encryption remove-key FINGERPRINT`; for the archive key
   `./ma encryption rotate-key --new-key-file /keys/archive-2.key` (with
   `--replace FINGERPRINT` if the archive key is not the configured one),
   which removes the replaced key's slot, and set `.env` as above.
2. Stop everything that writes, as for `enable`
   (`docker compose stop web worker`), and run `./ma encryption rekey`
   (see below for the recovery keys it keeps).
3. Start again: `docker compose up -d`.

`rekey` creates a new data key and a new keyring key, then re-encrypts
every message file under the new data key. Afterwards the leaked key opens
neither the keyring nor the new data key, whatever older copy of the
keyring it is used with. **Backups made before stay readable with the
leaked key**: they hold the keyring and the data keys of their time.
Replace them by new backups and destroy the old ones, and old snapshots,
where that matters.

**Which slots rekey keeps.** A new keyring key can only be wrapped for keys
at hand, so `rekey`:

- keeps the slot of the configured archive key;
- keeps the slot of each key you give with `--keep-key-file`;
- adds a new recovery key, unless `--no-recovery`;
- **drops every other slot, recovery keys included**, and lists each one.

The new recovery key is printed as soon as its slot is recorded, before the
first file is converted: write it down then, because the old recovery keys
no longer open the keyring from that moment. If the run is interrupted
before you saw it, the rerun lists the slots; add a recovery key with
`./ma encryption add-key`. To keep an existing recovery key instead, write
it to a file in the keys directory that only the container user can read,
pass the file, and delete it afterwards:

```sh
sudo install -m 0400 -o 65532 -g 65532 /dev/stdin keys/recovery.key  # paste the key, then Ctrl-D
./ma encryption rekey --keep-key-file /keys/recovery.key --no-recovery
sudo rm keys/recovery.key
```

`rekey` follows the rules of `enable`: writers stopped, the progress
printed, resumable by running it again, and done only after a full pass
from the start. Each file is decrypted and re-encrypted into `tmp/`,
synchronized, read back and checked against its SHA-256, and then replaces
the old file by an atomic `rename`; the directory is synchronized before
the position is recorded. The old data key stays in the keyring until a
full pass from the start finds no file naming it, so a crash at any point
leaves every message readable. A file that cannot be decrypted (damaged)
stays as it is and is listed, and its data key stays too; restore a good
copy from a backup, or delete the file once checked. The downtime is about
that of `enable` (table above): each file is read, decrypted, encrypted,
written and read back once, on one core, with at most 32 MiB of memory and
one batch of extra disk space.

**Old backups need the keys of their time.** A backup opens with the
archive key or a recovery key that had a slot when it was made, and with
the keyring it contains. Keep a replaced or removed key, offline, as long
as backups made before its removal exist.

**Restoring a dump from before a rekey** (`./ma restore-dump`, see
[Encrypted backups](#encrypted-backups)) next to the data directory after
it keeps the newer keyring: its current data key and its slots win, so a
key whose slot the rekey dropped still opens nothing. The data keys are the
union of both copies, so the old data key that the rekey deleted comes back
into the keyring, which `restore-dump` reports as "merged both copies".
That is harmless: it is sealed under the new keyring key, and the dump with
its keyring copy holds it anyway. The next `rekey` deletes it again.

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

With the bundled `docker-compose.yml`, the web server and the worker also
apply pending migrations themselves when they start
(`MAIL_ARCHIVE_MIGRATE_ON_START=on`, see
[Start-up migrations](#start-up-migrations)), so `docker compose up -d web`
alone upgrades the database too. `./ma migrate` first shows the migrations
and their messages in the terminal and finds a problem before the web
server is down; both use the same lock.

With the [worker service](#background-jobs) on (`COMPOSE_PROFILES=worker`),
restart it too: `docker compose up -d web worker` instead of
`docker compose up -d web`, here and in the steps below.

With the SMTP server's certificate mounted by `docker-compose.smtp.yml`
([SMTP certificate](#smtp-certificate)), every `docker compose` command for
the web server needs that file too: set
`COMPOSE_FILE=docker-compose.yml:docker-compose.smtp.yml` in `.env` once, or
add `-f docker-compose.yml -f docker-compose.smtp.yml` to each command here.

`docker compose up -d web`, `docker compose stop` and `docker compose restart`
give the web server up to 45 seconds to shut down. It ends syncs and fills
right away and records how far they got, so they resume at the next run.
Open HTTP requests get 10 seconds and SMTP connections (journaling) 30
seconds; mail still in transfer is stored or, if it is cut off, retried by
the sending mail server. Compose commands that recreate or restart `postgres`
together with `web` (`docker compose up -d`, `docker compose restart postgres`)
restart `web` too, so its start-up checks run again; `./ma`
starts only `postgres`.

The new bundle replaces `docker-compose.yml`, `docker-compose.smtp.yml`,
`./ma`, `.env.example` and `README.txt` and keeps `.env` and `data/`. Compare `.env.example` with your
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

`./ma restore-dump` needs the image of the same release as `./ma`: `./ma`
passes the holder in the image what it read about the dump (since 1.5.2).
An older `./ma` with a newer image is refused with "./ma is older than the
image"; a newer `./ma` with an older image (for example a kept
`MAIL_ARCHIVE_IMAGE` line) fails with "unknown flag: --dump-name". Both
stop before anything is restored; fix the `MAIL_ARCHIVE_IMAGE` line as
above.

With a clone of the repository (see [From source](./getting-started#install)),
`git pull` replaces the download, and `./ma migrate` rebuilds the image for
every service, so the web server and a cron job that uses `docker compose
run` both run the new version afterwards.

Two `./ma migrate` runs at the same time wait for each other, and after a
killed `migrate` the next one may wait for the database server to end the
killed statement; see [`migrate`](../reference/cli#migrate) for the messages
and time limits.

### Start-up migrations

`serve` and `worker` take the migration lock of `./ma migrate` when they
start, and hold it until they have checked the database schema:

- With `MAIL_ARCHIVE_MIGRATE_ON_START=on` (the default in the bundled
  `docker-compose.yml`), they first do what `./ma migrate` does: the
  one-time index rebuild of a database from the `postgres:18-alpine` image,
  the pending migrations and the password upgrade. The log names the pending
  migrations and each one applied.
- Then they check the schema. With migrations still pending (start-up
  migrations `off`, the default of the binary outside Compose), they refuse
  to start: `the database schema is not up to date: 1 migration
  (00039_instance_state.sql) pending; run ./ma migrate, or set
  MAIL_ARCHIVE_MIGRATE_ON_START=on`. A database that a newer version
  migrated is refused too, with both versions; see
  [Going back after an upgrade](#going-back-after-an-upgrade).
- On a new instance (no user, no message, no state of encryption at rest
  yet), the first of them to get the lock turns encryption at rest on with
  the archive key, or refuses to start without one (exit code `4`); see
  [Encryption at rest](#turning-it-on). It refuses too, naming it, while a
  command-line writer such as `./ma import` runs.
- The web server then checks the secret key against the check value the
  database keeps, and refuses another key; see
  [The secret key](../reference/security#the-secret-key). The worker does
  not need the key.
- Last, while no user exists, the web server stores the hash of the
  configured setup token, or revokes a stored one when none is configured;
  see [Start from configuration](./getting-started#start-from-configuration).

While it starts, the web server already listens: `/healthz` answers `503`
with `{"status":"starting"}` and every other request `503` with
`Retry-After: 5`, so a monitor tells a start that waits or migrates from a
server that is down. Once the start is complete the same listener serves
the web UI and `/healthz` answers `ok`.

The other commands that use the database (`./ma user add`, `./ma sync`,
...) refuse in the same way while migrations are pending or the database is
newer; `./ma backup` and `./ma status` only warn, so the backup before an
upgrade works. `./ma status` ends with a `schema:` line.

**Several starts at the same time.** The web server, the worker and
`./ma migrate` started together wait for each other: the first applies the
migrations, the others then find nothing left to do. Each migration is
applied once. A process that waits logs `waiting for the migration lock`
(or prints `another migrate is running`); the wait has no time limit,
because a first migration may rebuild indexes for many minutes. Processes
of the older version that still run are not stopped by this: Compose stops
them when it recreates the containers, so update web and worker together.

**Backups.** A start does not take a backup: the image has no `pg_dump`.
Take one before every upgrade (`./ma backup`, or a snapshot of the volumes),
also when an image update starts the migration. Before it migrates an
existing database, the start logs the time of the last `./ma backup` from
the audit log, and a warning when there is none from the last 26 hours.
The warning does not stop the start: a snapshot leaves no audit entry.

**Exit codes.** `serve` and `worker` end with exit code `3` when a
migration failed (see below) and `4` when a start-up check refused, such as
pending migrations without start-up migrations, a database that is newer
than the binary, or, for `serve`, a missing, unprotected or wrong secret
key. Other errors end with `1`. Commands that use the database
refuse with `4` as well, and `./ma migrate` ends with `3` when a migration
failed. Under Compose, `restart: unless-stopped` starts the container again
after either code; the log shows why.

#### When a migration fails

Each migration runs in a transaction of its own. When one fails, it is
rolled back completely; the migrations before it in the same run stay
applied. The process logs an `ERROR` line and ends with exit code `3` and a
message that names the failed migration, the error, and the migrations this
run kept:

```
error: migration 00041_example.sql failed and was rolled back: ERROR: ... (SQLSTATE 23505)
applied in this run and kept: 1 migration (00040_other.sql)
starts do not run it again while the file is unchanged; ./ma migrate does
restore the backup from before the upgrade, or fix the cause and start again (docs/guide/operations.md, "When a migration fails")
```

An error that comes back on the same data (invalid data, a constraint, a
syntax or permission error, a check the migration raises itself) is
recorded in the database with the migration's number and a hash of its
file. The next start with the same migration refuses at once with exit
code `3`, without running it again, so a restart loop does not repeat a
long failing statement every minute. An error that may pass (a lock or
statement timeout, a lost connection, a full disk, a cancelled start) is
not recorded: the next start runs the migration again. A migration that
fails before the upgrade to 1.6.0 created the record's table is only
logged.

Then either:

- restore the backup from before the upgrade (see [Restore](#restore)) and
  start the old version again, or
- fix the cause, for example the data a constraint refuses, and run
  `./ma migrate`. It runs the migration again also when it is recorded,
  and clears the record when it succeeds. A new image whose migration file
  differs is run again at start as well.

A failed one-time index rebuild (databases from the `postgres:18-alpine`
image of versions before 1.0.0, see [Database locale](#database-locale)) is
not recorded and runs again at every start.

#### Going back after an upgrade

From 1.6.0 on, an older version refuses a database that a newer one
migrated: `the database schema is at migration 40, newer than this
binary`. Versions before 1.6.0 do not check and start anyway, on a schema
they do not know; going back to one needs the restore all the same. To go
back:

1. Stop the web server and the worker: `docker compose stop web worker`.
2. Restore the backup from before the upgrade (see [Restore](#restore)).
3. Start the old image: set `MAIL_ARCHIVE_IMAGE` in `.env` to it (see
   [Versions](#versions)) and run `docker compose up -d web`.

Mail archived since the upgrade is not in that backup; the message files
stay in `data/` and `./ma verify` lists them as orphans. Rolling back with
`./ma migrate down` instead deletes the data of each migration it undoes
and is meant for development.

### Versions

Versions follow [semantic versioning](https://semver.org/), `X.Y.Z`, from
`1.0.0` on:

- a **major** release (`2.0.0`) has a breaking change that needs manual steps
  when upgrading (see the release notes);
- a **minor** release (`1.5.0`) adds features and may change the database
  schema; then it needs `./ma migrate`, or a start with
  [start-up migrations](#start-up-migrations) on;
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
Pinning an exact version is safer than `latest`. **Do not update images
automatically** (Watchtower, Renovate, Diun, or a floating tag such as
`latest` or a major tag like `1`): with
[start-up migrations](#start-up-migrations) on, as in the bundled
`docker-compose.yml`, the web server migrates the database at its next
start, without the backup an upgrade needs before it; with them off, it
refuses to start until `./ma migrate` runs. A tool on `latest` also installs
breaking changes without their upgrade steps. If a tool updates the image anyway, pin an exact
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
| A version before 1.6.0 | Nothing to run, if the web server has `MAIL_ARCHIVE_SECRET_KEY`. An instance that already has users counts as set up: a setup token never works on it, and encryption at setup leaves it alone. An instance with neither a user nor a message, on which encryption at rest was never turned on, counts as new: the web server and the worker refuse to start without an archive key (exit code `4`, `new instances start encrypted`). Then either configure the archive key (`MAIL_ARCHIVE_ARCHIVE_KEY_FILE`, see [The archive key](./getting-started#the-archive-key)), and the next start turns encryption at rest on; or create the first admin before the web server starts again (`./ma migrate`, then `./ma user add NAME --admin`), and the instance starts without encryption at rest until you turn it on (same section). `MAIL_ARCHIVE_ENCRYPTION_AT_SETUP=off` is for test instances only. The first start of 1.6.0 checks the secret key: if accounts with stored passwords or users with TOTP exist, the key must open them, otherwise the start is refused with exit code `4` (then configure the key the instance used, or see [`secret-key accept`](../reference/cli#secret-key)); then the database keeps a check value of the key, and every later start with another key is refused. A web server without a secret key no longer starts in browse-only mode: create one with `./ma keygen`. The bundled `docker-compose.yml` no longer requires `MAIL_ARCHIVE_SECRET_KEY` in `.env`, because the key may come from a file instead (`MAIL_ARCHIVE_SECRET_KEY_FILE`, see [Configuration](../reference/configuration#docker-compose-only)), and no longer gives it to the worker service. |
| A version without deletion marks per customer group (before 1.6.0) | Stop the web server and the worker before upgrading: `docker compose stop web worker`, then `./ma migrate` and `docker compose up -d web` (`web worker` with the worker on). The migration only adds an empty table and is quick; it neither reads nor rewrites existing rows. Deliveries, syncs and search go on; retention runs and purges wait at most a moment while it adds its foreign keys (to `deleted_messages`, `deletion_runs` and `account_purges`). From then on a retention run marks every deleted message with the accounts and customer groups that held it, and a purge every purged message with the account and its group, so a deleted message is skipped only there (see [Deletion](../reference/cli#deletion) and [Security](../reference/security#login)). Deletions recorded before need no step: they count as deleted in every group that holds no copy. A process of a version before 1.6.0 that still runs after the migration behaves as before 1.6.0: its syncs, fills and deliveries skip and store messages as before, and its retention runs and purges record their deletions without accounts and groups, so these count in every group, and a purge of a message that another account keeps is not recorded at all. Rolling back the migration drops the marks: every recorded deletion counts in every group again, and messages purged while another account kept them are archived again. |
| A version without SMTP login (before 1.6.0) | Only with the SMTP server on (`MAIL_ARCHIVE_SMTP_LISTEN` set). `MAIL_ARCHIVE_SMTP_AUTH=required` is the new default: mail servers log in with an [SMTP secret](../reference/cli#smtp-secrets) over `STARTTLS`, and without `MAIL_ARCHIVE_SMTP_TLS_CERT` and `MAIL_ARCHIVE_SMTP_TLS_KEY` the web server and every `./ma` command refuse to start, naming the variable. Add `MAIL_ARCHIVE_SMTP_AUTH=off` to `.env` **before** the upgrade: a setup that relies on the allowlist alone, such as a Postfix on the same host, then keeps working unchanged. To move to logins afterwards, in this order: upgrade with `off`; create a secret per smtp account (`./ma account smtp-secret NAME --new`, which needs the new version's migration); change the mail server ([Log in with an SMTP secret](./journaling#log-in-with-an-smtp-secret)); put the certificate in place ([SMTP certificate](#smtp-certificate)); remove `off` from `.env` and run `docker compose up -d web`. A setup that already has a certificate starts in `required` mode and answers `451` to mail servers that do not log in yet: their copies wait in the queue (5 days by default) until they log in or `off` is set. The SMTP server now takes 16 connections at once instead of 10. |
| A version without storage usage (before 1.6.0) | Nothing to run. The migration adds the storage counters and an index over the hashes and sizes of the messages, and holds back every write of messages (syncs, SMTP deliveries, fills, imports, `reindex`) until it is done; searches and reads go on. It reads the message table once, from disk if it is not in memory. Measured on PostgreSQL 16 with 2 cores and 300,000 messages (511 MB of message rows): about 0.6 seconds with the rows in memory, about 4.5 seconds with them on disk (138 MB/s); for 1,500,000 messages (about 2.6 GB) expect about 3 seconds in memory and 20 to 30 seconds from disk. `serve` and `worker` migrate before they work (`serve` answers `503` until then); a separate worker or a `./ma sync` from cron waits for the migration. With a large archive, stop the worker before the upgrade (`docker compose stop worker`). After the upgrade, `./ma usage` shows the groups and accounts as `-` until the background jobs of `serve` or `worker` have taken the first snapshot, soon after their start. Rolling the migration back (`./ma migrate down`) drops the storage figures and the group quotas. |
| A version without attachment text per customer group (before 1.6.0) | Stop the web server and the worker before upgrading: `docker compose stop web worker`, then `./ma migrate` and `docker compose up -d web` (`web worker` with the worker on). The migration adds a column to the message locations and an index over the few locations that do not show their attachment text yet; every existing location keeps what it shows. Building the index reads every location and holds an exclusive lock on the locations until it is done: everything that reads or writes them waits, also a web server of the old version that still runs (its deliveries, syncs and search). Measured on PostgreSQL 16 with 2 cores: about 0.2 seconds per 1,000,000 locations with them in memory, longer when they are read from disk. From then on, each customer group searches a message's attachment text only after an extraction for that group, so a message that a second group receives is extracted once more in the background (see [Security](../reference/security#login)). A process of a version before 1.6.0 that still runs after the migration behaves as before 1.6.0: its search shows attachment text as before, and the locations it stores show none until their extraction; the web server or worker of 1.6.0 queues those extractions at its start and every 15 minutes after the old processes have stopped. Rolling back the migration (`./ma migrate down`, for development) drops the column, and migrating up again shows the attachment text through every location. |
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

Each customer group searches a message's attachment text only after an
extraction for that group (see [Security](../reference/security#login)), so
a message that a second group receives is extracted once more. A process
that runs the jobs also checks at its start and every 15 minutes that no
message waits for such an extraction without a job, and queues the ones
that do (log line `attachment text: repaired locations`); this catches up
on what a process of a version before 1.6.0 stored while it still ran
after the upgrade.

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

**Storage usage** (`./ma usage`, see
[Storage usage](../development/architecture#storage-usage)) costs, measured on
PostgreSQL 16 pinned to 2 cores, with 300,000 messages in 166 accounts and
21 groups (750,000 locations); PostgreSQL 18 was not measured:

| What | When | Cost |
|---|---|---|
| Row locks of a save (account and owner, `FOR KEY SHARE`) | every save transaction | under 0.03 ms and about 120 bytes of WAL |
| Group lookup of a save (`GroupHeldMessages`) | every sync, fill or import batch of up to 100 messages new to the account | grows with the folders of the account's group: 6 ms with 40 folders, 71 ms with 400, 239 ms with 1,500 (50 accounts × 30 folders; 364 ms at the 95th percentile) |
| The same | every message received over SMTP | 0.9 ms with 40 folders, 2.7 ms with 400, 7 ms with 1,500 |
| Snapshot (`usage_refresh`) | hourly, and after moves, purges, deletion runs, imports and restores | 4 seconds of one core, up to 175 MiB of PostgreSQL memory (`work_mem` 32 MB; each hash may use up to 64 MB, as PostgreSQL's `hash_mem_multiplier` is 2) and 220 MiB of temporary files; at 1,500,000 messages about 12 seconds and 650 MiB of temporary files, the same memory |
| Fold (`storage_fold`) | every 5 minutes | under 25 ms |
| The index over hashes and sizes | on disk | about 100 bytes per message: 30 MB per 300,000 messages |

The snapshot's memory fits in the headroom of PostgreSQL's share above; it
runs in one job slot after the work of new mail. The group lookup looks only
into the group's own folders, so that its time tells nothing about other
groups; a group with many thousands of folders makes its syncs slower by
about 0.16 ms per folder and batch.

## IMAP limits

Syncs, fills and login checks talk to IMAP servers that the archive does
not control. A server that stops answering, sends without end, or offers
a folder far larger than any real one must not hold the web server's only
sync worker, use up its memory or keep a core busy. These limits apply;
none of them is reached by a normal mailbox.

### Deadlines

| What | Limit | When it is reached |
|---|---|---|
| Connection setup (TCP, TLS, greeting, STARTTLS) | 30 s | The run fails (`connect` or `timeout`). |
| Server silent while a command runs | 2 min, `MAIL_ARCHIVE_IMAP_IDLE_TIMEOUT` | The connection is closed and the folder fails; the next folder gets a new connection. The error names the command and the setting. Between commands no deadline applies, so slow database work never breaks a connection. |
| LOGOUT | 5 s | The connection is closed anyway. |
| A background sync of one account, or a run of a fill | 60 min (the web server; `./ma sync` and `./ma fill` have none) | What was committed stays. If the run committed messages, it ends `partial` and the account is queued again behind the accounts waiting; a fill stays running and is queued again behind the other fills. Without progress the run fails and counts as a failure for [alerts](#monitoring-and-alerts). |
| A login check (adding or changing an account, a fill's folder list) | 60 s in all | The check fails. |

### Sizes and counts

| What | Limit | When it is reached |
|---|---|---|
| Data the client did not ask for, per connection | 64 KiB at the start, at most 256 KiB; each command adds 16 KiB, each response the archive uses 512 B plus its size, each byte of a message body 1¼ B | The connection is closed (`unrequested data`). A real server stays far below it. Before a body fetch the archive asks for the folder's newest UID and sends no fetch when nothing is new, so the one message a server returns for an empty `UID FETCH n:*` is never downloaded. Only if the newest messages of a folder are deleted on the server in the moment between those two commands can that message still exceed the budget; the folder then fails once and syncs in the next run. |
| Read by one login check | 16 MiB, or 1.1 KiB per folder of `MAIL_ARCHIVE_IMAP_MAX_FOLDERS` if that is more | The check fails. |
| Folders of one account | 10,000, `MAIL_ARCHIVE_IMAP_MAX_FOLDERS` | The whole sync, fill or login check fails; the error names the setting. |
| Length of a folder name | 1,024 bytes | As above. |
| Flags of one message | 1,000 | The folder fails. |
| Messages a FETCH may answer | the folder's message count at the start + 1,000 | The folder fails. |
| A message | 256 MiB, as for imports | See [A message over 256 MiB](#a-message-over-256-mib). The size is checked before anything is downloaded. |
| Header of one message in a fill's listing | 128 KiB | The folder fails. |
| Messages a reconcile compares | 2,000,000 per folder, `MAIL_ARCHIVE_RECONCILE_MAX_MESSAGES` | The folder is not reconciled; its sync ends `partial`, and the error names the setting. |
| Distinct flag combinations in one reconcile | 4 MiB, counting each combination's bytes plus 88 B for its entry in memory | The folder is not reconciled; real folders have a few hundred. |
| Messages a fill lists of one folder per pass | 500,000, `MAIL_ARCHIVE_FILL_MAX_FOLDER_MESSAGES` | The folder fails; the error names the setting. |
| Message-IDs kept in that listing | 96 bytes per message of the fill limit in all (48 MiB at the default) | As above. A single Message-ID over 998 bytes is not kept: that message counts as one without a Message-ID, so it is imported, never left out as journaled. |
| New connections after a broken one, per run | 5 | The remaining folders wait for the next run; the run is `partial` if earlier folders synced, otherwise `failed`. |

When a cap is reached in a folder, the connection is closed; the next
folder gets a new connection, so one bad folder does not fail the folders
after it.

### Login checks

Adding or changing an account, a fill's folders, starting, switching on or
resuming a fill, and an account's folder list from the server each check the
login first. Besides the minute and the read limit above:

| What | Limit | When it is reached |
|---|---|---|
| Checks per user | 20 in 10 minutes | `429` with `Retry-After`; no connection is made. |
| Checks per client address (IPv6: per `/64`) | 60 in 10 minutes | As above. |
| Failed logins per IMAP server, port and user name | 10 in 10 minutes, whoever makes them; every failure after the login was sent counts. The folder list of a saved account does not count. Kept per spelling: case, a trailing dot and the forms of one IP address count as one, but a DNS alias or another address of the same server counts separately (the limits per user and per address still apply). | As above, until the 10 minutes have passed. |
| Checks at a time | 2 in all, 1 per user | A check waits for its turn up to 15 seconds, then `429` with `Retry-After: 15`. |

These limits are fixed: the server keeps them in memory, and a restart
resets them.

### Error messages

Errors from an IMAP server or the network are shown, stored and sent in
[alerts](#monitoring-and-alerts) as a fixed sentence of their category,
after the folder they occurred in. The folder name comes from the server,
so a hostile server can still put text into a run error and an alert
through it. The server's own answer and the network's error text, which can
name addresses and certificates, go only to the server log
(`docker compose logs web`, the lines `sync failed`, `folder sync failed`,
`fill of a folder failed`, `fill ended`, `fill run ended` and
`imap login check failed`).

| Category | Sentence | Code of a login check |
|---|---|---|
| The server cannot be reached (DNS, connection refused) | cannot connect to the IMAP server | `imap_connect` |
| TLS handshake or certificate | the secure connection to the IMAP server failed | `imap_tls` |
| The server refused the login | the IMAP server refused the login | `imap_login` |
| No answer in time: the idle deadline, a login check's minute | the IMAP server did not answer in time (during COMMAND) | `imap_timeout` |
| The server refused another command (NO or BAD) | the IMAP server refused a command | `imap_server` |
| An answer that cannot be read | the IMAP server sent an answer that cannot be read | `imap_protocol` |
| A limit above was reached | the IMAP server exceeded a limit (WHAT: N) | `imap_limit` |

A limit an operator can raise is followed by the setting, as in "…; raise
`MAIL_ARCHIVE_IMAP_MAX_FOLDERS`". The web UI shows a login check's error in
the user's language. Run errors stored before 1.5.3 keep their old text until
the account's or fill's next run replaces them.

### A message over 256 MiB

Each sync stops that folder right before the message: messages with a
higher UID in that folder are not archived until the message is deleted or
moved on the server. Other folders sync as usual. The run ends `partial`
with the error "a message (UID n) is larger than 256 MiB; later messages in
this folder are not archived until it is removed from the server", and it
counts as a **failure** for [alerts](#monitoring-and-alerts): after
`MAIL_ARCHIVE_ALERT_AFTER_FAILURES` such runs the "failing" alert names the
folder, and "recovered" follows once the message is gone. `/healthz/sync`
shows the account as failing meanwhile, since it is not fully archived. A
recurring fill holds such a message the same way.

### Settings and their memory

The four settings are read at start and refused outside their range. Their
defaults fit the [4 GB budget](#hardware-and-budgets): with every IMAP limit
reached at once (one sync, one fill and two login checks), the IMAP work
needs about 280 MiB of live heap, about 560 MiB with the garbage
collector's headroom, of the web server's 768 MiB. A real mailbox stays far
below that, under 30 MiB.

| Setting | Default | Range | Memory for raising it (with the garbage collector's headroom) |
|---|---|---|---|
| `MAIL_ARCHIVE_FILL_MAX_FOLDER_MESSAGES` | 500,000 | 10,000 to 10,000,000 | about 42 MiB per 100,000 messages |
| `MAIL_ARCHIVE_RECONCILE_MAX_MESSAGES` | 2,000,000 | 10,000 to 20,000,000 | about 9 MiB per 100,000 messages |
| `MAIL_ARCHIVE_IMAP_MAX_FOLDERS` | 10,000 | 100 to 200,000 | up to about 13 MiB per 1,000 folders (folder names of 1,024 bytes; real ones need a quarter of that) |
| `MAIL_ARCHIVE_IMAP_IDLE_TIMEOUT` | 2m | 30s to 30m | none; a silent server holds the sync worker longer |

**Raise the web server's memory with them.** The web server sets no memory
limit by default; on a 4 GB host a raised limit takes its memory from the
page cache. A host that sets one (see [Hardware and budgets](#hardware-and-budgets))
adds the cost above to both `mem_limit` and `GOMEMLIMIT`. Set them in `.env`
and restart with `docker compose up -d web`; Compose passes them to the web
server and to `./ma`.

### CPU

Data that no code of the archive uses is bounded by the limits above. In
the worst case a misbehaving server costs **about 40 to 95 seconds of CPU per
background run**, on one core and only during that account's turn, plus
512 B per used response, which is far less than storing the message. A
login check costs **about 1 to 2 seconds** of CPU at most; with
`MAIL_ARCHIVE_IMAP_MAX_FOLDERS` raised, about 0.1 s per 1,000 folders of the
limit more.

## SMTP journaling

The SMTP server receives the copies of [journaling](./journaling). On a
hosted instance, customers' mail servers connect over the internet and log
in with an [SMTP secret](../reference/cli#smtp-secrets)
(`MAIL_ARCHIVE_SMTP_AUTH=required`, the default); see
[The SMTP server](../reference/security#the-smtp-server) for what protects
the port.

### The hosted SMTP port

```sh
MAIL_ARCHIVE_SMTP_LISTEN=:2525
MAIL_ARCHIVE_SMTP_HOSTNAME=archive.customer.example
MAIL_ARCHIVE_SMTP_TLS_CERT=/tls/fullchain.pem
MAIL_ARCHIVE_SMTP_TLS_KEY=/tls/privkey.pem
SMTP_BIND=0.0.0.0
SMTP_PORT=587
COMPOSE_FILE=docker-compose.yml:docker-compose.smtp.yml
```

The log shows `smtp on` with `auth=required`, the certificate's
`cert_not_after` and `max_conns_per_addr`.

- **Port 587.** Many networks block outgoing connections to port 25, but not
  to 587. The archive speaks plain SMTP with `STARTTLS` there; implicit TLS
  (port 465) is not offered.
- **The real client address.** The failed-login limit and the cap per
  address count the address the connection comes from. Publish the port
  directly: Docker with `"userland-proxy": false` in the daemon's
  `/etc/docker/daemon.json` (otherwise, for example, IPv6 clients reach an
  IPv4-only container network through a proxy and all share its address),
  or host networking, or a TCP proxy that does not translate addresses. A
  load balancer that does translate them puts every customer behind one
  address, and one scanner then blocks them all. The PROXY protocol is not
  supported.
- **Connections.** At most 16 at once, at most 8 per client address
  (`MAIL_ARCHIVE_SMTP_MAX_CONNS_PER_ADDR`), and 60 seconds to log in. A
  customer's Postfix with one transport per smtp account and `maxproc` 2
  ([Log in with an SMTP secret](./journaling#log-in-with-an-smtp-secret))
  opens up to 2 connections per account: raise the cap for a customer with
  more than 4 smtp accounts behind one address. A few addresses that hold
  connections without logging in can take every slot; each is closed after
  60 seconds at the latest, and in the meantime the customers' mail servers
  get `421` and retry later, so the copies wait in their queue but are not
  lost.
- **Failed logins** are logged as `smtp auth failed` with the client address
  and a reason; 20 within 15 minutes block that address for 15 minutes
  (`smtp auth blocked`). A customer whose mail server logs in with an old or
  wrong secret sees its copies wait in the queue: Postfix keeps them after a
  `535` (`smtp_sasl_auth_soft_bounce`, on by default).
- **Refusals before a login cost little:** after 3 refused logins or `MAIL`
  commands on one connection the archive answers the third and closes it
  (`smtp connection closed`), so a client cannot fill the log by sending
  refused commands back to back.
- **Log rotation.** A public port is scanned all the time, and every
  scanner leaves a few lines. Docker's default `json-file` log driver does
  not rotate: give the daemon a limit in `/etc/docker/daemon.json`, for
  example `{"log-driver": "local"}` (keeps at most 100 MB per container) or
  `{"log-opts": {"max-size": "20m", "max-file": "5"}}`, then restart Docker
  and recreate the containers (`docker compose up -d --force-recreate`).
  Keep the logs on a disk where a full log cannot stop PostgreSQL.

### SMTP certificate

The SMTP server uses a certificate the operator provides, as PEM files, for
the name in `MAIL_ARCHIVE_SMTP_HOSTNAME`; there is no ACME client in the
archive. Customers' mail servers verify it, so it must come from a public CA
(Let's Encrypt with certbot or lego, or a copy of the one Caddy gets for the
same name).

- **Compose mounts it** from `SMTP_TLS_DIR` (default `./tls`) as `/tls` with
  `docker-compose.smtp.yml`. Every command that starts or updates the web
  server needs both files: `docker compose -f docker-compose.yml -f
  docker-compose.smtp.yml up -d web`, or once
  `COMPOSE_FILE=docker-compose.yml:docker-compose.smtp.yml` in `.env`, which
  `docker compose` and `./ma` read. The file is separate so that an instance
  without SMTP needs no `./tls`, and Docker never creates a missing directory
  as root.
- **The key** must be readable by the container user (UID 65532) and by
  nobody else: `chown 65532 tls/privkey.pem && chmod 0400 tls/privkey.pem`.
  A key every user of the machine can read is logged as a warning.
- **Renewal without a restart.** When a client connects, the server looks at
  the two files at most once a minute and loads them again when one has
  changed (`smtp certificate reloaded`). Copy the renewed files in with a
  deploy hook; mounting certbot's `live/` directory alone does not work,
  because its files are links into `archive/`. For certbot:

  ```sh
  #!/bin/sh
  # /etc/letsencrypt/renewal-hooks/deploy/mail-archive-smtp
  set -eu
  dir=/srv/mail-archive/tls
  install -m 0644 "$RENEWED_LINEAGE/fullchain.pem" "$dir/fullchain.pem.new"
  install -m 0400 -o 65532 "$RENEWED_LINEAGE/privkey.pem" "$dir/privkey.pem.new"
  mv "$dir/fullchain.pem.new" "$dir/fullchain.pem"
  mv "$dir/privkey.pem.new" "$dir/privkey.pem"
  ```

  A pair that does not load, such as a certificate and a key that do not
  match yet, leaves the current one in use; the error is logged once and the
  pair is tried again at the next check.
- **Expiry.** From 14 days before the certificate expires, the log warns at
  start and then at most once a day while mail servers connect (the check
  runs on connections; `smtp certificate expires soon`); an expired
  certificate is logged as an error the same way. The web server keeps
  running, but mail servers that verify the certificate no longer log in and
  queue the copies.

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
  [Reconcile](./syncing#alert-for-mail-deleted-on-the-server). Storage
  alerts report the quota state of the instance and of each customer
  group ([Storage alerts](#storage-alerts)). By default an alert names
  accounts, fills and groups by ID only ([Detail](#detail-ids-or-full)).
- **`/api/metrics`** gives admins and their monitoring the instance's
  figures in the Prometheus text format, IDs and counts only; see
  [Metrics](#metrics).
- **`/healthz`** answers `ok` once the web server is ready, and `503` with
  `{"status":"starting"}` while it starts (it waits for a running
  `./ma migrate` or applies migrations): a monitor tells starting from down.
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

Anyone who knows a topic on the public ntfy.sh server can read it. Alerts
carry IDs only by default, but they still tell when and how often syncs
fail. Use a long random topic, or better a protected topic with an access
token:

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

### Detail: ids or full

Webhook receivers are often services of third parties (ntfy.sh, Slack,
Discord). So by default (`MAIL_ARCHIVE_NOTIFY_WEBHOOK_DETAIL=ids`) an alert
names accounts, fills and customer groups by their ID only: no account or
owner names, no IMAP user names (`mailbox`), no folder names and no error
texts, and `title` and `message` are fixed text with IDs, for example
"Mail archive: sync of account 3 keeps failing" and "Account 3 failed 3
syncs in a row since 2026-10-01 12:00 UTC.". The entries in `accounts` have
no `account`, `owner`, `lastError`, `mailbox`, `folders` or `moreFolders`
field. `./ma status` shows which account an ID is, and `./ma usage --json`
which account or group.

`MAIL_ARCHIVE_NOTIFY_WEBHOOK_DETAIL=full` adds the names, mailboxes, folder
names and the last error (up to 200 characters). That sends customer data
to the webhook's operator: use it only for a receiver you run yourself, for
example on a test instance. The examples below show `full`.

**How names and errors appear** (with `full`). Account and owner names, mailboxes, sync
errors and folder names come from users and mail servers, so alerts show
them as literal text, never as formatting, links or mentions:

- In `text` and `content` each of them is inline code, for example
  `` Account `work` (owner `anna`, ID 3) ``. A backtick in a name shows as
  `'`. In `text`, `&`, `<` and `>` are written as `&amp;`, `&lt;` and `&gt;`,
  which Slack shows as the characters; Mattermost shows them as written,
  inside the code. `content` carries `"allowed_mentions": {"parse": []}`, so
  Discord pings no one. A message that is too long for Discord (2,000
  characters) or for Mattermost (16,000 here) lists the first accounts and
  ends with "… and N more (see accounts)"; a single very long name is cut
  with "…". `accounts` always has all of them, unchanged.
- `title` and `message` (Gotify and generic receivers) and ntfy's message
  are plain text. The body carries
  `"extras": {"client::display": {"contentType": "text/plain"}}`, so Gotify
  never renders Markdown; ntfy gets no `Markdown` header. ntfy's `Title`
  header is always RFC 2047 encoded.
- Everywhere, line breaks, other control characters, the Unicode line and
  paragraph separators and the bidirectional controls in a name or error
  become a space, so a value cannot start a new line or reorder the text.

**Anything else:** the `json` format posts:

```json
{
  "id": "3-1759320000-failing",
  "event": "failing",
  "title": "Mail archive: sync of work keeps failing",
  "message": "Account \"work\" (owner anna, ID 3) failed 3 syncs in a row since 2026-10-01 12:00 UTC.\nLast error: …",
  "text": "…", "content": "…",
  "allowed_mentions": {"parse": []},
  "extras": {"client::display": {"contentType": "text/plain"}},
  "accounts": [{"id": "3-1759320000-failing", "event": "failing", "accountId": 3, "account": "work",
                "owner": "anna", "failureStreak": 3, "failingSince": "2026-10-01T12:00:00Z", "lastError": "…"}]
}
```

`event` is `failing`, `recovered`, `mixed` (alerts and recoveries in one
message), `gone` (mail deleted on the server), `storage` or `test`.

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

### Storage alerts

When the quota state of the instance or of a customer group changes
(`ok`, `warn`, `full`, `overflow`, and `disk_low` for the instance), one
`storage` message is sent per change, in both detail modes with IDs and
figures only:

```json
{
  "id": "storage:4:2",
  "event": "storage",
  "title": "Mail archive: storage of group 4 at 82% of 50.0 GiB",
  "message": "Storage of group 4: warn since 2026-10-01 12:00 UTC, 41.0 GiB used of 50.0 GiB (82%).",
  "text": "…", "content": "…",
  "accounts": [],
  "storage": [{"id": "storage:4:2", "event": "storage", "level": "warn", "scope": "group", "groupId": 4,
               "usedBytes": 44023414784, "quotaBytes": 53687091200, "percent": 82, "since": "2026-10-01T12:00:00Z"}]
}
```

`scope` is `instance` (then `groupId` is `null`) or `group`. `usedBytes` is
the scope's message bytes after deduplication when the alert is sent;
`quotaBytes` and `percent` are `null` without a quota. The `id` names the
scope (`instance` or the group ID) and counts its changes, so `warn`, `ok`
and `warn` again are three messages with three IDs. Changes that follow
each other faster than they can be sent go out once, with the latest
state. A group that is deleted takes its pending alert with it. With ntfy a
level other than `ok` has the priority `high` and the tag `warning`.
`./ma usage --json` shows which group an ID is. The storage quota checks
write the states; while every state stays `ok`, no storage alert is sent.

Alerts are delivered **at least once**: if the server stops right after the
receiver accepted a message, it is sent again with the same `id`. ntfy,
Gotify and Slack do not drop such repeats; a receiver of your own can use
the `id`. A receiver that is down is retried after 30 seconds, then with
growing pauses up to an hour, and given up after 24 hours (logged as
`notification given up`; a `gone` alert 24 hours after its run). An answer
like `400` or `404` usually means a wrong URL, token or format: it is logged
as an error and retried hourly. Redirects are not followed.

The log records each alert (`alert sent`, `recovery sent`, `loss alert sent`)
with the account and its owner, also with the detail `ids`: logs are
customer data ([Security](../reference/security#operator-admin-and-customer-groups)).
`storage alert sent` names the scope by ID. The log never contains the
webhook URL, the authorization or the message; failed attempts name only
the scheme and host.

### Metrics

`GET /api/metrics` gives the instance's figures in the Prometheus text
format (0.0.4) for a monitoring system. Only admins read it: with their
session, or with an API key with the scope `metrics`, which only an admin
can create (Profile → API keys, with the password and a 2FA code, like
every key) and which has no other scope. Such a key opens no other route,
and a `read` or `sync` key gets `403` here. The owner's role and lock are
checked on every request: a key of an admin who is demoted gets `403`, of
one who is locked `401`. Revoke it in the profile like any key.

A Prometheus scrape job:

```yaml
scrape_configs:
  - job_name: mail-archive
    scheme: https
    metrics_path: /api/metrics
    authorization:
      credentials_file: /etc/prometheus/mail-archive.key   # the ma_… key
    static_configs:
      - targets: ["archive.example.org"]
```

The answer is cached for 15 seconds, so a shorter scrape interval adds
nothing. It holds IDs, counts, times and fixed values only, never a name,
host, IMAP user name, address, folder, subject or error text, because
scrapes often end in monitoring services of third parties:

| Metric | Labels | Meaning |
|---|---|---|
| `mail_archive_build_info` | `version` | Always 1. |
| `mail_archive_schema_version`, `mail_archive_schema_latest_version`, `mail_archive_migrations_pending` | | The database's migration, this binary's newest, and how many are not applied. |
| `mail_archive_storage_messages`, `mail_archive_storage_bytes` | `scope` (`instance`, `group`, `ungrouped`), `group_id` | Unique messages and their bytes: exact for the instance, as of the last snapshot plus what was added since for a group, at the last snapshot for the accounts without a group. |
| `mail_archive_storage_quota_bytes` | `scope`, `group_id` | The quota; absent without one. |
| `mail_archive_storage_state` | `scope`, `group_id`, `state` | 1 for the current quota state, 0 for the others. |
| `mail_archive_storage_state_since_timestamp_seconds` | `scope`, `group_id` | Since when. |
| `mail_archive_storage_snapshot_timestamp_seconds`, `mail_archive_storage_counter_drift_bytes` | | The last usage snapshot and its check of the counters (0 is right). |
| `mail_archive_account_messages`, `mail_archive_account_bytes` | `account_id`, `group_id`, `kind` | Per account that is not removed, at the last snapshot. A message in several accounts counts in each. |
| `mail_archive_account_enabled` | `account_id` | 1 when enabled. |
| `mail_archive_account_health` | `account_id`, `health` (`ok`, `failing`, `stale`) | 1 for the current health, as in `/healthz/sync`. |
| `mail_archive_account_last_success_timestamp_seconds` | `account_id` | The last successful sync of an IMAP account. |
| `mail_archive_disk_free_bytes`, `mail_archive_disk_total_bytes` | | The data directory's file system. The PostgreSQL volume is not measured. |
| `mail_archive_database_bytes`, `mail_archive_database_relation_bytes` | `table` | The database and its largest tables, at the last snapshot. |
| `mail_archive_jobs` | `job_kind`, `state` (`queued`, `retrying`, `running`, `failed`) | Background jobs with work that is not done. |
| `mail_archive_job_oldest_due_seconds` | `job_kind` | How long the oldest due job has waited. |
| `mail_archive_users` | `kind` (`admin`, `member`, `auditor`), `state` (`active`, `locked`) | Users. |
| `mail_archive_encryption_state` | `state` (`off`, `on`, `encrypting`, `decrypting`, `rekeying`) | Encryption at rest: 1 for the current state. |
| `mail_archive_last_backup_timestamp_seconds` | | The newest `./ma backup` in the audit log; 0 when none in the last 30 days. |
| `mail_archive_smtp_storage_deferrals_total` | | SMTP recipients deferred with `452` for storage since the start. |

Exclusive bytes per account (what a purge would free) are computed across
customer groups and are not in the metrics; `./ma usage` shows them. A
label value that does not match its fixed pattern is left out with its
sample, so a name can never end up in a label.

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

Logs contain customer data: SMTP senders and recipients, account, user and
folder names, and the texts of server errors. On a hosted instance, treat
them like the archive: limit who reads them, keep them only as long as
needed, and cover both in the data processing agreement
([Security](../reference/security#operator-admin-and-customer-groups)).
