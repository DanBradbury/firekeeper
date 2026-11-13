# Firekeeper browser TUI spike

## Verdict: go with caveats

Real `internal/tui` renders synthetic sessions through Bubble Tea 1.3.10 in js/wasm. A local Bubble Tea copy with five js-only stubs compiles and runs with custom input/output, no TTY. Landing page Processes frame now has click-to-start preview; recorded video stays visible until click and remains fallback if build is absent. This is local spike, not deployed or production ready.

## See it locally

From repository root:

```sh
cmd/demo-wasm/build.sh
python3 -m http.server 8765 --bind 127.0.0.1 --directory docs/landing
```

Open `http://127.0.0.1:8765/#processes-live` and press **Try the real TUI**. Click terminal, use arrows, `Tab`, `Enter`, `c`, `Esc`, `?`, `q`. Use `←`/`→` in Usage to switch four synthetic providers; `Enter` opens sample session chat. `s` and chat `e` show harmless demo messages. Build script uses already cached Go modules with `GOPROXY=off`; it copies pinned Bubble Tea 1.3.10 into temporary directory, adds two js files, and builds to ignored `docs/landing/demo/firekeeper-demo.wasm`. It also copies Go's `wasm_exec.js`. Neither generated file is tracked.

## Five questions

1. **Compile:** Yes, after excluding `internal/sqlitejson/sqlitejson.go` on js and providing `sqlitejson_js.go`; this removes `modernc.org/sqlite` and unsupported `modernc.org/libc` from browser build. Bubble Tea 1.3.10 itself lacked `listenForResize`, `suspendSupported`, `openInputTTY`, `suspendProcess`, and `Program.initInput` on js. `cmd/demo-wasm/bubbletea-patch/{tty_js,signals_js}.go` supply these in temporary module copy. No other package needed build constraints. Unpatched Bubble Tea build still fails with those six compiler references.
2. **No TTY:** Yes. Node js/wasm smoke used Bubble Tea custom `io.Pipe` input and output writer. It emitted first frame, received `q`, and exited with nil error. Full demo uses `tea.WithInput`, `tea.WithOutput`, `tea.WithoutSignalHandler`, and explicit `tea.WindowSizeMsg` from `ResizeObserver`. No provider CLI, tty, or termios call is part of demo path. Browser visual behavior remains unverified in automated browser: in-app browser control was unavailable in this session.
3. **Synthetic data:** `NewDemoModel` wraps real model in js-only file. It supplies four invented projects across Claude, Codex, Copilot, Kimi and cycles ACTIVE, WAITING, NEEDS INPUT on two-second tick. It also seeds four invented usage snapshots for real Overview and Usage renderers, including quotas, token history, models and selectable past sessions. `c` and Usage `Enter` open real chat view with invented text. Wrapper discards I/O commands and mouse input, and intercepts refresh, tab/provider switching, `s`, `e`, `o`, and chat reload. Normal `NewModel` and native app path are unchanged. A future native `--demo` could move wrapper to platform-neutral file and reuse same fixture.
4. **Size and speed:** Full demo WASM: **19,770,648 bytes raw**, **4,655,700 gzip**, **3,282,654 brotli**. xterm.js + fit addon JS/CSS add about 297 KB raw. All assets load only on click. Browser click-to-first-frame was not measured because browser control was unavailable; page displays time from WASM startup to first output. Node js/wasm first output arrived about 52 ms after instantiation began on this machine; that excludes page asset loading and xterm rendering, so it is not browser click-to-first-frame time. TinyGo was not tried because `tinygo` is not installed; plain Go exceeds ~3 MB brotli threshold only slightly, but stays under 5 MB stop threshold.
5. **Feel:** Node js/wasm drove arrow down, `Enter`, `c`, `Esc`, `?`, `Tab`, resize message, `s`, `q`; confirmed sample rows/chat/help/message, ANSI escapes and box-drawing characters in output. Separate Node check confirmed all four Overview cards, Usage cards for each provider, provider cycling, and Usage Enter chat. Colors as visually rendered and container reflow still require browser QA. Browser page loaded over local HTTP with WASM MIME `application/wasm`; in-app browser interaction was unavailable.

## Files

- `cmd/demo-wasm/main_js.go`, `main_other.go`: browser I/O bridge and native build placeholder.
- `cmd/demo-wasm/bubbletea-patch/tty_js.go`, `signals_js.go`: js-only Bubble Tea platform stubs.
- `cmd/demo-wasm/build.sh`: reproducible local build, no Go module change and no network.
- `cmd/demo-wasm/SPIKE.md`: findings and local preview instructions.
- `internal/tui/demo_js.go`, `demo_usage_js.go`: synthetic model wrapper and invented usage snapshots; no existing TUI file changed.
- `internal/sqlitejson/sqlitejson.go`: existing file, `!js` constraint; `sqlitejson_js.go`: rejecting browser stub.
- `docs/landing/index.html`, `styles.css`, `demo/processes-demo.js`: click-to-start Processes preview, styling, loading and resize bridge.
- `docs/landing/demo/vendor/`: pinned xterm.js 5.5.0 and fit addon 0.10.0 JS/CSS plus licenses.
- `.gitignore`: excludes generated WASM and Go runtime JS.

## Remaining work before shipping

1. **Risk:** Browser QA: first frame timing, key sequence, ANSI colors, box drawing, resize, `q` restart, error fallback, and narrow layouts. No browser automation tool was exposed during this run.
2. **Risk:** Replace temporary Bubble Tea copy patch with maintained upstream js/wasm support or pinned fork; track upstream [WASM issue](https://github.com/charmbracelet/bubbletea/issues/1410). Bubble Tea v2 has breaking API changes, so a new version does not automatically remove blocker.
3. Confirm demo command path never invokes provider sources using runtime instrumentation.
4. Measure click-to-first-frame on typical laptop over realistic network, then decide cache headers and compression. Current brotli is 3.28 MB.
5. Review accessibility, keyboard focus, and licensing, then decide whether to deploy landing assets. No deployment was performed.

## Verification

`gofmt -w .`, `go build ./...`, `go vet ./...`, `GOOS=windows go vet ./...`, `GOOS=darwin go vet ./...`, `go test -race ./...`, `git diff --check`, `shellcheck cmd/demo-wasm/build.sh`, and `node --check docs/landing/demo/processes-demo.js` passed. Initial sandbox race test failed on denied loopback listeners and `/bin/ps`; host-access rerun passed. Go 1.26.5 darwin/arm64.
