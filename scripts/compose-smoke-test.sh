#!/bin/sh
# End-to-end smoke test of the Docker Compose setup and the ./ma wrapper:
# starts PostgreSQL, migrates, archives a test IMAP account twice and checks
# the results. Uses a throwaway .env and data directory and cleans up after.
#
# Usage: scripts/compose-smoke-test.sh   (from anywhere; needs Docker and Go)
#
# With SMOKE_DIR set to an extracted release bundle (scripts/release-bundle.sh),
# it tests the bundle instead: its docker-compose.yml and ./ma, with the image
# in MAIL_ARCHIVE_IMAGE, which must exist locally (the bundle builds nothing).
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "${SMOKE_DIR:-$repo}"

if [ -e .env ]; then
	echo "refusing to run: .env exists and would be overwritten" >&2
	exit 1
fi

work=$(mktemp -d)

# Own Compose project, image tag, test container and random host ports, so
# the test does not touch a local mail-archive installation.
COMPOSE_PROJECT_NAME=mail-archive-smoke
MAIL_ARCHIVE_IMAGE=${MAIL_ARCHIVE_IMAGE:-mail-archive:smoke}
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
		docker compose --profile worker logs worker >&2 || true
	fi
	docker rm -f "$imap" >/dev/null 2>&1 || true
	in_data 'rm -rf /data/* /data/.[!.]*' >/dev/null 2>&1 || true
	docker compose --progress quiet run --rm --no-deps -T --entrypoint sh -v "$work/keys:/keys" postgres \
		-c 'rm -rf /keys/* /keys/.[!.]*; chmod 0777 /keys' >/dev/null 2>&1 || true
	docker compose --progress quiet --profile worker down -v >/dev/null 2>&1 || true
	rm -f .env
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

# Test IMAP server, run as a container on the Compose network.
(cd "$repo" && CGO_ENABLED=0 go build -o "$work/imap-testserver" ./tools/imap-testserver)

# No data directory: the first ./ma migrate creates it, as on a real first start.
# A test instance: the web server's first start finds no user yet, and
# MAIL_ARCHIVE_ENCRYPTION_AT_SETUP=off lets it start without an archive key;
# the encryption commands are tested further down.
mkdir -p "$work/import"
cp -R "$repo/internal/archive/testdata/import/thunderbird" "$work/import/"
chmod -R a+rX "$work/import"
cat > .env <<ENV
POSTGRES_PASSWORD=smoke-test
MAIL_ARCHIVE_SECRET_KEY=$(openssl rand -base64 32)
ARCHIVE_DIR=$work/data
IMPORT_DIR=$work/import
ARCHIVE_KEY_DIR=$work/keys
SECRET_KEY_DIR=$work/secret-key
POSTGRES_PORT=${SMOKE_POSTGRES_PORT:-0}
WEB_PORT=${SMOKE_WEB_PORT:-0}
MAIL_ARCHIVE_PUBLIC_URL=http://localhost
MAIL_ARCHIVE_ENCRYPTION_AT_SETUP=off
ENV

# Remove what an interrupted previous run left behind.
docker compose --profile worker down -v --remove-orphans >/dev/null 2>&1 || true
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
if [ "$(uname -s)" = Linux ]; then
	owner=$(in_data 'stat -c %u:%g /data' | tr -d '\r')
	if [ "$owner" != 65532:65532 ]; then
		echo "expected the data directory to belong to 65532:65532, got $owner" >&2
		exit 1
	fi
fi

echo "== hardening settings"
# A new database uses PostgreSQL's builtin locale (POSTGRES_INITDB_ARGS), not
# the image's C library.
docker compose --progress quiet exec -T postgres psql -U mailarchive -d mailarchive -Atc \
	"SELECT datlocprovider::text || '|' || COALESCE(datlocale, '') FROM pg_database WHERE datname = 'mailarchive'" > "$work/out"
expect "$work/out" '^b\|C\.UTF-8$'
# The one-shot CLI container (also in the release bundle's docker-compose.yml).
docker compose --progress quiet --profile cli config mail-archive > "$work/out"
expect "$work/out" 'no-new-privileges:true'

