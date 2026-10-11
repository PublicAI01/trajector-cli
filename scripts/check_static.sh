#!/bin/sh
# Static checks that CI and local runs share. The Go checks run under the
# toolchain that go.mod names, which is the one CI and releases build
# with; the other tools run from the image pinned on the one FROM line
# of tools/<tool>/Dockerfile, with the repository mounted read-only.
#
#   check_static.sh gofix        go fix has nothing left to rewrite
#   check_static.sh tidy         go.mod and go.sum are tidy
#   check_static.sh govulncheck  no known vulnerability is reachable from
#                                this code; the version is pinned in
#                                tools/govulncheck/go.mod
#   check_static.sh zizmor       GitHub Actions security audit; online
#                                when GH_TOKEN is set, offline otherwise
#   check_static.sh actionlint   workflow syntax, expressions, and the
#                                shell in run: steps
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)

usage() {
	sed -n '7,15p' "$0" >&2
	exit 2
}

# +auto lets a tool whose own go.mod needs a newer Go switch up to it;
# everything else runs under the pinned version.
use_toolchain() {
	toolchain=$(sed -n 's/^toolchain[[:space:]]\{1,\}\([^[:space:]]\{1,\}\).*/\1/p' "$root/go.mod")
	[ -n "$toolchain" ] || {
		echo "check_static: no toolchain line in go.mod" >&2
		exit 2
	}
	GOTOOLCHAIN=$toolchain+auto
	export GOTOOLCHAIN
	cd "$root"
}

image() {
	ref=$(sed -n 's/^FROM[[:space:]]\{1,\}\([^[:space:]]\{1,\}\).*/\1/p' "$root/tools/$1/Dockerfile")
	[ -n "$ref" ] || {
		echo "check_static: no FROM line in tools/$1/Dockerfile" >&2
		exit 2
	}
	echo "$ref"
}

zizmor() {
	if [ -n "${GH_TOKEN:-}" ]; then
		echo "zizmor: online"
		docker run --rm -v "$root:/repo:ro" -w /repo -e GH_TOKEN "$(image zizmor)" --quiet --persona=regular .
	else
		echo "zizmor: offline (no GH_TOKEN): impostor-commit, known-vulnerable-actions, ref-confusion and stale-action-refs do not run"
		docker run --rm -v "$root:/repo:ro" -w /repo "$(image zizmor)" --quiet --persona=regular --offline .
	fi
}

actionlint() {
	docker run --rm -v "$root:/repo:ro" -w /repo "$(image actionlint)" -no-color
}

[ $# = 1 ] || usage
case $1 in
gofix) use_toolchain && go fix -diff ./... ;;
tidy) use_toolchain && go mod tidy -diff ;;
govulncheck) use_toolchain && go tool -modfile=tools/govulncheck/go.mod govulncheck ./... ;;
zizmor) zizmor ;;
actionlint) actionlint ;;
*) usage ;;
esac
