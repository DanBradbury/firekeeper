package reporter

import (
	"strings"

	"github.com/firekeeper-ai/firekeeper/internal/transcript"
)

// sessionKey identifies one session in the reporter's maps: its provider
// and its session id.
type sessionKey struct {
	provider transcript.Provider
	id       string
}

// matchKey is the key for finding the same session from two sources, such
// as discovery and a transcript enumeration, which may spell its id in a
// different case. Keys that must keep the id exactly use sessionKey
// directly.
func matchKey(provider transcript.Provider, id string) sessionKey {
	return sessionKey{provider: provider, id: strings.ToLower(id)}
}
