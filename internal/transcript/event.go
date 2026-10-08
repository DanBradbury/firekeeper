// Package transcript defines the normalized transcript event shared by
// provider parsers, the reporter, and the dashboard server.
//
// The JSON form of Event is pinned by docs/event.schema.json; a test fails if
// the two drift.
package transcript

import (
	"encoding/json"
	"fmt"
	"time"
)

// Provider names the harness that produced a transcript.
type Provider string

const (
	ProviderCodex   Provider = "codex"
	ProviderCopilot Provider = "copilot"
	ProviderKimi    Provider = "kimi"
	ProviderClaude  Provider = "claude"
)

// Providers lists every provider the event contract allows, in schema order.
func Providers() []Provider {
	return []Provider{ProviderCodex, ProviderCopilot, ProviderKimi, ProviderClaude}
}

// Valid reports whether p is a provider the event contract allows.
func (p Provider) Valid() bool {
	for _, known := range Providers() {
		if p == known {
			return true
		}
	}
	return false
}

// Role classifies what an event represents to a human reader.
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolCall   Role = "tool_call"
	RoleToolResult Role = "tool_result"
	RoleSystem     Role = "system"
	// RoleMeta marks records a parser could not interpret. Parsers never
	// drop a record; they emit it with this role instead.
	RoleMeta Role = "meta"
)

// Roles lists every role the event contract allows, in schema order.
func Roles() []Role {
	return []Role{RoleUser, RoleAssistant, RoleToolCall, RoleToolResult, RoleSystem, RoleMeta}
}

// Valid reports whether r is a role the event contract allows.
func (r Role) Valid() bool {
	for _, known := range Roles() {
		if r == known {
			return true
		}
	}
	return false
}

// Tokens holds per-event token counts. Unknown counts are zero.
type Tokens struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Cache  int64 `json:"cache"`
}

// Event is one normalized transcript record. The idempotency key is
// (MachineID, SessionID, Seq).
//
// Seq is the zero-based index of the record in its source and must be
// identical every time the same source is re-read: the line index for JSONL,
// or row order by a stable key for SQLite sources.
//
// Raw is the original record after redaction. Text is what a human would
// read; tool output longer than 64 KiB is truncated in Text with a trailing
// "[truncated N bytes]" marker while Raw keeps the original.
type Event struct {
	MachineID string          `json:"machine_id"`
	SessionID string          `json:"session_id"`
	Provider  Provider        `json:"provider"`
	Seq       int64           `json:"seq"`
	TS        *time.Time      `json:"ts"`
	Role      Role            `json:"role"`
	Text      string          `json:"text"`
	ToolName  *string         `json:"tool_name"`
	Model     *string         `json:"model"`
	Tokens    Tokens          `json:"tokens"`
	Raw       json.RawMessage `json:"raw"`
}

// Validate checks the constraints the JSON Schema expresses. It does not
// inspect Text or Raw contents.
func (e Event) Validate() error {
	if !e.Provider.Valid() {
		return fmt.Errorf("transcript: invalid provider %q", e.Provider)
	}
	if !e.Role.Valid() {
		return fmt.Errorf("transcript: invalid role %q", e.Role)
	}
	if e.Seq < 0 {
		return fmt.Errorf("transcript: negative seq %d", e.Seq)
	}
	if e.Tokens.Input < 0 || e.Tokens.Output < 0 || e.Tokens.Cache < 0 {
		return fmt.Errorf("transcript: negative token count")
	}
	if len(e.Raw) > 0 && !json.Valid(e.Raw) {
		return fmt.Errorf("transcript: raw is not valid JSON")
	}
	return nil
}
