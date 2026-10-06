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

The UI lives in `web/`: React, TypeScript, Vite and Tailwind CSS, with pnpm
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
the type check.

pnpm refuses packages released less than a day ago (supply-chain protection).
Keep it that way instead of adding exceptions.

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
<http://127.0.0.1:18080>.

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
squash-merged when all CI checks pass. To accept a pull request, review it and
enable **auto-merge**; GitHub merges it once CI is green.

| Label | Meaning |
|---|---|
| `feature`, `bug`, `chore`, `docs` | Kind of change (set by hand). |
| `dependencies` | Dependency update (set by Dependabot). |
| `db-migration` | Changes the database schema (set automatically). Back up before upgrading. |
| `breaking` | Needs manual steps when upgrading (set by hand). |
| `plan`, `review`, `ready` | Planning state of an issue; see [Issues, plans and releases](./process). |

Labels are defined in `.github/labels.json` and synced to GitHub when that
file changes on `main`.

## Dependency security

- CI runs `govulncheck`, `go mod verify`, `go vet`, golangci-lint (with
  `gosec`), tests with the race detector, and `pnpm audit` for the UI.
- Dependabot opens weekly update pull requests for Go modules, npm packages
  (UI and docs), GitHub Actions and Docker images. PostgreSQL major versions
  are excluded because they need a data migration.
- GitHub Actions are pinned to commit SHAs; workflows run with read-only
  permissions unless a job needs more.
- Direct Go dependencies are few: go-imap v2 (still in beta) with go-sasl,
  go-message, pgx, goose, cobra, x/net, x/term and x/text.

CI runs on a [self-hosted runner](./self-hosted-runner); pull requests from
forks run on GitHub-hosted runners.
