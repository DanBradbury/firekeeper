# Cleanup and Restructure Review 1 Resolution

Review: [`review-1.md`](review-1.md)

## Resolutions

1. Staged every file this change produced, including the new root `main.go`,
   `internal/version/`, `internal/testenv/`, `scripts/ci-go-test.sh`, the new
   tests, the plan, and these review notes. A checkout of the index alone now
   builds, vets, and passes its tests. `AGENTS.md`, `CLAUDE.md`, and the
   `old-*.md` drafts stay unstaged because they hold the owner's own edits.
2. Separated the `session_bridge.go` file comment from the package clause and
   added package documentation for `internal/tui` and `internal/cli`.
3. Not changed. The `|---|---|` separator was already in the owner's
   uncommitted `AGENTS.md` edits before this work started; it is left for the
   owner rather than reverted.
4. `requireMacOSIntegration` now fails when `CI` is set and still skips
   locally, matching the `AGENTS.md` rule. The `macos` job sets the variable, so
   it is unaffected.
5. Restored each changed file's original import grouping from `HEAD`, adding
   the `internal/transcript/reader` import to the existing first-party group.
   The unrelated reorders in `kimi_usage.go` and `exclude_test.go` are gone.
6. Only `TestKillAndRestartMidBatch` and `TestHelperDaemon` moved to
   `restart_unix_test.go`; `restart_test.go` has no build constraint again and
   builds for Windows.
7. Updated the `Store.SearchText` comment and added notes on why
   `redact.Kinds`, `redact.JSON`, and `State.UnmarshalJSON` are kept; the plan's
   implementation notes now match.
8. Kept. Tests in `internal/tui` use the concrete `model` that `NewModel`
   returns, and `main` only needs it as a `tea.Model`.

The reviewer's two questions (the extra `AGENTS.md` sections, and
`gofmt -w .` versus changed files only) are owner decisions and are raised
with the owner, not changed here.
