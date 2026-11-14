#!/bin/sh
# Fails when an address from before the move to keepia (#585) appears in the
# repository outside the allowlist below: the repository
# github.com/pklnx/mail-archive (also as the slug pklnx/mail-archive), the
# image ghcr.io/pklnx/mail-archive and the docs site
# pklnx.github.io/mail-archive. Tracked and untracked (not ignored) text
# files count; case is ignored.
#
# Usage: scripts/old-names.sh [DIR]
#   DIR             the repository to check; default: this script's repository
#   OLD_NAMES_ALLOW a file with another allowlist, in the format below (tests)
#
# An allowlist line is PATH, a tab, and TEXT: a hit is allowed if its file is
# PATH (or lies under PATH when PATH ends with /) and the line contains TEXT
# literally; TEXT * allows every line. An entry that allows no hit fails the
# check too, so the list only shrinks.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
dir=${1:-$here/..}
cd "$dir"

allow=$(mktemp)
trap 'rm -f "$allow"' EXIT
if [ -n "${OLD_NAMES_ALLOW:-}" ]; then
	cat "$OLD_NAMES_ALLOW" > "$allow"
else
	# Historic text, and the check itself.
	cat > "$allow" << 'EOF'
scripts/old-names.sh	*
scripts/old-names-test.sh	*
scripts/release-notes.sh	**First release with semantic versioning.**
docs/guide/operations.md	1.6.1 were published as `ghcr.io/pklnx/mail-archive`
.claude/HANDOVER.md	*
.claude/	*
EOF
	# The last line covers the process files (.claude/), which change after
	# the transfer in part 4 of #585 (#592); that part removes it.
fi

hits=$(git grep --untracked -nIiE -e 'pklnx/mail-archive' -e 'pklnx\.github\.io/mail-archive' -- .) || {
	rc=$?
	[ "$rc" -eq 1 ] || exit "$rc"
	hits=
}

printf '%s\n' "$hits" | awk -v allowfile="$allow" '
BEGIN {
	tab = sprintf("\t")
	while ((getline line < allowfile) > 0) {
		if (line == "" || line ~ /^#/) continue
		i = index(line, tab)
		if (i == 0) { print "old-names: allowlist line without a tab: " line; bad = 1; continue }
		n++
		path[n] = substr(line, 1, i - 1)
		text[n] = substr(line, i + 1)
		entry[n] = line
		used[n] = 0
	}
}
$0 != "" {
	# path:line:content; paths in this repository hold no colon.
	p = $0; sub(/:.*/, "", p)
	content = $0; sub(/^[^:]*:[0-9]+:/, "", content)
	ok = 0
	for (k = 1; k <= n; k++) {
		inpath = (p == path[k]) || (path[k] ~ /\/$/ && index(p, path[k]) == 1)
		if (inpath && (text[k] == "*" || index(content, text[k]) > 0)) { used[k] = 1; ok = 1 }
	}
	if (!ok) { print "old address: " $0; bad = 1 }
}
END {
	for (k = 1; k <= n; k++) if (!used[k]) { print "old-names: allowlist entry allows nothing, remove it: " entry[k]; bad = 1 }
	if (bad) {
		print "old-names: use github.com/keepia/archive, ghcr.io/keepia/archive or keepia.github.io/archive (scripts/old-names.sh)"
		exit 1
	}
}
'
