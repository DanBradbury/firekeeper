// Package fakeapi is an in-memory stand-in for a Firekeeper server, for tests
// of the reporter, daemon and login commands. It implements the client-facing
// part of docs/api.md: POST /v1/ingest, POST /v1/heartbeat, the device-code
// link flow, GET /v1/account and DELETE /v1/tokens/current. It also answers
// the two session reads the tests use to check what was uploaded. It stores
// nothing on disk and is not a server: it has no accounts, limits or web UI.
//
// It follows the golden fixtures of pkg/contract: every error body is the
// fixture for its code, every ingest body must pass contract.ValidateBatch
// (so a reporter that produces an event the schema rejects fails its test),
// and TestResponsesMatchFixtures checks each success body against the
// fixture's shape.
package fakeapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DanBradbury/firekeeper/pkg/contract"
	"github.com/DanBradbury/firekeeper/pkg/event"
)

// Limits from docs/api.md.
const (
	MaxBodyBytes    = contract.MaxBodyBytes
	MaxEventsPerReq = contract.MaxEventsPerReq
)

var validStates = map[string]bool{"ACTIVE": true, "WAITING": true, "NEEDS_INPUT": true, "ENDED": true, "UNKNOWN": true}

// Session is the stored metadata of one uploaded session.
type Session struct {
	MachineID, ID, Provider, CWD, Project, Branch, Commit, Model, State, Title string
	// Origin is where the session ran, "local" or "cloud" (contract.Origin).
	// A new session is "local" until a batch names another origin.
	FilesSource, Origin       string
	StartedAt, LastActivityAt *time.Time
}

// File is one changed file of a session.
type File struct {
	Path    string
	Changes int
}

// Machine is a machine that has uploaded or sent a heartbeat.
type Machine struct {
	ID, Name, Hostname, OS, Version string
	LastHeartbeatAt                 *time.Time
}

type stored struct {
	meta   Session
	events map[int64]event.Event
	tools  map[string]int // changes per path from tool calls
	git    []string
	hasGit bool
}

type link struct {
	machineID, userCode string
	approved            bool
	name                string
	email               string
	collected           bool
	expires             time.Time
}

type token struct {
	machineID, name, email string
	revoked                bool
}

// Server holds everything uploaded to it.
type Server struct {
	// RequireToken, when true, makes ingest and heartbeat need a token
	// issued by the link flow, and only for that token's machine.
	RequireToken bool
	// NoAccounts makes POST /v1/link/start answer 409 accounts_required.
	NoAccounts bool

	mu       sync.Mutex
	sessions map[string]*stored
	machines map[string]*Machine
	links    map[string]*link  // by device code
	tokens   map[string]*token // by secret
	skew     time.Duration     // added to the clock by Advance
}

// New returns an empty server.
func New() *Server {
	return &Server{
		sessions: map[string]*stored{}, machines: map[string]*Machine{},
		links: map[string]*link{}, tokens: map[string]*token{},
	}
}

func key(machine, session string) string { return machine + "\x00" + session }

// Advance moves the server's clock forward, so a test can let a link code
// expire without waiting.
func (s *Server) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skew += d
}

// nowLocked is the server's clock. The caller holds s.mu.
func (s *Server) nowLocked() time.Time { return time.Now().Add(s.skew) }

// Approve marks the link with userCode as approved by the account email,
// as the dashboard's approval page would, and names the machine.
func (s *Server) Approve(userCode, machineName, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := strings.ToUpper(strings.ReplaceAll(userCode, "-", ""))
	for _, l := range s.links {
		if strings.ReplaceAll(l.userCode, "-", "") == code && !l.approved {
			l.approved, l.name, l.email = true, machineName, email
			return nil
		}
	}
	return fmt.Errorf("no pending link for %q", userCode)
}

// RevokeAll revokes every token, as an owner would from a tokens page.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens {
		t.revoked = true
	}
}

// Events returns a session's stored events in seq order.
func (s *Server) Events(machine, session string) []event.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventsLocked(machine, session)
}

