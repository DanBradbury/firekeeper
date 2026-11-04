package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	// Explicit ids (codex resume <UUID>) win: that group gets exactly that
	// thread, whatever it holds, and is never given a guess.
	explicit := make(map[int]string)
	for _, group := range groups {
		if id, ok := group.explicitSessionID(); ok && group.Tool == "Codex" {
			explicit[group.Root.PID] = id
			threadIDs[id] = true
		}
	}
	// Recent Codex builds keep no rollout open in the TUI process: an
	// app-server holds the thread, so session groups are matched by cwd to
	// the threads Codex processes hold. A server keeps only the threads its
	// own processes hold (evidence). Where open files can't be listed,
	// recent threads stand in, and servers are matched by cwd after the
	// session groups (the Windows exception, invariant 8).
	unmatched := d.unmatchedCodexGroups(groups, rollouts, explicit)
	servers := emptyCodexServers(groups, rollouts)
	candidates := make(map[string]bool)
	var heldBy map[int]map[string]bool
	if len(unmatched) > 0 || len(servers) > 0 {
		candidates, heldBy = d.discoverHeldCodexThreads(groups)
		if len(candidates) == 0 && !openFilesListable {
			candidates = d.discoverRecentCodexThreads()
		}
		for id := range candidates {
			threadIDs[id] = true
		}
	}
	if len(threadIDs) == 0 {
		return nil
	}

	metadata, err := d.readThreadMetadata(threadIDs)
	if err != nil {
		return err
	}
	applyExplicitCodexThreads(groups, explicit, metadata, rollouts)
	d.matchCodexGroupsByCWD(groups, unmatched, candidates, metadata, rollouts)
	if openFilesListable {
		d.giveCodexServersHeldThreads(groups, servers, heldBy, metadata, rollouts)
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
			state, tokens, _ := d.readRolloutStatus(rolloutPath)
			forkedFrom := ""
			if own, ok := readCodexSessionMetaFile(rolloutPath, row.ID); ok && own.ForkedFromID != row.ID {
				forkedFrom = d.sanitizeProcessCommand(own.ForkedFromID)
			}
			group.Sessions = append(group.Sessions, Info{
				ID:          row.ID,
				Name:        name,
				State:       state,
				CWD:         d.sanitizeProcessCommand(row.CWD),
				Model:       d.sanitizeProcessCommand(row.Model),
				Source:      d.sanitizeProcessCommand(row.Source),
				Subagent:    row.isSubagent(),
				ParentID:    row.parentThreadID(),
				AgentName:   d.sanitizeProcessCommand(row.agentNickname()),
				AgentRole:   d.sanitizeProcessCommand(row.agentRole()),
				ForkedFrom:  forkedFrom,
				GitBranch:   d.sanitizeProcessCommand(row.GitBranch),
				RolloutPath: rolloutPath,
				UpdatedAt:   time.UnixMilli(row.UpdatedAtMS),
				TokensUsed:  row.TokensUsed,
				Tokens:      tokens,
			})
		}
		sort.Slice(group.Sessions, func(i, j int) bool {
			return group.Sessions[i].UpdatedAt.After(group.Sessions[j].UpdatedAt)
		})
	}
	attachCodexSubagents(groups)
	return nil
}

// attachCodexSubagents gives each subagent thread to the runtime that owns its
// primary session and lists it right after that session. A background server
// hosts the subagents of a CLI, so they are moved to the CLI's group; a
// subagent whose primary session is on no runtime is dropped, because it is not
// something to switch to.
func attachCodexSubagents(groups []ProcessGroup) {
	var orphans []Info
	for index := range groups {
		group := &groups[index]
		if group.Tool != "Codex" {
			continue
		}
		kept := group.Sessions[:0:0]
		for _, info := range group.Sessions {
			if !info.Subagent {
				kept = append(kept, info)
				continue
			}
			orphans = append(orphans, info)
		}
		group.Sessions = kept
	}
	for _, sub := range orphans {
		if target := codexSubagentOwner(groups, sub); target != nil {
			target.Sessions = append(target.Sessions, sub)
		}
	}
	for index := range groups {
		group := &groups[index]
		if group.Tool != "Codex" || len(group.Sessions) < 2 {
			continue
		}
		group.Sessions = orderSubagentsUnderParents(group.Sessions)
	}
}

