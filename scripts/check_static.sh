#!/bin/sh
# Static checks that CI and local runs share. The Go checks run under the
# toolchain that go.mod names, which is the one CI and releases build
# with; the other tools run from the image pinned on the one FROM line
# of tools/<tool>/Dockerfile, with the repository mounted read-only.
#
#   check_static.sh golangci [GOOS]
#                                golangci-lint with .golangci.yml, the
#                                formatters included; GOOS picks the
#                                platform files it reads
#   check_static.sh gofix        go fix has nothing left to rewrite
#   check_static.sh tidy         go.mod and go.sum are tidy
#   check_static.sh govulncheck  no known vulnerability is reachable from
#                                this code; the version is pinned in
#                                tools/govulncheck/go.mod
#   check_static.sh shellcheck   install.sh and scripts/*.sh, each in the
#                                dialect its shebang names
#   check_static.sh zizmor       GitHub Actions security audit; online
#                                when GH_TOKEN is set, offline otherwise
#   check_static.sh actionlint   workflow syntax, expressions, and the
#                                shell in run: steps
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)

usage() {
	sed -n '7,21p' "$0" >&2
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

# golangci-lint runs from its release archive, not an image: it loads
# packages through the host's Go toolchain and module cache. The version
# and the archive checksums are updated by hand, together.
golangci_version=2.14.0

golangci_sum() {
	case $1 in
	linux-amd64) echo ab90aeb7b066f92a33415b638a50fe5344bbb75a0d32ad30cc248d88f81032ab ;;
	linux-arm64) echo ee7ec5f3453d15ddf106fae5a4d6c71737712348a979d1fe9cd52ec7ea299bae ;;
	darwin-amd64) echo a5667c1c3536be1740133213e1e822bfb8f0d98ea12903174d6d5f635e4ed68d ;;
	darwin-arm64) echo 5ef5f36a7147e91dc58ef9ef4d11bb7bad5ead0c76eb6c01327a73c641d1dcc3 ;;
	*)
		echo "check_static: no golangci-lint archive for $1" >&2
		exit 2
		;;
	esac
}

sha256() {
	if command -v sha256sum >/dev/null; then
		sha256sum "$1" | cut -d ' ' -f 1
	else
		shasum -a 256 "$1" | cut -d ' ' -f 1
	fi
}

# Sets bin to the path of the verified golangci-lint binary, downloading
# it into the user cache on first use. Runs that download at the same
# time each write their own files and rename the result into place.
golangci_bin() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) arch=$(uname -m) ;;
	esac
	name=golangci-lint-$golangci_version-$os-$arch
	want=$(golangci_sum "$os-$arch")
	dir=${XDG_CACHE_HOME:-$HOME/.cache}/trajector-cli/$name
	bin=$dir/golangci-lint
	[ -x "$bin" ] && return 0
	mkdir -p "$dir"
	part=$dir/.part.$$
	curl -fsSL --retry 3 -o "$part.tar.gz" \
		"https://github.com/golangci/golangci-lint/releases/download/v$golangci_version/$name.tar.gz"
	got=$(sha256 "$part.tar.gz")
	[ "$got" = "$want" ] || {
		echo "check_static: $name.tar.gz has checksum $got, want $want" >&2
		exit 1
	}
	tar -xzOf "$part.tar.gz" "$name/golangci-lint" >"$part"
	chmod 755 "$part"
	mv -f "$part.tar.gz" "$dir/archive.tar.gz"
	mv -f "$part" "$bin"
}

golangci() {
	golangci_bin
	use_toolchain
	if [ -n "${1:-}" ]; then
		echo "golangci-lint: GOOS=$1"
		GOOS=$1 "$bin" run
	else
		"$bin" run
	fi
}

image() {
	ref=$(sed -n 's/^FROM[[:space:]]\{1,\}\([^[:space:]]\{1,\}\).*/\1/p' "$root/tools/$1/Dockerfile")
	[ -n "$ref" ] || {
		echo "check_static: no FROM line in tools/$1/Dockerfile" >&2
		exit 2
	}
	echo "$ref"
}

shellcheck() {
	cd "$root"
	docker run --rm -v "$root:/repo:ro" -w /repo "$(image shellcheck)" install.sh scripts/*.sh
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

case $#:${1:-} in
2:golangci | 1:*) ;;
*) usage ;;
esac
case $1 in
golangci) golangci "${2:-}" ;;
gofix) use_toolchain && go fix -diff ./... ;;
tidy) use_toolchain && go mod tidy -diff ;;
govulncheck) use_toolchain && go tool -modfile=tools/govulncheck/go.mod govulncheck ./... ;;
shellcheck) shellcheck ;;
zizmor) zizmor ;;
actionlint) actionlint ;;
*) usage ;;
esac
