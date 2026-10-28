#!/bin/sh
# Tests the PNPM_SETUP step of the Makefile (web, web-e2e, docs): a pnpm on PATH
# is used and corepack is never called; without pnpm, `corepack enable` runs.
# Offline: pnpm and corepack are stubs. Needs make and sh.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

mkdir "$tmp/with" "$tmp/without"
log=$tmp/log
cat > "$tmp/with/pnpm" <<EOS
#!/bin/sh
echo "pnpm \$*" >> "$log"
EOS
cat > "$tmp/with/corepack" <<EOS
#!/bin/sh
echo "corepack \$*" >> "$log"
exit 1
EOS
cp "$tmp/with/corepack" "$tmp/without/corepack"
chmod +x "$tmp/with/pnpm" "$tmp/with/corepack" "$tmp/without/corepack"

# A pnpm in the system dirs would break the "no pnpm" case.
if [ -e /usr/bin/pnpm ] || [ -e /bin/pnpm ]; then
	echo "SKIP: a pnpm exists in /usr/bin or /bin, cannot test the no-pnpm case" >&2
	skip_none=1
else
	skip_none=0
fi

run() { # run <pathdir> <target>
	(cd "$repo" && PATH="$1:/usr/bin:/bin" DEMO_DATABASE_URL=postgres://stub/stub \
		make -s "$2") >"$tmp/out" 2>&1
}

for target in web web-e2e docs; do
	: > "$log"
	rc=0
	run "$tmp/with" "$target" || rc=$?
	if [ "$rc" -eq 0 ] && ! grep -q corepack "$log" && grep -q '^pnpm install' "$log"; then
		echo "ok   $target uses the pnpm on PATH"
	else
		echo "FAIL $target (exit $rc)"; cat "$log" "$tmp/out"; fail=1
	fi
done

if [ "$skip_none" -eq 0 ]; then
	: > "$log"
	rc=0
	run "$tmp/without" web || rc=$?
	if [ "$rc" -ne 0 ] && grep -q '^corepack enable' "$log"; then
		echo "ok   no pnpm: falls back to corepack enable"
	else
		echo "FAIL no-pnpm fallback (exit $rc)"; cat "$log" "$tmp/out"; fail=1
	fi
fi
exit "$fail"
