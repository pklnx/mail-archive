#!/bin/sh
# Regenerates the screenshots in docs/public/screenshots from the demo server.
# Needs a PostgreSQL user with CREATEDB in DEMO_DATABASE_URL (the demo creates
# and drops its own database), Go, Node.js with pnpm, and a Chromium for
# Playwright (set CHROMIUM_PATH or run `npx playwright install chromium`).
#
# The shots use a fixed clock, so two runs give the same images: the demo
# dates its mail from DOCS_NOW, the browser's clock stands just after it, and
# docs/scripts/screenshots.mjs moves the times the server records during the
# run (syncs, logins, the log) next to DOCS_NOW. DOCS_NOW must stay a few
# days before the real time of any run. When a shot starts to differ because
# of retention (the server decides it against the real clock), move DOCS_NOW
# forward and commit the full set of new images once.
set -eu
: "${DEMO_DATABASE_URL:?set DEMO_DATABASE_URL to a PostgreSQL URL with CREATEDB}"
cd "$(dirname "$0")/.."

DOCS_NOW=2026-10-23T08:41:00Z
export DOCS_NOW

make web
go build -o bin/demo ./tools/demo
# Before the demo starts, so the install time does not stretch the gap
# between the demo's own sync and the shots.
(cd docs && pnpm install --frozen-lockfile)

# A fixed IMAP port: the account pages show it.
bin/demo -db "$DEMO_DATABASE_URL" -listen 127.0.0.1:18080 -imap-listen 127.0.0.1:18043 -now "$DOCS_NOW" &
demo=$!
trap 'kill $demo 2>/dev/null; wait $demo 2>/dev/null || true' EXIT INT TERM

i=0
until curl -fs -o /dev/null -H 'Host: localhost' http://127.0.0.1:18080/healthz; do
	i=$((i + 1))
	[ "$i" -lt 60 ] || { echo "demo server did not start" >&2; exit 1; }
	sleep 0.5
done
# The moment the demo is ready (Unix seconds): the anchor of the time rewrite.
DOCS_READY=$(date +%s)
export DOCS_READY

cd docs
pnpm screenshots
