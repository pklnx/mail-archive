#!/bin/sh
# End-to-end smoke test of the Docker Compose setup and the ./ma wrapper:
# starts PostgreSQL, migrates, archives a test IMAP account twice and checks
# the results. Uses a throwaway .env and data directory and cleans up after.
#
# Usage: scripts/compose-smoke-test.sh   (from anywhere; needs Docker and Go)
set -eu

cd "$(dirname "$0")/.."

if [ -e .env ]; then
	echo "refusing to run: .env exists and would be overwritten" >&2
	exit 1
fi

work=$(mktemp -d)

# Own Compose project, image tag, test container and random host ports, so
# the test does not touch a local mail-archive installation.
COMPOSE_PROJECT_NAME=mail-archive-smoke
MAIL_ARCHIVE_IMAGE=mail-archive:smoke
export COMPOSE_PROJECT_NAME MAIL_ARCHIVE_IMAGE
imap=mail-archive-smoke-imap

# Archive files belong to the container user (UID 65532, mode 0600/0700), so
# on Linux the host user cannot read or delete them. Inspect and remove them
# from a container instead, reusing the PostgreSQL image (it has a shell).
in_data() {
	docker compose --progress quiet run --rm --no-deps -T --entrypoint sh \
		-v "$work/data:/data" postgres -c "$1"
}

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then
		echo "--- smoke test failed, logs follow ---" >&2
		docker compose logs postgres >&2 || true
		docker logs "$imap" >&2 || true
		docker compose logs web >&2 || true
	fi
	docker rm -f "$imap" >/dev/null 2>&1 || true
	in_data 'rm -rf /data/* /data/.[!.]*' >/dev/null 2>&1 || true
	docker compose --progress quiet down -v >/dev/null 2>&1 || true
	rm -f .env
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

# Test IMAP server, run as a container on the Compose network.
CGO_ENABLED=0 go build -o "$work/imap-testserver" ./tools/imap-testserver

mkdir -p "$work/data" "$work/import"
cp -R internal/archive/testdata/import/thunderbird "$work/import/"
chmod -R a+rX "$work/import"
# The app container runs as UID 65532; on Docker Desktop this is a no-op.
chmod 0777 "$work/data"
cat > .env <<ENV
POSTGRES_PASSWORD=smoke-test
MAIL_ARCHIVE_SECRET_KEY=$(openssl rand -base64 32)
ARCHIVE_DIR=$work/data
IMPORT_DIR=$work/import
POSTGRES_PORT=${SMOKE_POSTGRES_PORT:-0}
WEB_PORT=${SMOKE_WEB_PORT:-0}
MAIL_ARCHIVE_PUBLIC_URL=http://localhost
ENV

# Remove what an interrupted previous run left behind.
docker compose down -v --remove-orphans >/dev/null 2>&1 || true
docker rm -f "$imap" >/dev/null 2>&1 || true

expect() { # expect <file> <extended regex>
	if ! grep -Eq "$2" "$1"; then
		echo "expected output matching: $2" >&2
		cat "$1" >&2
		exit 1
	fi
}

echo "== migrate"
./ma migrate | tee "$work/out"
expect "$work/out" "applied 00001_initial.sql"

docker run -d --rm --name "$imap" --network "${COMPOSE_PROJECT_NAME}_default" \
	-v "$work/imap-testserver:/imap-testserver:ro" \
	gcr.io/distroless/static-debian12:nonroot /imap-testserver >/dev/null

echo "== account add"
echo secret | ./ma account add test --host "$imap" --port 1143 --tls none \
	--username alice --exclude Trash | tee "$work/out"
expect "$work/out" 'account "test" added'

echo "== sync"
./ma sync | tee "$work/out"
# The first sync also compares the new folders with the server.
expect "$work/out" "test +fetched=3 +new=3 +gone=0 +back=0 +flags=0 +ok"

