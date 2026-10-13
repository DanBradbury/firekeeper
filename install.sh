#!/bin/sh
# Install Firekeeper from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --version v0.1.0
#
# Installs to ~/.local/bin, or to $FIREKEEPER_INSTALL_DIR. Never uses sudo.
# FIREKEEPER_RELEASES_URL overrides the release location (used by the tests).
set -eu

RELEASES_URL="${FIREKEEPER_RELEASES_URL:-https://github.com/DanBradbury/firekeeper/releases}"
INSTALL_DIR="${FIREKEEPER_INSTALL_DIR:-${HOME:-}/.local/bin}"

say() { printf '%s\n' "$*"; }
die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'USAGE'
Usage: install.sh [--version vX.Y.Z]

Installs the firekeeper binary from a GitHub release. Without --version it
installs the latest release.

Environment:
  FIREKEEPER_INSTALL_DIR  install directory (default ~/.local/bin)
USAGE
}

requested=""
while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || die "--version needs a value such as v0.1.0"
		requested="$2"
		shift 2
		;;
	--version=*)
		requested="${1#--version=}"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		die "unknown argument: $1"
		;;
	esac
done

if [ -z "$INSTALL_DIR" ] || [ "$INSTALL_DIR" = "/.local/bin" ]; then
	die "cannot work out the install directory: HOME is not set; set FIREKEEPER_INSTALL_DIR"
fi

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) die "unsupported operating system $(uname -s): Firekeeper releases cover macOS and Linux. See the README to build from source." ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m): Firekeeper releases cover amd64 and arm64. See the README to build from source." ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
	die "sha256sum or shasum is required to verify the download"
fi

if [ -z "$requested" ]; then
	# /releases/latest redirects to /releases/tag/<tag>.
	final=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$RELEASES_URL/latest") ||
		die "could not look up the latest release at $RELEASES_URL/latest"
	tag="${final##*/}"
	[ "$tag" != "latest" ] || die "could not work out the latest release tag"
else
	case "$requested" in
	v*) tag="$requested" ;;
	*) tag="v$requested" ;;
	esac
fi
case "$tag" in
*[!A-Za-z0-9._-]*) die "invalid version: $tag" ;;
esac
ver="${tag#v}"

archive="firekeeper_${ver}_${os}_${arch}.tar.gz"
tmp=$(mktemp -d) || die "could not create a temporary directory"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Installing firekeeper $tag for $os/$arch"
curl -fsSL -o "$tmp/$archive" "$RELEASES_URL/download/$tag/$archive" ||
	die "could not download $archive for $tag (does that release exist?)"
curl -fsSL -o "$tmp/checksums.txt" "$RELEASES_URL/download/$tag/checksums.txt" ||
	die "could not download checksums.txt for $tag"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "checksums.txt has no entry for $archive"
got=$(sha256 "$tmp/$archive")
if [ "$got" != "$want" ]; then
	die "checksum mismatch for $archive (expected $want, got $got); nothing was installed"
fi

mkdir "$tmp/x"
tar -xzf "$tmp/$archive" -C "$tmp/x" firekeeper || die "could not extract firekeeper from $archive"

mkdir -p "$INSTALL_DIR" || die "could not create $INSTALL_DIR; set FIREKEEPER_INSTALL_DIR to a writable directory"
# Copy beside the target, then rename, so an existing binary is replaced
# atomically and a running one is not overwritten in place.
cp "$tmp/x/firekeeper" "$INSTALL_DIR/.firekeeper.new" ||
	die "could not write to $INSTALL_DIR; set FIREKEEPER_INSTALL_DIR to a writable directory"
chmod 755 "$INSTALL_DIR/.firekeeper.new"
mv -f "$INSTALL_DIR/.firekeeper.new" "$INSTALL_DIR/firekeeper"

say "Installed $INSTALL_DIR/firekeeper"
case ":${PATH:-}:" in
*":$INSTALL_DIR:"*) ;;
*)
	say ""
	say "$INSTALL_DIR is not on your PATH. Add it, for example:"
	say "  export PATH=\"$INSTALL_DIR:\$PATH\""
	;;
esac
