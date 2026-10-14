# Cleanup and restructure plan

Status: tasks 1–12 implemented on branch `dash/cleanup-restructure` (uncommitted), against `main` at `bc0d56b`. Task 6 still needs the repository owner's approval before merge. See "Implementation notes" at the end for the decisions made while implementing.

This plan covers removing dead, unused, and redundant code, tightening the local and CI checks, and splitting the root `main` package into `internal/cli` and `internal/tui`. Each task is one pull request. Tasks are ordered so that small, independent changes land first and the large move lands last, after everything it depends on.

Out of scope here: the remaining `AGENTS.md` documentation fixes (code map, the "only Processes and Usage views" line, test-location guidance), a separate `fmt` CI job, and a pre-commit hook.

## How the findings were gathered

All numbers below come from one-off runs against `bc0d56b`. None of these tools is adopted as a project dependency or CI step.

| Tool | Command | Used for |
| --- | --- | --- |
| staticcheck 2026.2.2 | `go run honnef.co/go/tools/cmd/staticcheck@latest ./...` | unused identifiers (`U1000`), error-string style (`ST1005`) |
| deadcode | `go run golang.org/x/tools/cmd/deadcode@latest [-test] ./...` | functions unreachable from `main`, with and without tests as callers |
| shellcheck | `shellcheck $(git ls-files '*.sh')` | shell script findings |
| scratch move | the cli/tui split applied to a copy of the repo, then `go build`, `go vet`, `go test` | which names cross the new package boundary |

Re-run the relevant command before starting a task; the counts will drift as `main` moves.

## Summary

| # | Task | Depends on | Risk |
| --- | --- | --- | --- |
| 1 | Delete dead code | none | low |
| 2 | Remove the redundant session adapter layer | 1 | medium |
| 3 | Reorder `just check` | none | low |
| 4 | Decouple the server from process discovery | none | medium |
| 5 | Create `internal/version` and fix ldflags | none | low, but fails silently if wrong |
| 6 | Owner sign-off on the `AGENTS.md` contract change | none | none (decision) |
| 7 | Split the root package into `internal/cli` and `internal/tui` | 1, 2, 5, 6 | medium (large, mechanical) |
| 8 | Convert existing `t.Skip` calls | none | low |
| 9 | Fail the Linux CI job on skipped integration tests | 8 | low |
| 10 | Fix shellcheck findings | none | low |
| 11 | Rename stuttering types (optional) | 4, 7 | low (mechanical) |
| 12 | Capitalized error strings (optional) | none | none, see finding |

Minimum useful set: tasks 1–4. They pay off even if the split in task 7 never happens.

## 1. Delete dead code

Code that nothing calls, including tests. Deleting it changes no behavior.

**Unreachable functions** (`deadcode -test`, 40 total):

- `session_bridge.go`: 22 private wrappers, for example `enrichCodexSessions`, `classifyProcess`, `readThreadMetadata`, `discoverOpenRollouts`, `toSliceprocessGroup`.
- `internal/session/compat.go`: the 17 exported helpers those wrappers called, for example `EnrichCodexSessions`, `ClassifyProcess`, `ReadThreadMetadata`, `NewestTime`.
- `internal/session/status.go:39`: `discoverer.readRolloutSessionState`.

**Unused constants, types, and fields** (staticcheck `U1000`):

- `copilot_sessions.go`: `copilotEventTailBytes`, `copilotLogHeadBytes`, `copilotLogTailBytes`, type `copilotEvent`.
- `session_status.go`: `rolloutStatusTailBytes`, `rolloutMetaHeadBytes`, types `rolloutSessionMetaRecord`, `rolloutStatusEvent`.
- `kimi_sessions.go`: type `kimiLatestSession`.
- `main.go:814`: type `threadMetadataRow`.
- Test files: field `inviteIssuer` (`cmd_admin_test.go:117`) and field `otherMach` (`internal/server/auth/auth_test.go:19`).

