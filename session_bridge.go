// These adapters preserve the dashboard's private types and parser helpers
// while all discovery implementations live in internal/session.
package main

import (
	"bufio"
	"github.com/DanBradbury/firekeeper/internal/session"
	"io"
	"time"
)

func enrichCopilotSessions(groups []processGroup) error {
	g := toSliceprocessGroup(groups)
	err := session.EnrichCopilotSessions(g)
	for i := range groups {
		groups[i] = fromProcessGroup(g[i])
	}
	return err
}
func copilotHome() (string, error) {
	r0, r1 := session.CopilotHome()
	return r0, r1
}
func discoverOpenCopilotSessions(pids []int) (map[int][]string, error) {
	r0, r1 := session.DiscoverOpenCopilotSessions(pids)
	return r0, r1
}
func copilotSessionIDFromStatePath(path string) (string, bool) {
	r0, r1 := session.CopilotSessionIDFromStatePath(path)
	return r0, r1
}
func copilotSessionIDFromGroupCommands(group processGroup) (string, bool) {
	r0, r1 := session.CopilotSessionIDFromGroupCommands(toProcessGroup(group))
	return r0, r1
}
func copilotSessionIDFromCommand(command string) (string, bool) {
	r0, r1 := session.CopilotSessionIDFromCommand(command)
	return r0, r1
}
func copilotSessionIDFromGroupLogs(home string, group processGroup) (string, bool) {
	r0, r1 := session.CopilotSessionIDFromGroupLogs(home, toProcessGroup(group))
	return r0, r1
}
func readCopilotSessionIDFromLog(path string) (string, error) {
	r0, r1 := session.ReadCopilotSessionIDFromLog(path)
	return r0, r1
}
func scanCopilotLogSessionID(reader io.Reader) string {
	r0 := session.ScanCopilotLogSessionID(reader)
	return r0
}
func lastUUIDInString(value string) (string, bool) {
	r0, r1 := session.LastUUIDInString(value)
	return r0, r1
}
func newestCopilotSession(home string, ids []string) string {
	r0 := session.NewestCopilotSession(home, ids)
	return r0
}
func readCopilotWorkspace(path string) (copilotWorkspaceMetadata, error) {
	r0, r1 := session.ReadCopilotWorkspace(path)
	return r0, r1
}
func unquoteCopilotYAMLValue(value string) string {
	r0 := session.UnquoteCopilotYAMLValue(value)
	return r0
}
func readCopilotStoredMetadata(home string, ids map[string]bool) (map[string]copilotStoredMetadata, error) {
	r0, r1 := session.ReadCopilotStoredMetadata(home, ids)
	return r0, r1
}
func loadCopilotSession(home string, id string, stored copilotStoredMetadata) (sessionInfo, error) {
	r0, r1 := session.LoadCopilotSession(home, id, stored)
	return fromSessionInfo(r0), r1
}
func readCopilotEventMetadata(path string) (copilotEventMetadata, error) {
	r0, r1 := session.ReadCopilotEventMetadata(path)
	return r0, r1
}
func scanCopilotEvents(reader io.Reader) (copilotEventMetadata, error) {
	r0, r1 := session.ScanCopilotEvents(reader)
	return r0, r1
}
func parseCopilotTime(value string) time.Time {
	r0 := session.ParseCopilotTime(value)
	return r0
}
func firstNonEmpty(values ...string) string {
	r0 := session.FirstNonEmpty(values...)
	return r0
}
func newestTime(values ...time.Time) time.Time {
	r0 := session.NewestTime(values...)
	return r0
}
func enrichKimiSessions(groups []processGroup) error {
	g := toSliceprocessGroup(groups)
	err := session.EnrichKimiSessions(g)
	for i := range groups {
		groups[i] = fromProcessGroup(g[i])
	}
	return err
}
func latestKimiSessions(root string) (map[string]kimiLatestSession, error) {
	r0, r1 := session.LatestKimiSessions(root)
	return fromMapmapstringkimiLatestSession(r0), r1
}
func kimiHome() (string, error) {
	r0, r1 := session.KimiHome()
	return r0, r1
}
func discoverProcesses() ([]byte, error) {
	r0, r1 := session.DiscoverProcesses()
	return r0, r1
}
func groupProcesses(processes []processInfo) []processGroup {
	r0 := session.GroupProcesses(toSliceprocessInfo(processes))
	return fromSliceprocessGroup(r0)
}
func enrichCodexSessions(groups []processGroup) error {
	g := toSliceprocessGroup(groups)
	err := session.EnrichCodexSessions(g)
	for i := range groups {
		groups[i] = fromProcessGroup(g[i])
	}
	return err
}
func discoverOpenRollouts(rootPIDs []int) (map[int][]string, error) {
	r0, r1 := session.DiscoverOpenRollouts(rootPIDs)
	return r0, r1
}
func dropSupersededForkRollouts(paths []string) []string {
	r0 := session.DropSupersededForkRollouts(paths)
	return r0
}
func threadIDFromRolloutPath(path string) (string, bool) {
	r0, r1 := session.ThreadIDFromRolloutPath(path)
	return r0, r1
}
func validUUID(value string) bool {
	r0 := session.ValidUUID(value)
	return r0
}
func readThreadMetadata(threadIDs map[string]bool) (map[string]threadMetadataRow, error) {
	r0, r1 := session.ReadThreadMetadata(threadIDs)
	return r0, r1
}
func codexStatePath() (string, error) {
	r0, r1 := session.CodexStatePath()
	return r0, r1
}
func emptyFallback(value string, fallback string) string {
	r0 := session.EmptyFallback(value, fallback)
	return r0
}
func parseProcesses(output string) []processInfo {
	r0 := session.ParseProcesses(output)
	return fromSliceprocessInfo(r0)
}
func normalizeTTY(value string) string {
	r0 := session.NormalizeTTY(value)
	return r0
}
func classifyProcess(command string) string {
	r0 := session.ClassifyProcess(command)
	return r0
}
func sanitizeProcessCommand(command string) string {
	r0 := session.SanitizeProcessCommand(command)
	return r0
}
func readRolloutParentThreadID(path string) string {
	r0 := session.ReadRolloutParentThreadID(path)
	return r0
}
func readRolloutSessionState(path string) (sessionState, error) {
	r0, r1 := session.ReadRolloutSessionState(path)
	return sessionState(r0), r1
}
func scanRolloutSessionState(reader *bufio.Reader) (sessionState, error) {
	r0, r1 := session.ScanRolloutSessionState(reader)
	return sessionState(r0), r1
}
func toProcessInfo(v processInfo) session.ProcessInfo {
	return session.ProcessInfo{Tool: v.tool, PID: v.pid, PPID: v.ppid, TTY: v.tty, Elapsed: v.elapsed, Command: v.command}
}
func fromProcessInfo(v session.ProcessInfo) processInfo {
	return processInfo{tool: v.Tool, pid: v.PID, ppid: v.PPID, tty: v.TTY, elapsed: v.Elapsed, command: v.Command}
}
func toSessionInfo(v sessionInfo) session.SessionInfo {
	return session.SessionInfo{ID: v.id, Name: v.name, State: session.SessionState(v.state), CWD: v.cwd, Model: v.model, Source: v.source, Repository: v.repository, GitBranch: v.gitBranch, RolloutPath: v.rolloutPath, UpdatedAt: v.updatedAt, TokensUsed: v.tokensUsed}
}
func fromSessionInfo(v session.SessionInfo) sessionInfo {
	return sessionInfo{id: v.ID, name: v.Name, state: sessionState(v.State), cwd: v.CWD, model: v.Model, source: v.Source, repository: v.Repository, gitBranch: v.GitBranch, rolloutPath: v.RolloutPath, updatedAt: v.UpdatedAt, tokensUsed: v.TokensUsed}
}
func toProcessGroup(v processGroup) session.ProcessGroup {
	return session.ProcessGroup{Tool: v.tool, Root: toProcessInfo(v.root), Processes: toSliceprocessInfo(v.processes), Sessions: toSlicesessionInfo(v.sessions)}
}
func fromProcessGroup(v session.ProcessGroup) processGroup {
	return processGroup{tool: v.Tool, root: fromProcessInfo(v.Root), processes: fromSliceprocessInfo(v.Processes), sessions: fromSlicesessionInfo(v.Sessions)}
}
func toKimiLatestSession(v kimiLatestSession) session.KimiLatestSession {
	return session.KimiLatestSession{KimiSessionState: v.kimiSessionState, Model: v.Model, Path: v.path}
}
func fromKimiLatestSession(v session.KimiLatestSession) kimiLatestSession {
	return kimiLatestSession{kimiSessionState: v.KimiSessionState, Model: v.Model, path: v.Path}
}
func fromSlicesessionInfo(v []session.SessionInfo) []sessionInfo {
	if v == nil {
		return nil
	}
	out := make([]sessionInfo, len(v))
	for i, x := range v {
		out[i] = fromSessionInfo(x)
	}
	return out
}
func toSlicesessionInfo(v []sessionInfo) []session.SessionInfo {
	if v == nil {
		return nil
	}
	out := make([]session.SessionInfo, len(v))
	for i, x := range v {
		out[i] = toSessionInfo(x)
	}
	return out
}
func fromSliceprocessInfo(v []session.ProcessInfo) []processInfo {
	if v == nil {
		return nil
	}
	out := make([]processInfo, len(v))
	for i, x := range v {
		out[i] = fromProcessInfo(x)
	}
	return out
}
func fromSliceprocessGroup(v []session.ProcessGroup) []processGroup {
	if v == nil {
		return nil
	}
	out := make([]processGroup, len(v))
	for i, x := range v {
		out[i] = fromProcessGroup(x)
	}
	return out
}
func toSliceprocessInfo(v []processInfo) []session.ProcessInfo {
	if v == nil {
		return nil
	}
	out := make([]session.ProcessInfo, len(v))
	for i, x := range v {
		out[i] = toProcessInfo(x)
	}
	return out
}
func fromMapmapstringkimiLatestSession(v map[string]session.KimiLatestSession) map[string]kimiLatestSession {
	if v == nil {
		return nil
	}
	out := make(map[string]kimiLatestSession, len(v))
	for i, x := range v {
		out[i] = fromKimiLatestSession(x)
	}
	return out
}
func toSliceprocessGroup(v []processGroup) []session.ProcessGroup {
	if v == nil {
		return nil
	}
	out := make([]session.ProcessGroup, len(v))
	for i, x := range v {
		out[i] = toProcessGroup(x)
	}
	return out
}
