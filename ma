#!/bin/sh
# ma: run mail-archive commands through Docker Compose.
#
#   ./ma migrate
#   ./ma account add private --host imap.mail.de --username me@mail.de
#   ./ma sync
#   ./ma backup ./backups
#   ./ma restore-dump ./backups/mailarchive-<time>.dump.enc
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
# the server version and lands on the host; the image has no pg_dump. With
# encryption at rest on, pg_dump's output is piped through seal-stream into
# DIR/<name>.dump.enc, next to the keyring copy DIR/<name>.keyring: the
# plaintext dump only passes through the pipe.
if [ "${1:-}" = backup ]; then
	if [ $# -ne 2 ] || [ -z "$2" ]; then
		echo "usage: ./ma backup DIR" >&2
		exit 2
	fi
	dir=$2
	umask 077
	mkdir -p "$dir"
	base=mailarchive-$(date -u +%Y%m%dT%H%M%SZ)
	name=$base.dump
	# The note first: it also writes an attestation, which the dump then holds.
	if ! compose run --rm $build -T -e MAIL_ARCHIVE_COMMAND=./ma -e MAIL_ARCHIVE_OPERATOR="$operator" mail-archive \
		backup --note-only "$name" >"$dir/BACKUP-NOTE.txt.partial"; then
		rm -f "$dir/BACKUP-NOTE.txt.partial"
		echo "error: could not write BACKUP-NOTE.txt; no dump was written" >&2
		exit 1
	fi
	# The note names the dump .dump.enc when encryption at rest is on.
	if grep -qxF "Database dump: $name.enc" "$dir/BACKUP-NOTE.txt.partial"; then
		ring=$dir/$base.keyring
		if ! compose run --rm -T mail-archive backup --keyring-copy >"$ring.partial"; then
			rm -f "$ring.partial" "$dir/BACKUP-NOTE.txt.partial"
			echo "error: could not copy the keyring; no dump was written" >&2
			exit 1
		fi
		# pg_dump's exit status, which the pipe would hide: seal-stream
		# cannot tell the end of a failed dump from the end of a good one.
		failed=$dir/$name.failed
		rm -f "$failed"
		MAIL_ARCHIVE_KEYRING_COPY=$(base64 <"$ring.partial" | tr -d '\n')
		export MAIL_ARCHIVE_KEYRING_COPY
		if ! { compose exec -T postgres pg_dump --format=custom -U mailarchive -d mailarchive || echo pg_dump >"$failed"; } |
			compose run --rm -T -e MAIL_ARCHIVE_KEYRING_COPY mail-archive seal-stream >"$dir/$name.enc.partial" ||
			[ -e "$failed" ]; then
			rm -f "$failed" "$dir/$name.enc.partial" "$ring.partial" "$dir/BACKUP-NOTE.txt.partial"
			echo "error: pg_dump or its encryption failed; no dump was written" >&2
			exit 1
		fi
		unset MAIL_ARCHIVE_KEYRING_COPY
		mv "$ring.partial" "$ring"
		mv "$dir/$name.enc.partial" "$dir/$name.enc"
		mv "$dir/BACKUP-NOTE.txt.partial" "$dir/BACKUP-NOTE.txt"
		echo "wrote $dir/$name.enc (encrypted), $ring and $dir/BACKUP-NOTE.txt"
		echo "now copy the data directory (ARCHIVE_DIR in .env, default ./data), and keep MAIL_ARCHIVE_SECRET_KEY and the archive key separately"
		exit 0
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

# restore-dump loads a dump written by backup into the database, which must
# be empty, in one transaction, with pg_restore in the database container.
# An encrypted dump (MA-ENC1 header) is decrypted by unseal-stream on its way
# there, with the keyring copy next to it. The whole time, a container with
# `restore-dump --hold` holds the writers lock, so that no web server, worker
# or cron sync starts: it checks the database and the keyring copy, says
# "ready", waits while pg_restore runs, and when told "restored" reconciles
# the keyring and the state of encryption at rest under the same lock and
# prints what it changed.
if [ "${1:-}" = restore-dump ]; then
	if [ $# -ne 2 ] || [ -z "$2" ]; then
		echo "usage: ./ma restore-dump FILE" >&2
		exit 2
	fi
	file=$2
	if [ ! -f "$file" ]; then
		echo "error: $file: no such file" >&2
		exit 2
	fi
	sealed=
	MAIL_ARCHIVE_KEYRING_COPY=
	if [ "$(head -c 7 "$file")" = MA-ENC1 ]; then
		sealed=1
		ring=${file%.enc}
		ring=${ring%.dump}.keyring
		if [ -f "$ring" ]; then
			MAIL_ARCHIVE_KEYRING_COPY=$(base64 <"$ring" | tr -d '\n')
		fi
	fi
	export MAIL_ARCHIVE_KEYRING_COPY
	fifos=$(mktemp -d)
	mkfifo "$fifos/to-holder" "$fifos/from-holder"
	compose run --rm $build -T -e MAIL_ARCHIVE_COMMAND=./ma -e MAIL_ARCHIVE_KEYRING_COPY mail-archive \
		restore-dump --hold <"$fifos/to-holder" >"$fifos/from-holder" &
	holder=$!
	# Closing fd 3 without "restored" makes the holder give up (also when
	# this script ends early); fd 4 carries its output.
	exec 3>"$fifos/to-holder" 4<"$fifos/from-holder"
	rm -rf "$fifos"
	finish() { # finish <status>: let the holder end, then exit
		exec 3>&-
		cat <&4
		wait "$holder" || [ "$1" -ne 0 ] || exit 1
		exit "$1"
	}
	if ! IFS= read -r line <&4 || [ "$line" != ready ]; then
		# The holder refused (writers running, database not empty, another
		# archive's keyring) and said why on stderr.
		finish 1
	fi
	restore() {
		compose exec -T postgres pg_restore --no-owner --single-transaction --exit-on-error \
			-U mailarchive -d mailarchive
	}
	if [ -n "$sealed" ]; then
		# unseal-stream stops before the end of its output on a damaged
		# dump, so pg_restore fails and its transaction rolls back.
		failed=$(mktemp)
		if ! { compose run --rm -T -e MAIL_ARCHIVE_KEYRING_COPY mail-archive unseal-stream <"$file" || echo unseal >"$failed"; } | restore ||
			[ -s "$failed" ]; then
			rm -f "$failed"
			echo "error: the restore failed; the database is unchanged (pg_restore ran in one transaction)" >&2
			finish 1
		fi
		rm -f "$failed"
	elif ! restore <"$file"; then
		echo "error: the restore failed; the database is unchanged (pg_restore ran in one transaction)" >&2
		finish 1
	fi
	echo "restored $file"
	echo restored >&3
	finish 0
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
