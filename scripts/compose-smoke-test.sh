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
		docker logs imap-test >&2 || true
	fi
	docker rm -f imap-test >/dev/null 2>&1 || true
	in_data 'rm -rf /data/* /data/.[!.]*' >/dev/null 2>&1 || true
	docker compose --progress quiet down -v >/dev/null 2>&1 || true
	rm -f .env
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

# Test IMAP server, run as a container on the Compose network.
CGO_ENABLED=0 go build -o "$work/imap-testserver" ./tools/imap-testserver

mkdir -p "$work/data"
# The app container runs as UID 65532; on Docker Desktop this is a no-op.
chmod 0777 "$work/data"
cat > .env <<ENV
POSTGRES_PASSWORD=smoke-test
MAIL_ARCHIVE_SECRET_KEY=$(openssl rand -base64 32)
ARCHIVE_DIR=$work/data
POSTGRES_PORT=${SMOKE_POSTGRES_PORT:-55432}
ENV

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

docker run -d --rm --name imap-test --network mail-archive_default \
	-v "$work/imap-testserver:/imap-testserver:ro" \
	gcr.io/distroless/static-debian12:nonroot /imap-testserver >/dev/null

echo "== account add"
echo secret | ./ma account add test --host imap-test --port 1143 --tls none \
	--username alice --exclude Trash | tee "$work/out"
expect "$work/out" 'account "test" added'

echo "== sync"
./ma sync | tee "$work/out"
expect "$work/out" "test +fetched=3 +new=3 +ok"

echo "== sync again"
./ma sync | tee "$work/out"
expect "$work/out" "test +fetched=0 +new=0 +ok"

echo "== status"
./ma status | tee "$work/out"
expect "$work/out" "unique messages in archive: 3"

count=$(in_data "find /data/messages -name '*.eml' | wc -l" | tr -d ' \r')
if [ "$count" -ne 3 ]; then
	echo "expected 3 .eml files, found $count" >&2
	exit 1
fi

echo "smoke test passed"
