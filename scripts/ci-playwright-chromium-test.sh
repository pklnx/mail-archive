#!/bin/sh
# Tests scripts/ci-playwright-chromium.sh offline: stub pnpm and ldd on
# PATH and a fake browser directory, no download and no apt. The stub's dry
# run lists FFmpeg first, then the full Chromium, then the headless shell, so
# taking the first "Install location" would fail here for either binary.
# TEST_SHELL runs the script with another shell (default sh).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=$here/ci-playwright-chromium.sh
shell=${TEST_SHELL:-sh}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

mkdir -p "$tmp/bin"
# pnpm exec playwright …: notes each call; the dry run prints the blocks
# (DRY=nofull leaves out the full Chromium's).
cat >"$tmp/bin/pnpm" <<'STUB'
#!/bin/sh
echo "$*" >>"$CASE/calls"
case "$*" in
"exec playwright install --dry-run chromium")
	cat <<OUT
FFmpeg (playwright ffmpeg v1013)
  Install location:    $CASE/browsers/ffmpeg-1013
  Download url:        https://cdn.example/ffmpeg-linux.zip

OUT
	if [ "$DRY" != nofull ]; then
		cat <<OUT
Chrome for Testing 156.0.8078.4 (playwright chromium v1248)
  Install location:    $CASE/browsers/chromium-1248
  Download url:        https://cdn.example/chrome-linux64.zip

OUT
	fi
	cat <<OUT
Chrome Headless Shell 156.0.8078.4 (playwright chromium-headless-shell v1248)
  Install location:    $CASE/browsers/chromium_headless_shell-1248
  Download url:        https://cdn.example/chrome-headless-shell-linux64.zip

OUT
	;;
"exec playwright install chromium") ;;
"exec playwright install-deps chromium") : >"$CASE/deps-installed" ;;
*)
	echo "pnpm stub: unexpected call: $*" >&2
	exit 2
	;;
esac
STUB
# ldd: notes each binary; LDD says what it finds. ok: everything; missing:
# one library until install-deps ran; still: one library even after it;
# fail: not dynamic.
cat >"$tmp/bin/ldd" <<'STUB'
#!/bin/sh
case $1 in
*/chromium_headless_shell-1248/* | */chromium-1248/*) echo "$1" >>"$CASE/ldd" ;;
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
STUB
chmod +x "$tmp/bin/pnpm" "$tmp/bin/ldd"

shell_bin=chrome-headless-shell-linux64/chrome-headless-shell
full_bin=chrome-linux64/chrome

# run <name> <ldd mode> <shell binary below its revision's directory, or "">
# <full binary below its revision's directory, or ""> <dry run> <want exit>
# <want install-deps calls> <want binaries ldd read> <text the output must have>
run() {
	name=$1 mode=$2 sbin=$3 fbin=$4 dry=$5 want=$6 deps=$7 read=$8 text=$9
	CASE=$tmp/$name
	shell_dir=$CASE/browsers/chromium_headless_shell-1248
	full_dir=$CASE/browsers/chromium-1248
	mkdir -p "$CASE/browsers/ffmpeg-1013" "$shell_dir" "$full_dir"
	# Binaries in the FFmpeg directory too: only the blocks' own may be used.
	: >"$CASE/browsers/ffmpeg-1013/chrome-headless-shell-linux64"
	mkdir -p "$CASE/browsers/ffmpeg-1013/chrome-linux64"
	: >"$CASE/browsers/ffmpeg-1013/chrome-linux64/chrome"
	if [ -n "$sbin" ]; then
		mkdir -p "$(dirname "$shell_dir/$sbin")"
		: >"$shell_dir/$sbin"
	fi
	if [ -n "$fbin" ]; then
		mkdir -p "$(dirname "$full_dir/$fbin")"
		: >"$full_dir/$fbin"
	fi
	: >"$CASE/calls"
	: >"$CASE/ldd"
	rc=0
	CASE=$CASE LDD=$mode DRY=$dry PATH="$tmp/bin:$PATH" "$shell" "$script" >"$CASE/out" 2>&1 || rc=$?
	got=$(grep -c '^exec playwright install-deps chromium$' "$CASE/calls" || true)
	problem=
	[ "$rc" = "$want" ] || problem="exit $rc, want $want"
	[ "$got" = "$deps" ] || problem="$problem; install-deps calls $got, want $deps"
	case $read in
	both)
		grep -qxF "$shell_dir/$sbin" "$CASE/ldd" || problem="$problem; ldd did not read the headless shell"
		grep -qxF "$full_dir/$fbin" "$CASE/ldd" || problem="$problem; ldd did not read the full Chromium"
		;;
	none) [ ! -s "$CASE/ldd" ] || problem="$problem; ldd ran" ;;
	esac
	grep -qF -- "$text" "$CASE/out" || problem="$problem; output lacks: $text"
	if [ -n "$problem" ]; then
		echo "FAIL $name: $problem" >&2
		sed 's/^/    /' "$CASE/out" >&2
		fail=1
	else
		echo "ok   $name"
	fi
}

run all-found ok "$shell_bin" "$full_bin" all 0 0 both "Chromium libraries present, no apt step"
run old-layout ok chrome-linux/headless_shell chrome-linux/chrome all 0 0 both "Chromium libraries present, no apt step"
run fallback missing "$shell_bin" "$full_bin" all 0 1 both "::warning title=Chromium libraries missing::"
run fallback-installs missing "$shell_bin" "$full_bin" all 0 1 both "Chromium libraries installed with apt"
run still-missing still "$shell_bin" "$full_bin" all 1 1 both "error: Chromium libraries still missing after install-deps"
run no-shell-binary ok "" "$full_bin" all 1 0 none "error: no chromium-headless-shell binary in"
run no-full-binary ok "$shell_bin" "" all 1 0 none "error: no full Chromium binary in"
run no-full-block ok "$shell_bin" "$full_bin" nofull 1 0 none "error: the dry run names no install location for full Chromium"
run ldd-fails fail "$shell_bin" "$full_bin" all 1 0 any "error: ldd cannot read"

exit "$fail"
