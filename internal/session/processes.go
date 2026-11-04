package session

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/DanBradbury/firekeeper/pkg/event"
)

// Unlimited command width prevents long installation paths hiding harness names.
func (d *discoverer) discoverProcesses() ([]byte, error) {

	return d.command("ps", "-ww", "-axo", "pid=,ppid=,tty=,etime=,command=").Output()
}

// groupProcesses groups each process under its topmost parent with the same
// provider (invariant 7): the walk climbs from servers and tools into the
// session that started them, directly or through its other servers and
// tools, but never from a session into a server or a tool, so a session is
// the root of its own group there. A session started by another session of
// the same provider joins that session's group, as before. A group's
// Provider, Role and Tool are its root's.
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
			if !ok || parent.Provider != process.Provider || seen[parent.PID] ||
				(root.Role == RoleSession && parent.Role != RoleSession) {
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
		root := roots[rootPID]
		groups = append(groups, ProcessGroup{
			Provider:  root.Provider,
			Role:      root.Role,
			Tool:      root.Tool,
			Root:      root,
			Processes: members,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Provider == groups[j].Provider {
			return groups[i].Root.PID < groups[j].Root.PID
		}
		return groups[i].Provider < groups[j].Provider
	})
	return groups
}

func (d *discoverer) emptyFallback(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// providerMarkers are substrings every provider's program or entry point
// contains. A ps line without one can't match any signature, so its argument
// list isn't read; this only saves work, it doesn't classify.
var providerMarkers = []string{"codex", "kimi", "claude", "gemini", "cursor-agent", "opencode", "copilot"}

func mayBeProvider(command string) bool {
	lower := strings.ToLower(command)
	for _, marker := range providerMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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
		if !mayBeProvider(command) {
			continue
		}
		inv := parseInvocation(d.processArgs(pid, command))
		provider, role, ok := d.classifyProcess(inv)
		if !ok {
			continue
		}
		processes = append(processes, ProcessInfo{
			Provider:   provider,
			Role:       role,
			Invocation: inv,
			Tool:       toolName(provider),
			PID:        pid,
			PPID:       ppid,
			TTY:        d.normalizeTTY(fields[2]),
			Elapsed:    fields[3],
			Command:    command,
		})
	}

	sort.Slice(processes, func(i, j int) bool {
		if processes[i].Provider == processes[j].Provider {
			return processes[i].PID < processes[j].PID
		}
		return processes[i].Provider < processes[j].Provider
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

// classifyProcess returns the provider and role of an invocation, from the
// adapters' signatures, or ok false when no provider matches (most
// processes: shells, editors). Firekeeper itself, whose subcommands name
// providers in their flags, and Microsoft Teams, whose helpers name
// Microsoft 365 Copilot hosts, are excluded first.
func (d *discoverer) classifyProcess(inv invocation) (provider event.Provider, role Role, ok bool) {
	switch inv.programName() {
	case "", "firekeeper", "firekeeper.exe":
		return "", RoleUnknown, false
	}
	if isMicrosoftTeamsCommand(strings.ToLower(strings.Join(inv.Args, " "))) {
		return "", RoleUnknown, false
	}
	for _, a := range adapters {
		if r, matched := a.Signature().classify(inv); matched {
			return a.Provider(), r, true
		}
	}
	return "", RoleUnknown, false
}

func (d *discoverer) sanitizeProcessCommand(command string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, command)
}

// commandFields splits a process command line into fields. Windows quotes an
// executable path that contains spaces, as in "C:\Program Files\nodejs\node.exe"
// script.js, so a leading quoted path is one field, without its quotes. ps
// output has no such quoting and splits on whitespace.
func commandFields(command string) []string {
	command = strings.TrimSpace(command)
	if strings.HasPrefix(command, `"`) {
		if end := strings.IndexByte(command[1:], '"'); end >= 0 {
			return append([]string{command[1 : end+1]}, strings.Fields(command[end+2:])...)
		}
	}
	return strings.Fields(command)
}

// baseName returns the last element of an executable or script path,
// whichever of / and \ separates it. filepath.Base only knows the running
// platform's separator, and process command lines come from either kind.
func baseName(path string) string {
	return path[strings.LastIndexAny(path, `/\`)+1:]
}

// slashPath converts Windows separators so a path can be matched against
// slash-separated markers on any platform.
func slashPath(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
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
