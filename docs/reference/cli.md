# Command line

With Docker Compose, run every command through the wrapper, for example
`./ma status`. The binary itself is called `mail-archive`. Every command has
`--help`.

## Setup

| Command | Purpose |
|---|---|
| `migrate` (or `migrate up`) | Create or upgrade the database schema. |
| `migrate status` | Show applied and pending migrations. |
| `migrate down --yes` | Roll back the latest migration. Usually deletes data; for development. |
| `keygen` | Print a random secret key for `MAIL_ARCHIVE_SECRET_KEY`. |

## Accounts

| Command | Purpose |
|---|---|
| `account add NAME` | Add an account. Flags: `--host` and `--username` (required), `--port`, `--tls tls\|starttls\|none` (default `tls`), `--include FOLDER`, `--exclude FOLDER` (repeatable), `--password-stdin`, `--skip-check`. The login is checked before saving unless `--skip-check` is set. |
| `account list` | List accounts with server, user, state (enabled, disabled, removed) and folder filters. |
| `account folders NAME` | Connect and show which folders will be archived, with their role (trash, junk, sent, …). |
| `account set-folders NAME` | Replace the folder filters with `--include` and `--exclude`. No flags: archive all folders. |
| `account set-password NAME` | Replace the stored password (`--password-stdin` to read it from stdin). |
| `account enable NAME`, `account disable NAME` | Include or exclude the account from automatic syncs. Archived mail is kept. |
| `account remove NAME` | Remove the account. Archived mail is kept; see [Disabling and removing](../guide/accounts#disabling-and-removing). |

Folder filters: with `--include`, only those folders are archived; `--exclude`
then removes folders from what is left. Names are matched case-insensitively.

## Archive

| Command | Purpose |
|---|---|
| `sync` | Copy new messages from all enabled accounts. Exits non-zero if any account failed. Accounts that are being synced elsewhere are skipped. |
| `sync --account NAME` | Only these accounts (repeatable), also when disabled. |
| `status` | Messages per account and the last sync result. |
| `reindex` | Extract search text from messages archived before full-text search existed. |
| `serve [--listen ADDR]` | Run the web server: UI, JSON API and sync schedule. Default `127.0.0.1:8080`. |
