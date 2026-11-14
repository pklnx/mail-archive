# Getting started

mail-archive is offered as a hosted service. The operator (the provider of
the hosted service) runs the instance and is its admin: the operator
installs, upgrades and configures it, creates the users and sets up
accounts, retention rules, legal holds and exports on request. Customers and their people are users of that instance.
This guide speaks to both:

| Who | Does |
|---|---|
| **The operator** | Runs the instance: the server, the database, configuration, upgrades, backups and restores, the command line (`./ma …`), exports and keys. Is the instance's admin: creates and manages users, smtp accounts, retention rules and legal holds, and sees the audit log. |
| **Users** | Read and search the mail of their own accounts and of accounts shared with them; add and manage their own IMAP accounts; manage their own login. |

Where this guide says *admin*, that is the operator. Every command-line
command in it (`./ma …`) runs on the instance's host, which only the
operator can reach; ask your operator for anything shown as a command.
Everything else happens in the web UI.

What the operator changes or exports on the command line is recorded in the
[audit log](./audit-log#the-fields) as command-line entries (which commands:
see the [command line](../reference/cli) reference). What the operator can
reach on the host, and what protects your mail there, is in
[Security](../reference/security).

## Your login

Your operator gives you the address of the instance (`https://…`), your
user name and a one-time password. Open the address and log in. For a new
instance of your own, the operator may send a one-time setup link instead
(`https://…/#setup=…`): open it and choose the user name and password of the
instance's first admin, see [The first admin](./users#the-first-admin).

![Login page](/screenshots/login.png)

At the first login you choose your own password (at least 12 characters).
Two-factor authentication and passkeys are yours to set up in your profile;
see [Users](./users).

## Requirements