// codexSubagentOwner finds the group whose primary session spawned sub: the
// one named by ParentID, or, when the provider names none, a runtime with a
// terminal (then any runtime) whose primary session shares sub's directory.
func codexSubagentOwner(groups []ProcessGroup, sub Info) *ProcessGroup {
	var byCWD, byCWDTTY *ProcessGroup
	for index := range groups {
		group := &groups[index]
		if group.Tool != "Codex" || len(group.Sessions) == 0 || group.Sessions[0].Subagent {
			continue
		}
		for _, info := range group.Sessions {
			if sub.ParentID != "" && info.ID == sub.ParentID {
				return group
			}
		}
		if sub.ParentID == "" && sameDirectory(group.Sessions[0].CWD, sub.CWD) {
			if byCWD == nil {
				byCWD = group
			}
			if byCWDTTY == nil && group.Root.TTY != "" {
				byCWDTTY = group
			}
		}
	}
	if byCWDTTY != nil {
		return byCWDTTY
	}
	return byCWD
}

// orderSubagentsUnderParents keeps primary sessions in their order and lists
// each subagent after its parent, newest first, with parentless ones after the
// first primary session.
func orderSubagentsUnderParents(sessions []Info) []Info {
	var primaries, subs []Info
	for _, info := range sessions {
		if info.Subagent {
			subs = append(subs, info)
		} else {
			primaries = append(primaries, info)
		}
	}
	sort.SliceStable(subs, func(i, j int) bool { return subs[i].UpdatedAt.After(subs[j].UpdatedAt) })
	out := make([]Info, 0, len(sessions))
	used := make([]bool, len(subs))
	for primaryIndex, primary := range primaries {
		out = append(out, primary)
		for subIndex, sub := range subs {
			if used[subIndex] {
				continue
			}
			if sub.ParentID == primary.ID || (sub.ParentID == "" && primaryIndex == 0) {
				used[subIndex] = true
				out = append(out, sub)
			}
		}
	}
	for subIndex, sub := range subs {
		if !used[subIndex] {
			out = append(out, sub)
		}
	}
	return out
}

// unmatchedCodexGroups returns the Codex groups that may be given a thread by
// working directory: session groups with no open rollout file and no
// explicit id (invariant 8), and, only where open files can't be listed,
// servers too, after every session group (the Windows exception). Within a
// role, runtimes with a terminal come first.
func (d *discoverer) unmatchedCodexGroups(groups []ProcessGroup, rollouts map[int][]string, explicit map[int]string) []ProcessGroup {
	var unmatched []ProcessGroup
	for _, group := range groups {
		if group.Tool != "Codex" || len(rollouts[group.Root.PID]) != 0 {
			continue
		}
		if _, ok := explicit[group.Root.PID]; ok {
			continue
		}
		switch group.Role {
		case RoleSession:
		case RoleServer:
			if openFilesListable {
				continue // servers never guess where evidence can be read
			}
		case RoleTool, RoleUnknown:
			continue
		default:
			continue
		}
		unmatched = append(unmatched, group)
	}
	sort.SliceStable(unmatched, func(i, j int) bool {
		iSession, jSession := unmatched[i].Role == RoleSession, unmatched[j].Role == RoleSession
		if iSession != jSession {
			return iSession
		}
		iTTY, jTTY := unmatched[i].Root.TTY != "", unmatched[j].Root.TTY != ""
		if iTTY != jTTY {
			return iTTY
		}
		return unmatched[i].Root.PID < unmatched[j].Root.PID
	})
	return unmatched
}

// emptyCodexServers returns the Codex server groups whose root holds no
// rollout file. Their evidence is the threads their processes hold.
func emptyCodexServers(groups []ProcessGroup, rollouts map[int][]string) []ProcessGroup {
	var servers []ProcessGroup
	for _, group := range groups {
		if group.Tool == "Codex" && group.Role == RoleServer && len(rollouts[group.Root.PID]) == 0 {
			servers = append(servers, group)
		}
	}
	return servers
}

