#!/bin/sh
# Prints the head of a release's notes, in Markdown: the image line, new
# installation and upgrade. The release workflow passes it to `gh release
# create --notes-file`, which appends the notes generated from the pull
# requests (.github/release.yml). For the first semver release (LAST empty)
# it adds a paragraph for automatic image updaters, since the old `26.10`
# tag stays behind.
#
# Usage: scripts/release-notes.sh VERSION IMAGE LAST
#   VERSION  X.Y.Z, for example 1.0.0
#   IMAGE    for example ghcr.io/pklnx/mail-archive
#   LAST     the last semver version; empty for the first semver release
set -eu

if [ $# -ne 3 ]; then
	echo "usage: release-notes.sh VERSION IMAGE LAST" >&2
	exit 2
fi
version=$1 image=$2 last=$3
minor=${version%.*} major=${version%%.*}

cat << EOF
Image: \`$image:$version\` (linux/amd64, linux/arm64), also tagged \`$minor\`, \`$major\` and \`latest\`.

**New installation:** download \`mail-archive.tar.gz\` below, extract it and follow \`README.txt\` or the [getting started guide](https://pklnx.github.io/mail-archive/guide/getting-started).

**Upgrade:** back up, extract \`mail-archive.tar.gz\` in the directory above your \`mail-archive/\` directory (it keeps \`.env\` and \`data/\`), then \`./ma migrate\` and \`docker compose up -d web\`. Read the database migrations and breaking changes below first; see [Upgrades](https://pklnx.github.io/mail-archive/guide/operations#upgrades).
EOF

[ -z "$last" ] || exit 0

# Printed only for the first semver release (1.0.0 unless overridden): the
# breaking-change sentence describes that release.
cat << EOF

**First release with semantic versioning.** Earlier releases were named \`YY.MM-N\` (\`26.10-1\` to \`26.10-11\`); their tags and images stay. The image tag \`26.10\` stays on \`26.10-11\` and gets no more updates. If a tool updates the image automatically (Watchtower, Renovate, Diun), switch it once to an exact version, \`$minor\` or \`$major\` (\`$major\` also brings migrations, which need \`./ma migrate\`): on \`26.10\` it gets nothing new, tools that compare tags may rank \`26.10\` above \`$version\`, and on \`latest\` it installs this release without its upgrade steps. This release has a breaking change: behind a reverse proxy that does not connect from loopback (with Docker Compose, a proxy on the host such as Caddy or \`tailscale serve\` connects from the gateway of the Compose network, not from loopback), set \`MAIL_ARCHIVE_TRUSTED_PROXIES\` before updating. See [One-time steps](https://pklnx.github.io/mail-archive/guide/operations#one-time-steps) and [Versions](https://pklnx.github.io/mail-archive/guide/operations#versions).
EOF
