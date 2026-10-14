# Cleanup and Restructure Review 1

Plan: [`CLEANUP_AND_RESTRUCTURE_PLAN.md`](../../../CLEANUP_AND_RESTRUCTURE_PLAN.md)

## Verdict

Approved with changes requested. The independent read-only review found no
blockers, two should-fix items, and six nits.

## Findings

1. Should fix: the index is half-staged. The `git mv` renames are staged while
   the new root `main.go`, `internal/version/`, `internal/testenv/`,
   `scripts/ci-go-test.sh`, and the new test files are untracked, so a commit of
   the staged state would not build.
2. Should fix: the file comment in `internal/tui/session_bridge.go` sits
   directly above the package clause and becomes the package documentation for
   `internal/tui`; `internal/cli` has no package documentation.
3. Nit: the HTTP API table separator in the `AGENTS.md` contract section changed
   from `| --- | --- |` to `|---|---|`.
4. Nit: `AGENTS.md` says an unset opt-in variable such as
   `FIREKEEPER_MACOS_INTEGRATION` fails under `CI`, but
   `requireMacOSIntegration` still skips.
5. Nit: the new `internal/transcript/reader` import sits in its own group in
   about 20 files, and `internal/tui/kimi_usage.go` and
   `internal/reporter/exclude_test.go` carry unrelated import reorders.
6. Nit: `//go:build unix` on all of `internal/daemon/restart_test.go` also drops
   `TestGracefulStopFinishesBatchInFlight` on Windows, though it needs no
   signals.
7. Nit: the `Store.SearchText` comment was not updated as the implementation
   notes claim, and `redact.Kinds`, `redact.JSON`, and `State.UnmarshalJSON`
   were kept without an in-code note.
8. Nit: the exported `tui.NewModel` returns the unexported `model` type.

The reviewer also asked whether the "Go type design and error handling"
section and the new Testing style bullets in `AGENTS.md`, which the plan does
not mention, belong in this change, and whether the owner wants `gofmt -w .` or
formatting of changed files only.

## Validation

The reviewer ran `gofmt -l`, `go build` (host and Windows), `go vet` (host,
Linux, macOS, Windows, FreeBSD), `go test -race -count=1 ./...` (21 packages
passed), `git diff --check`, shellcheck on every script, the `go list -deps`
decoupling checks, one-off staticcheck (only the six expected ST1005 hits) and
deadcode (`-test` clean), and `scripts/ci-go-test.sh` on several packages,
including the skip and missing-`sqlite3` failure paths. The test inventory went
from 454 to 457 functions with none dropped.
