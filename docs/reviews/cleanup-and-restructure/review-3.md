# Cleanup and Restructure Review 3

Plan: [`CLEANUP_AND_RESTRUCTURE_PLAN.md`](../../../CLEANUP_AND_RESTRUCTURE_PLAN.md)

## Verdict

Approved with two changes requested. Review 2's resolutions are verified. The
third independent read-only review found one should-fix coverage drop and one
nit; neither changes behavior.

## Findings

1. Should fix: the dashboard's conversion helpers in
   `internal/tui/session_bridge.go` (`fromProcessGroup`, `fromProcessInfo`,
   `fromSessionInfo`, and the slice helpers) lost their test coverage. The root
   tests used to reach them through the removed wrappers; those tests moved to
   `internal/session` and now check `session.Info` directly, so a dropped or
   swapped field would go unnoticed.
2. Nit: `tui.NewModel` and `cli.PrintUsage`, exported by task 7, have no doc
   comments.

The reviewer also asked whether the `session.SessionState*` constants, which
still repeat the package name, should be renamed; the plan lists only the
types.

## Validation

The reviewer ran `gofmt -l`, `go build`, `go vet` for the host, Linux, macOS,
Windows, and FreeBSD, `go test -race -count=1 ./...` in both the working tree
and the staged snapshot (21 packages each), `git diff --check` for the index
and working tree, shellcheck on all seven scripts, the `go list -deps`
decoupling checks, one-off staticcheck and deadcode, `scripts/ci-go-test.sh`
with `CI=true` on several packages, a coverage comparison against `HEAD`, and
a local build with the GoReleaser `-X` paths.
