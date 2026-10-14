# Cleanup and Restructure Review 4 Resolution

Review: [`review-4.md`](review-4.md)

## Resolution

The `AGENTS.md` code map now describes `model.go` as polling through
`session.Discover` and retaining known session metadata, and lists process
grouping and per-provider metadata enrichment under `internal/session`.
