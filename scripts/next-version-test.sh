#!/bin/sh
# Tests scripts/next-version.sh in a temporary git repository, with JSON
# fixtures for the GitHub API responses. No network.
#
# Usage: scripts/next-version-test.sh
#   NEXT_VERSION=path   test another copy of the script (for counter-proofs)
#   RELEASE_NOTES=path  the same for scripts/release-notes.sh
set -eu

command -v jq > /dev/null || {
	echo "next-version-test: jq is required" >&2
	exit 1
}
command -v git > /dev/null || {
	echo "next-version-test: git is required" >&2
	exit 1
}

here=$(cd "$(dirname "$0")" && pwd)
script=${NEXT_VERSION:-$here/next-version.sh}
script=$(cd "$(dirname "$script")" && pwd)/$(basename "$script")
notes_script=${RELEASE_NOTES:-$here/release-notes.sh}
notes_script=$(cd "$(dirname "$notes_script")" && pwd)/$(basename "$notes_script")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tab=$(printf '\t')
failed=0 passed=0

# Only the settings below, not the user's git configuration.
GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL GIT_CONFIG_NOSYSTEM

fail() {
	echo "FAIL $1: $2"
	failed=$((failed + 1))
}

# ok NAME WANT INPUT ARGS...: the script prints WANT, nothing on stderr (no
# warning) and exits 0.
ok() {
	name=$1 want=$2 input=$3
	shift 3
	if got=$(printf '%s' "$input" | sh "$script" "$@" 2> "$tmp/err"); then
		if [ "$got" != "$want" ]; then
			fail "$name" "got '$got', want '$want'"
		elif [ -s "$tmp/err" ]; then
			fail "$name" "unexpected stderr: $(cat "$tmp/err")"
		else
			passed=$((passed + 1))
		fi
	else
		fail "$name" "exit $?, want '$want': $(cat "$tmp/err")"
	fi
}

# warns NAME WANT TEXT INPUT ARGS...: the script prints WANT, exits 0 and
# writes a warning that contains TEXT on stderr.
warns() {
	name=$1 want=$2 text=$3 input=$4
	shift 4
	if got=$(printf '%s' "$input" | sh "$script" "$@" 2> "$tmp/err"); then
		if [ "$got" != "$want" ]; then
			fail "$name" "got '$got', want '$want'"
		elif ! grep -q 'warning:' "$tmp/err" || ! grep -qF -- "$text" "$tmp/err"; then
			fail "$name" "stderr '$(cat "$tmp/err")' lacks a warning with '$text'"
		else
			passed=$((passed + 1))
		fi
	else
		fail "$name" "exit $?, want '$want': $(cat "$tmp/err")"
	fi
}

# bad NAME TEXT INPUT ARGS...: the script exits 2 with TEXT in its message
# and prints nothing on stdout.
bad() {
	name=$1 text=$2 input=$3
	shift 3
	set +e
	got=$(printf '%s' "$input" | sh "$script" "$@" 2> "$tmp/err")
	code=$?
	set -e
	if [ "$code" -ne 2 ]; then
		fail "$name" "exit $code, want 2 (stdout '$got')"
	elif [ -n "$got" ]; then
		fail "$name" "printed '$got' on failure"
	elif ! grep -qF -- "$text" "$tmp/err"; then
		fail "$name" "message '$(cat "$tmp/err")' lacks '$text'"
	else
		passed=$((passed + 1))
	fi
}

# contains NAME TEXT OUTPUT / lacks NAME TEXT OUTPUT
contains() {
	case $3 in
	*"$2"*) passed=$((passed + 1)) ;;
	*) fail "$1" "'$2' missing in: $3" ;;
	esac
}
lacks() {
	case $3 in
	*"$2"*) fail "$1" "'$2' found in: $3" ;;
	*) passed=$((passed + 1)) ;;
	esac
}

# row NUMBER TITLE LABEL...: a line as `pr` prints it.
row() {
	r="$1$tab$2"
	shift 2
	for l in "$@"; do
		r="$r$tab$l"
	done
	printf '%s\n' "$r"
}

# --- last and base, in a temporary repository ---------------------------

repo=$tmp/repo
git init -q "$repo"
cd "$repo"
git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false \
	commit -q --allow-empty -m tags

ok "last: no tags" "" "" last
ok "base: no tags" "" "" base

git tag v26.9-1
git tag v26.09-7
git tag v26.10-2
git tag v26.10-11
git tag v26.10-rc
git tag v1.0.0-rc1
git tag v01.0.0
git tag v1.0.0.0
git tag xv2.0.0
ok "last: only old and malformed tags" "" "" last
ok "base: highest vYY.MM-N, numeric N" "v26.10-11" "" base

