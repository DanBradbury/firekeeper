# AGENTS.md

## Project identity

- Project: **Firekeeper**
- Go module: `github.com/DanBradbury/firekeeper`
- Command: `firekeeper`
- Product: local, keyboard-driven terminal dashboard for monitoring coding-agent harnesses.
- Current harness support: Codex, GitHub Copilot CLI, and OpenCode process discovery. Codex and Copilot also have session metadata adapters.
- Current platform focus: macOS. Terminal switching supports Ghostty, Terminal.app, and iTerm2.

Keep Firekeeper compatible with normal agent CLI workflows. Running `firekeeper` with no subcommand must stay free of daemons, proxies, wrapper commands, remote flags, and changed launch procedures, and must never upload anything. Transcript reporting (see "Reporting and dashboard") is an explicit, opt-in set of subcommands: `snapshot`, `export`, `report`, `backfill`, `daemon`, and `serve`. Do not make agent CLIs depend on any of them.

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

Package is intentionally `main`; tests can exercise unexported helpers directly. The reporting and dashboard code is the one exception: it lives in `internal/` packages (see "Reporting and dashboard"), and `main` only dispatches to it.

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

## Reporting and dashboard

Firekeeper is gaining an opt-in way to send session metadata and full transcripts to a dashboard, in phases: a one-shot `report` runner and a `serve` dashboard first, then a `daemon`. Work arrives as numbered tasks (T0.1, T1.2, and so on). This section is the contract every task shares; do not change it as a side effect of a task. If a task needs the contract to change, stop and say so.

### Ground rules

- Go module `github.com/DanBradbury/firekeeper`; the root `main` package must stay installable with `go install github.com/DanBradbury/firekeeper@latest`.
- New code goes in `internal/` packages: `session`, `transcript`, `redact`, `server`, `reporter`, `daemon`, `config`. Keep `main.go` thin. Do not rewrite or reformat existing TUI code.
- No cgo in new code. The server's own database uses `modernc.org/sqlite` with FTS5. Reading provider SQLite files keeps using `sqlite3 -readonly -json` as described above unless a task says otherwise.
- Provider data is read-only, as above, and `CODEX_HOME`, `COPILOT_HOME`, and `KIMI_CODE_HOME` are honored.
- Never commit real transcripts. Fixtures are synthetic or fully scrubbed.
- Tests never touch the network or the real home directory. Use `t.TempDir()` and fixture trees.
- Never print transcript text, prompts, or raw event payloads in logs, errors, or test output.
- Verify with the commands in "Build and verification". Add `go test -race ./...` for server, reporter, daemon, and parser work.
- For a dispatched task, work on branch `dash/<task id>-<slug>`, touch only the files the task lists, and flag anything else in the PR description. Never push to `main`. Outside a dispatched task, the "do not commit or push" rule above still applies.
- Confirm real file and function names in the repo before editing; task briefs name them from the README and may be stale.

### Privacy and opt-in

- Upload is opt-in per provider. With no provider allowlisted, `report` and `daemon` upload nothing, and `report --dry-run` shows what would be sent.
- Every event passes through `internal/redact` before leaving the machine. Redaction is best effort, and docs must say so plainly.
- Sessions in excluded directories, or in repositories containing `.firekeeper-ignore`, are never read or uploaded.

### Normalized event

One record per transcript event. The idempotency key is `(machine_id, session_id, seq)`.

```json
{
  "machine_id": "string",
  "session_id": "string, provider-native id",
  "provider": "codex | copilot | kimi | claude",
  "seq": 0,
  "ts": "RFC3339 or null",
  "role": "user | assistant | tool_call | tool_result | system | meta",
  "text": "string, may be empty",
  "tool_name": "string or null",
  "model": "string or null",
  "tokens": { "input": 0, "output": 0, "cache": 0 },
  "raw": {}
}
```

- `seq` is the zero-based index of the event in its source and must be identical every time the same source is re-read. For JSONL it is the line index; for SQLite sources it is row order by a stable key.
- `raw` is the original record after redaction. Parsers never drop a record they cannot interpret; they emit it with role `meta`.
- `text` is what a human would read. Tool output longer than 64 KiB is truncated with a trailing `[truncated N bytes]` marker, while `raw` keeps the original.

### Session metadata

`machine_id`, `session_id`, `provider`, `cwd`, `project` (basename of the Git root or cwd), `branch`, `model`, `state` (`ACTIVE | WAITING | NEEDS_INPUT | ENDED | UNKNOWN`), `title`, `started_at`, `last_activity_at`, `event_count`, and token totals `input`, `output`, `cache`. This extends `sessionInfo`; keep the existing state semantics above.

### HTTP API (v1)

| Method and path | Purpose |
| --- | --- |
| `POST /v1/ingest` | Upsert session metadata and append events. Body `{machine, sessions: [{meta, events}]}`, at most 500 events and 5 MiB per request. Returns `{accepted, duplicates}`. Idempotent on the event key. |
| `POST /v1/heartbeat` | Body `{machine, sessions: [{session_id, state, last_activity_at}]}`. Marks the machine online. |
| `GET /v1/sessions` | List with filters `machine`, `provider`, `project`, `state`, `q`, `limit`, `cursor`. Newest activity first. |
| `GET /v1/sessions/{uid}` | One session's metadata. `uid` is `<machine_id>:<session_id>`, URL-escaped. |
| `GET /v1/sessions/{uid}/events` | Events with `after_seq` and `limit` (default 200, max 1000). |
| `GET /v1/machines` | Each machine with `last_heartbeat_at` and session count. |
| `GET /v1/stream` | Server-sent events: `session.updated`, `event.appended`, `machine.status`. |

`machine` is `{id, name, hostname, os, version}`. The server listens on `127.0.0.1:7777` by default with no auth until bearer tokens land. Errors are JSON `{error, code}` with a standard HTTP status.

### Local state

The reporter and daemon keep per-file read offsets in `~/.firekeeper/state.json`: `{"files": {"<abs path>": {"offset": 0, "size": 0, "mtime": "...", "last_seq": 0}}}`. A file smaller than its stored size means truncation or rotation: reset the offset to 0 and rely on server-side idempotency. The per-machine id lives in `~/.firekeeper/machine-id`.

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
