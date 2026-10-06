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

## Logs

```sh
docker compose logs web --tail 100
```

Set `MAIL_ARCHIVE_LOG_LEVEL=debug` in `.env` for more detail.
