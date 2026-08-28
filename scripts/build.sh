#!/bin/sh
# Build trajector with its version stamped in.
#
# Use this rather than a bare `go build`. The service gates uploads on the
# version a client reports, and a build that reports nothing usable is
# treated as one that cannot be placed — which is the safe answer for a
# binary of unknown provenance, and the wrong answer for a build made from
# a checkout that knows exactly which release it sits on.
#
# `git describe --tags` answers that question. A checkout at or past a tag
# reports a real version; a checkout behind one reports the older version
# it actually is, which is the point — the gate is meant to notice that.
#
# Outside a git checkout (a source tarball) there is nothing to describe.
# Pass the version instead:
#
#   ./scripts/build.sh v0.4.1
#
# The release pipeline stamps the same variable through goreleaser's
# ldflags; this script is the same recipe for everyone else.
set -eu

PKG=github.com/PublicAI01/trajector-cli/internal/cli
OUT=${OUT:-trajector}

if [ $# -ge 1 ]; then
	VERSION=$1
elif VERSION=$(git describe --tags --dirty 2>/dev/null); then
	:
else
	echo "build.sh: no git tags to describe and no version argument given." >&2
	echo "  Building from a tarball? Pass the release version:" >&2
	echo "      ./scripts/build.sh v0.4.1" >&2
	echo "  In a shallow clone? Fetch tags: git fetch --tags --unshallow" >&2
	exit 1
fi

exec go build -trimpath -ldflags "-X ${PKG}.version=${VERSION}" -o "${OUT}" ./cmd/trajector
