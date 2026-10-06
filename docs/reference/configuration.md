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
| `MAIL_ARCHIVE_COMMAND` | `mail-archive` | Command name used in copy-paste hints. The `./ma` wrapper sets it to `./ma`. |

## Docker Compose only

| Variable | Default | Description |
|---|---|---|
| `POSTGRES_PASSWORD` | (required) | Password of the bundled PostgreSQL. |
| `ARCHIVE_DIR` | `./data` | Host directory mounted as `/data`. |
| `WEB_PORT` | `8080` | Port of the web UI on `127.0.0.1`. |
| `POSTGRES_PORT` | `5432` | Port of PostgreSQL on `127.0.0.1`, for backups and development. |

Both published ports listen on `127.0.0.1` only. Changes to `.env` take effect
with `docker compose up -d web`.
