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

Messages deleted on the server stay in the archive. A
[reconcile](#reconcile) finds out which ones those are.

## Starting a sync

| How | What it syncs |
|---|---|
| Schedule of the web server | Every enabled account whose last sync started more than the interval ago. |
| **Sync** button on the account page | That account, also when it is disabled. |
| **Check server** button on the account page | That account, and then [reconciles](#reconcile) every folder not compared in the last 5 minutes. |
| **Sync all** button | All enabled accounts. |
| `./ma sync` | All enabled accounts. |
| `./ma sync --account NAME` | Only that account, also when it is disabled. |
| `./ma sync --reconcile` | Syncs and reconciles every selected folder now (combines with `--account`). |

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

## Reconcile

A sync only fetches messages with UIDs above the last archived one. It does
not notice when a message is deleted on the server later, or when its flags
change. A reconcile compares a folder with the server and records both:

- Messages the server no longer lists are marked **no longer on the
  server**. They stay in the archive, in search, in conversations and in
  exports.
- A message listed again under the same UID loses the mark.
- Flags (`\Seen`, `\Answered`, `\Flagged`, …) are updated to the server's.
  `\Recent` is never stored: it only describes one session.

It runs right after a folder's sync, on the same connection and under the
same [lock](#one-sync-per-account), so it never overlaps a sync, another
reconcile or the deletion of the account.

### What it costs

After `EXAMINE`, a reconcile sends one command:
`UID FETCH 1:* (UID FLAGS)`. The server answers with every UID and its
flags, no message bodies. Nothing is written to the server: no `STORE`, no
`EXPUNGE`, no read-write `SELECT`.

Measured against a local test server:

| Messages in the folder | Time | Extra memory |
|---|---|---|
| 100,000 | 0.6 s | 22 MB |
| 500,000 | 4 s | 30 MB |

On a real server the transfer adds roughly 40 bytes per message. Only rows
that change are written to the database.

Folders with more than 2,000,000 messages are not reconciled. The sync of
such a folder ends `partial` with an error that says so.

### When it runs

A folder is reconciled when its last reconcile is older than
`MAIL_ARCHIVE_RECONCILE_INTERVAL` (default `24h`), by the web server's
schedule and by `./ma sync` alike. So a daily cron job reconciles daily, and
a folder whose reconcile failed is retried with the next sync.

```sh
MAIL_ARCHIVE_RECONCILE_INTERVAL=24h   # default
MAIL_ARCHIVE_RECONCILE_INTERVAL=1h    # the minimum
MAIL_ARCHIVE_RECONCILE_INTERVAL=0     # only with the button or --reconcile
```

A folder is reconciled only after it synced without error in the same run.
If the listing breaks off or holds fewer messages than the folder, nothing
is marked: the sync ends `partial`, which does not count toward
[alerts](#alerts).

### Renumbered and vanished folders

When the server renumbers a folder (`UIDVALIDITY` changes), the sync scans
it again and stores a new location for every message still there. The
reconcile then marks the old locations as gone. The message view shows them
as *renumbered*, and the messages do not count as gone, because their new
location is present.

A folder that the account's folder selection includes but the server no
longer lists has all its messages marked gone, and a warning is logged.
Folders excluded from the selection are never checked: whether their mail
is still on the server is unknown, not gone.

Removed accounts are never reconciled, and [import accounts](./importing)
have no server.

### What "only in archive" means

A message counts as **only in the archive** when it has locations in your
IMAP accounts and all of them are gone. A message moved to another folder on
the server is not: its new folder has it. A message also found in an import
account still counts, because the import says nothing about a server.

The sidebar entry **Only in archive** lists these messages, and the account
page shows their number per account (`GONE` in `./ma status`). The folder
counts in the sidebar keep counting gone messages: the archive still has
them.

Each user only sees their own accounts' state. If two users archived the
same message and only one of them deleted it, only that user sees it as
gone.

### Log

After a reconcile that changed anything, the log has one line per account
with the number of reconciled folders and of messages gone, back and with
new flags. A warning is logged when one folder loses at least 100 messages,
or at least 10 % of them, in one run, and when a folder vanished. The log
names folders and counts, never subjects or addresses.

### Alert for mail deleted on the server

With a [webhook](./operations#webhook-alerts) set up, a run that finds many
of an account's messages deleted sends one alert. It counts **messages
that lost their last copy on the server** in that account during the run:

- A message moved to another folder of the account is not lost, nor is a
  renamed folder: the messages are still on the server. Neither is a
  folder the server renumbered.
- A folder's first reconcile never counts. It finds what was deleted
  before, for example everything deleted before the upgrade that brought
  reconcile.
- Copies in other accounts, also of other users, do not matter.

The run alerts when it lost **at least 100 messages**, or **at least 10**
that make up **at least 10 %** of the messages the account had on the
server before. Losing 3 of 40 drafts does not alert; losing 12 of 20
messages does.

Each run alerts at most once per account. Losses spread over several runs,
each below the threshold, do not add up. A run stopped by a shutdown after
some folders were reconciled still alerts for those.

A message moved into a folder whose sync failed in the same run counts as
lost, because its new copy is not archived yet. The next run of that folder
finds it; the alert says that the messages stay in the archive.

An alert that cannot be delivered is retried like the
[sync alerts](./operations#webhook-alerts) and dropped 24 hours after the
run. So setting up a webhook later never sends old losses.

### CONDSTORE

Servers with CONDSTORE or QRESYNC could report only the changes since the
last run. mail-archive does not use them yet; a full UID listing works with
every server.

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

A sync ends as `ok`, `partial` (some folders failed, or could not be
reconciled; the others are archived) or `failed` (for example a wrong
password).

Below the status, the card shows how many messages are no longer on the
server (a link to them) and when the account was last compared with the
server, with what the last run found.

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
