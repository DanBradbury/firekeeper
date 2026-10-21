# Public Go packages

These packages are the only importable Go API of this module. Everything else
lives under `internal/` and may change at any time.

| Package | Purpose |
| --- | --- |
| `pkg/event` | The normalized transcript event (`Event`, `Tokens`, `Role`, `Provider` and their constants), `Validate`, and `TouchedFiles`. Standard library only. |
| `pkg/redact` | The redaction entry points (`Event`, `String`, `Options`, `Counts`, `Kind`). |

They are a stable contract with other tools, such as servers that receive
sessions from the reporter, and follow semantic versioning. Names and JSON
forms are not removed or changed incompatibly within a major version. The
wire format is specified in [`docs/api.md`](../docs/api.md) and
[`docs/event.schema.json`](../docs/event.schema.json).
