#!/bin/sh
# Self-test of scripts/review-gate.sh (`make review-gate-test`). Offline: temp
# git repos with a base and a head commit built from
# scripts/testdata/review-gate, the real tools (GOLANGCI_LINT, SQUAWK,
# ACTIONLINT, ZIZMOR, OXLINT, TSGOLINT; the Makefile sets them), stubs for tool failures, no module
# downloads (the Go fixture uses only the standard library).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
gate=$here/review-gate.sh
fx=$here/testdata/review-gate
: "${GOLANGCI_LINT:?set GOLANGCI_LINT (make review-gate-test sets it)}"
: "${SQUAWK:?set SQUAWK (make review-gate-test sets it)}"
: "${ACTIONLINT:?set ACTIONLINT (make review-gate-test sets it)}"
: "${ZIZMOR:?set ZIZMOR (make review-gate-test sets it)}"
: "${OXLINT:?set OXLINT (make review-gate-test sets it)}"
: "${TSGOLINT:?set TSGOLINT (make review-gate-test sets it)}"
real_golangci=$GOLANGCI_LINT real_squawk=$SQUAWK real_actionlint=$ACTIONLINT real_zizmor=$ZIZMOR
real_oxlint=$OXLINT real_tsgolint=$TSGOLINT

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# No network and no toolchain download; a cache of our own for golangci-lint.
export GOPROXY=off GOFLAGS=-mod=mod GOTOOLCHAIN=local GOWORK=off
export GOLANGCI_LINT_CACHE="$tmp/golangci-cache"
fail=0

# Stubs for tool failures.
mkdir "$tmp/stubs"
printf '#!/bin/sh\necho "panic: boom" >&2\nexit 3\n' >"$tmp/stubs/crash"
printf '#!/bin/sh\necho "this is not JSON"\nexit 1\n' >"$tmp/stubs/garbage"
# Valid empty JSON, but a failing exit code: the exit code alone must count.
printf '#!/bin/sh\necho "[]"\necho "fatal: config" >&2\nexit 3\n' >"$tmp/stubs/empty-fail"
# zizmor exits 10-14 for findings: that code with unreadable output.
printf '#!/bin/sh\necho "this is not JSON"\nexit 14\n' >"$tmp/stubs/garbage14"
# oxlint exits 1 for findings and for its own failures: a report without
# diagnostics under exit 1, and a clean report with noise on stderr.
printf '#!/bin/sh\necho "{\\"diagnostics\\": []}"\nexit 1\n' >"$tmp/stubs/ox-empty-1"
printf '#!/bin/sh\necho "{\\"diagnostics\\": []}"\necho "warning: something" >&2\nexit 0\n' >"$tmp/stubs/ox-noise"
chmod +x "$tmp/stubs/crash" "$tmp/stubs/garbage" "$tmp/stubs/empty-fail" "$tmp/stubs/garbage14" \
	"$tmp/stubs/ox-empty-1" "$tmp/stubs/ox-noise"

# line <file> <marker>: the line number of the fixture line carrying the marker.
line() { grep -n -- "$2" "$1" | head -1 | cut -d: -f1; }

# repo <name> <head setup>: a temp repo with the base commit on branch `base`
# and the head commit on `head` (checked out). The setup runs in the repo.
repo() {
	r=$tmp/$1
	mkdir -p "$r"
	(
		cd "$r"
		git init -q
		git config user.email t@example.invalid
		git config user.name t
		git config commit.gpgsign false
		mkdir -p fetch internal/store/migrations .github/workflows
		printf 'module example.invalid/fixture\n\ngo 1.22\n' >go.mod
		cp "$fx/fetch-base.go" fetch/fetch.go
		cp "$fx/00001_old.sql" internal/store/migrations/
		cp "$fx/old.yml" .github/workflows/
		mkdir -p web/src web/node_modules
		cp "$fx/web/tsconfig.json" web/
		cp "$fx/web/old.ts" web/src/old.ts
		echo '# fixture' >README.md
		git add -A
		git commit -q -m base
		git branch -q base
		git checkout -q -b head
		eval "$2"
		git add -A
		git commit -q --allow-empty -m head
	)
}

