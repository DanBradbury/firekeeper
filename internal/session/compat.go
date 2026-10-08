// Compatibility helpers keep existing parser and usage callers on the same
// implementation as Discover. New discovery callers should use Discover.
package session

import (
	"bufio"
	"context"
	"io"
	"time"
)

func EnrichCopilotSessions(groups []ProcessGroup) error {
	return (&discoverer{ctx: context.Background()}).enrichCopilotSessions(groups)
}
func CopilotHome() (string, error) { return (&discoverer{ctx: context.Background()}).copilotHome() }
func DiscoverOpenCopilotSessions(pids []int) (map[int][]string, error) {
	return (&discoverer{ctx: context.Background()}).discoverOpenCopilotSessions(pids)
}
func CopilotSessionIDFromStatePath(path string) (string, bool) {
	return (&discoverer{ctx: context.Background()}).copilotSessionIDFromStatePath(path)
}
func CopilotSessionIDFromGroupCommands(group ProcessGroup) (string, bool) {
	return (&discoverer{ctx: context.Background()}).copilotSessionIDFromGroupCommands(group)
}
func CopilotSessionIDFromCommand(command string) (string, bool) {
	return (&discoverer{ctx: context.Background()}).copilotSessionIDFromCommand(command)
}
func CopilotSessionIDFromGroupLogs(home string, group ProcessGroup) (string, bool) {
	return (&discoverer{ctx: context.Background()}).copilotSessionIDFromGroupLogs(home, group)
}
func ReadCopilotSessionIDFromLog(path string) (string, error) {
	return (&discoverer{ctx: context.Background()}).readCopilotSessionIDFromLog(path)
}
func ScanCopilotLogSessionID(reader io.Reader) string {
	return (&discoverer{ctx: context.Background()}).scanCopilotLogSessionID(reader)
}
func LastUUIDInString(value string) (string, bool) {
	return (&discoverer{ctx: context.Background()}).lastUUIDInString(value)
}
func NewestCopilotSession(home string, ids []string) string {
	return (&discoverer{ctx: context.Background()}).newestCopilotSession(home, ids)
}
func ReadCopilotWorkspace(path string) (CopilotWorkspaceMetadata, error) {
	return (&discoverer{ctx: context.Background()}).readCopilotWorkspace(path)
}
func UnquoteCopilotYAMLValue(value string) string {
	return (&discoverer{ctx: context.Background()}).unquoteCopilotYAMLValue(value)
}
func ReadCopilotStoredMetadata(home string, ids map[string]bool) (map[string]CopilotStoredMetadata, error) {
	return (&discoverer{ctx: context.Background()}).readCopilotStoredMetadata(home, ids)
}
func LoadCopilotSession(home string, id string, stored CopilotStoredMetadata) (SessionInfo, error) {
	return (&discoverer{ctx: context.Background()}).loadCopilotSession(home, id, stored)
}
func ReadCopilotEventMetadata(path string) (CopilotEventMetadata, error) {
	return (&discoverer{ctx: context.Background()}).readCopilotEventMetadata(path)
}
func ScanCopilotEvents(reader io.Reader) (CopilotEventMetadata, error) {
	return (&discoverer{ctx: context.Background()}).scanCopilotEvents(reader)
}
func ParseCopilotTime(value string) time.Time {
	return (&discoverer{ctx: context.Background()}).parseCopilotTime(value)
}
func FirstNonEmpty(values ...string) string {
	return (&discoverer{ctx: context.Background()}).firstNonEmpty(values...)
}
func NewestTime(values ...time.Time) time.Time {
	return (&discoverer{ctx: context.Background()}).newestTime(values...)
}
func EnrichKimiSessions(groups []ProcessGroup) error {
	return (&discoverer{ctx: context.Background()}).enrichKimiSessions(groups)
}
func LatestKimiSessions(root string) (map[string]KimiLatestSession, error) {
	return (&discoverer{ctx: context.Background()}).latestKimiSessions(root)
}
func KimiHome() (string, error) { return (&discoverer{ctx: context.Background()}).kimiHome() }
func DiscoverProcesses() ([]byte, error) {
	return (&discoverer{ctx: context.Background()}).discoverProcesses()
}
func GroupProcesses(processes []ProcessInfo) []ProcessGroup {
	return (&discoverer{ctx: context.Background()}).groupProcesses(processes)
}
func EnrichCodexSessions(groups []ProcessGroup) error {
	return (&discoverer{ctx: context.Background()}).enrichCodexSessions(groups)
}
func DiscoverOpenRollouts(rootPIDs []int) (map[int][]string, error) {
	return (&discoverer{ctx: context.Background()}).discoverOpenRollouts(rootPIDs)
}
func DropSupersededForkRollouts(paths []string) []string {
	return (&discoverer{ctx: context.Background()}).dropSupersededForkRollouts(paths)
}
func ThreadIDFromRolloutPath(path string) (string, bool) {
	return (&discoverer{ctx: context.Background()}).threadIDFromRolloutPath(path)
}
func ValidUUID(value string) bool { return (&discoverer{ctx: context.Background()}).validUUID(value) }
func ReadThreadMetadata(threadIDs map[string]bool) (map[string]ThreadMetadataRow, error) {
	return (&discoverer{ctx: context.Background()}).readThreadMetadata(threadIDs)
}
func CodexStatePath() (string, error) {
	return (&discoverer{ctx: context.Background()}).codexStatePath()
}
func EmptyFallback(value string, fallback string) string {
	return (&discoverer{ctx: context.Background()}).emptyFallback(value, fallback)
}
func ParseProcesses(output string) []ProcessInfo {
	return (&discoverer{ctx: context.Background()}).parseProcesses(output)
}
func NormalizeTTY(value string) string {
	return (&discoverer{ctx: context.Background()}).normalizeTTY(value)
}
func ClassifyProcess(command string) string {
	return (&discoverer{ctx: context.Background()}).classifyProcess(command)
}
func SanitizeProcessCommand(command string) string {
	return (&discoverer{ctx: context.Background()}).sanitizeProcessCommand(command)
}
func ReadRolloutParentThreadID(path string) string {
	return (&discoverer{ctx: context.Background()}).readRolloutParentThreadID(path)
}
func ReadRolloutSessionState(path string) (SessionState, error) {
	return (&discoverer{ctx: context.Background()}).readRolloutSessionState(path)
}
func ScanRolloutSessionState(reader *bufio.Reader) (SessionState, error) {
	return (&discoverer{ctx: context.Background()}).scanRolloutSessionState(reader)
}
