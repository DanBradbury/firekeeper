# Cleanup and Restructure Review 2 Resolution

Review: [`review-2.md`](review-2.md)

## Resolutions

1. The `AGENTS.md` code map now says that `reader` and the provider packages
   import `internal/session`, and that the `transcript` package itself must not.
2. `AGENTS.md` now says most tests sit in the same package as the code.
3. The local checks are introduced as "the fast checks come before the tests",
   which holds for `git diff --check` at the end.
4. Merged the standard-library imports in
   `internal/tui/terminal_darwin_test.go` into one group. A scan of every added
   or changed Go file found no other split groups introduced by this change; the
   five `internal/session` files with split groups have the same import blocks
   as `HEAD`.

The plan's implementation notes now say why `session_status.go` and
`kimi_sessions.go` stay. Whether the plan and review notes ship with the change
is left to the owner.
