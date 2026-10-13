# Audit log

The archive records who searched, read, exported and changed what: in the
web UI, with API keys and on the command line. The log cannot be changed or
shortened by the application. The **Log** page in the web UI shows it, and
`./ma audit` writes it out for a period.

## Privacy

The audit log records **reading behaviour**: searches with their text,
opened messages, downloads, the IP address and the browser of each request.

- **Admins can see all of it**, including other users' search queries,
  account names and which messages (by their SHA-256) they opened. Messages
  are stored once for all users, so an admin can tell that a user opened a
  message the admin has too.
- **Tell your users** before they use the archive. In a company, a works
  council may have a say in such recording.
- The log is personal data itself. Nothing in the archive deletes its rows;
  how long it is kept is part of the retention rules, not of the log.

## What is recorded

Each event writes exactly one entry.

| Action | When |
|---|---|
| `auth.login` | A login succeeded; `method` is `password`, `2fa` or `passkey`. With 2FA, the entry is written after the second step. |
| `auth.login_failed` | A login was refused; `method`, and `reason`: `wrong_password`, `unknown_user`, `locked`, `second_factor`, `unknown_passkey` or `passkey_invalid`. For an unknown user, the name is kept only if it is a valid user name, so a password typed into the name field is not stored. A refused [API key](../reference/api#api-keys) has `method: api_key` and `reason` `invalid` (no such key), `revoked`, `expired` or `locked`, with the key's `key_id`, `key_name` and owner when the key exists. The key itself is never stored. |
| `auth.logout` | A valid session logged out. |
| `message.search` | A list with a search or filter: `q`, `from`, `to`, `attachment`, `after`, `before` or `has`. The details hold every parameter that was set. |
| `message.open` | A message was opened. |
| `message.html` | Its HTML body was loaded; `images: true` with remote images. |
| `message.raw` | The `.eml` file was downloaded. |
| `message.part` | An attachment or inline image was loaded; `index`, `content_type` and `inline`. |
| `sync.start` | A sync was started by hand, for one account (`reconcile`) or all (`accounts`, the number queued). |
| `account.add`, `account.change`, `account.remove`, `account.move` | Account management in the web UI or on the command line. `account.change` names the changed fields, the old and new name, the new state and the new `sync_interval` (`default`, `manual` or a duration such as `30m0s`), never the server, login or password. `account.add` of an smtp account records its `addresses`; a change of them records `added` and `removed`. `account.remove` says whether the account was `deleted` or `removed`. |
| `user.add`, `user.change`, `user.remove`, `user.password_reset`, `user.password_set`, `user.2fa_reset`, `user.passkeys_removed` | User administration. |
| `profile.password_change`, `profile.2fa_enable`, `profile.2fa_disable`, `profile.recovery_codes`, `profile.passkey_add`, `profile.passkey_remove` | Changes a user makes to their own login: password, 2FA and passkeys. |
| `profile.change` | A user changed their own web UI settings. `fields` names them (`language`, `locale`), each with its old and new value (`from`, `to`). Saving an unchanged value records nothing. |
| `retention.rule_add`, `retention.rule_remove`, `hold.add`, `hold.release` | [Retention rules and legal holds](../reference/cli#retention), with the account, folder and years, or the hold's name, scope, filter and release note. Group `retention`. |
| `retention.preview`, `retention.confirm`, `retention.cancel`, `retention.batch`, `retention.finish`, `retention.sweep` | A [deletion run](../reference/cli#deletion): proposed (candidates, bytes), confirmed, cancelled, each batch (`deleted`, `kept`), its end with the totals, and removed files. The deleted messages themselves are recorded per message in the deletion records, not here. Runs resumed by the web server are written with actor `cli` and `by: web server`. |
| `api_key.create`, `api_key.revoke` | An API key was created (`key_name`, `scopes`, `expires_at`) or revoked (`key_id`, `key_name`, `reason`: `user`, `admin` or `cli`). The target is the key's owner. `user.password_reset` and `user.remove` say how many keys went with them (`api_keys_revoked`, `api_keys_removed`). |
| `data.import`, `data.export`, `data.backup` | `import`, `export` and `backup` on the command line, before they read or write mail. |
| `audit.export` | `./ma audit` itself. |

Not recorded:

- automatic syncs, on schedule or with `sync` on the command line (the sync
  history has them);
- page views: the account list, the status, a folder listed without a
  search, further pages of a list (the first page was recorded),
  expanding a conversation (the search was recorded), the conversation of a
  message;
- reading the log;
- requests with an API key that the web UI would not record either, such as
  `GET /api/status`; the key's last use is in the profile instead;
- logins refused because of too many failures (`429`), so a flood cannot
  grow the log. The login limits bound the failures that are recorded.

Some reading writes several entries. Opening a message with an HTML body
writes `message.open`, `message.html`, and one `message.part` per inline
image. The search box searches as you type: a pause of 300 ms in "invoice"
can write "inv" and "invoice". Each download request is recorded, also a
repeated or resumed one. The entries record what the browser asked for;
whether it was shown cannot be known.

## The fields

| Field | Content |
|---|---|
| Time | When the entry was written, from the database clock. |
| Actor | `session` (the web UI, also for login attempts), `cli` (the command line), or `api_key` (a script with an [API key](../reference/api#api-keys); the entry names the key and its owner). |
| User | ID and name as they were then. Entries stay when a user is removed. |
| Action | One of the actions above. |
| Target | A message (its SHA-256), an account or a user, with ID and name. |
| Client IP | The address of the browser. Behind a reverse proxy, see [Behind a reverse proxy](#behind-a-reverse-proxy). |
| User agent | The browser's identification, up to 256 bytes, stored once per distinct value. |
| Details | The search parameters, the login method and reason, and the other values named above. |

Command line entries have no user, IP or user agent. Their details hold
`os_user`, the operating system user (`65532` in the Docker image), and
`operator` from `MAIL_ARCHIVE_OPERATOR`, which `./ma` sets to the user on
the host. Neither is verified: whoever runs a command holds the database
password.

## Nothing happens without an entry

- **Reads, searches, exports, imports and syncs started by hand** are
  recorded first. If the entry cannot be written, the web UI answers `503`
  ("the audit log cannot be written; nothing was done") and the command
  fails, before any mail is sent, read or written.
- **Changes and logins** write their entry in the same database
  transaction: if the entry fails, the change is not saved and the login
  creates no session.
- **Failed logins and logouts** are recorded if possible. A failed login is
  refused anyway, and a logout always logs out.

## Append-only, and its limits

A trigger refuses `UPDATE`, `DELETE` and `TRUNCATE` on the log, on every
monthly partition and on the table of user agents, for every database role,
also in replication mode. The application's role has also given up these
privileges, which matters when it is not a superuser.

What this does not stop: whoever holds the database password owns the
tables. That role can drop the trigger, replace its function, grant itself
the privileges back, detach and drop a month, or drop the table. A
PostgreSQL superuser can also edit the files on disk. The trigger stops
mistakes and every code path of the application; it does not stop someone
with the database password. In Docker Compose the application connects as
the database superuser.

A rollback of the database migration that created the log refuses to run
while the log has entries.

## Storage

The log is split into one partition per month (UTC). The web server creates
the partitions of the current and the next three months at start and every
hour; every command creates the current and the next month's. A missing
partition is created when an entry needs it.

One entry takes about 290 bytes including its indexes (measured with a mix
of opened messages, searches and inline images). A user with 300 entries a
day adds about 30 MB a year.

## The Log page

**Log** in the sidebar shows the entries of the last 30 days, newest first.
Everyone sees their own entries: logins (also failed attempts with their
name), searches with their text, opened messages and downloads, changes,
and requests with their API keys. Admins see every user's entries and the
command line's, and can pick a user, the command line or API keys.

![Log page](/screenshots/log.png)

Filter by period and by a group of actions (logins, searches, reading,
export and backup, accounts, user management, own login settings, API keys,
manual syncs). A message shows its subject only to a user who has that
message too; otherwise the start of its SHA-256. A click on a subject opens
the message.

Two kinds of rows are folded, so that the list reads like what happened:

- the inline images of an HTML message go under its "Showed HTML" row;
- searches typed into the search box, which writes one row per pause
  ("inv", "invoice"), go under the last search when each is the start of
  the next and they are less than 10 seconds apart.

"+2 more" shows the folded rows. The log itself keeps every row; only the
page groups them. Reading the log writes no entry.

The same entries come from `GET /api/audit` (see the
[JSON API](../reference/api#audit-log)).

## Writing it out

```sh
./ma audit --since 2026-01-01 --format csv --out /data/audit-2026.csv
./ma audit --since 2026-10-01 --until 2026-10-31 --user alice --action read --format jsonl
```

`--since` is required, `--until` is exclusive (a plain date includes that
day). `--action` takes a group (`login`, `search`, `read`, `export`,
`accounts`, `users`, `profile`, `api_keys`, `sync`, `retention`) or an action, and is
repeatable.
`--user` selects a current user's entries. Without `--out`, the entries go
to stdout. A file must not exist yet; it is written with mode 0600 and
appears when it is complete. Each run is recorded as `audit.export`.

CSV has the columns `at`, `actor`, `user_id`, `user_name`, `api_key_name`,
`action`, `target_type`, `target_id`, `target_name`, `client_ip`,
`user_agent` and `details` (as JSON). A cell that starts with `=`, `+`,
`-`, `@`, a tab or a carriage return gets a leading `'`, so a spreadsheet
does not run a search query as a formula. JSON lines carry the values
unchanged.

## Behind a reverse proxy

The server records the address it is connected from. Behind a reverse
proxy (Caddy, nginx, `tailscale serve`) that is the proxy, and with Docker's
port publishing on `127.0.0.1` it is the gateway of the Compose network.
List the proxy in `MAIL_ARCHIVE_TRUSTED_PROXIES`: then the client's address
is taken from the proxy's `X-Forwarded-For` header, and the login limits
count per client again.

For Caddy on the same computer in front of Docker Compose, trust the
gateway of the Compose network. `docker network ls` lists it, usually as
`<directory>_default`:

```sh
docker network inspect mail-archive_default --format '{{(index .IPAM.Config 0).Gateway}}'
```

Then set, for example, `MAIL_ARCHIVE_TRUSTED_PROXIES=172.18.0.1` in `.env`.
List only the proxy: every address in the list may name any client. And
trust it only if all access goes through the proxy; a client that reaches
the web port directly from a trusted address could choose its own.
