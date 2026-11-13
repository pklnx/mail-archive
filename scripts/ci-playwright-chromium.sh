#!/bin/sh
# Installs the Chromium of the pinned Playwright for the web-e2e job, both
# the headless shell (the suite) and the full Chromium (web/e2e/hints.spec.ts:
# the headless shell sends no resource hint at all), normally without apt.
# Run it from web/ (where @playwright/test is).
#
# `playwright install --with-deps` runs an apt step that took 8-13 minutes
# in mail-archive (#216 there) when the Ubuntu mirror was slow, although
# GitHub's runner image already has Chromium's shared libraries (it ships
# Google Chrome); apt only added fonts the tests do not need. So:
#
# 1. Download the browsers only (`install chromium` fetches both; a few
#    seconds from Playwright's CDN).
# 2. Find each binary of the pinned revision: the install location the dry
#    run prints in its block (the block of chromium-headless-shell, and the
#    one of chromium itself; it lists FFmpeg too), then the binary's known
#    names there (they changed across Playwright versions).
# 3. Check their libraries with ldd. ldd failing (a missing or not dynamic
#    file) is an error, not "nothing missing".
# 4. Libraries missing: a ::warning:: on the run, then the old apt path
#    (playwright install-deps, with sudo as --with-deps uses it) and the
#    check again; still missing is an error.
#
# ldd does not see libraries loaded with dlopen (NSS modules and the like).
# Playwright checks the host itself when it launches the browser and names
# what is missing: that is the second net.
set -eu

pnpm exec playwright install chromium

dry=$(pnpm exec playwright install --dry-run chromium)

# binary <block header text> <what> <names...> prints the first of the names
# that exists in the install location of the dry run's block whose header
# line holds the text, or fails.
binary() {
	header=$1 what=$2
	shift 2
	dir=$(printf '%s\n' "$dry" | awk -v want="$header" '
		/^[^ \t]/ { block = (index($0, want) > 0) }
		block && /^[ \t]+Install location:/ { sub(/^[ \t]+Install location:[ \t]*/, ""); print; exit }
	')
	if [ -z "$dir" ]; then
		echo "error: the dry run names no install location for $what" >&2
		exit 1
	fi
	for name in "$@"; do
		if [ -f "$dir/$name" ]; then
			printf '%s\n' "$dir/$name"
			return
		fi
	done
	echo "error: no $what binary in $dir" >&2
	exit 1
}

shell=$(binary "(playwright chromium-headless-shell v" chromium-headless-shell \
	chrome-headless-shell-linux64/chrome-headless-shell chrome-linux/headless_shell)
full=$(binary "(playwright chromium v" "full Chromium" chrome-linux64/chrome chrome-linux/chrome)

# check sets missing to ldd's "not found" lines of both binaries (empty:
# complete).
check() {
	missing=
	for bin in "$shell" "$full"; do
		if ! out=$(ldd "$bin" 2>&1); then
			printf 'error: ldd cannot read %s:\n%s\n' "$bin" "$out" >&2
			exit 1
		fi
		lines=$(printf '%s\n' "$out" | grep 'not found' || true)
		if [ -n "$lines" ]; then
			missing=${missing:+$missing
}$lines
		fi
	done
}

check
if [ -z "$missing" ]; then
	echo "Chromium libraries present, no apt step ($shell, $full)"
	exit 0
fi

printf 'Chromium libraries missing:\n%s\n' "$missing"
echo "::warning title=Chromium libraries missing::The runner image lacks libraries of Chromium; installing them with apt (playwright install-deps), which can take minutes."
pnpm exec playwright install-deps chromium
check
if [ -n "$missing" ]; then
	printf 'error: Chromium libraries still missing after install-deps:\n%s\n' "$missing" >&2
	exit 1
fi
echo "Chromium libraries installed with apt"
