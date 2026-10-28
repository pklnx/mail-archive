#!/bin/sh
# Review gate: deterministic checks on what the branch changes, for the
# reviewer (docs/development/review-gate.md). Run it from the repository to
# check; `make review-gate` builds the tools and sets their paths.
#
#   BASE=<rev> sh scripts/review-gate.sh
#
# It checks the committed HEAD against `git merge-base $BASE HEAD` (BASE
# defaults to origin/main; CI passes the pull request's base SHA):
#   noctx              golangci-lint, noctx only, on the lines the branch adds
#                      to non-test Go files (tools/review-gate/golangci.yml)
#   squawk             the `-- +goose Up` section of added migrations
#                      (.squawk.toml)
#   migration-changed  an existing migration modified or deleted (a rename is
#                      a deletion plus an addition)
#   actionlint         added or modified workflows
#   zizmor             the same workflows: unpinned actions, dangerous
#                      triggers, permissions, ... (--offline, medium and up)
#   oxlint             added or modified web/src .ts/.tsx files: floating and
#                      misused promises and the rules of hooks, only on the
#                      lines the branch adds (tools/review-gate/oxlintrc.json);
#                      needs web/node_modules (pnpm install in web/), else
#                      skipped
# A check whose files did not change is listed as skipped, with the reason.
#
# Tools: GOLANGCI_LINT, SQUAWK, ACTIONLINT, ZIZMOR, OXLINT, TSGOLINT (commands
# or paths). Configs come from REVIEW_GATE_CONF (default: the repository this
# script is in). REVIEW_GATE_REQUIRE_WEB=1 (CI) makes missing web/node_modules
# an error instead of a skip.
#
# Output: one line. Clean: "review-gate: clean (...)". Otherwise compact JSON:
#   {"base","head","ran":[check],"skipped":[{"check","reason"}],
#    "findings":[{"tool","rule","file","line","message"}],
#    "errors":[{"tool","message"}]}
# Exit 0 clean, 1 findings, 2 a tool error (also together with findings). A
# missing or failing tool is exit 2, never a pass.
set -u

here=$(cd "$(dirname "$0")" && pwd)
conf=${REVIEW_GATE_CONF:-$here/..}
BASE=${BASE:-origin/main}
GOLANGCI_LINT=${GOLANGCI_LINT:-golangci-lint}
SQUAWK=${SQUAWK:-squawk}
ACTIONLINT=${ACTIONLINT:-actionlint}
ZIZMOR=${ZIZMOR:-zizmor}
OXLINT=${OXLINT:-oxlint}
TSGOLINT=${TSGOLINT:-tsgolint}
migrations=internal/store/migrations

if ! command -v jq >/dev/null 2>&1; then
	echo 'review-gate: error: jq not found'
	exit 2
fi

work=$(mktemp -d) || {
	echo 'review-gate: error: mktemp failed'
	exit 2
}
trap 'rm -rf "$work"' EXIT
: >"$work/findings"
: >"$work/errors"
: >"$work/ran"
: >"$work/skipped"
base_sha=
head_sha=

# One line of text, at most 400 characters, for an error message.
oneline() { tr '\n\t' '  ' | sed 's/  */ /g; s/^ //; s/ $//' | cut -c1-400; }

error() { # <tool> <message>
	jq -nc --arg t "$1" --arg m "$2" '{tool: $t, message: $m}' >>"$work/errors"
}
ran() { echo "$1" >>"$work/ran"; }
skipped() { # <check> <reason>
	jq -nc --arg c "$1" --arg r "$2" '{check: $c, reason: $r}' >>"$work/skipped"
}