Verify: `go build ./...`, `go vet ./...`, `go test -race ./...`, and a re-run of staticcheck and deadcode showing these entries gone.

## 2. Remove the redundant session adapter layer

When discovery moved into `internal/session`, two compatibility layers were left in place so existing callers kept working:

```text
root TUI code
  → session_bridge.go (277 lines): private wrappers plus to*/from* type converters
    → internal/session/compat.go (125 lines): exported helpers
      → the real implementation (discoverer methods)
```

`session_status.go`, `copilot_sessions.go`, and `kimi_sessions.go` are now only type and constant aliases onto `internal/session`.

After task 1, what is left of these layers is reachable only from tests (`deadcode` without `-test`): 20 wrappers in `session_bridge.go` and 11 helpers in `compat.go`, such as `scanRolloutSessionState`, `dropSupersededForkRollouts`, `scanCopilotEvents`, `parseProcesses`, and `discoverProcesses`. Root tests call these wrappers about 30 times, so they test `internal/session` through two layers of indirection.

Plan:

1. Move the root tests that exercise wrappers into `internal/session`, calling the real functions. Delete a test where an equivalent test already exists there.
2. Delete the wrappers that no longer have callers, and the alias files once nothing refers to them.
3. Keep the `compat.go` helpers that `internal/transcript/copilot` still uses (7 call sites in `enumerate.go` and `source.go`), or move them next to their real implementation. Do not keep them under a "compatibility" label.

**Also reachable only from tests, review case by case** (these are outside the adapter layer and may be legitimate API that only tests exercise today):

- `internal/redact`: `Kinds`, `Counts.Add`, `JSON`.
- `internal/server/store`: `Store.SchemaVersion`, `Store.Ingest`, `Store.Stats`, `Store.SearchText`.
- `internal/session/status.go`: `SessionState.UnmarshalJSON`, `discoverer.scanRolloutSessionState`.

For each: delete it, move it into an `export_test.go` file if it exists only to help tests, or keep it with a note on why.

**Possible duplication, decide before changing:** `internal/daemon/lock_unix.go` and `internal/reporter/lock_unix.go` both wrap `flock`, with different behavior (the daemon writes its pid and checks whether the owner is alive; the reporter only tries the lock). Merging them would need a shared package, so only do it if a third caller appears.

Verify: as task 1, plus `go test -race ./internal/session/... ./internal/transcript/...`.

## 3. Reorder `just check`

`justfile:41` runs the slowest step first:

```just
check: test vet fmt-check diff-check
```

Change it to run fast checks first, matching the order in `AGENTS.md`:

```just
check: fmt-check vet test diff-check
```

Verify: `just check` passes; break formatting in one file and confirm it fails in seconds without running tests.

## 4. Decouple the server from process discovery

The dashboard server links process-discovery code it never runs:

```text
server/store, server/api → transcript → session (ps, lsof, os/exec)
```

The server only uses the event contract from `transcript`: `Event`, `Provider` and its constants, the `Role` constants, `Tokens`, and `TouchedFiles` (`event.go`, `files.go`). The dependency on `session` comes from the reader registry in the same package: `source.go` and `enumerate.go` use `session.Meta` and `session.Warning`.

`session.Meta` cannot simply move into `transcript`, for two reasons:

- It embeds `Runtime ProcessGroup`, a process-discovery type, so `transcript` would still need `session`.
- `session.Discover` builds and returns `Meta`. With `Meta` in `transcript`, `session` would import `transcript` while `transcript` imports `session`, and Go rejects import cycles.

Two import rules follow, enforced differently:

- No cycles: `reader` may import `transcript` and `session`, and neither of them may import `reader`. The Go compiler enforces this; a cycle does not build.
- Decoupling: `transcript` must not import `session`. This is the goal of the task, not a cycle rule (a one-way import compiles fine), so the compiler does not catch it; the CI guard below does.

Decision: keep `internal/transcript` as the event contract (`event.go`, `files.go`) and move the session-dependent reader registry into a new subpackage, `internal/transcript/reader`:

