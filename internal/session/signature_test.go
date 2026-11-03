package session

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/pkg/event"
)

// classifyWith runs every adapter's signature over args in the given order
// and returns the single match, or ok false for none. More than one match
// fails the test (no overlap).
func classifyWith(t *testing.T, order []providerAdapter, args []string) (event.Provider, Role, bool) {
	t.Helper()
	inv := parseInvocation(args)
	var found []event.Provider
	var role Role
	for _, a := range order {
		if r, ok := a.Signature().classify(inv); ok {
			found = append(found, a.Provider())
			role = r
		}
	}
	if len(found) > 1 {
		t.Fatalf("%q matched %d providers: %v", args, len(found), found)
	}
	if len(found) == 0 {
		return "", RoleUnknown, false
	}
	return found[0], role, true
}

// signatureCorpus is every command line the repository's tests use, plus
// the plan's disputed cases, with the expected provider and role. Lines are
// ps strings, split as the fallback splits them; list cases give real
// argument lists.
var signatureCorpus = []struct {
	line     string
	args     []string
	provider event.Provider
	role     Role
}{
	// Codex.
	{line: "codex", provider: event.ProviderCodex, role: RoleSession},
	{line: "codex resume", provider: event.ProviderCodex, role: RoleSession},
	{line: "codex app-server --listen unix://", provider: event.ProviderCodex, role: RoleServer},
	{line: "codex app-server --listen stdio://", provider: event.ProviderCodex, role: RoleServer},
	{line: "codex app-server generate-ts", provider: event.ProviderCodex, role: RoleTool},
	{line: "codex --model gemini-3-pro", provider: event.ProviderCodex, role: RoleSession},
	{line: "codex -m o3 doctor", provider: event.ProviderCodex, role: RoleTool},
	{line: "codex --some-unlisted-flag doctor", provider: event.ProviderCodex, role: RoleSession},
	{line: "codex exec fix it", provider: event.ProviderCodex, role: RoleSession},
	{line: "codex e fix it", provider: event.ProviderCodex, role: RoleSession},
	{line: "codex login", provider: event.ProviderCodex, role: RoleTool},
	{line: "codex mcp list", provider: event.ProviderCodex, role: RoleTool},
	{line: "codex debug app-server send-message-v2", provider: event.ProviderCodex, role: RoleTool},
	{line: "codex cloud", provider: event.ProviderCodex, role: RoleSession},
	{line: "codex exec-server", provider: event.ProviderCodex, role: RoleServer},
	{line: `"C:\Program Files\nodejs\node.exe" C:\Users\me\AppData\Roaming\npm\node_modules\@openai\codex\bin\codex.js`, provider: event.ProviderCodex, role: RoleSession},
	{line: `C:\Users\me\AppData\Roaming\npm\node_modules\@openai\codex\vendor\x86_64-pc-windows-msvc\codex\codex.exe`, provider: event.ProviderCodex, role: RoleSession},
	// Step 0 row 5: conhost hosting codex.
	{line: `C:\Windows\System32\conhost.exe 0xffffffff -ForceV1 codex`, provider: event.ProviderCodex, role: RoleSession},
	// Step 0 row 4: Codex.app's window and helpers; its app-server is a server.
	{args: []string{"/Applications/Codex.app/Contents/MacOS/Codex", "--session", "one"}},
	{args: []string{"/Applications/Codex.app/Contents/Resources/codex", "app-server", "--listen", "stdio://"}, provider: event.ProviderCodex, role: RoleServer},
	{args: []string{"/Applications/Codex.app/Contents/Resources/codex", "login"}},
	// Step 0 row 1: a hook script naming codex is not Codex.
	{line: "node /home/u/.claude/hooks/codex-notify.js"},

	// Kimi Code.
	{line: "kimi", provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi web", provider: event.ProviderKimi, role: RoleServer},
	{line: "kimi -m kimi-k2 web", provider: event.ProviderKimi, role: RoleServer},
	{line: "kimi web rotate-token", provider: event.ProviderKimi, role: RoleTool},
	{line: "kimi server kill", provider: event.ProviderKimi, role: RoleTool},
	{line: "kimi acp", provider: event.ProviderKimi, role: RoleServer},
	{line: "kimi doctor", provider: event.ProviderKimi, role: RoleTool},
	{line: "kimi export", provider: event.ProviderKimi, role: RoleTool},
	{line: "kimi vis", provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi provider list", provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi app", provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi -c", provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi -S", provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi -S web", provider: event.ProviderKimi, role: RoleSession},
	{args: []string{"kimi", "-p", "web"}, provider: event.ProviderKimi, role: RoleSession},
	{args: []string{"kimi", "-p", "-S abc"}, provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi --foo web", provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi --foo notes export", provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi --foo --bar export", provider: event.ProviderKimi, role: RoleSession},
	{line: "kimi --foo -m kimi-k2 export", provider: event.ProviderKimi, role: RoleTool},
	{line: "kimi --foo=1 export", provider: event.ProviderKimi, role: RoleTool},
	{line: "kimi -- web", provider: event.ProviderKimi, role: RoleSession},
	{line: "node /usr/lib/node_modules/@moonshot-ai/kimi-code/dist/main.mjs web", provider: event.ProviderKimi, role: RoleServer},
	{args: []string{"/opt/my dir/bin/kimi", "-c"}, provider: event.ProviderKimi, role: RoleSession},
	// Step 0 row 9: the old Python CLI doesn't match Kimi Code's signature.
	{line: "/usr/bin/python3 /home/u/.local/bin/kimi"},
	{line: "python3 -m kimi_cli"},
	{line: "node -e require('/x/@moonshot-ai/kimi-code/dist/main.mjs')"},

	// Claude Code.
	{line: "claude", provider: event.ProviderClaude, role: RoleSession},
	{line: "/usr/local/bin/claude --resume 0f8fad5b-d9cb-469f-a165-70867728950e", provider: event.ProviderClaude, role: RoleSession},
	{line: "node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js -p hello", provider: event.ProviderClaude, role: RoleSession},
	{line: "claude bg-pty-host --bg-pty-host /tmp/x.sock", provider: event.ProviderClaude, role: RoleTool},
	{line: "claude.exe daemon run --origin transient", provider: event.ProviderClaude, role: RoleTool},
	{line: "claude mcp serve", provider: event.ProviderClaude, role: RoleServer},
	{line: "claude mcp list", provider: event.ProviderClaude, role: RoleTool},
	{line: "/Applications/Claude.app/Contents/MacOS/Claude"},
	{line: "/usr/lib/claude-desktop/claude-desktop --type=renderer"},
	{line: `C:\Users\me\AppData\Local\AnthropicClaude\app-0.9.0\claude.exe`},
	{line: "vim /home/u/.claude/settings.json"},
	{line: "node /srv/claude-tools/index.js"},

	// Gemini CLI.
	{line: "gemini", provider: event.ProviderGemini, role: RoleSession},
	{line: "node /usr/local/bin/gemini -m gemini-2.5-pro", provider: event.ProviderGemini, role: RoleSession},
	{line: "/usr/local/bin/node --max-old-space-size=8192 --no-warnings=DEP0040 /usr/local/lib/node_modules/@google/gemini-cli/bundle/gemini.js", provider: event.ProviderGemini, role: RoleSession},
	{line: "gemini mcp list", provider: event.ProviderGemini, role: RoleTool},
	{line: "gemini extensions install x", provider: event.ProviderGemini, role: RoleTool},
	{line: "/Applications/Gemini.app/Contents/MacOS/gemini"},
	{line: "node server.js gemini"},
	{line: "vim gemini.md"},

	// Cursor CLI.
	{line: "cursor-agent", provider: event.ProviderCursor, role: RoleSession},
	{line: "bash /home/u/.local/bin/cursor-agent -p fix", provider: event.ProviderCursor, role: RoleSession},
	{line: "/home/u/.local/share/cursor-agent/versions/2026.09.02-abc123/node --use-system-ca /home/u/.local/share/cursor-agent/versions/2026.09.02-abc123/index.js resume", provider: event.ProviderCursor, role: RoleSession},
	{line: "cursor-agent --model gpt-5-codex", provider: event.ProviderCursor, role: RoleSession},
	{line: "cursor-agent login", provider: event.ProviderCursor, role: RoleTool},
	{line: "cursor-agent status", provider: event.ProviderCursor, role: RoleTool},
	{line: "agent"},
	{line: "/Applications/Cursor.app/Contents/Resources/app/bin/cursor-agent"},
	{line: "node server.js cursor-agent"},

	// OpenCode. Step 0 row 8: opencode serve is a server.
	{line: "opencode", provider: event.ProviderOpenCode, role: RoleSession},
	{line: "opencode -m google/gemini-2.5-pro", provider: event.ProviderOpenCode, role: RoleSession},
	{line: "/home/u/.opencode/bin/opencode run fix it", provider: event.ProviderOpenCode, role: RoleSession},
	{line: "opencode serve --port 4096", provider: event.ProviderOpenCode, role: RoleServer},
	{line: "opencode session export ses_x", provider: event.ProviderOpenCode, role: RoleTool},
	{line: `C:\Users\me\AppData\Roaming\npm\node_modules\opencode-ai\bin\opencode.exe`, provider: event.ProviderOpenCode, role: RoleSession},

	// Copilot CLI. Step 0 rows 2 and 3: an editor extension and a bare
	// /opt/copilot script are not the Copilot CLI.
	{line: "copilot", provider: event.ProviderCopilot, role: RoleSession},
	{line: "copilot --resume", provider: event.ProviderCopilot, role: RoleSession},
	{line: "node /opt/homebrew/lib/node_modules/@github/copilot/index.js --resume", provider: event.ProviderCopilot, role: RoleSession},
	{line: `"C:\Program Files\nodejs\node.exe" C:\Users\me\AppData\Roaming\npm\node_modules\@github\copilot\index.js`, provider: event.ProviderCopilot, role: RoleSession},
	{line: "node /extensions/github.copilot/chat.js"},
	{line: "node /opt/copilot/index.js --log-dir /home/u/firekeeper-logs"},
	{line: "/Library/Caches/github-copilot-sdk/cli/1.0.0/copilot --stdio"},
	{args: []string{"/Applications/GitHub Copilot.app/Contents/Resources/copilot", "--server"}},

	// Nothing else.
	{line: "/bin/zsh"},
	{line: `C:\Windows\explorer.exe`},
	{line: ""},
}

func corpusArgs(line string, args []string) []string {
	if args != nil {
		return args
	}
	return fallbackArgs(line)
}

func TestSignatureCorpus(t *testing.T) {
	for _, tt := range signatureCorpus {
		args := corpusArgs(tt.line, tt.args)
		provider, role, ok := classifyWith(t, adapters, args)
		wantOK := tt.provider != ""
		if ok != wantOK || provider != tt.provider || (ok && role != tt.role) {
			t.Errorf("classify(%q) = %q %s %v, want %q %s", args, provider, role, ok, tt.provider, tt.role)
		}
	}
}

// The order of the signatures doesn't matter (invariant 3).
func TestSignatureOrderDoesNotMatter(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		order := append([]providerAdapter(nil), adapters...)
		r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, tt := range signatureCorpus {
			args := corpusArgs(tt.line, tt.args)
			p1, r1, ok1 := classifyWith(t, adapters, args)
			p2, r2, ok2 := classifyWith(t, order, args)
			if p1 != p2 || r1 != r2 || ok1 != ok2 {
				t.Fatalf("classify(%q) depends on order: %q %s vs %q %s", args, p1, r1, p2, r2)
			}
		}
	}
}

// Every event.Provider has exactly one adapter, every adapter's provider is a
// known constant, and every provider has a display name (invariant 9).
func TestAdapterRegistry(t *testing.T) {
	count := make(map[event.Provider]int)
	for _, a := range adapters {
		if !a.Provider().Valid() {
			t.Errorf("adapter provider %q is not a known event.Provider", a.Provider())
		}
		count[a.Provider()]++
	}
	for _, p := range event.Providers() {
		if count[p] != 1 {
			t.Errorf("provider %q has %d adapters, want 1", p, count[p])
		}
		if toolName(p) == "" {
			t.Errorf("provider %q has no display name", p)
		}
	}
	if toolName("") != "" || toolName("unknown") != "" {
		t.Error("toolName returned a name for an unknown provider")
	}
}

func TestRoleString(t *testing.T) {
	for role, want := range map[Role]string{RoleUnknown: "unknown", RoleSession: "session", RoleServer: "server", RoleTool: "tool", Role(99): "invalid"} {
		if got := role.String(); got != want {
			t.Errorf("Role(%d).String() = %q, want %q", int(role), got, want)
		}
	}
}

const (
	sessionUUID = "0f8fad5b-d9cb-469f-a165-70867728950e"
	otherUUID   = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

func TestSessionIDPerAdapter(t *testing.T) {
	tests := []struct {
		adapter providerAdapter
		line    string
		args    []string
		want    string
	}{
		// Happy path: each provider's id format.
		{adapter: codexAdapter{}, line: "codex resume " + sessionUUID, want: sessionUUID},
		{adapter: codexAdapter{}, line: "codex resume -m o3 " + sessionUUID, want: sessionUUID},
		{adapter: codexAdapter{}, line: "codex exec resume " + sessionUUID, want: sessionUUID},
		{adapter: codexAdapter{}, line: "codex exec --json resume " + sessionUUID, want: sessionUUID},
		{adapter: codexAdapter{}, line: "codex e -o out.txt --ephemeral resume --last-not-a-flag " + sessionUUID},
		{adapter: codexAdapter{}, line: "codex exec -o out.txt resume -m o3 " + sessionUUID, want: sessionUUID},
		{adapter: claudeAdapter{}, line: "claude --resume " + sessionUUID, want: sessionUUID},
		{adapter: claudeAdapter{}, line: "claude --session-id " + sessionUUID, want: sessionUUID},
		{adapter: openCodeAdapter{}, line: "opencode -s ses_ffe9a1b2c3d4Abc123XYZ789", want: "ses_ffe9a1b2c3d4Abc123XYZ789"},
		{adapter: copilotAdapter{}, line: "copilot --resume=" + sessionUUID, want: sessionUUID},
		{adapter: geminiAdapter{}, line: "gemini --resume " + sessionUUID, want: sessionUUID},
		{adapter: cursorAdapter{}, line: "cursor-agent --resume " + sessionUUID, want: sessionUUID},
		// Error path: not a UUID (a Codex session name), no id, another
		// flag's value, forks, a non-session role, look-alikes.
		{adapter: codexAdapter{}, line: "codex resume my-session-name"},
		{adapter: codexAdapter{}, line: "codex resume --last"},
		{adapter: codexAdapter{}, line: "codex resume"},
		{adapter: codexAdapter{}, line: "codex fork " + sessionUUID},
		{adapter: codexAdapter{}, line: "codex exec fork " + sessionUUID},
		{adapter: codexAdapter{}, line: "codex exec --json fork " + sessionUUID},
		{adapter: codexAdapter{}, line: "codex exec --unknown resume " + sessionUUID},
		{adapter: codexAdapter{}, line: "codex exec resume my-thread-name"},
		{adapter: codexAdapter{}, line: "codex exec review " + sessionUUID},
		{adapter: codexAdapter{}, line: "codex app-server"},
		{adapter: codexAdapter{}, line: "codex " + sessionUUID},
		{adapter: codexAdapter{}, line: "codex --unknown resume " + sessionUUID},
		{adapter: codexAdapter{}, line: "codex -m resume " + sessionUUID},
		{adapter: kimiAdapter{}, line: "kimi -S " + sessionUUID},
		{adapter: kimiAdapter{}, line: "kimi -r " + sessionUUID},
		{adapter: kimiAdapter{}, line: "kimi -S"},
		{adapter: kimiAdapter{}, line: "kimi -c"},
		{adapter: kimiAdapter{}, args: []string{"kimi", "-p", "-S abc"}},
		{adapter: claudeAdapter{}, line: "claude --resume " + sessionUUID + " --fork-session"},
		{adapter: claudeAdapter{}, line: "claude mcp serve --session-id " + sessionUUID},
		{adapter: openCodeAdapter{}, line: "opencode serve --session ses_ffe9a1b2c3d4Abc123XYZ789"},
		{adapter: openCodeAdapter{}, line: "opencode -s ses_ffe9a1b2c3d4Abc123XYZ789 --fork"},
		{adapter: geminiAdapter{}, line: "gemini --resume latest"},
		{adapter: cursorAdapter{}, line: "cursor-agent --resume"},
		{adapter: copilotAdapter{}, line: "copilot --resume=not-a-uuid"},
		// Another provider's command line is never this adapter's session.
		{adapter: claudeAdapter{}, line: "codex resume " + sessionUUID},
		{adapter: codexAdapter{}, line: "claude --resume " + sessionUUID},
	}
	for _, tt := range tests {
		args := corpusArgs(tt.line, tt.args)
		got, ok := tt.adapter.SessionID(parseInvocation(args))
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("%T.SessionID(%q) = %q, %v; want %q", tt.adapter, args, got, ok, tt.want)
		}
	}
}

func FuzzSessionID(f *testing.F) {
	for _, seed := range []string{
		"codex resume " + sessionUUID, "codex exec resume " + otherUUID, "claude --resume " + sessionUUID,
		"opencode -s ses_abc", "copilot --resume=" + sessionUUID, "gemini --session-id abc_1",
		"cursor-agent --resume " + sessionUUID, "kimi -S " + sessionUUID, "codex fork " + sessionUUID,
	} {
		f.Add(seed)
	}
	validators := map[event.Provider]func(string) bool{
		event.ProviderCodex:    standalone().validUUID,
		event.ProviderClaude:   standalone().validUUID,
		event.ProviderCopilot:  standalone().validUUID,
		event.ProviderCursor:   standalone().validUUID,
		event.ProviderOpenCode: ValidOpenCodeSessionID,
		event.ProviderGemini:   func(id string) bool { return ValidGeminiSessionID(id) },
		event.ProviderKimi:     func(string) bool { return false },
	}
	f.Fuzz(func(t *testing.T, line string) {
		inv := parseInvocation(strings.Fields(line))
		for _, a := range adapters {
			id, ok := a.SessionID(inv)
			if ok && !validators[a.Provider()](id) {
				t.Fatalf("%T.SessionID(%q) = %q, which fails validation", a, line, id)
			}
			if !ok && id != "" {
				t.Fatalf("%T.SessionID(%q) returned %q with ok false", a, line, id)
			}
		}
	})
}

// Every corpus case through both inputs: a ps line split as the fallback
// splits it, and an argument list joined back into the ps line it would
// produce. Where no argument has a space, both give the same result; where
// one does, ps loses the quoting, and the fallback must fail safe: the same
// result, or no provider, never another provider or role.
func TestSignatureCorpusBothPaths(t *testing.T) {
	for _, tt := range signatureCorpus {
		if tt.args == nil {
			if strings.ContainsRune(tt.line, '"') {
				continue // Windows quoting: the fallback is the argument list
			}
			p1, r1, ok1 := classifyWith(t, adapters, strings.Fields(tt.line))
			p2, r2, ok2 := classifyWith(t, adapters, fallbackArgs(tt.line))
			if p1 != p2 || r1 != r2 || ok1 != ok2 {
				t.Errorf("%q: argument list %q %s, ps fallback %q %s", tt.line, p1, r1, p2, r2)
			}
			continue
		}
		pa, ra, oka := classifyWith(t, adapters, tt.args)
		pf, rf, okf := classifyWith(t, adapters, fallbackArgs(strings.Join(tt.args, " ")))
		spaced := false
		for _, a := range tt.args {
			spaced = spaced || strings.ContainsAny(a, " \t")
		}
		switch {
		case !spaced && (pa != pf || ra != rf || oka != okf):
			t.Errorf("%q: argument list %q %s, ps fallback %q %s", tt.args, pa, ra, pf, rf)
		case spaced && okf && (pa != pf || ra != rf):
			t.Errorf("%q: ps fallback %q %s, argument list %q %s; the fallback must fail safe", tt.args, pf, rf, pa, ra)
		}
	}
}
