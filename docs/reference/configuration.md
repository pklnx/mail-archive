# Configuration

This page is for the operator. All settings are environment variables; with
Docker Compose they come from the instance's `.env` (see `.env.example`), which
only the operator edits. Users change nothing here. The settings they notice
are named on the guide pages: the default sync interval
(`MAIL_ARCHIVE_SYNC_INTERVAL`), the 2FA requirement
(`MAIL_ARCHIVE_REQUIRE_2FA`), the instance's address for passkeys
(`MAIL_ARCHIVE_PUBLIC_URL`) and the SMTP server for
[journaling](../guide/journaling).

## mail-archive

| Variable | Default | Description |
|---|---|---|
| `MAIL_ARCHIVE_DATABASE_URL` | (required) | PostgreSQL connection URL. Compose sets it from `POSTGRES_PASSWORD`. |
| `MAIL_ARCHIVE_SECRET_KEY` | (required by `serve`, and to add accounts and sync) | 32 bytes, base64. Encrypts IMAP passwords and TOTP secrets. Create with `openssl rand -base64 32` or `./ma keygen`. The web server refuses to start without it, and with another key than the instance's: see [The secret key](./security#the-secret-key) and [`secret-key accept`](./cli#secret-key). The worker does not need it. |
| `MAIL_ARCHIVE_SECRET_KEY_FILE` | (none) | File holding the secret key, base64 (Compose: `/secret-key/secret.key`, from `SECRET_KEY_DIR`). Preferred; a systemd credential or a Docker secret works too if group and others have no permission on it. Setting both this and `MAIL_ARCHIVE_SECRET_KEY` is refused. Refused when group or others have any permission on it (0400 or 0600 are fine; 0440 or 0644 are not), when it is not a regular file and when it lies inside the data directory. The error names the reason, never the key. Every refusal of the secret key ends a command with exit code 4. |
| `MAIL_ARCHIVE_DATA_DIR` | `./data` (`/data` in Docker) | Directory for the message files (`.eml`, or `.enc` with encryption at rest) and the keyring. |
| `MAIL_ARCHIVE_SYNC_INTERVAL` | `6h` | How often the web server syncs each enabled account without its own interval, and runs each [recurring fill](../guide/journaling#keep-importing-recurring-fill) without its own. A Go duration like `30m` or `12h`; at least `5m` and at least `MAIL_ARCHIVE_SYNC_INTERVAL_MIN`; `0` turns the schedule off for every account and every recurring fill (a fill just switched on, resumed or interrupted still finishes its current run). See [The schedule](../guide/syncing#the-schedule). |
| `MAIL_ARCHIVE_SYNC_INTERVAL_MIN` | `5m` | The shortest interval the schedule syncs any account at, or runs a recurring fill at, also one with a shorter own interval. From `5m` to `720h`. |
| `MAIL_ARCHIVE_RECONCILE_INTERVAL` | `24h` | How often a sync compares each folder with the server, to mark messages deleted there and update flags. Used by the web server's schedule and by `sync`. At least `1h`; `0` reconciles only with the **Check server** button or `sync --reconcile`. See [Reconcile](../guide/syncing#reconcile). |
| `MAIL_ARCHIVE_ALLOWED_HOSTS` | `localhost,127.0.0.1,::1` | Host names the web server accepts in the `Host` header, comma-separated. See [Security](./security#dns-rebinding). Over plain HTTP under a host name other than `localhost` or a loopback address the guard against requests from HTML mail does not work; the server then logs a warning once per such name (see [Plain LAN access](./security#plain-lan-access)). |
| `MAIL_ARCHIVE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `MAIL_ARCHIVE_MIGRATE_ON_START` | `off` (Compose: `on`) | `on`: `serve` and `worker` apply pending database migrations when they start, as `migrate` does, under the same lock. `off`: they refuse to start (exit code 4) while migrations are pending. Either way they wait while a `migrate` or another start migrates, and refuse a database that a newer version migrated. See [Start-up migrations](../guide/operations#start-up-migrations). |
| `MAIL_ARCHIVE_SETUP_TOKEN_FILE` | (none) | File holding the setup token of a new instance (Compose: `/secret-key/setup.token`, from `SECRET_KEY_DIR`); read by `serve` only. While no user exists, the link `MAIL_ARCHIVE_PUBLIC_URL/#setup=<token>` creates the first admin, once, within 7 days of the first start with this token; another token starts a new window, a start without one revokes it, and after the first user it is not used. 32 to 256 printable ASCII characters without spaces, not one of the instance's keys; [`setup token`](./cli#setup-token) prints one. Preferred over `MAIL_ARCHIVE_SETUP_TOKEN`; both set is refused. The file has the rules of the secret key file. Every refusal ends `serve` with exit code 4 and names the reason, never the token. See [Start from configuration](../guide/getting-started#start-from-configuration). |
| `MAIL_ARCHIVE_SETUP_TOKEN` | (none) | The setup token itself, for a secret manager that sets variables. |
| `MAIL_ARCHIVE_REQUIRE_2FA` | `false` | Require TOTP for all non-admin users. Administrators always require TOTP. |
| `MAIL_ARCHIVE_PUBLIC_URL` | (none; Compose: `http://localhost:WEB_PORT`) | The address you open in the browser, like `https://archive.example.ts.net`: scheme, host name and port, no path. Passkeys work only there; without it they are off. Only `https://` with a host name, or `http://localhost`. Its host is accepted in addition to `MAIL_ARCHIVE_ALLOWED_HOSTS`. Changing the host later makes existing passkeys unusable. |
| `MAIL_ARCHIVE_ALERT_AFTER_FAILURES` | `3` | How many syncs of an account (or runs of a recurring fill) in a row must fail before it counts as failing: the alert, the banner and [`/healthz/sync`](../guide/operations#monitoring-and-alerts) use it. `1` to `100`. |
| `MAIL_ARCHIVE_FILL_MAX_FOLDER_MESSAGES` | `500000` | How many messages a [fill](../guide/journaling) lists of one IMAP folder per pass; its Message-IDs may take 96 bytes per message of this limit in all. A larger folder fails with an error that names this setting. `10000` to `10000000`; each 100,000 more cost about 42 MiB of the web server's memory. See [IMAP limits](../guide/operations#imap-limits). |
| `MAIL_ARCHIVE_RECONCILE_MAX_MESSAGES` | `2000000` | Folders with more messages are not [reconciled](../guide/syncing#reconcile); their sync ends `partial` with an error that names this setting. `10000` to `20000000`; each 100,000 more cost about 9 MiB. |
| `MAIL_ARCHIVE_IMAP_MAX_FOLDERS` | `10000` | How many folders one IMAP account may list. More fail the whole sync, fill or login check, with an error that names this setting. `100` to `200000`; each 1,000 more cost up to about 13 MiB. A login check may read about 1.1 KiB per folder of this limit, at least 16 MiB. |
| `MAIL_ARCHIVE_IMAP_IDLE_TIMEOUT` | `2m` | How long an IMAP server may send nothing while a command is in flight before the connection is closed (the folder fails; the next one gets a new connection). From `30s` to `30m`. |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL` | (none: no alerts) | Where alerts about failing syncs are posted. `http://` or `https://`, no `#fragment`. A secret: it often contains a topic or token, and is never logged. See [Monitoring and alerts](../guide/operations#monitoring-and-alerts). |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_FORMAT` | `json` | `json` (Gotify, Slack, Mattermost, Discord and generic receivers) or `ntfy` (plain text with `Title`, `Priority` and `Tags` headers). |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION` | (none) | Sent as the `Authorization` header, like `Bearer tk_...` for ntfy or Gotify, so the token need not be in the URL. A secret, never logged. |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_DETAIL` | `ids` | `ids`: alerts name accounts, fills and customer groups by ID only, with no account, owner or group names, IMAP user names, folder names or error texts. `full`: with those, which sends customer data to the receiver; for receivers you run yourself. See [Detail](../guide/operations#detail-ids-or-full). |
| `MAIL_ARCHIVE_TRUSTED_PROXIES` | (none) | Reverse proxies whose `X-Forwarded-For` header names the client, comma-separated networks or addresses like `172.18.0.1` or `10.0.0.0/8`. The audit log records that client, and the login limits count per client (IPv6 per `/64`). Their `X-Forwarded-Proto: https` also counts, as from loopback: the session cookie is then `__Host-` and `Secure`, and HSTS is sent. From other addresses the header is ignored and a warning is logged once. Under Docker Compose a proxy on the host connects from the Compose network's gateway: set this to that address. Every listed address may claim any client address: list only the proxy. See [Behind a reverse proxy](../guide/audit-log#behind-a-reverse-proxy) and [Network access](./security#network-access). |
| `MAIL_ARCHIVE_ATTEST_KEY_FILE` | `MAIL_ARCHIVE_DATA_DIR/attest/signing.key` | The Ed25519 key that signs [attestations](./cli#attest). Created by `attest keygen`; must have mode 0600. The attestation file, the leaves and the public keys always stay in `MAIL_ARCHIVE_DATA_DIR/attest/`, also when the key is elsewhere. Without the key, attestations are off and the web server logs it. |
| `MAIL_ARCHIVE_RETENTION_FILE_GRACE` | `168h` | How long the files of messages deleted by [retention](./cli#deletion) outlive their database rows. A backup (the dump first, then the files) made within it is complete, and restoring the last dump undoes a wrong deletion. At least `1h`, or `0` to remove them at the next sweep. |
| `MAIL_ARCHIVE_ATTEST_INTERVAL` | `24h` | How often the web server writes an attestation. At least `1h`; `0` turns the schedule off (`attest` still works by hand). `verify` reports `attest-stale` and `status` `overdue` when the newest attestation is older than twice this. |
| `MAIL_ARCHIVE_JOB_WORKERS` | `1` | How many [background jobs](../guide/operations#background-jobs) (extracting attachment text for search) the process runs at a time. For `serve` `0` to `8`; `0` runs none there, for a setup with a separate `worker`, which needs `1` to `8`. Each job at a time needs up to 224 MiB and about one core; see [Hardware and budgets](../guide/operations#hardware-and-budgets). Compose sets it from `WEB_JOB_WORKERS` for the web server and from `WORKER_JOB_WORKERS` for the worker service. |
| `MAIL_ARCHIVE_OPERATOR` | (none; `./ma` sets the host user) | Who runs a command, recorded with command line entries in the [audit log](../guide/audit-log). Not verified. |
| `MAIL_ARCHIVE_COMMAND` | `mail-archive` | Command name used in copy-paste hints. The `./ma` wrapper sets it to `./ma`. |

## SMTP

The SMTP server receives copies of every message from a mail server for
[smtp accounts](../guide/journaling). It runs in `serve` and is off unless
`MAIL_ARCHIVE_SMTP_LISTEN` is set; the other variables then require it.

| Variable | Default | Description |
|---|---|---|
| `MAIL_ARCHIVE_SMTP_LISTEN` | (none: off) | Address to listen on, like `:2525` or `127.0.0.1:2525`. In Docker, `:2525`; Compose publishes it on `SMTP_BIND:SMTP_PORT`. |
| `MAIL_ARCHIVE_SMTP_AUTH` | `required` | `required`: a mail server logs in with the [SMTP secret](./cli#smtp-secrets) of an smtp account, only after `STARTTLS`, and hands over mail only for that account; the certificate below is required, the allowlist optional. `off`: no login, the allowlist is required and the only gate, for a mail server on a network you control. Any other value stops the start. See [The SMTP server](./security#the-smtp-server). |
| `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS` | (required with `off`) | Networks and addresses that may connect, comma-separated: `172.16.0.0/12, 192.0.2.10`. Everyone else gets `554`. With `off` this is the only gate. With `required` it is optional: empty allows any address, which then has to log in; set, a client must be in it and log in. |
| `MAIL_ARCHIVE_SMTP_TLS_CERT`, `MAIL_ARCHIVE_SMTP_TLS_KEY` | (required with `required`) | PEM certificate (with its chain) and key; both turn STARTTLS on. The files are checked for changes at most once a minute when a client connects, and a renewed pair is used without a restart. In Docker, `/tls/fullchain.pem` and `/tls/privkey.pem` from `SMTP_TLS_DIR`; see [SMTP certificate](../guide/operations#smtp-certificate). |
| `MAIL_ARCHIVE_SMTP_MAX_CONNS_PER_ADDR` | `8` | With `required`: connections at once from one client address (an IPv6 address counts per `/64`), `1` to `64`; more get `421`. A mail server with one transport per smtp account opens up to 2 per account: raise it for more than 4 accounts behind one address. Refused with `off`. |
| `MAIL_ARCHIVE_SMTP_MAX_SIZE` | `50M` | Largest message accepted, in bytes or with `K`, `M` or `G` (powers of 1024), from `1M` to `1G`. Must be at least Postfix's `message_size_limit`: Postfix drops a copy the archive refuses as too large. |
| `MAIL_ARCHIVE_SMTP_HOSTNAME` | the host name | Name in the greeting. With `required`, the name in the certificate that mail servers connect to. |

## Encryption at rest

The archive key (KEK) opens the keyring `MAIL_ARCHIVE_DATA_DIR/keys/keyring`,
which holds the data keys. Create both with [`encryption keygen`](./cli#encryption).
A configured key is checked at every start of a command that reads the
configuration. While encryption at rest is on, every command that reads or
writes message files needs a key that opens the keyring; see [The archive
key](./security#the-archive-key).

| Variable | Default | Description |
|---|---|---|
| `MAIL_ARCHIVE_ARCHIVE_KEY_FILE` | (none) | File holding the archive key, 32 bytes base64 (Compose: `/keys/archive.key`, from `ARCHIVE_KEY_DIR`). Preferred; a systemd credential or a Docker secret works too if group and others have no permission on it. Refused when group or others have any permission on it (0400, 0600 or 0700 are fine; 0440 or 0644 are not), when it is not a regular file, when it lies inside the data directory (also through another path to it, such as a Compose `ARCHIVE_KEY_DIR` inside `ARCHIVE_DIR`), and when it equals `MAIL_ARCHIVE_SECRET_KEY`. The error names the reason, never the key. |
| `MAIL_ARCHIVE_ARCHIVE_KEY` | (none) | The archive key itself, base64, for a secret manager that sets variables. Setting both is refused. A recovery key printed by `encryption keygen` or `add-key` can be given here to open the keyring when the key file is lost. |
| `MAIL_ARCHIVE_ENCRYPTION_AT_SETUP` | `on` | `on`: a new instance (no user, no message, no state of encryption at rest yet) turns encryption at rest on at the first start of `serve` or `worker`, before anything is archived, and does not start without an archive key (exit code 4). Without a keyring file it creates one with a slot for the archive key alone. `off`: a new instance starts without encryption at rest, **for test instances only** (CI, demos), never for a customer's; `./ma status` says so. An instance that is not new is not affected either way. |

The archive key is separate from `MAIL_ARCHIVE_SECRET_KEY` on purpose:
losing the secret key costs the stored IMAP passwords, losing the archive key
and every recovery key costs all encrypted mail. A key file on the same disk
as `data/` protects copies and backups of `data/`, not snapshots of that disk:
move `ARCHIVE_KEY_DIR` to another disk or a secret store for real separation.

The processes that write message files (`serve`, `worker`, `sync`, `import`,
`account fill`, `reindex`) hold a session advisory lock on a database
connection of their own, so that the offline encryption commands run only
while none of them does. Connect them, and the encryption commands, to
PostgreSQL directly or through a pooler in session mode: a pooler in
transaction mode (PgBouncer `pool_mode=transaction`) breaks session locks.

## Docker Compose only

| Variable | Default | Description |
|---|---|---|
| `POSTGRES_PASSWORD` | (required) | Password of the bundled PostgreSQL. |
| `ARCHIVE_DIR` | `./data` | Host directory mounted as `/data`. |
| `SECRET_KEY_DIR` | `./secret-key` | Host directory mounted read-only as `/secret-key` into the web server and the CLI, not the worker. Put the secret key there as `secret.key`, owned by the container user (UID 65532) with mode 0400, and set `MAIL_ARCHIVE_SECRET_KEY_FILE=/secret-key/secret.key` instead of `MAIL_ARCHIVE_SECRET_KEY`. Keep it out of `ARCHIVE_DIR`. |
| `ARCHIVE_KEY_DIR` | `./keys` | Host directory mounted read-only as `/keys` into the web server, the worker and the CLI, and writable only by `./ma encryption keygen` and `./ma encryption rotate-key`. The default lies in the Compose project directory, next to `./data` on the same disk; keep it out of `ARCHIVE_DIR`, and move it to another disk or a secret store for real separation. |
| `WEB_BIND` | `127.0.0.1` | Address the web UI is published on. `0.0.0.0` for all interfaces, or a LAN or Tailscale address. See [Network access](./security#network-access). |
| `WEB_PORT` | `8080` | Port of the web UI. |
| `POSTGRES_PORT` | `5432` | Port of PostgreSQL on `127.0.0.1`, for backups and development. |
| `SMTP_BIND` | `127.0.0.1` | Address the SMTP port for journaling is published on. A mail server on another machine needs a reachable address here: `0.0.0.0` for a hosted instance with `MAIL_ARCHIVE_SMTP_AUTH=required`; with `off`, also that machine in `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS`. |
| `MAIL_ARCHIVE_IMAGE` | the bundle's version; `mail-archive:local` in a clone | The image of the web server and the CLI. In a [release bundle](../guide/getting-started#install) it defaults to `ghcr.io/pklnx/mail-archive:<version>`; see [Versions](../guide/operations#versions). A value set in `.env` stays when you extract a newer bundle; see [Upgrades](../guide/operations#upgrades). `./ma restore-dump` needs the image of the same release as `./ma`. In a clone of the repository the image is built locally under this name. |
| `COMPOSE_PROFILES` | (none) | `worker` starts the optional [worker service](../guide/operations#background-jobs) for background jobs with the other services. |
| `WEB_JOB_WORKERS` | `1` | `MAIL_ARCHIVE_JOB_WORKERS` of the web server; `0` with the worker service. |
| `WORKER_JOB_WORKERS` | `1` | `MAIL_ARCHIVE_JOB_WORKERS` of the worker service, `1` to `8`. |
| `WORKER_GOMEMLIMIT` | `576MiB` | Go's soft heap limit in the worker service: its `mem_limit` (768 MiB) minus 128 MiB per job at a time minus 64 MiB; `448MiB` for two. |
| `SMTP_PORT` | `2525` | Port of the SMTP server on the host. Compose always binds this port on the host; the container accepts connections only when `MAIL_ARCHIVE_SMTP_LISTEN=:2525` is set. `587` for a hosted instance: many networks block outgoing connections to port 25, but not to 587. |
| `SMTP_TLS_DIR` | `./tls` | Host directory with the SMTP certificate and key, mounted read-only as `/tls` into the web server by `docker-compose.smtp.yml`. Use that file with every command that starts the web server, most simply with `COMPOSE_FILE=docker-compose.yml:docker-compose.smtp.yml` in `.env`. The key must be readable by the container user (UID 65532). See [SMTP certificate](../guide/operations#smtp-certificate). |

The published ports listen on `127.0.0.1` only unless `WEB_BIND` or
`SMTP_BIND` is set.
Changes to `.env` take effect with `docker compose up -d web` (`docker compose up -d web worker` with the worker service).

Compose passes `MAIL_ARCHIVE_SYNC_INTERVAL`, `MAIL_ARCHIVE_SYNC_INTERVAL_MIN`, `MAIL_ARCHIVE_RECONCILE_INTERVAL`,
`MAIL_ARCHIVE_ALERT_AFTER_FAILURES` and the four `MAIL_ARCHIVE_NOTIFY_WEBHOOK_*`
variables to both the web server and the CLI (`./ma`), so `./ma sync` from
cron reconciles and sends alerts too.

Compose passes the four IMAP limits (`MAIL_ARCHIVE_FILL_MAX_FOLDER_MESSAGES`,
`MAIL_ARCHIVE_RECONCILE_MAX_MESSAGES`, `MAIL_ARCHIVE_IMAP_MAX_FOLDERS`,
`MAIL_ARCHIVE_IMAP_IDLE_TIMEOUT`) to both as well, so `./ma sync`, `./ma fill`
and `./ma account add` use the same limits as the web server.

Compose passes the two `MAIL_ARCHIVE_ATTEST_*` variables to both as well, so
`./ma attest` uses the same key as the web server, and the two
`MAIL_ARCHIVE_ARCHIVE_KEY*` variables to the web server, the worker and the CLI.

Compose passes the `MAIL_ARCHIVE_SMTP_*` variables to the CLI too, so that
`./ma account add-smtp` can tell whether the web server receives mail.

Invalid values stop the server and every command at start, with a message
naming the variable. Messages about the webhook URL never repeat it.