# gate <name> [VAR=value ...]: runs the gate in repo <name> against `base`;
# sets rc and leaves stdout in $tmp/<name>.out for the next check.
gate() {
	n=$1
	last=$1
	shift
	rc=0
	(
		cd "$tmp/$n"
		GOLANGCI_LINT=$real_golangci SQUAWK=$real_squawk ACTIONLINT=$real_actionlint ZIZMOR=$real_zizmor
		OXLINT=$real_oxlint TSGOLINT=$real_tsgolint
		export GOLANGCI_LINT SQUAWK ACTIONLINT ZIZMOR OXLINT TSGOLINT
		for kv in "$@"; do export "${kv?}"; done
		BASE=base sh "$gate"
	) >"$tmp/$n.out" 2>"$tmp/$n.err" || rc=$?
}

# check <label> <want rc> <jq filter that must be true on the JSON | "clean">:
# judges the last gate run.
check() {
	label=$1 n=$last want=$2 expect=$3
	why=
	lines=$(wc -l <"$tmp/$n.out")
	if [ "$rc" != "$want" ]; then
		why="exit $rc, want $want"
	elif [ "$lines" -ne 1 ]; then
		why="$lines lines of output, want 1"
	elif [ "$expect" = clean ]; then
		grep -q '^review-gate: clean (' "$tmp/$n.out" || why="not a clean line"
	elif ! jq -e "$expect" "$tmp/$n.out" >/dev/null 2>&1; then
		why="filter false: $expect"
	fi
	if [ -n "$why" ]; then
		echo "FAIL $label: $why" >&2
		sed 's/^/     out: /' "$tmp/$n.out" >&2
		sed 's/^/     err: /' "$tmp/$n.err" >&2
		fail=1
	else
		echo "ok   $label"
	fi
}

# --- clean diff: one line, every check skipped.
repo clean 'echo more >>README.md'
gate clean
check clean 0 clean
grep -q 'skipped: noctx (no changed non-test Go files), squawk (no new migrations), migration-changed (no migration changes), actionlint (no workflow changes), zizmor (no workflow changes), oxlint (no changed web/src TypeScript files))$' "$tmp/clean.out" ||
	{ echo "FAIL clean-skipped: $(cat "$tmp/clean.out")" >&2; fail=1; }

