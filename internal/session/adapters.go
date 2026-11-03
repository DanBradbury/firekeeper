package session

import "github.com/DanBradbury/firekeeper/pkg/event"

// providerAdapter is one provider's implementation of process
// classification (concern 1) and process-to-session linking with metadata
// (concerns 2 and 3). Every supported provider has exactly one adapter in
// adapters. Usage, transcript readers and UI stay in their own packages.
type providerAdapter interface {
	// Provider is the provider this adapter implements.
	Provider() event.Provider
	// Signature is the provider's classification data.
	Signature() signature
	// SessionID reads an explicit session id from a session's command line.
	// It only parses and validates; it does no I/O. It reports false for a
	// command line that isn't a session of this provider.
	SessionID(inv invocation) (id string, ok bool)
	// Link attaches sessions and metadata to this provider's groups. It
	// receives only this provider's groups.
	Link(d *discoverer, groups []*ProcessGroup) error
}

// adapters is the registry, in the order Discover links providers.
var adapters = []providerAdapter{
	codexAdapter{}, copilotAdapter{}, kimiAdapter{}, claudeAdapter{},
	openCodeAdapter{}, geminiAdapter{}, cursorAdapter{},
}

// toolName is the display name stored in ProcessInfo.Tool and
// ProcessGroup.Tool for p. The follow-up moves it to internal/tui and
// removes Tool.
func toolName(p event.Provider) string {
	switch p {
	case event.ProviderCodex:
		return "Codex"
	case event.ProviderCopilot:
		return "Copilot"
	case event.ProviderKimi:
		return "Kimi"
	case event.ProviderClaude:
		return "Claude"
	case event.ProviderOpenCode:
		return "OpenCode"
	case event.ProviderGemini:
		return "Gemini"
	case event.ProviderCursor:
		return "Cursor"
	default:
		return ""
	}
}

// sessionArgs returns the provider arguments of inv when its signature
// classifies it as a session, else nil and false.
func sessionArgs(s signature, inv invocation) ([]string, bool) {
	role, ok := s.classify(inv)
	if !ok || role != RoleSession {
		return nil, false
	}
	return inv.Rest, true
}

// linkValues runs an enrich function written for a slice of groups on the
// adapter's group pointers, and writes the results back, so moving it into
// Link changes no linking behaviour.
func linkValues(groups []*ProcessGroup, enrich func([]ProcessGroup) error) error {
	values := make([]ProcessGroup, len(groups))
	for i, g := range groups {
		values[i] = *g
	}
	err := enrich(values)
	for i, g := range groups {
		*g = values[i]
	}
	return err
}

type codexAdapter struct{}

func (codexAdapter) Provider() event.Provider { return event.ProviderCodex }
func (codexAdapter) Signature() signature     { return codexSignature }

// SessionID reads `codex resume <UUID>` and `codex exec resume <UUID>`. A
// session name (not a UUID), --last and the picker name no id. A fork never
// gives one: its id names the session being forked from.
func (codexAdapter) SessionID(inv invocation) (string, bool) {
	args, ok := sessionArgs(codexSignature, inv)
	if !ok {
		return "", false
	}
	cw := scanCommand(args, codexSignature.flags)
	if cw.Uncertain || len(cw.Words) == 0 {
		return "", false
	}
	after := args[cw.Index+1:]
	switch {
	case cw.Words[0] == "resume":
	case (cw.Words[0] == "exec" || cw.Words[0] == "e") && len(cw.Words) == 2 && cw.Words[1] == "resume":
		after = args[cw.Index+2:]
	default:
		return "", false
	}
	id := scanCommand(after, codexSignature.flags)
	if id.Uncertain || len(id.Words) == 0 || !standalone().validUUID(id.Words[0]) {
		return "", false
	}
	return id.Words[0], true
}

func (codexAdapter) Link(d *discoverer, groups []*ProcessGroup) error {
	return linkValues(groups, d.enrichCodexSessions)
}

type copilotAdapter struct{}

func (copilotAdapter) Provider() event.Provider { return event.ProviderCopilot }
func (copilotAdapter) Signature() signature     { return copilotSignature }
func (copilotAdapter) SessionID(inv invocation) (string, bool) {
	args, ok := sessionArgs(copilotSignature, inv)
	if !ok {
		return "", false
	}
	return standalone().copilotSessionIDFromArgs(args)
}
func (copilotAdapter) Link(d *discoverer, groups []*ProcessGroup) error {
	return linkValues(groups, d.enrichCopilotSessions)
}

type kimiAdapter struct{}

func (kimiAdapter) Provider() event.Provider { return event.ProviderKimi }
func (kimiAdapter) Signature() signature     { return kimiSignature }

// SessionID always reports false until Kimi Code's session id format is
// confirmed (plan step 5, Kimi guards): no format is invented, so kimi -S
// <id> sessions use the directory guess.
func (kimiAdapter) SessionID(invocation) (string, bool) { return "", false }
func (kimiAdapter) Link(d *discoverer, groups []*ProcessGroup) error {
	return linkValues(groups, d.enrichKimiSessions)
}

type claudeAdapter struct{}

func (claudeAdapter) Provider() event.Provider { return event.ProviderClaude }
func (claudeAdapter) Signature() signature     { return claudeSignature }
func (claudeAdapter) SessionID(inv invocation) (string, bool) {
	args, ok := sessionArgs(claudeSignature, inv)
	if !ok {
		return "", false
	}
	return standalone().claudeSessionIDFromArgs(args)
}
func (claudeAdapter) Link(d *discoverer, groups []*ProcessGroup) error {
	return linkValues(groups, d.enrichClaudeSessions)
}

type openCodeAdapter struct{}

func (openCodeAdapter) Provider() event.Provider { return event.ProviderOpenCode }
func (openCodeAdapter) Signature() signature     { return openCodeSignature }
func (openCodeAdapter) SessionID(inv invocation) (string, bool) {
	args, ok := sessionArgs(openCodeSignature, inv)
	if !ok {
		return "", false
	}
	return standalone().openCodeSessionIDFromArgs(args)
}
func (openCodeAdapter) Link(d *discoverer, groups []*ProcessGroup) error {
	return linkValues(groups, d.enrichOpenCodeSessions)
}

type geminiAdapter struct{}

func (geminiAdapter) Provider() event.Provider { return event.ProviderGemini }
func (geminiAdapter) Signature() signature     { return geminiSignature }
func (geminiAdapter) SessionID(inv invocation) (string, bool) {
	args, ok := sessionArgs(geminiSignature, inv)
	if !ok {
		return "", false
	}
	return standalone().geminiSessionIDFromArgs(args)
}
func (geminiAdapter) Link(d *discoverer, groups []*ProcessGroup) error {
	return linkValues(groups, d.enrichGeminiSessions)
}

type cursorAdapter struct{}

func (cursorAdapter) Provider() event.Provider { return event.ProviderCursor }
func (cursorAdapter) Signature() signature     { return cursorSignature }
func (cursorAdapter) SessionID(inv invocation) (string, bool) {
	args, ok := sessionArgs(cursorSignature, inv)
	if !ok {
		return "", false
	}
	return standalone().cursorSessionIDFromArgs(args)
}
func (cursorAdapter) Link(d *discoverer, groups []*ProcessGroup) error {
	return linkValues(groups, d.enrichCursorSessions)
}