git tag v1.0.0
ok "last: v1.0.0" "1.0.0" "" last
ok "base: after v1.0.0" "v1.0.0" "" base

git tag v1.2.3
git tag v1.9.0
git tag v1.10.0
ok "last: numeric, not lexical" "1.10.0" "" last
ok "base: highest semver tag" "v1.10.0" "" base

# Only the first old tag: the case of a product with a single vYY.MM-N
# release.
repo1=$tmp/repo1
git init -q "$repo1"
cd "$repo1"
git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false \
	commit -q --allow-empty -m tags
git tag v26.10-1
ok "last: only v26.10-1" "" "" last
ok "base: only v26.10-1" "v26.10-1" "" base

cd "$tmp"

# --- pr, with API response fixtures ---------------------------------------

sha=0123456789abcdef0123456789abcdef01234567
other=fedcba9876543210fedcba9876543210fedcba98

# pull SHA NUMBER MERGED BASE LABELS-JSON [TITLE]: one pull request object.
pull() {
	jq -n --arg sha "$1" --argjson n "$2" --argjson merged "$3" --arg base "$4" \
		--argjson labels "$5" --arg title "${6:-Title $2}" \
		'{number: $n, title: $title, merged_at: $merged, merge_commit_sha: $sha,
		  base: {ref: $base}, labels: [$labels[] | {name: .}]}'
}
merged='"2026-10-23T12:00:00Z"'

ok "pr: merged into main" "$(row 212 "Title 212" docs bug)" \
	"[$(pull $sha 212 "$merged" main '["docs","bug"]')]" pr $sha
ok "pr: no labels" "$(row 7 "Title 7")" \
	"[$(pull $sha 7 "$merged" main '[]')]" pr $sha
ok "pr: not merged" "-$tab$sha$tab" \
	"[$(pull $sha 5 null main '["feature"]')]" pr $sha
ok "pr: base not main" "-$tab$sha$tab" \
	"[$(pull $sha 5 "$merged" release '["feature"]')]" pr $sha
ok "pr: other merge_commit_sha" "-$tab$sha$tab" \
	"[$(pull $other 5 "$merged" main '["feature"]')]" pr $sha
ok "pr: two PRs, one matches" "$(row 9 "Title 9" bug)" \
	"[$(pull $other 8 "$merged" main '["breaking"]'),$(pull $sha 9 "$merged" main '["bug"]')]" pr $sha
ok "pr: empty array" "-$tab$sha$tab" "[]" pr $sha
ok "pr: label with a space" "$(row 3 "Title 3" "good first issue" feature)" \
	"[$(pull $sha 3 "$merged" main '["good first issue","feature"]')]" pr $sha
ok "pr: title with tab, newline and |" "$(row 4 "a b | c d" bug)" \
	"[$(pull $sha 4 "$merged" main '["bug"]' "a${tab}b | c
d")]" pr $sha
bad "pr: empty input" "no API response" "" pr $sha
bad "pr: blank input" "no API response" "
" pr $sha
bad "pr: API error object" "not a JSON array" '{"message":"Not Found"}' pr $sha
bad "pr: invalid JSON" "not a JSON array" '[{"number":' pr $sha
bad "pr: no SHA" "usage" "[]" pr

# --- next -----------------------------------------------------------------

bug=$(row 10 "Fix" bug)
docs=$(row 11 "Docs" docs)
none=$(row 12 "Unlabelled")
feature=$(row 13 "Feature" feature)
breaking=$(row 14 "Breaking" breaking)
migration=$(row 15 "Schema" db-migration)
featured=$(row 16 "Featured" featured)
nonbreaking=$(row 17 "Non-breaking" non-breaking)
spaced=$(row 18 "Spaced" "breaking change")
nopr="-$tab$sha$tab"
nopr2="-$tab$other$tab"
nl='
'

