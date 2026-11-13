#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM

module=$(cd "$root" && GOWORK=off GOPROXY=off go list -m -f '{{.Dir}}' github.com/charmbracelet/bubbletea)
cp -R "$module" "$work/bubbletea"
chmod -R u+w "$work/bubbletea"
cp "$root/cmd/demo-wasm/bubbletea-patch/tty_js.go" "$root/cmd/demo-wasm/bubbletea-patch/signals_js.go" "$work/bubbletea/"

cat > "$work/go.work" <<WORKSPACE
go 1.26.0

use $root

replace github.com/charmbracelet/bubbletea => $work/bubbletea
WORKSPACE

mkdir -p "$root/docs/landing/demo"
(cd "$root" && GOWORK="$work/go.work" GOPROXY=off GOOS=js GOARCH=wasm go build -o docs/landing/demo/firekeeper-demo.wasm ./cmd/demo-wasm)
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$root/docs/landing/demo/wasm_exec.js"
printf 'Built docs/landing/demo/firekeeper-demo.wasm\n'
