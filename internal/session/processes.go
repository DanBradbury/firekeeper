package session

import (
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
	case d.isClaudeCodeCommand(command):
		// Checked first: Claude Code command lines can name other
		// harnesses in resumed transcript paths.
		return "Claude"
	case strings.Contains(lower, "opencode"):
		return "OpenCode"
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
