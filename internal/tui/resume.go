package tui

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/transcript"
	tea "github.com/charmbracelet/bubbletea"
)

// resume.go reopens a recent session with its harness's own resume flag in a
// new tab of the terminal Firekeeper runs in. It launches the user's CLI the
// way they would by hand; nothing is wrapped or proxied.

// resumeIDPattern is what a provider session id may contain. Ids come from
// local files, so anything else is refused before it reaches a shell.
var resumeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// resumeArgs returns the command line that resumes session id.
func resumeArgs(provider transcript.Provider, id string) ([]string, error) {
	if !resumeIDPattern.MatchString(id) {
		return nil, fmt.Errorf("session id cannot be resumed from here")
	}
	switch provider {
	case transcript.ProviderCodex:
		return []string{"codex", "resume", id}, nil
	case transcript.ProviderCopilot:
		return []string{"copilot", "--resume=" + id}, nil
	case transcript.ProviderClaude:
		return []string{"claude", "--resume", id}, nil
	case transcript.ProviderKimi:
		return []string{"kimi", "--session", id}, nil
	case transcript.ProviderOpenCode:
		return []string{"opencode", "--session", id}, nil
	case transcript.ProviderGemini:
		return []string{"gemini", "--resume", id}, nil
	case transcript.ProviderCursor:
		return []string{"cursor-agent", "--resume", id}, nil
	default:
		return nil, fmt.Errorf("resume is not supported for %s", provider)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// resumeShellLine is the line typed into the new tab's shell.
func resumeShellLine(args []string, cwd string) string {
	quoted := make([]string, len(args))
	for index, arg := range args {
		quoted[index] = shellQuote(arg)
	}
	line := strings.Join(quoted, " ")
	if cwd != "" {
		line = "cd " + shellQuote(cwd) + " && " + line
	}
	return line
}

type resumeResultMsg struct {
	app  string
	line string
	err  error
}

func (r resumeResultMsg) status() string {
	if r.err != nil {
		return "resume failed: " + sanitizeProcessCommand(r.err.Error())
	}
	return "resumed in a new " + r.app + " tab"
}

// resumeTerminalScripts open a tab and type argv[1] into it. argv[2] is the
// working directory for terminals that take one at creation.
var resumeTerminalScripts = map[string]string{
	"Ghostty": `on run argv
	set cmdLine to item 1 of argv
	set targetCWD to item 2 of argv
	tell application id "com.mitchellh.ghostty"
		activate
		set cfg to new surface configuration from {initial working directory:targetCWD, initial input:cmdLine & linefeed}
		if (count of windows) is 0 then
			new window with configuration cfg
		else
			new tab in front window with configuration cfg
		end if
	end tell
	return "ok"
end run`,
	"iTerm2": `on run argv
	set cmdLine to item 1 of argv
	tell application id "com.googlecode.iterm2"
		activate
		if (count of windows) is 0 then
			create window with default profile
		else
			tell current window to create tab with default profile
		end if
		tell current session of current window to write text cmdLine
	end tell
	return "ok"
end run`,
	// Terminal.app has no scripting command for a new tab; Cmd-T needs the
	// Accessibility permission for the app running Firekeeper.
	"Terminal.app": `on run argv
	set cmdLine to item 1 of argv
	tell application id "com.apple.Terminal"
		activate
		if (count of windows) is 0 then
			do script cmdLine
			return "ok"
		end if
	end tell
	tell application "System Events" to keystroke "t" using command down
	delay 0.3
	tell application id "com.apple.Terminal" to do script cmdLine in front window
	return "ok"
end run`,
}

// resumeTerminalName maps TERM_PROGRAM to a supported terminal.
func resumeTerminalName(termProgram string) (string, bool) {
	switch termProgram {
	case "ghostty":
		return "Ghostty", true
	case "iTerm.app":
		return "iTerm2", true
	case "Apple_Terminal":
		return "Terminal.app", true
	default:
		return "", false
	}
}

// openResumeTab opens line in a new tab of the terminal named by termProgram.
func openResumeTab(goos, termProgram, line, cwd string, run func(string, ...string) (string, error)) (string, error) {
	if goos != "darwin" {
		return "", fmt.Errorf("new-tab resume requires macOS; run: %s", line)
	}
	app, ok := resumeTerminalName(termProgram)
	if !ok {
		return "", fmt.Errorf("new-tab resume supports Ghostty, iTerm2 and Terminal.app; run: %s", line)
	}
	if _, err := run(resumeTerminalScripts[app], line, cwd); err != nil {
		return app, err
	}
	return app, nil
}

func (m *model) resumeRecent() tea.Cmd {
	if m.recentCursor >= len(m.recentSessions) {
		return nil
	}
	recent := m.recentSessions[m.recentCursor]
	args, err := resumeArgs(recent.provider, recent.id)
	if err != nil {
		m.terminalStatus = "resume failed: " + err.Error()
		return nil
	}
	line := resumeShellLine(args, recent.cwd)
	cwd := recent.cwd
	if cwd == "" {
		cwd = os.Getenv("HOME")
	}
	m.terminalStatus = "resuming " + recent.tool + " session…"
	return func() tea.Msg {
		app, err := openResumeTab(runtime.GOOS, os.Getenv("TERM_PROGRAM"), line, cwd, runOsascript)
		return resumeResultMsg{app: app, line: line, err: err}
	}
}
