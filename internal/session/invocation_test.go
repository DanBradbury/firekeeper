package session

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParseInvocation(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		program  string
		launcher launcher
		script   string
		rest     []string
	}{
		{"native program", []string{"/usr/local/bin/codex", "resume"}, "/usr/local/bin/codex", LauncherNone, "", []string{"resume"}},
		{"node script", []string{"node", "/lib/@moonshot-ai/kimi-code/dist/main.mjs", "web"}, "node", LauncherScript, "/lib/@moonshot-ai/kimi-code/dist/main.mjs", []string{"web"}},
		{"node flags before script", []string{"/usr/bin/node", "--max-old-space-size=8192", "--no-warnings", "/x/gemini.js", "-m", "pro"}, "/usr/bin/node", LauncherScript, "/x/gemini.js", []string{"-m", "pro"}},
		{"node value flag before script", []string{"node", "-r", "./preload.js", "/x/cli.js"}, "node", LauncherScript, "/x/cli.js", nil},
		{"bash script", []string{"bash", "/home/u/.local/bin/cursor-agent", "-p", "fix"}, "bash", LauncherScript, "/home/u/.local/bin/cursor-agent", []string{"-p", "fix"}},
		{"provider -p after script is the provider's", []string{"node", "/x/@moonshot-ai/kimi-code/dist/main.mjs", "-p", "hi"}, "node", LauncherScript, "/x/@moonshot-ai/kimi-code/dist/main.mjs", []string{"-p", "hi"}},
		{"conhost hosting codex", []string{`C:\Windows\System32\conhost.exe`, "0xffffffff", "-ForceV1", "codex"}, "codex", LauncherNone, "", []string{}},
		{"double dash then script", []string{"node", "--", "/x/cli.js", "a"}, "node", LauncherScript, "/x/cli.js", []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseInvocation(tt.args)
			if got.Program != tt.program || got.Launcher != tt.launcher || got.Script != tt.script {
				t.Fatalf("parseInvocation(%q) = program %q launcher %d script %q", tt.args, got.Program, got.Launcher, got.Script)
			}
			if len(got.Rest) != len(tt.rest) || (len(tt.rest) > 0 && !reflect.DeepEqual(got.Rest, tt.rest)) {
				t.Fatalf("rest = %q, want %q", got.Rest, tt.rest)
			}
		})
	}
}

func TestParseInvocationRejectsInlineCodeAndEmpty(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{},
		{""},
		{"node", "-e", "require('/x/@moonshot-ai/kimi-code/dist/main.mjs')"},
		{"node", "--eval", "x"},
		{"node", "-p", "process.argv"},
		{"bash", "-c", "codex"},
		{"python3", "-m", "kimi_cli"},
		{"python3", "-c", "import kimi"},
		{"node"},
		{"node", "--inspect"},
		{`C:\Windows\System32\conhost.exe`, "--headless"},
	} {
		inv := parseInvocation(args)
		if inv.Script != "" || (inv.Launcher == LauncherNone && inv.Program != "" && len(args) > 0 && !strings.EqualFold(baseName(args[0]), "conhost.exe")) {
			t.Errorf("parseInvocation(%q) = program %q launcher %d script %q, want no entry point", args, inv.Program, inv.Launcher, inv.Script)
		}
	}
}

// kimiTestSpec mirrors the shape of a provider's flags for the scan tests.
var kimiTestSpec = flagSpec{
	value:    map[string]bool{"-m": true, "--model": true, "-p": true, "--prompt": true},
	optional: map[string]bool{"-S": true, "--session": true},
	multi:    map[string]bool{"-i": true},
	boolean:  map[string]bool{"-c": true, "--continue": true, "-y": true},
}

