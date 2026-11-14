#!/bin/sh
# Checks that IMAGE:VERSION can be pulled without credentials, the way an
# operator's `docker compose pull` does: an anonymous token from the
# registry's token endpoint, then the manifest. The release workflow runs it
# after "Build and push" and before "Create the release", because a package
# that a release creates for the first time on ghcr.io (as
# ghcr.io/keepia/archive with 1.7.0) starts private: without this check the
# release would be published with an image nobody can pull.
#
# Usage: scripts/release-image-public.sh IMAGE VERSION
#   IMAGE    REGISTRY/REPOSITORY, for example ghcr.io/keepia/archive
#   VERSION  X.Y.Z
# Exits 0 if the manifest answers 200, 1 if not (the message says why), 2 on
# wrong arguments.
set -eu

if [ $# -ne 2 ]; then
	echo "usage: release-image-public.sh IMAGE VERSION" >&2
	exit 2
fi
image=$1 version=$2
case $image in
*[!a-z0-9./_-]* | */ | /* | *//* | [!a-z0-9]*) echo "not an image name: $image" >&2; exit 2 ;;
*/*) ;;
*) echo "not an image name: $image" >&2; exit 2 ;;
esac
if ! printf '%s\n' "$version" | grep -qxE '[0-9]+\.[0-9]+\.[0-9]+'; then
	echo "not a release version: $version" >&2
	exit 2
fi
registry=${image%%/*} repo=${image#*/}

# Transient errors (timeouts, 5xx, 429) are retried; a refusal is not.
curl_opts="--silent --show-error --retry 3 --max-time 30"

# A private or missing package answers the anonymous token request with 401
# or 403; ghcr.io hands out a token for a public one.
# shellcheck disable=SC2086 # curl_opts is a list of options
if ! body=$(curl $curl_opts --fail "https://$registry/token?scope=repository:$repo:pull&service=$registry" 2>&1); then
	echo "the package is not public: no anonymous pull token for $image ($body)" >&2
	exit 1
fi
token=$(printf '%s' "$body" | sed -n 's/.*"token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
if [ -z "$token" ]; then
	echo "the package is not public: the token endpoint of $registry answered without a token" >&2
	exit 1
fi

# The release pushes an image index (two platforms, provenance, SBOM); the
# other types are accepted so that a single-platform image would pass too.
accept="application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json"
# shellcheck disable=SC2086
status=$(curl $curl_opts --head --output /dev/null --write-out '%{http_code}' \
	--header "Authorization: Bearer $token" --header "Accept: $accept" \
	"https://$registry/v2/$repo/manifests/$version") || true
[ -n "$status" ] || status=none
if [ "$status" != 200 ]; then
	echo "the package is not public, or the tag is missing: anonymous pull of $image:$version answered HTTP $status" >&2
	exit 1
fi
echo "$image:$version can be pulled without credentials"
