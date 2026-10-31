# Architecture

One Go binary does everything: the command line, the sync and the web
server with the embedded UI. PostgreSQL holds metadata, the search index and
account settings; message files live on disk.

```
IMAP servers ──(EXAMINE, BODY.PEEK[])──▶ internal/imapsync
                                              │
                                              ▼
                                       internal/archive ──▶ internal/blobstore ──▶ data/messages/ab/cd/<sha256>.eml
                                       (sync, schedule)  ──▶ internal/store ──────▶ PostgreSQL
                                              ▲                      ▲
                                              │                      │
cmd/mail-archive (CLI) ───────────────────────┘                      │
internal/web (JSON API, UI from web/) ───────────────────────────────┘
```

## Packages

| Path | Purpose |
|---|---|
| `cmd/mail-archive` | Command line (cobra). |
| `internal/archive` | Sync of accounts and folders, deduplication, header parsing, the background `Runner` with its schedule, `reindex`, `verify` and `export`. |
| `internal/mailbox` | mbox and Maildir writers for `export`; readers and source walkers (Thunderbird, Apple Mail, Maildir++) for `import`. |
| `internal/imapsync` | Read-only IMAP client on top of go-imap v2. |
| `internal/blobstore` | Content-addressed `.eml` storage: files are named by their SHA-256 and published and removed durably (see [Blob durability](#blob-durability)). |
| `internal/mime` | MIME parsing: text for the search index, parts and HTML for display. |
| `internal/attachtext` | Text of attachments for search: plain text, CSV, TSV, Markdown, HTML, and PDFs through PDFium compiled to WebAssembly in wazero (no file system, network or environment; one PDF at a time per process; a new instance per document). No database. |
| `internal/store` | PostgreSQL access: migrations (goose), queries (sqlc), the per-account sync lock, the blob lock, the attestation fence and the migration lock. Migrations run under goose's session lock; `migrate` and `migrate down` also hold the command lock (advisory lock class `0x6d68`, key 0) for the whole command, on a connection of its own outside the pool, so two runs wait for each other. goose's lock is taken by a locker of our own (`gooselock.go`) on the connection that runs the migrations. goose runs on a short-lived pool of its own (one extra connection per call), whose teardown frees a lock left on a given-up connection (#269); a second run waits for it without a time limit and prints its holder. `migrate` and `migrate down` open the store with `ForMigrations` (`client_connection_check_interval` 10 s and TCP keepalives), so the server ends a killed command's statement, and frees goose's lock, about 10 s after the process died. Status reads take neither; `serve` takes neither. |
| `internal/attest` | Formats of the tamper-evidence chain: the signed text of an attestation, leaves files, the attestation file and the Ed25519 key files. No database. |
| `internal/notify` | Alerts for accounts whose syncs keep failing, and for recurring fills whose runs do: compares each failure streak with what was last announced and posts the difference to a webhook. Leases in `account_sync_health` keep two notifiers from sending the same alert. It also sends the `loss_alerts` that sync runs wrote. |
| `internal/jobs` | Background jobs on the queue in the `jobs` table: the registry of kinds and the bounded pool that `serve` and `worker` run (see [Background jobs](#background-jobs)). |
| `internal/web` | HTTP server, JSON API, request protection, embedded UI (`internal/web/ui`). Every route is listed once in `routes.go`, with the access it needs (session only, login, public, or session and API key with `read` or `sync`); `authenticate` enforces it, `openapi.yaml` describes it, and a test fails when the two differ. A request with `Authorization: Bearer ma_…` is authenticated by its key alone and skips the Origin check (`api_keys.go`). |
| `internal/crypto` | AES-256-GCM for stored passwords. |
| `internal/config` | Configuration from environment variables. |
| `internal/imaptest` | In-memory IMAP server for tests. |
| `internal/testwait` | Waiting in tests: `Signal`, a broadcast, and `Until`, which checks a condition again after each signal instead of on a timer. |
| `web/` | Web UI source (React, TypeScript, Vite, Carbon Design System). |
| `docs/` | This documentation (VitePress). |
| `tools/demo` | Demo server with made-up mail, used for screenshots. |
| `tools/imap-testserver` | IMAP server for the Compose smoke test. |

## Data model

| Table | One row per |
|---|---|
| `users` | Login for the web UI: name, Argon2id hash, admin flag, `locked_at`. |
| `sessions` | Login session: SHA-256 of the cookie token, user, last use, expiry. |
| `accounts` | Account: `kind` (`imap`, or `import` for mail from files), owner (`owner_id`), server, login, encrypted password, folder filters, `enabled`, `removed_at`. Names are unique per owner. Import accounts have port 0 and are never enabled; check constraints enforce both. |
| `folders` | Folder of an account, with its `UIDVALIDITY`, last archived UID, last sync time and `last_reconciled_at`. |
| `messages` | Unique message content (by SHA-256): size, subject, sender, recipients, date, path of the file, body text and search vector, attachment names, the links to other messages (`in_reply_to`, `reference_ids`, `thread_id`) and `index_version`. |
| `message_locations` | Place where a message was seen: folder, `UIDVALIDITY`, UID, flags, internal date, `last_seen_at`, and `gone_at` once a reconcile found it missing on the server. `fill_id` names the fill that imported a location into an smtp account (`NULL` otherwise). |
| `smtp_fills` | Fill of an smtp account from an IMAP mailbox: server, login, the password (encrypted, only while it runs or keeps running), folder filters, the cut-off of a one-time fill, status. A recurring fill has `recurring_since`, `keep_running`, its own `interval_seconds` (`NULL` for the server's), `run_started_at` and the trash and spam folders its last run skipped (`unconfirmed_folders`). `folder_prefix` is set only for fills from before fills wrote into `JOURNAL`. One running fill per account (partial unique index). |
| `smtp_fill_folders` | A fill's progress in one IMAP folder: the server's `UIDVALIDITY` and last UID, the archive folder it writes into, `done`, the counts (of the last run for a recurring fill, `waiting` included) and the error. |
| `smtp_fill_health` | The failure streak of a recurring fill and what the notifier last announced about it, like `account_sync_health` for IMAP accounts. |
| `sync_runs` | Sync or import of an account: start, end, status, counters (also of the reconcile), error. |
| `loss_alerts` | Alert about messages an account lost on the server in one sync run, with its delivery state. |
| `audit_log` | Event of the [audit log](../guide/audit-log): time, actor, user ID and name, action, target, client IP, user agent, details (JSONB). Partitioned by month; append-only by trigger. No foreign keys to users or accounts, so entries outlive them. |
| `audit_user_agents` | Distinct user agent string, referenced from `audit_log`. |
| `attachment_texts` | Extracted text of one attachment of a message (`message_sha256`, `part_index` as in the parts API): file name, status (`ok`, `truncated`, `no_text`, `encrypted`, `too_large`, `failed`, `timeout`), the text for `ok` and `truncated`, and a generated `tsvector` with a GIN index. Deleted with its message. |
| `attachment_index_state` | Extraction state of a message with attachments: `version`; the message is done at `store.AttachmentIndexVersion`, also without text (no readable attachment, an unreadable file). Deleted with its message. |
| `jobs` | Background job (see [Background jobs](#background-jobs)). |

Deduplication is by exact content: the same bytes in two folders or accounts
give one `messages` row and two `message_locations`. The same mail delivered
twice with different headers (for example different `Received` lines) is
stored twice.

Deduplication is global, also across users: a message in two users'
accounts is stored once. A user sees a message if at least one of its
locations is in one of their accounts (removed accounts included), and only
those locations. Every query of the web API takes the user's ID for this;
the CLI, the schedule and `sync` work on all accounts. An account without
owner exists only before the first user is created, who then gets it.

### UIDVALIDITY and counts

When a server renumbers a folder (a new `UIDVALIDITY`), the sync stores the
new value and fetches the folder again from UID 1. Every message still there
gets a new location; the old locations stay as history. A location is
**superseded** when `l.uidvalidity <> f.uidvalidity`; queries use exactly
this expression, and the API marks such locations (`superseded`).

Counts are distinct messages, like the message lists:

- A folder count is the number of distinct messages in that folder. A
  message with an old and a new location counts once; one the server deleted
  before the renumbering (only an old location) still counts.
- An account count (`status`, `/api/status`) is the number of distinct
  messages in that account. The same bytes in INBOX and Archive count once
  in each folder but once for the account, so folder counts do not add up
  to the account count.

`ListFolderCounts` counts per folder in a subquery that reads
`message_locations_folder_sha_idx` alone. A `count(DISTINCT …)` over the
grouped join was about 20 times slower on 500,000 locations;
`TestFolderCountCost` (`MAIL_ARCHIVE_BENCH=1`) measures it.

## Storing messages: sync and import

Sync and import store a message the same way (`internal/archive/ingest.go`):
the bytes stream into the blob store while they are hashed,
`archive.BuildMeta` extracts headers (at most 1 MiB) and the index data
(body text, To and Cc, attachment names, `has_attachment`) for messages the
database does not know yet, and a folder writer commits batches of 100
messages with their locations and the folder's last UID (`Store.SaveBatch`).
Fields extracted in `BuildMeta` thus apply to imported mail too.

`messages.index_version` records which extraction filled a row
(`store.IndexVersion`, currently 3). Sync and import write the current
version. A migration that needs new extracted fields adds columns without
filling them; existing rows keep their lower version, and `reindex` walks
them in primary-key order, 200 per transaction, and fills them from the
stored files. Its update only touches rows below the current version, so it
never overwrites a row that a sync stored meanwhile. An advisory lock
(class `mc`) keeps a second reindex out. A later extraction bumps
`IndexVersion` and adds its fields to the same pass.

`archive.Importer` reads mbox files (`mailbox.MboxScanner`, streaming, one
line buffer per message) and Maildirs. Bytes stay as they are with one
exception, so that copies deduplicate against IMAP: if a message's first line
ends in LF only, every LF not preceded by CR becomes CRLF
(`mailbox.CRLFReader`). A message is skipped when its hash already has a
location in the folder (`LocationInFolder`), which makes a rerun add nothing.
The first import into a folder sets its `UIDVALIDITY` to the import's start
time; UIDs continue from the folder's last UID. An import holds the account's
lock with `TryLockSyncForWrite`, like a sync, and records a `sync_runs` row.

## Sync and concurrency

`archive.Syncer` syncs one account: it takes the account's PostgreSQL
advisory lock on a dedicated connection, records a `sync_runs` row, walks the
selected folders and commits batches of 100 messages together with the
folder's last UID. Counters are written after every batch so the UI can show
progress.

`archive.Runner` runs inside `serve`. It holds a queue of account IDs, fed by
the API (sync buttons, new accounts) and by the schedule, which every minute
queues enabled accounts whose last run started more than the interval ago.
It syncs one account at a time.

The API reports an account as *running* when any session holds its lock
(looked up in `pg_locks`), so syncs started by the command line are visible
too.

A sync writes each `.eml` file before the batch with its row commits, so for
a moment a file has no row. `verify` must not take such a file for an orphan.
The **blob lock** (advisory lock class `0x6d62`, key 0) handles this: a sync
takes it shared with `TryLockSyncForWrite`, on the connection that holds the
account lock, for the whole run. `verify` takes it exclusively, only while it
looks for orphans; a sync that starts meanwhile waits. If the lock stays busy
longer than `--lock-timeout`, `verify` skips the orphan check and exits `2`.
Deleting an account takes only the account lock, so it never waits for
`verify`. PostgreSQL drops both locks with the connection when a process dies.

While the blob lock is held exclusively (by `verify`, a deletion batch, the
sweep or a purge, in any process), at most one writer per process waits for
it with a pool connection, so reads keep working. Writers (SMTP deliveries
in `inWriteTx`, syncs, fills and imports in `TryLockSyncForWrite`) first try
the lock without waiting. If that fails, a delivery rolls its transaction
back before it wrote anything, and a sync gives back its account lock and
connection; both then wait in the process for the store's single wait slot
without a connection. The slot holder waits in PostgreSQL with the
blocking call, as before, and frees the slot once the lock is granted; when
the lock is released, the waiters follow one after the other within
milliseconds. A delivery takes the lock inside its write transaction and a
sync on its account-lock connection, as before. A sync takes the account
lock again after the wait and gives up (`ok=false`) if another session took
it meanwhile; `Filler.Run` reads the fill again under the lock, and syncs
and imports read the account again. So a parked sync holds no account
lock: `SyncingAccounts` does not report it. The web server shows its own
parked syncs and fills as `queued`, because its runners claim an account
or fill from the moment they take it off the queue until its run has
returned (`Runner.Pending`, `FillRunner.Pending`; see `loadBusy`). Its
health counts from the claim, so a sync parked behind a verify that hangs
turns stale after `StaleAfter`. A parked sync of the CLI is not shown: it
holds no lock and the web server does not know of it. The account can be
deleted meanwhile, and the CLI's "is being synced ... by another process"
can also mean that it lost that race after waiting. The sweep
takes its session connection anew for each try of the lock, so it keeps
no connection while it waits either.

## Blob durability

`blobstore.New`, `Put` and `Remove` return success only after the files and
directory entries their result depends on are synchronized with `fsync`.
Callers acknowledge nothing before that: import, sync and fill return the
error before the batch with the message's row and the folder's progress
commits, and SMTP answers `451` before any recipient transaction. An error
can still leave the result visible (a published file, a removed one); it
counts as a failure, and the retry completes it. A file being visible,
`Stat` and `Exists` say nothing about durability.

- **New** creates `messages/` and `tmp/` if needed and, on every call,
  synchronizes them, then the store root and each of its ancestors bottom up
  through `/`. It resolves symlinks in the root first, so the target's
  ancestry is synchronized, not the lexical parents of a link; `Root()`
  still returns the configured path. Every ancestor of the data directory
  must therefore be readable by the service user; one that is not fails the
  start with a message naming it. The configured path, its symlinks and, for
  a relative root, the working directory must not change while the process
  runs.
- **Put** writes into `tmp/put-*`, synchronizes and closes the file, and
  publishes it with a hard link under `messages/ab/cd/<sha256>.eml`. A link
  never replaces an existing name, so of identical concurrent writers, also
  in other processes, exactly one publishes (`created`), and nobody replaces
  the file another writer is still synchronizing. Then it synchronizes
  `messages/ab/cd`, `messages/ab` and `messages`, also when they existed.
  A writer that finds the file (before or through a lost link race)
  synchronizes that file and the same directories itself before it returns
  `created=false`: the file may be what an earlier, failed call published.
  If a sweep or `verify --fix-orphans` removes the file between the lookup
  and its synchronization (SMTP puts run without the blob lock), `Put`
  publishes its own copy instead. After a failure the final file stays for
  the retry; the temporary name is removed, but not durably.
- **Remove** unlinks the file and synchronizes its directory, also when the
  file was already gone: an earlier call may have unlinked it and failed
  before the barrier. If shard directories are missing, it synchronizes the
  nearest existing one and its ancestors through the store root; a missing
  store root is an error. `SweepBlobs` marks the removals of a page done
  only after every `Remove` succeeded, so a failure leaves them pending and
  the next sweep repeats them.

Stale temporary names in `tmp/` are possible after a crash. A name left
after publication is a second hard link to a live blob; `verify
--fix-orphans` moves it into `orphans/` as `stale_temp`, which removes only
that name, but counts its bytes among the orphans although the blob stays.

The guarantee holds on Linux, for a data directory on one local filesystem
that supports hard links and `fsync` on regular files and directories, on a
device that honours flushes. Storage without hard links or directory
`fsync` fails every write instead of falling back to weaker behaviour. A
container can only synchronize what it sees: the operator provisions the
host side of a bind mount or volume durably. File shares of Docker Desktop
(macOS, Windows) are not tested; if one lacks hard links or directory
`fsync`, deliveries get `451` and syncs fail. Other platforms build,
without a durability claim.

The tests in `internal/blobstore` and `TestSweepKeepsRemovalsUntilDurable`
prove the order of the barriers, error propagation, retries and concurrent
writers, with failures injected per store. The four caller tests
(`TestImportFailsWhenTheBlobStoreFails`, `TestSyncFailsWhenTheBlobStoreFails`,
`TestFillFailsWhenTheBlobStoreFails` and `TestReceivePutFailure`) guard the
existing error handling of import, sync, fill and SMTP; they are no
regression tests for new behaviour. None of these tests proves that data
survives a power loss. That needs a disposable VM or a fault-injecting
block device, power cuts at the publication and removal boundaries, a
restart, and a check that every acknowledged blob and every pending removal
is still there.

## Journaling and fills

An smtp account has one folder, `store.SMTPFolder` (`JOURNAL`).
`Store.ReceiveMessage` stores what arrives over SMTP there, with an
envelope; a fill (`archive.Filler`) imports from IMAP into the same folder,
without one. The envelope is what tells them apart: `filled` in the message
view, `FindReceivedLocation`, `FirstReceived` and `JournaledMessageIDs` only
count locations with an envelope.

Both write into one folder at the same time (deliveries take no sync lock),
so they number their locations the same way:

- A delivery takes the folder row with `LockFolder` (`SELECT … FOR UPDATE`),
  checks for an existing location with an envelope and takes the next UID
  with `NextUID`, which also sets `folders.last_synced_at`: `LastReceived`
  reads it as the last delivery.
- A fill's batch (`Store.SaveFillBatch`) takes the same row lock, checks
  inside the transaction which messages the folder already holds
  (`LocationInFolder`; they count as `present`), inserts the rest and
  reserves their UIDs at once with `AllocateUIDs`. That leaves
  `last_synced_at` alone, and the locations are inserted without
  `ON CONFLICT`, so a clash would fail rather than merge into another
  location. The fill's own check before downloading the bodies only saves
  work.

The lock order is the same in both: attestation fence, folder row, message
rows. Batches insert their messages in the order of the hashes
(`sortBatch`, also in `SaveBatch`), so two batches cannot deadlock on
message rows. A fill holds the account's sync lock for writing for its whole
run, like a sync, which keeps removal, other fills and `verify`'s orphan
check out.

Fills from before `JOURNAL` (migration 00027) have a `folder_prefix` and
write into `PREFIX/<IMAP folder>`, with the same code path.

### Recurring fills

A recurring fill reuses the statuses of a fill. One fill per account is in
progress at a time (`smtp_fills_running_idx`); the account's sync lock
guarantees the same.

| Column | Meaning |
|---|---|
| `recurring_since` | Set when the fill became recurring (created so, or switched on); never cleared. The fill then uses the two-hour rule instead of the cut-off, and a resume makes it recurring again. |
| `keep_running` | Recurring and not stopped. Stopping clears it and the password. |
| `interval_seconds` | `NULL` for `MAIL_ARCHIVE_SYNC_INTERVAL`; otherwise whole minutes from 5m to 720h. The floor `MAIL_ARCHIVE_SYNC_INTERVAL_MIN` applies when reading. |
| `run_started_at` | Start of the current or last run, by the runner's clock. The next run is due an interval later (`Schedule.NextFill`). |
| `status` | `running` during a run, or while a new or resumed fill waits for its first; afterwards the run's outcome `ok`, `partial` or `failed`, with `finished_at` and `error`. A stopped fill is `cancelled`. |

Between runs an active recurring fill keeps its sealed password; the CHECK
`smtp_fills_password_check` allows a password only while a fill runs or
keeps running. `FinishFill` records the run in `smtp_fill_health` in the
same transaction (`ok` and `partial` end the streak, `failed` extends it).
`StartFillRun` sets the next run going and resets the folders' `done`,
error and counts, so the counts are those of one run.

**Scheduling.** `archive.FillRunner` (one queue, one fill at a time, apart
from the sync `Runner`) queues running fills every minute, as before, and
the recurring fills whose next run is due (`RecurringFills`: active, between
runs, account enabled and not removed). It uses the sync runner's
`Schedule`; `MAIL_ARCHIVE_SYNC_INTERVAL=0` turns the scheduled runs off (a
run in status `running` is still finished). For
`RecurringStartHold` (70 minutes) after the web server started
(`HoldUntil`) it starts no run of a recurring fill: after an outage the mail
server retries the journal copies it queued (Postfix every 4000 seconds at
most), and the fill must not import the mailbox copies first. A long
one-time fill delays the recurring runs behind it; they show as queued.
After each run, `AfterRun` wakes the notifier.

**A run.** `Filler.Run` takes the account's sync lock, as for any fill, and
calls `StartFillRun`. A recurring fill whose account is disabled or removed
is skipped without a change (`ErrFillSkipped`, also in `RunLocked` for a run
already started), so it neither fails nor changes its health. One `now` per run decides by INTERNALDATE: journaled
(Message-ID with an envelope in the account) first, then younger than
`RecurringGrace` (two hours) waits, anything else is imported, also without a
Message-ID; a date more than a day in the future counts as old. The headers
are sorted by UID, and a folder's `last_uid` moves only over decided UIDs up
to the first one that waits, or whose body was requested but did not come
back (`watermark`); later UIDs are decided again by the next run. Before fetching bodies, a recurring fill leaves out what it
imported before: `FillImported` matches Message-ID, INTERNALDATE and
`RFC822.SIZE` against this fill's locations, which makes moved messages,
renamed folders and rescans after a `UIDVALIDITY` change cheap. Identical
bytes without a Message-ID are caught by `LocationInFolder`, before the
fetch and again under the folder lock. Moves and deletions in the mailbox
are not reconciled.

New IMAP folders are picked up through the fill's folder filters. A folder
the server marks `\Trash` or `\Junk` is filled only if it already has a
`smtp_fill_folders` row (created from the folders the user confirmed when
the fill started or was switched on) or `included_folders` names it;
otherwise the run skips it and records it in `unconfirmed_folders`.

**Switching on an earlier fill.** `SetFillRecurring` gives an ended fill the
new password and starts its first run. The first time a fill becomes
recurring, its folders get `last_uid = 0`, so the run applies the new rule
to what the cut-off left out. If the fill is from before `JOURNAL`,
that run first calls `ConvertLegacyFill`: in batches of 5,000, under the
`JOURNAL` row lock, it copies one location per message from the fill's own
folders (`DISTINCT ON (message_sha256)`, none for a message `JOURNAL` holds)
with `JOURNAL`'s `UIDVALIDITY` and UIDs from `AllocateUIDs` (refused past
`2^32-1`), and retries a batch that fails with `23503` (a message retention
deleted meanwhile). Then its IMAP folders point at `JOURNAL` with
`last_uid = 0`, so the scan applies the new rule to what the cut-off left
out. No files are written, and the old folders keep their locations.

**Stopping and restarts.** `CancelFill` stops a running or recurring fill
and wipes the password; a running batch notices at its `FillStatus` check.
A run interrupted by a shutdown stays `running` with its password and
continues at the next start. Removing an account stops its recurring fills
too (`CancelAccountFills`).

**Health.** `smtp_fill_health` has the columns of `account_sync_health`.
`FillHealth.State` and `SyncHealth.State` share `HealthState`; the stale
time counts from the last success, or from `recurring_since`; a fill counts
as busy while it waits behind its account's lock, and since the server's
start during the hold after it. `/healthz/sync`
puts the smtp account's ID into `failing` or `stale`, and
`CountFailingByOwner` counts it for admins. `Notifier.Deliver` has a pass
for fills (`ClaimFillNotifications`), between the sync pass and the loss
alerts; its event IDs are `fill-<id>-<since>-<event>`.

## Attestations

An attestation (`archive.Attest`) covers the `messages` rows with
`after < seq <= upto`, where `after` is the previous attestation's `upto`,
and the `message_deletions` rows in the same way. `messages.seq` is an
identity column with `CACHE 1`. A sequence value is drawn at `INSERT` and
becomes visible at commit, so `max(seq)` alone is not a safe upper bound:
a transaction holding seq 100 may commit after one with 105 is visible.

The **attestation fence** (advisory lock class `0x6d64`, key 0) orders the
two. Every transaction that inserts into `messages` or `message_deletions`
runs through `Store.inWriteTx`, which takes the fence shared as a
transaction lock, after the blob lock where the transaction takes that
itself (`ReceiveMessage`) and before any row lock. The attestation takes
the fence exclusively as a session lock on its own connection, which waits
for every writer that already drew a value and makes new ones queue. It
then starts a repeatable read transaction, whose first query (the bounds)
takes the snapshot, and releases the fence at once. All rows with
`seq <= upto` are in that snapshot, and none can appear later. Writers wait
only that long; `lock_timeout` (30 s) bounds the wait for running batches.
A test in `internal/store` parses the package and fails if an insert into
either table runs outside a function passed to `inWriteTx`, or if the locks
are taken in another order.

A second advisory lock (`0x6d65`) lets one attestation be written at a
time. The leaves are streamed in seq order into a temp file in
`attest/leaves/` and hashed; the row is inserted in the snapshot's
transaction; after the commit the leaves file is renamed into place and the
line appended to `attest/attestations.jsonl` with `fsync`. Before writing,
`catchUp` checks the database chain and compares the file with it: entries
only in the database (a crash after the commit) are appended, with their
leaves rebuilt from the database (`ListAttestedRange`); any other
difference stops the run.

## Reconcile

After a folder synced without error, `Syncer.SyncAccountWith` reconciles it
when it is due (`folders.last_reconciled_at` older than the interval, with
10 % slack, or a forced run). It uses the same connection, still in the
folder's `EXAMINE`, and the same account lock as the sync.

1. `imapsync.ListFlags` sends `UID FETCH 1:* (UID FLAGS)` (never for an
   empty folder) and fails with `ErrIncomplete` if fewer messages came back
   than the folder holds after the command (`EXISTS`, kept current by the
   client). Folders above `MaxReconcileMessages` (2,000,000) are refused.
2. The UIDs go into a `[]int64`; each message's flags without `\Recent`,
   sorted and joined by spaces, into a `[]string`, with one string per flag
   combination through a map.
3. `Store.ReconcileFolder` locks the folder row, checks its `UIDVALIDITY`
   and runs one statement (`ReconcileFolder` in `queries/reconcile.sql`):
   the arrays are unnested into a materialized CTE and joined with the
   folder's current locations up to `last_uid`. Data-modifying CTEs then
   mark missing locations gone, clear `gone_at` on listed ones, write flags
   that differ as a set, mark locations of older `UIDVALIDITY`s gone and set
   `last_reconciled_at`. Each returns its row count for `sync_runs`.

Any error writes nothing and ends the run `partial`, which counts as a
success for [sync health](#sync-and-concurrency). Stored folders that the
filters select but `LIST` no longer returns are marked gone with
`MarkFolderVanished`.

**Loss alerts.** When a run's reconciles marked at least 10 locations gone,
`finish` (also of a cancelled run, with an uncancelled context) counts the
messages that lost their last present location in the account during the
run (`LostMessages`: gone locations from the partial index with
`gone_at >= sync_runs.started_at`, outside the folders whose first
reconcile ran in it, without a present location in the account). If
`store.LossAlertDue` holds, `FinishSyncRun` inserts a `loss_alerts` row in
the same transaction as the run. `account_sync_health` holds one state per
account that the notifier compares with what it announced; losses are
events, so they get an outbox of their own with the same lease and retry
columns. `Notifier.Deliver` sends them in its last pass, after the failing
syncs and the failing recurring fills (`smtp_fill_health`). Rows not sent
within 24 hours are given up by the next finished run, so the pending
index stays small without a webhook.

`last_seen_at` is not rewritten for every present location on every run. A
present location was seen at its folder's `last_reconciled_at`, so the API
reports `GREATEST(l.last_seen_at, f.last_reconciled_at)`. When a location
becomes gone, `last_seen_at` is set to that value of the previous run;
when it comes back, to `now()`. `UpsertLocation` clears `gone_at`, for a
`UIDVALIDITY` that returns to an old value.

A message is **gone** for a user when it has locations in the user's IMAP
accounts and all of them have `gone_at` set. The `gone` search filter
starts from the partial index `message_locations_gone_idx` (only gone rows)
and checks each candidate's present locations through
`message_locations_sha_idx`, so it never reads all locations;
`TestSearchFilterPlans` checks the plan. `TestReconcileCost`
(`MAIL_ARCHIVE_BENCH=1`) measures time and peak heap for 100,000 and
500,000 messages, with the IMAP server in a child process.

## Accounts: ownership and changes

Every account has an owner, and every change to an account goes through
one of three store functions: `UpdateAccount` (name, connection, password,
folder filters, enabled), `SetAccountOwner` and `DeleteOrRemoveAccount`.
Each takes an `AccountRef`: the ID together with the owner and the
`version` the caller read. Each runs in one transaction that

1. locks the row (`SELECT … FOR UPDATE`),
2. checks that the account exists, is not removed and still has the
   expected owner (`ErrNotFound` otherwise, exactly like an unknown
   account: the API answers `404`),
3. checks the version (`ErrStale` otherwise: `409`, reload and retry),
4. applies all changes and increments the version.

So concurrent changes are serialized, a change based on an outdated read is
refused instead of overwriting the newer state, a combined change is saved
completely or not at all, and an account handed to another user can no
longer be changed by its former owner. Work that needs the network or the
key (the login check, encrypting the password) happens before the
transaction, never while a row is locked.

| Layer | Responsible for |
|---|---|
| SQL queries for messages, folders, search, status, account lists | Showing only the user's own data (owner filter in every query). |
| Store mutation functions | Owner, version and atomicity of every account change. |
| Web handlers and CLI | Resolving the account (owner-scoped in the web), checking input, calling one mutation, mapping errors to `404`/`409`. |
| Sync lock | Keeping deletes away from running syncs. |

A sync never writes the `accounts` row. Renaming, new connection settings,
filters, enabling and moving therefore do not disturb a running sync and
apply from the next one. Only deleting must not overlap a sync, which
writes folders and messages for the account: `DeleteOrRemoveAccount`
requires the caller to hold the account's sync lock, and a sync reads the
account again after taking the lock, so it skips an account deleted while
it was queued. The web server and the CLI delete with
`RemoveAccountLocked`, which takes the sync lock (`409` if another session
holds it) and runs the delete on the same dedicated connection, as a lock
session does (`locksession.go`): with every other pooled connection held
by writers waiting for the blob lock, a delete that needed a second
connection would wait for ever.

Stored IMAP passwords are bound to the account ID (AES-GCM associated
data), so renaming or moving needs no new encryption. `UpgradePasswords`
converts passwords bound to the old name with compare-and-swap, so it never
overwrites a password set at the same moment.

## Search

`messages.search` is a generated `tsvector` that combines the `simple`,
`german` and `english` configurations over subject, sender and body text,
with a GIN index. Queries use `websearch_to_tsquery` in all three
configurations; subject and sender also match as substrings through
`pg_trgm`. Results are ordered by date and paginated with a keyset cursor
(date, SHA-256), so deep pages stay fast.

Recipients and attachment names are not in the `tsvector`. The filters
`from`, `to` and `attachment` are `ILIKE` substring matches with trigram
indexes; `to` uses one index over To and Cc joined by a newline. A partial
index on `(sort_at, sha256) WHERE has_attachment` serves `has:attachment`
without a query. The web UI parses prefixes such as `to:` from the search
field (`web/src/searchSyntax.ts`); the server only sees API parameters.

**Attachment text** is in `attachment_texts`, with its own `tsvector` (the
same three configurations) and GIN index, not in `messages.search`: adding
it there would rewrite every message row and share PostgreSQL's 1 MB limit
of one `tsvector` with the body. A query first runs `AttachmentMatches`:
the messages of the viewer's scope with an attachment whose text matches,
up to 5,000 (`maxAttachmentMatches`). The query arm of the filter (all four
copies in `queries/search.sql`) then also matches `m.sha256 = ANY
(att_shas)`, which keeps a BitmapOr through `messages_pkey`; with more
matches, `att_walk` checks each walked message's attachments through
`attachment_texts_pkey` instead, as `to_walk` does for envelopes. So a
match is per document: all words in the message, or all in one attachment,
negations included. There are no trigram indexes on attachment text, so it
matches words, not substrings.

The text is extracted by two job kinds (`internal/archive/attachments.go`):
- **`attachment_text`** (key and `message_sha256` = the message,
  `MaxAttempts` 3, the crash-loop guard). The three insert sites (sync and
  import, fills, SMTP) enqueue it with `PriorityNew` in the transaction that
  inserts a new row with `has_attachment`. The handler skips a message that
  is done, reads its file, runs `attachtext.Extractor.Message` and writes
  the state and the texts in one transaction with a conditional
  `MarkAttachmentsIndexed`: the second of two writers changes nothing. An
  unreadable file is done without text; a message deleted meanwhile is
  done (foreign key error). Extraction results (a damaged PDF, a timeout,
  a password) are statuses, not errors; a PDF module that did not compile
  in the process fails the job at once with `extractor unavailable`. PDFs wait for their turn (one per process) without using up the message's 60 s, and one whose caller's deadline passes while it waits is retried (`pdf busy`), not stored as `timeout`.
- **`attachment_backfill`** (key `v<AttachmentIndexVersion>`, priority
  `PriorityBackfillCursor` = 101). `serve`, `worker` and `reindex` enqueue it
  at start with an empty payload. Each run walks `ListAttachmentPending`
  (`messages_attachment_sort_idx`, newest first by `(sort_at, sha256)`, the
  state's primary key; messages with a queued, running or failed job are
  skipped) from the cursor in its payload, enqueues up to 1,000 children
  with `PriorityBackfill` (100) in that order, and returns `jobs.Continue`
  with the last one as the cursor. Its priority after its children makes
  it run again only when its batch has been claimed, so the queue holds
  about one batch; new mail goes first. A walk that finds less than a batch
  ends it.

Neither ever writes a `messages` row. `reindex` enqueues the backfill and
runs a pool of the two kinds with `UntilIdle` until none is due. Raising
`AttachmentIndexVersion` extracts everything again: the backfill gets a new
key, and its enqueues then need `RetryFailed`.

## Conversations

`thread_id` is the first `References` ID, else `In-Reply-To`, else the own
`Message-ID` (`archive.threadID`). Queries group by the thread key
`COALESCE(thread_id, sha256)`, so a message without IDs is its own thread,
and use `messages_thread_idx` on that key with `(sort_at, sha256)`. The
thread ID depends only on the message bytes, which fits the global
deduplication: the row is shared, only its visibility differs per user.
There is no subject threading and no tree walk, so cycles cannot occur.

The message view adds one hop of direct links that need no common thread
ID: the parent (`message_id = in_reply_to`) and the replies (their
`in_reply_to` or `reference_ids` name the message). Every query uses the
owner predicate of the listings, so another user's reply to a shared
message is never listed or counted.

The grouped list (`SearchThreads`) walks the date order and keeps a message
only if no newer message of its thread passes the same filters
(`NOT EXISTS` over the thread index), so it stops after a page like the
plain list. `CountThreadMatches` counts the matches for the rows of a page.
sqlc has no fragments, so the filter predicate exists four times in
`search.sql`, between `filters:begin` and `filters:end` comments;
`TestSearchFilterCopies` keeps the copies equal and
`TestSearchThreadsParity` checks grouping against the plain list.

## Audit log

`internal/store/audit.go` writes the [audit log](../guide/audit-log). There
are two ways to write an entry:

- `Store.Audit` writes it on its own. Reads, searches, exports and manual
  syncs call it after the request is checked and before any data is sent
  or work is queued, and stop if it fails (`ErrAuditUnavailable`, `503`).
- `store.WithAudit(ctx, entry)` hands the entry to the next transaction of
  `inTx`, which writes it after the change succeeded and before the commit.
  Only one transaction writes it, and the store method may complete it
  inside (the user of a passkey login, the ID of a new account). Changes and
  logins use this: no change without its entry.

In `internal/web`, `auditEntry(r, action)` fills the actor, user, client
address and user agent; in the CLI, `cliEntry(action)` fills the operator.
A new event needs one of the two calls and a case in the tests that check
exactly one entry per event (`TestAudit*`, `TestCLIAudit`).

Partitions are created ahead by `audit_log_ensure_partitions` (migration,
`serve` hourly, every command, and on a missing partition). There is no
default partition: rows in it would block creating that month later.

## Background jobs

Work that runs after the transaction that caused it, in `serve` or in a
separate `mail-archive worker`, is a row in `jobs` (migration 00032; queries
in `internal/store/queries/jobs.sql`, the pool in `internal/jobs`). Its own
implementation, no library; it generalizes the leases of `loss_alerts` and
`account_sync_health`.

- **States.** `queued` → `running` (a claim) → `done`, back to `queued`
  (a retry, a release at shutdown, a continuation) or `failed` (attempts
  used up, or `jobs.Permanent`). `done` rows are removed after 24 hours by
  each pool, hourly; `failed` ones stay until `jobs retry` or until their
  message is deleted (`message_sha256` cascades).
- **Idempotent enqueue.** `(kind, key)` is unique; the key is an identifier
  (a SHA-256, `v1`), never content. Enqueueing again keeps a queued row and
  only moves it forward (lower priority, earlier `run_after`; a pending
  retry keeps its time), queues a done row again with the caller's payload
  and `attempts = 0`, and leaves running and failed rows alone (failed ones
  unless `RetryFailed`). `store.EnqueueJobs` runs in a transaction of its
  own; `enqueueJobs` runs in the caller's, so a job is queued together with
  what caused it, and storing never waits for it: the key is new, so the
  insert meets no conflict.
- **Claim and lease.** `ClaimJobs` takes due rows of the pool's kinds by
  `priority, run_after, id` with `FOR UPDATE SKIP LOCKED`, sets a random
  lease token per claim and `lease_until` = now + 60 s on the database's
  clock, and counts the attempt **before** the handler runs. A heartbeat
  extends the leases every 20 s in one statement; a job missing from its
  result lost its lease (it expired and was taken over, or its message was
  deleted): its handler's context is cancelled and its result dropped. Every
  finish, retry, release and continuation checks the lease token.
- **Crash-loop guard.** A process that dies during a handler (an OOM kill)
  keeps the attempt. Its lease expires, and `ReapExpiredJobs` (before each
  claim, and when the dispatcher wakes at the earliest foreign
  `lease_until`) retries it with code `lease expired`; after `MaxAttempts`
  the job is `failed` and never claimed again.
- **Retry.** A handler error queues the job again after
  min(first × 2^(attempts − 1), cap) × [0.5, 1) (equal jitter, so restarted
  pools do not retry in step); defaults 30 s and 1 h. `last_error` is a code
  (`internal`, `panic`, `timeout`, `db <SQLSTATE>`, `blob unreadable`,
  `lease expired`, or a kind's `jobs.Code`), never the error text, which is
  logged with the job ID, kind, key and attempt.
- **Priorities.** `store.PriorityNew` (0) for work caused by new mail,
  `store.PriorityBackfill` (100) for older mail. `/healthz/jobs` degrades
  only when a `PriorityNew` job has been due for 30 minutes.
- **Continuation.** A handler that did part of its work returns
  `jobs.Continue(payload)`: the job is queued again at once with the new
  `payload` (a cursor, at most 1 KiB) and `attempts = 0`, as a new job.
- **At least once.** A job can run twice (a lease that expired while its
  handler still worked, a crash after the work but before the finish).
  Handlers must be idempotent.
- **The pool.** One dispatcher and at most `MAIL_ARCHIVE_JOB_WORKERS`
  handlers (0-8, default 1); it claims only as many jobs as it has free
  slots. It sleeps until a notification, a free slot, the next due time
  (`NextJobDue`: the next `run_after`, or the next lease of its kinds that
  another process holds) or the 5-minute fallback poll. At shutdown it stops
  claiming, cancels the handlers and waits: a handler that returned the
  cancellation is released (queued at once, the attempt taken back), other
  results are recorded, all within 5 s. If the database is gone, the lease
  expires and the attempt counts.
- **Wake-up.** `LISTEN mail_archive_jobs` on a connection of its own
  (`application_name` `mail-archive jobs listener`), reconnected with a
  backoff of 1-30 s and followed by one pass. Its goroutine only reads
  notifications and wakes the dispatcher without blocking (a channel with a
  buffer of one), so PostgreSQL's notification queue cannot fill from this
  process. Senders of `NOTIFY`: `EnqueueJobs` and `enqueueJobs` (once per
  kind per transaction; PostgreSQL merges duplicates, so a sync batch wakes
  the pools once: sync, fill and SMTP batches, the attachment backfill's
batches, `reindex` and the start of `serve` and `worker`) and
`RetryFailedJobs` (`jobs retry`). `NOTIFY` makes the
  commits of the transactions that send it take turns database-wide; they
  are short. With a pooler in transaction mode `LISTEN` does not work; the
  poll covers it.

**Adding a kind:** register a `jobs.Kind` (name `^[a-z][a-z0-9_]{0,39}$`,
handler, `MaxAttempts`, retry delays) in `jobRegistry`
(`cmd/mail-archive/jobs.go`), so that `serve` and `worker` run it; enqueue
with `Kind.NewJob(key, priority)` through `store.EnqueueJobs`, or
`enqueueJobs` in the transaction that causes the work. Use `message_sha256`
for a job about one message. Return `jobs.Permanent` for errors a retry
cannot fix and `jobs.Code` to name an error in `last_error`. Tests register
their kinds in the test only.

## Web server

The server embeds the built UI and serves the JSON API. A middleware checks
the `Host` header and, for state-changing requests, the `Origin` and the
content type; see [Security](../reference/security). The UI keeps its state
in the URL and talks only to the API.
