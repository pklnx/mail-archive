# mail-archive

> [!WARNING]
> **This project is entirely vibe-coded.** All code, configuration and
> documentation were written by an AI (Claude) under human direction, without
> line-by-line human review. Review it yourself before trusting it with
> important data, and keep independent backups of your mail.

`mail-archive` copies messages from one or more IMAP mailboxes into a single,
deduplicated archive. It is a **read-only copy**: the servers are never modified.

## Features

- **Read-only.** Folders are opened with `EXAMINE` and messages are fetched with
  `BODY.PEEK[]`, so nothing changes on the server, not even the `\Seen` flag.
  Messages deleted on the server stay in the archive.
- **Many mailboxes, one archive.** Identical messages (same bytes) are stored
  once. For each message, the archive records every account, folder and UID
  where it was found.
- **Incremental.** Each folder remembers its `UIDVALIDITY` and last UID, so only
  new messages are downloaded. If a server resets `UIDVALIDITY`, the folder is
  rescanned and deduplication prevents duplicate files.
- **Plain files.** Raw messages are stored as `.eml` files
  (`data/messages/ab/cd/<sha256>.eml`) that any mail client can open.
  Metadata lives in PostgreSQL.
- **Encrypted credentials.** IMAP passwords are stored in the database,
  encrypted with AES-256-GCM using a key that you provide.

- **Full-text search** in PostgreSQL over subject, sender and body, with
  German and English word stemming ("Rechnungen" finds "Rechnung").
