#!/bin/sh
# Reads changed paths on stdin, one per line. Exits 0 only if there is at
# least one path and every path is documentation: docs/*, .claude/* or *.md
# (a case pattern, so `*` also matches across `/`). An empty list or any
# other path exits 1, so the caller fails closed and runs the e2e tests.
set -eu

n=0
while IFS= read -r path; do
	[ -n "$path" ] || continue
	case $path in
	docs/* | .claude/* | *.md) n=$((n + 1)) ;;
	*) exit 1 ;;
	esac
done
[ "$n" -gt 0 ]
