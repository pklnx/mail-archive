#!/bin/sh
# Regenerates the screenshots in docs/public/screenshots from the demo server.
# Needs a PostgreSQL user with CREATEDB in DEMO_DATABASE_URL (the demo creates
# and drops its own database), Go, Node.js with pnpm, and a Chromium for
# Playwright (set CHROMIUM_PATH or run `npx playwright install chromium`).
set -eu
: "${DEMO_DATABASE_URL:?set DEMO_DATABASE_URL to a PostgreSQL URL with CREATEDB}"
cd "$(dirname "$0")/.."

make web
go build -o bin/demo ./tools/demo
bin/demo -db "$DEMO_DATABASE_URL" -listen 127.0.0.1:18080 &
demo=$!
trap 'kill $demo 2>/dev/null; wait $demo 2>/dev/null || true' EXIT INT TERM

i=0
until curl -fs -o /dev/null -H 'Host: localhost' http://127.0.0.1:18080/healthz; do
	i=$((i + 1))
	[ "$i" -lt 60 ] || { echo "demo server did not start" >&2; exit 1; }
	sleep 0.5
done

cd docs
pnpm install --frozen-lockfile
pnpm screenshots