echo "== first start, as in README.txt"
docker compose --progress quiet up -d --wait web
# The web container's settings; its ID is kept for the exit code after `stop`,
# because stopped containers are not listed by `docker compose ps -q`.
web_id=$(docker compose ps -q web)
docker inspect -f '{{.HostConfig.SecurityOpt}}' "$web_id" > "$work/out"
expect "$work/out" 'no-new-privileges:true'
docker inspect -f '{{.Config.StopTimeout}}' "$web_id" > "$work/out"
expect "$work/out" '^45$'
WEB_PORT=$(docker compose port web 8080 | sed 's/.*://')
status=$(curl -s -o /dev/null -w '%{http_code}' --retry 10 --retry-delay 1 --retry-all-errors "http://localhost:$WEB_PORT/api/status")
if [ "$status" != 401 ]; then
	echo "expected 401 without a login, got $status" >&2
	exit 1
fi
# Stop it again: its scheduler would sync the test account on its own and
# race ./ma sync below. The web API step starts it again.
docker compose --progress quiet stop web
# serve ended on SIGTERM by itself; 137 would mean Docker killed it.
docker inspect -f '{{.State.ExitCode}}' "$web_id" > "$work/out"
expect "$work/out" '^0$'
# By default the web server runs the background jobs itself.
docker compose --progress quiet logs web > "$work/out" 2>&1
expect "$work/out" 'msg="jobs on" workers=1'

echo "== worker service"
# The optional worker for background jobs, under its profile: its limits, its
# start, and a stop on SIGTERM that hands running jobs back by itself.
docker compose --progress quiet --profile worker up -d --wait worker
worker_id=$(docker compose --profile worker ps -q worker)
docker inspect -f '{{.HostConfig.Memory}} {{.HostConfig.NanoCpus}} {{.Config.StopTimeout}} {{.HostConfig.SecurityOpt}}' "$worker_id" > "$work/out"
expect "$work/out" '^805306368 1000000000 20 \[no-new-privileges:true\]$'
docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$worker_id" > "$work/out"
expect "$work/out" '^GOMEMLIMIT=576MiB$'
expect "$work/out" '^MAIL_ARCHIVE_JOB_WORKERS=1$'
# The worker seals and opens no password: it gets no secret key (#396).
if grep -q '^MAIL_ARCHIVE_SECRET_KEY' "$work/out"; then
	echo "the worker gets the secret key" >&2
	exit 1
fi
docker compose --progress quiet --profile worker logs worker > "$work/out" 2>&1
expect "$work/out" 'msg="worker on" workers=1'
docker compose --progress quiet --profile worker stop worker
docker inspect -f '{{.State.ExitCode}}' "$worker_id" > "$work/out"
expect "$work/out" '^0$'
# Off unless the profile is on: a plain `up` does not start it.
docker compose --progress quiet config --services > "$work/out"
if grep -qx worker "$work/out"; then
	echo "the worker service is on without its profile" >&2
	exit 1
fi

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
expect "$work/out" "ok: read 4, added 4 \(4 new to the account\)"
./ma import old --from /import/thunderbird --format mbox > "$work/out"
expect "$work/out" "4 already there"

echo "== verify"
./ma verify | tee "$work/out"
expect "$work/out" 'checked 7 message\(s\)( and [0-9]+ attestation\(s\))?: no problems'

echo "== attest"
./ma attest keygen | tee "$work/out"
expect "$work/out" 'public key [A-Za-z0-9+/]{43}='
./ma attest | tee "$work/out"
expect "$work/out" 'attestation #1: 7 new message\(s\), 0 deletion\(s\), 7 message\(s\) in total'
in_data 'wc -l < /data/attest/attestations.jsonl && test -s /data/attest/leaves/1.txt' > "$work/out"
expect "$work/out" '^ *1$'

echo "== encryption keygen"
# A one-off container with the keys directory writable; afterwards every
# service reads the key through its read-only mount.
# A key file outside /keys would be lost with the one-off container.
if ./ma encryption keygen --key-file /tmp/archive.key > "$work/out" 2>&1; then
	echo "expected ./ma encryption keygen to refuse a key file outside /keys" >&2
	exit 1
fi
expect "$work/out" 'must lie in /keys'
./ma encryption keygen | tee "$work/out"
expect "$work/out" '^wrote archive key /keys/archive\.key \(fingerprint [0-9a-f]{16}\)$'
expect "$work/out" '^  [A-Za-z0-9+/]{43}=$'
echo 'MAIL_ARCHIVE_ARCHIVE_KEY_FILE=/keys/archive.key' >> .env
./ma encryption status | tee "$work/out"
expect "$work/out" 'archive key: file /keys/archive\.key, fingerprint [0-9a-f]{16}: opens the keyring'
if [ "$(uname -s)" = Linux ]; then
	docker compose --progress quiet run --rm --no-deps -T --entrypoint sh -v "$work/keys:/keys" postgres \
		-c 'stat -c "%u:%g %a" /keys/archive.key' > "$work/out"
	expect "$work/out" '^65532:65532 400'
