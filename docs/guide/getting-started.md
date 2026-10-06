# Getting started

mail-archive runs with Docker Compose: one container for PostgreSQL, one for
the web server, and a one-shot container for commands.

## Requirements

- Docker with a current Docker Compose plugin. Images are built for
  `linux/amd64` and `linux/arm64`, so Apple Silicon Macs run them natively.
- Git, to get the code.
- IMAP access to your mailboxes. Some providers need it enabled first or an
  app password; see [Accounts and providers](./accounts#providers).

## Install

```sh
git clone https://github.com/pklnx/mail-archive.git
cd mail-archive
cp .env.example .env
```

Edit `.env`:

- `POSTGRES_PASSWORD`: any long random password.
- `MAIL_ARCHIVE_SECRET_KEY`: encrypts the stored IMAP passwords. Create one
  with `openssl rand -base64 32` and **back it up** (see
  [Backups](./operations#backups)).

Then create the database schema and start the web server:

```sh
./ma migrate
docker compose up -d --build web
```

Open <http://localhost:8080>.

::: tip The `./ma` wrapper
`./ma` runs `docker compose run --rm mail-archive …`. It starts PostgreSQL
when needed, rebuilds the image after code changes (about a second when
nothing changed) and hides Docker Compose's progress output. Every `./ma`
command also works as `docker compose run --rm mail-archive …`.
:::

::: info Linux hosts
The containers run as UID 65532. Make the archive directory writable for it:
`sudo chown 65532:65532 data`. Docker Desktop on macOS handles this
automatically.
:::

## Add your first account

In the web UI, click **Manage accounts** at the bottom of the sidebar, then
**Add account**. Enter the IMAP server, user name and password; the login is
checked before the account is saved, and the first sync starts right away.

![Account page with two accounts](/screenshots/accounts.png)

The same works on the command line:

```sh
./ma account add personal --host imap.mail.de --username me@mail.de
./ma sync
./ma status
```

The password is prompted (or read from stdin with `--password-stdin`).

## What happens next

- The web server syncs every enabled account every 6 hours. See
  [Syncing](./syncing) to change that or to run syncs from cron.
- Archived messages appear in `./data` (set `ARCHIVE_DIR` in `.env` to move
  it) and in the web UI.
- Read [Security](../reference/security) before you make the web server
  reachable from anywhere but your own computer.
