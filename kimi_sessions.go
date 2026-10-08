package main

import (
	"github.com/DanBradbury/firekeeper/internal/session"
)

type kimiSessionState = session.KimiSessionState

type kimiLatestSession struct {
	kimiSessionState
	Model string
	path  string
}