- Move `source.go`, `enumerate.go`, and their tests, as `package reader`. Inside them, `Provider` and `Event` become `transcript.Provider` and `transcript.Event`.
- Keep every name; only the package changes, for example `transcript.For` → `reader.For`. The moving symbols are `TranscriptSource`, `Register`, `Registered`, `For`, `EnumeratorFor`, `Enumerator`, `EnumerateOptions`, `HeadBytes`, `ProjectName`, and `SortNewestFirst`. The `TranscriptSource` → `Source` rename stays in task 11.
- Move `enumerate.go` whole. `HeadBytes` and `ProjectName` do not use `session` and could stay in `transcript`, but they are only used by the provider enumerators, so they move with the rest of the enumeration helpers.
- Callers that switch to `reader`: `chat.go`, `internal/reporter/reporter.go`, `internal/reporter/backfill.go`, the provider packages `internal/transcript/{claude,codex,copilot}`, and `internal/transcript/transcripttest`, plus tests in `e2e/`, `internal/daemon`, `internal/reporter`, and `internal/server/store`. Production code in the server, `redact`, and `config` stays unchanged.
- Provider packages keep registering themselves in `init()`; only the package in `reader.Register` changes. Update the `"transcript: Register…"` panic messages to name `reader`.
- `reader` imports `transcript` and `session`, and neither imports `reader`, so there is no cycle. The server's tests may still link `session` through `reader.For`; production binaries do not.

The JSON shape of events and session metadata must not change; this is a Go package move only.

Verify: `go list -deps ./internal/transcript | grep internal/session` and `go list -deps ./internal/server/... | grep internal/session` both print nothing (`go list -deps` ignores test files); `go test -race ./...`; `e2e/smoke_test.go` passes.

Add a CI guard in the same PR, so the dependency cannot quietly return. No other check catches it: a new `session` import compiles and passes every test. Add this step to the `test` job in `.github/workflows/ci.yml`, after `go vet`:

```yaml
      - name: server does not depend on process discovery
        run: |
          if go list -deps ./internal/server/... | grep -q 'firekeeper/internal/session$'; then
            echo "internal/server must not depend on internal/session (process discovery)" >&2
            exit 1
          fi
```

The guard must land with this task or after it, never before: on the current code `go list` still finds `internal/session`, so the step would fail every PR. It checks production code only (`go list -deps` ignores test files), so the store tests that read fixtures through `reader.For` are unaffected.

## 5. Create `internal/version` and fix ldflags

`version`, `commit`, and `date` live in package `main` (`version.go`). Two places read them: the `version` subcommand and the TUI dashboard header (`dashboard.go:563`). After task 7 these are different packages, and Go does not allow importing package `main`, so the variables need a shared leaf package:

```go
// internal/version/version.go
package version

// Set at build time with -ldflags "-X github.com/DanBradbury/firekeeper/internal/version.Version=...".
var (
	Version = "dev"
	Commit  = "dev"
	Date    = "dev"
)
```

Update the build-time paths, or release binaries silently report `dev` (the Go linker ignores an `-X` flag that names a missing variable; this was confirmed in a scratch build):

- `.goreleaser.yaml:24-26`: `-X main.version` → `-X github.com/DanBradbury/firekeeper/internal/version.Version`, and the same for `Commit` and `Date`.
- `docs/releasing.md:77`.
- `AGENTS.md` build section.

Add a CI check to the `snapshot` job that runs the built binary's `version` command and fails if it reports `dev`.

Alternative that avoids the ldflags change: keep the variables in `main` and pass the version into `cli` and `tui` as a parameter. Either works; the shared package is the more common Go pattern.

Verify: a snapshot build reports the injected version, not `dev`.

## 6. Owner sign-off on the AGENTS.md contract change

`AGENTS.md:276` says: "Do not rewrite or reformat existing TUI code." It sits in the "Reporting and dashboard" contract, which tasks must not change as a side effect. Task 7 moves the TUI into `internal/tui`, so the rule needs rewording first, for example:

