# Browser demo

The "See what is running" section of the landing page runs the real dashboard
(`internal/tui`) in the browser, compiled to WebAssembly, on invented sample
sessions. It replaces the recorded video; the recording is the fallback.

Nothing here is part of the `firekeeper` binary or its launch procedure. The
demo never discovers processes, reads provider files or touches the network
(see "What the demo can reach"), and it is not uploaded anywhere.

## Build and look at it

```sh
cmd/demo-wasm/build.sh
python3 -m http.server 8765 --bind 127.0.0.1 --directory docs/landing
```

Open `http://127.0.0.1:8765/#processes-live`. `scripts/test-demo-wasm.cjs`
drives the built program in Node without a browser (CI runs it).

`build.sh` writes three untracked files into `docs/landing/demo/`: the program
(`firekeeper-demo.<hash>.wasm`), Go's loader for the same toolchain
(`wasm_exec.<hash>.js`) and `manifest.json` naming both. `landing.yml` builds
them on every deploy, so they are never committed.

## How it fits together

- `main_js.go`: the browser bridge. Keys come in through `firekeeperInput`,
  size through `firekeeperResize`, pause through `firekeeperPause`; frames go
  out through `firekeeperOutput`. `main_other.go` keeps `go build ./...` happy.
- `internal/tui/demo.go`, `demo_usage.go`: the demo model. It wraps the real
  model, seeds invented sessions and usage, and **drops every command the real
  model returns except the one that quits**, so no I/O can start.
  `demo_test.go` presses keys in every view and fails if one leaves a command
  behind. Built natively too, so it is tested natively.
- `internal/sqlitejson/sqlitejson_js.go`: the SQLite reader is `!js`, which
  keeps `modernc.org/sqlite` out of the browser build.
- `bubbletea-patch/`: Bubble Tea 1.3.x has no js/wasm support. `build.sh`
  builds against a temporary copy with these five stubs added; `go.mod` is
  untouched. It refuses any other Bubble Tea version, so a bump is a
  deliberate re-check of the stubs ([upstream issue](https://github.com/charmbracelet/bubbletea/issues/1410)).
- `docs/landing/demo/processes-demo.js`: loads xterm.js and the program when
  the section scrolls near the viewport, never earlier.
- `docs/landing/_headers`: the program and loader are named by content hash
  and cached for a year; `manifest.json` is revalidated, so a deploy is
  picked up at once and a visitor never gets a loader from another toolchain.

## When the recording shows instead

No WebAssembly, a touch device, a window under 700 px, or the demo failing to
load or start. Data Saver and slow connections, and `prefers-reduced-motion`,
show a poster and a start button instead of starting by themselves; with
reduced motion the sample sessions also start paused. A visitor can pause the
sample updates at any time.

Keyboard: click the terminal to type in it; `Ctrl+]` leaves it. The demo never
takes focus by itself.

## What the demo can reach

Only the synthetic model. `s`, `o`, `e` and chat reload show a message instead
of acting. The page downloads the program (about 3.2 MB with brotli, 19 MB
raw, under Cloudflare Pages' 25 MiB file limit; `build.sh` fails above
26,000,000 bytes) and xterm.js (about 300 KB), from its own origin only.

## Size and what is left

Plain Go. TinyGo was not tried. If the program needs to shrink, that and
`wasm-opt` are the next things to try.

Vendored: xterm.js 5.5.0 and addon-fit 0.10.0 in `docs/landing/demo/vendor/`
with their MIT licenses beside them, served with the page (`THIRD_PARTY_NOTICES.md` is generated from Go modules and does not cover them).
