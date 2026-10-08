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
	State      SessionState
	Model      string
	UpdatedAt  time.Time
	TokensUsed int64
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

type SessionInfo struct {
	ID          string
	Name        string
	State       SessionState
	CWD         string
	Model       string
	Source      string
	Repository  string
	GitBranch   string
	RolloutPath string
	StartedAt   time.Time
	UpdatedAt   time.Time
	TokensUsed  int64
}

type ProcessGroup struct {
	Tool      string
	Root      ProcessInfo
	Processes []ProcessInfo
	Sessions  []SessionInfo
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
}

const RolloutStatusTailBytes int64 = 4 << 20

const RolloutMetaHeadBytes int64 = 8 << 20

type SessionState int

const (
	SessionStateUnknown SessionState = iota
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
	} `json:"payload"`
}
