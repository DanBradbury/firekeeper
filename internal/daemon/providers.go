package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// DetectProviders finds supported transcript providers without reading their
// data or launching their CLIs. An existing provider home or executable on PATH
// is sufficient; providers without transcript readers are deliberately omitted.
func DetectProviders(home string, getenv func(string) string, lookPath func(string) (string, error)) []transcript.Provider {
	if getenv == nil {
		getenv = os.Getenv
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	var providers []transcript.Provider
	for _, candidate := range []struct {
		provider                transcript.Provider
		env, directory, command string
	}{
		{transcript.ProviderCodex, "CODEX_HOME", ".codex", "codex"},
		{transcript.ProviderCopilot, "COPILOT_HOME", ".copilot", "copilot"},
		{transcript.ProviderClaude, "CLAUDE_CONFIG_DIR", ".claude", "claude"},
	} {
		directory := strings.TrimSpace(getenv(candidate.env))
		if directory == "" {
			directory = filepath.Join(home, candidate.directory)
		}
		info, err := os.Stat(directory)
		if err == nil && info.IsDir() {
			providers = append(providers, candidate.provider)
			continue
		}
		if _, err := lookPath(candidate.command); err == nil {
			providers = append(providers, candidate.provider)
		}
	}
	return providers
}
