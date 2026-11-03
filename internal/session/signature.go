package session

import "strings"

// Role says what a classified process does, from its subcommand path.
type Role int

const (
	// RoleUnknown is the zero value: never shown, never linked.
	RoleUnknown Role = iota
	// RoleSession is a process the user works in: always shown, linked when
	// possible.
	RoleSession
	// RoleServer hosts sessions for clients: shown only while holding a
	// session no session group claimed.
	RoleServer
	// RoleTool is a one-off command: never shown, never linked.
	RoleTool
)

// String names the role for tests and logs.
func (r Role) String() string {
	switch r {
	case RoleUnknown:
		return "unknown"
	case RoleSession:
		return "session"
	case RoleServer:
		return "server"
	case RoleTool:
		return "tool"
	default:
		return "invalid"
	}
}

// signature is one provider's classification data: which programs and entry
// points are the provider, its flags, the role of each subcommand path, and
// the paths that are never the provider.
type signature struct {
	// programs are lowercased program base names run directly.
	programs map[string]bool
	// interpreters may run the provider's entry point as a script.
	interpreters map[string]bool
	// script reports whether a script path (slash separated) is the
	// provider's entry point.
	script func(path string) bool
	// excluded reports an invocation that matches the program but is not the
	// provider, such as a desktop app inside a .app bundle.
	excluded func(inv invocation) bool
	// excludedAllow keeps a role for an excluded invocation whose subcommand
	// path is listed, such as Codex.app's own app-server.
	excludedAllow map[string]Role
	// flags are the provider's own flags, so their values are never read as
	// a subcommand.
	flags flagSpec
	// roles maps one- and two-word subcommand paths to roles. Anything else,
	// and an uncertain scan, has the default role: session.
	roles map[string]Role
	// toolPrefixes are first-word prefixes that are tools (Claude's bg-*).
	toolPrefixes []string
}

// matches reports whether inv runs this provider's program or entry point,
// before any exclusion.
func (s signature) matches(inv invocation) bool {
	name := inv.programName()
	switch inv.Launcher {
	case LauncherNone:
		return s.programs[name]
	case LauncherScript:
		return s.interpreters[name] && s.script != nil && inv.Script != "" && s.script(slashPath(inv.Script))
	}
	return false
}

// rolePath returns the role a subcommand path has, and whether it is listed.
func (s signature) rolePath(path string) (Role, bool) {
	if role, ok := s.roles[path]; ok {
		return role, true
	}
	if !strings.Contains(path, " ") {
		for _, prefix := range s.toolPrefixes {
			if strings.HasPrefix(path, prefix) {
				return RoleTool, true
			}
		}
	}
	return RoleUnknown, false
}

// command returns the subcommand path in inv's arguments, or "" when there is
// none or it can't be known.
func (s signature) command(inv invocation) string {
	path, uncertain := matchCommand(inv.Rest, s.flags, func(p string) bool {
		_, ok := s.rolePath(p)
		return ok
	})
	if uncertain {
		return ""
	}
	return path
}

// classify returns inv's role for this provider, and false when inv is not
// this provider. Unlisted and uncertain subcommands have the default role,
// session.
func (s signature) classify(inv invocation) (Role, bool) {
	if !s.matches(inv) {
		return RoleUnknown, false
	}
	path := s.command(inv)
	if s.excluded != nil && s.excluded(inv) {
		if role, ok := s.excludedAllow[path]; ok && path != "" {
			return role, true
		}
		return RoleUnknown, false
	}
	if path == "" {
		return RoleSession, true
	}
	role, _ := s.rolePath(path)
	return role, true
}

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

func roleMap(role Role, paths ...string) map[string]Role {
	m := make(map[string]Role, len(paths))
	for _, p := range paths {
		m[p] = role
	}
	return m
}