// codexServerPIDs returns the root PIDs of the Codex server groups.
func codexServerPIDs(groups []ProcessGroup) map[int]bool {
	servers := make(map[int]bool)
	for _, group := range groups {
		if group.Tool == "Codex" && group.Role == RoleServer {
			servers[group.Root.PID] = true
		}
	}
	return servers
}

// removeCodexThread takes thread id off every server's rollouts: a session
// group claimed it, so it is not the server's unclaimed session.
func removeCodexThread(rollouts map[int][]string, servers map[int]bool, id string) {
	for pid := range servers {
		rollouts[pid] = slices.DeleteFunc(rollouts[pid], func(path string) bool {
			held, ok := standalone().threadIDFromRolloutPath(path)
			return ok && held == id
		})
	}
}

// applyExplicitCodexThreads gives each group with an explicit id exactly
// that thread. An id with no known thread (a typo, a deleted or another
// machine's session) leaves the group unlinked, with no fallback guess. Two
// groups naming the same id both get it. Servers lose the thread: a session
// group claimed it.
func applyExplicitCodexThreads(groups []ProcessGroup, explicit map[int]string, metadata map[string]ThreadMetadataRow, rollouts map[int][]string) {
	servers := codexServerPIDs(groups)
	for pid, id := range explicit {
		rollouts[pid] = nil
		if row, ok := metadata[id]; ok && row.RolloutPath != "" {
			rollouts[pid] = []string{row.RolloutPath}
		}
		removeCodexThread(rollouts, servers, id)
	}
}

// giveCodexServersHeldThreads gives each server group with no rollout of its
// own the threads its processes hold open (rollout files or thread-writer
// locks), except threads a session group already has: evidence, never a
// guess (invariant 8).
func (d *discoverer) giveCodexServersHeldThreads(groups, servers []ProcessGroup, heldBy map[int]map[string]bool, metadata map[string]ThreadMetadataRow, rollouts map[int][]string) {
	if len(servers) == 0 {
		return
	}
	serverPIDs := codexServerPIDs(groups)
	claimed := make(map[string]bool)
	for pid, paths := range rollouts {
		if serverPIDs[pid] {
			continue
		}
		for _, path := range paths {
			if id, ok := d.threadIDFromRolloutPath(path); ok {
				claimed[id] = true
			}
		}
	}
	for _, server := range servers {
		held := make(map[string]bool)
		for _, process := range server.Processes {
			for id := range heldBy[process.PID] {
				held[id] = true
			}
		}
		ids := make([]string, 0, len(held))
		for id := range held {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			row, ok := metadata[id]
			if !ok || claimed[id] || row.RolloutPath == "" || slices.Contains(rollouts[server.Root.PID], row.RolloutPath) {
				continue
			}
			if pathID, ok := d.threadIDFromRolloutPath(row.RolloutPath); ok && claimed[pathID] {
				continue
			}
			rollouts[server.Root.PID] = append(rollouts[server.Root.PID], row.RolloutPath)
		}
	}
}

// discoverHeldCodexThreads returns the thread IDs that any Codex process
// holds open, either as a rollout file or as a thread-writer lock, and the
// same ids by the PID holding them.
func (d *discoverer) discoverHeldCodexThreads(groups []ProcessGroup) (map[string]bool, map[int]map[string]bool) {
	var pids []string
	for _, group := range groups {
		if group.Tool != "Codex" {
			continue
		}
		for _, process := range group.Processes {
			pids = append(pids, strconv.Itoa(process.PID))
		}
	}
	held := make(map[string]bool)
	byPID := make(map[int]map[string]bool)
	if len(pids) == 0 {
		return held, byPID
	}
	output, _ := d.command("lsof", "-Fn", "-p", strings.Join(pids, ",")).Output()
	currentPID := 0
	for _, line := range strings.Split(string(output), "\n") {
		if len(line) >= 2 && line[0] == 'p' {
			currentPID, _ = strconv.Atoi(line[1:])
			continue
		}
		if len(line) < 2 || line[0] != 'n' {
			continue
		}
		path := line[1:]
		base := filepath.Base(path)
		switch {
		case strings.HasPrefix(base, "rollout-") && filepath.Ext(base) == ".jsonl":
		case filepath.Base(filepath.Dir(path)) == "thread-writer-locks" && filepath.Ext(base) == ".lock":
		default:
			continue
		}
		if id, ok := d.threadIDFromRolloutPath(path); ok {
			held[id] = true
			if currentPID != 0 {
				if byPID[currentPID] == nil {
					byPID[currentPID] = make(map[string]bool)
				}
				byPID[currentPID][id] = true
			}
		}
	}
	return held, byPID
}