ok "next: bug gives patch" 1.2.4 "$bug$nl" next 1.2.3
ok "next: docs gives patch" 1.2.4 "$docs$nl" next 1.2.3
ok "next: unlabelled gives patch" 1.2.4 "$none$nl" next 1.2.3
ok "next: bug and docs give patch" 1.2.4 "$bug$nl$docs$nl" next 1.2.3
ok "next: feature and bug give minor" 1.3.0 "$feature$nl$bug$nl" next 1.2.3
ok "next: bug and feature give minor" 1.3.0 "$bug$nl$feature$nl" next 1.2.3
ok "next: breaking and feature give major" 2.0.0 "$breaking$nl$feature$nl" next 1.2.3
ok "next: feature and breaking give major" 2.0.0 "$feature$nl$breaking$nl" next 1.2.3
ok "next: featured is not feature" 1.2.4 "$featured$nl" next 1.2.3
ok "next: non-breaking is not breaking" 1.2.4 "$nonbreaking$nl" next 1.2.3
ok "next: 'breaking change' is not breaking" 1.2.4 "$spaced$nl" next 1.2.3
ok "next: db-migration alone gives minor" 1.3.0 "$migration$nl" next 1.2.3
ok "next: bug and db-migration give minor" 1.3.0 "$bug$nl$migration$nl" next 1.2.3
ok "next: db-migration and breaking give major" 2.0.0 "$migration$nl$breaking$nl" next 1.2.3
ok "next: input without trailing newline" 1.3.0 "$bug$nl$feature" next 1.2.3
ok "next: minor resets patch" 1.10.0 "$feature$nl" next 1.9.7
ok "next: major resets minor and patch" 2.0.0 "$breaking$nl" next 1.9.7
ok "next: empty override means none" 1.2.4 "$bug$nl" next 1.2.3 ""
ok "next: empty override means none (minor)" 1.3.0 "$migration$nl" next 1.2.3 ""
bad "next: empty stdin" "nothing merged" "" next 1.2.3
bad "next: blank lines only" "nothing merged" "$nl$nl" next 1.2.3
bad "next: commit without PR" "$sha" "$bug$nl$nopr$nl" next 1.2.3
bad "next: commit without PR asks for the version input" "version input" "$nopr$nl" next 1.2.3
bad "next: two commits without PR name both" "$other" "$nopr$nl$nopr2$nl" next 1.2.3
bad "next: commit without PR, empty override" "$sha" "$nopr$nl" next 1.2.3 ""
bad "next: malformed line" "not a pull request line" "abc${tab}x$nl" next 1.2.3
bad "next: malformed last" "last version" "$bug$nl" next v1.2.3
bad "next: no arguments" "usage" "$bug$nl" next

# First semver release: 1.0.0 whatever the labels.
ok "first: breaking gives 1.0.0" 1.0.0 "$breaking$nl" next ""
ok "first: feature gives 1.0.0" 1.0.0 "$feature$nl" next ""
ok "first: bug gives 1.0.0" 1.0.0 "$bug$nl" next ""
ok "first: no labels give 1.0.0" 1.0.0 "$none$nl" next ""
ok "first: empty override means none" 1.0.0 "$bug$nl" next "" ""
bad "first: empty stdin" "nothing merged" "" next ""
bad "first: empty stdin with override 1.0.0" "nothing merged" "" next "" 1.0.0
bad "first: commit without PR" "$sha" "$nopr$nl" next ""
ok "first: commit without PR with override 1.0.0" 1.0.0 "$nopr$nl" next "" 1.0.0
ok "first: override equal to 1.0.0" 1.0.0 "$bug$nl" next "" 1.0.0

# Override.
bad "override: equal" "not greater" "$bug$nl" next 1.2.3 1.2.3
bad "override: lower" "not greater" "$bug$nl" next 1.2.3 1.1.9
bad "override: lower major" "not greater" "$bug$nl" next 1.2.3 0.9.9
ok "override: numeric patch compare" 1.2.10 "$bug$nl" next 1.2.9 1.2.10
ok "override: numeric minor compare" 1.10.0 "$bug$nl" next 1.9.0 1.10.0
ok "override: numeric major compare" 10.0.0 "$bug$nl" next 9.0.0 10.0.0
warns "override: below the proposal warns" 1.2.4 "lower than the proposal 1.3.0" "$feature$nl" next 1.2.3 1.2.4
warns "override: below a major proposal warns" 1.3.0 "lower than the proposal 2.0.0" "$breaking$nl" next 1.2.3 1.3.0
warns "override: below the proposal, numeric" 1.9.1 "lower than the proposal 1.10.0" "$feature$nl" next 1.9.0 1.9.1
ok "override: equal to the proposal" 1.3.0 "$feature$nl" next 1.2.3 1.3.0
ok "override: above the proposal" 3.0.0 "$bug$nl" next 1.2.3 3.0.0
ok "override: commit without PR, no proposal" 1.2.4 "$feature$nl$nopr$nl" next 1.2.3 1.2.4
bad "override: with v" "not X.Y.Z" "$bug$nl" next 1.2.3 v1.2.4
bad "override: two fields" "not X.Y.Z" "$bug$nl" next 1.2.3 1.2
bad "override: suffix" "not X.Y.Z" "$bug$nl" next 1.2.3 1.2.4-rc1
bad "override: leading zero" "not X.Y.Z" "$bug$nl" next 1.2.3 01.2.4
bad "override: newline" "not X.Y.Z" "$bug$nl" next 1.2.3 "1.2.4${nl}x"
bad "override: empty stdin" "nothing merged" "" next 1.2.3 1.2.4
ok "override: commit without PR" 1.2.4 "$nopr$nl" next 1.2.3 1.2.4
ok "override: commit without PR among PRs" 1.3.0 "$bug$nl$nopr$nl" next 1.2.3 1.3.0
bad "override: first release 0.9.0" "at least 1.0.0" "$bug$nl" next "" 0.9.0
bad "override: first release 0.100.0" "at least 1.0.0" "$bug$nl" next "" 0.100.0
ok "override: first release 2.0.0" 2.0.0 "$bug$nl" next "" 2.0.0

