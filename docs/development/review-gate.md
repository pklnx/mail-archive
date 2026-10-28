# Review gate

The review gate runs deterministic checks on what a branch changes, before a
reviewer reads the diff. It reports only findings in the changed files or
lines, as one line of output, so the reviewer can read it cheaply.

```sh
make review-gate                  # against origin/main
make review-gate BASE=<commit>    # against another base
make review-gate-test             # self-test (temp git repos, no network)
make review-gate-tools            # only build or install the tools
```

The gate checks the committed `HEAD` against `git merge-base $BASE HEAD`. A
dirty working tree is an error ("commit or stash them first"), so a local run
and the CI run see the same files.

## Checks

| Check | Runs when the branch | Tool | Reports |
|---|---|---|---|
| `noctx` | adds or modifies a non-test Go file | golangci-lint, only `noctx` (`tools/review-gate/golangci.yml`) | HTTP requests without a context, only on the lines the branch adds |
| `squawk` | adds a migration | squawk (`.squawk.toml`) | risky SQL in the `-- +goose Up` section |
| `migration-changed` | changes anything in `internal/store/migrations/` | the gate script | an existing migration that was modified or deleted (a rename counts as a deletion) |
| `actionlint` | adds or modifies a workflow | actionlint | errors in those workflows, including template injection |
| `zizmor` | adds or modifies a workflow | zizmor, `--offline --min-severity medium` | workflow security: unpinned actions (`unpinned-uses`), dangerous triggers such as `pull_request_target` (`dangerous-triggers`), missing or broad `permissions:` (`excessive-permissions`), template injection, credential persistence |

A check whose files did not change is listed as skipped, with the reason.

The gate does not repeat the required `lint` and `vulncheck` jobs: they check
the whole tree, are clean on `main`, and the reviewer reads their result
anyway. It runs only what CI does not check elsewhere.

- **noctx** is not in `.golangci.yml`, because `main` has 6 hits in
  production code and 33 in tests. The gate runs it with
  `--new-from-merge-base` and `--uniq-by-line=false` (otherwise golangci-lint
  keeps one finding per line). Test files are left out.
- **squawk** gets the `Up` section only, because dropping things is what a
  `Down` section is for. goose runs each migration in a transaction unless
  the file says `-- +goose NO TRANSACTION`; the gate tells squawk which case
  applies. squawk counts lines from 0; the gate reports file lines from 1.
