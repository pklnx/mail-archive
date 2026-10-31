#!/bin/sh
# Builds the Docker Compose bundle of a release: docker-compose.yml with the
# image of this version instead of a local build, .env.example, the ./ma
# wrapper and a README, in mail-archive/ inside OUTDIR/mail-archive.tar.gz,
# plus OUTDIR/SHA256SUMS.
#
# Usage: scripts/release-bundle.sh VERSION OUTDIR [IMAGE]
#   VERSION  X.Y.Z, for example 1.0.0
#   IMAGE    default ghcr.io/pklnx/mail-archive
set -eu

version=${1:?usage: release-bundle.sh VERSION OUTDIR [IMAGE]}
out=${2:?usage: release-bundle.sh VERSION OUTDIR [IMAGE]}
image=${3:-ghcr.io/pklnx/mail-archive}
# The case rejects newlines, which grep would treat as separate lines.
case $version in
'' | *[!0-9.]*) bad=1 ;;
*) bad= ;;
esac
if [ -n "$bad" ] || ! printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
	echo "not a release version (X.Y.Z): $version" >&2
	exit 2
fi

repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
dir=$stage/mail-archive
mkdir "$dir"

# The services pull the released image; the build sections go. A build
# section is the "build:" line of a service and the lines indented deeper.
{
	echo "# mail-archive $version: https://github.com/pklnx/mail-archive/releases/tag/v$version"
	echo "# Upgrade: extract a newer release's mail-archive.tar.gz in the directory"
	echo "# above this one (it keeps .env and data/), then ./ma migrate and"
	echo "# docker compose up -d web (web worker with the worker profile on)."
	echo "#"
	awk '
		/^    build:/ { skip = 1; next }
		skip && /^      / { next }
		{ skip = 0 }
		{ print }
	' "$repo/docker-compose.yml" |
		sed -e "s|\${MAIL_ARCHIVE_IMAGE:-mail-archive:local}|\${MAIL_ARCHIVE_IMAGE:-$image:$version}|" \
			-e 's| (image built by ./ma)||'
} > "$dir/docker-compose.yml"
if grep -q 'build:' "$dir/docker-compose.yml" || ! grep -q "$image:$version" "$dir/docker-compose.yml"; then
	echo "could not rewrite docker-compose.yml" >&2
	exit 1
fi

cp "$repo/.env.example" "$dir/.env.example"
cp "$repo/ma" "$dir/ma"
chmod 0755 "$dir/ma"
cat > "$dir/README.txt" <<EOF
mail-archive $version

Image:   $image:$version (linux/amd64, linux/arm64)
Docs:    https://pklnx.github.io/mail-archive/
Release: https://github.com/pklnx/mail-archive/releases/tag/v$version

First start:
  cp .env.example .env     # set POSTGRES_PASSWORD and MAIL_ARCHIVE_SECRET_KEY
  ./ma migrate             # also makes data/ writable for the container
  ./ma user add NAME --admin
  docker compose up -d web # http://localhost:8080

Upgrade: back up first, then extract the newer mail-archive.tar.gz in the
directory above this one, so that it replaces these files and keeps .env
and data/:
  ./ma migrate
  docker compose up -d web          # web worker with COMPOSE_PROFILES=worker

Optional worker service for background jobs (attachment text) with its own
memory limit: COMPOSE_PROFILES=worker and WEB_JOB_WORKERS=0 in .env.
EOF

# Numeric owner 0/0, not the build machine's user.
tar -C "$stage" --owner=0 --group=0 --numeric-owner -czf "$out/mail-archive.tar.gz" mail-archive
(cd "$out" && sha256sum mail-archive.tar.gz > SHA256SUMS)
echo "$out/mail-archive.tar.gz"
