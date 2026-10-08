# Backups and upgrades

## Backups

A complete backup has three parts, made in this order:

1. **The database:**
   ```sh
   ./ma backup ./backups
   ```
   This writes `backups/mailarchive-<time>.dump` (made with `pg_dump` inside
   the database container, so it always matches the server version) and
   `backups/BACKUP-NOTE.txt` with the restore steps. Without Docker Compose,
   `mail-archive backup DIR` does the same with the `pg_dump` in `PATH`.
2. **The archive directory** (`data/`, or `ARCHIVE_DIR`): the `.eml` files.
   Copy it **after** the dump. Files are only added, so a copy made later
   covers every message in the dump.
3. **The secret key** from `.env` (`MAIL_ARCHIVE_SECRET_KEY`), kept
   separately, for example in a password manager. It is not in the dump or
   the note. Without it, archived mail stays readable, but every IMAP
   password and every 2FA setup has to be entered again.

The `.eml` files are **not** encrypted on disk, and neither are the dumps.
Keep both on an encrypted volume; FileVault on macOS does this by default.

The files alone do not record where each message was found or which
accounts exist, so back up both the files and the database.

A nightly backup with cron, which also deletes dumps older than 30 days and
checks the archive once a week:

```sh
30 2 * * * cd /path/to/mail-archive && ./ma backup ./backups && rsync -a --delete ./data/ /backup/mail-archive-data/ && find ./backups -name 'mailarchive-*.dump' -mtime +30 -delete
0 4 * * 0 cd /path/to/mail-archive && ./ma verify > /tmp/mail-archive-verify.log 2>&1 || echo "mail-archive verify: see /tmp/mail-archive-verify.log"
```

On Linux the files in `data/` belong to UID 65532 (the container user), so
copy them as root or with `sudo`.

### Checking the archive

`./ma verify` hashes every archived file again and looks for files without a
database row. Exit code `0` means all is well; see the
[command reference](../reference/cli#verify) for the findings.

- **`missing` or `corrupt`:** the listed messages are damaged. Restore their
  files from a backup of the data directory; the path is the same there. A
  sync does not fetch them again, because it only fetches messages newer than
  the last one it archived. Without a backup, the message's details and
  search text stay, but it cannot be opened or exported.
- **`orphan` or `stale-temp`:** usually left by an interrupted sync. A later
  sync adopts orphans that are still on the server.
  `./ma verify --fix-orphans` moves the rest to `data/orphans/<time>/`;
  delete that directory once you are sure nothing in it is needed.

### Restore

1. Put `MAIL_ARCHIVE_SECRET_KEY` back into `.env` and copy the data directory
   back to `data/` (or `ARCHIVE_DIR`).
2. Load the dump into the database, which replaces what is there:
   ```sh
   docker compose up -d --wait postgres
   docker compose exec -T postgres pg_restore --clean --if-exists --no-owner \
     -U mailarchive -d mailarchive < backups/mailarchive-<time>.dump
   ```
3. `./ma migrate`, then `./ma sync`, then `./ma verify`.

Files newer than the dump show up as orphans. The sync adopts those still on
the server. Others came from mail deleted on the server since the dump: they
are the only copy, so look at them before `--fix-orphans` moves them aside.

## Exporting mail

`export` writes archived mail in a format mail clients import:

```sh
./ma export --user anna --format mbox --out /data/export/anna
```

With Docker Compose the output directory must be under `/data`; the example
writes to `./data/export/anna` on the host. On Linux the files belong to
UID 65532: `sudo chown -R "$USER" data/export/anna` makes them yours.

| Format | Result | Import |
|---|---|---|
| `mbox` | One `.mbox` file per folder: `anna/<account>/<folder>.mbox` | Thunderbird: add-on *ImportExportTools NG*, then *Import mbox file*. Apple Mail: *File, Import Mailboxes*, *Files in mbox format*. |
| `maildir` | One Maildir per folder with read, answered and flagged marks | Mail servers (Dovecot) and clients such as mutt or NeoMutt. |

Exports are **not encrypted**; the files are readable only by their owner.
Delete them once imported. `verify` ignores the `export/` directory.

## Upgrades

```sh
git pull
./ma migrate              # rebuild the image, apply new database migrations
docker compose up -d web  # restart the web server with the new image
./ma --version            # the version you are running
```

`./ma migrate` rebuilds the image for every service, so the web server and
a cron job that uses `docker compose run` both run the new version afterwards.

Pull requests that change the database schema carry the label
`db-migration`. Back up the database before you upgrade past one of them.

`./ma migrate status` lists applied and pending migrations.

### One-time steps

| When upgrading from | Run |
|---|---|
| A version without full-text search | `./ma reindex` once, so older messages become searchable. |
| A version without recipient and attachment search | `./ma reindex` once, so `to:`, `attachment:` and `has:attachment` find older messages. The migration itself is quick. Reindex reads every stored message file and updates every message row, so it takes a while on large archives and logs its progress; it can run while the web server and syncs run, and can be interrupted and run again. The table needs about twice its space until autovacuum has cleaned up; on large archives, `docker compose exec postgres psql -U mailarchive -c "VACUUM (ANALYZE) messages"` afterwards makes the space reusable right away. `./ma migrate` and the web server point out when messages are still pending. |
| A version without login | `./ma user add NAME --admin` once; until then the web UI only shows how to do it. This first user gets all existing accounts. |
| A version without separate users | Nothing. Existing accounts belong to the oldest admin; hand some to other users with `./ma account move NAME --to USER`. `./ma migrate` also encrypts the stored IMAP passwords again, bound to the account ID instead of the name (needs `MAIL_ARCHIVE_SECRET_KEY`; the web server does the same when it starts). Older versions cannot read these passwords: going back means entering them again with `account set-password`. |
| A version without 2FA | Nothing to run. Every admin must set up TOTP at the next web login and needs an authenticator app for it. Recovery codes and TOTP secrets depend on `MAIL_ARCHIVE_SECRET_KEY`: after changing the key nobody with 2FA can log in until `./ma user reset-2fa NAME` resets it. |
| A version without passkeys | Nothing to run. To use passkeys, set `MAIL_ARCHIVE_PUBLIC_URL` to the address you open in the browser (Compose defaults to `http://localhost:WEB_PORT`). Passkeys are bound to its host name: after changing it, remove the passkeys (`./ma user remove-passkeys NAME`) and register them again. |

## Logs

```sh
docker compose logs web --tail 100
```

Set `MAIL_ARCHIVE_LOG_LEVEL=debug` in `.env` for more detail.
