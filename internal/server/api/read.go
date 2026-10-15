package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/share"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/version"
)

// Read limits. Event limits come from the v1 contract; session limits are
// this server's choice.
const (
	DefaultSessionLimit = 50
	MaxSessionLimit     = 500
	DefaultEventLimit   = 200
	MaxEventLimit       = 1000
)

var validStates = map[string]bool{
	"ACTIVE": true, "WAITING": true, "NEEDS_INPUT": true, "ENDED": true, "UNKNOWN": true,
}

// parseLimit reads a positive limit, clamping values above max.
func parseLimit(r *http.Request, def, max int) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, false
	}
	return min(n, max), true
}

func listSessions(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, ok := parseLimit(r, DefaultSessionLimit, MaxSessionLimit)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		f := store.SessionFilter{
			Machine:  q.Get("machine"),
			Provider: q.Get("provider"),
			Project:  q.Get("project"),
			State:    q.Get("state"),
			Q:        q.Get("q"),
			Limit:    limit,
			Cursor:   q.Get("cursor"),
		}
		if f.Provider != "" && !transcript.Provider(f.Provider).Valid() {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid provider")
			return
		}
		if f.State != "" && !validStates[f.State] {
			writeErr(w, http.StatusBadRequest, "bad_request", "invalid state")
			return
		}
		sessions, next, err := s.ListSessions(r.Context(), accountID(r), f)
		if err != nil {
			storeErr(w, err)
			return
		}
		if sessions == nil {
			sessions = []store.Session{}
		}
		writeJSON(w, http.StatusOK, struct {
			Sessions   []store.Session `json:"sessions"`
			NextCursor string          `json:"next_cursor,omitempty"`
		}{sessions, next})
	}
}

// sessionFromPath resolves the {uid} path value, writing an error response
// and returning false when it is malformed or unknown.
func sessionFromPath(s *store.Store, w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	m, sid, ok := store.SplitUID(r.PathValue("uid"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request", "uid must be <machine_id>:<session_id>")
		return store.Session{}, false
	}
	se, err := s.GetSession(r.Context(), accountID(r), m, sid)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "session not found")
		return se, false
	}
	if err != nil {
		storeErr(w, err)
		return se, false
	}
	return se, true
}

// sessionDetail is one session plus the files its tool calls changed.
type sessionDetail struct {
	store.Session
	Files []store.SessionFile `json:"files"`
}

func getSession(s *store.Store, linkTmpl string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		se, ok := sessionFromPath(s, w, r)
		if !ok {
			return
		}
		files, err := s.ListFiles(r.Context(), accountID(r), se.MachineID, se.SessionID)
		if err != nil {
			storeErr(w, err)
			return
		}
		for i := range files {
			files[i].URL = fileLink(linkTmpl, se, files[i])
		}
		writeJSON(w, http.StatusOK, sessionDetail{se, files})
	}
}

func listEvents(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, ok := parseLimit(r, DefaultEventLimit, MaxEventLimit)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		after := int64(-1)
		if v := r.URL.Query().Get("after_seq"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < -1 {
				writeErr(w, http.StatusBadRequest, "bad_request", "after_seq must be an integer >= -1")
				return
			}
			after = n
		}
		se, ok := sessionFromPath(s, w, r)
		if !ok {
			return
		}
		events, more, err := s.ListEvents(r.Context(), accountID(r), se.MachineID, se.SessionID, after, limit)
		if err != nil {
			storeErr(w, err)
			return
		}
		if events == nil {
			events = []transcript.Event{}
		}
		writeJSON(w, http.StatusOK, struct {
			Events  []transcript.Event `json:"events"`
			HasMore bool               `json:"has_more"`
		}{events, more})
	}
}

func listMachines(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		machines, err := s.ListMachines(r.Context(), accountID(r))
		if err != nil {
			storeErr(w, err)
			return
		}
		if machines == nil {
			machines = []store.MachineInfo{}
		}
		writeJSON(w, http.StatusOK, struct {
			Machines []store.MachineInfo `json:"machines"`
		}{machines})
	}
}

// exportSession serves GET /v1/sessions/{uid}/export: the session, its
// changed files, and every event as one standalone HTML page to share (see
// package share). Original records are left out unless raw=1.
func exportSession(s *store.Store, linkTmpl string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		withRaw := false
		if v := r.URL.Query().Get("raw"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "bad_request", "raw must be true or false")
				return
			}
			withRaw = b
		}
		se, ok := sessionFromPath(s, w, r)
		if !ok {
			return
		}
		acct := accountID(r)
		files, err := s.ListFiles(r.Context(), acct, se.MachineID, se.SessionID)
		if err != nil {
			storeErr(w, err)
			return
		}
		for i := range files {
			files[i].URL = fileLink(linkTmpl, se, files[i])
		}

		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+share.Filename(se)+`"`)
		h.Set("Cache-Control", "no-store")
		// The page is meant to be saved and opened from disk. Should a
		// browser render it here anyway, keep it off this origin.
		h.Set("Content-Security-Policy", "sandbox allow-scripts; "+share.CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		rc := http.NewResponseController(w)
		// A long session outlives any server-wide write timeout.
		_ = rc.SetWriteDeadline(time.Time{})

		pw, err := share.Begin(w, share.Doc{
			Session:     se,
			Files:       files,
			GeneratedAt: time.Now().UTC(),
			Version:     version.Version,
			Raw:         withRaw,
		})
		if err != nil {
			return
		}
		// Page through every event. An error leaves the page unclosed, which
		// the viewer reports as incomplete.
		after := int64(-1)
		for {
			events, more, err := s.ListEvents(r.Context(), acct, se.MachineID, se.SessionID, after, MaxEventLimit)
			if err != nil {
				return
			}
			for _, ev := range events {
				if err := pw.Event(ev); err != nil {
					return
				}
				after = ev.Seq
			}
			if !more || len(events) == 0 {
				break
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
		_ = pw.Close()
	}
}
