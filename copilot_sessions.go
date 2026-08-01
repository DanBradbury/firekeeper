package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	copilotEventTailBytes = int64(4 << 20)
	copilotLogHeadBytes   = int64(256 << 10)
	copilotLogTailBytes   = int64(4 << 20)
)

type copilotWorkspaceMetadata struct {
	ID         string
	CWD        string
	GitRoot    string
	Repository string
	HostType   string
	Branch     string
	ClientName string
	Name       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type copilotStoredMetadata struct {
	ID         string `json:"id"`
	CWD        string `json:"cwd"`
	Repository string `json:"repository"`
	HostType   string `json:"host_type"`
	Branch     string `json:"branch"`
	Summary    string `json:"summary"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	Model      string `json:"model"`
	TokensUsed int64  `json:"tokens_used"`
}

type copilotEventMetadata struct {
	State      sessionState
	Model      string
	UpdatedAt  time.Time
	TokensUsed int64
}

type copilotEvent struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Data      struct {
		RequestID          string `json:"requestId"`
		Model              string `json:"model"`
		NewModel           string `json:"newModel"`
		CurrentModel       string `json:"currentModel"`
		OutputTokens       int64  `json:"outputTokens"`
		ConversationTokens int64  `json:"conversationTokens"`
	} `json:"data"`
}

func enrichCopilotSessions(groups []processGroup) error {
	var groupIndexes []int
	for groupIndex := range groups {
		group := &groups[groupIndex]
		if group.tool != "Copilot" {
			continue
		}
		groupIndexes = append(groupIndexes, groupIndex)
	}
	if len(groupIndexes) == 0 {
		return nil
	}

	home, err := copilotHome()
	if err != nil {
		return err
	}
	mapped := make(map[int]string, len(groupIndexes))
	for _, groupIndex := range groupIndexes {
		group := &groups[groupIndex]
		if id, ok := copilotSessionIDFromGroupCommands(*group); ok {
			mapped[groupIndex] = id
			continue
		}
		if id, ok := copilotSessionIDFromGroupLogs(home, *group); ok {
			mapped[groupIndex] = id
			continue
		}
	}

	var fallbackPIDs []int
	pidToGroup := make(map[int]int)
	for _, groupIndex := range groupIndexes {
		if mapped[groupIndex] != "" {
			continue
		}
		group := &groups[groupIndex]
		processes := group.processes
		if len(processes) == 0 {
			processes = []processInfo{group.root}
		}
		for _, process := range processes {
			fallbackPIDs = append(fallbackPIDs, process.pid)
			pidToGroup[process.pid] = groupIndex
		}
	}
	openByPID, _ := discoverOpenCopilotSessions(fallbackPIDs)
	for _, groupIndex := range groupIndexes {
		if mapped[groupIndex] != "" {
			continue
		}
		for pid, ids := range openByPID {
			if pidToGroup[pid] != groupIndex || len(ids) == 0 {
				continue
			}
			mapped[groupIndex] = newestCopilotSession(home, ids)
			break
		}
	}

	ids := make(map[string]bool, len(mapped))
	for _, id := range mapped {
		ids[id] = true
	}
	stored, _ := readCopilotStoredMetadata(home, ids)

	missing := 0
	for _, groupIndex := range groupIndexes {
		id, ok := mapped[groupIndex]
		if !ok {
			missing++
			continue
		}
		session, err := loadCopilotSession(home, id, stored[id])
		if err != nil {
			missing++
			continue
		}
		groups[groupIndex].sessions = []sessionInfo{session}
	}
	if missing > 0 {
		return fmt.Errorf("metadata unavailable for %d runtime(s)", missing)
	}
	return nil
}

func copilotHome() (string, error) {
	if home := strings.TrimSpace(os.Getenv("COPILOT_HOME")); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".copilot"), nil
}

func discoverOpenCopilotSessions(pids []int) (map[int][]string, error) {
	unique := make(map[int]bool, len(pids))
	var pidStrings []string
	for _, pid := range pids {
		if pid <= 0 || unique[pid] {
			continue
		}
		unique[pid] = true
		pidStrings = append(pidStrings, strconv.Itoa(pid))
	}
	if len(pidStrings) == 0 {
		return map[int][]string{}, nil
	}
	sort.Strings(pidStrings)
	output, err := exec.Command("lsof", "-Fn", "-p", strings.Join(pidStrings, ",")).Output()
	if err != nil && len(output) == 0 {
		return nil, fmt.Errorf("inspect Copilot process files: %w", err)
	}

	result := make(map[int][]string)
	seen := make(map[int]map[string]bool)
	currentPID := 0
	for _, line := range strings.Split(string(output), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			currentPID, _ = strconv.Atoi(line[1:])
		case 'n':
			id, ok := copilotSessionIDFromStatePath(line[1:])
			if currentPID == 0 || !ok {
				continue
			}
			if seen[currentPID] == nil {
				seen[currentPID] = make(map[string]bool)
			}
			if !seen[currentPID][id] {
				seen[currentPID][id] = true
				result[currentPID] = append(result[currentPID], id)
			}
		}
	}
	return result, nil
}

func copilotSessionIDFromStatePath(path string) (string, bool) {
	parts := strings.FieldsFunc(filepath.Clean(path), func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for index := 0; index+1 < len(parts); index++ {
		if parts[index] == "session-state" && validUUID(parts[index+1]) {
			return parts[index+1], true
		}
	}
	return "", false
}

func copilotSessionIDFromGroupCommands(group processGroup) (string, bool) {
	for _, process := range append([]processInfo{group.root}, group.processes...) {
		if id, ok := copilotSessionIDFromCommand(process.command); ok {
			return id, true
		}
	}
	return "", false
}

func copilotSessionIDFromCommand(command string) (string, bool) {
	fields := strings.Fields(command)
	for index, field := range fields {
		for _, prefix := range []string{"--session-id=", "--resume=", "--resume:", "-r="} {
			if strings.HasPrefix(field, prefix) {
				id := strings.Trim(strings.TrimPrefix(field, prefix), "'\"")
				if validUUID(id) {
					return id, true
				}
			}
		}
		if (field == "--session-id" || field == "--resume" || field == "-r") && index+1 < len(fields) {
			id := strings.Trim(fields[index+1], "'\"")
			if validUUID(id) {
				return id, true
			}
		}
	}
	return "", false
}

func copilotSessionIDFromGroupLogs(home string, group processGroup) (string, bool) {
	seen := make(map[int]bool)
	for _, process := range append([]processInfo{group.root}, group.processes...) {
		if process.pid <= 0 || seen[process.pid] {
			continue
		}
		seen[process.pid] = true
		pattern := filepath.Join(home, "logs", fmt.Sprintf("process-*-%d.log", process.pid))
		paths, _ := filepath.Glob(pattern)
		sort.Slice(paths, func(i, j int) bool {
			left, leftErr := os.Stat(paths[i])
			right, rightErr := os.Stat(paths[j])
			if leftErr != nil || rightErr != nil {
				return paths[i] > paths[j]
			}
			return left.ModTime().After(right.ModTime())
		})
		for _, path := range paths {
			if id, err := readCopilotSessionIDFromLog(path); err == nil && validUUID(id) {
				return id, true
			}
		}
	}
	return "", false
}

func readCopilotSessionIDFromLog(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}

	if info.Size() > copilotLogTailBytes {
		if _, err := file.Seek(info.Size()-copilotLogTailBytes, io.SeekStart); err == nil {
			reader := bufio.NewReader(file)
			_, _ = reader.ReadBytes('\n')
			if id := scanCopilotLogSessionID(reader); id != "" {
				return id, nil
			}
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	id := scanCopilotLogSessionID(io.LimitReader(file, copilotLogHeadBytes))
	if id == "" {
		return "", fmt.Errorf("Copilot session marker not found")
	}
	return id, nil
}

func scanCopilotLogSessionID(reader io.Reader) string {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	lastID := ""
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "Registering foreground session:") &&
			!strings.Contains(line, "Workspace initialized:") {
			continue
		}
		if id, ok := lastUUIDInString(line); ok {
			lastID = id
		}
	}
	return lastID
}

func lastUUIDInString(value string) (string, bool) {
	for start := len(value) - 36; start >= 0; start-- {
		candidate := value[start : start+36]
		if validUUID(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func newestCopilotSession(home string, ids []string) string {
	newestID := ""
	var newestTime time.Time
	for _, id := range ids {
		if !validUUID(id) {
			continue
		}
		path := filepath.Join(home, "session-state", id, "workspace.yaml")
		info, err := os.Stat(path)
		if newestID == "" || (err == nil && info.ModTime().After(newestTime)) {
			newestID = id
			if err == nil {
				newestTime = info.ModTime()
			}
		}
	}
	return newestID
}

func readCopilotWorkspace(path string) (copilotWorkspaceMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return copilotWorkspaceMetadata{}, err
	}
	defer file.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		values[strings.TrimSpace(parts[0])] = unquoteCopilotYAMLValue(strings.TrimSpace(parts[1]))
	}
	if err := scanner.Err(); err != nil {
		return copilotWorkspaceMetadata{}, err
	}
	return copilotWorkspaceMetadata{
		ID:         values["id"],
		CWD:        values["cwd"],
		GitRoot:    values["git_root"],
		Repository: values["repository"],
		HostType:   values["host_type"],
		Branch:     values["branch"],
		ClientName: values["client_name"],
		Name:       values["name"],
		CreatedAt:  parseCopilotTime(values["created_at"]),
		UpdatedAt:  parseCopilotTime(values["updated_at"]),
	}, nil
}

func unquoteCopilotYAMLValue(value string) string {
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
		(value[0] == '\'' && value[len(value)-1] == '\'')) {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return value[1 : len(value)-1]
	}
	return value
}

func readCopilotStoredMetadata(home string, ids map[string]bool) (map[string]copilotStoredMetadata, error) {
	var validIDs []string
	for id := range ids {
		if validUUID(id) {
			validIDs = append(validIDs, id)
		}
	}
	if len(validIDs) == 0 {
		return map[string]copilotStoredMetadata{}, nil
	}
	sort.Strings(validIDs)
	quoted := make([]string, len(validIDs))
	for index, id := range validIDs {
		quoted[index] = "'" + id + "'"
	}
	query := fmt.Sprintf(`
		SELECT
			s.id,
			COALESCE(s.cwd, '') AS cwd,
			COALESCE(s.repository, '') AS repository,
			COALESCE(s.host_type, '') AS host_type,
			COALESCE(s.branch, '') AS branch,
			COALESCE(s.summary, '') AS summary,
			COALESCE(s.created_at, '') AS created_at,
			COALESCE(s.updated_at, '') AS updated_at,
			COALESCE((
				SELECT a.model
				FROM assistant_usage_events a
				WHERE a.session_id = s.id
				ORDER BY a.id DESC
				LIMIT 1
			), '') AS model,
			COALESCE((
				SELECT SUM(COALESCE(a.input_tokens, 0) + COALESCE(a.output_tokens, 0))
				FROM assistant_usage_events a
				WHERE a.session_id = s.id
			), 0) AS tokens_used
		FROM sessions s
		WHERE s.id IN (%s)
	`, strings.Join(quoted, ","))
	path := filepath.Join(home, "session-store.db")
	output, err := exec.Command("sqlite3", "-readonly", "-json", path, query).Output()
	if err != nil {
		return nil, fmt.Errorf("read Copilot session store: %w", err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return map[string]copilotStoredMetadata{}, nil
	}
	var rows []copilotStoredMetadata
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, fmt.Errorf("decode Copilot session store: %w", err)
	}
	result := make(map[string]copilotStoredMetadata, len(rows))
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

func loadCopilotSession(home, id string, stored copilotStoredMetadata) (sessionInfo, error) {
	if !validUUID(id) {
		return sessionInfo{}, fmt.Errorf("invalid Copilot session ID")
	}
	directory := filepath.Join(home, "session-state", id)
	workspace, workspaceErr := readCopilotWorkspace(filepath.Join(directory, "workspace.yaml"))
	eventsPath := filepath.Join(directory, "events.jsonl")
	events, eventsErr := readCopilotEventMetadata(eventsPath)
	if workspaceErr != nil && stored.ID == "" {
		return sessionInfo{}, fmt.Errorf("read Copilot workspace metadata: %w", workspaceErr)
	}

	cwd := firstNonEmpty(workspace.CWD, stored.CWD, workspace.GitRoot)
	repository := firstNonEmpty(workspace.Repository, stored.Repository)
	name := firstNonEmpty(workspace.Name, stored.Summary, filepath.Base(cwd), repository, id)
	source := firstNonEmpty(workspace.ClientName, workspace.HostType, stored.HostType, "github/cli")
	model := firstNonEmpty(events.Model, stored.Model)
	updatedAt := newestTime(workspace.UpdatedAt, parseCopilotTime(stored.UpdatedAt), events.UpdatedAt)
	if updatedAt.IsZero() {
		if info, err := os.Stat(filepath.Join(directory, "workspace.yaml")); err == nil {
			updatedAt = info.ModTime()
		}
	}
	state := events.State
	if eventsErr != nil || state == sessionStateUnknown {
		state = sessionStateWaiting
	}
	tokens := stored.TokensUsed
	if tokens == 0 {
		tokens = events.TokensUsed
	}
	return sessionInfo{
		id:          id,
		name:        sanitizeProcessCommand(name),
		state:       state,
		cwd:         sanitizeProcessCommand(cwd),
		model:       sanitizeProcessCommand(model),
		source:      sanitizeProcessCommand(source),
		repository:  sanitizeProcessCommand(repository),
		gitBranch:   sanitizeProcessCommand(firstNonEmpty(workspace.Branch, stored.Branch)),
		rolloutPath: eventsPath,
		updatedAt:   updatedAt,
		tokensUsed:  tokens,
	}, nil
}

func readCopilotEventMetadata(path string) (copilotEventMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return copilotEventMetadata{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return copilotEventMetadata{}, err
	}
	if info.Size() > copilotEventTailBytes {
		if _, err := file.Seek(info.Size()-copilotEventTailBytes, io.SeekStart); err != nil {
			return copilotEventMetadata{}, err
		}
		reader := bufio.NewReader(file)
		_, _ = reader.ReadBytes('\n')
		return scanCopilotEvents(reader)
	}
	return scanCopilotEvents(file)
}

func scanCopilotEvents(reader io.Reader) (copilotEventMetadata, error) {
	metadata := copilotEventMetadata{State: sessionStateUnknown}
	pendingPermissions := make(map[string]bool)
	var outputTokens int64
	var conversationTokens int64
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var event copilotEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if timestamp := parseCopilotTime(event.Timestamp); timestamp.After(metadata.UpdatedAt) {
			metadata.UpdatedAt = timestamp
		}
		metadata.Model = firstNonEmpty(event.Data.NewModel, event.Data.CurrentModel, event.Data.Model, metadata.Model)
		outputTokens += event.Data.OutputTokens
		if event.Data.ConversationTokens > conversationTokens {
			conversationTokens = event.Data.ConversationTokens
		}

		switch event.Type {
		case "session.start", "session.resume", "assistant.turn_end", "session.task_complete", "session.shutdown", "abort":
			metadata.State = sessionStateWaiting
			clear(pendingPermissions)
		case "user.message", "assistant.turn_start", "assistant.message", "tool.execution_start",
			"tool.execution_complete", "external_tool.requested", "external_tool.completed":
			metadata.State = sessionStateActive
		case "permission.requested":
			key := event.Data.RequestID
			if key == "" {
				key = fmt.Sprintf("permission-%d", len(pendingPermissions)+1)
			}
			pendingPermissions[key] = true
			metadata.State = sessionStateNeedsInput
		case "permission.completed":
			delete(pendingPermissions, event.Data.RequestID)
			if len(pendingPermissions) == 0 {
				metadata.State = sessionStateActive
			}
		}
		if len(pendingPermissions) > 0 {
			metadata.State = sessionStateNeedsInput
		}
	}
	if err := scanner.Err(); err != nil {
		return metadata, err
	}
	metadata.TokensUsed = max(outputTokens, conversationTokens)
	return metadata, nil
}

func parseCopilotTime(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newestTime(values ...time.Time) time.Time {
	var newest time.Time
	for _, value := range values {
		if value.After(newest) {
			newest = value
		}
	}
	return newest
}