- IMAP access to your mailboxes. Some providers need it enabled first or an
  app password; see [Providers](./accounts#providers).
- For [journaling](./journaling), control over your own mail server
  (Postfix): hosted mailboxes (GMX, mail.de, Gmail) cannot be journaled. The
  operator sets the smtp account up with you.

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

- The instance syncs every enabled account every 6 hours. See
  [Syncing](./syncing) to give an account its own interval.
- Archived messages appear in the web UI; the operator keeps the message
  files and the database on the instance's host, with
  [backups](./operations#backups).
- More people get their own login from the operator; see [Users](./users).
- Read [Network access](../reference/security#network-access) for how the
  instance is reached.

## For the operator

The instance runs with Docker Compose: one container for PostgreSQL, one for
the web server, and a one-shot container for commands. The release bundle
and the Compose setup are the operator's tooling; users install nothing. The
hosted offering is one shared instance for all customers; a single customer
can also get an individual instance on request, set up the same way.

### Operator requirements

- Docker with Docker Compose 2.19 or later (`docker compose version`); `./ma`
  uses its `--progress` option. Images exist for `linux/amd64` and
  `linux/arm64`, so Apple Silicon Macs and a 64-bit Raspberry Pi run them
  natively.
- The smallest target for an instance is a host with 4 GB of RAM and 2
  cores; see [Hardware and budgets](./operations#hardware-and-budgets).

### Install

Download and extract the Docker Compose bundle of the latest
[release](https://github.com/keepia/archive/releases):

```sh
curl -fsSL https://github.com/keepia/archive/releases/latest/download/mail-archive.tar.gz | tar -xz
cd mail-archive
cp .env.example .env
```

The bundle holds `docker-compose.yml`, the `./ma` wrapper, `.env.example`
and a `README.txt`. Its `docker-compose.yml` uses the release's image,
`ghcr.io/keepia/archive:<version>`; nothing is built. See
[Versions](./operations#versions).

There are two ways to set the instance up: from configuration alone, the
way for hosted instances, or from the command line.

### Start from configuration

A new instance comes up from configuration alone: it creates its database
schema, turns [encryption at rest](./operations#encryption-at-rest) on, and
a one-time **setup link** creates its first admin. Nothing runs on the
command line of the instance.

1. Edit `.env`:
   - `POSTGRES_PASSWORD`: any long random password.
   - The secret key, which encrypts the stored IMAP passwords and TOTP
     secrets: `secret-key/secret.key` (`SECRET_KEY_DIR`), created with
     `openssl rand -base64 32`, owned by the container user (UID 65532) with
     mode 0400, and `KEEPIA_ARCHIVE_SECRET_KEY_FILE=/secret-key/secret.key`.
     **Back it up** (see [Backups](./operations#backups)).
   - `KEEPIA_ARCHIVE_PUBLIC_URL`: the address users open, like
     `https://archive.example.com`. Behind a reverse proxy also
     `KEEPIA_ARCHIVE_TRUSTED_PROXIES` (see [Security](../reference/security#login)).
2. The archive key, from your key custody: `keys/archive.key`
   (`ARCHIVE_KEY_DIR`, outside `data/`), owned by UID 65532 with mode 0400,
   and `KEEPIA_ARCHIVE_ARCHIVE_KEY_FILE=/keys/archive.key`. A new instance does
   not start without it. `./ma encryption keygen` creates one, with a
   recovery key (see [The archive key](#the-archive-key)); without a keyring,
   the first start creates one with a slot for the archive key alone, and
   `./ma encryption add-key` adds a recovery key later.
3. A setup token: the output of `./ma setup token` as
   `secret-key/setup.token`, owned by UID 65532 with mode 0400, and
   `KEEPIA_ARCHIVE_SETUP_TOKEN_FILE=/secret-key/setup.token`.
4. Start: `docker compose up -d`. The web server (and the worker, if you run
   it) applies the migrations (`KEEPIA_ARCHIVE_MIGRATE_ON_START=on` is the
   default in the bundled `docker-compose.yml`), turns encryption at rest on
   and logs `waiting for the first admin`.
5. Send the link `https://archive.example.com/#setup=<token>` to the
   customer, over a channel only they read. The token sits after `#`, so it
   never reaches the server's or a proxy's logs.

Whoever opens the link chooses the first admin's user name and password and
is logged in; two-factor authentication follows at once.

![The setup link's page](/screenshots/setup.png)

- The link works **once**, only while no user exists, and for **7 days**
  from the first start with that token. A start with another token starts a
  new window; a start without one revokes it. Once a first user exists,
  also one made with `./ma user add`, no setup link works again.
- Wrong tokens count as failed logins: 20 from one address within 15
  minutes block it for 15 minutes.
- Check that the customer used it: `./ma status` shows
  `setup: complete since …`, and the [audit log](./audit-log) has
  `user.setup` with the client address. Then remove the token from `.env`
  and `secret-key/`.
- `KEEPIA_ARCHIVE_ENCRYPTION_AT_SETUP=off` starts a new instance without
  encryption at rest: for test instances only, never for a customer's.

What the setup link guarantees, and the risk that remains, is in
[Security](../reference/security#login).

### From the command line

Edit `.env`:

- `POSTGRES_PASSWORD`: any long random password.
- `KEEPIA_ARCHIVE_SECRET_KEY`: encrypts the stored IMAP passwords and TOTP
  secrets; the web server does not start without it. Create one with
  `openssl rand -base64 32` and **back it up** (see
  [Backups](./operations#backups)). It can come from a file instead
  (`KEEPIA_ARCHIVE_SECRET_KEY_FILE`, see
  [Configuration](../reference/configuration#mail-archive)).

Create the schema and your own admin user, then start the web server. The
first `./ma` command downloads the images:

```sh
./ma migrate
./ma user add patrick --admin
docker compose up -d web
```

`user add` asks for a password (at least 12 characters). Open
<http://localhost:8080> and log in; at the first login an admin sets up
two-factor authentication. Users reach the instance over HTTPS, see
[Network access](../reference/security#network-access); they get their logins
from you, see [Users](./users#adding-users). An instance set up this way has
a user before its web server first starts, so it does not turn encryption at
rest on by itself: do that next, see [The archive key](#the-archive-key).

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
git clone https://github.com/keepia/archive.git
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

### The archive key

Encryption at rest stores every message file encrypted, under an archive
key that you hold apart from the data. New instances turn it on before they
archive anything. Create the key and its keyring once:

```sh
./ma encryption keygen
```

It writes `keys/archive.key` (`ARCHIVE_KEY_DIR` in `.env` moves it; keep it
outside `data/`) and the keyring `data/keys/keyring`, and prints a
**recovery key** once. Then:

- Add `KEEPIA_ARCHIVE_ARCHIVE_KEY_FILE=/keys/archive.key` to `.env`.
- Keep the recovery key offline, apart from this host and from the backups
  (a password manager, or paper in a safe). It opens the keyring when the
  key file is lost: set it as `KEEPIA_ARCHIVE_ARCHIVE_KEY` instead.
- Back up `keys/archive.key` apart from `data/` and its backups. On Linux it
  belongs to the container user: `sudo cp keys/archive.key /safe/place/`.
- Check with `./ma encryption status`.

Then turn it on. It runs only while the web server is stopped; on a new,
empty archive it takes a moment:

```sh
docker compose stop web
./ma encryption enable
docker compose up -d web
```

For an archive that already holds mail, the same command encrypts every
file and needs a maintenance window: see
[Encryption at rest](./operations#encryption-at-rest).

Without the archive key and every recovery key, encrypted mail cannot be
recovered, backups included. See [Encryption](../reference/cli#encryption).

### After the install

- The web server syncs every enabled account every 6 hours
  (`KEEPIA_ARCHIVE_SYNC_INTERVAL`); see [Syncing](./syncing#the-schedule).
- Archived messages appear in `./data` (`ARCHIVE_DIR` in `.env` moves it).
- Set up [backups](./operations#backups),
  [monitoring](./operations#monitoring-and-alerts) and, as needed,
  [tamper evidence](./tamper-evidence) and
  [journaling](./journaling#set-up-the-archive).
- Everything about running the instance is in [Backups and
  upgrades](./operations), the [configuration](../reference/configuration) and
  the [command line](../reference/cli) reference.
