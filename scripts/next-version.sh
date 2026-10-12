#!/bin/sh
# Prints the next release version: YY.MM-N, the year and month (UTC) and a
# build number that starts at 1 each month and counts up from the highest
# tag vYY.MM-N in the repository.
#
# Usage: scripts/next-version.sh [YY.MM]   (YY.MM for tests; default now)
set -eu

month=${1:-$(date -u +%y.%m)}
case $month in
[0-9][0-9].[0-1][0-9]) ;;
*)
	echo "not YY.MM: $month" >&2
	exit 2
	;;
esac
last=$(git tag -l "v$month-*" | sed "s/^v$month-//" | grep -E '^[0-9]+$' | sort -n | tail -1 || true)
echo "$month-$((${last:-0} + 1))"
