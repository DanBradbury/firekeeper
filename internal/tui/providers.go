package tui

import (
	"github.com/firekeeper-ai/firekeeper/internal/session"
	"github.com/firekeeper-ai/firekeeper/pkg/event"
)

// providerStyle is how the dashboard shows one provider. Its display name
// is not here: it comes from session.ToolName, so discovery and the
// dashboard spell it the same way.
type providerStyle struct {
	// color marks the provider's name, bars, chat replies and Usage card.
	color string
	// icon is the provider's logo: the private use codepoint
	// scripts/icon-font/build.mjs gives it in FirekeeperIcons.ttf.
	icon string
	// missing explains a runtime discovery found no session metadata for.
	missing string
}

// providerStyles has one entry for each provider in event.Providers();
// TestProviderStylesCoverEveryProvider fails until a new provider is added.
var providerStyles = map[event.Provider]providerStyle{
	event.ProviderCodex:    {color: dashTeal, icon: "", missing: "no Codex rollout metadata found"},
	event.ProviderCopilot:  {color: dashPurple, icon: "", missing: "no Copilot session metadata found"},
	event.ProviderKimi:     {color: dashYellow, icon: "", missing: "no Kimi session metadata found"},
	event.ProviderClaude:   {color: dashOrange, icon: "", missing: "no Claude Code transcript found"},
	event.ProviderOpenCode: {color: dashBlue, icon: "", missing: "no OpenCode session found"},
	event.ProviderGemini:   {color: dashGreen, icon: "", missing: "no Gemini CLI session log found"},
	// Cursor's mark is monochrome; red stays the NEEDS INPUT color.
	event.ProviderCursor: {color: usageBrightColor, icon: "", missing: "no Cursor CLI transcript found"},
}

// providerName is p's display name, or p itself when it is not a known
// provider, so an unexpected value still shows rather than vanishing.
func providerName(p event.Provider) string {
	if name := session.ToolName(p); name != "" {
		return name
	}
	return string(p)
}

// providerColor is p's accent color, or the plain text color when p is not
// a known provider.
func providerColor(p event.Provider) string {
	if style, ok := providerStyles[p]; ok {
		return style.color
	}
	return usageTextColor
}

// providerMissingMessage is the note for a p runtime without session
// metadata.
func providerMissingMessage(p event.Provider) string {
	if style, ok := providerStyles[p]; ok {
		return style.missing
	}
	return "no session metadata adapter"
}
