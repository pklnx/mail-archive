# Getting started

mail-archive is offered as a hosted service: one instance per customer, run
by the operator. This guide speaks to three readers:

| Who | Does |
|---|---|
| **The operator** (pklnx) | Runs your instance: the server, the database, configuration, upgrades, backups and restores, the command line (`./ma …`), exports and keys. |
| **Customer admins** | Manage users, accounts, retention rules and legal holds in the web UI and see the audit log of their instance. The first admin is created by the operator. |
| **Users** | Read and search the mail of their own accounts and of accounts shared with them. |

Every command-line command in this guide (`./ma …`) runs on the host of
your instance, which only the operator can reach. When a page shows one,
the operator runs it; ask your operator. Everything else happens in the
web UI.

The operator has no login in your instance. What the operator changes or
exports on the command line is recorded in your
[audit log](./audit-log#the-fields) as command-line entries (which commands:
see the [command line](../reference/cli) reference). What the operator can reach on the host, and what
protects your mail there, is in [Security](../reference/security).

## Your instance

Your operator gives you the address of your instance (`https://…`) and the
first admin's user name and one-time password. Open the address and log in.

![Login page](/screenshots/login.png)

At the first login you choose your own password (at least 12 characters);
admins then set up two-factor authentication with an authenticator app. See
[Users](./users#the-first-admin).

## Requirements

- IMAP access to your mailboxes. Some providers need it enabled first or an
  app password; see [Providers](./accounts#providers).
- For [journaling](./journaling), control over your own mail server
  (Postfix): hosted mailboxes (GMX, mail.de, Gmail) cannot be journaled.

## Add your first account

In the web UI, click **Accounts** in the navigation, then **Add account**.
Enter the IMAP server, user name and password. The login is checked before
the account is saved, and the first sync starts right away.

![The Accounts page: the accounts as a table, with each account's actions in its row menu](/screenshots/accounts.png)

The operator can also add an account on the command line:

```sh
./ma account add personal --host imap.mail.de --username me@mail.de
./ma sync
./ma status
```

The password is prompted (or read from stdin with `--password-stdin`).

## What happens next

- Your instance syncs every enabled account every 6 hours. See
  [Syncing](./syncing) to give an account its own interval.
- Archived messages appear in the web UI; the operator keeps the message
  files and the database on the instance's host, with
  [backups](./operations#backups).
- More people get their own login from an admin; see [Users](./users).
- Read [Network access](../reference/security#network-access) for how the
  instance is reached.

## For the operator

An instance runs with Docker Compose: one container for PostgreSQL, one for
the web server, and a one-shot container for commands. The release bundle
and the Compose setup are the operator's tooling; customers do not install
anything.

### Operator requirements

- Docker with Docker Compose 2.19 or later (`docker compose version`); `./ma`
  uses its `--progress` option. Images exist for `linux/amd64` and
  `linux/arm64`, so Apple Silicon Macs and a 64-bit Raspberry Pi run them
  natively.
- A host with at least 4 GB of RAM and 2 cores per instance; see
  [Hardware and budgets](./operations#hardware-and-budgets).

### Install

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
  [Backups](./operations#backups)). The operator holds this key for every
  instance; the customer never needs it.

Create the schema and the customer's first admin, then start the web server.
The first `./ma` command downloads the images:

```sh
./ma migrate
./ma user add patrick --admin
docker compose up -d web
```

`user add` asks for a password (at least 12 characters). Hand the name and
the password to the customer's admin, who changes it at the first login. Open
<http://localhost:8080> to check that the login page answers; the customer
reaches the instance over HTTPS, see
[Network access](../reference/security#network-access).

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

### After the install

- The web server syncs every enabled account every 6 hours
  (`MAIL_ARCHIVE_SYNC_INTERVAL`); see [Syncing](./syncing#the-schedule).
- Archived messages appear in `./data` (`ARCHIVE_DIR` in `.env` moves it).
- Set up [backups](./operations#backups),
  [monitoring](./operations#monitoring-and-alerts) and, if the customer wants
  it, [tamper evidence](./tamper-evidence) and
  [journaling](./journaling#set-up-the-archive).
- Everything about running the instance is in [Backups and
  upgrades](./operations), the [configuration](../reference/configuration) and
  the [command line](../reference/cli) reference.
