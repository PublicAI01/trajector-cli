#!/bin/sh
# Install trajector from a published GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/PublicAI01/trajector-cli/main/install.sh | sh
#
# Downloading through curl rather than a browser is deliberate: macOS
# Gatekeeper and Windows SmartScreen only intercept binaries that a
# quarantine-aware application downloaded, so a binary fetched here runs
# without the "unidentified developer" prompt an archive saved from a
# browser produces.
#
# The archive's checksum is always verified against the release's
# checksum file before anything is installed. A machine with no SHA-256
# tool is refused rather than installed on unverified.
#
# Environment:
#   TRAJECTOR_VERSION       install this tag instead of the newest release
#   TRAJECTOR_INSTALL_DIR   install here instead of ~/.local/bin
#   TRAJECTOR_API_BASE      release index origin (testing)
#   TRAJECTOR_DL_BASE       release asset origin (testing)
#
# The two origin overrides exist so this script can be exercised against
# a local server. They are safe here in a way they would not be inside
# trajector itself: this script is only ever run by a user at a shell,
# never from a session hook that a repository's committed settings can
# reach.

set -eu

REPO=PublicAI01/trajector-cli
API_BASE=${TRAJECTOR_API_BASE:-https://api.github.com}
DL_BASE=${TRAJECTOR_DL_BASE:-https://github.com/$REPO/releases/download}
CHECKSUMS=trajector_checksums.txt

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

detect_os() {
	os=$(uname -s)
	case $os in
	Darwin) printf 'darwin' ;;
	Linux) printf 'linux' ;;
	MINGW* | MSYS* | CYGWIN* | Windows_NT) printf 'windows' ;;
	*) die "unsupported operating system: $os" ;;
	esac
}

detect_arch() {
	arch=$(uname -m)
	case $arch in
	x86_64 | amd64) printf 'amd64' ;;
	arm64 | aarch64) printf 'arm64' ;;
	*) die "unsupported architecture: $arch (trajector ships amd64 and arm64)" ;;
	esac
}

# windows_instructions explains the manual path and exits non-zero:
# nothing was installed, and a caller chaining off this script must see
# that. Unblocking is spelled out because an archive saved by a browser
# carries a Mark-of-the-Web that SmartScreen acts on.
windows_instructions() {
	say "trajector does not install itself on Windows from this script."
	say ""
	say "Download and unpack the archive for your machine:"
	say "  $DL_BASE/<tag>/trajector_<version>_windows_$1.zip"
	say ""
	say "Then, in PowerShell, verify it and clear the download mark:"
	say "  Get-FileHash .\\trajector_<version>_windows_$1.zip -Algorithm SHA256"
	say "  # compare against $CHECKSUMS from the same release"
	say "  Unblock-File .\\trajector.exe"
	say ""
	say "Finally move trajector.exe somewhere on your PATH."
	exit 1
}

need_downloader() {
	if command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; then
		return 0
	fi
	die "neither curl nor wget is available"
}

# fetch writes a URL's body to stdout, failing on any non-2xx answer so
# an error page is never mistaken for content.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1"
	else
		wget -qO- "$1"
	fi
}

fetch_to_file() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	else
		wget -qO "$2" "$1"
	fi
}

# newest_tag reads the release index and returns the highest version in
# it. The /releases/latest endpoint is deliberately not used: it omits
# pre-releases, and every 0.x release is published as one.
#
# The index is ordered by publication, which is not version order: a
# patch to an older line, published after a newer minor, is listed
# first and would otherwise be handed to every new install. So the tags
# are compared as numbers, the way `trajector upgrade` compares them.
# A tag that is not three numbers behind an optional "v" is skipped;
# between tags with the same three numbers the one published most
# recently wins, which puts a finished release ahead of its own
# candidates.
newest_tag() {
	fetch "$API_BASE/repos/$REPO/releases" |
		tr ',' '\n' |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
		awk '
			{
				version = $0
				sub(/^v/, "", version)
				sub(/[-+].*$/, "", version)
				if (version !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) next
				split(version, n, ".")
				major = n[1] + 0
				minor = n[2] + 0
				patch = n[3] + 0
				if (!found ||
				    major > best_major ||
				    (major == best_major && minor > best_minor) ||
				    (major == best_major && minor == best_minor && patch > best_patch)) {
					found = 1
					best_major = major
					best_minor = minor
					best_patch = patch
					best_tag = $0
				}
			}
			END { if (found) print best_tag }
		'
}