func (s *Server) eventsLocked(machine, session string) []event.Event {
	st := s.sessions[key(machine, session)]
	if st == nil {
		return nil
	}
	out := make([]event.Event, 0, len(st.events))
	for _, e := range st.events {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Session returns a session's stored metadata.
func (s *Server) Session(machine, session string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.sessions[key(machine, session)]
	if st == nil {
		return Session{}, false
	}
	return st.meta, true
}

// Files returns a session's changed files ordered by path.
func (s *Server) Files(machine, session string) []File {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.sessions[key(machine, session)]
	if st == nil {
		return nil
	}
	var out []File
	if st.hasGit {
		for _, p := range st.git {
			out = append(out, File{Path: p, Changes: st.tools[p]})
		}
	} else {
		for p, n := range st.tools {
			out = append(out, File{Path: p, Changes: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Machines returns the machines seen, ordered by id.
func (s *Server) Machines() []Machine {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Machine
	for _, m := range s.machines {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", s.ingest)
	mux.HandleFunc("POST /v1/heartbeat", s.heartbeat)
	mux.HandleFunc("POST /v1/link/start", s.linkStart)
	mux.HandleFunc("POST /v1/link/poll", s.linkPoll)
	mux.HandleFunc("GET /v1/account", s.account)
	mux.HandleFunc("DELETE /v1/tokens/current", s.revokeCurrent)
	mux.HandleFunc("GET /v1/sessions/{uid}", s.getSession)
	mux.HandleFunc("GET /v1/sessions/{uid}/events", s.getEvents)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail answers with the golden error envelope for code. The message argument
// documents the call site only: clients see the fixture's text. The two
// test-only session reads use codes docs/api.md does not define, and for
// those the message is sent as given.
func fail(w http.ResponseWriter, status int, code, msg string) {
	body := contract.Fixture("error." + code)
	if body == nil {
		writeJSON(w, status, map[string]string{"error": msg, "code": code})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Fakeapi-Detail", strings.ReplaceAll(msg, "\n", " "))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func random(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// auth checks the bearer token when RequireToken is set. It returns the
// token's machine id ("" when any machine may write).
func (s *Server) auth(w http.ResponseWriter, r *http.Request) (machineID string, ok bool) {
	if !s.RequireToken {
		return "", true
	}
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	t := s.tokens[secret]
	s.mu.Unlock()
	if secret == "" || t == nil || t.revoked {
		fail(w, http.StatusUnauthorized, "unauthorized", "missing, malformed, expired or revoked credentials")
		return "", false
	}
	return t.machineID, true
}

type machineBody struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Version  string `json:"version"`
}

type metaBody struct {
	SessionID      string     `json:"session_id"`
	Provider       string     `json:"provider"`
	CWD            string     `json:"cwd"`
	Project        string     `json:"project"`
	Branch         string     `json:"branch"`
	Commit         string     `json:"commit"`
	Model          string     `json:"model"`
	State          string     `json:"state"`
	Title          string     `json:"title"`
	StartedAt      *time.Time `json:"started_at"`
	LastActivityAt *time.Time `json:"last_activity_at"`
	FilesSource    string     `json:"files_source"`
	Origin         string     `json:"origin"`
	Files          []string   `json:"files"`
}

func (s *Server) touchMachine(m machineBody) {
	cur := s.machines[m.ID]
	if cur == nil {
		cur = &Machine{ID: m.ID}
		s.machines[m.ID] = cur
	}
	for dst, src := range map[*string]string{&cur.Name: m.Name, &cur.Hostname: m.Hostname, &cur.OS: m.OS, &cur.Version: m.Version} {
		if src != "" {
			*dst = src
		}
	}
	now := time.Now().UTC()
	cur.LastHeartbeatAt = &now
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	bound, ok := s.auth(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		if strings.Contains(err.Error(), "too large") {
			fail(w, http.StatusRequestEntityTooLarge, "payload_too_large", "body over 5 MiB")
			return
		}
		fail(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	var req struct {
		Machine  machineBody `json:"machine"`
		Sessions []struct {
			Meta   metaBody      `json:"meta"`
			Events []event.Event `json:"events"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			// Well-formed JSON of the wrong shape breaks a rule.
			fail(w, http.StatusBadRequest, "invalid_request", "wrong type")
			return
		}
		fail(w, http.StatusBadRequest, "bad_request", "malformed JSON")
		return
	}
	if req.Machine.ID == "" {
		fail(w, http.StatusBadRequest, "invalid_request", "machine.id is required")
		return
	}
	if bound != "" && bound != req.Machine.ID {
		fail(w, http.StatusForbidden, "forbidden", "token is bound to another machine")
		return
	}
	n := 0
	for _, sess := range req.Sessions {
		n += len(sess.Events)
	}
	if n > MaxEventsPerReq {
		fail(w, http.StatusRequestEntityTooLarge, "too_many_events", "more than 500 events")
		return
	}
	// The whole body must satisfy the public contract: the schema for every
	// event, plus the metadata rules of docs/api.md.
	if err := contract.ValidateBatch(body); err != nil {
		fail(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchMachine(req.Machine)
	accepted, dups := 0, 0
	for _, sess := range req.Sessions {
		k := key(req.Machine.ID, sess.Meta.SessionID)
		st := s.sessions[k]
		if st == nil {
			st = &stored{events: map[int64]event.Event{}, tools: map[string]int{}}
			st.meta = Session{MachineID: req.Machine.ID, ID: sess.Meta.SessionID, State: "UNKNOWN", Origin: string(contract.OriginLocal)}
			s.sessions[k] = st
		}
		mergeMeta(&st.meta, sess.Meta)
		if sess.Meta.FilesSource == "git" {
			st.hasGit, st.git = true, nil
			for _, f := range sess.Meta.Files {
				st.git = append(st.git, relTo(st.meta.CWD, f))
			}
		}
		for _, e := range sess.Events {
			if _, dup := st.events[e.Seq]; dup {
				dups++
				continue
			}
			st.events[e.Seq] = e
			accepted++
			for _, p := range event.TouchedFiles(e) {
				st.tools[relTo(st.meta.CWD, p)]++
			}
		}
		if !st.hasGit && len(st.tools) > 0 {
			st.meta.FilesSource = "tool_calls"
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": accepted, "duplicates": dups})
}

// relTo makes a path relative to the session's working directory when it is
// inside it, as docs/api.md describes for changed files.
func relTo(cwd, p string) string {
	if cwd != "" && strings.HasPrefix(p, cwd+"/") {
		return strings.TrimPrefix(p, cwd+"/")
	}
	return p
}

func mergeMeta(dst *Session, m metaBody) {
	set := func(d *string, v string) {
		if v != "" {
			*d = v
		}
	}
	dst.Provider = m.Provider
	set(&dst.CWD, m.CWD)
	set(&dst.Project, m.Project)
	set(&dst.Branch, m.Branch)
	set(&dst.Model, m.Model)
	set(&dst.State, m.State)
	set(&dst.Title, m.Title)
	// ValidateBatch has already rejected an unknown origin. Empty (omitted,
	// "" or null) keeps the stored value, so a later batch without origin
	// never turns a cloud session back into a local one.
	set(&dst.Origin, m.Origin)
	if dst.Commit == "" {
		dst.Commit = m.Commit
	}
	if m.FilesSource != "" {
		dst.FilesSource = m.FilesSource
	}
	if dst.StartedAt == nil {
		dst.StartedAt = m.StartedAt
	}
	if m.LastActivityAt != nil && (dst.LastActivityAt == nil || m.LastActivityAt.After(*dst.LastActivityAt)) {
		dst.LastActivityAt = m.LastActivityAt
	}
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	bound, ok := s.auth(w, r)
	if !ok {
		return
	}
	var req struct {
		Machine  machineBody `json:"machine"`
		Sessions []struct {
			SessionID      string     `json:"session_id"`
			State          string     `json:"state"`
			LastActivityAt *time.Time `json:"last_activity_at"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "malformed JSON")
		return
	}
	if req.Machine.ID == "" {
		fail(w, http.StatusBadRequest, "invalid_request", "machine.id is required")
		return
	}
	if bound != "" && bound != req.Machine.ID {
		fail(w, http.StatusForbidden, "forbidden", "token is bound to another machine")
		return
	}
	for _, sess := range req.Sessions {
		if !validStates[sess.State] {
			fail(w, http.StatusBadRequest, "invalid_request", "state is required and must be valid")
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchMachine(req.Machine)
	for _, sess := range req.Sessions {
		st := s.sessions[key(req.Machine.ID, sess.SessionID)]
		if st == nil {
			continue
		}
		st.meta.State = sess.State
		if sess.LastActivityAt != nil && (st.meta.LastActivityAt == nil || sess.LastActivityAt.After(*st.meta.LastActivityAt)) {
			st.meta.LastActivityAt = sess.LastActivityAt
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) linkStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MachineID string `json:"machine_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req) != nil || req.MachineID == "" {
		fail(w, http.StatusBadRequest, "invalid_request", "machine_id is required")
		return
	}
	if s.NoAccounts {
		fail(w, http.StatusConflict, "accounts_required", "this server has no accounts, so nobody could approve")
		return
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	code := make([]byte, 0, 9)
	for i, b := range raw {
		if i == 4 {
			code = append(code, '-')
		}
		code = append(code, alphabet[int(b)%len(alphabet)])
	}
	device := random("fkd_", 16)
	s.mu.Lock()
	s.links[device] = &link{machineID: req.MachineID, userCode: string(code), expires: s.nowLocked().Add(10 * time.Minute)}
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"device_code": device, "user_code": string(code), "expires_in": 600, "interval": 1})
}

func (s *Server) linkPoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.links[req.DeviceCode]
	if l == nil || l.collected {
		fail(w, http.StatusNotFound, "link_invalid", "unknown, expired or used link code")
		return
	}
	if s.nowLocked().After(l.expires) {
		fail(w, http.StatusGone, "link_expired", "the code expired")
		return
	}
	if !l.approved {
		writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
		return
	}
	l.collected = true
	secret := random("fk_", 24)
	s.tokens[secret] = &token{machineID: l.machineID, name: l.name, email: l.email}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "approved", "token": secret, "token_id": secret[3:11],
		"account": map[string]string{"id": "acct-" + l.email, "email": l.email},
		"machine": map[string]string{"id": l.machineID, "name": l.name},
	})
}

func (s *Server) bearer(r *http.Request) (string, *token) {
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tokens[secret]
	if t == nil || t.revoked {
		return secret, nil
	}
	return secret, t
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	_, t := s.bearer(r)
	if t == nil {
		fail(w, http.StatusUnauthorized, "unauthorized", "missing, malformed, expired or revoked credentials")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": "acct-" + t.email, "email": t.email,
		"token": map[string]string{"name": t.name, "scope": "ingest", "machine_id": t.machineID},
	})
}

func (s *Server) revokeCurrent(w http.ResponseWriter, r *http.Request) {
	secret, t := s.bearer(r)
	if t == nil {
		fail(w, http.StatusUnauthorized, "unauthorized", "missing, malformed, expired or revoked credentials")
		return
	}
	s.mu.Lock()
	s.tokens[secret].revoked = true
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func splitUID(r *http.Request) (machine, session string, ok bool) {
	uid, err := url.PathUnescape(r.PathValue("uid"))
	if err != nil {
		return "", "", false
	}
	machine, session, ok = strings.Cut(uid, ":")
	return machine, session, ok
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	machine, id, ok := splitUID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "bad_request", "malformed uid")
		return
	}
	sess, found := s.Session(machine, id)
	if !found {
		fail(w, http.StatusNotFound, "not_found", "unknown session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uid": machine + ":" + id, "machine_id": machine, "session_id": id, "provider": sess.Provider,
		"cwd": sess.CWD, "project": sess.Project, "branch": sess.Branch, "commit": sess.Commit,
		"model": sess.Model, "state": sess.State, "title": sess.Title,
		"files_source": sess.FilesSource, "origin": sess.Origin, "event_count": len(s.Events(machine, id)),
	})
}

func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) {
	machine, id, ok := splitUID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "bad_request", "malformed uid")
		return
	}
	if _, found := s.Session(machine, id); !found {
		fail(w, http.StatusNotFound, "not_found", "unknown session")
		return
	}
	after, limit := int64(-1), 200
	if v := r.URL.Query().Get("after_seq"); v != "" {
		fmt.Sscan(v, &after)
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscan(v, &limit)
	}
	limit = min(max(limit, 1), 1000)
	out := []event.Event{}
	more := false
	for _, e := range s.Events(machine, id) {
		if e.Seq <= after {
			continue
		}
		if len(out) == limit {
			more = true
			break
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "has_more": more})
}
