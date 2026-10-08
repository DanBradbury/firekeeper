#!/bin/sh
# Firekeeper installer.
#
#   curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh -s -- --version v0.1.0
#
# Downloads the release archive for this OS and architecture, verifies its
# SHA-256 checksum, and installs the binary to ~/.local/bin, or to
# $FIREKEEPER_INSTALL_DIR when set. It never uses sudo.
#
# FIREKEEPER_RELEASES_API and FIREKEEPER_DOWNLOAD_URL override where releases
# are looked up and downloaded from (a mirror, or a local directory in tests).

set -eu

REPO="DanBradbury/firekeeper"

say() {
	printf 'firekeeper install: %s\n' "$*" >&2
}

die() {
	say "error: $*"
	exit 1
}

usage() {
	cat <<'USAGE'
Usage: install.sh [--version vX.Y.Z]

Installs Firekeeper to ~/.local/bin (override with FIREKEEPER_INSTALL_DIR).
Without --version, the latest release is installed.
USAGE
}

detect_os() {
	os=$(uname -s)
	case "$os" in
	Darwin) echo darwin ;;
	Linux) echo linux ;;
	*) die "unsupported operating system '$os'; release binaries exist for macOS and Linux. Build from source instead: go install github.com/$REPO@latest" ;;
	esac
}

detect_arch() {
	arch=$(uname -m)
	case "$arch" in
	x86_64 | amd64)
		# A shell running under Rosetta reports x86_64 on Apple Silicon.
		if [ "$(uname -s)" = Darwin ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
			echo arm64
		else
			echo amd64
		fi
		;;
	arm64 | aarch64) echo arm64 ;;
	*) die "unsupported architecture '$arch'; release binaries exist for amd64 and arm64. Build from source instead: go install github.com/$REPO@latest" ;;
	esac
}

# fetch URL DEST downloads URL to DEST.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --proto '=https,file' -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		die "curl or wget is required"
	fi
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		die "sha256sum or shasum is required to verify the download"
	fi
}

latest_tag() {
	api=${FIREKEEPER_RELEASES_API:-https://api.github.com/repos/$REPO/releases/latest}
	fetch "$api" "$tmp/latest.json" || die "could not look up the latest release at $api"
	tag=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)
	[ -n "$tag" ] || die "could not find the latest release tag at $api"
	echo "$tag"
}

main() {
	tag=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--version)
			[ $# -ge 2 ] || die "--version needs a value such as v0.1.0"
			tag=$2
			shift 2
			;;
		--version=*)
			tag=${1#--version=}
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage >&2
			die "unknown argument '$1'"
			;;
		esac
	done

	os=$(detect_os)
	arch=$(detect_arch)

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t firekeeper)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	if [ -z "$tag" ]; then
		tag=$(latest_tag)
	fi
	case "$tag" in
	v*) ;;
	*) tag="v$tag" ;;
	esac
	version=${tag#v}

	base=${FIREKEEPER_DOWNLOAD_URL:-https://github.com/$REPO/releases/download}
	archive="firekeeper_${version}_${os}_${arch}.tar.gz"
	checksums="firekeeper_${version}_checksums.txt"

	say "downloading Firekeeper $tag for $os/$arch"
	fetch "$base/$tag/$archive" "$tmp/$archive" || die "could not download $archive from release $tag; check that the release exists"
	fetch "$base/$tag/$checksums" "$tmp/$checksums" || die "could not download $checksums from release $tag"

	want=$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/$checksums")
	[ -n "$want" ] || die "$checksums has no entry for $archive"
	got=$(sha256_of "$tmp/$archive")
	if [ "$got" != "$want" ]; then
		die "checksum mismatch for $archive (expected $want, got $got); nothing was installed"
	fi

	mkdir "$tmp/x"
	tar -xzf "$tmp/$archive" -C "$tmp/x" || die "could not unpack $archive"
	[ -f "$tmp/x/firekeeper" ] || die "$archive does not contain a firekeeper binary"

	dir=${FIREKEEPER_INSTALL_DIR:-${HOME:?HOME is not set}/.local/bin}
	mkdir -p "$dir" || die "could not create $dir; set FIREKEEPER_INSTALL_DIR to a directory you can write"
	# Copy next to the target, then rename, so a running firekeeper is never
	# left with a half-written binary.
	cp "$tmp/x/firekeeper" "$dir/.firekeeper.new" || die "could not write to $dir; set FIREKEEPER_INSTALL_DIR to a directory you can write"
	chmod 755 "$dir/.firekeeper.new"
	mv -f "$dir/.firekeeper.new" "$dir/firekeeper"

	say "installed $dir/firekeeper ($tag)"
	case ":${PATH:-}:" in
	*":$dir:"*) ;;
	*)
		say "$dir is not on your PATH. Add it with:"
		# shellcheck disable=SC2016 # $PATH is meant literally here.
		printf '\n    export PATH="%s:$PATH"\n\n' "$dir" >&2
		say "and add that line to your shell profile (~/.zshrc or ~/.bashrc)."
		;;
	esac
	say "run 'firekeeper' to start the dashboard"
}

main "$@"