# verify_checksum checks archive against its line in the release's
# checksum file. Only that one line is checked, so the tool needs
# nothing beyond plain -c: the other five platforms' archives are not
# present and must not be reported as failures. A machine with no
# SHA-256 tool is an error — installing unverified is not an option this
# script offers.
verify_checksum() {
	archive=$1
	expected=$archive.sha256
	grep " $archive\$" "$CHECKSUMS" >"$expected" ||
		die "$CHECKSUMS has no entry for $archive; refusing to install unverified"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum --check --status "$expected" ||
			die "checksum mismatch for $archive; nothing was installed"
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 --check --status "$expected" ||
			die "checksum mismatch for $archive; nothing was installed"
	else
		die "no SHA-256 tool found (sha256sum or shasum); refusing to install unverified"
	fi
}

# on_path reports whether dir is already a PATH entry, matched whole so
# a directory that merely prefixes one does not count.
on_path() {
	case ":$PATH:" in
	*":$1:"*) return 0 ;;
	*) return 1 ;;
	esac
}

# absolute resolves a possibly relative install directory against the
# working directory, before the script moves into its own temp dir.
absolute() {
	case $1 in
	/*) printf '%s' "$1" ;;
	*) printf '%s/%s' "$PWD" "$1" ;;
	esac
}

main() {
	os=$(detect_os)
	arch=$(detect_arch)
	if [ "$os" = windows ]; then
		windows_instructions "$arch"
	fi
	need_downloader

	install_dir=$(absolute "${TRAJECTOR_INSTALL_DIR:-$HOME/.local/bin}")
	target=$install_dir/trajector

	tag=${TRAJECTOR_VERSION:-$(newest_tag)}
	[ -n "$tag" ] || die "could not determine the newest release of $REPO"
	version=${tag#v}
	archive="trajector_${version}_${os}_${arch}.tar.gz"

	tmp=$(mktemp -d)
	# staged sits in the destination directory so the final move is a
	# rename within one filesystem: a reader of the destination sees
	# either the old binary or the new one, never a partial file.
	staged=$install_dir/.trajector.incoming.$$
	trap 'rm -rf "$tmp" "$staged"' EXIT INT TERM
	cd "$tmp"

	say "Downloading $archive ($tag)..."
	fetch_to_file "$DL_BASE/$tag/$archive" "$archive" ||
		die "release $tag has no asset $archive"
	fetch_to_file "$DL_BASE/$tag/$CHECKSUMS" "$CHECKSUMS" ||
		die "release $tag has no $CHECKSUMS; refusing to install unverified"
	verify_checksum "$archive"
	tar -xzf "$archive" trajector || die "no trajector binary inside $archive"

	previous=""
	if [ -x "$target" ]; then
		previous=$("$target" version 2>/dev/null || true)
	fi

	mkdir -p "$install_dir"
	cp trajector "$staged"
	chmod 0755 "$staged"
	mv -f "$staged" "$target"

	if [ -n "$previous" ]; then
		say "Replaced $previous with trajector $version in $install_dir."
	else
		say "Installed trajector $version in $install_dir."
	fi

	if ! on_path "$install_dir"; then
		say ""
		say "$install_dir is not on your PATH. Add it to your shell profile:"
		say "  export PATH=\"$install_dir:\$PATH\""
	fi
	say ""
	say "Next: trajector login, then trajector enable in a project."
}

main "$@"
