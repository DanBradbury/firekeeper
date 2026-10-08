package main

import (
	"github.com/DanBradbury/firekeeper/internal/session"
)

const (
	copilotEventTailBytes = session.CopilotEventTailBytes
	copilotLogHeadBytes   = session.CopilotLogHeadBytes
	copilotLogTailBytes   = session.CopilotLogTailBytes
)

type copilotWorkspaceMetadata = session.CopilotWorkspaceMetadata

type copilotStoredMetadata = session.CopilotStoredMetadata

type copilotEventMetadata = session.CopilotEventMetadata

type copilotEvent = session.CopilotEvent