finish() {
	nf=$(wc -l <"$work/findings")
	ne=$(wc -l <"$work/errors")
	if [ "$nf" -eq 0 ] && [ "$ne" -eq 0 ]; then
		ranlist=$(paste -sd, "$work/ran" | sed 's/,/, /g')
		skiplist=$(jq -r '"\(.check) (\(.reason))"' "$work/skipped" | paste -sd, - | sed 's/,/, /g')
		echo "review-gate: clean (base $(echo "$base_sha" | cut -c1-7), head $(echo "$head_sha" | cut -c1-7); ran: ${ranlist:-none}; skipped: ${skiplist:-none})"
		exit 0
	fi
	jq -nc --arg base "$base_sha" --arg head "$head_sha" \
		--rawfile ran "$work/ran" \
		--slurpfile skipped "$work/skipped" \
		--slurpfile findings "$work/findings" \
		--slurpfile errors "$work/errors" \
		'{base: $base, head: $head, ran: ($ran | split("\n") | map(select(. != ""))),
		  skipped: $skipped, findings: $findings, errors: $errors}'
	if [ "$ne" -gt 0 ]; then exit 2; fi
	exit 1
}

# have <tool command>: true if it can be run; otherwise records an error.
have() { # <check> <command>
	if command -v "$2" >/dev/null 2>&1; then return 0; fi
	error "$1" "not found: $2 (make review-gate-tools builds it)"
	return 1
}

top=$(git rev-parse --show-toplevel 2>"$work/git.err") || {
	error git "$(oneline <"$work/git.err")"
	finish
}
cd "$top" || {
	error git "cannot cd to $top"
	finish
}
# The committed HEAD is checked, so local and CI runs see the same files.
if ! git status --porcelain >"$work/status" 2>"$work/git.err"; then
	error git "$(oneline <"$work/git.err")"
	finish
fi
if [ -s "$work/status" ]; then
	error git "uncommitted changes in the working tree; commit or stash them first"
	finish
fi
base_sha=$(git merge-base "$BASE" HEAD 2>"$work/git.err") || {
	error git "no merge base of $BASE and HEAD: $(oneline <"$work/git.err")"
	finish
}
head_sha=$(git rev-parse HEAD)

# changed <filter> <file> [-- <path>]: changed paths, one per line, unquoted.
changed() {
	f=$1 out=$2
	shift 2
	git -c core.quotePath=false diff --name-only --no-renames --diff-filter="$f" \
		"$base_sha" "$head_sha" "$@" >"$out" 2>"$work/git.err" || {
		error git "$(oneline <"$work/git.err")"
		finish
	}
}
changed AMT "$work/present"
changed A "$work/mig-added" -- "$migrations/"
changed MDT "$work/mig-changed" -- "$migrations/"



# --- noctx: packages of added or modified non-test Go files. Go ignores
# testdata and directories starting with "." or "_", so the gate does too.
grep '\.go$' "$work/present" | grep -v '_test\.go$' |
	grep -Ev '(^|/)(testdata|vendor|[._][^/]*)/' |
	while IFS= read -r f; do dirname "$f"; done | sort -u |
	while IFS= read -r d; do [ -d "$d" ] && echo "./$d"; done >"$work/go-pkgs"
if [ ! -s "$work/go-pkgs" ]; then
	skipped noctx "no changed non-test Go files"
elif have golangci-lint "$GOLANGCI_LINT"; then
	ran noctx
	set --
	while IFS= read -r p; do set -- "$@" "$p"; done <"$work/go-pkgs"
	rc=0
	"$GOLANGCI_LINT" run -c "$conf/tools/review-gate/golangci.yml" \
		--new-from-merge-base="$base_sha" --uniq-by-line=false \
		--max-issues-per-linter=0 --max-same-issues=0 --issues-exit-code=0 \
		--show-stats=false --output.json.path="$work/gl.json" \
		"$@" >"$work/gl.log" 2>&1 || rc=$?
	if [ "$rc" -ne 0 ]; then
		error golangci-lint "exit $rc: $(oneline <"$work/gl.log")"
	elif ! jq -e 'type == "object"' "$work/gl.json" >/dev/null 2>&1; then
		error golangci-lint "unreadable JSON output: $(oneline <"$work/gl.json" 2>/dev/null)"
	else
		jq -c '(.Issues // []) | .[] | {tool: "golangci-lint", rule: .FromLinter,
			file: .Pos.Filename, line: .Pos.Line, message: .Text}' "$work/gl.json" >>"$work/findings"
	fi
fi

# --- squawk: the Up section of each added .sql migration.
grep '\.sql$' "$work/mig-added" >"$work/sql-added"
if [ ! -s "$work/sql-added" ]; then
	skipped squawk "no new migrations"
