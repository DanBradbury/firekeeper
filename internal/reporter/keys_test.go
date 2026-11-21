package reporter

import (
	"testing"

	"github.com/firekeeper-ai/firekeeper/internal/transcript"
)

func TestMatchKeyIgnoresIDCase(t *testing.T) {
	codex, claude := transcript.ProviderCodex, transcript.ProviderClaude
	if matchKey(codex, "ABC-123") != matchKey(codex, "abc-123") {
		t.Error("matchKey differs for ids that differ only in case")
	}
	if matchKey(codex, "abc-123") != (sessionKey{provider: codex, id: "abc-123"}) {
		t.Error("matchKey of a lowercase id differs from the exact key")
	}
	// A different provider or id is a different session.
	if matchKey(codex, "abc-123") == matchKey(claude, "abc-123") {
		t.Error("matchKey is the same for two providers")
	}
	if matchKey(codex, "abc-123") == matchKey(codex, "abc-124") {
		t.Error("matchKey is the same for two ids")
	}
	// The exact key keeps case, so it can tell such ids apart.
	if (sessionKey{provider: codex, id: "ABC"}) == (sessionKey{provider: codex, id: "abc"}) {
		t.Error("exact keys match ids that differ in case")
	}
	// No id text can make two providers' keys equal, as joined strings could.
	if matchKey(codex, "x\x00claude") == matchKey(claude, "x") {
		t.Error("keys collide across providers")
	}
}
