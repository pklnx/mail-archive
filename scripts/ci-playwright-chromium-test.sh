#!/bin/sh
# Tests scripts/ci-playwright-chromium.sh offline: stub pnpm and ldd on
# PATH and a fake browser directory, no download and no apt. The stub's dry
# run lists FFmpeg before the headless shell, so taking the first "Install
# location" would fail here. TEST_SHELL runs the script with another shell
# (default sh).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=$here/ci-playwright-chromium.sh
shell=${TEST_SHELL:-sh}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

mkdir -p "$tmp/bin"
# pnpm exec playwright …: notes each call; the dry run prints both blocks.
cat >"$tmp/bin/pnpm" <<'EOF'
#!/bin/sh
echo "$*" >>"$CASE/calls"
case "$*" in
"exec playwright install --dry-run --only-shell chromium")
	cat <<OUT
FFmpeg (playwright ffmpeg v1013)
  Install location:    $CASE/browsers/ffmpeg-1013
  Download url:        https://cdn.example/ffmpeg-linux.zip

Chrome Headless Shell 156.0.8078.4 (playwright chromium-headless-shell v1248)
  Install location:    $CASE/browsers/chromium_headless_shell-1248
  Download url:        https://cdn.example/chrome-headless-shell-linux64.zip

OUT
	;;
"exec playwright install --only-shell chromium") ;;
"exec playwright install-deps chromium") : >"$CASE/deps-installed" ;;
*)
	echo "pnpm stub: unexpected call: $*" >&2
	exit 2
	;;
esac
EOF
# ldd: LDD says what it finds. ok: everything; missing: one library until
# install-deps ran; still: one library even after it; fail: not dynamic.
cat >"$tmp/bin/ldd" <<'EOF'
#!/bin/sh
case $1 in
*/chromium_headless_shell-1248/*) ;;
*)
	echo "ldd stub: wrong binary: $1" >&2
	exit 2
	;;
esac
case $LDD in
fail)
	echo "	not a dynamic executable"
	exit 1
	;;
still | missing)
	if [ "$LDD" = still ] || [ ! -e "$CASE/deps-installed" ]; then
		echo "	libxkbcommon.so.0 => not found"
	fi
	;;
esac
echo "	libnss3.so => /usr/lib/x86_64-linux-gnu/libnss3.so (0x00007f0000000000)"
echo "	libc.so.6 => /usr/lib/x86_64-linux-gnu/libc.so.6 (0x00007f0000000000)"
EOF
chmod +x "$tmp/bin/pnpm" "$tmp/bin/ldd"

# run <name> <ldd mode> <binary path below the revision's directory, or "">
# <want exit> <want install-deps calls> <text the output must have>
run() {
	name=$1 mode=$2 binary=$3 want=$4 deps=$5 text=$6
	CASE=$tmp/$name
	mkdir -p "$CASE/browsers/ffmpeg-1013" "$CASE/browsers/chromium_headless_shell-1248"
	# A binary in the FFmpeg directory too: only the shell's may be used.
	: >"$CASE/browsers/ffmpeg-1013/chrome-headless-shell-linux64"
	if [ -n "$binary" ]; then
		mkdir -p "$(dirname "$CASE/browsers/chromium_headless_shell-1248/$binary")"
		: >"$CASE/browsers/chromium_headless_shell-1248/$binary"
	fi
	: >"$CASE/calls"
	rc=0
	CASE=$CASE LDD=$mode PATH="$tmp/bin:$PATH" "$shell" "$script" >"$CASE/out" 2>&1 || rc=$?
	got=$(grep -c '^exec playwright install-deps chromium$' "$CASE/calls" || true)
	problem=
	[ "$rc" = "$want" ] || problem="exit $rc, want $want"
	[ "$got" = "$deps" ] || problem="$problem; install-deps calls $got, want $deps"
	grep -qF -- "$text" "$CASE/out" || problem="$problem; output lacks: $text"
	if [ -n "$problem" ]; then
		echo "FAIL $name: $problem" >&2
		sed 's/^/    /' "$CASE/out" >&2
		fail=1
	else
		echo "ok   $name"
	fi
}

shell_bin=chrome-headless-shell-linux64/chrome-headless-shell
run all-found ok "$shell_bin" 0 0 "Chromium libraries present, no apt step"
run old-layout ok chrome-linux/headless_shell 0 0 "Chromium libraries present, no apt step"
run fallback missing "$shell_bin" 0 1 "::warning title=Chromium libraries missing::"
run fallback-installs missing "$shell_bin" 0 1 "Chromium libraries installed with apt"
run still-missing still "$shell_bin" 1 1 "error: Chromium libraries still missing after install-deps"
run no-binary ok "" 1 0 "error: no headless Chromium binary in"
run ldd-fails fail "$shell_bin" 1 0 "error: ldd cannot read"

exit "$fail"
