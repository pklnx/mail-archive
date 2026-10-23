# Contributing

## Setup

You need Go (version in `go.mod`), Docker, Node.js 26 with corepack for the
web UI and the docs, and optionally [golangci-lint](https://golangci-lint.run/)
v2. Node.js 26 no longer ships corepack; install it once with
`npm install -g corepack`.

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

The UI moves to the Carbon Design System (`@carbon/react`, #168). Carbon's
packages and IBM Plex run a telemetry script after install that reports to
IBM; `web/pnpm-workspace.yaml` blocks the install scripts of all of them, and
the install fails when a new dependency brings a script that is not listed
there. Set `IBM_TELEMETRY_DISABLED=true` as well (the `Makefile`, CI and the
`Dockerfile` do). The fonts are bundled (`web/src/fonts.css`): Carbon's CSS
loads them from IBM's CDN, so the build uses a copy without those rules
(`web/build/stripRemoteFonts.ts` writes `web/src/vendor/carbon.css` when
Vite starts), and `pnpm build` fails if the result refers to another host
(`web/scripts/check-dist.mjs`). Carbon's built-in English texts get German
ones from the `carbon` section of `i18n.ts`.

The colours come from the pklnx design system
([pklnx/design-system](https://github.com/pklnx/design-system), #211):
Carbon v11 unchanged, with Purple instead of Blue as the interactive colour
(buttons, links, focus in light mode, interactive borders).
`web/src/index.css` imports its generated theme,
`@pklnx/design-system/theme/pklnx-carbon.css`, right after Carbon's CSS and
in the same `carbon` layer. Its rules use the theme classes `theme.ts` sets
on `<html>` (`cds--white`, `cds--g100`) with the same specificity as
Carbon's, so they replace Carbon's token values; light and dark mode need
nothing else, and the file has no URLs, so `check-dist` is unaffected. No
colour value is copied into this repository. The package is a tarball
attached to the design system's GitHub release, so `web/package.json` names
the release asset's URL and `web/pnpm-lock.yaml` pins its integrity; it
needs no registry account, has no install script, and pnpm's one-day
release age does not apply to it. Rules of the design system:

- At most one `kind="primary"` button per view (a tab panel and a modal
  each count as one view; Carbon's `Button` is primary without a `kind`).
  The others are `secondary`, `tertiary` or `ghost`.
- Do not use `support-caution-undefined`: it is Purple in Carbon and would
  look like the interactive colour.

### End-to-end tests

`web/e2e` holds a Playwright suite that drives the UI in Chromium against
the demo server (see [Documentation](#documentation)): login with 2FA and its
errors, search and the message view, "Only in archive", accounts, retention,
users and the phone layout. It needs Go, a PostgreSQL user with `CREATEDB`
and a Chromium:

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
trace in `web/test-results/` (`pnpm exec playwright show-trace <file>`).

The tests fail on page errors, console errors, CSP violations and requests
to any other host (`web/e2e/fixtures.ts`). They use role and label queries,
wait for elements and responses, never for time, and run without retries.
Names a test creates are unique per run, and no test depends on another.
CI runs the suite in the `web-e2e` job.

The job is skipped on docs-only pull requests, where only `docs/**`,
`.claude/**` or `*.md` files change (`*.md` also matches in code
directories). The `changes` job decides this with `scripts/ci-docs-only.sh`.
The skip happens at job level, not with a workflow-level `paths:` filter: a
skipped job counts as passing for a required check, while a workflow that never
starts would leave it pending for ever. The filter fails closed: a failed
detection, an empty diff or any other changed file runs the suite, and so does
every push to `main`. `web-e2e` is meant to be a required check.

## Documentation

This site is built with [VitePress](https://vitepress.dev/) from `docs/` and
published to GitHub Pages on every merge to `main`. Pull requests that touch
`docs/` build it in CI, which fails on broken links.

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
again, so a broken `Down` section fails CI.

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

Changes start as an issue with an approved plan; see
[Issues, plans and releases](./process).

`main` is protected: changes go through pull requests, which can only be
squash-merged when all CI checks pass. A pull request is merged (squash) after
an approving review and green checks; auto-merge is not used. Agent rules are
in `CLAUDE.md` at the repository root.

| Label | Meaning |
|---|---|
| `feature`, `bug`, `chore`, `docs` | Kind of change (set by hand). `feature` makes at least a minor release. |
| `dependencies` | Dependency update (set by Dependabot). |
| `db-migration` | Changes the database schema (set automatically). Back up before upgrading. Makes at least a minor release. |
| `breaking` | Needs manual steps when upgrading (set by hand). Makes a major release. |
| `plan`, `review`, `ready` | Planning state of an issue; see [Issues, plans and releases](./process). |
| `second-review` | Risky pull request: needs a second, independent session review (a marker; it triggers nothing). |

Labels are defined in `.github/labels.json` and synced to GitHub when that
file changes on `main`.

## Flaky tests

A rerun caused by a flaky test is logged in [Flaky tests](./flaky-tests).

## Security checks

### Dependencies

- CI runs `govulncheck`, `go mod verify`, `go vet`, golangci-lint (with
  `gosec`), tests with the race detector, and `pnpm audit` for the UI.
- Dependabot opens weekly update pull requests for Go modules, npm packages
  (UI and docs), GitHub Actions and Docker images. PostgreSQL major versions
  are excluded because they need a data migration.
- Dependabot does not update URL dependencies such as
  `@pklnx/design-system` (the theme, see [Web UI](#web-ui)): bump them by
  hand in a pull request. Download the new release's tarball, verify it
  with `gh attestation verify pklnx-design-system-X.Y.Z.tgz --repo
  pklnx/design-system --source-ref refs/heads/main`, change the URL in
  `web/package.json`, run `pnpm install`, and compare the `integrity` line
  in `web/pnpm-lock.yaml` with the one in the release notes (and its
  SHA256 with the release's `SHA256SUMS`). Note the check in the pull
  request.
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
the maintainer adds it to the required checks of the `main` ruleset. Secret
values are redacted in the log. Pull request runs cancel older runs of the
same pull request; push runs are never cancelled.

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
