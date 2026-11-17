#!/bin/sh
# Builds the browser demo of the dashboard for the landing page.
#
# Writes into docs/landing/demo/ (all untracked, see .gitignore):
#   firekeeper-demo.<hash>.wasm   the dashboard on synthetic data
#   wasm_exec.<hash>.js           Go's loader for that exact toolchain
#   manifest.json                 the two file names, the only uncached file
#
# Bubble Tea 1.3.x has no js/wasm support, so the build runs against a
# temporary copy of the module in go.mod with five small js-only stubs added
# (cmd/demo-wasm/bubbletea-patch). go.mod and the native build are untouched.
# The stubs match one Bubble Tea version; any other version fails here rather
# than producing a demo that was never tried.
set -eu

expected_bubbletea=v1.3.10
max_wasm_bytes=${DEMO_WASM_MAX_BYTES:-26000000}

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
out="$root/docs/landing/demo"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM

cd "$root"
export GOWORK=off
version=$(go list -m -f '{{.Version}}' github.com/charmbracelet/bubbletea)
if [ "$version" != "$expected_bubbletea" ]; then
	echo "build.sh: bubbletea is $version, the js stubs were written for $expected_bubbletea." >&2
	echo "Re-check cmd/demo-wasm/bubbletea-patch against the new version, then update expected_bubbletea." >&2
	exit 1
fi

module=$(go list -m -f '{{.Dir}}' github.com/charmbracelet/bubbletea)
cp -R "$module" "$work/bubbletea"
chmod -R u+w "$work/bubbletea"
cp cmd/demo-wasm/bubbletea-patch/tty_js.go cmd/demo-wasm/bubbletea-patch/signals_js.go "$work/bubbletea/"

cat > "$work/go.work" <<WORKSPACE
go 1.26.0

use $root

replace github.com/charmbracelet/bubbletea => $work/bubbletea
WORKSPACE

mkdir -p "$out"
GOWORK="$work/go.work" GOOS=js GOARCH=wasm CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w" -o "$work/firekeeper-demo.wasm" ./cmd/demo-wasm

hash8() { shasum -a 256 "$1" | cut -c1-8; }
wasm_name="firekeeper-demo.$(hash8 "$work/firekeeper-demo.wasm").wasm"
exec_src="$(go env GOROOT)/lib/wasm/wasm_exec.js"
exec_name="wasm_exec.$(hash8 "$exec_src").js"

size=$(wc -c < "$work/firekeeper-demo.wasm" | tr -d ' ')
if [ "$size" -gt "$max_wasm_bytes" ]; then
	echo "build.sh: $wasm_name is $size bytes, over the $max_wasm_bytes limit (Cloudflare Pages allows 25 MiB per file)." >&2
	exit 1
fi

rm -f "$out"/firekeeper-demo*.wasm "$out"/wasm_exec*.js
cp "$work/firekeeper-demo.wasm" "$out/$wasm_name"
cp "$exec_src" "$out/$exec_name"
printf '{"wasm":"%s","exec":"%s"}\n' "$wasm_name" "$exec_name" > "$out/manifest.json"
printf 'Built docs/landing/demo/%s (%s bytes)\n' "$wasm_name" "$size"