```markdown
- New code goes in `internal/` packages: `session`, `transcript`, `redact`, `server`, `reporter`, `daemon`, `config`, `cli`, `tui`, `version`. Keep `main.go` thin. The TUI lives in `internal/tui`; do not rewrite or reformat it unless a task asks for that.
```

This needs the repository owner's approval. It can be its own PR or part of task 7.

## 7. Split the root package into `internal/cli` and `internal/tui`

A move-only change: `git mv` and package renames, no logic edits.

| Destination | Files (with their `_test.go` files) |
| --- | --- |
| `internal/cli/` | `cli.go`, `cmd_*.go` (12 files) |
| `internal/tui/` | `main.go` (TUI parts, renamed `model.go`), `dashboard.go`, `chat.go`, `help.go`, `herdr.go`, `codex_usage.go`, `copilot_usage.go`, `kimi_usage.go`, `terminal_darwin_test.go`, and any adapter or alias files task 2 left behind |
| `internal/version/` | `version.go` (task 5) |
| root | `main.go`, rewritten to about 20 lines |

The scratch move showed the CLI and TUI never call each other. Apart from `version` (task 5), only three names cross the boundary, all used by `func main()`, and they become exported: `runSubcommand` → `cli.RunSubcommand`, `printUsage` → `cli.PrintUsage`, `newModel` → `tui.NewModel`.

Other edits the move needs:

- `.github/workflows/ci.yml`: the `macos` job runs its three required AppleScript tests against the root package (`go test … .`); point it at `./internal/tui`. If this is missed, the job fails loudly because it checks each test reports `PASS`.
- `AGENTS.md`: code map and the "Package is intentionally `main`" paragraph.

The install path does not change: `main.go` stays at the root, so `go install github.com/DanBradbury/firekeeper@latest` keeps working.

Verify: `go build ./...`, `go vet ./...`, `GOOS=darwin go vet ./...`, `go test -race ./...`, `FIREKEEPER_MACOS_INTEGRATION=1 go test ./internal/tui` on macOS, and `git diff -M --stat` showing renames rather than rewrites.

## 8. Convert existing `t.Skip` calls

`AGENTS.md` now says integration tests must not skip in CI: locally a missing optional tool may skip, but when `CI` is set the same condition fails with a clear message. Platform-specific tests use build constraints instead of runtime skips. The repository has 13 `t.Skip` calls:

| Kind | Where | Change |
| --- | --- | --- |
| Missing tool | `sqlite3` ×3 (`codex_usage_test.go`, `copilot_usage_test.go`), `bash` (`main_test.go`) | skip locally, fail when `CI` is set |
| Wrong platform | BSD/Linux `ps` and AppleScript (`main_test.go`), daemon service (`cmd_serve_local_test.go`), signals (`internal/daemon/restart_test.go`) | move to files with build constraints |
| Opt-in gate | `FIREKEEPER_MACOS_INTEGRATION` (`terminal_darwin_test.go`) | keep; the `macos` CI job already fails if these skip |
| Environment | Automation permission (`terminal_darwin_test.go`), state lock (`internal/reporter/backfill_test.go`) | decide per test |
| Not real skips | subprocess helper pattern (`internal/daemon/restart_test.go`), opt-in benchmark (`internal/server/store/usage_bench_test.go`) | leave as is |

A small helper avoids repeating the `CI` check, in the style of the Go project's own `internal/testenv`:

```go
// NeedTool skips the test locally when name is not on PATH, and fails it in CI.
func NeedTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is required in CI: %v", name, err)
		}
		t.Skipf("%s unavailable", name)
	}
}
```

Decision needed: put it in a small `internal/testenv` package, or repeat it in the few packages that need it.

Verify: tests pass locally with and without `sqlite3` on `PATH`; with `CI=1` and `sqlite3` hidden, the affected tests fail with the message.

## 9. Fail the Linux CI job on skipped integration tests