# --- noctx: only the lines the head adds, not Old in the base, not tests;
# two findings on one line stay two.
repo go 'cp "$fx/fetch-head.go" fetch/fetch.go; cp "$fx/fetch_test.go" fetch/'
gate go
l_new=$(line "$fx/fetch-head.go" 'want noctx$')
l_pair=$(line "$fx/fetch-head.go" 'want noctx noctx')
check go 1 "([.findings[] | [.tool, .rule, .file, .line]] | sort) ==
	[[\"golangci-lint\",\"noctx\",\"fetch/fetch.go\",$l_new],
	 [\"golangci-lint\",\"noctx\",\"fetch/fetch.go\",$l_pair],
	 [\"golangci-lint\",\"noctx\",\"fetch/fetch.go\",$l_pair]]
	and .ran == [\"noctx\"] and .errors == []"

repo go-test-only 'cp "$fx/fetch_test.go" fetch/'
gate go-test-only
check go-test-only 0 clean
grep -q 'skipped: noctx (no changed non-test Go files)' "$tmp/go-test-only.out" ||
	{ echo "FAIL go-test-only-skipped: $(cat "$tmp/go-test-only.out")" >&2; fail=1; }

# --- squawk on the Up section of an added migration, lines mapped to the file.
repo mig-new 'cp "$fx/00002_new.sql" internal/store/migrations/'
gate mig-new
l_def=$(line "$fx/00002_new.sql" 'want adding-field-with-default')
l_drop=$(line "$fx/00002_new.sql" 'want ban-drop-column')
check mig-new 1 "([.findings[] | [.tool, .rule, .file, .line]] | sort) ==
	[[\"squawk\",\"adding-field-with-default\",\"internal/store/migrations/00002_new.sql\",$l_def],
	 [\"squawk\",\"ban-drop-column\",\"internal/store/migrations/00002_new.sql\",$l_drop]]
	and .ran == [\"squawk\", \"migration-changed\"]"

# NO TRANSACTION switches off the in-transaction rules; without the line the
# same file is a finding.
repo mig-notx 'cp "$fx/00003_index.sql" internal/store/migrations/'
gate mig-notx
check mig-notx 0 clean
repo mig-tx 'grep -v "NO TRANSACTION" "$fx/00003_index.sql" >internal/store/migrations/00003_index.sql'
gate mig-tx
check mig-tx 1 '[.findings[].rule] == ["ban-concurrent-index-creation-in-transaction"]'

# --- migration-changed: edit, delete, rename of an existing migration.
repo mig-edit 'echo "-- edited" >>internal/store/migrations/00001_old.sql'
gate mig-edit
check mig-edit 1 '[.findings[] | [.rule, .file, .line]] ==
	[["migration-changed", "internal/store/migrations/00001_old.sql", null]]
	and (.findings[0].message | test("modified"))'
repo mig-delete 'git rm -q internal/store/migrations/00001_old.sql'
gate mig-delete
check mig-delete 1 '[.findings[] | [.rule, .file]] ==
	[["migration-changed", "internal/store/migrations/00001_old.sql"]]
	and (.findings[0].message | test("deleted"))'
repo mig-rename 'git mv internal/store/migrations/00001_old.sql internal/store/migrations/00001_renamed.sql'
gate mig-rename
check mig-rename 1 '[.findings[] | [.rule, .file]] ==
	[["migration-changed", "internal/store/migrations/00001_old.sql"]]'

# --- actionlint: planted workflow (template injection, undefined matrix
# property); a clean workflow change next to the base's broken old.yml.
repo wf-bad 'cp "$fx/bad.yml" .github/workflows/'
gate wf-bad
l_inj=$(line "$fx/bad.yml" 'want template injection')
l_mat=$(line "$fx/bad.yml" 'want undefined matrix property')
check wf-bad 1 "[.findings[] | select(.tool == \"actionlint\") | [.tool, .rule, .file, .line]] ==
	[[\"actionlint\",\"expression\",\".github/workflows/bad.yml\",$l_inj],
	 [\"actionlint\",\"expression\",\".github/workflows/bad.yml\",$l_mat]]
	and ([.findings[] | select(.tool == \"actionlint\")][0].message | test(\"github.event.pull_request.title.*untrusted\"))
	and ([.findings[] | select(.tool == \"actionlint\")][1].message | test(\"golang\"))
	and .ran == [\"actionlint\", \"zizmor\"]"
repo wf-ok 'cp "$fx/ok.yml" .github/workflows/'
gate wf-ok
check wf-ok 0 clean
grep -q 'ran: actionlint, zizmor;' "$tmp/wf-ok.out" || { echo "FAIL wf-ok-ran: $(cat "$tmp/wf-ok.out")" >&2; fail=1; }

# --- zizmor: an unpinned action, the pull_request_target trigger and a
# missing permissions block each give a finding on their line; the same
# action pinned to a SHA gives none. The template injection of bad.yml is found
# by zizmor as well (offline, medium and up; the gate reports file lines from 1).
repo zz-unpinned 'cp "$fx/zz-unpinned.yml" .github/workflows/'
gate zz-unpinned
l=$(line "$fx/zz-unpinned.yml" 'want unpinned-uses')
check zz-unpinned 1 "[.findings[] | [.tool, .rule, .file, .line]] ==
	[[\"zizmor\",\"unpinned-uses\",\".github/workflows/zz-unpinned.yml\",$l]]"
repo zz-target 'cp "$fx/zz-target.yml" .github/workflows/'
gate zz-target
l=$(line "$fx/zz-target.yml" 'want dangerous-triggers')
check zz-target 1 "[.findings[] | [.tool, .rule, .file, .line]] ==
	[[\"zizmor\",\"dangerous-triggers\",\".github/workflows/zz-target.yml\",$l]]"
repo zz-noperm 'cp "$fx/zz-noperm.yml" .github/workflows/'
gate zz-noperm
l=$(line "$fx/zz-noperm.yml" 'want excessive-permissions')
check zz-noperm 1 "[.findings[] | [.tool, .rule, .file, .line]] ==
	[[\"zizmor\",\"excessive-permissions\",\".github/workflows/zz-noperm.yml\",$l]]"
repo zz-pinned 'cp "$fx/zz-pinned.yml" .github/workflows/'
gate zz-pinned
check zz-pinned 0 clean
gate wf-bad
l_inj=$(line "$fx/bad.yml" 'want template injection')
check zz-bad 1 "[.findings[] | select(.tool == \"zizmor\") | [.rule, .line]] == [[\"template-injection\",$l_inj]]"
# main stays clean: the repository's own workflows, all of them as changed
# files (labeler.yml uses pull_request_target on purpose; zizmor knows
# actions/labeler as a safe user of it and needs no exemption, see
# docs/development/review-gate.md).
repo real-workflows 'cp "$here"/../.github/workflows/*.yml .github/workflows/'
gate real-workflows
check real-workflows 0 clean
repo real-labeler 'cp "$here/../.github/workflows/labeler.yml" .github/workflows/'
gate real-labeler
check real-labeler 0 clean
grep -q 'ran: actionlint, zizmor;' "$tmp/real-labeler.out" || { echo "FAIL real-labeler-ran: $(cat "$tmp/real-labeler.out")" >&2; fail=1; }

# --- oxlint (type-aware, three rules) on changed web/src files, only on the
# lines the branch adds. web/src/old.ts has a dropped promise in the base.
repo web-bad 'cp "$fx/web/bad.ts" web/src/'
gate web-bad
l_fl=$(line "$fx/web/bad.ts" 'want no-floating-promises')
l_cond=$(line "$fx/web/bad.ts" 'want promise in a condition')
l_cb=$(line "$fx/web/bad.ts" 'want promise-returning callback')
l_hook=$(line "$fx/web/bad.ts" 'want rules-of-hooks')
check web-bad 1 "([.findings[] | [.tool, .rule, .file, .line]] | sort) ==
	[[\"oxlint\",\"react-hooks/rules-of-hooks\",\"web/src/bad.ts\",$l_hook],
	 [\"oxlint\",\"typescript/no-floating-promises\",\"web/src/bad.ts\",$l_fl],
	 [\"oxlint\",\"typescript/no-misused-promises\",\"web/src/bad.ts\",$l_cond],
	 [\"oxlint\",\"typescript/no-misused-promises\",\"web/src/bad.ts\",$l_cb]]
	and .ran == [\"oxlint\"] and .errors == []
	and ([.findings[] | select(.rule == \"react-hooks/rules-of-hooks\")][0].message | test(\"conditionally\"))"
repo web-ok 'cp "$fx/web/ok.ts" web/src/'
gate web-ok
check web-ok 0 clean
grep -q 'ran: oxlint;' "$tmp/web-ok.out" || { echo "FAIL web-ok-ran: $(cat "$tmp/web-ok.out")" >&2; fail=1; }
# The base's own dropped promise: not reported when the branch touches the file
# elsewhere, reported when the branch adds one next to it (only the new one).
repo web-old-touched 'cp "$fx/web/old-touched.ts" web/src/old.ts'
gate web-old-touched
check web-old-touched 0 clean
grep -q 'ran: oxlint;' "$tmp/web-old-touched.out" || { echo "FAIL web-old-touched-ran: $(cat "$tmp/web-old-touched.out")" >&2; fail=1; }
repo web-old-added 'cp "$fx/web/old-added.ts" web/src/old.ts'
gate web-old-added
l=$(line "$fx/web/old-added.ts" 'want no-floating-promises')
check web-old-added 1 "[.findings[] | [.rule, .file, .line]] ==
	[[\"typescript/no-floating-promises\",\"web/src/old.ts\",$l]]"
# Without web/node_modules the check is skipped with the reason (never silent),
# and an error where CI requires it.
repo web-nodeps 'rm -rf web/node_modules; cp "$fx/web/bad.ts" web/src/'
gate web-nodeps
check web-nodeps 0 clean
grep -q 'skipped: .*oxlint (web deps not installed' "$tmp/web-nodeps.out" || { echo "FAIL web-nodeps-skipped: $(cat "$tmp/web-nodeps.out")" >&2; fail=1; }
gate web-nodeps REVIEW_GATE_REQUIRE_WEB=1
check web-nodeps-required 2 '.errors[0].tool == "oxlint" and (.errors[0].message | test("node_modules")) and .findings == []'

# --- errors: never a pass.
repo dirty 'echo more >>README.md'
echo untracked >"$tmp/dirty/new.txt"
gate dirty
check dirty 2 '.errors[0].tool == "git" and (.errors[0].message | test("uncommitted"))'

gate go GOLANGCI_LINT="$tmp/stubs/crash"
check go-crash 2 '.errors[0].tool == "golangci-lint" and (.errors[0].message | test("exit 3.*boom"))'
gate go GOLANGCI_LINT="$tmp/stubs/garbage"
check go-garbage 2 '.errors[0].tool == "golangci-lint"'
gate mig-new SQUAWK="$tmp/stubs/garbage"
check squawk-garbage 2 '.errors[0].tool == "squawk" and (.errors[0].message | test("unreadable JSON"))'
gate mig-new SQUAWK="$tmp/stubs/crash"
check squawk-crash 2 '.errors[0].tool == "squawk" and (.errors[0].message | test("exit 3"))'
gate wf-bad ACTIONLINT="$tmp/stubs/crash"
check actionlint-crash 2 '.errors[0].tool == "actionlint" and ([.findings[] | select(.tool == "actionlint")] == [])'
gate wf-bad ACTIONLINT="$tmp/stubs/empty-fail"
check actionlint-empty-fail 2 '.errors[0].tool == "actionlint" and (.errors[0].message | test("exit 3.*fatal"))'
gate zz-unpinned ZIZMOR="$tmp/stubs/crash"
check zizmor-crash 2 '.errors[0].tool == "zizmor" and (.errors[0].message | test("exit 3.*boom")) and .findings == []'
gate zz-unpinned ZIZMOR="$tmp/stubs/empty-fail"
check zizmor-empty-fail 2 '.errors[0].tool == "zizmor" and (.errors[0].message | test("exit 3.*fatal"))'
gate zz-unpinned ZIZMOR="$tmp/stubs/garbage"
check zizmor-garbage 2 '.errors[0].tool == "zizmor" and (.errors[0].message | test("exit 1:"))'
gate zz-unpinned ZIZMOR="$tmp/stubs/garbage14"
check zizmor-garbage-findings-code 2 '.errors[0].tool == "zizmor" and (.errors[0].message | test("unreadable JSON"))'
gate mig-new SQUAWK="$tmp/stubs/empty-fail"
check squawk-empty-fail 2 '.errors[0].tool == "squawk" and (.errors[0].message | test("exit 3.*fatal"))'
# A missing tool is an error where it is needed, together with the findings
# of the other checks, and no error where its check is skipped.
repo mixed 'cp "$fx/00002_new.sql" internal/store/migrations/; cp "$fx/ok.yml" .github/workflows/'
gate mixed ACTIONLINT="$tmp/no-such-actionlint"
check missing-tool-with-findings 2 '(.errors | length) == 1 and (.errors[0].message | test("not found"))
	and (.findings | length) == 2'
gate mixed ZIZMOR="$tmp/no-such-zizmor"
check missing-zizmor-with-findings 2 '(.errors | length) == 1 and .errors[0].tool == "zizmor"
	and (.errors[0].message | test("not found")) and (.findings | length) == 2'
gate web-bad OXLINT="$tmp/stubs/crash"
check oxlint-crash 2 '.errors[0].tool == "oxlint" and (.errors[0].message | test("exit 3.*boom")) and .findings == []'
gate web-bad OXLINT="$tmp/stubs/garbage"
check oxlint-garbage 2 '.errors[0].tool == "oxlint" and (.errors[0].message | test("unreadable output"))'
gate web-bad OXLINT="$tmp/stubs/empty-fail"
check oxlint-empty-fail 2 '.errors[0].tool == "oxlint" and (.errors[0].message | test("exit 3.*fatal"))'
gate web-bad OXLINT="$tmp/stubs/ox-empty-1"
check oxlint-exit1-no-diagnostics 2 '.errors[0].tool == "oxlint" and (.errors[0].message | test("without diagnostics"))'
gate web-ok OXLINT="$tmp/stubs/ox-noise"
check oxlint-stderr 2 '.errors[0].tool == "oxlint" and (.errors[0].message | test("stderr: warning"))'
# The real oxlint with a type engine that crashes or is not there.
gate web-bad TSGOLINT="$tmp/stubs/crash"
check tsgolint-crash 2 '.errors[0].tool == "oxlint" and .findings == []'
gate web-bad TSGOLINT="$tmp/no-such-tsgolint"
check missing-tsgolint 2 '.errors[0].tool == "tsgolint" and (.errors[0].message | test("not found"))'
gate web-bad OXLINT="$tmp/no-such-oxlint"
check missing-oxlint 2 '.errors[0].tool == "oxlint" and (.errors[0].message | test("not found"))'
gate clean ACTIONLINT="$tmp/no-such-actionlint" GOLANGCI_LINT="$tmp/no-such" SQUAWK="$tmp/no-such" ZIZMOR="$tmp/no-such" OXLINT="$tmp/no-such" TSGOLINT="$tmp/no-such"
check missing-tools-unneeded 0 clean

exit "$fail"
