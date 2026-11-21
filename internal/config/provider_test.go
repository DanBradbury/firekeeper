package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

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
		// Unicode spaces around the name are trimmed: a pasted no-break
		// space is not part of the name.
		{"claude\u00a0", transcript.ProviderClaude},
		{"\u3000codex", transcript.ProviderCodex},
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

// Provider ids are ASCII, so a name with any other character is unknown,
// even one that Unicode lowercasing would turn into an id.
func TestParseProviderRejectsLookAlikes(t *testing.T) {
	for _, in := range []string{
		"\u212Aimi",    // KELVIN SIGN, lowercases to "k"
		"cop\u0130lot", // LATIN CAPITAL LETTER I WITH DOT ABOVE, lowercases to "i"
		"gem\u0130n\u0130",
		"\u212A\u0130M\u0130",
		"ｃｌａｕｄｅ",       // fullwidth letters
		"claude\u200b", // ZERO WIDTH SPACE is not trimmed
		"c\u0301laude", // combining accent
	} {
		if got, err := ParseProvider(in); !errors.Is(err, ErrUnknownProvider) || got != "" {
			t.Errorf("ParseProvider(%q) = %q, %v; want ErrUnknownProvider", in, got, err)
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
	for _, seed := range []string{"", " ", "claude", "Claude", " CODEX ", "gpt", "open code", "\x00", "ｃｌａｕｄｅ", "ǅ", "\u212Aimi", "cop\u0130lot", "claude\u00a0"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		p, err := ParseProvider(in)
		switch {
		case err == nil:
			if !p.Valid() || !slices.Contains(transcript.Providers(), p) {
				t.Fatalf("ParseProvider(%q) = %q, not a canonical provider", in, p)
			}
			// Only ASCII case may differ: the trimmed input is ASCII and
			// spells the id.
			trimmed := strings.TrimSpace(in)
			for i := 0; i < len(trimmed); i++ {
				if trimmed[i] >= utf8.RuneSelf {
					t.Fatalf("ParseProvider(%q) = %q from non-ASCII input", in, p)
				}
			}
			if !strings.EqualFold(trimmed, string(p)) {
				t.Fatalf("ParseProvider(%q) = %q, which the input does not spell", in, p)
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
