#!/bin/sh
# Tests install.sh against a fake local release directory. No network.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
installer="$here/../install.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"; [ -z "${server_pid:-}" ] || kill "$server_pid" 2>/dev/null || true' EXIT INT TERM

fails=0
ok() { printf 'ok   %s\n' "$1"; }
bad() {
	printf 'FAIL %s\n' "$1"
	fails=$((fails + 1))
}

case "$(uname -s)" in
Darwin) os=darwin ;;
*) os=linux ;;
esac
case "$(uname -m)" in
arm64 | aarch64) arch=arm64 ;;
*) arch=amd64 ;;
esac

sum() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d ' ' -f 1; else shasum -a 256 "$1" | cut -d ' ' -f 1; fi
}

# make_release DIR TAG: a release directory laid out like GitHub's
# releases/download/<tag>/ with a valid archive and checksums.txt.
make_release() {
	rel="$1/download/$2"
	ver="${2#v}"
	mkdir -p "$rel" "$work/pkg"
	printf '#!/bin/sh\necho "firekeeper %s (fake)"\n' "$ver" >"$work/pkg/firekeeper"
	chmod 755 "$work/pkg/firekeeper"
	tar -czf "$rel/firekeeper_${ver}_${os}_${arch}.tar.gz" -C "$work/pkg" firekeeper
	{
		printf '%s  firekeeper_%s_%s_%s.tar.gz\n' "$(sum "$rel/firekeeper_${ver}_${os}_${arch}.tar.gz")" "$ver" "$os" "$arch"
		printf '%064d  firekeeper_%s_other_other.tar.gz\n' 0 "$ver"
	} >"$rel/checksums.txt"
}

make_release "$work/releases" v1.2.3
base="file://$work/releases"

# 1. Install a pinned version and run it.
dir="$work/bin1"
if out=$(FIREKEEPER_RELEASES_URL="$base" FIREKEEPER_INSTALL_DIR="$dir" sh "$installer" --version v1.2.3 2>&1) &&
	[ "$("$dir/firekeeper")" = "firekeeper 1.2.3 (fake)" ]; then ok "install pinned version"; else bad "install pinned version: $out"; fi
case "$out" in *"not on your PATH"*) ok "PATH hint printed" ;; *) bad "PATH hint printed: $out" ;; esac

# 2. No PATH hint when the directory is already on PATH.
out=$(PATH="$dir:$PATH" FIREKEEPER_RELEASES_URL="$base" FIREKEEPER_INSTALL_DIR="$dir" sh "$installer" --version 1.2.3 2>&1)
case "$out" in *"not on your PATH"*) bad "no PATH hint when on PATH (also: bare 1.2.3 accepted)" ;; *) ok "no PATH hint when on PATH; bare version accepted" ;; esac

# 3. Checksum mismatch aborts and installs nothing.
make_release "$work/bad" v1.2.3
printf 'tampered' >>"$work/bad/download/v1.2.3/firekeeper_1.2.3_${os}_${arch}.tar.gz"
dir="$work/bin2"
if out=$(FIREKEEPER_RELEASES_URL="file://$work/bad" FIREKEEPER_INSTALL_DIR="$dir" sh "$installer" --version v1.2.3 2>&1); then
	bad "checksum mismatch must fail"
else
	case "$out" in
	*"checksum mismatch"*) if [ ! -e "$dir/firekeeper" ]; then ok "checksum mismatch aborts, nothing installed"; else bad "binary installed despite mismatch"; fi ;;
	*) bad "checksum mismatch message: $out" ;;
	esac
fi

# 4. A mismatch must not clobber an existing install.
mkdir -p "$dir"
printf 'old' >"$dir/firekeeper"
FIREKEEPER_RELEASES_URL="file://$work/bad" FIREKEEPER_INSTALL_DIR="$dir" sh "$installer" --version v1.2.3 >/dev/null 2>&1 || true
if [ "$(cat "$dir/firekeeper")" = old ]; then ok "mismatch leaves existing binary alone"; else bad "mismatch clobbered existing binary"; fi

