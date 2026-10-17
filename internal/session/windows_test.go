package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Windows command lines, as the process list reports them. These run on every
// platform: classification is string matching and must not depend on the
// separator of the machine running the tests.
func TestClassifyWindowsCommandLines(t *testing.T) {
	d := &discoverer{}
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"claude native", `C:\Users\me\.local\bin\claude.exe --resume`, "Claude"},
		{"claude native quoted path", `"C:\Users\me dev\.local\bin\claude.exe" --model opus`, "Claude"},
		{"claude via node", `"C:\Program Files\nodejs\node.exe" C:\Users\me\AppData\Roaming\npm\node_modules\@anthropic-ai\claude-code\cli.js`, "Claude"},
		{"claude helper subcommand", `C:\Users\me\.local\bin\claude.exe mcp serve`, ""},
		{"claude desktop squirrel", `C:\Users\me\AppData\Local\AnthropicClaude\app-0.9.0\claude.exe`, ""},
		{"claude desktop msix", `"C:\Program Files\WindowsApps\Claude_1.0.0.0_x64__abc\app\Claude.exe"`, ""},
		{"claude desktop helper", `C:\Users\me\AppData\Local\somewhere\claude.exe --type=renderer --lang=en-US`, ""},
		{"codex via node", `"C:\Program Files\nodejs\node.exe" C:\Users\me\AppData\Roaming\npm\node_modules\@openai\codex\bin\codex.js`, "Codex"},
		{"codex native", `C:\Users\me\AppData\Roaming\npm\node_modules\@openai\codex\vendor\x86_64-pc-windows-msvc\codex\codex.exe`, "Codex"},
		{"gemini via node", `"C:\Program Files\nodejs\node.exe" C:\Users\me\AppData\Roaming\npm\node_modules\@google\gemini-cli\bundle\gemini.js`, "Gemini"},
		{"gemini exe", `C:\tools\gemini.exe`, "Gemini"},
		{"cursor agent", `C:\Users\me\AppData\Local\cursor-agent\cursor-agent.exe`, "Cursor"},
		{"opencode", `C:\Users\me\AppData\Roaming\npm\node_modules\opencode-ai\bin\opencode.exe`, "OpenCode"},
		{"copilot cli", `"C:\Program Files\nodejs\node.exe" C:\Users\me\AppData\Roaming\npm\node_modules\@github\copilot\index.js`, "Copilot"},
		{"copilot windows app", `"C:\Program Files\WindowsApps\Microsoft.Copilot_1.2.3.4_x64__8wekyb3d8bbwe\Copilot.exe"`, ""},
		{"copilot sdk runtime", `C:\Users\me\AppData\Local\github-copilot-sdk\cli\copilot.exe --stdio`, ""},
		{"firekeeper itself", `"C:\Users\me\AppData\Local\Programs\firekeeper\firekeeper.exe" daemon --provider codex`, ""},
		{"firekeeper unquoted", `C:\bin\firekeeper.exe report --provider copilot`, ""},
		{"explorer", `C:\Windows\explorer.exe`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.classifyProcess(tt.command); got != tt.want {
				t.Fatalf("classifyProcess(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestCommandFields(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{`"C:\Program Files\nodejs\node.exe" script.js --flag`, []string{`C:\Program Files\nodejs\node.exe`, "script.js", "--flag"}},
		{`C:\bin\tool.exe --a b`, []string{`C:\bin\tool.exe`, "--a", "b"}},
		{`/usr/bin/node app.js`, []string{"/usr/bin/node", "app.js"}},
		{`"unterminated path`, []string{`"unterminated`, "path"}},
		{`  `, nil},
		{``, nil},
	}
	for _, tt := range tests {
		got := commandFields(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("commandFields(%q) = %q, want %q", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("commandFields(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	}
}

func TestBaseNameAndSlashPath(t *testing.T) {
	for in, want := range map[string]string{
		`C:\a\b\node.exe`: "node.exe",
		`/a/b/node`:       "node",
		`a\b/c.js`:        "c.js",
		`node`:            "node",
		``:                "",
	} {
		if got := baseName(in); got != want {
			t.Errorf("baseName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slashPath(`C:\a\b`); got != "C:/a/b" {
		t.Errorf("slashPath = %q", got)
	}
}

func TestSameDirectory(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{`C:\Work\App`, `c:\work\app`, true},
		{`C:\Work\App`, `C:/Work/App`, true},
		{`C:\Work\App\`, `C:\Work\App`, true},
		{`\\?\C:\Work\App`, `C:\Work\App`, true},
		{`C:\Work\App`, `C:\Work\Other`, false},
		{`C:\Work\App`, `D:\Work\App`, false},
		{`/work/app`, `/work/app`, true},
		{`/work/app`, `/Work/App`, false}, // Unix paths are case sensitive
		{``, `C:\x`, false},
	}
	for _, tt := range tests {
		if got := sameDirectory(tt.a, tt.b); got != tt.want {
			t.Errorf("sameDirectory(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// Without lsof, Windows cannot see which thread a Codex runtime holds open,
// so it matches the newest recent thread in the runtime's working directory.
func TestEnrichCodexSessionsWithoutOpenFiles(t *testing.T) {
	previous := openFilesListable
	openFilesListable = false
	t.Cleanup(func() { openFilesListable = previous })

	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "state_5.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const (
		older = "01a141cf-34d4-7813-9969-ad12c634738d"
		newer = "01a10dae-2417-7fd1-b90a-9798256f74a6"
		other = "01a131d1-68e6-76a1-9a85-536c8a00dc20"
	)
	rollout := func(id string) string {
		return strings.ReplaceAll(filepath.Join(codexHome, "sessions", "rollout-2026-10-05T13-07-58-"+id+".jsonl"), `\`, `\\`)
	}
	d := &discoverer{ctx: context.Background(), opts: Options{Home: home, CodexHome: codexHome, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "lsof" && strings.Contains(joined, "-d cwd"):
			return []byte("p100\nnc:\\work\\app\n"), nil
		case name == "lsof":
			return nil, nil // no open-file information on Windows
		case name == "sqlite3" && strings.Contains(joined, "LIMIT 100"):
			return []byte(`[{"id":"` + older + `"},{"id":"` + newer + `"},{"id":"` + other + `"}]`), nil
		case name == "sqlite3":
			return []byte(`[
			 {"id":"` + older + `","display_name":"older","cwd":"C:\\Work\\App","updated_at_ms":10,"rollout_path":"` + rollout(older) + `"},
			 {"id":"` + newer + `","display_name":"newer","cwd":"\\\\?\\C:\\Work\\App","updated_at_ms":20,"rollout_path":"` + rollout(newer) + `"},
			 {"id":"` + other + `","display_name":"other","cwd":"C:\\Work\\Elsewhere","updated_at_ms":30,"rollout_path":"` + rollout(other) + `"}
			]`), nil
		}
		return nil, os.ErrNotExist
	}}}

	groups := d.groupProcesses(d.parseProcesses("100 1 ? 01:00 \"C:\\Program Files\\nodejs\\node.exe\" C:\\npm\\node_modules\\@openai\\codex\\bin\\codex.js"))
	if err := d.enrichCodexSessions(groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Sessions) != 1 || groups[0].Sessions[0].ID != newer {
		t.Fatalf("sessions = %+v, want the newest thread in the working directory (%s)", groups[0].Sessions, newer)
	}
}
