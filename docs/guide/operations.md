# Backups and upgrades

## Backups

Back up three things:

1. **The archive directory** (`data/`, or `ARCHIVE_DIR`): the `.eml` files.
2. **The database:**
   ```sh
   docker compose exec postgres pg_dump -U mailarchive mailarchive > mailarchive.sql
   ```
3. **The secret key** from `.env` (`MAIL_ARCHIVE_SECRET_KEY`). Without it,
   archived mail stays readable, but every IMAP password has to be entered
   again.

The `.eml` files are **not** encrypted on disk. Keep the archive on an
encrypted volume; FileVault on macOS does this by default.

The files alone do not record where each message was found or which
accounts exist, so back up both the files and the database.

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
| A version without login | `./ma user add NAME --admin` once; until then the web UI only shows how to do it. This first user gets all existing accounts. |
| A version without separate users | Nothing. Existing accounts belong to the oldest admin; hand some to other users with `./ma account move NAME --to USER`. `./ma migrate` also encrypts the stored IMAP passwords again, bound to the account ID instead of the name (needs `MAIL_ARCHIVE_SECRET_KEY`; the web server does the same when it starts). Older versions cannot read these passwords: going back means entering them again with `account set-password`. |
| A version without 2FA | Nothing to run. Every admin must set up TOTP at the next web login and needs an authenticator app for it. Recovery codes and TOTP secrets depend on `MAIL_ARCHIVE_SECRET_KEY`: after changing the key nobody with 2FA can log in until `./ma user reset-2fa NAME` resets it. |

## Logs

```sh
docker compose logs web --tail 100
```

Set `MAIL_ARCHIVE_LOG_LEVEL=debug` in `.env` for more detail.
