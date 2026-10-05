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

A web UI with search is planned (see [Roadmap](#roadmap)).

## Quick start (Docker Compose)

Requirements: Docker with Compose v2. Images are built for `linux/amd64` and
`linux/arm64`, so this also runs natively on Apple Silicon Macs.

```sh
cp .env.example .env
# Edit .env: set POSTGRES_PASSWORD and a secret key from:
openssl rand -base64 32

docker compose up -d postgres
docker compose run --rm mail-archive migrate
```

Add accounts. The password is prompted and the login is checked before saving:

```sh
docker compose run --rm mail-archive account add private \
  --host imap.mail.de --username me@mail.de

docker compose run --rm mail-archive account folders private   # what will be archived
docker compose run --rm mail-archive sync
docker compose run --rm mail-archive status
```

Archived files appear in `./data` (configurable with `ARCHIVE_DIR`).

> On Linux hosts, the container runs as UID 65532. Make the archive
> directory writable for it: `sudo chown 65532:65532 data`.
> Docker Desktop on macOS handles this automatically.

### Run it regularly

`sync` is a one-shot command. Schedule it with cron (macOS and Linux), for
example every hour:

```cron
0 * * * * cd /path/to/mail-archive && /usr/local/bin/docker compose run --rm -T mail-archive sync >> sync.log 2>&1
```

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
docker compose run --rm mail-archive account add gmail \
  --host imap.gmail.com --username me@gmail.com \
  --include "[Gmail]/All Mail" --include "[Gmail]/Sent Mail"
```

(Folder names are localized, for example `[Gmail]/Alle Nachrichten`. Run
`account folders gmail` to see the exact names.)

Microsoft (Outlook.com, Microsoft 365) requires OAuth2 for IMAP and is not
supported yet.

## Commands

| Command | Purpose |
|---|---|
| `migrate` | Create or upgrade the database schema. |
| `keygen` | Print a random secret key. |
| `account add NAME` | Add an account (`--host`, `--username`, `--port`, `--tls tls\|starttls\|none`, `--include`, `--exclude`, `--password-stdin`). |
| `account list` | List accounts. |
| `account folders NAME` | Connect and show which server folders will be archived. |
| `account set-folders NAME` | Replace the include and exclude lists. |
| `account set-password NAME` | Replace the stored password. |
| `account enable\|disable NAME` | Include or exclude an account from `sync`. Archived data is kept. |
| `account remove NAME` | Delete an account that has no archived messages yet. |
| `sync [--account NAME]` | Copy new messages. Exits non-zero if any account failed. |
| `status` | Per-account statistics and the last sync result. |

## Configuration

All configuration comes from environment variables:

| Variable | Default | Description |
|---|---|---|
| `MAIL_ARCHIVE_DATABASE_URL` | (required) | PostgreSQL connection URL. |
| `MAIL_ARCHIVE_SECRET_KEY` | (required to add accounts and sync) | Base64 key, 32 bytes. Encrypts IMAP passwords. |
| `MAIL_ARCHIVE_DATA_DIR` | `./data` (`/data` in Docker) | Directory for `.eml` files. |
| `MAIL_ARCHIVE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |

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
```

Integration tests start an in-process IMAP server and create a throwaway
database for each test (the user in `TEST_DATABASE_URL` needs `CREATEDB`).

### Layout

```
cmd/mail-archive     CLI
internal/archive     sync orchestration, deduplication, header parsing
internal/imapsync    read-only IMAP client
internal/blobstore   content-addressed .eml storage
internal/store       PostgreSQL access and migrations
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

- JSON API and a single-page web UI (account management, browsing, full-text
  search with PostgreSQL).
- Login for the UI through any OpenID Connect provider.
- Optional daemon mode with a built-in schedule.

## License

[MIT](LICENSE)
