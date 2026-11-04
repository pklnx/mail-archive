#!/bin/sh
# ma: run mail-archive commands through Docker Compose.
#
#   ./ma migrate
#   ./ma account add private --host imap.mail.de --username me@mail.de
#   ./ma sync
#   ./ma backup ./backups
#
# Starts PostgreSQL if needed, rebuilds the image when the code changed (fast
# when cached) and hides Docker Compose's progress output. In a release
# bundle, which has no source, it uses the released image instead. `migrate`
# also makes a fresh data directory writable for the container user.
set -eu

cd "$(dirname "$0")"

build=
if [ -f Dockerfile ]; then
	build=--build
	# The version shown by `mail-archive --version`, baked into the image.
	MAIL_ARCHIVE_VERSION=${MAIL_ARCHIVE_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
	export MAIL_ARCHIVE_VERSION
fi

compose() {
	docker compose --progress quiet "$@"
}

# encryption keygen runs in a container removed afterwards: a key written
# anywhere but the keys directory (/keys) would be lost with it.
if [ "${1:-}" = encryption ] && [ "${2:-}" = keygen ]; then
	prev=
	for arg in "$@"; do
		case $prev in
		--key-file) value=$arg ;;
		*) value= ;;
		esac
		case $arg in
		--key-file=*) value=${arg#--key-file=} ;;
		esac
		case $value in
		*..*) value=bad ;;
		esac
		case $value in
		'' | /keys/?*) ;;
		*)
			echo "error: --key-file must lie in /keys (ARCHIVE_KEY_DIR on the host); elsewhere the key is lost with the container" >&2
			exit 2
			;;
		esac
		prev=$arg
	done
fi

compose up -d --wait postgres

# The first start: Docker creates a missing data directory as root, but the
# container runs as UID 65532. Make it writable (no-op when it already is).
# Only warns, so the schema upgrade below always runs.
if [ "${1:-}" = migrate ]; then
	compose run --rm --no-deps $build -T --user 0:0 mail-archive prepare-data ||
		echo "warning: could not prepare the data directory (ARCHIVE_DIR in .env, default ./data)" >&2
fi

# export writes GoBD exports to /export (EXPORT_DIR, default ./export),
# which Docker creates as root on first use: make it writable for the
# container user as for the data directory. Only warns; the export then
# reports what it cannot write.
if [ "${1:-}" = export ] && [ "${2:-}" != verify ]; then
	compose run --rm --no-deps $build -T --user 0:0 -e MAIL_ARCHIVE_DATA_DIR=/export mail-archive prepare-data >/dev/null 2>&1 ||
		echo "warning: could not prepare the export directory (EXPORT_DIR in .env, default ./export)" >&2
fi

# Who runs the command, for the audit log (not verified).
operator=$(id -un 2>/dev/null || echo unknown)

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
	# The note first: it also writes an attestation, which the dump then holds.
	if ! compose run --rm $build -T -e MAIL_ARCHIVE_COMMAND=./ma -e MAIL_ARCHIVE_OPERATOR="$operator" mail-archive \
		backup --note-only "$name" >"$dir/BACKUP-NOTE.txt.partial"; then
		rm -f "$dir/BACKUP-NOTE.txt.partial"
		echo "error: could not write BACKUP-NOTE.txt; no dump was written" >&2
		exit 1
	fi
	if ! compose exec -T postgres pg_dump --format=custom -U mailarchive -d mailarchive >"$dir/$name.partial"; then
		rm -f "$dir/$name.partial" "$dir/BACKUP-NOTE.txt.partial"
		echo "error: pg_dump failed" >&2
		exit 1
	fi
	mv "$dir/$name.partial" "$dir/$name"
	mv "$dir/BACKUP-NOTE.txt.partial" "$dir/BACKUP-NOTE.txt"
	echo "wrote $dir/$name and $dir/BACKUP-NOTE.txt"
	echo "now copy the data directory (ARCHIVE_DIR in .env, default ./data), and keep MAIL_ARCHIVE_SECRET_KEY separately"
	exit 0
fi

# encryption keygen writes the archive key into the keys directory
# (ARCHIVE_KEY_DIR, default ./keys), which every other service mounts
# read-only: a one-off container with it writable, run as root so that it
# can write there and give the files to the container user. The data
# directory is prepared first, as by migrate, so that the keyring in it does
# not leave a fresh directory owned by root.
if [ "${1:-}" = encryption ] && [ "${2:-}" = keygen ]; then
	shift 2
	compose run --rm --no-deps $build -T --user 0:0 mail-archive prepare-data ||
		echo "warning: could not prepare the data directory (ARCHIVE_DIR in .env, default ./data)" >&2
	exec docker compose --progress quiet run --rm --no-deps $build -T --user 0:0 \
		-e MAIL_ARCHIVE_COMMAND=./ma mail-archive-keygen encryption keygen --chown 65532:65532 "$@"
fi

# Allocate a TTY only when attached to a terminal (not in cron or pipes),
# so the hidden password prompt works interactively and stdin works otherwise.
tty_flag=-T
if [ -t 0 ] && [ -t 1 ]; then
	tty_flag=
fi

exec docker compose --progress quiet run --rm $build $tty_flag \
	-e MAIL_ARCHIVE_COMMAND=./ma -e MAIL_ARCHIVE_OPERATOR="$operator" mail-archive "$@"
