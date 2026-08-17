package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type terminalSwitchTarget struct {
	provider    string
	tty         string
	cwd         string
	sessionID   string
	processPIDs []int
}

type herdrCommandRunner func(context.Context, ...string) ([]byte, error)
type herdrProcessRunner func(context.Context) ([]byte, error)

type herdrSession struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	Running bool   `json:"running"`
}

type herdrSessionList struct {
	Sessions []herdrSession `json:"sessions"`
}

type herdrAgentSession struct {
	Agent string `json:"agent"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type herdrAgent struct {
	Agent         string             `json:"agent"`
	PaneID        string             `json:"pane_id"`
	CWD           string             `json:"cwd"`
	ForegroundCWD string             `json:"foreground_cwd"`
	AgentSession  *herdrAgentSession `json:"agent_session,omitempty"`
}

type herdrAgentListResponse struct {
	Result struct {
		Agents []herdrAgent `json:"agents"`
	} `json:"result"`
}

type herdrForegroundProcess struct {
	PID int `json:"pid"`
}

type herdrProcessInfoResponse struct {
	Result struct {
		ProcessInfo struct {
			ForegroundProcesses []herdrForegroundProcess `json:"foreground_processes"`
		} `json:"process_info"`
	} `json:"result"`
}

type herdrAgentCandidate struct {
	session string
	agent   herdrAgent
}

type herdrFocusTarget struct {
	session   string
	paneID    string
	clientTTY string
}

func newTerminalSwitchTarget(group processGroup, sessionIndex int) terminalSwitchTarget {
	target := terminalSwitchTarget{
		provider: group.tool,
		tty:      group.root.tty,
		cwd:      groupWorkingDirectory(group),
	}
	seen := make(map[int]bool, len(group.processes)+1)
	if group.root.pid > 0 {
		seen[group.root.pid] = true
		target.processPIDs = append(target.processPIDs, group.root.pid)
	}
	for _, process := range group.processes {
		if process.pid <= 0 || seen[process.pid] {
			continue
		}
		seen[process.pid] = true
		target.processPIDs = append(target.processPIDs, process.pid)
	}
	if sessionIndex >= 0 && sessionIndex < len(group.sessions) {
		session := group.sessions[sessionIndex]
		target.sessionID = session.id
		if session.cwd != "" {
			target.cwd = session.cwd
		}
	}
	return target
}

func runHerdrCommand(ctx context.Context, arguments ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "herdr", arguments...).Output()
}

func listHerdrAgents(ctx context.Context, run herdrCommandRunner) ([]herdrAgentCandidate, error) {
	output, err := run(ctx, "session", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("list Herdr sessions: %w", err)
	}
	var sessionList herdrSessionList
	if err := json.Unmarshal(output, &sessionList); err != nil {
		return nil, fmt.Errorf("decode Herdr sessions: %w", err)
	}

	var candidates []herdrAgentCandidate
	var firstErr error
	successfulLists := 0
	for _, session := range sessionList.Sessions {
		if !session.Running || strings.TrimSpace(session.Name) == "" {
			continue
		}
		output, err := run(ctx, "--session", session.Name, "agent", "list")
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("list Herdr agents in session %q: %w", session.Name, err)
			}
			continue
		}
		var response herdrAgentListResponse
		if err := json.Unmarshal(output, &response); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("decode Herdr agents in session %q: %w", session.Name, err)
			}
			continue
		}
		successfulLists++
		for _, agent := range response.Result.Agents {
			if strings.TrimSpace(agent.PaneID) == "" {
				continue
			}
			candidates = append(candidates, herdrAgentCandidate{session: session.Name, agent: agent})
		}
	}
	if successfulLists == 0 && firstErr != nil {
		return nil, firstErr
	}
	return candidates, nil
}

func resolveHerdrTarget(
	ctx context.Context,
	target terminalSwitchTarget,
	run herdrCommandRunner,
) (herdrFocusTarget, bool, error) {
	candidates, err := listHerdrAgents(ctx, run)
	if err != nil {
		return herdrFocusTarget{}, false, err
	}
	provider := herdrProviderName(target.provider)
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.agent.Agent, provider) {
			filtered = append(filtered, candidate)
		}
	}

	if target.sessionID != "" {
		var matches []herdrAgentCandidate
		for _, candidate := range filtered {
			session := candidate.agent.AgentSession
			if session != nil && session.Value == target.sessionID &&
				(session.Agent == "" || strings.EqualFold(session.Agent, provider)) {
				matches = append(matches, candidate)
			}
		}
		if match, ok := uniqueHerdrCandidate(matches); ok {
			return herdrFocusTarget{session: match.session, paneID: match.agent.PaneID}, true, nil
		}
	}

	pidSet := make(map[int]bool, len(target.processPIDs))
	for _, pid := range target.processPIDs {
		if pid > 0 {
			pidSet[pid] = true
		}
	}
	if len(pidSet) > 0 {
		var matches []herdrAgentCandidate
		for _, candidate := range filtered {
			output, err := run(ctx, "--session", candidate.session, "pane", "process-info", "--pane", candidate.agent.PaneID)
			if err != nil {
				continue
			}
			var response herdrProcessInfoResponse
			if json.Unmarshal(output, &response) != nil {
				continue
			}
			for _, process := range response.Result.ProcessInfo.ForegroundProcesses {
				if pidSet[process.PID] {
					matches = append(matches, candidate)
					break
				}
			}
		}
		if match, ok := uniqueHerdrCandidate(matches); ok {
			return herdrFocusTarget{session: match.session, paneID: match.agent.PaneID}, true, nil
		}
	}

	if strings.TrimSpace(target.cwd) != "" {
		var matches []herdrAgentCandidate
		for _, candidate := range filtered {
			if sameWorkingDirectory(target.cwd, candidate.agent.ForegroundCWD) ||
				sameWorkingDirectory(target.cwd, candidate.agent.CWD) {
				matches = append(matches, candidate)
			}
		}
		if match, ok := uniqueHerdrCandidate(matches); ok {
			return herdrFocusTarget{session: match.session, paneID: match.agent.PaneID}, true, nil
		}
	}

	return herdrFocusTarget{}, false, nil
}

func focusHerdrTarget(ctx context.Context, target terminalSwitchTarget) (herdrFocusTarget, bool, error) {
	return focusHerdrTargetWith(ctx, target, runHerdrCommand, func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,tty=,command=").Output()
	})
}

func focusHerdrTargetWith(
	ctx context.Context,
	target terminalSwitchTarget,
	run herdrCommandRunner,
	listProcesses herdrProcessRunner,
) (herdrFocusTarget, bool, error) {
	resolved, found, err := resolveHerdrTarget(ctx, target, run)
	if err != nil || !found {
		return resolved, found, err
	}
	if _, err := run(ctx, "--session", resolved.session, "agent", "focus", resolved.paneID); err != nil {
		return resolved, true, fmt.Errorf("focus Herdr pane: %w", err)
	}
	if output, err := listProcesses(ctx); err == nil {
		resolved.clientTTY = herdrClientTTYFromProcessList(string(output), resolved.session)
	}
	return resolved, true, nil
}

func uniqueHerdrCandidate(matches []herdrAgentCandidate) (herdrAgentCandidate, bool) {
	if len(matches) != 1 {
		return herdrAgentCandidate{}, false
	}
	return matches[0], true
}

func herdrProviderName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "copilot", "github copilot", "github copilot cli":
		return "copilot"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func sameWorkingDirectory(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}

func herdrClientTTYFromProcessList(output, session string) string {
	type client struct {
		pid int
		tty string
	}
	var clients []client
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		tty := normalizeTTY(fields[2])
		if tty == "" || !isHerdrClientCommand(fields[3:], session) {
			continue
		}
		clients = append(clients, client{pid: pid, tty: tty})
	}
	if len(clients) == 0 {
		return ""
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].pid < clients[j].pid })
	return clients[0].tty
}

func isHerdrClientCommand(command []string, session string) bool {
	if len(command) == 0 || filepath.Base(command[0]) != "herdr" {
		return false
	}
	arguments := command[1:]
	selectedSession := "default"
	for index := 0; index < len(arguments); index++ {
		switch {
		case arguments[index] == "--session" && index+1 < len(arguments):
			selectedSession = arguments[index+1]
			arguments = append(arguments[:index], arguments[index+2:]...)
			index--
		case strings.HasPrefix(arguments[index], "--session="):
			selectedSession = strings.TrimPrefix(arguments[index], "--session=")
			arguments = append(arguments[:index], arguments[index+1:]...)
			index--
		}
	}
	if len(arguments) >= 3 && arguments[0] == "session" && arguments[1] == "attach" {
		selectedSession = arguments[2]
		arguments = arguments[3:]
	}
	if selectedSession != session {
		return false
	}
	return len(arguments) == 0 || (len(arguments) == 1 && arguments[0] == "client")
}
