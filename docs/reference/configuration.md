# Configuration

All settings are environment variables. With Docker Compose they come from
`.env`; see `.env.example`.

## mail-archive

| Variable | Default | Description |
|---|---|---|
| `MAIL_ARCHIVE_DATABASE_URL` | (required) | PostgreSQL connection URL. Compose sets it from `POSTGRES_PASSWORD`. |
| `MAIL_ARCHIVE_SECRET_KEY` | (required to add accounts and sync) | 32 bytes, base64. Encrypts IMAP passwords. Create with `openssl rand -base64 32` or `./ma keygen`. Without it the web UI is browse-only. |
| `MAIL_ARCHIVE_DATA_DIR` | `./data` (`/data` in Docker) | Directory for the `.eml` files. |
| `MAIL_ARCHIVE_SYNC_INTERVAL` | `6h` | How often the web server syncs each enabled account without its own interval, and runs each [recurring fill](../guide/journaling#keep-importing-recurring-fill) without its own. A Go duration like `30m` or `12h`; at least `5m` and at least `MAIL_ARCHIVE_SYNC_INTERVAL_MIN`; `0` turns the schedule off for every account and every recurring fill (a fill just switched on, resumed or interrupted still finishes its current run). See [The schedule](../guide/syncing#the-schedule). |
| `MAIL_ARCHIVE_SYNC_INTERVAL_MIN` | `5m` | The shortest interval the schedule syncs any account at, or runs a recurring fill at, also one with a shorter own interval. From `5m` to `720h`. |
| `MAIL_ARCHIVE_RECONCILE_INTERVAL` | `24h` | How often a sync compares each folder with the server, to mark messages deleted there and update flags. Used by the web server's schedule and by `sync`. At least `1h`; `0` reconciles only with the **Check server** button or `sync --reconcile`. See [Reconcile](../guide/syncing#reconcile). |
| `MAIL_ARCHIVE_ALLOWED_HOSTS` | `localhost,127.0.0.1,::1` | Host names the web server accepts in the `Host` header, comma-separated. See [Security](./security#dns-rebinding). |
| `MAIL_ARCHIVE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `MAIL_ARCHIVE_REQUIRE_2FA` | `false` | Require TOTP for all non-admin users. Administrators always require TOTP. |
| `MAIL_ARCHIVE_PUBLIC_URL` | (none; Compose: `http://localhost:WEB_PORT`) | The address you open in the browser, like `https://archive.example.ts.net`: scheme, host name and port, no path. Passkeys work only there; without it they are off. Only `https://` with a host name, or `http://localhost`. Its host is accepted in addition to `MAIL_ARCHIVE_ALLOWED_HOSTS`. Changing the host later makes existing passkeys unusable. |
| `MAIL_ARCHIVE_ALERT_AFTER_FAILURES` | `3` | How many syncs of an account (or runs of a recurring fill) in a row must fail before it counts as failing: the alert, the banner and [`/healthz/sync`](../guide/operations#monitoring-and-alerts) use it. `1` to `100`. |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL` | (none: no alerts) | Where alerts about failing syncs are posted. `http://` or `https://`, no `#fragment`. A secret: it often contains a topic or token, and is never logged. See [Monitoring and alerts](../guide/operations#monitoring-and-alerts). |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_FORMAT` | `json` | `json` (Gotify, Slack, Mattermost, Discord and generic receivers) or `ntfy` (plain text with `Title`, `Priority` and `Tags` headers). |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION` | (none) | Sent as the `Authorization` header, like `Bearer tk_...` for ntfy or Gotify, so the token need not be in the URL. A secret, never logged. |
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
| `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS` | (required with the listen address) | Networks and addresses that may connect, comma-separated: `172.16.0.0/12, 192.0.2.10`. Everyone else gets `554`. There is no SMTP login: this is the only gate. |
| `MAIL_ARCHIVE_SMTP_MAX_SIZE` | `50M` | Largest message accepted, in bytes or with `K`, `M` or `G` (powers of 1024), from `1M` to `1G`. Must be at least Postfix's `message_size_limit`: Postfix drops a copy the archive refuses as too large. |
| `MAIL_ARCHIVE_SMTP_HOSTNAME` | the host name | Name in the greeting. |
| `MAIL_ARCHIVE_SMTP_TLS_CERT`, `MAIL_ARCHIVE_SMTP_TLS_KEY` | (none) | PEM certificate and key; both turn STARTTLS on. |

## Docker Compose only

| Variable | Default | Description |
|---|---|---|
| `POSTGRES_PASSWORD` | (required) | Password of the bundled PostgreSQL. |
| `ARCHIVE_DIR` | `./data` | Host directory mounted as `/data`. |
| `WEB_BIND` | `127.0.0.1` | Address the web UI is published on. `0.0.0.0` for all interfaces, or a LAN or Tailscale address. See [Network access](./security#network-access). |
| `WEB_PORT` | `8080` | Port of the web UI. |
| `POSTGRES_PORT` | `5432` | Port of PostgreSQL on `127.0.0.1`, for backups and development. |
| `SMTP_BIND` | `127.0.0.1` | Address the SMTP port for journaling is published on. A mail server on another machine needs a reachable address here, and that machine in `MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS`. |
| `MAIL_ARCHIVE_IMAGE` | the bundle's version; `mail-archive:local` in a clone | The image of the web server and the CLI. In a [release bundle](../guide/getting-started#install) it defaults to `ghcr.io/pklnx/mail-archive:<version>`; see [Versions](../guide/operations#versions). A value set in `.env` stays when you extract a newer bundle; see [Upgrades](../guide/operations#upgrades). In a clone of the repository the image is built locally under this name. |
| `COMPOSE_PROFILES` | (none) | `worker` starts the optional [worker service](../guide/operations#background-jobs) for background jobs with the other services. |
| `WEB_JOB_WORKERS` | `1` | `MAIL_ARCHIVE_JOB_WORKERS` of the web server; `0` with the worker service. |
| `WORKER_JOB_WORKERS` | `1` | `MAIL_ARCHIVE_JOB_WORKERS` of the worker service, `1` to `8`. |
| `WORKER_GOMEMLIMIT` | `576MiB` | Go's soft heap limit in the worker service: its `mem_limit` (768 MiB) minus 128 MiB per job at a time minus 64 MiB; `448MiB` for two. |
| `SMTP_PORT` | `2525` | Port of the SMTP server on the host. Compose always binds this port on the host; the container accepts connections only when `MAIL_ARCHIVE_SMTP_LISTEN=:2525` is set. |

The published ports listen on `127.0.0.1` only unless `WEB_BIND` or
`SMTP_BIND` is set.
Changes to `.env` take effect with `docker compose up -d web` (`docker compose up -d web worker` with the worker service).

Compose passes `MAIL_ARCHIVE_SYNC_INTERVAL`, `MAIL_ARCHIVE_SYNC_INTERVAL_MIN`, `MAIL_ARCHIVE_RECONCILE_INTERVAL`,
`MAIL_ARCHIVE_ALERT_AFTER_FAILURES` and the three `MAIL_ARCHIVE_NOTIFY_WEBHOOK_*`
variables to both the web server and the CLI (`./ma`), so `./ma sync` from
cron reconciles and sends alerts too.

Compose passes the two `MAIL_ARCHIVE_ATTEST_*` variables to both as well, so
`./ma attest` uses the same key as the web server.

Compose passes the `MAIL_ARCHIVE_SMTP_*` variables to the CLI too, so that
`./ma account add-smtp` can tell whether the web server receives mail.

Invalid values stop the server and every command at start, with a message
naming the variable. Messages about the webhook URL never repeat it.
