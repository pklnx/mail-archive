# Getting started

mail-archive runs with Docker Compose: one container for PostgreSQL, one for
the web server, and a one-shot container for commands.

## Requirements

- Docker with Docker Compose 2.19 or later (`docker compose version`); `./ma`
  uses its `--progress` option. Images exist for `linux/amd64` and
  `linux/arm64`, so Apple Silicon Macs and a 64-bit Raspberry Pi run them
  natively.
- IMAP access to your mailboxes. Some providers need it enabled first or an
  app password; see [Providers](./accounts#providers).

## Install

Download and extract the Docker Compose bundle of the latest
[release](https://github.com/pklnx/mail-archive/releases):

```sh
curl -fsSL https://github.com/pklnx/mail-archive/releases/latest/download/mail-archive.tar.gz | tar -xz
cd mail-archive
cp .env.example .env
```

The bundle holds `docker-compose.yml`, the `./ma` wrapper, `.env.example`
and a `README.txt`. Its `docker-compose.yml` uses the release's image,
`ghcr.io/pklnx/mail-archive:<version>`; nothing is built. See
[Versions](./operations#versions).

Edit `.env`:

- `POSTGRES_PASSWORD`: any long random password.
- `MAIL_ARCHIVE_SECRET_KEY`: encrypts the stored IMAP passwords. Create one
  with `openssl rand -base64 32` and **back it up** (see
  [Backups](./operations#backups)).

Create the schema and your web UI user, then start the web server. The
first `./ma` command downloads the images:

```sh
./ma migrate
./ma user add patrick --admin
docker compose up -d web
```

`user add` asks for a password (at least 12 characters). Open
<http://localhost:8080> and log in.

![Login page](/screenshots/login.png)

::: tip The `./ma` wrapper
`./ma` runs `docker compose run --rm mail-archive …`, starts PostgreSQL when
needed and hides Compose's progress output. Every `./ma` command also works
as `docker compose run --rm mail-archive …`, except that only `./ma migrate`
makes the data directory writable for the container.
:::

::: details From source instead
To run or change the code of a branch, clone the repository. Its
`docker-compose.yml` builds the image locally, and `./ma` rebuilds it after
code changes (about a second when nothing changed):

```sh
git clone https://github.com/pklnx/mail-archive.git
cd mail-archive
cp .env.example .env
```

The rest is the same. Upgrades start with `git pull`; see
[Upgrades](./operations#upgrades).
:::

<a id="linux-hosts"></a>

::: info Linux hosts
The containers run as UID 65532. `./ma migrate` creates `data/` (or
`ARCHIVE_DIR`) and makes it writable for that user. Docker Desktop on macOS
needs nothing.

Only a directory that already holds files, for example after a restore,
needs a fix by hand; `./ma migrate` then prints a warning with the command:

- Docker: `sudo chown -R 65532:65532 data`
- Rootless Docker, where UID 65532 in the container is another UID on the
  host: `docker run --rm --user 0 -v "$PWD/data:/data" alpine chown -R 65532:65532 /data`
- Podman (untested): `podman unshare chown -R 65532:65532 data`
:::

## Add your first account

In the web UI, click **Accounts** in the navigation, then **Add account**.
Enter the IMAP server, user name and password. The login is checked before
the account is saved, and the first sync starts right away.

![The Accounts page: the accounts as a table, with each account's actions in its row menu](/screenshots/accounts.png)

On the command line:

```sh
./ma account add personal --host imap.mail.de --username me@mail.de
./ma sync
./ma status
```

The password is prompted (or read from stdin with `--password-stdin`).

## What happens next

- The web server syncs every enabled account every 6 hours. See
  [Syncing](./syncing) to change that or to sync from cron.
- Archived messages appear in `./data` (`ARCHIVE_DIR` in `.env` moves it)
  and in the web UI.
- More people get their own login from an admin; see [Users](./users).
- Before you use the web UI from your home network or a VPN, read
  [Network access](../reference/security#network-access).
