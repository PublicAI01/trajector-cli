#!/bin/sh
# Static checks that CI and local runs share. Each tool runs from the
# image pinned on the one FROM line of tools/<tool>/Dockerfile, with the
# repository mounted read-only.
#
#   check_static.sh zizmor       GitHub Actions security audit; online
#                                when GH_TOKEN is set, offline otherwise
#   check_static.sh actionlint   workflow syntax, expressions, and the
#                                shell in run: steps
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)

usage() {
	sed -n '6,9p' "$0" >&2
	exit 2
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
zizmor) zizmor ;;
actionlint) actionlint ;;
*) usage ;;
esac