func TestScanCommand(t *testing.T) {
	tests := []struct {
		args      string
		words     []string
		uncertain bool
	}{
		// Happy path: known flags are skipped, the first word is the command.
		{"web", []string{"web"}, false},
		{"web rotate-token", []string{"web", "rotate-token"}, false},
		{"-m kimi-k2 web", []string{"web"}, false},
		{"--model=kimi-k2 web", []string{"web"}, false},
		{"-mkimi-k2 web", []string{"web"}, false},
		{"-c web", []string{"web"}, false},
		{"--foo=1 export", []string{"export"}, false},
		{"--foo -m kimi-k2 export", []string{"export"}, false},
		{"--foo --bar", nil, false},
		// A value flag's value is never the command.
		{"-p web", nil, false},
		{"-S web", nil, false},
		{"-S -c", nil, false},
		{"-i a.png b.png export", nil, false},
		{"-- web", nil, false},
		{"", nil, false},
		// Error path: an unknown flag followed by a word makes it uncertain.
		{"--foo web", []string{"web"}, true},
		{"--foo notes export", []string{"notes", "export"}, true},
		{"--foo --bar export", []string{"export"}, true},
		{"--foo notes.md", []string{"notes.md"}, true},
		{"-p --zz web", []string{"web"}, true},
	}
	// kimi -p "-S abc": the quoted prompt is one argument and never a command.
	if got := scanCommand([]string{"-p", "-S abc"}, kimiTestSpec); len(got.Words) != 0 || got.Uncertain {
		t.Errorf(`scanCommand(-p "-S abc") = %q uncertain=%v, want no command`, got.Words, got.Uncertain)
	}
	for _, tt := range tests {
		got := scanCommand(strings.Fields(tt.args), kimiTestSpec)
		if !slices.Equal(got.Words, tt.words) || got.Uncertain != tt.uncertain {
			t.Errorf("scanCommand(%q) = %q uncertain=%v, want %q uncertain=%v", tt.args, got.Words, got.Uncertain, tt.words, tt.uncertain)
		}
	}
}

func TestFlagValue(t *testing.T) {
	args := []string{"-m", "o3", "--session=abc", "--", "-S", "late"}
	if v, ok := flagValue(args, "-m", "--model"); !ok || v != "o3" {
		t.Fatalf("flagValue -m = %q %v", v, ok)
	}
	if v, ok := flagValue(args, "--session", "-S"); !ok || v != "abc" {
		t.Fatalf("flagValue --session = %q %v", v, ok)
	}
	if _, ok := flagValue([]string{"-m"}, "-m"); ok {
		t.Fatal("a flag with no value reported one")
	}
	if _, ok := flagValue([]string{"--", "-m", "x"}, "-m"); ok {
		t.Fatal("a flag after -- reported a value")
	}
}