elif have squawk "$SQUAWK"; then
	ran squawk
	while IFS= read -r f; do
		tx=--assume-in-transaction
		if grep -q '^-- +goose NO TRANSACTION' "$f"; then tx=--no-assume-in-transaction; fi
		awk '/^-- \+goose Down/ { exit } { print }' "$f" >"$work/up.sql"
		rc=0
		"$SQUAWK" -c "$conf/.squawk.toml" --reporter json --stdin-filepath "$f" "$tx" \
			<"$work/up.sql" >"$work/sq.json" 2>"$work/sq.err" || rc=$?
		# 0 clean, 1 findings; anything else is a failure of squawk.
		if [ "$rc" -gt 1 ]; then
			error squawk "$f: exit $rc: $(oneline <"$work/sq.err")"
		elif ! jq -e 'type == "array"' "$work/sq.json" >/dev/null 2>&1; then
			error squawk "$f: unreadable JSON output: $(oneline <"$work/sq.json")"
		else
			# squawk counts lines from 0; the Up section starts at line 1.
			jq -c --arg f "$f" '.[] | {tool: "squawk", rule: .rule_name, file: $f,
				line: (.line + 1), message: .message}' "$work/sq.json" >>"$work/findings"
		fi
	done <"$work/sql-added"
fi

# --- migration-changed: released migrations are never edited.
if [ ! -s "$work/mig-changed" ] && [ ! -s "$work/mig-added" ]; then
	skipped migration-changed "no migration changes"
else
	ran migration-changed
	git -c core.quotePath=false diff --name-status --no-renames --diff-filter=MDT \
		"$base_sha" "$head_sha" -- "$migrations/" |
		jq -Rc 'split("\t") | {tool: "review-gate", rule: "migration-changed",
			file: .[1], line: null,
			message: ("existing migration " + (if .[0] == "D" then "deleted" else "modified" end)
				+ "; add a new migration instead")}' >>"$work/findings"
fi

# --- actionlint: added or modified workflows.
grep -E '^\.github/workflows/[^/]+\.ya?ml$' "$work/present" >"$work/workflows"
if [ ! -s "$work/workflows" ]; then
	skipped actionlint "no workflow changes"
elif have actionlint "$ACTIONLINT"; then
	ran actionlint
	set --
	while IFS= read -r p; do set -- "$@" "$p"; done <"$work/workflows"
	rc=0
	# No shellcheck/pyflakes: the result must not depend on what is installed.
	"$ACTIONLINT" -shellcheck= -pyflakes= -no-color -format '{{json .}}' \
		"$@" >"$work/al.json" 2>"$work/al.err" || rc=$?
	if [ "$rc" -gt 1 ]; then
		error actionlint "exit $rc: $(oneline <"$work/al.err") $(oneline <"$work/al.json")"
	elif ! jq -e 'type == "array"' "$work/al.json" >/dev/null 2>&1; then
		error actionlint "unreadable JSON output: $(oneline <"$work/al.json")"
	else
		jq -c '.[] | {tool: "actionlint", rule: .kind, file: .filepath, line: .line,
			message: .message}' "$work/al.json" >>"$work/findings"
	fi
fi

# --- zizmor: the same workflows. Offline (no GitHub API), medium severity and
# up. Exit 0 is clean and 10-14 are findings (by severity); anything else is a
# failure of zizmor. A zizmor.yml in the repository is honored.
if [ ! -s "$work/workflows" ]; then
	skipped zizmor "no workflow changes"