// openFilesListable reports whether discovery can list the files a process
// holds open. Windows cannot without extra tools, so Codex runtimes there are
// matched to recently updated threads by working directory instead of to the
// threads they hold. Tests replace it.
var openFilesListable = runtime.GOOS != "windows"

// recentCodexWindow is how recently a thread must have changed to be
// considered held by a running runtime when open files cannot be listed.
const recentCodexWindow = 24 * time.Hour

// discoverRecentCodexThreads returns the ids of threads updated within
// recentCodexWindow. It is the stand-in for discoverHeldCodexThreads where
// open files are unavailable, so it can name a closed thread that shares a
// working directory with a running one; state and recency still come from
// the thread itself.
func (d *discoverer) discoverRecentCodexThreads() map[string]bool {
	recent := make(map[string]bool)
	statePath, err := d.codexStatePath()
	if err != nil {
		return recent
	}
	cutoff := time.Now().Add(-recentCodexWindow).UnixMilli()
	query := fmt.Sprintf(`SELECT id FROM threads WHERE COALESCE(updated_at_ms, updated_at * 1000) >= %d ORDER BY COALESCE(updated_at_ms, updated_at * 1000) DESC LIMIT 100`, cutoff)
	output, err := d.command("sqlite3", "-readonly", "-json", statePath, query).Output()
	if err != nil || len(bytes.TrimSpace(output)) == 0 {
		return recent
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(output, &rows) != nil {
		return recent
	}
	for _, row := range rows {
		if d.validUUID(row.ID) {
			recent[row.ID] = true
		}
	}
	return recent
}

// matchCodexGroupsByCWD assigns each unmatched group the newest held,
// unclaimed, non-subagent thread that shares its working directory.
//
// A background server (an app-server daemon, no terminal) often holds the
// rollout of a CLI's thread open, because the CLI talks to it. Those threads
// stay claimable: the CLI with a terminal takes its thread from the server,
// so switching goes to the terminal that runs it.
func (d *discoverer) matchCodexGroupsByCWD(groups, unmatched []ProcessGroup, candidates map[string]bool, metadata map[string]ThreadMetadataRow, rollouts map[int][]string) {
	serverPIDs := codexServerPIDs(groups)
	claimed := make(map[string]bool)
	for pid, paths := range rollouts {
		if serverPIDs[pid] {
			continue
		}
		for _, path := range paths {
			if id, ok := d.threadIDFromRolloutPath(path); ok {
				claimed[id] = true
			}
		}
	}
	for _, group := range unmatched {
		cwd := d.processCWD(group.Root.PID)
		if cwd == "" {
			continue
		}
		var best ThreadMetadataRow
		for id := range candidates {
			row, ok := metadata[id]
			if !ok || claimed[id] || !sameDirectory(row.CWD, cwd) || row.RolloutPath == "" || row.isSubagent() {
				continue
			}
			if best.ID == "" || row.UpdatedAtMS > best.UpdatedAtMS {
				best = row
			}
		}
		if best.ID == "" {
			continue
		}
		claimed[best.ID] = true
		// A claim by a session group takes the thread off the servers that
		// hold it, so it isn't also shown as a server's session (invariant
		// 6). The condition is the role, not a terminal: codex exec run by
		// a script is a session too.
		if group.Role == RoleSession {
			removeCodexThread(rollouts, serverPIDs, best.ID)
		}
		rollouts[group.Root.PID] = append(rollouts[group.Root.PID], best.RolloutPath)
	}
}

// processCWD returns the working directory lsof reports for pid, or "".
func (d *discoverer) processCWD(pid int) string {
	output, err := d.command("lsof", "-a", "-d", "cwd", "-Fn", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "n") {
			return d.sanitizeProcessCommand(line[1:])
		}
	}
	return ""
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
			COALESCE(source, '') AS raw_source,
			COALESCE(git_branch, '') AS git_branch,
			COALESCE(updated_at_ms, updated_at * 1000) AS updated_at_ms,
			tokens_used,
			COALESCE(rollout_path, '') AS rollout_path
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

// CodexStatePath returns the path of Codex's state database under CODEX_HOME
// (default ~/.codex), or an error when the database does not exist.
func CodexStatePath() (string, error) { return standalone().codexStatePath() }

// ValidUUID reports whether value has the 8-4-4-4-12 hexadecimal UUID layout.
func ValidUUID(value string) bool { return standalone().validUUID(value) }

// ThreadIDFromRolloutPath returns the thread id embedded in a Codex rollout
// file name.
func ThreadIDFromRolloutPath(path string) (string, bool) {
	return standalone().threadIDFromRolloutPath(path)
}

// sameDirectory reports whether a and b name the same directory. Windows
// paths differ in drive-letter case, separators, trailing separators and the
// \\?\ prefix depending on which program wrote them; other paths compare
// exactly.
func sameDirectory(a, b string) bool {
	if a == b {
		return true
	}
	return normalizeDirectory(a) == normalizeDirectory(b)
}

func normalizeDirectory(path string) string {
	path = strings.TrimPrefix(path, `\\?\`)
	if len(path) < 2 || path[1] != ':' {
		return path
	}
	path = strings.TrimRight(slashPath(path), "/")
	return strings.ToLower(path)
}

// isSubagent reports whether the thread was spawned by another one rather than
// started by a person: a subagent, or the guardian review.
func (r ThreadMetadataRow) isSubagent() bool {
	switch r.Source {
	case "subagent", "guardian_review":
		return true
	}
	return strings.HasPrefix(r.Source, "{") || strings.HasPrefix(r.RawSource, "{")
}

// CodexSpawn is what a Codex subagent thread's source says about who spawned
// it. Fields are empty when Codex did not record them.
type CodexSpawn struct {
	// ParentID is the thread that spawned this one.
	ParentID string
	// AgentName is the agent's nickname, such as "Galileo".
	AgentName string
	// AgentRole is the role it was spawned with, such as "explorer".
	AgentRole string
}

// ParseCodexSpawn decodes a Codex session source, the JSON the state
// database's source column holds as a string and a rollout's session_meta
// holds as an object: {"subagent":{"thread_spawn":{"parent_thread_id":...,
// "agent_nickname":...,"agent_role":...}}}. Older rollouts and rows name the
// role agent_type, which Codex still accepts as an alias. Any other source,
// such as "cli" or {"subagent":"review"}, gives the zero CodexSpawn.
func ParseCodexSpawn(raw []byte) CodexSpawn {
	spawn, _ := parseCodexSpawn(raw)
	return spawn
}

// parseCodexSpawn is ParseCodexSpawn that also reports whether the source
// marks a spawned subagent: a thread_spawn, or a subagent object of a shape
// this version does not know (so a newer source still counts). Strings such
// as "review" and {"other":...} are not spawns.
func parseCodexSpawn(raw []byte) (CodexSpawn, bool) {
	var decoded struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(raw, &decoded) != nil || len(decoded.Subagent) == 0 {
		return CodexSpawn{}, false
	}
	var kinds map[string]json.RawMessage
	if json.Unmarshal(decoded.Subagent, &kinds) != nil || kinds == nil {
		return CodexSpawn{}, false
	}
	threadSpawn, ok := kinds["thread_spawn"]
	if !ok {
		_, other := kinds["other"]
		return CodexSpawn{}, !other && len(kinds) > 0
	}
	var sub struct {
		ParentThreadID string  `json:"parent_thread_id"`
		AgentNickname  *string `json:"agent_nickname"`
		AgentRole      *string `json:"agent_role"`
		// AgentType is the older name Codex still reads as agent_role.
		AgentType *string `json:"agent_type"`
	}
	if json.Unmarshal(threadSpawn, &sub) != nil {
		return CodexSpawn{}, true
	}
	spawn := CodexSpawn{ParentID: strings.TrimSpace(sub.ParentThreadID)}
	if sub.AgentNickname != nil {
		spawn.AgentName = strings.TrimSpace(*sub.AgentNickname)
	}
	if sub.AgentRole != nil {
		spawn.AgentRole = strings.TrimSpace(*sub.AgentRole)
	}
	if spawn.AgentRole == "" && sub.AgentType != nil {
		spawn.AgentRole = strings.TrimSpace(*sub.AgentType)
	}
	return spawn, true
}

// CodexSessionMeta is what a rollout's own session_meta says about the
// thread: who spawned it and which thread it was forked from.
type CodexSessionMeta struct {
	// ID is the thread id the session_meta describes.
	ID string
	// ForkedFromID is the thread this one was forked from (a user fork, or a
	// subagent spawned with its parent's context). A forked rollout starts
	// with a copy of that thread's records, token counts included.
	ForkedFromID string
	// Spawn is from source.subagent.thread_spawn, with the session_meta's own
	// parent_thread_id, agent_nickname and agent_role (or agent_type) filling
	// fields the source leaves empty, for spawned subagents only.
	Spawn CodexSpawn
}

func readCodexSessionMetaFile(path, id string) (CodexSessionMeta, bool) {
	file, err := os.Open(path)
	if err != nil {
		return CodexSessionMeta{}, false
	}
	defer file.Close()
	return ReadCodexSessionMeta(file, id)
}

// ParseCodexSessionMeta decodes a session_meta payload.
func ParseCodexSessionMeta(payload []byte) CodexSessionMeta {
	var p struct {
		ID             string          `json:"id"`
		ForkedFromID   string          `json:"forked_from_id"`
		ParentThreadID string          `json:"parent_thread_id"`
		AgentNickname  string          `json:"agent_nickname"`
		AgentRole      string          `json:"agent_role"`
		AgentType      string          `json:"agent_type"`
		Source         json.RawMessage `json:"source"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return CodexSessionMeta{}
	}
	meta := CodexSessionMeta{ID: strings.TrimSpace(p.ID), ForkedFromID: strings.TrimSpace(p.ForkedFromID)}
	spawn, spawned := parseCodexSpawn(p.Source)
	if spawned {
		spawn.ParentID = FirstNonEmpty(spawn.ParentID, strings.TrimSpace(p.ParentThreadID))
		spawn.AgentName = FirstNonEmpty(spawn.AgentName, strings.TrimSpace(p.AgentNickname))
		spawn.AgentRole = FirstNonEmpty(spawn.AgentRole, strings.TrimSpace(p.AgentRole), strings.TrimSpace(p.AgentType))
	}
	meta.Spawn = spawn
	return meta
}

// ReadCodexSessionMeta reads the session_meta on the first line of a
// rollout, where Codex writes the thread's own. A forked rollout repeats the
// source thread's session_meta lines later; those are never read here. ok is
// false when the first line is not a session_meta for id (an empty id in the
// line is accepted).
func ReadCodexSessionMeta(r io.Reader, id string) (CodexSessionMeta, bool) {
	line, err := bufio.NewReaderSize(io.LimitReader(r, RolloutMetaHeadBytes), 64<<10).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return CodexSessionMeta{}, false
	}
	var record struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(bytes.TrimSpace(line), &record) != nil || record.Type != "session_meta" {
		return CodexSessionMeta{}, false
	}
	meta := ParseCodexSessionMeta(record.Payload)
	if meta.ID != "" && !strings.EqualFold(meta.ID, id) {
		return CodexSessionMeta{}, false
	}
	return meta, true
}

// spawnSource decodes the JSON the source column holds for a spawned thread.
func (r ThreadMetadataRow) spawnSource() CodexSpawn {
	raw := r.RawSource
	if !strings.HasPrefix(raw, "{") {
		raw = r.Source
	}
	return ParseCodexSpawn([]byte(raw))
}

func (r ThreadMetadataRow) parentThreadID() string { return r.spawnSource().ParentID }
func (r ThreadMetadataRow) agentNickname() string  { return r.spawnSource().AgentName }
func (r ThreadMetadataRow) agentRole() string      { return r.spawnSource().AgentRole }
