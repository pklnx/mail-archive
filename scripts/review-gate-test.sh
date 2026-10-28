#!/bin/sh
# Self-test of scripts/review-gate.sh (`make review-gate-test`). Offline: temp
# git repos with a base and a head commit built from
# scripts/testdata/review-gate, the real tools (GOLANGCI_LINT, SQUAWK,
# ACTIONLINT; the Makefile sets them), stubs for tool failures, no module
# downloads (the Go fixture uses only the standard library).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
gate=$here/review-gate.sh
fx=$here/testdata/review-gate
: "${GOLANGCI_LINT:?set GOLANGCI_LINT (make review-gate-test sets it)}"
: "${SQUAWK:?set SQUAWK (make review-gate-test sets it)}"
: "${ACTIONLINT:?set ACTIONLINT (make review-gate-test sets it)}"
real_golangci=$GOLANGCI_LINT real_squawk=$SQUAWK real_actionlint=$ACTIONLINT

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
chmod +x "$tmp/stubs/crash" "$tmp/stubs/garbage" "$tmp/stubs/empty-fail"

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
		GOLANGCI_LINT=$real_golangci SQUAWK=$real_squawk ACTIONLINT=$real_actionlint
		export GOLANGCI_LINT SQUAWK ACTIONLINT
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
grep -q 'skipped: noctx (no changed non-test Go files), squawk (no new migrations), migration-changed (no migration changes), actionlint (no workflow changes))$' "$tmp/clean.out" ||
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
check wf-bad 1 "[.findings[] | [.tool, .rule, .file, .line]] ==
	[[\"actionlint\",\"expression\",\".github/workflows/bad.yml\",$l_inj],
	 [\"actionlint\",\"expression\",\".github/workflows/bad.yml\",$l_mat]]
	and (.findings[0].message | test(\"github.event.pull_request.title.*untrusted\"))
	and (.findings[1].message | test(\"golang\"))"
repo wf-ok 'cp "$fx/ok.yml" .github/workflows/'
gate wf-ok
check wf-ok 0 clean
grep -q 'ran: actionlint;' "$tmp/wf-ok.out" || { echo "FAIL wf-ok-ran: $(cat "$tmp/wf-ok.out")" >&2; fail=1; }

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
check actionlint-crash 2 '.errors[0].tool == "actionlint" and .findings == []'
gate wf-bad ACTIONLINT="$tmp/stubs/empty-fail"
check actionlint-empty-fail 2 '.errors[0].tool == "actionlint" and (.errors[0].message | test("exit 3.*fatal"))'
gate mig-new SQUAWK="$tmp/stubs/empty-fail"
check squawk-empty-fail 2 '.errors[0].tool == "squawk" and (.errors[0].message | test("exit 3.*fatal"))'
# A missing tool is an error where it is needed, together with the findings
# of the other checks, and no error where its check is skipped.
repo mixed 'cp "$fx/00002_new.sql" internal/store/migrations/; cp "$fx/ok.yml" .github/workflows/'
gate mixed ACTIONLINT="$tmp/no-such-actionlint"
check missing-tool-with-findings 2 '(.errors | length) == 1 and (.errors[0].message | test("not found"))
	and (.findings | length) == 2'
gate clean ACTIONLINT="$tmp/no-such-actionlint" GOLANGCI_LINT="$tmp/no-such" SQUAWK="$tmp/no-such"
check missing-tools-unneeded 0 clean

exit "$fail"
