package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// keepaliveInterval is how often idle streams get a comment line. Tests
// shorten it.
var keepaliveInterval = 15 * time.Second

// clientBuffer is how many notifications a stream client may lag behind
// before the hub drops its connection.
const clientBuffer = 256

// streamEvent is the data payload of one server-sent event. Clients refetch
// details from the read endpoints.
type streamEvent struct {
	UID       string `json:"uid,omitempty"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id,omitempty"`
	Seq       *int64 `json:"seq,omitempty"`
}

// hub fans store change notifications out to stream clients. A client whose
// buffer is full is dropped so a slow reader never blocks ingest.
type hub struct {
	mu      sync.Mutex
	clients map[chan store.Change]struct{}
	done    bool
}

func newHub() *hub {
	return &hub{clients: make(map[chan store.Change]struct{})}
}

// run forwards changes until the store closes the channel, then ends every
// client stream.
func (h *hub) run(changes <-chan store.Change) {
	for c := range changes {
		h.broadcast(c)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.done = true
	for ch := range h.clients {
		close(ch)
		delete(h.clients, ch)
	}
}

func (h *hub) broadcast(c store.Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- c:
		default:
			close(ch)
			delete(h.clients, ch)
		}
	}
}

// subscribe registers a client. It returns nil when the hub has stopped.
func (h *hub) subscribe() chan store.Change {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		return nil
	}
	ch := make(chan store.Change, clientBuffer)
	h.clients[ch] = struct{}{}
	return ch
}

func (h *hub) unsubscribe(ch chan store.Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		close(ch)
		delete(h.clients, ch)
	}
}

func stream(h *hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		ch := h.subscribe()
		if ch == nil {
			writeErr(w, http.StatusServiceUnavailable, "unavailable", "server is shutting down")
			return
		}
		defer h.unsubscribe(ch)
		// Streams outlive any server-wide write timeout.
		_ = rc.SetWriteDeadline(time.Time{})

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil || rc.Flush() != nil {
			return
		}

		ticker := time.NewTicker(keepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil || rc.Flush() != nil {
					return
				}
			case c, ok := <-ch:
				if !ok {
					// Dropped as too slow, or the server is shutting down.
					return
				}
				if writeChange(w, c) != nil || rc.Flush() != nil {
					return
				}
			}
		}
	}
}

func writeChange(w http.ResponseWriter, c store.Change) error {
	ev := streamEvent{MachineID: c.MachineID, SessionID: c.SessionID}
	if c.SessionID != "" {
		ev.UID = store.UID(c.MachineID, c.SessionID)
	}
	if c.Kind == store.EventAppended {
		seq := c.Seq
		ev.Seq = &seq
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", c.Kind, data)
	return err
}
