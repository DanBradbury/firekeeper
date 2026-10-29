package session

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"unicode"
)

// Unlimited command width prevents long installation paths hiding harness names.
func (d *discoverer) discoverProcesses() ([]byte, error) {

	return d.command("ps", "-ww", "-axo", "pid=,ppid=,tty=,etime=,command=").Output()
}

func (d *discoverer) groupProcesses(processes []ProcessInfo) []ProcessGroup {
	byPID := make(map[int]ProcessInfo, len(processes))
	for _, process := range processes {
		byPID[process.PID] = process
	}

	grouped := make(map[int][]ProcessInfo)
	roots := make(map[int]ProcessInfo)
	for _, process := range processes {
		root := process
		seen := map[int]bool{root.PID: true}
		for {
			parent, ok := byPID[root.PPID]
			if !ok || parent.Tool != process.Tool || seen[parent.PID] {
				break
			}
			seen[parent.PID] = true
			root = parent
		}
		grouped[root.PID] = append(grouped[root.PID], process)
		roots[root.PID] = root
	}

	groups := make([]ProcessGroup, 0, len(grouped))
	for rootPID, members := range grouped {
		sort.Slice(members, func(i, j int) bool { return members[i].PID < members[j].PID })
		groups = append(groups, ProcessGroup{
			Tool:      roots[rootPID].Tool,
			Root:      roots[rootPID],
			Processes: members,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Tool == groups[j].Tool {
			return groups[i].Root.PID < groups[j].Root.PID
		}
		return groups[i].Tool < groups[j].Tool
	})
	return groups
}

func (d *discoverer) emptyFallback(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (d *discoverer) parseProcesses(output string) []ProcessInfo {
	var processes []ProcessInfo
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr != nil || ppidErr != nil {
			continue
		}

		command := d.sanitizeProcessCommand(strings.Join(fields[4:], " "))
		tool := d.classifyProcess(command)
		if tool == "" {
			continue
		}
		processes = append(processes, ProcessInfo{
			Tool:    tool,
			PID:     pid,
			PPID:    ppid,
			TTY:     d.normalizeTTY(fields[2]),
			Elapsed: fields[3],
			Command: command,
		})
	}

	sort.Slice(processes, func(i, j int) bool {
		if processes[i].Tool == processes[j].Tool {
			return processes[i].PID < processes[j].PID
		}
		return processes[i].Tool < processes[j].Tool
	})
	return processes
}

func (d *discoverer) normalizeTTY(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "/dev/"))
	if value == "" || value == "??" || value == "?" || value == "-" {
		return ""
	}
	return value
}

func (d *discoverer) classifyProcess(command string) string {
	lower := strings.ToLower(command)
	switch {
	case isFirekeeperCommand(command):
		// Firekeeper's own subcommands name harnesses in their flags, such
		// as daemon --provider copilot, but are never agent runtimes.
		return ""
	case isMicrosoftTeamsCommand(lower):
		// Teams helpers name Microsoft 365 Copilot hosts in their flags.
		return ""
	case d.isClaudeCodeCommand(command):
		// Checked first: Claude Code command lines can name other
		// harnesses in resumed transcript paths.
		return "Claude"
	case d.isGeminiCommand(command):
		// Matched on the executable, so a Gemini model named in another
		// harness's flags stays with that harness.
		return "Gemini"
	case d.isCursorCommand(command):
		// Matched on the executable before the substring checks below, so
		// a model such as gpt-5-codex in Cursor CLI's flags stays here.
		return "Cursor"
	case strings.Contains(lower, "opencode"):
		return "OpenCode"
	case isCopilotDesktopCommand(lower):
		// The desktop app is not the Copilot CLI and has no CLI session.
		return ""
	case isCopilotSDKCommand(lower):
		// Background SDK runtime spawned by other apps, with no terminal.
		return ""
	case strings.Contains(lower, "copilot"):
		return "Copilot"
	case strings.Contains(lower, "codex"):
		return "Codex"
	case strings.Contains(lower, "kimi"):
		return "Kimi"
	default:
		return ""
	}
}

func (d *discoverer) sanitizeProcessCommand(command string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, command)
}

// isFirekeeperCommand reports whether command runs the firekeeper executable.
func isFirekeeperCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(filepath.Base(fields[0])) {
	case "firekeeper", "firekeeper.exe":
		return true
	}
	return false
}

// EmptyFallback returns fallback when value is the empty string.
func EmptyFallback(value, fallback string) string { return standalone().emptyFallback(value, fallback) }

// NormalizeTTY returns the terminal name ps reported without a /dev/ prefix,
// or "" when the process has no terminal.
func NormalizeTTY(value string) string { return standalone().normalizeTTY(value) }

// SanitizeProcessCommand replaces control characters in a command line with
// spaces so it is safe to show in a terminal.
func SanitizeProcessCommand(command string) string {
	return standalone().sanitizeProcessCommand(command)
}
