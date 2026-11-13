#!/bin/sh
# Checks how docker-compose.yml resolves the variables of .env under their
# new names (KEEPIA_ARCHIVE_*) and their old names (MAIL_ARCHIVE_*, until
# 1.6; #585), with `docker compose config` only: no image, no container.
#
# - Every service passes each variable an operator sets in .env under its
#   new name, falling back to the old one, and under its old name raw, so
#   that the app both works and warns (and refuses two different values):
#   with only old names both carry the old value, with only new names the
#   old one is empty. Names that do not come from .env (the database URL,
#   the job workers, keygen's key file) have the new name only.
# - IMAGE, VERSION and PUBLIC_URL (three levels deep) under both names and
#   their defaults.
# - The project is mail-archive without COMPOSE_PROJECT_NAME, so that an
#   upgrade keeps its volume.
#
# Usage: scripts/compose-env-test.sh   (needs docker compose and jq)
# COMPOSE overrides the command, for example to run another Compose
# version: COMPOSE="/path/to/docker-compose" scripts/compose-env-test.sh
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
compose=${COMPOSE:-docker compose}
file=$repo/docker-compose.yml

# The variables of this process must not take part.
for name in $(env | awk -F= '/^(KEEPIA_ARCHIVE|MAIL_ARCHIVE|COMPOSE)_[A-Za-z0-9_]*=/ { print $1 }'); do
	unset "$name"
done
unset WEB_PORT 2>/dev/null || true

work=$(mktemp -d "${TMPDIR:-/tmp}/compose-env.XXXXXX")
trap 'rm -rf "$work"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# config <env file>: the resolved configuration of every service as JSON.
config() {
	# shellcheck disable=SC2086 # $compose is a command with arguments
	$compose --env-file "$1" -f "$file" --profile cli --profile worker config --format json > "$work/config.json" ||
		fail "docker compose config failed with $1"
}

# get <jq filter>: one value of the last config.
get() { jq -r "$1" "$work/config.json"; }

expect() { # expect <jq filter> <value>
	got=$(get "$1")
	[ "$got" = "$2" ] || fail "$1 is '$got', want '$2'"
}

# The suffixes of the variables an operator sets in .env: those Compose
# passes under their old name too.
suffixes=$(sed -n 's/^ *MAIL_ARCHIVE_\([A-Z0-9_]*\): \${MAIL_ARCHIVE_\1:-}$/\1/p' "$file" | sort -u)
count=$(printf '%s\n' "$suffixes" | wc -l | tr -d ' ')
# 43 of the 49 names the app reads: not DATABASE_URL and JOB_WORKERS
# (Compose sets them), DATA_DIR (the image's default), COMMAND, OPERATOR
# and KEYRING_COPY (./ma sets them).
[ "$count" -eq 43 ] || fail "$count variables with an old name in $file, want 43"

# The names a service may have under the new name only.
new_only='KEEPIA_ARCHIVE_DATABASE_URL KEEPIA_ARCHIVE_JOB_WORKERS'

