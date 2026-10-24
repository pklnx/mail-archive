#!/bin/sh
# Tests scripts/ci-docs-only.sh. Offline: path lists on stdin, plus temp git
# repos that run the same `git diff` command as the CI `changes` job.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=$here/ci-docs-only.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

report() { # <name> <rc> <want>
	if [ "$2" != "$3" ]; then
		echo "FAIL $1: exit $2, want $3" >&2
		fail=1
	else
		echo "ok   $1"
	fi
}

# expect <name> <0|1> <path list, one per line>
expect() {
	rc=0
	printf '%s' "$3" | sh "$script" || rc=$?
	report "$1" "$rc" "$2"
}

nl='
'
expect docs-only 0 "docs/index.md$nl"
expect docs-non-markdown 0 "docs/public/mail-archive-tile.svg$nl"
expect claude-only 0 ".claude/HANDOVER.md$nl"
expect nested-md 0 "web/README.md$nl"
expect docs-and-md 0 "docs/a.md${nl}README.md$nl"
expect mixed 1 "docs/a.md${nl}internal/x.go$nl"
expect web-code 1 "web/src/app.ts$nl"
expect github-file 1 ".github/workflows/ci.yml$nl"
expect empty 1 ""
expect md-go 1 "internal/notes.md.go$nl"
expect docs-as-file 1 "docs$nl"
expect space-in-name 0 "docs/my file.md$nl"
expect quoted-name 1 '"docs/m\303\274ll.md"'"$nl"

# diffcase <name> <want> <setup commands run in the repo after the base commit>
# Builds a temp repo, then runs the CI pipeline: git diff to a file, then the script.
diffcase() {
	name=$1 want=$2 setup=$3
	repo=$tmp/$name
	mkdir -p "$repo"
	(
		cd "$repo"
		git init -q
		git config user.email t@example.invalid
		git config user.name t
		git config commit.gpgsign false
		mkdir -p internal docs
		printf 'package x\n\nfunc F() {}\n' >internal/x.go
		printf '# docs\n' >docs/index.md
		git add -A
		git commit -q -m base
		git branch -q base
		git checkout -q -b head
		eval "$setup"
		git add -A
		git commit -q --allow-empty -m head
		git -c core.quotePath=false diff --name-only --no-renames base...head >"$tmp/$name.paths"
	)
	rc=0
	sh "$script" <"$tmp/$name.paths" || rc=$?
	report "git-$name" "$rc" "$want"
}

diffcase docs-change 0 'echo more >>docs/index.md; echo x >NOTES.md'
diffcase mixed-change 1 'echo more >>docs/index.md; echo // >>internal/x.go'
diffcase empty-diff 1 ':'
diffcase rename-into-docs 1 'git mv internal/x.go docs/x.go.txt'
diffcase space-in-name 0 'echo hi >"docs/my file.md"'
diffcase non-ascii-name 0 'echo hi >"docs/müll.md"'

exit "$fail"