- **migration-changed**: a migration that has been released is never edited;
  add a new one (see [Database migrations](./#database-migrations)).
- **actionlint** runs without shellcheck and pyflakes, so the result does not
  depend on what is installed on the machine.
- **zizmor** runs on the same changed workflows, offline (it calls no GitHub
  API) and at medium severity and up, so the low-severity style audits stay
  out. It exits 0 when clean and 10-14 for findings (by severity); any
  other exit code or unreadable output is a tool error (exit 2). The gate
  reports each finding's primary location as `file:line`. A `zizmor.yml` in
  the repository and `# zizmor: ignore[rule]` comments are honoured; there is
  none today. `labeler.yml` uses `pull_request_target` on purpose (it never
  checks out or runs the pull request's code, see the comment in the file),
  and needs no exemption: zizmor's `dangerous-triggers` audit knows
  `actions/labeler` as a safe user of that trigger, so `main` stays clean. The
  self-test runs the repository's own workflows, `labeler.yml` alone as well,
  to catch a zizmor bump that changes this. Should a future zizmor flag it, add
  an `ignore` entry for that rule and file to a `zizmor.yml`, with a comment
  that names this reason, rather than a special case in the gate script.

### squawk rules

The rules that stay on are about data loss, table rewrites and breaking
clients, for example `adding-field-with-default` (volatile defaults only),
`adding-required-field`, `ban-drop-column`, `ban-drop-table`,
`ban-drop-database`, `renaming-column`, `renaming-table`,
`changing-column-type`, `prefer-text-field` and
`ban-concurrent-index-creation-in-transaction`.

These rules are excluded in `.squawk.toml`:

| Rule | Why it is off |
|---|---|
| `require-lock-timeout`, `require-statement-timeout`, `require-timeout-settings` | Migrations run through `./ma migrate` before the server starts, and `lock_timeout` is set in Go (`internal/store/attest.go`). |
| `require-concurrent-index-creation` | Not possible inside goose's transaction. |
| `constraint-missing-not-valid`, `adding-foreign-key-constraint`, `disallowed-unique-constraint`, `ban-drop-constraint`, `ban-drop-not-null` | They are about lock times on busy tables; the archive migrates while it is stopped. |
| `prefer-bigint-over-int`, `prefer-bigint-over-smallint`, `prefer-identity`, `ban-char-field`, `prefer-robust-stmts` | Style; the schema already decides these case by case. |

With these rules, the `Up` sections of the 30 migrations on `main` (as of
2026-10-28) give 3 findings, all for generated columns in released migrations
(00002, 00019). The gate checks only added migrations, so `main` needs no
baseline.

## Output and exit codes

A clean run prints one line:

```text
review-gate: clean (base 1ed0258, head 4c1e2a9; ran: actionlint; skipped: noctx (no changed non-test Go files), squawk (no new migrations), migration-changed (no migration changes))
```

Otherwise it prints one line of compact JSON:

```json
{"base":"<sha>","head":"<sha>","ran":["squawk","migration-changed"],"skipped":[{"check":"noctx","reason":"no changed non-test Go files"}],"findings":[{"tool":"squawk","rule":"ban-drop-column","file":"internal/store/migrations/00031_x.sql","line":4,"message":"Dropping a column may break existing clients."}],"errors":[]}
```

`line` is `null` for a finding about a whole file (`migration-changed`).

| Exit | Meaning |
|---|---|
| 0 | clean |
| 1 | findings |
| 2 | a tool error: a missing or failing tool, unreadable tool output, a dirty working tree, no merge base. Also when there are findings as well. |

A tool error is never a pass. `make review-gate` exits non-zero for both 1
and 2 (make reports 2 for any failing recipe); the output line says which it
was, and `sh scripts/review-gate.sh` returns the exact code. The tool paths
come from `GOLANGCI_LINT`, `SQUAWK`, `ACTIONLINT` and `ZIZMOR`, which the
Makefile sets.

## Tools

Each tool is pinned to the newest release that was at least 14 days old when
it was pinned. Bump them by hand, together with this table. Dependabot updates
squawk through `tools/review-gate/package.json`.

| Tool | Version | Released | Pinned in | Install | Licence |
|---|---|---|---|---|---|
| golangci-lint | v2.14.0 (the same as the `lint` job) | 2026-09-24 | `Makefile` (`GOLANGCI_LINT_VERSION`) | `go install` from the Go module proxy, built with the project's toolchain into `bin/golangci-lint-<version>/` and verified through the Go checksum database | GPL-3.0 (a development tool, not shipped) |
| actionlint | v1.7.12 | 2026-03-30 | `Makefile` (`ACTIONLINT_VERSION`) | the same, into `bin/actionlint-<version>/` | MIT |
| squawk-cli | 2.68.0 | 2026-10-08 | `tools/review-gate/package-lock.json` | `npm ci --ignore-scripts` into `tools/review-gate/node_modules/`; the platform binary is an optional dependency with an integrity hash, without a download at install time | Apache-2.0 OR MIT |
| zizmor | 1.30.1 (1.31.0 and 1.31.1 are from 2026-10-15, younger than 14 days on 2026-10-28) | 2026-09-09 | `tools/review-gate/requirements.txt` | PyPI wheels only, every wheel hash-pinned: `pip install --require-hashes --only-binary=:all: --no-deps` into the venv `bin/zizmor-venv/`, with the system `python3` (`make review-gate-tools`) | MIT |

All four run offline. The gate sends nothing anywhere; the installs use
the Go module proxy, the npm registry and PyPI (zizmor only; the maintainer
accepted PyPI as a registry on 2026-10-28, #292). zizmor has no dependencies.
Dependabot (`pip`) proposes new zizmor versions; a bump must carry the hashes
of the new wheels (`https://pypi.org/pypi/zizmor/<version>/json`).
squawk's `upload-to-github` command is not used.

## CI

The `review-gate` job in `.github/workflows/ci.yml` runs on every pull
request: it builds the tools, runs the self-test, then the gate against the
pull request's base commit, and puts the output into the job summary. zizmor
is installed with the runner's `python3 -m venv`: no `setup-python` action, so
no new action, and the install takes about a second, so it has no cache. Pushes
to `main` run only the self-test; they also save the caches that pull
requests read (a Go cache keyed on the `Makefile`, an npm cache keyed on the
gate's lockfile). `review-gate` is **not a required check** (maintainer
decision, 2026-10-28; to be revisited after about ten pull requests).

## Findings: outcomes and exceptions

1. **Blocking**: a confirmed vulnerability that is reachable in a supported
   configuration and allows unauthorized access, code execution, secret
   exposure or destructive data loss. In addition, the blocking list in
   `CLAUDE.md` applies.
2. Every other gate finding gets a recorded outcome in the review: **fixed**,
   **not affected** (with the reason), or an **exception**.
3. An **exception** is a comment on the issue that names the finding and its
   scope, the reason, a compensating control, the maintainer's approval and
   an expiry date. An expired exception blocks again. There is no exception
   file and no further process.
4. A red, skipped or missing `review-gate` job is not a pass.
5. The advisory `audit` and `docs-audit` checks follow the same rule (see
   [Dependencies](./#dependencies)).

## Self-test

`scripts/review-gate-test.sh` builds temp git repos with a base and a head
commit from `scripts/testdata/review-gate/` and runs the gate with the real
tools, offline. Each check fires on a planted violation and stays quiet on
its counter-case:

- `noctx`: a new `http.Get` is reported on its line, the one already in the
  base is not, a test file is not, and two calls on one line give two
  findings.
- `squawk`: a volatile default and a dropped column are reported with their
  file lines; a stable `now()` default and the `Down` section are not;
  `-- +goose NO TRANSACTION` switches off the in-transaction rules.
- `migration-changed`: an edited, a deleted and a renamed migration.
- `actionlint`: template injection and an undefined `matrix` property in a
  new workflow; a clean workflow change next to a broken workflow that was
  already in the base gives no finding.
- `zizmor`: an action by tag, a `pull_request_target` trigger and a missing
  `permissions:` block each give a finding on their line (`unpinned-uses`,
  `dangerous-triggers`, `excessive-permissions`); the same action pinned to a
  SHA does not; zizmor also finds the template injection of the actionlint
  fixture; all of the repository's own workflows, and `labeler.yml` alone, give
  no finding.
- Errors: a dirty tree, a crashing or garbage-printing stub for each tool
  (for zizmor also garbage under a findings exit code),
  a stub with valid output but a failing exit code, and a missing tool
  (exit 2, also with findings of the other checks; no error when its check is
  skipped).
