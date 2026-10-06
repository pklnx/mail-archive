#!/bin/sh
# ma: run mail-archive commands through Docker Compose.
#
#   ./ma migrate
#   ./ma account add private --host imap.mail.de --username me@mail.de
#   ./ma sync
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

# Allocate a TTY only when attached to a terminal (not in cron or pipes),
# so the hidden password prompt works interactively and stdin works otherwise.
tty_flag=-T
if [ -t 0 ] && [ -t 1 ]; then
	tty_flag=
fi

exec docker compose --progress quiet run --rm --build $tty_flag \
	-e MAIL_ARCHIVE_COMMAND=./ma mail-archive "$@"