fi

echo "== encryption enable"
# Offline: the web server started for the API checks above is stopped first.
docker compose --progress quiet stop web
./ma encryption enable | tee "$work/out"
expect "$work/out" 'encryption at rest is on'
count=$(in_data "find /data/messages -name '*.eml' | wc -l" | tr -d ' \r')
if [ "$count" -ne 0 ]; then
	echo "expected no .eml files after encryption enable, found $count" >&2
	exit 1
fi
# Leak test: no subject is left in plaintext in the message files.
if in_data "grep -rqa 'Smoke test' /data/messages"; then
	echo "a message file holds a subject in plaintext" >&2
	exit 1
fi
./ma verify | tee "$work/out"
expect "$work/out" 'checked 7 message\(s\)( and [0-9]+ attestation\(s\))?: no problems'

echo "== export"
./ma export --user smoke --format mbox --out /data/export/smoke > "$work/out" 2>&1
cat "$work/out"
expect "$work/out" "exported 7 message\(s\)"
# Inside the data directory with encryption at rest on: a warning.
expect "$work/out" 'warning: the export is not encrypted'
in_data 'test -s /data/export/smoke/test/INBOX.mbox && head -c 5 /data/export/smoke/test/INBOX.mbox' > "$work/out"
expect "$work/out" '^From '
# Exports are not taken for orphans.
./ma verify > "$work/out"
expect "$work/out" 'no problems'

echo "== backup"
./ma backup "$work/backups" | tee "$work/out"
expect "$work/out" "wrote .*mailarchive-[0-9TZ]+\.dump\.enc \(encrypted\)"
dump=$(ls "$work/backups"/mailarchive-*.dump.enc)
test -s "${dump%.dump.enc}.keyring"
if [ "$(head -c 7 "$dump")" != MA-ENC1 ]; then
	echo "the dump is not encrypted" >&2
	exit 1
fi
expect "$work/backups/BACKUP-NOTE.txt" 'MAIL_ARCHIVE_SECRET_KEY'
expect "$work/backups/BACKUP-NOTE.txt" "Database dump: $(basename "$dump")"
expect "$work/backups/BACKUP-NOTE.txt" '[0-9a-f]{16}  archive key \(the configured archive key\)'
expect "$work/backups/BACKUP-NOTE.txt" 'restore-dump'
if grep -qF "$(grep MAIL_ARCHIVE_SECRET_KEY .env | cut -d= -f2-)" "$work/backups/BACKUP-NOTE.txt"; then
	echo "the backup note contains the secret key" >&2
	exit 1
fi
# Leak test on the .dump.enc. pg_dump's custom format compresses the rows,
# so two controls: the header and the schema (stored uncompressed) are bytes
# of the decrypted dump, and the subjects and the account's user name are in
# its rows (read back as SQL); none of them is in the encrypted file.
./ma unseal-stream < "$dump" > "$work/plain.dump"
docker compose --progress quiet exec -T postgres pg_restore -f - < "$work/plain.dump" > "$work/plain.sql"
for marker in PGDMP 'CREATE TABLE public.messages' 'Smoke test' 'Work message' alice; do
	if grep -qaF "$marker" "$dump"; then
		echo "the encrypted dump contains $marker in plaintext" >&2
		exit 1
	fi
done
for marker in PGDMP 'CREATE TABLE public.messages'; do
	if ! grep -qaF "$marker" "$work/plain.dump"; then
		echo "control: the decrypted dump's bytes lack $marker" >&2
		exit 1
	fi
done
for marker in 'Smoke test' 'Work message' alice; do
	if ! grep -qaF "$marker" "$work/plain.sql"; then
		echo "control: the decrypted dump's rows lack $marker" >&2
		exit 1
	fi
done
docker compose --progress quiet exec -T postgres pg_restore --list < "$work/plain.dump" > "$work/out"
expect "$work/out" 'TABLE public messages'
# plain.dump stays for the downgrade check of restore-dump below.
rm -f "$work/plain.sql"

