#!/bin/sh
# Tests scripts/release-image-public.sh with a fake curl on PATH that plays
# the registry's token endpoint and manifest. No network.
#
# Usage: scripts/release-image-public-test.sh
#   RELEASE_IMAGE_PUBLIC=path  test another copy of the script (counter-proofs)
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=${RELEASE_IMAGE_PUBLIC:-$here/release-image-public.sh}
script=$(cd "$(dirname "$script")" && pwd)/$(basename "$script")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0 passed=0

# The fake curl appends its arguments, one per line, to $FAKE_LOG, with a
# line "--" after each call. The token request (a URL with /token?) answers
# FAKE_TOKEN_BODY, or fails like curl --fail on a 403 if FAKE_TOKEN_BODY is
# "deny". Any other request prints FAKE_STATUS (as --write-out '%{http_code}'
# would) and exits FAKE_RC.
mkdir "$tmp/bin"
cat > "$tmp/bin/curl" << 'EOF'
#!/bin/sh
token=false
for a in "$@"; do
	printf '%s\n' "$a" >> "$FAKE_LOG"
	case $a in *"/token?"*) token=true ;; esac
done
echo -- >> "$FAKE_LOG"
if $token; then
	if [ "$FAKE_TOKEN_BODY" = deny ]; then
		echo "curl: (56) The requested URL returned error: 403" >&2
		exit 56
	fi
	printf '%s\n' "$FAKE_TOKEN_BODY"
	exit 0
fi
printf '%s' "$FAKE_STATUS"
exit "$FAKE_RC"
EOF
chmod +x "$tmp/bin/curl"

fail() {
	echo "FAIL $1: $2"
	failed=$((failed + 1))
}

# run TOKEN_BODY STATUS RC ARGS...: runs the script; sets rc, out and log.
run() {
	FAKE_TOKEN_BODY=$1 FAKE_STATUS=$2 FAKE_RC=$3
	shift 3
	: > "$tmp/log"
	rc=0
	out=$(FAKE_LOG="$tmp/log" FAKE_TOKEN_BODY=$FAKE_TOKEN_BODY FAKE_STATUS=$FAKE_STATUS FAKE_RC=$FAKE_RC \
		PATH="$tmp/bin:$PATH" sh "$script" "$@" 2>&1) || rc=$?
	log=$(cat "$tmp/log")
}

# expect NAME RC SUBSTRING: the last run exited RC and its output holds SUBSTRING.
expect() {
	if [ "$rc" -ne "$2" ]; then
		fail "$1" "exit $rc, want $2: $out"
	elif ! printf '%s\n' "$out" | grep -qF -- "$3"; then
		fail "$1" "output lacks '$3': $out"
	else
		passed=$((passed + 1))
	fi
}

# has NAME LINE: the fake curl was called with the argument LINE.
has() {
	if printf '%s\n' "$log" | grep -qxF -- "$2"; then
		passed=$((passed + 1))
	else
		fail "$1" "no curl argument '$2' in: $log"
	fi
}

token='{"token":"tok123"}'

run "$token" 200 0 ghcr.io/keepia/archive 1.7.0
expect "public image passes" 0 "ghcr.io/keepia/archive:1.7.0 can be pulled without credentials"
has "token request scope" "https://ghcr.io/token?scope=repository:keepia/archive:pull&service=ghcr.io"
has "manifest URL" "https://ghcr.io/v2/keepia/archive/manifests/1.7.0"
has "manifest uses the anonymous token" "Authorization: Bearer tok123"
# Anonymous: no credentials in any call, and the token request carries no
# Authorization header (it is the first call, up to the first "--").
if printf '%s\n' "$log" | grep -qxE -- '-u|--user|--netrc|-n|--oauth2-bearer'; then
	fail "no credentials" "curl got a credential option: $log"
else
	passed=$((passed + 1))
fi
if printf '%s\n' "$log" | sed '/^--$/q' | grep -q '^Authorization'; then
	fail "token request without Authorization" "$log"
else
	passed=$((passed + 1))
fi

run deny 200 0 ghcr.io/keepia/archive 1.7.0
expect "private package: no token" 1 "the package is not public: no anonymous pull token for ghcr.io/keepia/archive"

run '{"errors":[]}' 200 0 ghcr.io/keepia/archive 1.7.0
expect "token answer without a token" 1 "answered without a token"

run "$token" 401 0 ghcr.io/keepia/archive 1.7.0
expect "manifest 401" 1 "anonymous pull of ghcr.io/keepia/archive:1.7.0 answered HTTP 401"

run "$token" 404 0 ghcr.io/keepia/archive 1.7.0
expect "missing tag (404)" 1 "answered HTTP 404"

run "$token" 000 7 ghcr.io/keepia/archive 1.7.0
expect "registry unreachable" 1 "answered HTTP 000"

run "$token" "" 7 ghcr.io/keepia/archive 1.7.0
expect "no status at all" 1 "answered HTTP none"

run "$token" 200 0 ghcr.io/keepia/archive
expect "one argument" 2 "usage"
run "$token" 200 0 ghcr.io/keepia/archive 1.7
expect "version not X.Y.Z" 2 "not a release version: 1.7"
run "$token" 200 0 archive 1.7.0
expect "image without a repository" 2 "not an image name: archive"
run "$token" 200 0 ghcr.io/Keepia/archive 1.7.0
expect "upper-case image" 2 "not an image name"
run "$token" 200 0 'ghcr.io/keepia/archive&x' 1.7.0
expect "image with a query character" 2 "not an image name"

echo "release-image-public-test: $passed passed, $failed failed"
[ "$failed" -eq 0 ]
