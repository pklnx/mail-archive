#!/bin/sh
# Tests scripts/old-names.sh in temporary git repositories, then runs it on
# this repository. No network.
#
# Usage: scripts/old-names-test.sh
#   OLD_NAMES=path  test another copy of the script (for counter-proofs)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=${OLD_NAMES:-$here/old-names.sh}
script=$(cd "$(dirname "$script")" && pwd)/$(basename "$script")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tab=$(printf '\t')
failed=0 passed=0

GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL GIT_CONFIG_NOSYSTEM

fail() {
	echo "FAIL $1: $2"
	failed=$((failed + 1))
}

# repo NAME: a fresh repository $tmp/NAME with one committed file holding
# only new addresses, and the allowlist $tmp/NAME.allow (empty).
repo() {
	r=$tmp/$1
	mkdir -p "$r/docs" "$r/scripts"
	git -C "$r" init -q
	printf '%s\n' "https://github.com/keepia/archive" "ghcr.io/keepia/archive:1.7.0" \
		"https://keepia.github.io/archive/guide/" "mail-archive.tar.gz" > "$r/docs/new.md"
	git -C "$r" add -A
	git -C "$r" -c user.name=t -c user.email=t@example.invalid commit -q -m init
	: > "$tmp/$1.allow"
}

# check NAME RC SUBSTRING: the script on $tmp/NAME with $tmp/NAME.allow exits
# RC and (if SUBSTRING is not empty) prints SUBSTRING.
check() {
	rc=0
	out=$(OLD_NAMES_ALLOW="$tmp/$1.allow" sh "$script" "$tmp/$1" 2>&1) || rc=$?
	if [ "$rc" -ne "$2" ]; then
		fail "$1" "exit $rc, want $2: $out"
	elif [ -n "$3" ] && ! printf '%s\n' "$out" | grep -qF -- "$3"; then
		fail "$1" "output lacks '$3': $out"
	else
		passed=$((passed + 1))
	fi
}

repo clean
check clean 0 ""

repo github
echo "see https://github.com/pklnx/mail-archive/issues/1" > "$tmp/github/docs/a.md"
check github 1 "old address: docs/a.md:1:see https://github.com/pklnx/mail-archive/issues/1"

repo image
echo "KEEPIA_ARCHIVE_IMAGE=ghcr.io/pklnx/mail-archive:1.6.1" > "$tmp/image/env"
check image 1 "old address: env:1:"

repo docs-site
echo "https://pklnx.github.io/mail-archive/guide/" > "$tmp/docs-site/docs/a.md"
check docs-site 1 "old address: docs/a.md:1:"

repo slug
echo "gh api repos/pklnx/mail-archive/pulls" > "$tmp/slug/scripts/x.sh"
check slug 1 "old address: scripts/x.sh:1:"

repo upper-case
echo "https://GitHub.com/PKLNX/Mail-Archive" > "$tmp/upper-case/docs/a.md"
check upper-case 1 "old address: docs/a.md:1:"

# Committed files count as well as untracked ones.
repo committed
echo "ghcr.io/pklnx/mail-archive" > "$tmp/committed/docs/b.md"
git -C "$tmp/committed" add -A
git -C "$tmp/committed" -c user.name=t -c user.email=t@example.invalid commit -q -m b
check committed 1 "old address: docs/b.md:1:"

# An ignored file does not count.
repo ignored
echo "dist/" > "$tmp/ignored/.gitignore"
mkdir "$tmp/ignored/dist"
echo "ghcr.io/pklnx/mail-archive" > "$tmp/ignored/dist/notes.md"
check ignored 0 ""

# An allowed line passes; another old address in the same file does not.
repo allowed
printf '%s\n' "**First release** see https://pklnx.github.io/mail-archive/x" > "$tmp/allowed/scripts/notes.sh"
printf 'scripts/notes.sh%s**First release**\n' "$tab" > "$tmp/allowed.allow"
check allowed 0 ""
repo allowed-other-line
printf '%s\n' "**First release** see https://pklnx.github.io/mail-archive/x" \
	"Image: ghcr.io/pklnx/mail-archive" > "$tmp/allowed-other-line/scripts/notes.sh"
printf 'scripts/notes.sh%s**First release**\n' "$tab" > "$tmp/allowed-other-line.allow"
check allowed-other-line 1 "old address: scripts/notes.sh:2:Image: ghcr.io/pklnx/mail-archive"

# A directory entry allows every file under it, and only there.
repo directory
mkdir "$tmp/directory/.claude" "$tmp/directory/.claudex"
echo "repos/pklnx/mail-archive" > "$tmp/directory/.claude/ops.md"
echo "repos/pklnx/mail-archive" > "$tmp/directory/.claudex/ops.md"
printf '.claude/%s*\n' "$tab" > "$tmp/directory.allow"
check directory 1 "old address: .claudex/ops.md:1:"
if printf '%s\n' "$out" | grep -qF ".claude/ops.md"; then
	fail "directory: .claude/ops.md allowed" "$out"
else
	passed=$((passed + 1))
fi

# A file entry does not cover another file with the same prefix.
repo file-prefix
echo "ghcr.io/pklnx/mail-archive" > "$tmp/file-prefix/docs/a.md.bak"
printf 'docs/a.md%s*\n' "$tab" > "$tmp/file-prefix.allow"
check file-prefix 1 "old address: docs/a.md.bak:1:"

# An entry that allows nothing fails, so the list only shrinks.
repo stale
printf 'docs/gone.md%s*\n' "$tab" > "$tmp/stale.allow"
check stale 1 "allowlist entry allows nothing, remove it: docs/gone.md"

# This repository, with the script's own allowlist.
rc=0
out=$(sh "$script" "$here/.." 2>&1) || rc=$?
if [ "$rc" -eq 0 ]; then
	passed=$((passed + 1))
else
	fail "this repository" "exit $rc: $out"
fi

echo "old-names-test: $passed passed, $failed failed"
[ "$failed" -eq 0 ]
