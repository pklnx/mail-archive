# Syncing

A sync copies new messages from an account into the archive. It never
changes anything on the server.

## How a sync works

For each selected folder:

1. The folder is opened read-only with `EXAMINE`.
2. If the folder's `UIDVALIDITY` changed since the last run, the server has
   renumbered it. The folder is scanned again from the start; deduplication
   prevents duplicate files.
3. Messages with a UID above the last archived one are fetched with
   `BODY.PEEK[]`, which does not set the `\Seen` flag.
4. Each message is stored as `data/messages/ab/cd/<sha256>.eml`. If a file
   with the same content exists, only a new *location* (account, folder, UID)
   is recorded.
5. Headers and body text are indexed for search. Batches of 100 messages are
   committed together with the folder's last UID, so an interrupted sync
   continues where it stopped.

Messages deleted on the server stay in the archive.

## Starting a sync

| How | What it syncs |
|---|---|
| Schedule of the web server | Every enabled account whose last sync started more than the interval ago. |
| **Sync** button on the account page | That account, also when it is disabled. |
| **Sync all** button | All enabled accounts. |
| `./ma sync` | All enabled accounts. |
| `./ma sync --account NAME` | Only that account, also when it is disabled. |

[Import accounts](./importing) are never synced, not even by name: run
`import` again to add mail to them.

The web server works through its queue one account at a time, so a large
first sync does not open many connections at once.

## The schedule

The web server checks every minute which accounts are due. The interval is
set with `MAIL_ARCHIVE_SYNC_INTERVAL` in `.env`:

```sh
MAIL_ARCHIVE_SYNC_INTERVAL=6h    # default
MAIL_ARCHIVE_SYNC_INTERVAL=30m   # at least 5m
MAIL_ARCHIVE_SYNC_INTERVAL=0     # off: sync only by hand
```

Apply a change with `docker compose up -d web`.

## Do you need cron?

Usually not. While the `web` container runs, its schedule syncs every
enabled account. cron is only useful if you do not run the web server.

In that case, schedule the one-shot command, for example every hour:

```text
0 * * * * cd /path/to/mail-archive && docker compose run --rm -T mail-archive sync >> sync.log 2>&1
```

This starts in about a second. `./ma sync` works too, but checks first
whether the image needs rebuilding, which takes a few seconds more on every
run. The plain `docker compose run` does not rebuild, so after an update run
`./ma migrate` once (see [Upgrades](./operations#upgrades)); it rebuilds the
image that cron and the web server use.

cron runs with a minimal `PATH`. If `docker` is not found, add a line such as
`PATH=/usr/local/bin:/usr/bin:/bin` at the top of the crontab. `sync` exits
with a non-zero code if any account failed.

## One sync per account

A PostgreSQL advisory lock guards each account. When the schedule, a button
and `./ma sync` try to sync the same account at once, only the first one
runs; the others skip it (`./ma sync` prints `skipped`). The lock belongs to
a database connection, so a crashed process cannot leave it behind. A sync
that was interrupted by a crash is shown as failed with the error
`interrupted` the next time the account is synced.

## Sync status

The account page shows for each account whether it is waiting, syncing (with
the number of messages fetched and new so far) or when its last sync ended
and how. The page refreshes every 2 seconds while a sync is waiting or
running, and every 30 seconds otherwise, so syncs from the schedule or the
command line show up as well. `./ma status` prints the same per account.

A sync ends as `ok`, `partial` (some folders failed; the others are archived)
or `failed` (for example a wrong password).

## Alerts

An account counts as **failing** when its last
`MAIL_ARCHIVE_ALERT_AFTER_FAILURES` syncs (default 3) failed in a row, from
the schedule, a button or `./ma sync` alike. It counts as **stale** when no
sync succeeded within two sync intervals (counted from when it was added, if
it never did). Both show in the web UI and in
[`/healthz/sync`](./operations#healthz-sync); a failing account also sends an
alert if a [webhook](./operations#webhook-alerts) is set up.

What counts:

- A `failed` sync extends the streak.
- An `ok` or `partial` sync ends it: the login worked. Folders that keep
  failing in `partial` syncs do not alert.
- A sync stopped by a shutdown, a sync shown as `interrupted` after a crash,
  and a sync skipped because another one runs change nothing.
- Imports are never counted.

Each failing streak sends **one alert** when it reaches the threshold and
**one recovery** after the next successful sync, no matter how many syncs
fail in between. If the account recovers before the alert went out, nothing
is sent. Stale accounts do not send alerts; `/healthz/sync` reports them.

Alerts and recoveries end silently when the account is disabled, removed or
deleted. A disabled account keeps its state: enabled again while still
failing, it sends no second alert, and its next successful sync sends the
recovery. If it was disabled before its alert went out, the alert follows
once it is enabled again.
