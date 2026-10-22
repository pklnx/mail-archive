You review {{PR_URL}} in pklnx/mail-archive. Review only: no code changes, pushes or merges.
GitHub commands: see the GitHub operations table in CLAUDE.md (`gh` locally, MCP tools in cloud sessions).
Specification: the "## Plan" comment on #{{ISSUE}} with reviews and maintainer decisions.

Read the PR, its diff and the changed files in full on branch {{BRANCH}}. Look for: plan fidelity, data loss, crashes, hangs, security/privacy, tests that would fail without the change, time-based waits in tests; secret-scan weakening: check the file list for `.gitleaksignore`, `.gitleaks.toml` (and files it extends), `.github/workflows/secrets.yml`, `scripts/secret-scan-*` and the Makefile's gitleaks variables and targets, and grep the diff for `gitleaks:allow`; any hit is a blocking finding ("needs maintainer OK", list the entries) until the maintainer's OK is noted on the PR.
Blocking only per the CLAUDE.md list; everything else is a follow-up. Each finding: file:line, scenario, why, fix. Don't invent problems.

Checks: read CI status once (don't watch); locally run the tests of the changed code (`TEST_DATABASE_URL=postgres://mailarchive:mailarchive@localhost:5432/mailarchive?sslmode=disable go test -race -count=1 ./internal/<pkg>/ -run <filter>`); for a regression test, verify it fails without the fix.

Post one comment: "## Review", "Verdict: approve" / "approve, pending CI" / "changes requested", numbered findings, "## Follow-ups", "## Test report" (head SHA, checks table), `— Claude (<model>), reviewer`. Never start a line with the at-mention of claude.
Report: verdict, findings, follow-ups, checks.