# pairs <env file> <old value prefix or empty> <new value prefix or empty>:
# in every service, each KEEPIA_ARCHIVE_X from .env has its MAIL_ARCHIVE_X
# and the reverse; the values are as given (empty prefix: the variable is
# empty or, under the new name, its default).
pairs() {
	config "$1"
	for service in $(get '.services | keys[]'); do
		env_json=$(jq -c --arg s "$service" '.services[$s].environment // {}' "$work/config.json")
		for key in $(printf '%s' "$env_json" | jq -r 'keys[] | select(startswith("KEEPIA_ARCHIVE_") or startswith("MAIL_ARCHIVE_"))'); do
			suffix=${key#KEEPIA_ARCHIVE_}
			suffix=${suffix#MAIL_ARCHIVE_}
			case " $new_only " in
			*" $key "*) continue ;;
			esac
			if [ "$service" = mail-archive-keygen ] && [ "$key" = KEEPIA_ARCHIVE_ARCHIVE_KEY_FILE ]; then
				continue
			fi
			old=$(printf '%s' "$env_json" | jq -r --arg k "MAIL_ARCHIVE_$suffix" '.[$k] // "<missing>"')
			new=$(printf '%s' "$env_json" | jq -r --arg k "KEEPIA_ARCHIVE_$suffix" '.[$k] // "<missing>"')
			[ "$old" != "<missing>" ] || fail "$service: KEEPIA_ARCHIVE_$suffix without MAIL_ARCHIVE_$suffix ($1)"
			[ "$new" != "<missing>" ] || fail "$service: MAIL_ARCHIVE_$suffix without KEEPIA_ARCHIVE_$suffix ($1)"
			if [ -n "$2" ]; then
				[ "$old" = "$2$suffix" ] || fail "$service: MAIL_ARCHIVE_$suffix is '$old', want '$2$suffix' ($1)"
			else
				[ -z "$old" ] || fail "$service: MAIL_ARCHIVE_$suffix is '$old', want empty ($1)"
			fi
			if [ -n "$3" ]; then
				[ "$new" = "$3$suffix" ] || fail "$service: KEEPIA_ARCHIVE_$suffix is '$new', want '$3$suffix' ($1)"
			fi
		done
	done
}

# envfile <file> <prefix> <value prefix>: .env with every variable of
# $suffixes under <prefix>, each set to <value prefix><suffix>.
envfile() {
	echo POSTGRES_PASSWORD=pw > "$1"
	for suffix in $suffixes; do
		echo "$2$suffix=$3$suffix" >> "$1"
	done
}

echo "== project name"
echo POSTGRES_PASSWORD=pw > "$work/none.env"
config "$work/none.env"
expect '.name' mail-archive

echo "== neither name: defaults"
expect '.services.web.environment.KEEPIA_ARCHIVE_SYNC_INTERVAL' 6h
expect '.services.web.environment.MAIL_ARCHIVE_SYNC_INTERVAL' ''
expect '.services.web.environment.KEEPIA_ARCHIVE_PUBLIC_URL' 'http://localhost:8080'
expect '.services.web.image' 'mail-archive:local'
expect '.services.web.build.args.VERSION' dev
expect '.services.web.environment.KEEPIA_ARCHIVE_JOB_WORKERS' 1
expect '.services.worker.environment.KEEPIA_ARCHIVE_JOB_WORKERS' 1
expect '.services."mail-archive-keygen".environment.KEEPIA_ARCHIVE_ARCHIVE_KEY_FILE' /keys/archive.key
expect '.services.web.environment.KEEPIA_ARCHIVE_DATABASE_URL' 'postgres://mailarchive:pw@postgres:5432/mailarchive?sslmode=disable'
# The third level of PUBLIC_URL.
printf 'POSTGRES_PASSWORD=pw\nWEB_PORT=9090\n' > "$work/port.env"
config "$work/port.env"
expect '.services.web.environment.KEEPIA_ARCHIVE_PUBLIC_URL' 'http://localhost:9090'
pairs "$work/none.env" '' ''

echo "== old names only (a 1.6 .env)"
envfile "$work/old.env" MAIL_ARCHIVE_ old-
printf 'MAIL_ARCHIVE_IMAGE=img:old\nMAIL_ARCHIVE_VERSION=1.6.0\n' >> "$work/old.env"
pairs "$work/old.env" old- old-
for service in web worker mail-archive mail-archive-keygen mail-archive-keys; do
	expect ".services.\"$service\".image" img:old
done
expect '.services.web.build.args.VERSION' 1.6.0
expect '.services.web.environment.KEEPIA_ARCHIVE_PUBLIC_URL' old-PUBLIC_URL
expect '.services.web.environment.MAIL_ARCHIVE_PUBLIC_URL' old-PUBLIC_URL
expect '.name' mail-archive

echo "== new names only"
envfile "$work/new.env" KEEPIA_ARCHIVE_ new-
printf 'KEEPIA_ARCHIVE_IMAGE=img:new\nKEEPIA_ARCHIVE_VERSION=1.7.0\n' >> "$work/new.env"
pairs "$work/new.env" '' new-
for service in web worker mail-archive mail-archive-keygen mail-archive-keys; do
	expect ".services.\"$service\".image" img:new
done
expect '.services.web.build.args.VERSION' 1.7.0
expect '.services.web.environment.KEEPIA_ARCHIVE_PUBLIC_URL' new-PUBLIC_URL

echo "== both names, different values"
# Compose passes both; the app refuses to start (compose-smoke-test.sh).
envfile "$work/both.env" MAIL_ARCHIVE_ old-
envfile "$work/both-new.env" KEEPIA_ARCHIVE_ new-
grep -v '^POSTGRES_PASSWORD=' "$work/both-new.env" >> "$work/both.env"
printf 'MAIL_ARCHIVE_IMAGE=img:old\nKEEPIA_ARCHIVE_IMAGE=img:new\nMAIL_ARCHIVE_VERSION=1.6.0\nKEEPIA_ARCHIVE_VERSION=1.7.0\n' >> "$work/both.env"
pairs "$work/both.env" old- new-
expect '.services.web.image' img:new
expect '.services.web.build.args.VERSION' 1.7.0

echo "== the worker gets no secret key"
config "$work/old.env"
get '.services.worker.environment | keys[]' > "$work/keys"
if grep -Eq '^(KEEPIA|MAIL)_ARCHIVE_SECRET_KEY' "$work/keys"; then
	fail "the worker gets the secret key: $(grep -E '^(KEEPIA|MAIL)_ARCHIVE_SECRET_KEY' "$work/keys" | tr '\n' ' ')"
fi

echo "compose env test passed"
