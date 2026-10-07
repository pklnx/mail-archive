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

## Users

Users log in to the web UI. Names are lowercase (`a-z`, `0-9`, `.`, `-`,
`_`); `Patrick` and `patrick` are the same user. Passwords need at least 12
characters and are prompted twice, or read once from stdin with
`--password-stdin`.

| Command | Purpose |
|---|---|
| `user add NAME` | Create a user. `--admin` for an admin (admins will manage users in the web UI; they do not see other users' mail). |
| `user list` | List users with role, state (active, locked, must change password), number of accounts and passkeys, and last login. |
| `user set-password NAME` | Set a new password. Ends the user's sessions. |
| `user reset-password NAME` | Generate a password, print it once; the user must change it at the next login. Ends the user's sessions and removes their passkeys (`--keep-passkeys` keeps them). |
| `user remove-passkeys NAME` | Remove all passkeys of a user, for example after a lost device. Ends the user's sessions. |
| `user reset-2fa NAME` | Turn off the user's 2FA, for example after a lost phone. Ends the user's sessions; admins set it up again at the next login. |
| `user promote NAME`, `user demote NAME` | Grant or take away the admin role. |
| `user lock NAME`, `user unlock NAME` | Stop a user from logging in, or allow it again. Locking ends the user's sessions. |
| `user remove NAME` | Delete the user. Refused while the user owns accounts, also removed ones; hand them over with `account move` first. |

The last admin who can log in cannot be locked, removed or demoted. Admins
can do the same in the web UI; see [Users](../guide/users).

## Accounts

| Command | Purpose |
|---|---|
| `account add NAME` | Add an account. Flags: `--host` and `--username` (required), `--port`, `--tls tls\|starttls\|none` (default `tls`), `--include FOLDER`, `--exclude FOLDER` (repeatable), `--password-stdin`, `--skip-check`. The login is checked before saving unless `--skip-check` is set. |
| `account list` | List accounts with owner, server, login, state (enabled, disabled, removed) and folder filters. |
| `account folders NAME` | Connect and show which folders will be archived, with their role (trash, junk, sent, …). |
| `account set-folders NAME` | Replace the folder filters with `--include` and `--exclude`. No flags: archive all folders. |
| `account rename NAME NEW-NAME` | Rename the account; its mail moves with it. |
| `account set-password NAME` | Replace the stored password (`--password-stdin` to read it from stdin). |
| `account enable NAME`, `account disable NAME` | Include or exclude the account from automatic syncs. Archived mail is kept. |
| `account remove NAME` | Remove the account. Archived mail is kept; see [Disabling and removing](../guide/accounts#disabling-and-removing). Refused while the account is being synced. |
| `account move NAME --to USER` | Hand the account and its archived mail to another user. Also for removed accounts; refused if the user already has an account with that name. |

Account changes work while the account is being synced and apply from the
next sync; only `account remove` waits for the sync to finish. A command
that finds the account changed in the meantime (by the web UI or another
command) stops without changing anything; run it again.

Accounts belong to users, and names are unique per user. Every `account`
command takes `--user USER`; it is needed when several users have an account
with that name, and for `account add` when more than one user exists (with
exactly one user, new accounts are theirs; with none yet, the first user
created gets them). `account list` and `status` show all users' accounts
unless `--user` is given.

Folder filters: with `--include`, only those folders are archived; `--exclude`
then removes folders from what is left. Names are matched case-insensitively.

## Archive

| Command | Purpose |
|---|---|
| `sync` | Copy new messages from all enabled accounts. Exits non-zero if any account failed. Accounts that are being synced elsewhere are skipped. |
| `sync --account NAME` | Only these accounts (repeatable), also when disabled. `--user USER` limits to one user's accounts. |
| `status` | Distinct messages per account with owner and the last sync result (`--user USER` for one user). |
| `reindex` | Extract search text from messages archived before full-text search existed. |
| `serve [--listen ADDR]` | Run the web server: UI, JSON API and sync schedule. Default `127.0.0.1:8080`. |