elif have zizmor "$ZIZMOR"; then
	ran zizmor
	set --
	while IFS= read -r p; do set -- "$@" "$p"; done <"$work/workflows"
	rc=0
	"$ZIZMOR" --offline --min-severity medium --format json --no-progress \
		"$@" >"$work/zz.json" 2>"$work/zz.err" || rc=$?
	if [ "$rc" -ne 0 ] && { [ "$rc" -lt 10 ] || [ "$rc" -gt 14 ]; }; then
		error zizmor "exit $rc: $(oneline <"$work/zz.err") $(oneline <"$work/zz.json")"
	elif ! jq -e 'type == "array"' "$work/zz.json" >/dev/null 2>&1; then
		error zizmor "unreadable JSON output: $(oneline <"$work/zz.json")"
	else
		# zizmor counts rows from 0; the primary location is the finding's place.
		jq -c '.[] | select(.ignored | not) | . as $f
			| ([$f.locations[] | select(.symbolic.kind == "Primary")][0] // $f.locations[0]) as $l
			| {tool: "zizmor", rule: $f.ident, file: $l.symbolic.key.Local.verbatim_path,
			   line: ($l.concrete.location.start_point.row + 1), message: $f.desc}' \
			"$work/zz.json" >>"$work/findings"
	fi
fi

# --- oxlint: added or modified web/src TypeScript files, type-aware, three
# rules (tools/review-gate/oxlintrc.json). web/src has hits from before the
# gate, so only findings on lines the branch adds are reported (the same
# scope as noctx). oxlint exits 1 for findings and for its own failures alike:
# a failure is anything but a clean JSON report on stdout and an empty stderr.
grep -E '^web/src/.*\.(ts|tsx)$' "$work/present" >"$work/web-files"
if [ ! -s "$work/web-files" ]; then
	skipped oxlint "no changed web/src TypeScript files"
elif [ ! -d web/node_modules ]; then
	if [ "${REVIEW_GATE_REQUIRE_WEB:-}" = 1 ]; then
		error oxlint "web/node_modules is missing (pnpm install in web/)"
	else
		skipped oxlint "web deps not installed; pnpm install in web/ to run it"
	fi
elif have oxlint "$OXLINT" && have tsgolint "$TSGOLINT"; then
	ran oxlint
	# Lines added per file: "<path> <first> <last>".
	git -c core.quotePath=false diff -U0 --no-renames --diff-filter=AMT "$base_sha" "$head_sha" -- web/src |
		awk '/^\+\+\+ b\// { f = substr($0, 7); next }
			/^@@ / { split($3, a, ","); n = (a[2] == "" ? 1 : a[2]);
				if (n > 0) print f, substr(a[1], 2), substr(a[1], 2) + n - 1 }' >"$work/web-added"
	set --
	while IFS= read -r p; do set -- "$@" "${p#web/}"; done <"$work/web-files"
	rc=0
	(
		cd web &&
			OXLINT_TSGOLINT_PATH=$TSGOLINT "$OXLINT" -c "$conf/tools/review-gate/oxlintrc.json" \
				--type-aware --tsconfig tsconfig.json -f json "$@"
	) >"$work/ox.json" 2>"$work/ox.err" || rc=$?
	if [ "$rc" -gt 1 ]; then
		error oxlint "exit $rc: $(oneline <"$work/ox.err") $(oneline <"$work/ox.json")"
	elif [ -s "$work/ox.err" ]; then
		error oxlint "exit $rc, stderr: $(oneline <"$work/ox.err")"
	elif ! jq -e 'type == "object" and (.diagnostics | type == "array")' "$work/ox.json" >/dev/null 2>&1; then
		error oxlint "exit $rc, unreadable output: $(oneline <"$work/ox.json")"
	elif [ "$rc" -eq 1 ] && jq -e '.diagnostics | length == 0' "$work/ox.json" >/dev/null; then
		error oxlint "exit 1 without diagnostics: $(oneline <"$work/ox.json")"
	else
		# "typescript(no-floating-promises)" is reported as typescript/no-floating-promises;
		# the first label is the primary location, relative to web/.
		jq -c --rawfile added "$work/web-added" '
			($added | split("\n") | map(select(. != "") | split(" ") | {f: .[0], a: (.[1] | tonumber), b: (.[2] | tonumber)})) as $r
			| .diagnostics[] | ("web/" + .filename) as $f | .labels[0].span.line as $l
			| select(any($r[]; .f == $f and .a <= $l and $l <= .b))
			| {tool: "oxlint", rule: (.code | sub("\\((?<r>.*)\\)$"; "/\(.r)")), file: $f, line: $l, message: .message}
			' "$work/ox.json" >>"$work/findings"
	fi
fi

finish
