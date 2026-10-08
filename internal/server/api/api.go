// Package api serves the dashboard HTTP API.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// Limits from the v1 contract.
const (
	MaxIngestEvents = 500
	MaxBodyBytes    = 5 << 20
)

// Option configures Handler.
type Option func(*options)

type options struct {
	prices   map[string]store.Price
	fileLink string
}

// WithPrices sets the per-model price table /v1/usage uses to add cost.
// With no table, usage reports tokens only; prices are never built in.
func WithPrices(p map[string]store.Price) Option {
	return func(o *options) { o.prices = p }
}

// WithFileLink sets the template that links a session's changed files to
// their repository host. See ValidateFileLink. With none, files carry no URL.
func WithFileLink(tmpl string) Option {
	return func(o *options) { o.fileLink = tmpl }
}

// Handler routes the v1 API. It consumes s.Changes() to drive /v1/stream,
// so create at most one Handler per Store. Streams end when s is closed.
func Handler(s *store.Store, opts ...Option) http.Handler {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	h := newHub()
	go h.run(s.Changes())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", ingest(s))
	mux.HandleFunc("POST /v1/heartbeat", heartbeat(s))
	mux.HandleFunc("GET /v1/sessions", listSessions(s))
	mux.HandleFunc("GET /v1/sessions/{uid}", getSession(s, o.fileLink))
	mux.HandleFunc("GET /v1/sessions/{uid}/events", listEvents(s))
	mux.HandleFunc("GET /v1/machines", listMachines(s))
	mux.HandleFunc("GET /v1/usage", usage(s, o.prices))
	mux.HandleFunc("GET /v1/account", account(s))
	mux.HandleFunc("POST /v1/auth/logout", logout(s))
	mux.HandleFunc("GET /v1/stream", stream(s, h))
	return mux
}

type ingestRequest struct {
	Machine  store.Machine        `json:"machine"`
	Sessions []store.SessionBatch `json:"sessions"`
}

type heartbeatRequest struct {
	Machine  store.Machine     `json:"machine"`
	Sessions []store.Heartbeat `json:"sessions"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 5 MiB")
		} else {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		}
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 5 MiB")
		} else {
			writeErr(w, http.StatusBadRequest, "bad_request", "unexpected data after JSON body")
		}
		return false
	}
	return true
}

// accountID is the tenant a request acts for. The auth middleware always
// attaches one. Without it (a Handler mounted bare, as in single-user tests)
// requests act as the default account.
func accountID(r *http.Request) string {
	if p, ok := auth.From(r.Context()); ok {
		return p.AccountID
	}
	return store.DefaultAccountID
}

// machineAllowed rejects a request whose machine differs from the one the
// caller's ingest token is bound to.
func machineAllowed(w http.ResponseWriter, r *http.Request, machineID string) bool {
	if p, ok := auth.From(r.Context()); ok && p.MachineID != "" && p.MachineID != machineID {
		writeErr(w, http.StatusForbidden, "forbidden", "token is bound to a different machine")
		return false
	}
	return true
}

func storeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalid) {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal", "internal error")
}

func ingest(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ingestRequest
		if !decode(w, r, &req) || !machineAllowed(w, r, req.Machine.ID) {
			return
		}
		n := 0
		for _, b := range req.Sessions {
			n += len(b.Events)
		}
		if n > MaxIngestEvents {
			writeErr(w, http.StatusRequestEntityTooLarge, "too_many_events", "at most 500 events per request")
			return
		}
		accepted, dups, err := s.Ingest(r.Context(), accountID(r), req.Machine, req.Sessions)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"accepted": accepted, "duplicates": dups})
	}
}

func heartbeat(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req heartbeatRequest
		if !decode(w, r, &req) || !machineAllowed(w, r, req.Machine.ID) {
			return
		}
		if err := s.Heartbeat(r.Context(), accountID(r), req.Machine, req.Sessions); err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