# 5. Missing checksum entry aborts.
make_release "$work/noentry" v1.2.3
printf '%064d  other.tar.gz\n' 0 >"$work/noentry/download/v1.2.3/checksums.txt"
if FIREKEEPER_RELEASES_URL="file://$work/noentry" FIREKEEPER_INSTALL_DIR="$work/bin3" sh "$installer" --version v1.2.3 >/dev/null 2>&1; then
	bad "missing checksum entry must fail"
else ok "missing checksum entry aborts"; fi

# 6. Missing release fails clearly.
if out=$(FIREKEEPER_RELEASES_URL="$base" FIREKEEPER_INSTALL_DIR="$work/bin4" sh "$installer" --version v9.9.9 2>&1); then
	bad "missing release must fail"
else
	case "$out" in *"could not download"*) ok "missing release fails clearly" ;; *) bad "missing release message: $out" ;; esac
fi

# 7. Bad version strings are rejected before any request.
if FIREKEEPER_RELEASES_URL="$base" FIREKEEPER_INSTALL_DIR="$work/bin5" sh "$installer" --version 'v1/../x' >/dev/null 2>&1; then
	bad "invalid version must fail"
else ok "invalid version rejected"; fi

# 8. Unsupported platforms fail with a clear message. uname is shimmed.
mkdir -p "$work/shim"
cat >"$work/shim/uname" <<'SHIM'
#!/bin/sh
case "$1" in -s) echo "${FAKE_OS:-Linux}" ;; -m) echo "${FAKE_ARCH:-x86_64}" ;; esac
SHIM
chmod 755 "$work/shim/uname"
for combo in FreeBSD:x86_64 Linux:riscv64 Darwin:ppc; do
	fake_os=${combo%%:*}
	fake_arch=${combo##*:}
	if out=$(PATH="$work/shim:$PATH" FAKE_OS="$fake_os" FAKE_ARCH="$fake_arch" FIREKEEPER_RELEASES_URL="$base" FIREKEEPER_INSTALL_DIR="$work/bin6" sh "$installer" --version v1.2.3 2>&1); then
		bad "unsupported $combo must fail"
	else
		case "$out" in *unsupported*) ok "unsupported platform $combo" ;; *) bad "unsupported $combo message: $out" ;; esac
	fi
done

# 9. Latest-release lookup, through a local HTTP redirect like GitHub's.
if command -v python3 >/dev/null 2>&1; then
	port=$((20000 + $$ % 20000))
	cat >"$work/server.py" <<PY
import http.server, os, sys
root = sys.argv[1]
class H(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **k):
        super().__init__(*a, directory=root, **k)
    def do_GET(self):
        if self.path == "/latest":
            self.send_response(302)
            self.send_header("Location", "/tag/v1.2.3")
            self.end_headers()
            return
        if self.path == "/tag/v1.2.3":
            self.send_response(200); self.end_headers(); return
        super().do_GET()
    def log_message(self, *a):
        pass
http.server.HTTPServer(("127.0.0.1", $port), H).serve_forever()
PY
	python3 -I "$work/server.py" "$work/releases" &
	server_pid=$!
	n=0
	until curl -fs -o /dev/null "http://127.0.0.1:$port/tag/v1.2.3" 2>/dev/null || [ $n -ge 50 ]; do
		n=$((n + 1))
		sleep 0.1
	done
	dir="$work/bin7"
	if out=$(FIREKEEPER_RELEASES_URL="http://127.0.0.1:$port" FIREKEEPER_INSTALL_DIR="$dir" sh "$installer" 2>&1) &&
		[ "$("$dir/firekeeper")" = "firekeeper 1.2.3 (fake)" ]; then ok "latest release resolved and installed"; else bad "latest release: $out"; fi
else
	printf 'skip latest-release test: python3 not found\n'
fi

# 10. Works when piped to sh, as in the README.
dir="$work/bin8"
if FIREKEEPER_RELEASES_URL="$base" FIREKEEPER_INSTALL_DIR="$dir" sh -s -- --version v1.2.3 <"$installer" >/dev/null 2>&1 && [ -x "$dir/firekeeper" ]; then
	ok "piped to sh"
else bad "piped to sh"; fi

[ "$fails" -eq 0 ] || {
	printf '%d failure(s)\n' "$fails"
	exit 1
}
printf 'all passed\n'
