package main

import (
	"github.com/DanBradbury/firekeeper/internal/session"
)

const rolloutStatusTailBytes = session.RolloutStatusTailBytes
const rolloutMetaHeadBytes = session.RolloutMetaHeadBytes

type sessionState = session.SessionState

const (
	sessionStateUnknown    = session.SessionStateUnknown
	sessionStateActive     = session.SessionStateActive
	sessionStateWaiting    = session.SessionStateWaiting
	sessionStateNeedsInput = session.SessionStateNeedsInput
)

type rolloutSessionMetaRecord = session.RolloutSessionMetaRecord

type rolloutStatusEvent = session.RolloutStatusEvent
