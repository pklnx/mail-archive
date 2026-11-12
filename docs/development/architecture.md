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
| `internal/blobstore` | Content-addressed `.eml` storage, or `.enc` with encryption at rest: files are named by their SHA-256 and published and removed durably (see [Blob durability](#blob-durability)). |
| `internal/mime` | MIME parsing: text for the search index, parts and HTML for display. |
| `internal/attachtext` | Text of attachments for search: plain text, CSV, TSV, Markdown, HTML, and PDFs through PDFium compiled to WebAssembly in wazero (no file system, network or environment; one PDF at a time per process; a new instance per document). No database. |
| `internal/store` | PostgreSQL access: migrations (goose), queries (sqlc), the per-account sync lock, the blob lock, the attestation fence and the migration lock. `migrate` and `migrate down` hold the command lock (advisory lock class `0x6d68`, key 0) for the whole command on a connection of its own outside the pool; goose's session lock is taken by a locker of our own (`gooselock.go`) on the connection that runs the migrations, from a short-lived pool of its own (one extra connection per call) whose teardown frees a lock left on a given-up connection (#269). Both commands open the store with `ForMigrations` (`client_connection_check_interval` and TCP keepalives), so the server frees a killed command's locks. What an operator sees of this (two runs wait for each other, the holder message, the time limits, what `migrate status`, `backup` and `serve` do) is in the CLI reference under [`migrate`](../reference/cli#migrate). |
| `internal/attest` | Formats of the tamper-evidence chain: the signed text of an attestation, leaves files, the attestation file and the Ed25519 key files, and the signed statement of a GoBD export (`statement.go`). No database. |
| `internal/gobd` | The GoBD export, [format v1](#gobd-export-format-v1): the writer with its directory and ZIP sinks, README.txt, index.xml, SHA256SUMS, and the offline `export verify`. No database. |
| `internal/csvsafe` | The leading `'` that keeps spreadsheets from reading a CSV cell as a formula; used by the audit CSV, the deletion preview and the GoBD export. |
| `internal/notify` | Alerts for accounts whose syncs keep failing, and for recurring fills whose runs do: compares each failure streak with what was last announced and posts the difference to a webhook. Leases in `account_sync_health` keep two notifiers from sending the same alert. It also sends the `loss_alerts` that sync runs wrote. |
| `internal/jobs` | Background jobs on the queue in the `jobs` table: the registry of kinds and the bounded pool that `serve` and `worker` run (see [Background jobs](#background-jobs)). |
| `internal/web` | HTTP server, JSON API, request protection, embedded UI (`internal/web/ui`). Every route is listed once in `routes.go`, with the access it needs (session only, login, public, or session and API key with `read` or `sync`); `authenticate` enforces it, `openapi.yaml` describes it, and a test fails when the two differ. A request with `Authorization: Bearer ma_…` is authenticated by its key alone and skips the Origin check (`api_keys.go`). |
| `internal/crypto` | AES-256-GCM for stored passwords. |
| `internal/blobcrypt` | Encryption at rest: the STREAM-AES-256-GCM file format, the keyring and the archive key (see [Encryption at rest](#encryption-at-rest)). Standard library only; no database. |
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
  publishes it with a hard link under `messages/ab/cd/<sha256>.eml` (`.enc`
  with encryption at rest, see [Message files](#message-files)). A link
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

## Encryption at rest

`internal/blobcrypt` holds the file format, the keyring and the archive key
of encryption at rest (#392); `internal/blobstore` stores message files in
this format, and `internal/archive` (`atrest.go`) holds the start-up checks.
Encrypted backups use the same format with purpose 2 (backup, no binding):
`backup` and `seal-stream` write it through `blobcrypt.Writer`;
`unseal-stream` and `restore-dump` read it with `blobcrypt.StreamReader`,
which needs no size or random access, holds one chunk, reads one byte ahead
to tell the final chunk, and returns each chunk only after authenticating
it, so a cut stream fails before its end instead of ending cleanly.
A dump without the header is unauthenticated: `checkDumpKind`
(`cmd/mail-archive/backup_crypt.go`) refuses it before `pg_restore` starts
wherever the archive shows a sign of encryption at rest, unless the
operator passes `--plaintext`; under `./ma` the holder (`--hold`) makes
this decision from the facts `./ma` passes (`--dump-name`, `--dump-header`).
`restore-dump` then calls `archive.ReconcileRestored` (`restore.go`), which
reuses the start-up reconciliation of the keyring and `deriveFromFiles`.
Only the Go standard library is used (`crypto/aes`, `crypto/cipher` GCM,
`crypto/hkdf`, `crypto/hmac`, `crypto/rand`).

### Key hierarchy

- **Archive key (KEK):** 32 random bytes, base64, from
  `MAIL_ARCHIVE_ARCHIVE_KEY_FILE` or `MAIL_ARCHIVE_ARCHIVE_KEY` (see
  [Configuration](../reference/configuration#encryption-at-rest)). Never
  stored by the app. Its **fingerprint** is the first 8 bytes of
  HMAC-SHA256(KEK, `"mail-archive kek fingerprint v1"`), in hex.
- **Keyring key:** 32 random bytes per keyring. Each **slot** wraps it under
  one KEK: AES-256-GCM with the key HKDF-SHA256(KEK, no salt,
  `"mail-archive keyslot v1"`), a random 12-byte nonce, and as associated
  data `"mail-archive keyslot v1\0"` ‖ archive id (16 bytes) ‖ fingerprint
  (8 bytes).
- **Data keys (DK):** 32 random bytes each, under a random non-zero 32-bit
  id. All of them are sealed together with AES-256-GCM under
  HKDF-SHA256(keyring key, no salt, `"mail-archive keyring body v1"`), a
  random nonce, and as associated data the canonical metadata:
  `"mail-archive keyring v1\0"` ‖ archive id ‖ generation (uint64) ‖ current
  key id (uint32) ‖ slot count (uint16) ‖ per slot its fingerprint (8 bytes),
  label length (uint16), label and creation time (Unix seconds, int64), all
  big-endian. Plaintext: per data key its id (uint32) ‖ key, ascending.
- **File key:** per file HKDF-SHA256(DK, salt = the header's 32 random
  bytes, `"mail-archive blob v1"` ‖ purpose byte).

So the archive id is in every slot's associated data and the generation,
current key and slot list are in the data keys' associated data: none of
them can change without a KEK. The keyring key lets a slot be added or
removed without the other KEKs, as with LUKS keyslots. A removed KEK still
opens older copies of the keyring (backups) and so the keyring key, so after
a KEK leak a new keyring key and data key are needed, not only `remove-key`:
`encryption rekey` (below).

### Keyring file

`<data dir>/keys/keyring`, mode 0600 in a 0700 directory, JSON:

```json
{
  "format": "mail-archive keyring 1",
  "archive_id": "<32 hex digits>",
  "generation": 2,
  "current_key": 2615952247,
  "slots": [
    {"fingerprint": "<16 hex digits>", "label": "archive key",
     "created": "2026-11-04T10:00:00Z", "wrapped": "<base64: nonce ‖ sealed keyring key>"}
  ],
  "sealed": "<base64: nonce ‖ sealed data keys>"
}
```

Parsing is strict: unknown fields, trailing data, more than 16 slots or 64
data keys, a file over 64 KiB, upper-case hex, a non-UTC time or a wrapped
part of the wrong length are refused. The metadata is trusted only after a
KEK has opened the keyring. Every change increments the generation and
replaces the file atomically (temporary file in `keys/`, `fsync`, `rename`,
`fsync` of the directory); a leftover temporary file is ignored. The
commands that change it hold an exclusive `flock` on `keys/keyring.lock`
and refuse while another holds it.

The **database copy** is `blob_encryption.keyring` (below). Every command
that reads or writes message files reconciles the two copies at start, while
encryption is on or being changed (`blobcrypt.Reconcile`, revision 1, B2 and
revision 4 of the plan): both must open with the archive key and have the
same archive id; the copy with the newer generation decides the current
data key and the slots, and the data keys are the **union** of both, so a
data directory from before a rekey next to a database from after it (or the
other way round) keeps every file readable. The same data key id with other
key material in the two copies is refused. A result that differs from a
copy is written back to it: the database under a row lock, compared with the
keyring generation it read, the file under the keyring `flock`, only if it
is still the file that was read (otherwise the reconciliation starts again).
A missing file is rebuilt from the database. A copy that does not open with
the key is replaced only by a newer copy that does; a corrupt copy, the
keyring of another archive, or a key that opens neither copy refuse the
start.

### File format (STREAM-AES-256-GCM)

| Bytes | Field |
|---|---|
| 0–7 | magic `MA-ENC1\n` |
| 8 | version, `1` |
| 9 | purpose: `1` message, `2` backup |
| 10 | chunk size exponent, `16` (64 KiB) |
| 11 | reserved, `0` |
| 12–15 | data key id, uint32 big-endian, not `0` |
| 16–47 | salt, 32 random bytes |
| 48–63 | padding, `0` |

Then the chunks: each holds 64 KiB of plaintext (the last one 0 to 64 KiB)
followed by its 16-byte GCM tag. The nonce of chunk *i* is *i* as an
11-byte big-endian counter followed by a flag byte, `1` for the final chunk
and `0` otherwise. Every chunk's associated data is the 64-byte header; the
final chunk's is the header followed by the **binding**: the plaintext's
SHA-256 for a message, nothing for a backup. So a message file renamed to
another hash fails to open. The final chunk is full when the plaintext
length is a positive multiple of 64 KiB; only an empty plaintext has an
empty final chunk. A file of *n* plaintext bytes is therefore 64 + *n* + 16
× max(1, ⌈*n* / 65536⌉) bytes: 64 bytes plus 0.025 %.

A reader accepts only version 1, purposes 1 and 2, exponent 16, a non-zero
key id and zero reserved and padding bytes, before it allocates anything.
It derives the chunk count from the file size, refuses an empty final chunk
after a full one, and authenticates the final chunk on open, so the size
and the binding are proven before the first read. Every other chunk is
authenticated when it is read. Truncation, a dropped, reordered or appended
chunk, trailing bytes, any flipped header bit, another data key and
another binding are all refused (`ErrCorrupt`); a header naming a data key
the keyring lacks is `ErrUnknownKey`. A negative size, or one beyond the
largest whole number of chunks an int64 holds, is refused before any
arithmetic. The reader gives `Read`, `Seek`, `ReadAt` and `Size` with one
64 KiB buffer behind a mutex, so one reader can be shared between
goroutines (`TestReaderConcurrent`).

The known-answer vectors in `internal/blobcrypt/testdata` freeze the file
and the keyring formats; their keys are derived from public labels in the
test. `TestSpecByHand` rebuilds a file from the primitives as written here.
`FuzzOpen` feeds crafted headers and sizes.

**Cost** (benchmarks, one core of the CI-class Xeon): encrypting with
SHA-256 at the same time, as a writer does, runs at about 1.2–1.35 GB/s,
0.06 ms for a 75 KB message; decrypting at the same speed. Each open stream
holds about 75 KiB (one chunk buffer plus the cipher state), independent of
the file size, well inside the 4 GB / 2 core budget.

### Message files

The identity of a message stays the SHA-256 of its plaintext (database,
URLs, API, audit log, attestations); only the file name changes. With
encryption on, `blobstore.Put` encrypts while it hashes, under the
keyring's current data key, into `tmp/` (only ciphertext there), and binds
the final chunk to the hash; the file is `messages/ab/cd/<sha256>.enc`.
`stored_path` is informational and not rewritten; no reader opens a file by
it. Every reader opens a message by its hash through
`blobstore.OpenMessage`, which tries `.enc`, `.eml`, then `.enc` again (a
conversion between two lookups) and returns the plaintext as an
`io.ReadSeeker` and `io.ReaderAt` with its size, so `http.ServeContent`
serves byte ranges of `/raw` from an encrypted file. `Exists` and `Remove`
handle both forms (`Remove` unlinks both, with the same barriers); `Walk`
reports `Entry.Encrypted`.

`Put` never deduplicates onto a file of the other form, or onto an `.enc`
whose header is invalid or names a data key the keyring lacks (such as one
left by a writer of an older keyring): such a file counts as absent. Its
own copy is published, read back in full and hash-checked, and only then is
the `.eml` (or, with encryption off, the `.enc`) unlinked; an unusable
`.enc` is replaced by an atomic `rename` of the checked copy, followed by
the shard barrier. So is an existing file that fails the read-back (an
`.enc` with a valid header and a damaged body next to its `.eml`); the
other form is unlinked only after that. `created` is false in all these
cases: the message was there.
A crash between the publication and the unlink leaves both forms; readers
prefer `.enc`, verify reports the `.eml` as `plaintext`, and the next `Put`
of the message (or `encryption enable`) removes it.

### State and writers

`blob_encryption` (migration 00037) has at most one row; no row, or state
`off`, is off. `state` is `off`, `on` or a transitional state of an offline
conversion (`encrypting`, `decrypting`, `rekeying`; maintainer decision 5).
`generation` increases with every state or current-key change and never
repeats: turning encryption off keeps the row with state `off` and the next
generation, so a writer that pinned a generation before a `disable` and an
`enable` cannot match the new one. The row also holds the database copy of
the keyring and the cursor and counters of a conversion. Its `Down` refuses
while the state is not `off`.

A **writer** is a process that can write message files: `serve` (with the
SMTP receiver), `worker`, `sync`, `import` (also `--journal`), `account
fill` and `reindex`. At start (`archive.OpenAtRest`) it

1. takes the **writers lock** shared (advisory lock class `0x6d77`, key 0)
   on a connection of its own outside the pool, with
   `idle_session_timeout = 0`, the `ForMigrations` keepalives and
   connection check, and `application_name`
   `mail-archive writer <command>`. While an encryption command holds it exclusively, the writer
   refuses and names the holder;
2. reads the state. In a transitional state it refuses ("run
   `encryption enable|disable|rekey` to finish it"). Off with a keyring
   file, it looks for an `.enc` file on disk (a dump from before `enable`
   restored next to newer files): if it finds one, it records `encrypting`
   with the file as the database copy, and refuses; otherwise it records
   `off` with the file as the copy, at generation 0 like no row, so that
   writers already running keep going. A later start walks again only when
   the keyring file's id or generation differs from that copy (part 3's
   `enable` therefore writes the keyring file with a new generation before
   it encrypts the first file); with an unchanged keyring, a start reads
   the small keyring file and no directory of `messages/`;
3. with a row, needs the archive key, reconciles the keyring and, with
   `on`, makes its blob store encrypt;
4. pins the generation (`Store.PinBlobGeneration`) and watches the lock
   connection (a check every 10 s that the session still holds the lock).

A session advisory lock ends with its session, not with its process
(postgres restart, `pg_terminate_backend`, a network blip, a pooler
recycling its connection), so the writer must not outlive it: when the
check fails, the root context is cancelled with a cause and the process
exits non-zero, and Compose restarts it under the start-up rules. The
window until the next check is closed by the generation: every transaction
that inserts messages (`saveBatch` for sync and import, `saveIntoJournal`
for fills and journal imports, `ReceiveMessage` for SMTP) reads
`blob_encryption.generation` first and fails with `ErrBlobStateChanged`
on a difference (SMTP answers 451, sync records a failed run); the process
then stops the same way (final plan review, point 1). The encryption
commands `enable` and `disable` take the writers lock exclusively with a
try-lock (`Store.TryLockWritersExclusive`) and name the writers they find
by `application_name`.

Readers (`export`, also `export --format gobd`) need the key whenever a row exists; `verify` starts
without it, checks the attestations (database only) and then stops with the
reason. With encryption off, a configured key that opens the keyring file
still reads `.enc` files left over.

**Cost** (`go test -bench . -cpu 2 ./internal/blobstore/`, 75 KB
messages, on the development container's Xeon and disk, limited to two
cores as on the target hardware): a `Put` takes 1.27 ms encrypted
against 0.95 ms plaintext, fsync barriers included, and holds 78 KiB
instead of 4 KiB while it runs (one chunk buffer); reading a message
through `OpenMessage` takes 34 µs against 8 µs and holds 76 KiB per open
message. One extra query per inserting transaction reads the generation.
At the archive's rates (one sync batch at a time per account, SMTP
deliveries one by one) this stays far inside 4 GB and 2 cores; 100
messages open at once in the web server hold about 7.5 MiB.

### Offline conversion

`encryption enable` and `disable` (`archive.Enable`, `archive.Disable`)
convert the existing files in the foreground while no writer runs
(maintainer decision 5). They take the writers lock exclusively and the
blob lock exclusively, both with a try-lock, and refuse otherwise; then
the keyring lock for the rest of the run.

- **Start.** From off, `enable` writes the keyring file with a new
  generation (`Keyring.NextGeneration`; a database copy recorded off with
  the same archive id is merged in first) and only then records
  `encrypting` with it as the database copy (`SetBlobEncryption`, which
  continues the generation of the off row). So a dump recorded off,
  restored next to files `enable` encrypted later, no longer matches the
  keyring file, and the next writer walks `messages/` and derives
  `encrypting`. In any other state the copies are reconciled as at a
  writer's start; a change of direction (`on` or `encrypting` to
  `decrypting`, `decrypting` to `encrypting`) sets the new state, which
  increments the generation and resets the cursor.
- **Passes.** A pass walks `messages/` in hash order
  (`Store.MessagesAfter`, shard directories before the cursor unread) and
  then `orphans/` in `WalkDir` order (`Store.OrphansAfter`). A batch ends
  after 200 files or 256 MiB; `Conversion.Commit` then synchronizes the
  touched directories, unlinks the sources, synchronizes them again, and
  the cursor (the last hash, or a path below `orphans/`), `files_done` and
  `files_corrupt` are saved, guarded by the generation. A run resumes from
  the cursor; the terminal state (`FinishBlobEncryption` to `on`,
  `ClearBlobEncryption` to `off`) is set only after a pass that started
  from the beginning and converted nothing, so a resumed run always ends
  with a full pass.
- **One file** (`Conversion.Message`, `Orphan`): if the target form exists
  and reads back to the hash, the source is queued for unlinking. Otherwise
  the source is converted into `tmp/` while it is hashed (and decrypted
  with its binding checked), synchronized, read back in full, and linked
  under the target name (never replacing a file); a damaged target form is
  replaced by an atomic `rename` instead. A source that fails its hash or
  its authentication is `corrupt`: a plaintext one stays in place (never
  encrypted, coordinator decision 5), an encrypted one is moved by
  `disable` to `orphans/<time>/`, so that no `.enc` in `messages/` can make
  a later start derive `encrypting`. Files below `orphans/` keep their
  directory: `<sha256>.eml` becomes `<sha256>.enc` and back, any other file
  (a leftover of `tmp/`) `<sha256 of its content>.enc`, so `decrypt` opens
  each by its name.
- **`tmp/`.** Under the exclusive writers lock no `Put` can own a file
  in `tmp/`, so at the start of every run, also one that finds the state
  already final, every file there is moved to `orphans/<time>/tmp/`
  (`Store.MoveTempAside`) and converted with the orphans; with the state
  already `on`, `enable` encrypts just those. The `StaleTempAge` rule of
  verify protects writes in progress and does not apply offline.
- **Lock loss.** The run watches its writers lock connection as a writer
  does (`WritersLock.Watch`); a failed check cancels the run, which stops
  at the next file and returns `ErrWritersLockLost`. The keyring lock is
  taken before the state changes, and the terminal state is set only at
  the generation the run worked at (`FinishBlobEncryption`,
  `FinishBlobDecryption`). `disable` on a recorded off first derives the
  state from the files, as a writer start does, but always walks
  `messages/`, also when the keyring file matches the database copy: an
  encrypted file put back from `orphans/` is found and decrypted.
- **Progress.** The percentage is the cursor's first four hex digits ÷
  65536 (100 % in `orphans/`); the command prints it with files per second
  and the time left of the pass, and `status` shows it from the row.

Crash safety: before a target form is linked, the source is untouched;
between the link and the unlink both forms exist, and the rerun reads the
target form back before it unlinks the source; after the unlink and before
the cursor is saved, the rerun finds only the target form. **Cost**
(`TestConvertGate`, 50 KB files, two cores): 186–733 files/s on the
development container, bound by `fsync`; 0.2–0.8 of a core; the resident
set grows by 3–7 MiB, up to 17 MiB on a very fast disk (one 64 KiB chunk buffer, the copy buffer, at most
200 paths per batch); extra disk space at most one batch.

### Key changes and rekey (part 5)

`encryption add-key`, `remove-key` and `rotate-key` (`archive.ChangeKeyring`)
hold the writers lock **shared**, so no offline conversion runs meanwhile
while writers may keep running, and the keyring `flock`. Before the change
they reconcile the file with the database copy as a writer does at start
(off: the file merged with the copy an off row recorded, which is not
written, so that the next writer still compares it with the file); then
they write the database copy (compare-and-swap on its keyring generation)
and the file. Without that, a key added to a file restored from an older
backup would lose its slot to the newer database copy at the next start
(coordinator note on #437). None of them changes the current data key, so
the generation, which running writers pinned, stays.

`rotate-key` writes a new KEK (key file first, then the keyring) and
replaces one slot with one for it (`Keyring.ReplaceSlot`), under the
**existing** keyring key: the configured KEK's, or the one `--replace`
names (a lost archive key's, while a recovery key is configured). The new
slot is always labelled `archive key`, never inheriting the replaced
label; every other slot and the data keys stay, so no file changes
(coordinator decision on #437, 6640615690; review round 2 of #519). A new
keyring key there would protect nothing: `rotate-key` adds no data key, so
a replaced KEK with an older keyring copy can only reach data keys the
current keyring holds anyway. It does not help after a leak; `rekey` does.

A new **keyring key** (`Keyring.Rewrap`, `rekey` only) is wrapped for the
keys at hand only: the slots kept must be proven by their KEK (it unwraps
the slot to the current keyring key), so every other slot is dropped.

`encryption rekey` (`archive.Rekey`) runs offline like `enable`, with the
same locks, cursor, batches, generation guard and clean-pass rule:

1. From `on`, after reconciling the copies, it adds a new current data key
   and a new keyring key for the configured KEK, the kept keys and a new
   recovery key, and records the database copy with the state `rekeying`
   in one statement (`StartBlobRekey`, compare-and-swap on the keyring
   generation). The new recovery key is shown right then
   (`RekeyOptions.ShowRecoveryKey`), before the file is written and before
   any file is converted: the old recovery slots are already gone, so a run
   killed later must not leave the operator without one. Then it writes the
   file. A resumed run lists the slots and points to `add-key`. A KEK whose slot was removed before
   opens neither the new keyring nor its new data key, whatever older copy
   it has (coordinator decision on #437).
2. Each file whose header names another data key is decrypted and
   re-encrypted into `tmp/`, synchronized, read back with its hash checked,
   and renamed over the old file (`blobstore.NewRekey`); the directory is
   synchronized at the end of the batch, before the cursor moves on. There
   is never an unlink followed by a link, and the old key stays in the
   keyring, so a crash leaves a readable file and the rerun completes it.
   A plaintext straggler is encrypted as by `enable`. A file that cannot be
   decrypted stays and keeps its key named.
3. After a pass from the start that converted nothing, the data keys that
   no header in that pass named are deleted (`DeleteDataKeys`): first from
   the file, then from the database copy together with the state `on`
   (`FinishBlobRekey`). Interrupted in between, the state stays `rekeying`;
   the rerun's reconciliation brings the key back (union) and the next
   clean pass deletes it again. The other order would leave the state `on`
   with the key in the file, and the union would keep it for good.

### Commands

`encryption keygen` and `status`, and `decrypt`, need no database (see the
[command line](../reference/cli#encryption)).
`decrypt` recovers a file with the KEK and the keyring alone, from a copy of
`data/` or a backup.

## Journaling and fills

An smtp account has one folder, `store.SMTPFolder` (`JOURNAL`).
`Store.ReceiveMessage` stores what arrives over SMTP there, with an
envelope; a fill (`archive.Filler`) imports from IMAP into the same folder,
without one. The envelope is what tells them apart: `filled` in the message
view, `FindReceivedLocation`, `FirstReceived` and `JournaledMatchCandidates` only
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
(a message with an envelope in the account and the same `journalKey`, see
below) first, then younger than
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

**The journaled check** (fills and `import --journal`, advisory
GHSA-w6jg-ffvw-q4f5). `journalKey` (`internal/archive/journalkey.go`) is the
only comparison: the Message-ID, the Date as Unix seconds, and a SHA-256
over the From addresses (lowercased, without display names; the `<…>` parts
when the decoded value no longer parses) and the Subject in NFC, all taken
from what `ParseHeaders` returns. The mailbox side parses the four fields
`ListHeaders` fetches (`BODY.PEEK[HEADER.FIELDS (MESSAGE-ID DATE FROM
SUBJECT)]`; a fill keeps only the key per listed message, `fillHeader`) or
the file's header. The journaled side uses the stored columns `message_id`,
`sent_at`, `from_addr` and `subject`, which `BuildMeta` filled from
`ParseHeaders` (NULL is empty), so no file is read and no migration was
needed. `JournaledMatchCandidates` pre-selects rows with an envelope in the
account by Message-ID (`messages_message_id_idx`) and
`date_trunc('second', sent_at)`, at most ten per message of the batch; a cut
can only cause duplicates. A message without a Message-ID, a Date that
parses or a From never counts as journaled. `import --journal` schedules a
file for removal only for a message the check matched.

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

## GoBD export format v1

The GoBD export (#394) hands one customer group's mail to a tax audit
("Datenüberlassung", GoBD Rz. 167). `internal/gobd` writes and verifies the
format; `archive.GoBDExport` selects from the archive (part 2, #444). An export is a
directory, or the same tree as a ZIP. Paths are ASCII and never derived from
folder or group names:

```
README.txt                 German, then English (CRLF): contents, customer group, columns, how to verify, limits
index.xml                  description standard of the GoBD annex (UTF-8 declared): tables messages, locations, missing
messages.csv               one row per exported message (primary key sha256)
locations.csv              one row per selected place of a message (foreign key sha256 to messages)
missing.csv                messages of the selection without a valid file (sha256;file;reason), at most 100,000
mail/YYYY/MM/<sha256>.eml  original bytes; YYYY/MM is the month of sort_at in the export time zone
attest/attestations.jsonl  the attestation the statement names; the whole chain with "chain full"
attest/signing.pub, attest/retired-keys.pem
attest/leaves/<seq>.txt    only with "leaves yes"
SHA256SUMS                 GNU coreutils format ("<sha256>  <path>") for every file above
STATEMENT.txt              the signed export statement
```

**Pass order.** The writer (`gobd.Writer`) takes the files in one order, the
same for both sinks, so a ZIP needs no spool: the message files (`AddFile`,
which checks each file's bytes against its SHA-256 and size while copying),
then `messages.csv` (`AddMessageRow`, in the same order), `locations.csv`,
and in `Finish` `missing.csv`, `attest/`, README.txt, index.xml, SHA256SUMS
and STATEMENT.txt. It keeps no list per message: `Finish` gets the message
paths again from the caller (`Final.Files`) and compares them with a running
digest of `AddFile`, and the rows of `messages.csv` are checked against the
files the same way. Missing messages appear only in `missing.csv`, never in
the other tables. A ZIP entry cannot be taken back, so a message file that
fails its check while being copied into a ZIP breaks the export
(`ErrCannotUndo`); a directory sink removes it.

**From the archive** (`internal/archive/gobd.go`, `internal/store/gobd.go`).
One export runs on one dedicated connection. First, outside any
transaction, it waits for an attestation being written (a blocking
`pg_advisory_lock` on the writer lock, bounded by the command's context)
or, with a key and new messages, writes one; then it opens one read-only
`REPEATABLE READ` transaction, so the snapshot holds the attestation the
statement names. In it, the selection is resolved (hold, grant, the users
of a whole group), its customer group checked (on resume first against the
state file, then that it is one group, then against `--group` /
`--without-group`: `ErrGroupChanged`, `ErrSelectionSpansGroups`,
`ErrGroupClaimMismatch`), and built as a `store.Scope`: `OwnerScope` per
owner, `RestrictScope` for named accounts and folders and for a grant,
`MergeScopes`, then the period (an account without an owner is narrowed
to its named folders the same way), so the export shares the read predicate
(`scope_folder_granted`) with every other read; an export by hold adds
`location_holds`. The command line runs the same resolution in a short
snapshot of its own before the audit entry. The passes are B (emls, keyset
over `messages.seq`, 1000 rows a page, each file opened once by its hash
with `OpenMessage`, plaintext or encrypted at rest, never by the stored
path; a missing, unreadable or wrongly sized file (the plaintext size)
goes to the missing list before anything is written), A1 (`messages.csv`, the same keyset), A2 (`locations.csv`,
keyset over `message_locations.id`) and C. After the transaction, one read
committed query compares the selected users' and accounts' groups with the
snapshot (`GoBDResult.Moved`, exit 3). No row or advisory lock is held
while the export runs.

**Sinks.** `gobd.DirSink` writes into `<out>.partial`: directories 0700,
files 0600, each file under a temporary name (`.tmp-…`) renamed on commit.
Before SHA256SUMS the writer syncs everything (`syncfs` on Linux; where it
is not available, every file and directory one by one), and `Close`
removes the resume state, syncs again, renames the directory to `<out>`
and syncs its parent. A resumed export (`ResumeDirSink`) reads
`.export-state.json` (id, selection, time zone, the location cutoff
`max_location_id`, the group, the chain and leaves options), removes
everything but the emls, removes the emls that are no longer selected or
not at their path, keeps those whose size and SHA-256 are right
(`Writer.KeepFile`, without reading the archive's copy) and writes the
rest. A ZIP is written to `<out>.partial`, synced and renamed; it is never
resumed and removed on failure. The ZIP limit (200,000 messages) and a
free-space estimate are checked in the snapshot before the first file.

**CSV.** UTF-8 without BOM, CRLF, `;`, every text value in `"`, integers and
the `date` column (`DD.MM.YYYY`) without quotes, a header row. Inside a value,
`"` becomes `'` (GoBD annex, item 4), CR, LF, TAB, other control characters
and U+2028/U+2029 become a space, so each record is one line; a value longer
than 32,768 bytes is cut at a character boundary; a value starting with
`= + - @` gets a leading `'` (`internal/csvsafe`). The `.eml` stays
authoritative. The primary key columns lead (the description standard's DTD
lists `VariablePrimaryKey` before `VariableColumn`): `sha256` in messages and
missing, `sha256` to `uid` in locations. index.xml follows the structure of the
2004 DTD; it was not validated against the DTD from CaseWare, which is
available only on request.

**Statement** (`attest.Statement`). One field per line, starting with
`mail-archive export v1`: `id`, `created` (UTC), `software`, `selection`
(compact JSON), `time-zone`, `group-id N` and `group "<name>"` (JSON-quoted,
so any name stays on one line) or `group none`, `owners [...]` (sorted JSON
array of user names), `messages`, `locations`, `missing`, `bytes`,
`complete yes|no`, `resumed yes|no`, `chain one|full`, `leaves yes|no`,
`sha256sums <hex>`, `attestation <seq> <hash>` or `attestation none`,
`key <base64>` or `key none`, and last `sig <base64>` or `sig none`. The
Ed25519 signature, made with the attestation key, covers every line before
`sig`. The first line differs from an attestation's
(`mail-archive attestation v1`), so neither signature passes for the other.
`ParseStatement` accepts only the canonical form (the fields written again
give the same bytes), so what it returns is what the signature binds.

**Verify** (`gobd.Verify`, `mail-archive export verify`). Offline, without a
database, configuration or key. In order: the entries (names, duplicates also
after lowercasing and NFC, `..`, absolute paths, backslashes, directory,
symlink, encrypted or oddly compressed entries), the statement and its
signature (keys from `--public-key`, which must be the customer's copy kept
off the archive's host, not the one shipped in the export; else the export's
`attest/`, which shows only consistency), SHA256SUMS against the statement and the entries, the three
tables line by line against their format, each other and the statement's
counts, every message file against its name and size, index.xml against the
tables, the attestation against the statement and `--attestations` (one entry:
in the copy; a full chain: a prefix of it), each message against the leaves
file of its attestation (from the export or next to the copy), the
`locations.csv` owners against `owners`, and `--expect-group`. Directories
are read through `os.Root`, so a symbolic link cannot lead outside.

Memory on 4 GB / 2 cores: the directory writer stays at a few MB whatever
the number of messages (one table line, the missing list, the digests of the
few fixed files). archive/zip keeps about 290 bytes per entry until the end,
which is why a ZIP holds at most 200,000 messages (`MaxZIPMessages`): measured
at 200,000 small messages, 56 MB of live heap before the central directory is
written, about 190 MB resident with Go's default GC target. Verify keeps
about 90 bytes per message file and one line of a table at a time (at most
1 MiB): a 200,000-message ZIP took 3 s and about 200 MB resident, a
50,000-message directory 1 s and 52 MB. Before archive/zip reads a ZIP, a
prescan walks the central directory in constant memory. Because archive/zip
reads only the central directory while streaming unpackers read the local
headers, the prescan also requires the local entries to follow each other in
the records' order from offset 0 up to the central directory without a gap,
each with the record's name, flags and method and, in its data descriptor,
CRC-32 and sizes (two small reads per entry and no memory per entry: 0.16 s
and 7 KB allocated for 200,000 entries). The rule behind both checks is that
**every byte before the central directory is accounted for exactly once**: by
a local header, a data descriptor, or compressed data that the decompressor
consumes in full. The prescan accounts for headers, descriptors and each
entry's extent; verify reads every entry it checks through one path,
`openZIPEntry`, which takes the raw bytes (`zip.File.OpenRaw`), inflates them
through a byte-counting `io.ByteReader` (so flate never reads ahead), and at
the end of the stream requires that the consumed bytes equal the compressed
size, and checks the CRC-32 and the uncompressed size itself. Data after the
end of a Deflate stream, which archive/zip's own reader never looks at, is
refused that way. Only what archive/zip's writer produces is accepted:
Deflate entries, and zeros in the local header's CRC and size fields when a
data descriptor follows. The decompressors and buffers are pooled, so this
costs nothing measurable over archive/zip's reader (200,000 entries: 2.75 s,
767 MB allocated in all, against 2.9 s and 781 MB before). It refuses
more than
300,000 entries, more than 1 KiB of name, extra field and comment per entry or
64 MiB in all, and data before the archive (which would make archive/zip read
other records than the prescan); a declared size that Deflate cannot reach
from the compressed size is refused too. A crafted ZIP therefore costs verify
at most about 150 MB of live heap, and reading time in proportion to its
size. Both are single-threaded (one core); SHA-256 and Deflate set the speed.

## Reconcile

After a folder synced without error, `Syncer.SyncAccountWith` reconciles it
when it is due (`folders.last_reconciled_at` older than the interval, with
10 % slack, or a forced run). It uses the same connection, still in the
folder's `EXAMINE`, and the same account lock as the sync.

1. `imapsync.ListFlags` sends `UID FETCH 1:* (UID FLAGS)` (never for an
   empty folder) and fails with `ErrIncomplete` if fewer messages came back
   than the folder holds after the command (`EXISTS`, kept current by the
   client). Folders above `MaxReconcileMessages` (2,000,000 by default,
   `MAIL_ARCHIVE_RECONCILE_MAX_MESSAGES`) are refused. The two slices are
   allocated once for EXISTS + 1,000 messages, at most the limit, which is
   all `ListFlags` accepts; distinct flag combinations may take 4 MiB,
   each charged its bytes plus 88 B for its map entry (`flagSetOverhead`).
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

Both listing queries also return `text_match`: the query arm without its
attachment part, in the SELECT list and not in the filter block. For the
page's rows where it is false, `AttachmentSnippets` takes the matching
attachment with the lowest part index (one lookup per hash through
`attachment_texts_pkey`, again limited to the scope's accounts) and its
`ts_headline`, which replace the body snippet; the API names that
attachment as `matchedAttachment`.

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