- **JSON API** (`serve`) for browsing, searching and reading archived mail.
  A web UI on top of it is in progress (see [Roadmap](#roadmap)).

## Quick start (Docker Compose)

Requirements: Docker with a current Docker Compose. Images are built for
`linux/amd64` and `linux/arm64`, so this also runs natively on Apple Silicon
Macs.

```sh
cp .env.example .env
# Edit .env: set POSTGRES_PASSWORD and a secret key from:
openssl rand -base64 32

./ma migrate
```

`./ma` is a small wrapper around `docker compose run --rm mail-archive …`. It
starts PostgreSQL when needed, rebuilds the image after code changes (about a
second when nothing changed) and hides Docker Compose's progress messages.
Every command below also works as `docker compose run --rm mail-archive …`.

Add accounts. The password is prompted and the login is checked before saving:

```sh
./ma account add private --host imap.mail.de --username me@mail.de

./ma account folders private   # which folders will be archived
./ma sync
./ma status
```

`account add` and `account folders` show each folder's role (`trash`, `junk`,
`sent`, …) as reported by the server. Spam and trash folders are archived like
any other folder; the output suggests a ready-to-run `set-folders` command if
you want to skip them.

Archived files appear in `./data` (configurable with `ARCHIVE_DIR`).

> On Linux hosts, the container runs as UID 65532. Make the archive
> directory writable for it: `sudo chown 65532:65532 data`.
> Docker Desktop on macOS handles this automatically.

### Web API

```sh
docker compose up -d --build web   # http://localhost:8080
curl 'http://localhost:8080/api/messages?q=invoice'
```

There is no login yet, so the server is only reachable from this machine
(Compose publishes it on `127.0.0.1`). It also rejects requests whose `Host`
header is not `localhost` or `127.0.0.1`, which stops other websites from
reading your mail through DNS rebinding, and it only accepts state-changing
requests from its own origin. Do not expose it to a network until login is
available.

| Endpoint | Description |
|---|---|
| `GET /api/messages?q=&account=&folder=&after=&before=&limit=&cursor=` | List or search, newest first. `q` supports quotes, `OR` and `-word`; dates are `YYYY-MM-DD`; follow `nextCursor` for more. Snippets mark matches with U+E000/U+E001. |
| `GET /api/messages/{id}` | Headers, text, attachment list and where the message was found. |
| `GET /api/messages/{id}/html[?images=1]` | HTML body for a sandboxed iframe. Scripts are blocked; remote images only with `images=1`. |
| `GET /api/messages/{id}/raw` | The original `.eml`. |
| `GET /api/messages/{id}/parts/{n}` | One attachment. Only common image types are shown inline; everything else is a download. |
| `GET /api/accounts` | Accounts with folders and message counts. |
| `GET /api/status` | Same as `./ma status`. |

After upgrading from a version without search, run `./ma reindex` once so
that older messages become searchable.

### Run it regularly

`sync` is a one-shot command. Schedule it with cron (macOS and Linux), for
example every hour:

```cron
0 * * * * /path/to/mail-archive/ma sync >> /path/to/mail-archive/sync.log 2>&1
```

cron runs with a minimal `PATH`. If `docker` is not found, add a line such as
`PATH=/usr/local/bin:/usr/bin:/bin` at the top of the crontab.

## Provider notes

| Provider | Host | Notes |
|---|---|---|
| mail.de | `imap.mail.de` | Regular password. |
| GMX | `imap.gmx.net` | Enable IMAP in the web settings first. |
| WEB.DE | `imap.web.de` | Enable IMAP in the web settings first. |
| Gmail | `imap.gmail.com` | Requires 2-step verification and an [app password](https://myaccount.google.com/apppasswords). |
| iCloud | `imap.mail.me.com` | Requires an [app-specific password](https://support.apple.com/en-us/102654). Username is the full address. |

All use implicit TLS on port 993 (the default).

**Gmail:** labels appear as IMAP folders, so the same message shows up in many
folders. Deduplication keeps storage at one copy, but every label is still
downloaded. Archive only the folders that contain everything:

```sh
./ma account add gmail --host imap.gmail.com --username me@gmail.com \
  --include "[Gmail]/All Mail" --include "[Gmail]/Sent Mail"
```

(Folder names are localized, for example `[Gmail]/Alle Nachrichten`. Run
`account folders gmail` to see the exact names.)

Microsoft (Outlook.com, Microsoft 365) requires OAuth2 for IMAP and is not
supported yet.

## Commands

| Command | Purpose |
|---|---|
| `migrate` (or `migrate up`) | Create or upgrade the database schema. |
| `migrate status` | Show applied and pending migrations. |
| `migrate down --yes` | Roll back the latest migration. Usually deletes data; intended for development. |
| `keygen` | Print a random secret key. |
| `account add NAME` | Add an account (`--host`, `--username`, `--port`, `--tls tls\|starttls\|none`, `--include`, `--exclude`, `--password-stdin`). |
| `account list` | List accounts. |
| `account folders NAME` | Connect and show which server folders will be archived, with their role (trash, junk, sent, …). |
| `account set-folders NAME` | Replace the include and exclude lists. |
| `account set-password NAME` | Replace the stored password. |
| `account enable\|disable NAME` | Include or exclude an account from `sync`. Archived data is kept. |
| `account remove NAME` | Delete an account that has no archived messages yet. |
| `sync [--account NAME]` | Copy new messages. Exits non-zero if any account failed. |
| `status` | Per-account statistics and the last sync result. |
| `serve [--listen ADDR]` | Run the web server (JSON API). Default `127.0.0.1:8080`. |
| `reindex` | Extract text for full-text search from messages archived before search existed. |

## Configuration

All configuration comes from environment variables:

| Variable | Default | Description |
|---|---|---|
| `MAIL_ARCHIVE_DATABASE_URL` | (required) | PostgreSQL connection URL. |
| `MAIL_ARCHIVE_SECRET_KEY` | (required to add accounts and sync) | Base64 key, 32 bytes. Encrypts IMAP passwords. |
| `MAIL_ARCHIVE_DATA_DIR` | `./data` (`/data` in Docker) | Directory for `.eml` files. |
| `MAIL_ARCHIVE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `MAIL_ARCHIVE_ALLOWED_HOSTS` | `localhost,127.0.0.1,::1` | Host names the web server accepts (comma-separated). |
| `MAIL_ARCHIVE_COMMAND` | `mail-archive` | Command name used in copy-paste hints. Set to `./ma` by the wrapper. |

## Backups

Back up three things: the archive directory (`data/`), the PostgreSQL database
(`docker compose exec postgres pg_dump -U mailarchive mailarchive`), and the
secret key from `.env`. Without the key, archived mail stays readable, but you
have to re-enter the IMAP passwords with `account set-password`.

Note that `.eml` files are **not** encrypted on disk. Store the archive on an
encrypted volume (FileVault on macOS does this by default).

## Development

Requires Go (version in `go.mod`), Docker, and optionally
[golangci-lint](https://golangci-lint.run/) v2.

```sh
make build              # bin/mail-archive
make test-unit          # unit tests, no database needed
docker compose up -d postgres
make test TEST_DATABASE_URL='postgres://mailarchive:<password>@localhost:5432/mailarchive?sslmode=disable'
make lint
make vuln               # govulncheck
make docker-multiarch   # build amd64 + arm64 images
scripts/compose-smoke-test.sh   # end-to-end test of Compose and ./ma (also in CI)
```

Integration tests start an in-process IMAP server and create a throwaway
database for each test (the user in `TEST_DATABASE_URL` needs `CREATEDB`).

CI runs on a self-hosted runner; pull requests from forks run on
GitHub-hosted runners. See [docs/self-hosted-runner.md](docs/self-hosted-runner.md).

### Database migrations

Migrations are SQL files in `internal/store/migrations`, managed with
[goose](https://github.com/pressly/goose) and embedded in the binary. Each file
has an `Up` and a `Down` section:

```sql
-- +goose Up
ALTER TABLE messages ADD COLUMN thread_id TEXT;

-- +goose Down
ALTER TABLE messages DROP COLUMN thread_id;
```

Name new files with the next number (`00002_add_thread_id.sql`). Never edit a
migration that has already been released; add a new one instead. The store
tests roll every migration down and up again, so a broken `Down` section fails
CI.

### Pull requests and labels

`main` is protected: changes go through pull requests, which can only be merged
(squash) when all CI checks pass. To accept a pull request, review it and
enable **auto-merge**; GitHub merges it as soon as CI is green.

| Label | Meaning |
|---|---|
| `feature`, `bug`, `chore`, `docs` | Kind of change (set manually). |
| `dependencies` | Dependency update (set by Dependabot). |
| `db-migration` | Changes the database schema. Set automatically. Back up the database before deploying. |
| `breaking` | Requires manual steps when updating (set manually). |

Labels are defined in `.github/labels.json` and synced to GitHub when that file
changes on `main`.

### Database queries

Queries live as plain SQL in `internal/store/queries/*.sql`.
[sqlc](https://sqlc.dev/) checks them against the schema (the migrations) and
generates type-safe Go code in `internal/store/db`, which `internal/store`
wraps. After changing a query or adding a migration:

```sh
make generate      # regenerate internal/store/db (commit the result)
make sqlc-check    # what CI runs: vet queries and fail on stale code
```

### Layout

```
cmd/mail-archive     CLI
internal/archive     sync orchestration, deduplication, header parsing
internal/imapsync    read-only IMAP client
internal/mime        MIME parsing: text for search, parts for display
internal/web         HTTP server, JSON API, request protection
internal/blobstore   content-addressed .eml storage
internal/store       PostgreSQL access, migrations and SQL queries (sqlc)
internal/crypto      AES-256-GCM for stored credentials
internal/config      environment configuration
```

### Dependency security

- CI runs `govulncheck`, `go mod verify`, `go vet`, golangci-lint (including
  `gosec`) and tests with the race detector.
- Dependabot opens weekly update PRs for Go modules, GitHub Actions and Docker
  images.
- GitHub Actions are pinned to commit SHAs. Workflows run with read-only
  permissions.
- Direct dependencies are kept small: go-imap (with go-sasl), pgx, goose, cobra, x/term and
  x/text. go-imap v2 is still in beta.

## Roadmap

- Web UI (React) for browsing, searching and reading, served by `serve`.
- Account management and sync from the UI.
- Login through any OpenID Connect provider, so the UI can be reachable from
  other devices.
- Optional daemon mode with a built-in schedule.

## License

[MIT](LICENSE)
