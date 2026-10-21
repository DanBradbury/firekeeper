# AGENTS.md

## Project identity

- Project: **Firekeeper**
- Go module: `github.com/DanBradbury/firekeeper`
- Command: `firekeeper`
- Product: local, keyboard-driven terminal dashboard for monitoring coding-agent harnesses.
- Current harness support: Codex, GitHub Copilot CLI, Claude Code, Kimi Code, OpenCode, Gemini CLI, and Cursor CLI process discovery, each with a session metadata adapter.
- Current platform focus: macOS. Terminal switching supports Ghostty, Terminal.app, and iTerm2.

Keep Firekeeper compatible with normal agent CLI workflows. Running `firekeeper` with no subcommand must stay free of daemons, proxies, wrapper commands, remote flags, and changed launch procedures, and must never upload anything. Transcript reporting (see "Reporting") is an explicit, opt-in set of subcommands: `snapshot`, `export`, `report`, `backfill`, `daemon`, and `login`. `serve` is a stub: the dashboard server lives in the private `firekeeper-server` repo. Do not make agent CLIs depend on any of them.

## Before changing code

1. Read `README.md` for current user-facing behavior and controls.
2. Check `git status --short` and preserve unrelated or pre-existing changes.
3. Locate tests next to the relevant implementation before editing.
4. Treat provider data (`~/.codex`, `~/.copilot`, `~/.claude`, `~/.kimi-code`, OpenCode's database, `~/.gemini`, `~/.cursor`) as private user data. Never print prompts, secrets, raw event payloads, or full command lines unnecessarily.

Do not commit, push, create issues, or modify remote state unless the user asks.

## Build and verification

Go 1.26 or newer is declared in `go.mod`.

Run these local checks in order before handoff. The fast checks come before the tests, so most failures show up early:

```sh
gofmt -w .
go build ./...
go vet ./...
go test -race ./...
git diff --check
```

Use `-race` for changes involving model updates, polling, commands, parsing, provider adapters, reporter, or daemon. For other changes, plain `go test ./...` is enough.

When creating or modifying shell scripts (`*.sh`), run `shellcheck` on each touched script and fix its findings. CI also runs `shellcheck` on every tracked script.

GitHub CI runs more than the local checks: Linux tests through `scripts/ci-go-test.sh` (which fails on any skipped test outside a short allowlist), macOS AppleScript tests (set `FIREKEEPER_MACOS_INTEGRATION=1` to run them locally), the installer test, a GoReleaser snapshot build that must report the injected version, and (in `security.yml`, also weekly) `govulncheck` plus a `gitleaks` scan of the full history. A test fixture or doc example that trips gitleaks gets a narrow allowlist entry in `.gitleaks.toml`. CI uses the Go version declared in `go.mod`, so do not use APIs newer than that version. A green local run usually means CI passes, but does not guarantee it.

Release builds set version information with `-ldflags "-X github.com/DanBradbury/firekeeper/internal/version.Version=..."` (also `Commit` and `Date`). A wrong `-X` path fails silently and the binary reports `dev`.

Useful manual check:

```sh
go run .
```

Live process and terminal-switch checks are macOS-specific and may need Automation permission.

## Code map

```text
firekeeper/
├── main.go
│   └── Entry point: runs a subcommand from internal/cli, otherwise starts
│       the dashboard from internal/tui
├── internal/
│   ├── cli/
│   │   ├── cli.go: subcommand dispatch and usage
│   │   └── cmd_*.go: one file per subcommand (report, daemon, login, version, ...)
│   ├── tui/
│   │   ├── model.go: Bubble Tea model/update/view, Processes and Usage tabs,
│   │   │   process polling through session.Discover, retention of known
│   │   │   session metadata, macOS terminal switching
│   │   ├── dashboard.go: dashboard layout for the Processes tab
│   │   ├── chat.go: the c view, the selected session's chat history
│   │   ├── help.go: the ? overlay listing every keyboard shortcut
│   │   ├── herdr.go: terminal switching through herdr
│   │   ├── codex_usage.go: short-lived Codex app-server requests and Codex usage UI
│   │   ├── copilot_usage.go: read-only Copilot session-store.db aggregation and
│   │   │   the shared Usage-tab provider UI
│   │   ├── kimi_usage.go: read-only Kimi usage and its Usage-tab UI
│   │   └── session_bridge.go, session_status.go, kimi_sessions.go: the
│   │       dashboard's private types and helpers onto internal/session
│   ├── session/
│   │   └── Process discovery and grouping, per-provider metadata enrichment,
│   │       and best-effort state parsing into ACTIVE, WAITING, NEEDS INPUT,
│   │       or UNKNOWN
│   ├── transcript/
│   │   ├── Aliases of the public pkg/event types; no session import
│   │   ├── claude/, codex/, copilot/, cursor/, kimi/, opencode/, gemini/: per-provider
│   │   │   transcript parsers
│   │   ├── reader/: reader registry, provider lookup, and enumeration. reader
│   │   │   and the provider packages import internal/session; the transcript
│   │   │   package itself must not
│   │   └── transcripttest/: shared test helpers for parsers
│   ├── redact/
│   │   └── Redaction before data leaves the machine
│   ├── reporter/
│   │   └── One-shot transcript reporting and backfill
│   ├── daemon/
│   │   └── Background reporting and local state tracking
│   ├── config/
│   │   └── Reporting and provider configuration
│   ├── version/
│   │   └── Build information set at link time
│   └── testenv/
│       └── Test helpers: skip locally, fail in CI when a tool is missing
├── pkg/
│   ├── event/: public Event, Tokens, Role, Provider, TouchedFiles (stable contract)
│   └── redact/: public redaction entry points (stable contract)
└── scripts/
    └── Installer test, CI test runner, third-party notices

The dashboard server, web UI, accounts, storage and deployment are not in this repository; they live in the private `firekeeper-server` repo, which imports only `pkg/event` and `pkg/redact`. Tests here use `internal/testenv/fakeapi`, an in-memory stand-in that follows `docs/api.md`.
```

The root `main` package only wires the CLI and the dashboard together; `internal/cli` and `internal/tui` never import each other. Most tests sit in the same package as the code, so they can exercise unexported helpers directly. The reporting code lives in the other `internal/` packages (see "Reporting").

The tree describes architectural responsibilities. Confirm actual file and function names in the repository before editing.

## Bubble Tea architecture

- Keep `Update` fast and non-blocking. Filesystem reads, subprocess calls, and provider refreshes belong in `tea.Cmd` functions returning typed messages.
- Process polling runs every two seconds through `pollProcesses` and `refreshProcesses`.
- Preserve selection by root PID across refreshes.
- Preserve last known Codex/Copilot session metadata when a transient refresh returns no metadata, for at most `sessionMetadataGraceRefreshes` consecutive refreshes, so a session closed inside a long-lived runtime (such as the Codex app-server daemon) still disappears. See `retainKnownSessionMetadata`.
- Treat provider metadata and state as best effort. Partial data should remain useful and should not crash or blank unrelated UI.
- Keep terminal output width-safe. Use existing truncation, padding, sanitization, and `fitLine` helpers.

## Process and provider integrations

Process discovery uses `ps`; open-file mapping uses `lsof`; local database queries use `sqlite3 -readonly -json`; terminal focus uses `osascript`. Invoke commands with argument arrays, never shell interpolation.

Provider rules:

- Read local provider data only. Never mutate provider databases, rollout files, logs, or workspace metadata.
- Honor `CODEX_HOME`, `COPILOT_HOME`, `KIMI_CODE_HOME`, `CLAUDE_CONFIG_DIR`, `OPENCODE_DB`, `XDG_DATA_HOME`, `GEMINI_CLI_HOME`, and `CURSOR_CONFIG_DIR` overrides.
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

## Go type design and error handling

Follow idiomatic Go. Prefer simple, explicit types and established Go conventions. Use stronger type-system guarantees when they provide meaningful protection rather than imitating Rust's type system wholesale.

### Named types and constants

- Prefer named types and constants for finite sets of domain values.
- Use named string types for values whose string representation crosses JSON, HTTP, configuration, file, or database boundaries.
- Use named integer types with `iota` for internal state when appropriate.
- Prefer named constants over raw literals when referring to recognized domain values.
- Remember that named types do not restrict values to declared constants. A conversion or external input can still produce an unrecognized value.
- Do not introduce wrapper structs solely to simulate enums.
- Preserve existing public types and APIs unless a concrete requirement justifies changing them.

Example:

```go
type Provider string

const (
    ProviderCodex    Provider = "codex"
    ProviderCopilot  Provider = "copilot"
    ProviderOpenCode Provider = "opencode"
    ProviderGemini   Provider = "gemini"
    ProviderCursor   Provider = "cursor"
)
```

Use these constants throughout the codebase instead of repeating raw string literals for recognized providers.

### Construction and validation

- Parse and validate untrusted external values at application boundaries.
- Provide a canonical `Parse` function for finite domain values when appropriate.
- Reject unknown values explicitly instead of silently mapping them to a valid value.
- Pass domain types through internal APIs instead of repeatedly converting them to primitives.
- Avoid redundant validation inside trusted code when the required invariant is already guaranteed.
- Revalidate when data crosses an untrusted boundary, its integrity is uncertain, or the operation has additional requirements.
- Keep validation separate from authorization.
- Remember that a named type distinguishes values at compile time but does not guarantee that the value belongs to the declared set.

### Structs and stronger invariants

- Use ordinary structs for related data.
- Use structs with unexported fields and validated constructors when preventing invalid construction provides a meaningful correctness or security benefit.
- Use the struct pattern selectively for new types where an invalid value would be costly and compile-time restrictions justify the additional complexity.
- Define zero-value behavior explicitly for types with invariants. An unexported field does not prevent a zero value from existing.
- Do not migrate existing named types to wrapper structs without a concrete justification and an assessment of compatibility and migration costs.

### Algebraic data types

Go does not have native Rust-style enums with associated data or compiler-enforced exhaustive matching.

- For finite values without associated data, prefer named types and constants.
- For variants carrying different data, consider explicit variant structs behind a package-scoped interface with an unexported marker method, or a carefully validated tagged representation.
- Keep variant definitions and invariants inside the owning package when practical.
- Validate variant-specific invariants during construction or parsing.
- Avoid `any` when a meaningful domain type is available.
- Do not introduce an ADT merely to imitate Rust when ordinary Go types express the domain adequately.
- When adding a variant, update every relevant consumer and its tests.
- Account for zero values and pointer-versus-value implementations.
- Do not assume that a closed interface guarantees exhaustive handling. Go does not natively enforce exhaustive type switches.

Use the struct/interface pattern when the variants carry genuinely different data or when preventing invalid combinations is worth the complexity. Do not apply it to every finite set of strings.

### Exhaustive checking

- Handle every recognized constant explicitly in switches over finite domain types.
- Use an exhaustive-switch analyzer, such as `exhaustive`, in the repository's lint and CI workflow when appropriate.
- Pin tooling versions in reproducible development and CI workflows.
- When adding a constant, update affected switches and tests.
- Do not silently ignore an unhandled value or treat an unsupported case as success.
- Use defensive default branches where needed to handle invalid runtime values; a default branch does not replace exhaustive checking.
- An `All()` function is optional. Add one only when callers need to enumerate valid values, such as building a selector or validating a collection.
- Avoid maintaining duplicated lists of recognized values unnecessarily.
- Where parsing, enumeration, and handling are separate, add tests to detect inconsistencies.
- For interface-based ADTs, supplement linting with focused tests and review. Do not claim compiler-enforced exhaustiveness where Go provides no such guarantee.

### Custom errors

- Return errors explicitly for expected operational and domain failures.
- Use sentinel errors when callers need stable error classification.
- Use custom error types when callers need structured information or error-specific behavior.
- Use `errors.Is` for sentinel errors and `errors.As` for typed errors.
- Wrap errors with `%w` when callers should retain the ability to inspect the underlying error.
- Add useful context when propagating errors across abstraction boundaries.
- Do not inspect error strings to determine error identity or type.
- Avoid creating a separate error type for every trivial failure.
- Do not silently discard errors that affect correctness.
- Avoid logging the same error repeatedly at multiple layers.
- Never expose credentials, transcript contents, prompts, secrets, or other sensitive information through errors or logs.

### Error boundaries

- Distinguish domain failures, infrastructure failures, and programming errors.
- Return errors for invalid external input rather than panicking.
- Translate internal errors into appropriate HTTP or CLI errors at the relevant boundary.
- Preserve error identity when it is part of a package's contract.
- Reserve panics for genuine programming errors or violated internal invariants, not ordinary operational failures.
- Recover from panics only at deliberate process or request boundaries where recovery is part of the application's design.

### Tests and enforcement

When adding or changing domain types, ADTs, or error contracts:

- Test valid values and invalid external input.
- Test parsing and serialization behavior.
- Update relevant switches and tests when adding constants or variants.
- Test error classification with `errors.Is` and `errors.As` where applicable.
- Test constructor invariants and zero-value behavior for encapsulated types.
- Test invalid tagged combinations if a tagged representation is used.
- Run the repository's configured exhaustive-switch analyzer and relevant tests.
- Keep tests deterministic and use synthetic fixtures.
- Do not use real provider data, transcripts, credentials, or home-directory contents in tests.

### Design decision rule

Use the simplest representation that provides the guarantees the domain actually requires.

| Requirement | Default |
|---|---|
| Finite set of strings or integers | Named type and constants |
| Untrusted external value | Parse and validate at the boundary |
| Missing cases in finite-value switches | Exhaustive-switch linting and tests |
| Related structured data | Struct |
| Strongly constrained value | Encapsulated struct when justified |
| Variants with different associated data | Package-scoped interface or validated tagged representation |
| Error classification | Sentinel error or typed error |
| Error propagation | Explicit returns and `%w` wrapping |

Do not introduce wrappers, interfaces, generic frameworks, or additional dependencies without a concrete benefit.

## Reporting

Firekeeper has an opt-in way to send session metadata and full transcripts to a dashboard server: a one-shot `report`, `backfill` for history, and a `daemon`. This section is the contract every task shares; do not change it as a side effect of a task. If a task needs the contract to change, stop and say so. The server is not in this repository.

### Ground rules

- Go module `github.com/DanBradbury/firekeeper`; the root `main` package must stay installable with `go install github.com/DanBradbury/firekeeper@latest`.
- New code goes in `internal/` packages: `session`, `transcript`, `redact`, `reporter`, `daemon`, `config`, `cli`, `tui`, `version`. Keep `main.go` thin. The TUI lives in `internal/tui`; do not rewrite or reformat it unless a task asks for that. The only importable packages are `pkg/event` and `pkg/redact`; keep them dependency-light and do not export more than other programs need.
- No cgo. Reading provider SQLite files uses `sqlite3 -readonly -json` as described above unless a task says otherwise.
- Provider data is read-only, as above, and `CODEX_HOME`, `COPILOT_HOME`, `KIMI_CODE_HOME`, `GEMINI_CLI_HOME`, and `CURSOR_CONFIG_DIR` are honored.
- Never commit real transcripts. Fixtures are synthetic or fully scrubbed.
- Tests never touch the network or the real home directory. Use `t.TempDir()` and fixture trees.
- Never print transcript text, prompts, or raw event payloads in logs, errors, or test output.
- Verify with the commands in "Build and verification". Add `go test -race ./...` for reporter, daemon, and parser work.
- For a dispatched task, work on a branch named for the task, touch only the files the task lists, and flag anything else in the PR description. Never push to `main` after the initial import. Outside a dispatched task, the "do not commit or push" rule above still applies.
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
  "provider": "codex | copilot | kimi | claude | opencode | gemini | cursor",
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

The client talks to a server over the HTTP API in `docs/api.md`, which is the public contract with any compatible server. The client uses `POST /v1/ingest`, `POST /v1/heartbeat`, the device-code link endpoints (`POST /v1/link/start` and `POST /v1/link/poll`), `GET /v1/account` (for `whoami`), and `DELETE /v1/tokens/current` (for `logout`). `machine` is `{id, name, hostname, os, version}`. Errors are JSON `{error, code}` with a standard HTTP status. Server behavior (accounts, tenancy, storage, the web UI) is specified and implemented in the private `firekeeper-server` repo.

### Local state

The reporter and daemon keep per-file read offsets in `~/.firekeeper/state.json`: `{"files": {"<abs path>": {"offset": 0, "size": 0, "mtime": "...", "last_seq": 0}}}`. A file smaller than its stored size means truncation or rotation: reset the offset to 0 and rely on server-side idempotency. The per-machine id lives in `~/.firekeeper/machine-id`.

## Terminal layout

- `chromeRows` reserves tab bar plus footer rows. View content must fit remaining height and handle minimum terminal size.
- The TUI has only Processes and Usage views; it contains no artwork or graphics renderers.

## Testing style

- Prefer deterministic table-driven or focused unit tests.
- Test pure parsers with strings/readers and synthetic provider records.
- Test model behavior by sending Bubble Tea messages and inspecting returned model/view.
- Test errors without exposing machine-specific absolute paths or terminal escape sequences.
- Every bug fix adds a regression test that fails without the fix.
- Changes that cross packages or call external tools (`ps`, `lsof`, `sqlite3`, `osascript`, `git`) get an integration test.
- Integration tests must not skip in CI. Locally, a test may skip when an optional tool such as `sqlite3` is missing or an opt-in variable such as `FIREKEEPER_MACOS_INTEGRATION` is unset, but when `CI` is set (GitHub Actions sets `CI=true`) the same condition is a test failure with a clear message. Limit platform-specific tests with build constraints (for example `_darwin_test.go`) instead of runtime skips. Use `testenv.NeedTool` from `internal/testenv` for missing tools and `testenv.InCI` for other conditions.
- Changes to the report pipeline (discovery, transcript readers, redaction, reporter state and offsets, ingest) are tested against `internal/testenv/fakeapi`, which follows `docs/api.md`. Extend it when the API contract grows.

For user-visible changes, update `README.md` when controls, supported providers, requirements, limitations, or CLI flags change.

## Change discipline

- Keep changes scoped to request.
- Preserve backward-compatible keyboard controls unless user requests redesign.
- Avoid broad refactors while fixing provider drift or rendering bugs.
- Do not overwrite user edits in dirty worktree.
- Do not add generated binaries, database copies, provider logs, account metadata, or secrets.
- Explain remaining platform/provider limitations plainly during handoff.
