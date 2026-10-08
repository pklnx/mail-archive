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
| `account list` | List accounts with owner, server, login, state (enabled, disabled, removed, import) and folder filters. |
| `account folders NAME` | Connect and show which folders will be archived, with their role (trash, junk, sent, …). |
| `account set-folders NAME` | Replace the folder filters with `--include` and `--exclude`. No flags: archive all folders. |
| `account rename NAME NEW-NAME` | Rename the account; its mail moves with it. |
| `account set-password NAME` | Replace the stored password (`--password-stdin` to read it from stdin). |
| `account enable NAME`, `account disable NAME` | Include or exclude the account from automatic syncs. Archived mail is kept. |
| `account remove NAME` | Remove the account. Archived mail is kept; see [Disabling and removing](../guide/accounts#disabling-and-removing). Refused while the account is being synced or imported into. |
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
| `sync` | Copy new messages from all enabled accounts. Exits non-zero if any account failed. Accounts that are being synced elsewhere are skipped. With `MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL` set, sends the alerts that became due at the end (for up to 15 seconds; failed deliveries are retried by the next sync or the web server). |
| `sync --account NAME` | Only these accounts (repeatable), also when disabled. `--user USER` limits to one user's accounts. |
| `status` | Distinct messages per account with its ID, owner and the last sync result (`--user USER` for one user). Import accounts show `import` instead of enabled. `FAILED` shows `-`, or how many syncs in a row failed and since when (`4x since 2026-10-01 12:00`). The ID is what [`/healthz/sync`](../guide/operations#healthz-sync) and alerts name. |
| `notify test` | Send a test message to `MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL`. Prints the HTTP status (never the URL) and exits non-zero if the receiver did not accept it. See [Monitoring and alerts](../guide/operations#monitoring-and-alerts). |
| `import NAME --from PATH --format mbox\|maildir` | Import mbox files or a Maildir into the import account `NAME`, created if needed. See below. |
| `reindex` | Extract body text, To and Cc recipients, attachment names and the links between replies from messages archived by an older version, so search, its filters and conversations find them. Needed once after an upgrade that says so; see [Upgrades](../guide/operations#one-time-steps). Safe to interrupt and rerun, and to run alongside the web server and syncs; a second `reindex` at the same time exits with `reindex already running`. |
| `verify` | Check the whole archive: every message file is present and matches its hash, and no files lie around without a database row. See below. |
| `export --format mbox\|maildir --out DIR` | Write archived mail for a mail client, for `--user USER`, or `--account NAME` with an optional `--folder NAME`. See below. |
| `backup DIR` | Dump the database into `DIR` with `pg_dump`, next to `BACKUP-NOTE.txt`. With Docker Compose: `./ma backup DIR`. See [Backups](../guide/operations#backups). |
| `serve [--listen ADDR]` | Run the web server: UI, JSON API and sync schedule. Default `127.0.0.1:8080`. |

### import

```sh
./ma import old-laptop --from /import/thunderbird --format mbox
./ma import takeout --from "/import/All mail Including Spam and Trash.mbox" --format mbox --folder Gmail
./ma import old-server --from /import/Maildir --format maildir --dry-run
```

| Flag | Default | |
|---|---|---|
| `--from PATH` | | A file or directory. With Docker Compose under `/import` (`./import` on the host, read-only). |
| `--format` | | `mbox` or `maildir`. |
| `--folder NAME` | from the files | The folder name of a single-folder source, or a prefix (`NAME/…`) for several folders. |
| `--user USER` | the only user | The owner of the import account. |
| `--max-message-size SIZE` | `256MiB` | Larger messages are skipped; the import then exits `1` (`partial`). |
| `--dry-run` | off | List folders and message counts, store nothing. |

Running the same import again adds only what is missing. Progress goes to
stderr after every batch, a summary per folder to stdout. Exit codes: `0`
done, `1` messages skipped or an error. Import accounts are never synced:
`sync --account NAME` refuses them, and `account enable`, `disable`,
`set-password`, `set-folders` and `folders` do not apply. `rename`, `move`
and `remove` work. See [Importing mail](../guide/importing).

### verify

`verify` reads every archived message from disk and hashes it again, then
checks every file in the data directory's `messages/` and `tmp/` against the
database. It changes no database row and no archived file.

| Finding | Meaning |
|---|---|
| `missing` | The database has the message, the file is gone. Listed with user, account, folder and UID. |
| `corrupt` | The file's content or size no longer matches its SHA-256. Listed like `missing`. |
| `bad-path` | The database points to a different path than the hash gives. |
| `orphan` | A message file without a database row, for example after an interrupted sync or a restore from an older dump. |
| `stale-temp` | A file in `tmp/` left by an interrupted sync. |
| `unexpected` | Anything else in `messages/` or `tmp/`: symlinks, other names. Never moved. |
| `io-error` | A file that could not be read. |

| Flag | Default | |
|---|---|---|
| `--jobs N` | 4 | Files hashed in parallel, 1 to 16. |
| `--lock-timeout D` | `1m` | The orphan check waits this long while a sync writes files, then is skipped. |
| `--fix-orphans` | off | Move orphans and `stale-temp` files to `orphans/<UTC time>/` in the data directory. Delete that directory yourself once you are sure. |

Exit codes: `0` nothing found, `1` findings, `2` the check is incomplete
(orphan check skipped, unreadable files, no database connection, wrong
flags). Progress goes to stderr every 10,000 messages.

### export

```sh
./ma export --user anna --format mbox --out /data/export/anna
./ma export --account private --folder INBOX --format maildir --out /data/export/inbox
```

Writes one mbox file (`DIR/<account>/<folder>.mbox`, mboxrd) or one Maildir
(`DIR/<account>/<folder>/{cur,new,tmp}`) per folder. Each message is written
once per folder, byte for byte; the mbox adds a final newline where one is
missing. Removed accounts are included, mail synced during the export is not.
`DIR` must not exist; it appears when the export is complete. Characters that
cannot be part of a file name (`/`, `\`, `%`, control characters, a leading
`.`) are written as `%XX`.

One export never mixes users: give `--user`, or `--account` (with `--user`
when several users have an account with that name). With Docker Compose,
`DIR` must be under `/data`, which is `./data` on the host. Exits `1` if
messages were skipped because their file is missing or corrupt (run `verify`).
See [Exporting mail](../guide/operations#exporting-mail).