The `macos` job already fails if its three AppleScript tests skip. The Linux `test` job passes silently if, for example, a `sqlite3` test skips. After task 8, skips in CI become failures inside the tests themselves. Add a backstop step to the `test` job that runs `go test -json ./...` and fails on any `"Action":"skip"` event outside a short allowlist (the helper-process and benchmark cases from task 8).

Verify: temporarily add a `t.Skip` to a test on a branch and confirm the job fails.

## 10. Fix shellcheck findings

`scripts/third-party-notices.sh` has 5 findings. The other five scripts are clean.

- 4 × SC2016 (lines 17, 28, 29, 30): "expressions don't expand in single quotes." These are intentional; the strings are Markdown with literal backticks. Suppress each with a `# shellcheck disable=SC2016` directive and a short comment.
- 1 × SC2010 (line 49): `ls | grep` to find license files. Replace with a glob or `find`, which also handles unusual file names.

Optional, after the fix: widen the CI step from `shellcheck install.sh scripts/test-install.sh` to `shellcheck $(git ls-files '*.sh')` so every script is checked.

Verify: `shellcheck $(git ls-files '*.sh')` is clean; `just notices` produces an unchanged `THIRD_PARTY_NOTICES.md`.

## 11. Rename stuttering types (optional)

Go style avoids repeating the package name in exported names:

| Current | New |
| --- | --- |
| `session.SessionInfo` (`internal/session/types.go:91`) | `session.Info` |
| `session.SessionState` (`internal/session/types.go:147`) | `session.State` |
| `reader.TranscriptSource` (`internal/transcript/reader/source.go`, after task 4) | `reader.Source` |

Do this after tasks 4 and 7 so the renames do not conflict with the file moves. Mechanical; `gopls rename` handles it.

## 12. Capitalized error strings (optional, recommend no change)

staticcheck reports 6 error strings starting with a capital letter (`ST1005`): 5 in `codex_usage.go` (lines 232, 257, 278, 312, 450) and 1 in `internal/session/copilot.go:257`. Each starts with a product name, for example `"Codex usage request timed out"` and `"Copilot session marker not found"`. Go's style guidance allows a capital letter when the string begins with a proper noun, so these are acceptable as written. Lowercasing `codex` would misspell the product name. Leave them unless a later change rewords them for another reason.

## Verification for every PR

Run the local checks from `AGENTS.md` in order:

```sh
gofmt -w .
go build ./...
go vet ./...
go test -race ./...
git diff --check
```

Also run `shellcheck` on any touched script, and keep `e2e/smoke_test.go` passing for changes to the report pipeline. Merging needs the `test`, `macos`, `installer`, and `snapshot` checks to pass, as required by the ruleset on `main`.

## Decisions

1. Task 4: `internal/transcript/reader` holds the reader registry.
2. Task 5: a shared `internal/version` package, not a parameter.
3. Task 6: the `AGENTS.md` rule is reworded in the same change as task 7; the owner still has to approve it.
4. Task 8: a small `internal/testenv` package (`NeedTool`, `InCI`), shared by `internal/tui` and `internal/session`.
5. Task 10: CI's shellcheck step now checks every tracked script.
6. Task 11 is done; task 12 makes no change.

## Logistics

- Branches follow `AGENTS.md`: `dash/<task id>-<slug>`, one task per PR, touching only the files the task lists.
- Contributors with read-only access open these PRs from a fork. Task 6 needs the repository owner.
- 10 PRs for tasks 1–10, or 9 if task 6 folds into task 7. Task 11 adds one more if taken; task 12 recommends no change, so it needs none.

## Implementation notes

What changed relative to the task descriptions above, and why.