echo "== sync again"
# Reconciled a moment ago: not due, so no reconcile counts.
./ma sync | tee "$work/out"
expect "$work/out" "test +fetched=0 +new=0 +ok"

echo "== status"
./ma status | tee "$work/out"
expect "$work/out" "unique messages in archive: 3"

echo "== user add"
echo 'smoke test password' | ./ma user add smoke --password-stdin | tee "$work/out"
expect "$work/out" 'user "smoke" created'

echo "== web API"
docker compose --progress quiet up -d --wait web
WEB_PORT=$(docker compose port web 8080 | sed 's/.*://')
base="http://localhost:$WEB_PORT"
api() { curl -fsS --retry 10 --retry-delay 1 --retry-all-errors -b "$work/cookies" "$base$1"; }
status=$(curl -s -o /dev/null -w '%{http_code}' --retry 10 --retry-delay 1 --retry-all-errors "$base/api/status")
if [ "$status" != 401 ]; then
	echo "expected 401 without a login, got $status" >&2
	exit 1
fi
curl -fsS -c "$work/cookies" -X POST "$base/api/session" \
	-H 'Content-Type: application/json' -H "Origin: $base" \
	-d '{"username":"smoke","password":"smoke test password"}' > "$work/out"
expect "$work/out" '"name":"smoke"'
api "/api/messages?q=Smoke" > "$work/out"
expect "$work/out" '"subject":"Smoke test 1"'
if [ "$(grep -o '"id":' "$work/out" | wc -l | tr -d ' ')" -ne 3 ]; then
	echo "expected 3 search results" >&2
	cat "$work/out" >&2
	exit 1
fi
api "/" > "$work/out"
expect "$work/out" '<div id="root">'
asset=$(grep -o '/assets/[^"]*\.js' "$work/out" | head -1)
api "$asset" > /dev/null
status=$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.example' "http://localhost:$WEB_PORT/api/status")
if [ "$status" != 403 ]; then
	echo "expected 403 for a foreign Host header, got $status" >&2
	exit 1
fi

count=$(in_data "find /data/messages -name '*.eml' | wc -l" | tr -d ' \r')
if [ "$count" -ne 3 ]; then
	echo "expected 3 .eml files, found $count" >&2
	exit 1
fi

echo "== import"
./ma import old --from /import/thunderbird --format mbox | tee "$work/out"
expect "$work/out" "ok: read 4, added 4 \(4 new to the archive\)"
./ma import old --from /import/thunderbird --format mbox > "$work/out"
expect "$work/out" "4 already there"

echo "== verify"
./ma verify | tee "$work/out"
expect "$work/out" 'checked 7 message\(s\): no problems'

echo "== export"
./ma export --user smoke --format mbox --out /data/export/smoke | tee "$work/out"
expect "$work/out" "exported 7 message\(s\)"
in_data 'test -s /data/export/smoke/test/INBOX.mbox && head -c 5 /data/export/smoke/test/INBOX.mbox' > "$work/out"
expect "$work/out" '^From '
# Exports are not taken for orphans.
./ma verify > "$work/out"
expect "$work/out" 'no problems'

echo "== backup"
./ma backup "$work/backups" | tee "$work/out"
expect "$work/out" "wrote .*mailarchive-[0-9TZ]+\.dump"
dump=$(ls "$work/backups"/mailarchive-*.dump)
docker compose --progress quiet exec -T postgres pg_restore --list < "$dump" > "$work/out"
expect "$work/out" 'TABLE public messages'
expect "$work/backups/BACKUP-NOTE.txt" 'MAIL_ARCHIVE_SECRET_KEY'
if grep -qF "$(grep MAIL_ARCHIVE_SECRET_KEY .env | cut -d= -f2-)" "$work/backups/BACKUP-NOTE.txt"; then
	echo "the backup note contains the secret key" >&2
	exit 1
fi

echo "smoke test passed"
