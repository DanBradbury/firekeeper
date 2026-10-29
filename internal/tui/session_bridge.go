// The dashboard keeps its own private process and session types. These
// helpers convert from internal/session's types and expose the few session
// helpers the dashboard calls; all discovery logic lives in internal/session.

package tui

import (
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
)

func copilotHome() (string, error)                 { return session.CopilotHome() }
func parseCopilotTime(value string) time.Time      { return session.ParseCopilotTime(value) }
func firstNonEmpty(values ...string) string        { return session.FirstNonEmpty(values...) }
func kimiHome() (string, error)                    { return session.KimiHome() }
func claudeConfigDir() (string, error)             { return session.ClaudeConfigDir() }
func codexStatePath() (string, error)              { return session.CodexStatePath() }
func emptyFallback(value, fallback string) string  { return session.EmptyFallback(value, fallback) }
func normalizeTTY(value string) string             { return session.NormalizeTTY(value) }
func sanitizeProcessCommand(command string) string { return session.SanitizeProcessCommand(command) }

func fromProcessInfo(v session.ProcessInfo) processInfo {
	return processInfo{tool: v.Tool, pid: v.PID, ppid: v.PPID, tty: v.TTY, elapsed: v.Elapsed, command: v.Command}
}

func fromSessionInfo(v session.Info) sessionInfo {
	return sessionInfo{id: v.ID, name: v.Name, state: sessionState(v.State), cwd: v.CWD, model: v.Model, source: v.Source, repository: v.Repository, gitBranch: v.GitBranch, rolloutPath: v.RolloutPath, updatedAt: v.UpdatedAt, tokensUsed: v.TokensUsed, tokens: v.Tokens, agentSummary: v.AgentSummary}
}

func fromProcessGroup(v session.ProcessGroup) processGroup {
	return processGroup{tool: v.Tool, root: fromProcessInfo(v.Root), processes: fromSliceprocessInfo(v.Processes), sessions: fromSlicesessionInfo(v.Sessions)}
}

func fromSlicesessionInfo(v []session.Info) []sessionInfo {
	if v == nil {
		return nil
	}
	out := make([]sessionInfo, len(v))
	for i, x := range v {
		out[i] = fromSessionInfo(x)
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