func TestWindowsArgs(t *testing.T) {
	tests := []struct {
		command string
		want    []string
	}{
		{`codex`, []string{"codex"}},
		{`"C:\Program Files\nodejs\node.exe" C:\npm\@openai\codex\bin\codex.js resume`, []string{`C:\Program Files\nodejs\node.exe`, `C:\npm\@openai\codex\bin\codex.js`, "resume"}},
		{`C:\bin\kimi.exe -p "web ui" -m k2`, []string{`C:\bin\kimi.exe`, "-p", "web ui", "-m", "k2"}},
		{`a.exe "C:\dir with space\"`, []string{"a.exe", `C:\dir with space"`}},
		{`a.exe C:\dir\\ x`, []string{"a.exe", `C:\dir\\`, "x"}},
		{`a.exe "say \"hi\""`, []string{"a.exe", `say "hi"`}},
		{`a.exe \\\"x`, []string{"a.exe", `\"x`}},
		{`a.exe "a""b"`, []string{"a.exe", `a"b`}},
		{`"C:\no close quote`, []string{`C:\no close quote`}},
		{"  \t ", nil},
	}
	for _, tt := range tests {
		if got := windowsArgs(tt.command); !slices.Equal(got, tt.want) {
			t.Errorf("windowsArgs(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestFallbackArgs(t *testing.T) {
	if got := fallbackArgs("node /x/cli.js  --resume abc"); !slices.Equal(got, []string{"node", "/x/cli.js", "--resume", "abc"}) {
		t.Fatalf("unquoted = %q", got)
	}
	if got := fallbackArgs(`"C:\a b\kimi.exe" web`); !slices.Equal(got, []string{`C:\a b\kimi.exe`, "web"}) {
		t.Fatalf("quoted = %q", got)
	}
	if got := fallbackArgs(""); len(got) != 0 {
		t.Fatalf("empty = %q", got)
	}
}

func TestSplitNULArgs(t *testing.T) {
	if got := splitNULArgs([]byte("kimi\x00--foo\x00a b\x00"), -1); !slices.Equal(got, []string{"kimi", "--foo", "a b"}) {
		t.Fatalf("cmdline = %q", got)
	}
	if got := splitNULArgs([]byte("a\x00b\x00HOME=/x\x00"), 2); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("limited = %q", got)
	}
	if got := splitNULArgs(nil, -1); len(got) != 0 {
		t.Fatalf("empty = %q", got)
	}
}

var errTestNoArgs = errors.New("no argument list")

func TestProcessArgsUsesSeamThenFallback(t *testing.T) {
	d := &discoverer{opts: Options{Args: func(pid int) ([]string, error) {
		return []string{"/opt/my dir/kimi", "web"}, nil
	}}}
	if got := d.processArgs(1, "/opt/my dir/kimi web"); !slices.Equal(got, []string{"/opt/my dir/kimi", "web"}) {
		t.Fatalf("seam = %q", got)
	}
	d.opts.Args = func(pid int) ([]string, error) { return nil, errTestNoArgs }
	if got := d.processArgs(1, "kimi web"); !slices.Equal(got, []string{"kimi", "web"}) {
		t.Fatalf("seam error fallback = %q", got)
	}
	// Fixture runs never read live processes: Run set, Args nil.
	d = &discoverer{opts: Options{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}}
	if got := d.processArgs(1, "codex resume"); !slices.Equal(got, []string{"codex", "resume"}) {
		t.Fatalf("fixture fallback = %q", got)
	}
}

func FuzzParseInvocation(f *testing.F) {
	for _, seed := range []string{
		"codex resume", "node /x/@moonshot-ai/kimi-code/dist/main.mjs -p web",
		"node -e x", "conhost.exe 0x1 -ForceV1 codex", "bash -c codex", "node -- x y",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		args := strings.Fields(line)
		inv := parseInvocation(args)
		if inv.Script != "" && inv.Launcher != LauncherScript {
			t.Fatalf("script %q without a script launcher", inv.Script)
		}
		if inv.Program != "" && !slices.Contains(args, inv.Program) {
			t.Fatalf("program %q not in args", inv.Program)
		}
		if len(inv.Rest) > len(args) {
			t.Fatalf("rest longer than args")
		}
		if len(inv.Rest) > 0 && !slices.Equal(inv.Rest, args[len(args)-len(inv.Rest):]) {
			t.Fatalf("rest %q is not a suffix of %q", inv.Rest, args)
		}
	})
}

// testRoles is a stand-in for a signature's role table, for the safety
// property: an unknown flag may only ever move a command line to the default.
var testRoles = map[string]string{
	"web": "server", "acp": "server", "web rotate-token": "tool",
	"export": "tool", "login": "tool",
}

func testRole(args []string) string {
	path, uncertain := matchCommand(args, kimiTestSpec, func(p string) bool { _, ok := testRoles[p]; return ok })
	if uncertain || path == "" {
		return "session"
	}
	return testRoles[path]
}

func TestMatchCommand(t *testing.T) {
	tests := []struct{ args, want string }{
		{"web", "server"},
		{"web rotate-token", "tool"},
		{"web --port 8080", "server"},
		{"-m k2 export", "tool"},
		{"fix the bug", "session"},
		// Error path: the second word can't be placed past an unknown flag
		// or a value, and an unknown flag before the command is uncertain.
		{"web --zz rotate-token", "session"},
		{"web --port 1 rotate-token", "session"},
		{"--zz web", "session"},
		{"web -- rotate-token", "server"},
	}
	for _, tt := range tests {
		if got := testRole(strings.Fields(tt.args)); got != tt.want {
			t.Errorf("role(%q) = %s, want %s", tt.args, got, tt.want)
		}
	}
}

func FuzzScanCommandUnknownFlagNeverHidesSession(f *testing.F) {
	for _, seed := range []string{"web", "-m k2 export", "-p web", "--foo=1 export", "web rotate-token", "-S web acp", ""} {
		f.Add(seed, uint8(0))
	}
	f.Fuzz(func(t *testing.T, line string, at uint8) {
		args := strings.Fields(line)
		before := testRole(args)
		i := int(at) % (len(args) + 1)
		inserted := slices.Insert(slices.Clone(args), i, "--zz-unknown")
		after := testRole(inserted)
		if after != before && after != "session" {
			t.Fatalf("inserting an unknown flag into %q at %d changed %s to %s", args, i, before, after)
		}
		_ = scanCommand(args, flagSpec{}) // no panic with an empty spec
	})
}

// BenchmarkParseAndScan measures the per-process cost a refresh adds:
// parsing a typical command line and scanning it for a subcommand.
func BenchmarkParseAndScan(b *testing.B) {
	args := []string{"/usr/local/bin/node", "--max-old-space-size=8192", "/lib/node_modules/@moonshot-ai/kimi-code/dist/main.mjs", "-m", "kimi-k2", "--yolo", "web", "--port", "5494"}
	known := func(p string) bool { return p == "web" || p == "web rotate-token" }
	for b.Loop() {
		inv := parseInvocation(args)
		_, _ = matchCommand(inv.Rest, kimiTestSpec, known)
	}
}
