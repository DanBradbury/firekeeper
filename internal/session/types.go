package session

import (
	"time"
)

const (
	CopilotEventTailBytes = int64(4 << 20)
	CopilotLogHeadBytes   = int64(256 << 10)
	CopilotLogTailBytes   = int64(4 << 20)
)

type CopilotWorkspaceMetadata struct {
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

type CopilotStoredMetadata struct {
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

type CopilotEventMetadata struct {
	State      State
	Model      string
	UpdatedAt  time.Time
	TokensUsed int64
	Tokens     TokenBreakdown
}

type CopilotEvent struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Data      struct {
		RequestID          string `json:"requestId"`
		Model              string `json:"model"`
		NewModel           string `json:"newModel"`
		CurrentModel       string `json:"currentModel"`
		OutputTokens       int64  `json:"outputTokens"`
		ConversationTokens int64  `json:"conversationTokens"`
		ResponseChunk      struct {
			Usage *struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
				Details    *struct {
					Cached int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		} `json:"responseChunk"`
	} `json:"data"`
}

type KimiSessionState struct {
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Title     string    `json:"title"`
	WorkDir   string    `json:"workDir"`
}

type KimiLatestSession struct {
	KimiSessionState
	Model string
	Path  string
}

type ProcessInfo struct {
	Tool    string
	PID     int
	PPID    int
	TTY     string
	Elapsed string
	Command string
}

type Info struct {
	ID          string
	Name        string
	State       State
	CWD         string
	Model       string
	Source      string
	Repository  string
	GitBranch   string
	RolloutPath string
	StartedAt   time.Time
	UpdatedAt   time.Time
	TokensUsed  int64
	// Tokens breaks usage down where the provider records it. It follows the
	// transcript event convention: Input excludes cached input, and cache
	// reads and cache writes are counted apart. It is zero when unknown.
	Tokens TokenBreakdown
	// UndercountWarning is set for a Claude Code session that has subagent
	// transcripts. It is not included in Tokens or TokensUsed.
	UndercountWarning string
}

// TokenBreakdown splits a session's token usage into uncached input, output,
// cache reads, and cache writes. Providers that report only cached input
// count it as cache reads and leave CacheWrite zero.
type TokenBreakdown struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// Cache is the sum of cache reads and cache writes.
func (t TokenBreakdown) Cache() int64 { return t.CacheRead + t.CacheWrite }

// Total is the sum of every counted token.
func (t TokenBreakdown) Total() int64 { return t.Input + t.Output + t.Cache() }

func (t TokenBreakdown) add(other TokenBreakdown) TokenBreakdown {
	return TokenBreakdown{
		Input:      t.Input + other.Input,
		Output:     t.Output + other.Output,
		CacheRead:  t.CacheRead + other.CacheRead,
		CacheWrite: t.CacheWrite + other.CacheWrite,
	}
}

type ProcessGroup struct {
	Tool      string
	Root      ProcessInfo
	Processes []ProcessInfo
	Sessions  []Info
}

type ThreadMetadataRow struct {
	ID          string `json:"id"`
	Name        string `json:"display_name"`
	CWD         string `json:"cwd"`
	Model       string `json:"model"`
	Source      string `json:"source"`
	GitBranch   string `json:"git_branch"`
	UpdatedAtMS int64  `json:"updated_at_ms"`
	TokensUsed  int64  `json:"tokens_used"`
	RolloutPath string `json:"rollout_path"`
}

const RolloutStatusTailBytes int64 = 4 << 20

const RolloutMetaHeadBytes int64 = 8 << 20

type State int

const (
	SessionStateUnknown State = iota
	SessionStateActive
	SessionStateWaiting
	SessionStateNeedsInput
	SessionStateEnded
)

type RolloutSessionMetaRecord struct {
	Type    string `json:"type"`
	Payload struct {
		ParentThreadID string `json:"parent_thread_id"`
	} `json:"payload"`
}

type RolloutStatusEvent struct {
	Type    string `json:"type"`
	Payload struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		CallID string `json:"call_id"`
		Info   *struct {
			Total *struct {
				Input  int64 `json:"input_tokens"`
				Cached int64 `json:"cached_input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}
