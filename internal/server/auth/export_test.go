package auth

import (
	"net/http"
	"time"
)

func init() {
	// Keep tests fast; production cost is set in password.go.
	hashParams.memory = 64
	hashParams.time = 1
}

// SetClock replaces the middleware's clock.
func (m *Middleware) SetClock(now func() time.Time) { m.now = now }

// ClientIP exposes the limiter key derived for r.
func (m *Middleware) ClientIP(r *http.Request) string { return m.clientIP(r) }
