#!/bin/sh
# Installs the headless Chromium of the pinned Playwright for the web-e2e
# job, normally without apt. Run it from web/ (where @playwright/test is).
#
# `playwright install --with-deps` runs an apt step that took 8-13 minutes
# in mail-archive (#216 there) when the Ubuntu mirror was slow, although
# GitHub's runner image already has Chromium's shared libraries (it ships
# Google Chrome); apt only added fonts the tests do not need. So:
#
# 1. Download the browser only (a few seconds from Playwright's CDN).
# 2. Find the binary of the pinned revision: the install location the dry
#    run prints in the block of chromium-headless-shell (it lists FFmpeg
#    too), then the binary's known names there (they changed across
#    Playwright versions).
# 3. Check its libraries with ldd. ldd failing (a missing or not dynamic
#    file) is an error, not "nothing missing".
# 4. Libraries missing: a ::warning:: on the run, then the old apt path
#    (playwright install-deps, with sudo as --with-deps uses it) and the
#    check again; still missing is an error.
#
# ldd does not see libraries loaded with dlopen (NSS modules and the like).
# Playwright checks the host itself when it launches the browser and names
# what is missing: that is the second net.
set -eu

pnpm exec playwright install --only-shell chromium

dir=$(pnpm exec playwright install --dry-run --only-shell chromium | awk '
	/^[^ \t]/ { shell = ($0 ~ /chromium-headless-shell/) }
	shell && /^[ \t]+Install location:/ { sub(/^[ \t]+Install location:[ \t]*/, ""); print; exit }
')
if [ -z "$dir" ]; then
	echo "error: the dry run names no install location for chromium-headless-shell" >&2
	exit 1
fi

bin=
for name in chrome-headless-shell-linux64/chrome-headless-shell chrome-linux/headless_shell; do
	if [ -f "$dir/$name" ]; then
		bin=$dir/$name
		break
	fi
done
if [ -z "$bin" ]; then
	echo "error: no headless Chromium binary in $dir" >&2
	exit 1
fi

# check sets missing to ldd's "not found" lines (empty: complete).
check() {
	if ! out=$(ldd "$bin" 2>&1); then
		printf 'error: ldd cannot read %s:\n%s\n' "$bin" "$out" >&2
		exit 1
	fi
	missing=$(printf '%s\n' "$out" | grep 'not found' || true)
}

check
if [ -z "$missing" ]; then
	echo "Chromium libraries present, no apt step ($bin)"
	exit 0
fi

printf 'Chromium libraries missing:\n%s\n' "$missing"
echo "::warning title=Chromium libraries missing::The runner image lacks libraries of the headless Chromium; installing them with apt (playwright install-deps), which can take minutes."
pnpm exec playwright install-deps chromium
check
if [ -n "$missing" ]; then
	printf 'error: Chromium libraries still missing after install-deps:\n%s\n' "$missing" >&2
	exit 1
fi
echo "Chromium libraries installed with apt"
