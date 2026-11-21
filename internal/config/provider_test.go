package config

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/firekeeper-ai/firekeeper/internal/transcript"
)

func TestParseProvider(t *testing.T) {
	for _, test := range []struct {
		in   string
		want transcript.Provider
	}{
		{"claude", transcript.ProviderClaude},
		{"Claude", transcript.ProviderClaude},
		{" CLAUDE ", transcript.ProviderClaude},
		{"\tOpenCode\n", transcript.ProviderOpenCode},
	} {
		got, err := ParseProvider(test.in)
		if err != nil || got != test.want {
			t.Errorf("ParseProvider(%q) = %q, %v; want %q", test.in, got, err, test.want)
		}
	}
	// Every provider parses from its id and from a typed variant of it.
	for _, p := range transcript.Providers() {
		for _, in := range []string{string(p), strings.ToUpper(string(p)), "  " + string(p) + " "} {
			if got, err := ParseProvider(in); err != nil || got != p {
				t.Errorf("ParseProvider(%q) = %q, %v; want %q", in, got, err, p)
			}
		}
	}
}

func TestParseProviderRejects(t *testing.T) {
	for _, in := range []string{"", " ", "\t\n"} {
		if got, err := ParseProvider(in); !errors.Is(err, ErrNoProvider) || got != "" {
			t.Errorf("ParseProvider(%q) = %q, %v; want ErrNoProvider", in, got, err)
		}
	}
	for _, in := range []string{"gpt", "codexx", "open code", "claude-code", "c laude", "copilot,codex"} {
		got, err := ParseProvider(in)
		if !errors.Is(err, ErrUnknownProvider) || got != "" {
			t.Errorf("ParseProvider(%q) = %q, %v; want ErrUnknownProvider", in, got, err)
			continue
		}
		// The message names the input and every valid choice.
		if msg := err.Error(); !strings.Contains(msg, `unknown provider "`+in+`"`) || !strings.Contains(msg, "codex, copilot") {
			t.Errorf("ParseProvider(%q) error = %q", in, msg)
		}
	}
}

func TestParseProvidersSkipsEmptyAndDuplicates(t *testing.T) {
	got, err := parseProviders(strings.Split("Claude, codex,,claude,", ","))
	want := []transcript.Provider{transcript.ProviderClaude, transcript.ProviderCodex}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("parseProviders = %v, %v; want %v", got, err, want)
	}
	if _, err := parseProviders([]string{"codex", "gpt"}); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("parseProviders with an unknown name: %v, want ErrUnknownProvider", err)
	}
}

// A typed name either parses to a canonical provider or fails with one of
// the two errors; it never yields anything else.
func FuzzParseProvider(f *testing.F) {
	for _, seed := range []string{"", " ", "claude", "Claude", " CODEX ", "gpt", "open code", "\x00", "ｃｌａｕｄｅ", "ǅ"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		p, err := ParseProvider(in)
		switch {
		case err == nil:
			if !p.Valid() || !slices.Contains(transcript.Providers(), p) {
				t.Fatalf("ParseProvider(%q) = %q, not a canonical provider", in, p)
			}
		case errors.Is(err, ErrNoProvider), errors.Is(err, ErrUnknownProvider):
			if p != "" {
				t.Fatalf("ParseProvider(%q) returned %q with error %v", in, p, err)
			}
		default:
			t.Fatalf("ParseProvider(%q) returned an unexpected error: %v", in, err)
		}
	})
}
