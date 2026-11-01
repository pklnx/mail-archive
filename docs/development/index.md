# Contributing

## Setup

You need Go (version in `go.mod`), Docker, Node.js 26 with corepack for the
web UI and the docs, and optionally [golangci-lint](https://golangci-lint.run/)
v2. Node.js 26 no longer ships corepack; install it once with
`npm install -g corepack`.

`make web`, `make web-e2e` and `make docs` use a pnpm that is already on
`PATH` and run `corepack enable` only when there is none. To use your own
pnpm (for example in a sandbox where corepack must not be enabled), put a
wrapper on `PATH`, or skip the step with `make PNPM_SETUP=true web`. pnpm
switches to the version pinned in `packageManager` by itself.

```sh
make build              # bin/mail-archive (with the web UI if built)
make test-unit          # unit tests, no database needed
docker compose up -d postgres
make test TEST_DATABASE_URL='postgres://mailarchive:<password>@localhost:5432/mailarchive?sslmode=disable'
make lint
make vuln               # govulncheck
make web                # build the web UI
make docker-multiarch   # build amd64 + arm64 images
scripts/compose-smoke-test.sh   # end-to-end test of Compose and ./ma (also in CI)
```

Integration tests start an in-memory IMAP server (`internal/imaptest`) and
create a throwaway database per test; the user in `TEST_DATABASE_URL` needs
`CREATEDB`.

Tests run in random order (`go test -shuffle=on`, in CI and `make test`) and
must not depend on each other's state. A failure that shows only in some
orders prints the seed in the failing package's log as `-test.shuffle N`;
rerun it with `go test -shuffle=N -run … ./internal/<pkg>`.

## Web UI

The UI lives in `web/`: React, TypeScript, Vite and the Carbon Design System
(`@carbon/react`; page layout in `web/src/layout.css`), with pnpm
through corepack. `make web` builds it into `internal/web/ui/dist`, which is
embedded into the binary; a plain `go build` without it serves a hint page.

```sh
cd web
corepack enable
pnpm install
pnpm dev        # http://localhost:5173, proxies /api to `serve` on :8080
pnpm test
```

UI texts are in `web/src/i18n.ts`, in English and German. The German
dictionary has the type of the English one, so a missing translation fails
the type check. A new language is one more dictionary with its
`languageName`; the profile offers it without a server change. A new format
for dates and numbers is one more entry in `formatChoices`
(`web/src/Profile.tsx`).

pnpm refuses packages released less than a day ago (supply-chain protection).
Keep it that way instead of adding exceptions.

The rules for UI pull requests (Carbon's install scripts and telemetry, the
bundled fonts, lazy views and the initial-bundle budget, the pklnx design
system and its theme) are in
[`.claude/rules/web-ui.md`](https://github.com/pklnx/mail-archive/blob/main/.claude/rules/web-ui.md).

The logo (the mark per theme in the header, the empty reader and the login
pages; the tile as favicon and app icon) and the docs logos are copied
unchanged from the design system; `brand-assets.json` names the commit and
the checksum of each file, and `web/src/brandAssets.test.ts` fails on a
changed copy or on a brand file without a row. They are not under the MIT
License: see `BRAND-ASSETS.md`. A new file in `web/public` also needs an entry
in `rootFiles` (`internal/web/ui/ui.go`), or the server answers 404 for it.

### End-to-end tests

`web/e2e` holds a Playwright suite that drives the UI in Chromium against
the demo server (see [Documentation](#documentation)): login with 2FA and its
errors, search and the message view, "Only in archive", accounts, retention,
users and the phone layout. It also checks dark mode (computed colours and
contrast), phones and tablets through device emulation (touch, `pointer:
coarse`, the iPhone's safe-area insets) and a model of Safari's and
Firefox's focus on click (`web/e2e/clickModel.ts`). What only a real device,
Safari, Firefox or a screen reader can show stays on the manual checklist,
the open issue labelled
[`device-checks`](https://github.com/pklnx/mail-archive/issues?q=is%3Aissue%20is%3Aopen%20label%3Adevice-checks).
It needs Go, a
PostgreSQL user with `CREATEDB` and a Chromium:

```sh
make web-e2e DEMO_DATABASE_URL='postgres://mailarchive:<password>@localhost:5432/mailarchive?sslmode=disable'
```

The config (`web/playwright.config.ts`) builds the UI, builds and starts a
fresh demo on <http://localhost:18181> (IMAP on port 18143), logs the demo
admin in once and stops the demo at the end. Set `CHROMIUM_PATH` to use an
installed Chromium, or run `pnpm exec playwright install chromium` once in
`web/`. An installed Chromium should have the same major version as the one
Playwright brings (`pnpm exec playwright --version` names the Playwright
release; its release notes name the Chromium). Older builds word console
messages differently, and the guards match some of them by their text. CI
uses Playwright's own headless shell (`--only-shell`). A single test: `cd web && pnpm e2e -g "phone"`. A failed test keeps a
trace in `web/test-results/` (`pnpm exec playwright show-trace <file>`); the
trace has no DOM snapshots and no network log, because Playwright's DOM
snapshotter makes the browser log blocked script in sandboxed frames, which
the guards fail on (#260).

The tests fail on page errors, console errors, CSP violations and requests
to any other host (`web/e2e/fixtures.ts`). They use role and label queries,
wait for elements and responses, never for time, and run without retries.
Names a test creates are unique per run, and no test depends on another.
CI runs the suite in the `web-e2e` job. The suite itself (`pnpm e2e`, about
170 tests on one worker) takes about six minutes there; the device checks of
[#367](https://github.com/pklnx/mail-archive/issues/367) added about 40
seconds of it, measured locally. The job installs only Playwright's headless
Chromium, without apt, and checks its libraries (`scripts/ci-playwright-chromium.sh`): the runner image
has them. If one is missing, the run shows a warning and installs them with
apt, which can take 8 to 13 minutes when the runner's apt mirror is slow. The
job's timeout is 20 minutes so that this fallback fits.

The job is skipped on docs-only pull requests, where only `docs/**`,
`.claude/**` or `*.md` files change (`*.md` also matches in code
directories). The `changes` job decides this with `scripts/ci-docs-only.sh`.
The skip happens at job level, not with a workflow-level `paths:` filter: a
skipped job counts as passing for a required check, while a workflow that never
starts would leave it pending for ever. The filter fails closed: a failed
detection, an empty diff or any other changed file runs the suite, and so does
every push to `main`. `web-e2e` is a required check.

One spec also runs in Playwright's WebKit until
[#398](https://github.com/pklnx/mail-archive/issues/398) runs the suite there:
`web/e2e/frame-links.spec.ts`, which checks that no link in an HTML mail leads
into the app and that a link redirecting back to the archive is refused
(#459; its server listens on 127.0.0.2:18182, so it needs Linux's loopback
range). `E2E_BROWSER` picks the engine (`chromium` by default, or
`webkit`; anything else is an error), and `pnpm e2e:webkit` in `web/` runs the
`webkit` project, which runs only that spec and records traces without
screenshots. WebKit has to be installed first (`pnpm exec playwright install
webkit`, plus its system libraries: `--with-deps`). CI runs it in the
`web-e2e-webkit` job, with the same skip rule as `web-e2e`. That job always
installs WebKit's libraries with apt, which took 8 to 13 minutes in this
repository when the mirror was slow (`scripts/ci-playwright-chromium.sh`), so
its timeout is 30 minutes. It is not a required check, but a red result
blocks merging.

## Documentation

This site is built with [VitePress](https://vitepress.dev/) from `docs/` and
published to GitHub Pages on every merge to `main`. Pull requests that touch
`docs/` build it in CI, which fails on broken links. It uses the pklnx
VitePress theme (`@pklnx/design-system/theme/vitepress.css`, IBM Plex, from
the design system's release tarball as in `web/`). `pnpm build` ends with
`docs/scripts/check-dist.mjs`, which fails when the built site lacks the font
and icon licence texts under `licenses/`, emits the default theme's Inter
font, misses a logo or `theme-color`, or when the lockfile installs a
`@carbon/` or `@ibm/` package.

```sh
cd docs
pnpm install
pnpm dev        # http://localhost:5173/mail-archive/
```

Screenshots come from the demo server, which serves made-up mail from an
in-memory IMAP server and a temporary database:

```sh
make docs-screenshots DEMO_DATABASE_URL='postgres://mailarchive:<password>@localhost:5432/mailarchive?sslmode=disable'
```

It needs a Chromium for Playwright (`npx playwright install chromium`, or set
`CHROMIUM_PATH`). Run it after visible UI changes and commit the new images.
The shots use a fixed clock (`DOCS_NOW` in `scripts/docs-screenshots.sh`):
the demo dates its mail from it, the browser's clock stands just after it,
and times the server records during the run are moved next to it
(`docs/scripts/pin-times.mjs`). Two runs give the same images, so only real
UI changes show up in the diff and the output can be committed as it is.
`DOCS_SCREENSHOTS_DIR` sets another output folder.

The run fails when an API answer has a time after `DOCS_NOW` that the
rewrite did not move (the error names the path, the key and the value), or
when it ends more than 4 minutes after the demo was ready. Such a time
comes from the real clock and would change from day to day: pin it in the
rewrite's window, derive it from `DOCS_NOW` in the demo, or, if it is stable
(derived from a mail date, like `retainUntil`), add its API path and key to
`STABLE_TIMES`. `pnpm test` in `docs` runs the unit tests of the rewrite and
the check; CI runs them with the docs build. Retention is still decided
against the real clock: move `DOCS_NOW` forward and commit the full set of
new images once if it ever flips a shot.

Known gaps. The check sees only RFC 3339 strings in JSON answers. It misses
a real-clock time at or before `DOCS_NOW` (it looks like a mail date),
date-only strings, epoch numbers and answers that are not JSON. It does not
compare two runs either, so randomness or an unstable order shows up only
when the next `make docs-screenshots` changes an image.
The demo alone: `go run ./tools/demo -db URL`, then open
<http://localhost:18080> (passkeys need the host name, not the IP address).

## Database migrations

Migrations are SQL files in `internal/store/migrations`, managed with
[goose](https://github.com/pressly/goose) and embedded in the binary. Each
file has an `Up` and a `Down` section:

```sql
-- +goose Up
ALTER TABLE messages ADD COLUMN thread_id TEXT;

-- +goose Down
ALTER TABLE messages DROP COLUMN thread_id;
```

Name new files with the next number. Never edit a migration that has been
released; add a new one. The store tests roll every migration down and up
again, so a broken `Down` section fails CI. The [review gate](./review-gate)
lints the `Up` section of new migrations with squawk and reports an edited
or deleted migration.

## Database queries

Queries are plain SQL in `internal/store/queries/*.sql`.
[sqlc](https://sqlc.dev/) checks them against the schema and generates
type-safe Go code in `internal/store/db`, which `internal/store` wraps. After
changing a query or adding a migration:

```sh
make generate      # regenerate internal/store/db (commit the result)
make sqlc-check    # what CI runs: vet queries and fail on stale code
```

## Pull requests and labels

Changes start as an issue with an approved plan; the flow, the reviews and
the labels are in [Issues, plans and releases](./process).

`main` is protected: changes go through pull requests, squash-merged after
an approving review and green required checks (`test`, `lint`, `vulncheck`,
`docker`, `compose`, `web`, `web-e2e` and `secret-scan`); auto-merge is not
used. Not required are `audit` and `docs-audit` (see
[Dependencies](#dependencies)), `review-gate` (see
[Review gate](./review-gate)) and `web-e2e-webkit` (see
[End-to-end tests](#end-to-end-tests)). Agent rules are in `CLAUDE.md` at
the repository root. The labels and who sets them:
[Labels](./process#labels).

## CI

- A pull request's CI run cancels the older run of the same pull request;
  runs on `main` are never cancelled, so every merge gets a complete result.
  The docs workflow queues its `main` runs instead, so deploys stay in order.
- Every job has a timeout (`timeout-minutes`), about two to five times its
  longest run so far.
- CI pulls Docker Hub images through mirror.gcr.io (Google's public Docker Hub
  cache), with Docker Hub as the fallback, so a Docker Hub error or rate limit
  does not fail a job. These pulls are anonymous: dockerd would send the
  runner's Docker Hub login to the mirror, which refuses it. The PostgreSQL
  services of the `test`, `web-e2e` and `web-e2e-webkit` jobs are named on the
  mirror directly and have no fallback. Users' builds (the Dockerfile, `docker-compose.yml`,
  `./ma`, the release workflow) do not use the mirror; see the comments in
  `.github/workflows/ci.yml`.
- The `docker` job keeps its layer cache in the GitHub Actions cache. Only
  runs on `main` write it; pull requests read it.

## Flaky tests

A rerun caused by a flaky test is logged in [Flaky tests](./flaky-tests).

## Security checks

### Dependencies

- CI runs `govulncheck`, `go mod verify`, `go vet`, golangci-lint (with
  `gosec`) and tests with the race detector.
- The `review-gate` job checks what a pull request changes: HTTP requests
  without a context in new Go code, new migrations and changed workflows
  (template injection and other actionlint errors). See
  [Review gate](./review-gate).
- `pnpm audit` runs in the `audit` (UI, production dependencies) and
  `docs-audit` (docs site) checks, which are not required: they call the npm
  registry, which can time out, and Dependabot covers the same advisories. A
  red audit does not block a merge, but it is not ignored either. The
  coordinator reads its log: a registry error needs nothing (no flaky-test
  row, since it is not a test); a real advisory gets a recorded outcome, as a
  review gate finding does: fixed (usually a Dependabot pull request), not
  affected (with the reason), or an exception
  ([outcomes and exceptions](./review-gate#findings-outcomes-and-exceptions)).
  An advisory that cannot be fixed now gets a follow-up issue as well.
- Dependabot opens weekly update pull requests for Go modules, npm packages
  (UI and docs), GitHub Actions and Docker images. PostgreSQL major versions
  are excluded because they need a data migration. Dependabot waits 14 days
  after a release before it opens a version update (`cooldown` in
  `.github/dependabot.yml`); security updates are exempt.
- Dependabot does not update URL dependencies such as
  `@pklnx/design-system` (the theme, see [Web UI](#web-ui); used by both
  `web/` and `docs/`): bump them by hand in a pull request. Download the new
  release's tarball and verify it with `gh attestation verify
  pklnx-design-system-X.Y.Z.tgz --repo pklnx/design-system --signer-workflow
  pklnx/design-system/.github/workflows/release.yml --source-ref
  refs/heads/main --format json` (pinned to the release workflow). Without a
  `gh` login that command exits 4; then fetch the public attestation of the
  tarball's SHA256 and verify against that bundle instead:
  `curl -sS https://api.github.com/repos/pklnx/design-system/attestations/sha256:<sha256>`,
  write each `attestations[].bundle` as one JSON line into `b.jsonl`, and add
  `--bundle b.jsonl` to the command above. As a counter-proof, the same
  command with a wrong `--repo` must fail. Then change the URL in both
  `web/package.json` and `docs/package.json`, run `pnpm install` in `web/`
  and in `docs/`, and compare the `integrity` line in `web/pnpm-lock.yaml`
  and `docs/pnpm-lock.yaml` with the one in the release notes (and the
  tarball's SHA256 with the release's `SHA256SUMS`). `pnpm build` in `docs/`
  runs `scripts/check-dist.mjs`, which counts the Carbon icons' Apache-2.0
  notice in the built CSS; if the new release adds or removes an icon, update
  the expected count there. If the release changes brand files, copy them
  unchanged and update `brand-assets.json`. Note the verification output in
  the pull request.
- GitHub Actions are pinned to commit SHAs; workflows run with read-only
  permissions unless a job needs more.
- Direct Go dependencies are few: go-imap v2 (still in beta) with go-sasl,
  go-message, pgx, goose, cobra, x/net, x/term and x/text.

### Secret scanning

The `secret-scan` check (`.github/workflows/secrets.yml`) runs
[gitleaks](https://github.com/gitleaks/gitleaks) with its built-in rules and
the narrow allowlist in `.gitleaks.toml`:

- on every pull request: the commits the pull request adds
  (`base..head`);
- on every push to `main`: the commits the push adds (`before..after`). A
  push without a previous commit, or whose previous commit is not in the
  clone, scans only its last commit and says so in the log.

The workflow has no `paths` filter, so the check runs on every pull request;
`secret-scan` is a required check. Secret values are redacted in the log. Pull
request runs cancel older runs of the same pull request; push runs are never
cancelled.

```sh
make secrets        # scan the full history of all local refs
make secrets-test   # self-test: the scan fails and passes where it should
```

gitleaks is pinned in the Makefile (`GITLEAKS_VERSION`) and verified through
the Go checksum database. Dependabot does not update it: bump it by hand,
then rescan the full history with `make secrets` on a clone with all
branches and tags.

**If the check finds a credential:**

1. Treat it as leaked: rotate or revoke it first. Removing the commit does
   not help, because the value stays in the history and in clones.
2. Never post the value on GitHub (issues, pull requests, comments). Report
   it to the maintainer privately.
3. Remove it from the branch. On a pull request branch, rewrite the branch
   so no commit contains the value. On `main` the history is not rewritten:
   after rotating, add the finding's fingerprint (`commit:file:rule:line`,
   printed by gitleaks) to `.gitleaksignore` with a comment.

**False positives** (a fake key in a test fixture) get a narrow entry in
`.gitleaks.toml`: one `[[allowlists]]` block with `condition = "AND"`, the
fixture's exact path and a regex for the fake value, and a comment saying why
it is safe. Global regexes and path globs are not allowed. Better still,
build fake credentials at runtime in tests, so nothing in the repository
matches a rule (see `scripts/secret-scan-test.sh`).
