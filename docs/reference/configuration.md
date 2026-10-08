# Configuration

All settings are environment variables. With Docker Compose they come from
`.env`; see `.env.example`.

## mail-archive

| Variable | Default | Description |
|---|---|---|
| `MAIL_ARCHIVE_DATABASE_URL` | (required) | PostgreSQL connection URL. Compose sets it from `POSTGRES_PASSWORD`. |
| `MAIL_ARCHIVE_SECRET_KEY` | (required to add accounts and sync) | 32 bytes, base64. Encrypts IMAP passwords. Create with `openssl rand -base64 32` or `./ma keygen`. Without it the web UI is browse-only. |
| `MAIL_ARCHIVE_DATA_DIR` | `./data` (`/data` in Docker) | Directory for the `.eml` files. |
| `MAIL_ARCHIVE_SYNC_INTERVAL` | `6h` | How often the web server syncs each enabled account. A Go duration like `30m` or `12h`; at least `5m`; `0` turns the schedule off. |
| `MAIL_ARCHIVE_ALLOWED_HOSTS` | `localhost,127.0.0.1,::1` | Host names the web server accepts in the `Host` header, comma-separated. See [Security](./security#dns-rebinding). |
| `MAIL_ARCHIVE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `MAIL_ARCHIVE_REQUIRE_2FA` | `false` | Require TOTP for all non-admin users. Administrators always require TOTP. |
| `MAIL_ARCHIVE_PUBLIC_URL` | (none; Compose: `http://localhost:WEB_PORT`) | The address you open in the browser, like `https://archive.example.ts.net`: scheme, host name and port, no path. Passkeys work only there; without it they are off. Only `https://` with a host name, or `http://localhost`. Its host is accepted in addition to `MAIL_ARCHIVE_ALLOWED_HOSTS`. Changing the host later makes existing passkeys unusable. |
| `MAIL_ARCHIVE_ALERT_AFTER_FAILURES` | `3` | How many syncs of an account in a row must fail before it counts as failing: the alert, the banner and [`/healthz/sync`](../guide/operations#monitoring-and-alerts) use it. `1` to `100`. |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL` | (none: no alerts) | Where alerts about failing syncs are posted. `http://` or `https://`, no `#fragment`. A secret: it often contains a topic or token, and is never logged. See [Monitoring and alerts](../guide/operations#monitoring-and-alerts). |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_FORMAT` | `json` | `json` (Gotify, Slack, Mattermost, Discord and generic receivers) or `ntfy` (plain text with `Title`, `Priority` and `Tags` headers). |
| `MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION` | (none) | Sent as the `Authorization` header, like `Bearer tk_...` for ntfy or Gotify, so the token need not be in the URL. A secret, never logged. |
| `MAIL_ARCHIVE_COMMAND` | `mail-archive` | Command name used in copy-paste hints. The `./ma` wrapper sets it to `./ma`. |

## Docker Compose only

| Variable | Default | Description |
|---|---|---|
| `POSTGRES_PASSWORD` | (required) | Password of the bundled PostgreSQL. |
| `ARCHIVE_DIR` | `./data` | Host directory mounted as `/data`. |
| `WEB_BIND` | `127.0.0.1` | Address the web UI is published on. `0.0.0.0` for all interfaces, or a LAN or Tailscale address. See [Network access](./security#network-access). |
| `WEB_PORT` | `8080` | Port of the web UI. |
| `POSTGRES_PORT` | `5432` | Port of PostgreSQL on `127.0.0.1`, for backups and development. |

Both published ports listen on `127.0.0.1` only unless `WEB_BIND` is set.
Changes to `.env` take effect with `docker compose up -d web`.

Compose passes `MAIL_ARCHIVE_SYNC_INTERVAL`, `MAIL_ARCHIVE_ALERT_AFTER_FAILURES`
and the three `MAIL_ARCHIVE_NOTIFY_WEBHOOK_*` variables to both the web server
and the CLI (`./ma`), so `./ma sync` from cron sends alerts too.

Invalid values stop the server and every command at start, with a message
naming the variable. Messages about the webhook URL never repeat it.