func mergeRoles(maps ...map[string]Role) map[string]Role {
	out := make(map[string]Role)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// inAppBundle reports a program inside a macOS .app bundle.
func inAppBundle(inv invocation) bool {
	return strings.Contains(slashPath(inv.Program), ".app/Contents/")
}

var nodeInterpreters = set("node", "node.exe", "nodejs", "bun", "bun.exe")

// codexSignature comes from Codex's own argument definitions at release
// rust-v0.166.0 (codex-rs/cli/src/main.rs and the shared option structs).
var codexSignature = signature{
	programs:     set("codex", "codex.exe"),
	interpreters: nodeInterpreters,
	script:       func(path string) bool { return strings.Contains(path, "/@openai/codex/") },
	// Codex.app's window and helpers are not the CLI; its bundled
	// app-server is a Codex server.
	excluded:      inAppBundle,
	excludedAllow: map[string]Role{"app-server": RoleServer},
	flags: flagSpec{
		value: set("-c", "--config", "--enable", "--disable", "--remote", "--remote-auth-token-env",
			"-a", "--ask-for-approval", "-m", "--model", "--local-provider", "-p", "--profile",
			"-s", "--sandbox", "-C", "--cd", "--add-dir"),
		multi: set("-i", "--image"),
		boolean: set("--strict-config", "--search", "--no-alt-screen", "--no-daemon", "--oss",
			"--approve-for-me", "--not-so-yolo", "--dangerously-bypass-approvals-and-sandbox", "--yolo",
			"--dangerously-bypass-hook-trust", "--worktree", "--last", "--all",
			"--include-non-interactive", "-h", "--help", "-V", "--version"),
	},
	roles: mergeRoles(
		// agents, cloud and remote-control stay sessions until a capture
		// shows otherwise (plan step 5, Codex roles).
		roleMap(RoleSession, "resume", "fork", "exec", "e", "review", "agents", "cloud", "cloud-tasks", "remote-control"),
		roleMap(RoleServer, "app-server", "exec-server", "mcp-server"),
		roleMap(RoleTool, "login", "logout", "mcp", "plugin", "completion", "update", "doctor", "sandbox",
			"debug", "execpolicy", "apply", "a", "queue", "archive", "delete", "unarchive",
			"migrate-rollouts", "migrate-nfs-db", "features", "app", "tcp-tunnel",
			"responses-api-proxy", "stdio-to-uds", "app-server generate-ts",
			"app-server generate-json-schema", "app-server generate-internal-json-schema"),
	),
}

// codexExecFlags are Codex's flags plus codex exec's own (codex-rs/exec/src/
// cli.rs at rust-v0.166.0), for reading `codex exec … resume <id>`.
var codexExecFlags = flagSpec{
	value: mergeSets(codexSignature.flags.value, set("--thread-source", "--cyber-access-program",
		"--output-schema", "-o", "--output-last-message", "--color")),
	multi: codexSignature.flags.multi,
	boolean: mergeSets(codexSignature.flags.boolean, set("--skip-git-repo-check", "--ephemeral",
		"--ignore-user-config", "--ignore-rules", "--json", "--experimental-json", "--bypass-hook-trust")),
}

func mergeSets(sets ...map[string]bool) map[string]bool {
	out := make(map[string]bool)
	for _, s := range sets {
		for k := range s {
			out[k] = true
		}
	}
	return out
}

// kimiSignature comes from Kimi Code's documentation (ASSUMED: the package's
// bundled definitions couldn't be read). vis, provider and app stay sessions
// until a capture confirms them; there is no desktop app exclusion until its
// path is captured.
var kimiSignature = signature{
	programs:     set("kimi", "kimi.exe"),
	interpreters: nodeInterpreters,
	script:       func(path string) bool { return strings.Contains(path, "/@moonshot-ai/kimi-code/") },
	flags: flagSpec{
		value: set("-m", "--model", "-p", "--prompt", "--output-format", "--skills-dir", "--agent",
			"--agent-file", "--add-dir", "--environment", "--environment-cwd"),
		optional: set("-S", "--session", "-r", "--resume"),
		boolean:  set("-c", "--continue", "-y", "--yolo", "--yes", "--auto-approve", "--auto", "--plan", "-h", "--help", "-V", "--version"),
	},
	roles: mergeRoles(
		roleMap(RoleSession, "vis", "provider", "app"),
		roleMap(RoleServer, "acp", "web"),
		roleMap(RoleTool, "login", "doctor", "export", "migrate", "import-sessions", "upgrade", "update",
			"install-desktop", "install-app", "web rotate-token", "server kill"),
	),
}

var claudeSignature = signature{
	programs:     set("claude", "claude.exe"),
	interpreters: nodeInterpreters,
	script:       func(path string) bool { return strings.Contains(path, "@anthropic-ai/claude-code/") },
	// The desktop app and its helpers live inside a .app bundle, or under
	// AnthropicClaude or WindowsApps on Windows with Electron --type flags.
	excluded: func(inv invocation) bool { return inAppBundle(inv) || isClaudeDesktopWindows(inv.Args) },
	flags: flagSpec{
		value:    set("--session-id"),
		optional: set("-r", "--resume"),
		boolean:  set("--fork-session"),
	},
	roles: mergeRoles(
		// mcp serve is a server pending confirmation (plan step 5).
		roleMap(RoleServer, "mcp serve"),
		roleMap(RoleTool, "daemon", "mcp", "config", "update", "upgrade", "doctor", "install",
			"setup-token", "migrate-installer", "plugin", "auth"),
	),
	toolPrefixes: []string{"bg-"},
}

var geminiSignature = signature{
	programs:     set("gemini", "gemini.exe"),
	interpreters: nodeInterpreters,
	script:       isGeminiScript,
	excluded:     inAppBundle,
	flags: flagSpec{
		value: set("--session-id"),
	},
	roles: roleMap(RoleTool, "mcp", "extensions", "extension", "skills", "skill", "hooks", "hook", "gemma"),
}

var cursorSignature = signature{
	programs:     set("cursor-agent", "cursor-agent.exe"),
	interpreters: set("node", "node.exe", "bun", "bun.exe", "sh", "bash", "zsh"),
	script:       isCursorScript,
	excluded:     inAppBundle,
	flags: flagSpec{
		optional: set("--resume"),
	},
	roles: roleMap(RoleTool, "login", "logout", "status", "whoami", "update", "upgrade", "mcp", "about",
		"help", "create-chat", "install-shell-integration", "uninstall-shell-integration"),
}

var openCodeSignature = signature{
	programs:     set("opencode", "opencode.exe"),
	interpreters: nodeInterpreters,
	script:       func(path string) bool { return baseName(path) == "opencode" },
	flags: flagSpec{
		value:   set("-s", "--session", "-m", "--model"),
		boolean: set("-c", "--continue", "--fork"),
	},
	roles: mergeRoles(
		roleMap(RoleSession, "attach", "run", "pr", "mini"),
		roleMap(RoleServer, "serve", "web", "acp"),
		roleMap(RoleTool, "upgrade", "update", "uninstall", "api", "debug", "auth", "mcp", "plugin",
			"models", "stats", "session", "service", "reload", "pair", "export", "import", "github",
			"agent", "db", "completion"),
	),
}

var copilotSignature = signature{
	programs:     set("copilot", "copilot.exe"),
	interpreters: nodeInterpreters,
	script:       func(path string) bool { return strings.Contains(path, "/@github/copilot/") },
	// The desktop app's bundle and the SDK's background runtime are not the
	// CLI.
	excluded: func(inv invocation) bool {
		where := strings.ToLower(inv.Program + " " + inv.Script)
		return isCopilotDesktopCommand(where) || isCopilotSDKCommand(where)
	},
	flags: flagSpec{
		value:    set("--session-id"),
		optional: set("--resume", "-r"),
	},
}