- **Task 1.** Also removed an orphaned doc comment for `dropSupersededForkRollouts` left in `main.go` after the function moved to `internal/session`.
- **Task 2.**
  - The root wrappers that production code still calls (`sanitizeProcessCommand`, `emptyFallback`, `copilotHome`, `kimiHome`, `codexStatePath`, `normalizeTTY`, `parseCopilotTime`, `firstNonEmpty`) and the `from*` converters stay in `internal/tui/session_bridge.go`; the dashboard keeps its private types. Everything else in the bridge, `compat.go`, and `copilot_sessions.go` is gone. The alias files `session_status.go` and `kimi_sessions.go` stay, trimmed, because the dashboard still refers to the aliases they hold.
  - The 12 exported helpers still used outside `internal/session` moved next to their implementations (`codex.go`, `copilot.go`, `processes.go`, `kimi.go`), built on a `standalone()` discoverer.
  - The root tests that went through the wrappers moved to `internal/session` (`copilot_test.go`, `status_test.go`, `processes_test.go`, `processes_unix_test.go`) and call the real methods. `TestCopilotProcessDetailsShowProviderMetadata` is a dashboard test and stayed with the TUI.
  - Case-by-case review: `redact.Counts.Add` deleted (no callers); `discoverer.scanRolloutSessionState` deleted (tests call `scanRolloutStatus`); `Store.SchemaVersion` moved into `store_test.go` (only its own package's tests use it). Kept: `redact.Kinds` and `redact.JSON` (public API the package tests exercise), `Store.Ingest`, `Store.Stats`, `Store.SearchText` (tests in other server packages seed and inspect data with them), and `State.UnmarshalJSON` (the counterpart of `MarshalJSON` in the session metadata JSON). Each kept function's comment says why it stays.
  - The two `lock_unix.go` files were left as they are, as the plan says.
- **Task 4.** Moved exactly as described; `go list -deps` for `internal/transcript` and `internal/server/...` no longer includes `internal/session`.
- **Task 5.** The build information moved to `internal/version` (`Version`, `Commit`, `Date`, `String()`); the `version` command handler became `internal/cli/cmd_version.go`. Besides the dashboard header, `cmd_config.go` (reporter version) and `cmd_serve_local.go` (local machine record) also read the version.
- **Task 7.** `TestDiscoverProcessesPreservesLongCopilotCommand` moved to `internal/session` in task 2, so the `macos` job now runs its required tests in `./internal/tui ./internal/session`. Only `RunSubcommand`, `PrintUsage`, and `NewModel` cross the boundary; `internal/cli` and `internal/tui` do not import each other.
- **Task 8.** `sqlite3` and `bash` use `testenv.NeedTool`. Platform skips became build constraints: `TestMacTerminalAdapterScriptsCompile` moved into `terminal_darwin_test.go`, `TestOfferLocalReporting` into `cmd_serve_local_daemon_test.go` (`darwin || linux`, and it fails if `daemon.Supported` disagrees), `TestKillAndRestartMidBatch` and its helper `TestHelperDaemon` moved into `restart_unix_test.go` (`unix`), and `TestStateLockTimesOut` moved into `backfill_lock_unix_test.go` with `lock_unix.go`'s constraint and now fails instead of skipping. In `terminal_darwin_test.go` the opt-in gate and the Automation-permission check still skip locally but fail when `CI` is set, matching the `AGENTS.md` rule; the `macos` job sets `FIREKEEPER_MACOS_INTEGRATION=1`, so it is unaffected.
- **Task 9.** `scripts/ci-go-test.sh` runs `go test -json`, prints package results, build errors, and failed tests' output, and fails on any skip outside its allowlist (`TestHelperDaemon`, `TestUsageSpeed`). The `test` job installs `sqlite3` if the runner lacks it, since a missing tool now fails tests in CI.
- **Task 10.** `just notices` output is unchanged when run with the Go version the committed file was generated with and `LC_ALL=C`. Under other locales `sort -u` orders modules differently; that was true before this change.
- **Verification limits.** `just` is not installed locally, so `just check` was exercised by running its recipes in the new order. Docker was not running, so the Linux test run and `scripts/ci-go-test.sh` on the full suite were not run on Linux locally; cross-compiled `go vet` for Linux passed, and the script was exercised on macOS. The snapshot path and the version check were verified with a local GoReleaser build.
