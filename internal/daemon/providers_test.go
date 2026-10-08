package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func TestDetectProviders(t *testing.T) {
	for _, scenario := range []string{"none", "directories", "executables", "override", "missing override", "file"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			env := map[string]string{}
			commands := map[string]bool{}
			var want []transcript.Provider
			mkdir := func(path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "directories":
				for _, name := range []string{".codex", ".copilot", ".claude", ".kimi-code", ".opencode"} {
					mkdir(filepath.Join(home, name))
				}
				want = []transcript.Provider{transcript.ProviderCodex, transcript.ProviderCopilot, transcript.ProviderClaude}
			case "executables":
				commands = map[string]bool{"codex": true, "copilot": true, "claude": true, "kimi": true, "opencode": true}
				mkdir(filepath.Join(home, ".codex"))
				want = []transcript.Provider{transcript.ProviderCodex, transcript.ProviderCopilot, transcript.ProviderClaude}
			case "override":
				for _, key := range []string{"CODEX_HOME", "COPILOT_HOME", "CLAUDE_CONFIG_DIR"} {
					env[key] = filepath.Join(home, key)
					mkdir(env[key])
				}
				want = []transcript.Provider{transcript.ProviderCodex, transcript.ProviderCopilot, transcript.ProviderClaude}
			case "missing override":
				mkdir(filepath.Join(home, ".codex"))
				env["CODEX_HOME"] = filepath.Join(home, "missing")
			case "file":
				if err := os.WriteFile(filepath.Join(home, ".codex"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := DetectProviders(home, func(key string) string { return env[key] }, func(command string) (string, error) {
				if commands[command] {
					return filepath.Join(home, command), nil
				}
				return "", errors.New("not installed")
			})
			if !slices.Equal(got, want) {
				t.Fatalf("providers = %v, want %v", got, want)
			}
		})
	}
}
