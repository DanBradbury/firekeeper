package session

import (
	"bytes"
	"encoding/json"

	"fmt"

	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (d *discoverer) enrichCodexSessions(groups []ProcessGroup) error {
	var rootPIDs []int
	for _, group := range groups {
		if group.Tool == "Codex" {
			rootPIDs = append(rootPIDs, group.Root.PID)
		}
	}
	if len(rootPIDs) == 0 {
		return nil
	}

	rollouts, err := d.discoverOpenRollouts(rootPIDs)
	if err != nil {
		return err
	}
	threadIDs := make(map[string]bool)
	for _, paths := range rollouts {
		for _, path := range paths {
			if id, ok := d.threadIDFromRolloutPath(path); ok {
				threadIDs[id] = true
			}
		}
	}
	if len(threadIDs) == 0 {
		return nil
	}

	metadata, err := d.readThreadMetadata(threadIDs)
	if err != nil {
		return err
	}
	for groupIndex := range groups {
		group := &groups[groupIndex]
		if group.Tool != "Codex" {
			continue
		}
		seen := make(map[string]bool)
		for _, rolloutPath := range d.dropSupersededForkRollouts(rollouts[group.Root.PID]) {
			threadID, ok := d.threadIDFromRolloutPath(rolloutPath)
			if !ok || seen[threadID] {
				continue
			}
			seen[threadID] = true
			row, ok := metadata[threadID]
			if !ok {
				continue
			}
			name := d.sanitizeProcessCommand(row.Name)
			if name == "" {
				name = row.ID
			}
			state, _ := d.readRolloutSessionState(rolloutPath)
			group.Sessions = append(group.Sessions, SessionInfo{
				ID:          row.ID,
				Name:        name,
				State:       state,
				CWD:         d.sanitizeProcessCommand(row.CWD),
				Model:       d.sanitizeProcessCommand(row.Model),
				Source:      d.sanitizeProcessCommand(row.Source),
				GitBranch:   d.sanitizeProcessCommand(row.GitBranch),
				RolloutPath: rolloutPath,
				UpdatedAt:   time.UnixMilli(row.UpdatedAtMS),
				TokensUsed:  row.TokensUsed,
			})
		}
		sort.Slice(group.Sessions, func(i, j int) bool {
			return group.Sessions[i].UpdatedAt.After(group.Sessions[j].UpdatedAt)
		})
	}
	return nil
}

func (d *discoverer) discoverOpenRollouts(rootPIDs []int) (map[int][]string, error) {
	pidStrings := make([]string, len(rootPIDs))
	for index, pid := range rootPIDs {
		pidStrings[index] = strconv.Itoa(pid)
	}
	output, err := d.command("lsof", "-Fn", "-p", strings.Join(pidStrings, ",")).Output()
	if err != nil && len(output) == 0 {
		return nil, fmt.Errorf("run lsof: %w", err)
	}

	rollouts := make(map[int][]string)
	currentPID := 0
	for _, line := range strings.Split(string(output), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			currentPID, _ = strconv.Atoi(line[1:])
		case 'n':
			path := line[1:]
			if currentPID == 0 || !strings.HasPrefix(filepath.Base(path), "rollout-") || filepath.Ext(path) != ".jsonl" {
				continue
			}
			if _, ok := d.threadIDFromRolloutPath(path); !ok {
				continue
			}
			rollouts[currentPID] = append(rollouts[currentPID], path)
		}
	}
	return rollouts, nil
}

// Codex keeps parent rollouts open after forking; omit superseded parents
// so one runtime does not appear as multiple sessions.
func (d *discoverer) dropSupersededForkRollouts(paths []string) []string {
	if len(paths) < 2 {
		return paths
	}
	parents := make(map[string]bool, len(paths))
	for _, path := range paths {
		if parent := d.readRolloutParentThreadID(path); parent != "" {
			parents[parent] = true
		}
	}
	if len(parents) == 0 {
		return paths
	}
	kept := make([]string, 0, len(paths))
	for _, path := range paths {
		if id, ok := d.threadIDFromRolloutPath(path); ok && parents[id] {
			continue
		}
		kept = append(kept, path)
	}
	return kept
}

func (d *discoverer) threadIDFromRolloutPath(path string) (string, bool) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if len(name) < 36 {
		return "", false
	}
	id := name[len(name)-36:]
	if !d.validUUID(id) {
		return "", false
	}
	return id, true
}

func (d *discoverer) validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func (d *discoverer) readThreadMetadata(threadIDs map[string]bool) (map[string]ThreadMetadataRow, error) {
	ids := make([]string, 0, len(threadIDs))
	for id := range threadIDs {
		if d.validUUID(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no valid Codex thread IDs found")
	}
	sort.Strings(ids)
	quoted := make([]string, len(ids))
	for index, id := range ids {
		quoted[index] = "'" + id + "'"
	}

	statePath, err := d.codexStatePath()
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT
			id,
			COALESCE(NULLIF(name, ''), NULLIF(title, ''), NULLIF(preview, ''), id) AS display_name,
			cwd,
			COALESCE(model, '') AS model,
			COALESCE(thread_source, source, '') AS source,
			COALESCE(git_branch, '') AS git_branch,
			COALESCE(updated_at_ms, updated_at * 1000) AS updated_at_ms,
			tokens_used
		FROM threads
		WHERE id IN (%s)
	`, strings.Join(quoted, ","))
	output, err := d.command("sqlite3", "-readonly", "-json", statePath, query).Output()
	if err != nil {
		return nil, fmt.Errorf("read Codex state: %w", err)
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return map[string]ThreadMetadataRow{}, nil
	}

	var rows []ThreadMetadataRow
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, fmt.Errorf("decode Codex state: %w", err)
	}
	result := make(map[string]ThreadMetadataRow, len(rows))
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

func (d *discoverer) codexStatePath() (string, error) {
	codexHome := d.opts.CodexHome
	if codexHome == "" {
		codexHome = os.Getenv("CODEX_HOME")
	}
	if codexHome == "" {
		home, err := d.home()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	path := filepath.Join(codexHome, "state_5.sqlite")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("find Codex state database: %w", err)
	}
	return path, nil
}
