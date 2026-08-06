# AGENTS.md

## Project identity

- Project: **Firekeeper**
- Go module: `github.com/DanBradbury/firekeeper`
- Command: `firekeeper`
- Product: local, keyboard-driven terminal dashboard for monitoring coding-agent harnesses.
- Current harness support: Codex, GitHub Copilot CLI, and OpenCode process discovery. Codex and Copilot also have session metadata adapters.
- Current platform focus: macOS. Terminal switching supports Ghostty, Terminal.app, and iTerm2.

Keep Firekeeper compatible with normal agent CLI workflows. Do not require a daemon, proxy, wrapper command, remote flag, or changed launch procedure unless the user explicitly changes that product constraint.

## Before changing code

1. Read `README.md` for current user-facing behavior and controls.
2. Check `git status --short` and preserve unrelated or pre-existing changes.
3. Locate tests next to the relevant implementation before editing.
4. Treat files under `~/.codex` and `~/.copilot` as private user data. Never print prompts, secrets, raw event payloads, or full command lines unnecessarily.

Do not commit, push, create issues, or modify remote state unless the user asks.

## Build and verification

Go 1.26 or newer is declared in `go.mod`.

```sh
go run .
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

Before handoff, run at least `go test ./...`, `go vet ./...`, and `git diff --check`. Use `go test -race ./...` for changes involving model updates, polling, commands, parsing, or provider adapters. Run `gofmt -w` on touched Go files.

Useful manual checks:

```sh
go run . --renderer blocks
go run . --renderer kitty
go run . --demo forest
go run ./cmd/kitty-demo
```

Kitty rendering depends on terminal support. Test block rendering when Kitty graphics cannot be verified. Live process and terminal-switch checks are macOS-specific and may need Automation permission.

## Code map

- `main.go` — Bubble Tea model/update/view flow, tabs, RPG menu, process discovery/grouping, Codex metadata enrichment, macOS terminal switching, sprite composition, block/Kitty rendering, CLI entry point.
- `session_status.go` — best-effort Codex rollout event parsing into `ACTIVE`, `WAITING`, `NEEDS INPUT`, or `UNKNOWN`.
- `codex_usage.go` — short-lived Codex `app-server` stdio requests plus Codex usage UI.
- `copilot_sessions.go` — Copilot process-to-session mapping and read-only local metadata/event parsing.
- `copilot_usage.go` — read-only Copilot `session-store.db` aggregation and shared Usage-tab provider UI.
- `forest_demo.go` — layered tileset demo and static PNG export.
- `cmd/kitty-demo/` — standalone Kitty graphics example.
- `*_test.go` — parser, state, layout, renderer, navigation, and provider tests.
- `ASSETS.md` — required artwork attribution.

Package is intentionally `main`; tests can exercise unexported helpers directly.

## Bubble Tea architecture

- Keep `Update` fast and non-blocking. Filesystem reads, subprocess calls, and provider refreshes belong in `tea.Cmd` functions returning typed messages.
- Process polling runs every two seconds through `pollProcesses` and `refreshProcesses`.
- Preserve selection by root PID across refreshes.
- Preserve last known Codex/Copilot session metadata when a transient refresh returns no metadata. See `retainKnownSessionMetadata`.
- Treat provider metadata and state as best effort. Partial data should remain useful and should not crash or blank unrelated UI.
- Keep terminal output width-safe. Use existing truncation, padding, sanitization, and `fitLine` helpers.
- Menus are overlays. Opening a menu must not shift or recompute scene placement beneath it.

## Process and provider integrations

Process discovery uses `ps`; open-file mapping uses `lsof`; local database queries use `sqlite3 -readonly -json`; terminal focus uses `osascript`. Invoke commands with argument arrays, never shell interpolation.

Provider rules:

- Read local provider data only. Never mutate provider databases, rollout files, logs, or workspace metadata.
- Honor `CODEX_HOME` and `COPILOT_HOME` overrides.
- Use bounded operations and actionable, sanitized errors.
- Add parser tests for malformed, missing, partial, and evolving provider data.
- Use temporary directories and fixtures in tests. Do not depend on developer account data or write into real home directories.
- Avoid undocumented network endpoints, credential extraction, billing-page scraping, or secret persistence.
- Codex usage may launch authenticated `codex app-server --listen stdio://` for a short-lived request; do not turn it into a persistent server requirement.
- Copilot usage currently represents local CLI history from `session-store.db`. GitHub does not expose equivalent real-time individual allowance, remaining quota, and reset metrics through a supported API. Do not label local history as account-wide quota.

When adding a harness, separate these concerns:

1. Process classification and grouping.
2. Process-to-session correlation.
3. Metadata and state parsing.
4. Usage/quota collection, if a supported source exists.
5. Provider-specific UI and failure messaging.

One adapter failing must not block other providers or base process discovery.

## Session semantics

- `processGroup` represents one discovered runtime root plus child processes.
- `sessionInfo` carries normalized cross-provider metadata.
- State values describe latest observable provider event, not guaranteed ground truth.
- Existing Codex session counts use running Codex process groups. They include waiting/unknown runtimes, not only `sessionStateActive` entries.
- Never imply Firekeeper can detect every session state perfectly without provider cooperation.

## Rendering invariants

- Preserve pixel-art sharpness. Use nearest-neighbor resizing; never introduce smoothing.
- Ground tiles remain native 16×16 atlas sprites. `animationSourceScale` maps source pixels to terminal geometry without enlarging background tiles first.
- Compose one native-resolution scene, then feed same scene to block and Kitty renderers so both modes show equivalent content.
- Respect alpha: `a == 0` means transparent.
- Draw back-to-front. Ground is base; scene sprites and indicators follow; RPG menu overlays rendered scene.
- Keep character and fire layout collision-free. Fire scaling preserves aspect ratio.
- `chromeRows` reserves tab bar plus footer rows. View content must fit remaining height and handle minimum terminal size.
- Test layout with synthetic solid-color sprites instead of relying only on visual inspection.

Embedded artwork is licensed CC BY 3.0. Preserve `ASSETS.md` attribution when moving, replacing, or redistributing assets. Generated `forest-demo.png` is ignored and should not be committed.

## Testing style

- Prefer deterministic table-driven or focused unit tests.
- Test pure parsers with strings/readers and synthetic provider records.
- Test model behavior by sending Bubble Tea messages and inspecting returned model/view.
- Test renderer/layout behavior through sprite dimensions and exact pixels.
- Test errors without exposing machine-specific absolute paths or terminal escape sequences.
- If an optional executable such as `sqlite3` is unavailable, integration-style tests may skip; pure decoding tests should still run.

For user-visible changes, update `README.md` when controls, supported providers, requirements, limitations, or CLI flags change.

## Change discipline

- Keep changes scoped to request.
- Preserve backward-compatible keyboard controls unless user requests redesign.
- Avoid broad refactors while fixing provider drift or rendering bugs.
- Do not overwrite user edits in dirty worktree.
- Do not add generated binaries, database copies, provider logs, account metadata, or secrets.
- Explain remaining platform/provider limitations plainly during handoff.
