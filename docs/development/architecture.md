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
| `internal/archive` | Sync of accounts and folders, deduplication, header parsing, the background `Runner` with its schedule, `reindex`. |
| `internal/imapsync` | Read-only IMAP client on top of go-imap v2. |
| `internal/blobstore` | Content-addressed `.eml` storage: files are named by their SHA-256 and written atomically. |
| `internal/mime` | MIME parsing: text for the search index, parts and HTML for display. |
| `internal/store` | PostgreSQL access: migrations (goose), queries (sqlc), the per-account sync lock. |
| `internal/web` | HTTP server, JSON API, request protection, embedded UI (`internal/web/ui`). |
| `internal/crypto` | AES-256-GCM for stored passwords. |
| `internal/config` | Configuration from environment variables. |
| `internal/imaptest` | In-memory IMAP server for tests. |
| `web/` | Web UI source (React, TypeScript, Vite, Tailwind CSS). |
| `docs/` | This documentation (VitePress). |
| `tools/demo` | Demo server with made-up mail, used for screenshots. |
| `tools/imap-testserver` | IMAP server for the Compose smoke test. |

## Data model

| Table | One row per |
|---|---|
| `users` | Login for the web UI: name, Argon2id hash, admin flag, `locked_at`. |
| `sessions` | Login session: SHA-256 of the cookie token, user, last use, expiry. |
| `accounts` | IMAP account: owner (`owner_id`), server, login, encrypted password, folder filters, `enabled`, `removed_at`. Names are unique per owner. |
| `folders` | Folder of an account, with its `UIDVALIDITY`, last archived UID and last sync time. |
| `messages` | Unique message content (by SHA-256): size, subject, sender, date, path of the file, body text and search vector. |
| `message_locations` | Place where a message was seen: folder, `UIDVALIDITY`, UID, flags, internal date. |
| `sync_runs` | Sync of an account: start, end, status, counters, error. |

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
requires the caller to hold the account's sync lock (`409` if a sync holds
it), and a sync reads the account again after taking the lock, so it skips
an account deleted while it was queued.

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

## Web server

The server embeds the built UI and serves the JSON API. A middleware checks
the `Host` header and, for state-changing requests, the `Origin` and the
content type; see [Security](../reference/security). The UI keeps its state
in the URL and talks only to the API.
