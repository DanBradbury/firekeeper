# Cleanup and Restructure Review 2

Plan: [`CLEANUP_AND_RESTRUCTURE_PLAN.md`](../../../CLEANUP_AND_RESTRUCTURE_PLAN.md)

## Verdict

Changes requested. Review 1's resolutions are verified, and the second
independent read-only review found no code defects, but one should-fix
documentation error and three nits.

## Findings

1. Should fix: the `AGENTS.md` code map calls `reader/` "the only transcript
   package that imports internal/session", but the claude, codex, and copilot
   provider packages import it too. The rule task 4 enforces is narrower:
   `internal/transcript` itself and `internal/server` must not depend on
   session.
2. Nit: `AGENTS.md` says tests sit in the same package as the code, but
   `internal/server/auth` uses external `auth_test` packages.
3. Nit: "Each step is faster than the next" is not true of the last local
   check; `git diff --check` is faster than the race tests before it.
4. Nit: `internal/tui/terminal_darwin_test.go` has its standard-library imports
   split across three groups after the moved test was merged in.

The reviewer also suggested noting in the plan that `session_status.go` and
`kimi_sessions.go` stay because the dashboard still refers to them, and asked
whether the plan and review notes ship with the change.

## Validation

The staged snapshot, checked out on its own, built, vetted, and passed
`go test -race -count=1 ./...` (22 packages). In the working tree the reviewer
ran `gofmt -l`, `go build`, `go vet` for the host, Linux, macOS, Windows, and
FreeBSD, `git diff --check` (index and working tree), shellcheck, the
`go list -deps` decoupling checks, one-off staticcheck (only the six expected
ST1005 hits) and deadcode (`-test` clean), `scripts/ci-go-test.sh` with
`CI=true` across most packages, and the macOS compile and process tests. The
test inventory went from 456 to 459 functions with none dropped.
