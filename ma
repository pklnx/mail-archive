#!/bin/sh
# ma: run mail-archive commands through Docker Compose.
#
#   ./ma migrate
#   ./ma account add private --host imap.mail.de --username me@mail.de
#   ./ma sync
#   ./ma backup ./backups
#
# Starts PostgreSQL if needed, rebuilds the image when the code changed (fast
# when cached) and hides Docker Compose's progress output.
set -eu

cd "$(dirname "$0")"

# The version shown by `mail-archive --version`, baked into the image.
MAIL_ARCHIVE_VERSION=${MAIL_ARCHIVE_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
export MAIL_ARCHIVE_VERSION

compose() {
	docker compose --progress quiet "$@"
}

compose up -d --wait postgres

# backup runs pg_dump in the database container, so the dump always matches
# the server version and lands on the host; the image has no pg_dump.
if [ "${1:-}" = backup ]; then
	if [ $# -ne 2 ] || [ -z "$2" ]; then
		echo "usage: ./ma backup DIR" >&2
		exit 2
	fi
	dir=$2
	umask 077
	mkdir -p "$dir"
	name=mailarchive-$(date -u +%Y%m%dT%H%M%SZ).dump
	if ! compose exec -T postgres pg_dump --format=custom -U mailarchive -d mailarchive >"$dir/$name.partial"; then
		rm -f "$dir/$name.partial"
		echo "error: pg_dump failed" >&2
		exit 1
	fi
	mv "$dir/$name.partial" "$dir/$name"
	if ! compose run --rm --build -T -e MAIL_ARCHIVE_COMMAND=./ma mail-archive \
		backup --note-only "$name" >"$dir/BACKUP-NOTE.txt.partial"; then
		rm -f "$dir/BACKUP-NOTE.txt.partial"
		echo "error: could not write BACKUP-NOTE.txt; the dump $dir/$name is complete" >&2
		exit 1
	fi
	mv "$dir/BACKUP-NOTE.txt.partial" "$dir/BACKUP-NOTE.txt"
	echo "wrote $dir/$name and $dir/BACKUP-NOTE.txt"
	echo "now copy the data directory (ARCHIVE_DIR in .env, default ./data), and keep MAIL_ARCHIVE_SECRET_KEY separately"
	exit 0
fi

# Allocate a TTY only when attached to a terminal (not in cron or pipes),
# so the hidden password prompt works interactively and stdin works otherwise.
tty_flag=-T
if [ -t 0 ] && [ -t 1 ]; then
	tty_flag=
fi

exec docker compose --progress quiet run --rm --build $tty_flag \
	-e MAIL_ARCHIVE_COMMAND=./ma mail-archive "$@"
