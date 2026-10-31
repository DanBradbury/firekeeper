// Package transcript holds the readers that turn each coding agent's local
// history into normalized events. The event types themselves live in
// pkg/event, the public contract; the names here are aliases of them so the
// readers, reporter and TUI keep one short spelling.
package transcript

import "github.com/DanBradbury/firekeeper/pkg/event"

type (
	// Provider names the harness that produced a transcript.
	Provider = event.Provider
	// Role classifies what an event represents to a human reader.
	Role = event.Role
	// Tokens holds per-event token counts.
	Tokens = event.Tokens
	// Event is one normalized transcript record.
	Event = event.Event
)

const (
	ProviderCodex       = event.ProviderCodex
	ProviderCopilot     = event.ProviderCopilot
	ProviderKimi        = event.ProviderKimi
	ProviderClaude      = event.ProviderClaude
	ProviderOpenCode    = event.ProviderOpenCode
	ProviderGemini      = event.ProviderGemini
	ProviderCursor      = event.ProviderCursor
	ProviderAntigravity = event.ProviderAntigravity
	ProviderGrok        = event.ProviderGrok

	RoleUser       = event.RoleUser
	RoleAssistant  = event.RoleAssistant
	RoleToolCall   = event.RoleToolCall
	RoleToolResult = event.RoleToolResult
	RoleSystem     = event.RoleSystem
	RoleMeta       = event.RoleMeta
)

// Providers lists every provider the event contract allows.
func Providers() []Provider { return event.Providers() }

// Roles lists every role the event contract allows.
func Roles() []Role { return event.Roles() }

// TouchedFiles returns the file paths a tool call in e edits, writes, or
// patches. See event.TouchedFiles.
func TouchedFiles(e Event) []string { return event.TouchedFiles(e) }