# --- summary --------------------------------------------------------------

summary() {
	printf '%s\n' "$1" | sh "$script" summary
}
s=$(summary "$migration")
contains "summary: db-migration row is minor" "| #15 | Schema | \`db-migration\` | minor, no kind label |" "$s"
lacks "summary: db-migration row never says patch" "patch" "$s"
s=$(summary "$none")
contains "summary: unlabelled row" "| #12 | Unlabelled |  | patch, no kind label |" "$s"
s=$(summary "$(row 20 "Security fix" security)")
contains "summary: security is a kind label" "| #20 | Security fix | \`security\` | patch |" "$s"
s=$(summary "$(row 21 "Both" bug db-migration)")
contains "summary: bug and db-migration" "| #21 | Both | \`bug\`, \`db-migration\` | minor |" "$s"
s=$(summary "$breaking")
contains "summary: breaking" "| #14 | Breaking | \`breaking\` | major |" "$s"
s=$(summary "$feature")
contains "summary: feature" "| #13 | Feature | \`feature\` | minor |" "$s"
s=$(summary "$featured")
contains "summary: featured" "| #16 | Featured | \`featured\` | patch, no kind label |" "$s"
s=$(summary "$nopr")
contains "summary: commit without PR" "| - | $sha | | commit without pull request: set \`version\` |" "$s"
s=$(summary "$(row 22 "a | b" bug)")
contains "summary: | escaped" "| #22 | a \\| b | \`bug\` | patch |" "$s"
s=$(summary "$(row 23 "Spaced" "good first issue" chore)")
contains "summary: label with a space" "| #23 | Spaced | \`good first issue\`, \`chore\` | patch |" "$s"
s=$(summary "")
contains "summary: header" "| PR | Title | Labels | Effect |" "$s"

# --- release-bundle.sh ----------------------------------------------------

for v in 26.10-11 1.0 v1.0.0 01.0.0; do
	if out=$(sh "$here/release-bundle.sh" "$v" "$tmp/dist" 2>&1); then
		fail "release-bundle: rejects $v" "accepted: $out"
	else
		contains "release-bundle: rejects $v" "not a release version" "$out"
	fi
done
if out=$(sh "$here/release-bundle.sh" 99.0.0 "$tmp/dist" 2>&1); then
	passed=$((passed + 1))
	contains "release-bundle: README names the version" "mail-archive 99.0.0" \
		"$(tar -xzOf "$tmp/dist/mail-archive.tar.gz" mail-archive/README.txt)"
else
	fail "release-bundle: accepts 99.0.0" "$out"
fi

# --- release-notes.sh -----------------------------------------------------

if out=$(sh "$notes_script" 1.0.0 ghcr.io/pklnx/mail-archive "" 2>&1); then
	contains "release-notes: first, image line" \
		"\`ghcr.io/pklnx/mail-archive:1.0.0\` (linux/amd64, linux/arm64), also tagged \`1.0\`, \`1\` and \`latest\`." "$out"
	contains "release-notes: first, updater paragraph" "\`26.10\` stays on \`26.10-11\`" "$out"
	contains "release-notes: first, trusted proxies" "MAIL_ARCHIVE_TRUSTED_PROXIES" "$out"
else
	fail "release-notes: first release" "$out"
fi
if out=$(sh "$notes_script" 1.0.1 ghcr.io/pklnx/mail-archive 1.0.0 2>&1); then
	lacks "release-notes: later, no updater paragraph" "26.10" "$out"
	contains "release-notes: later, upgrade paragraph" "./ma migrate" "$out"
else
	fail "release-notes: later release" "$out"
fi
if out=$(sh "$notes_script" 1.0.0 ghcr.io/pklnx/mail-archive 2>&1); then
	fail "release-notes: two arguments" "accepted: $out"
else
	contains "release-notes: two arguments" "usage" "$out"
fi

echo "next-version-test: $passed passed, $failed failed"
[ "$failed" -eq 0 ]