echo "== restore-dump into a fresh database"
# A recovery key added after the backup lives in the keyring file only; the
# restore must keep it (the newer generation wins).
./ma encryption add-key | tee "$work/out"
docker compose --progress quiet exec -T postgres dropdb -U mailarchive --force mailarchive
docker compose --progress quiet exec -T postgres createdb -U mailarchive mailarchive
tables() {
	docker compose --progress quiet exec -T postgres psql -U mailarchive -d mailarchive -Atc \
		"SELECT count(*) FROM pg_tables WHERE schemaname = 'public'"
}
# A dump without encryption in place of an encrypted one is refused on this
# archive (archive key, keyring, .enc name) before pg_restore runs
# (GHSA-w6jg-ffvw-q4f5); with --plaintext it is restored, and the holder's
# note does not break the ./ma protocol.
downgrade=$work/backups/x.dump.enc
mv "$work/plain.dump" "$downgrade"
if ./ma restore-dump "$downgrade" > "$work/out" 2>&1; then
	cat "$work/out" >&2
	echo "expected ./ma restore-dump to refuse a dump without encryption named .dump.enc" >&2
	exit 1
fi
expect "$work/out" 'has no encryption header'
expect "$work/out" 'restore-dump --plaintext'
if [ "$(tables | tr -d ' \r')" != 0 ]; then
	echo "a refused dump without encryption left tables behind" >&2
	exit 1
fi
./ma restore-dump --plaintext "$downgrade" > "$work/out" 2>&1 || { cat "$work/out" >&2; exit 1; }
cat "$work/out"
expect "$work/out" 'restoring an unencrypted dump \(--plaintext\)'
expect "$work/out" "restored $downgrade"
rm -f "$downgrade"
docker compose --progress quiet exec -T postgres dropdb -U mailarchive --force mailarchive
docker compose --progress quiet exec -T postgres createdb -U mailarchive mailarchive
# A damaged dump is refused and leaves the new database empty.
cut=$work/backups/cut.dump.enc
head -c "$(($(wc -c < "$dump") - 1))" "$dump" > "$cut"
# A watchdog: a holder that never saw the end of the restore would hang
# here instead of failing.
status=0
timeout 300 ./ma restore-dump "$cut" > "$work/out" 2>&1 || status=$?
if [ "$status" -eq 124 ]; then
	cat "$work/out" >&2
	echo "./ma restore-dump of a cut dump hung" >&2
	exit 1
fi
if [ "$status" -eq 0 ]; then
	echo "expected ./ma restore-dump to refuse a cut dump" >&2
	exit 1
fi
expect "$work/out" 'the database is unchanged'
if [ "$(tables | tr -d ' \r')" != 0 ]; then
	echo "a cut dump left tables behind" >&2
	exit 1
fi
./ma restore-dump "$dump" | tee "$work/out"
expect "$work/out" "restored $dump"
expect "$work/out" 'restored database: encryption at rest on, keyring copy generation [0-9]+'
expect "$work/out" 'state: on, as in the dump'
expect "$work/out" 'keyring: the keyring file is newer than the dump.s copy'
./ma status | tee "$work/out"
expect "$work/out" "unique messages in archive: 7"
expect "$work/out" 'encryption at rest: on'
./ma verify | tee "$work/out"
expect "$work/out" 'checked 7 message\(s\)( and [0-9]+ attestation\(s\))?: no problems'
# The way back after an interrupted restore or a migrate: the copies agree now.
./ma restore-dump --reconcile-only | tee "$work/out"
expect "$work/out" 'keyring: the database copy and the keyring file agree'
# A database that is not empty is refused, before pg_restore runs.
if ./ma restore-dump "$dump" > "$work/out" 2>&1; then
	echo "expected ./ma restore-dump to refuse a database that is not empty" >&2
	exit 1
fi
expect "$work/out" 'is not empty'

echo "== audit"
# Every step above that reads, imports, exports or changes was recorded,
# with the client address and the host user from ./ma.
# A fixed early day, because every run starts on a fresh database (down -v; the
# migrate check fails on a stale schema), so the window never depends on the clock.
./ma audit --since 2000-01-01 --format jsonl > "$work/out"
for action in account.add user.add auth.login message.search data.import data.export data.backup audit.export; do
	expect "$work/out" "\"action\":\"$action\""
done
expect "$work/out" '"action":"auth.login".*"clientIp":"[0-9a-f.:]+"'
expect "$work/out" "\"operator\":\"$(id -un)\""
if grep -q '"action":"sync.start"' "$work/out"; then
	echo "a sync from the command line was recorded" >&2
	exit 1
fi

echo "smoke test passed"
