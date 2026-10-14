package tui

import (
	"github.com/DanBradbury/firekeeper/internal/session"
)

type sessionState = session.State

const (
	sessionStateUnknown    = session.SessionStateUnknown
	sessionStateActive     = session.SessionStateActive
	sessionStateWaiting    = session.SessionStateWaiting
	sessionStateNeedsInput = session.SessionStateNeedsInput
)
