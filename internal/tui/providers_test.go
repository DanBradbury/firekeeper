package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/firekeeper-ai/firekeeper/pkg/event"
)

// A provider added to event.Providers() needs a style before the dashboard
// can show it, and the table holds nothing else.
func TestProviderStylesCoverEveryProvider(t *testing.T) {
	for _, provider := range event.Providers() {
		style, ok := providerStyles[provider]
		if !ok {
			t.Errorf("provider %q has no style", provider)
			continue
		}
		if style.color == "" || style.icon == "" || style.missing == "" {
			t.Errorf("provider %q has an incomplete style: %+v", provider, style)
		}
	}
	for provider := range providerStyles {
		if !provider.Valid() {
			t.Errorf("style for %q, which is not in event.Providers()", provider)
		}
	}
}

// Two providers in the same color could not be told apart in the provider
// bars or the session list.
func TestProviderColorsAreDistinct(t *testing.T) {
	seen := map[string]event.Provider{}
	for _, provider := range event.Providers() {
		color := providerColor(provider)
		if other, ok := seen[color]; ok {
			t.Errorf("%q and %q share color %s", other, provider, color)
		}
		seen[color] = provider
	}
}

func TestProviderLookups(t *testing.T) {
	for _, test := range []struct {
		provider             event.Provider
		name, color, missing string
	}{
		{event.ProviderCodex, "Codex", dashTeal, "no Codex rollout metadata found"},
		{event.ProviderKimi, "Kimi", dashYellow, "no Kimi session metadata found"},
		{event.ProviderOpenCode, "OpenCode", dashBlue, "no OpenCode session found"},
		// Not a provider: the raw value, plain text and the generic note.
		{"mystery", "mystery", usageTextColor, "no session metadata adapter"},
		{"Codex", "Codex", usageTextColor, "no session metadata adapter"},
		{"", "", usageTextColor, "no session metadata adapter"},
	} {
		if got := providerName(test.provider); got != test.name {
			t.Errorf("providerName(%q) = %q, want %q", test.provider, got, test.name)
		}
		if got := providerColor(test.provider); got != test.color {
			t.Errorf("providerColor(%q) = %q, want %q", test.provider, got, test.color)
		}
		if got := providerMissingMessage(test.provider); got != test.missing {
			t.Errorf("providerMissingMessage(%q) = %q, want %q", test.provider, got, test.missing)
		}
	}
}

// The Usage tab's Overview cards used their own colors, so Codex was blue
// there and teal everywhere else, and Kimi teal instead of yellow.
func TestOverviewCardsUseProviderColors(t *testing.T) {
	m := overviewTestModel(time.Now())
	m.kimiUsage.Summary.Sessions = 1
	view := m.viewOverview(40)
	for _, provider := range []event.Provider{event.ProviderCodex, event.ProviderCopilot, event.ProviderClaude, event.ProviderKimi} {
		if title := dashPaint(toolLabel(provider), providerColor(provider), true); !strings.Contains(view, title) {
			t.Errorf("%s card title is not in %s's color", provider, providerName(provider))
		}
	}
}

// The setup panel previews every provider's logo, alphabetically by name.
func TestIconSetupPreviewsEveryProvider(t *testing.T) {
	providers := iconSetupProviders()
	if len(providers) != len(event.Providers()) {
		t.Fatalf("preview has %d providers, want %d", len(providers), len(event.Providers()))
	}
	for index := 1; index < len(providers); index++ {
		if providerName(providers[index-1]) > providerName(providers[index]) {
			t.Fatalf("preview order %v is not alphabetical", providers)
		}
	}
}
