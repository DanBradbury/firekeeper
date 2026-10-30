// Package contract is the machine-readable form of Firekeeper's public wire
// contract: the event JSON Schema, golden example bodies for every endpoint a
// client calls, and the helpers to check bodies against them. It is what
// docs/api.md and docs/event.schema.json say, embedded in a Go package so that
// a client and a server, which live in different repositories, can test
// against one pinned copy and notice when they drift.
//
// # Versioning
//
// This package is part of the module's public API and follows semantic
// versioning together with docs/api.md. Within a major version, fixtures are
// only added, never removed or renamed, and a fixture's shape only gains
// optional fields that docs/api.md documents. Version names the API version
// the fixtures describe.
//
// # Dependencies
//
// Standard library only. The schema validator is a small built-in subset of
// JSON Schema (see jsonschema.go), sufficient for docs/event.schema.json.
//
// # Fixtures
//
// Fixture returns an embedded body by name. Names are the file names under
// testdata/ without ".json", and Names lists them by prefix:
//
//	ingest.request, ingest.response        POST /v1/ingest
//	heartbeat.request, heartbeat.response  POST /v1/heartbeat
//	link_start.request, link_start.response
//	link_poll.request, link_poll.response.pending, link_poll.response.approved
//	account.response                       GET /v1/account
//	tokens_current.response                DELETE /v1/tokens/current
//	error.<code>                           the error envelope for each code
//	event.valid.*                          events every server must accept
//	event.invalid.*                        events every server must reject with
//	                                       invalid_request (inside a batch)
//	event.schema_invalid.*                 events only the strict schema rejects
//	                                       (missing keys, extra keys, wrong types)
//	batch.valid.*                          ingest bodies every server must accept
//	batch.invalid.*                        well-formed bodies rejected with invalid_request
//	batch.malformed                        not JSON: rejected with bad_request
//
// Response fixtures hold example values. Compare a live response with
// ContainsShape (a real server, which may add fields) or MatchShape (a stand-in
// that must mirror the fixtures). Both check keys and JSON types and ignore
// values.
package contract

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/pkg/event"
)

// Version is the API version the embedded fixtures describe.
const Version = "v1"

// The machine and session every sample event and batch fixture uses, so a
// server test can wrap a sample event in a batch with EventBatch.
const (
	SampleMachineID = "m1"
	SampleSessionID = "0199-abc"
)

// Limits from docs/api.md.
const (
	MaxBodyBytes    = 5 << 20
	MaxEventsPerReq = 500
	MaxFiles        = 5000
	MaxFilePathLen  = 4096
)

//go:embed event.schema.json
var schemaJSON []byte

//go:embed testdata/*.json
var fixtures embed.FS

var eventSchema = func() *schemaNode {
	n, err := compileSchema(schemaJSON)
	if err != nil {
		panic("contract: " + err.Error())
	}
	return n
}()

// Schema returns the event JSON Schema, a copy of docs/event.schema.json.
func Schema() []byte { return append([]byte(nil), schemaJSON...) }

// Fixture returns the embedded golden body called name, or nil when there is
// none. The result is a copy the caller may modify.
func Fixture(name string) []byte {
	data, err := fixtures.ReadFile(path.Join("testdata", name+".json"))
	if err != nil {
		return nil
	}
	return data
}

// Names lists the fixture names that start with prefix, sorted.
func Names(prefix string) []string {
	entries, err := fixtures.ReadDir("testdata")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Endpoint is one endpoint of docs/api.md that a client calls.
type Endpoint struct {
	Method string
	Path   string
	// Status is the HTTP status of a success.
	Status int
	// Request names the request body fixture, "" when there is no body.
	Request string
	// Responses names the success body fixtures. More than one means the
	// endpoint answers in more than one shape (link polling).
	Responses []string
}

// Endpoints lists every endpoint docs/api.md specifies for clients.
func Endpoints() []Endpoint {
	return []Endpoint{
		{"POST", "/v1/ingest", 200, "ingest.request", []string{"ingest.response"}},
		{"POST", "/v1/heartbeat", 200, "heartbeat.request", []string{"heartbeat.response"}},
		{"POST", "/v1/link/start", 201, "link_start.request", []string{"link_start.response"}},
		{"POST", "/v1/link/poll", 200, "link_poll.request", []string{"link_poll.response.pending", "link_poll.response.approved"}},
		{"GET", "/v1/account", 200, "", []string{"account.response"}},
		{"DELETE", "/v1/tokens/current", 200, "", []string{"tokens_current.response"}},
	}
}

// ErrorCase is one documented error: its HTTP status and machine-readable
// code. Its envelope fixture is "error."+Code.
type ErrorCase struct {
	Status int
	Code   string
}

// Fixture returns the name of the error envelope fixture.
func (c ErrorCase) Fixture() string { return "error." + c.Code }

// ErrorCases lists every error code in docs/api.md.
func ErrorCases() []ErrorCase {
	return []ErrorCase{
		{400, "bad_request"},
		{400, "invalid_request"},
		{401, "unauthorized"},
		{403, "forbidden"},
		{404, "link_invalid"},
		{409, "accounts_required"},
		{410, "link_expired"},
		{413, "payload_too_large"},
		{413, "too_many_events"},
		{413, "storage_limit"},
		{413, "session_limit"},
		{429, "rate_limited"},
		{500, "internal"},
		{503, "unavailable"},
	}
}

// ValidateEvent checks one event, as JSON, against the event schema. The
// error lists every violation by path and rule. It never contains the
// event's values, so it is safe to log.
func ValidateEvent(data []byte) error {
	v, err := decodeJSON(data)
	if err != nil {
		return err
	}
	return joinProblems(eventSchema.validate("event", v))
}

var (
	validStates = []string{"ACTIVE", "WAITING", "NEEDS_INPUT", "ENDED", "UNKNOWN"}
	validFiles  = []string{"git", "tool_calls"}
	// metaKeys are the documented sessions[].meta fields (docs/api.md).
	metaKeys = []string{
		"session_id", "provider", "cwd", "project", "branch", "commit", "model",
		"state", "title", "started_at", "last_activity_at", "origin",
		"files_source", "files",
	}
)

// Origin says where a session ran. It is optional on ingest session metadata
// (sessions[].meta.origin), not a field of the transcript event.
//
// The empty origin is unset. ValidateBatch accepts it, along with a missing
// field and JSON null. A server keeps the stored origin in that case, and
// records local when the session is new. Any other value is rejected.
type Origin string

const (
	// OriginLocal is a session that ran on the reporting machine.
	OriginLocal Origin = "local"
	// OriginCloud is a session that ran on a hosted service.
	OriginCloud Origin = "cloud"
)

// Origins lists every origin ingest accepts, in documentation order.
func Origins() []Origin {
	return []Origin{OriginLocal, OriginCloud}
}

// Valid reports whether o is local or cloud. The empty origin is unset, not
// a member of the set.
func (o Origin) Valid() bool {
	switch o {
	case OriginLocal, OriginCloud:
		return true
	default:
		return false
	}
}

// ValidateBatch checks a POST /v1/ingest request body: the machine, each
// session's metadata and every event (with ValidateEvent), that every event
// carries its batch's machine id and its session's id, the session origin
// when one is set, and the 500-event limit. An empty, null, or absent origin
// is allowed. It does not apply the 5 MiB body limit or any per-account limit.
// Like ValidateEvent, its errors never contain values from the body.
func ValidateBatch(data []byte) error {
	v, err := decodeJSON(data)
	if err != nil {
		return err
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	body, ok := v.(map[string]any)
	if !ok {
		return errors.New("batch: want a JSON object")
	}
	machineID := nonEmptyString(body, "machine", "id")
	machine, _ := body["machine"].(map[string]any)
	if machine == nil {
		add("batch.machine: missing object")
	} else if machineID == "" {
		add("batch.machine.id: missing or empty")
	}
	var sessions []any
	switch s := body["sessions"].(type) {
	case nil:
	case []any:
		sessions = s
	default:
		add("batch.sessions: want an array")
	}
	total := 0
	for i, raw := range sessions {
		p := fmt.Sprintf("batch.sessions[%d]", i)
		sess, ok := raw.(map[string]any)
		if !ok {
			add("%s: want an object", p)
			continue
		}
		meta, ok := sess["meta"].(map[string]any)
		if !ok {
			add("%s.meta: missing object", p)
			continue
		}
		// Keys are case-sensitive. A key that differs from a documented one
		// only in case is rejected here (docs/api.md lets a server do the
		// same, but does not require it): decoders that match field names
		// case-insensitively, such as encoding/json into a struct, would
		// otherwise store a value that was never validated ({"Origin":"mars"}).
		for _, key := range sortedKeys(meta) {
			for _, known := range metaKeys {
				if key != known && strings.EqualFold(key, known) {
					add("%s.meta.%s: a key differs from it only in case", p, known)
				}
			}
		}
		sessionID, _ := meta["session_id"].(string)
		if sessionID == "" {
			add("%s.meta.session_id: missing or empty", p)
		}
		if prov, _ := meta["provider"].(string); !event.Provider(prov).Valid() {
			add("%s.meta.provider: not a known provider", p)
		}
		if st, present := meta["state"]; present {
			if s, ok := st.(string); !ok || (s != "" && !contains(validStates, s)) {
				add("%s.meta.state: not a known state", p)
			}
		}
		for _, key := range []string{"started_at", "last_activity_at"} {
			switch t := meta[key].(type) {
			case nil:
			case string:
				if _, err := time.Parse(time.RFC3339, t); err != nil {
					add("%s.meta.%s: not an RFC 3339 date-time", p, key)
				}
			default:
				add("%s.meta.%s: want a string or null", p, key)
			}
		}
		// Absent, empty, and null keep the stored origin (local for a new
		// session). A non-string or an unknown string is rejected.
		if raw, present := meta["origin"]; present && raw != nil {
			s, ok := raw.(string)
			if !ok || (s != "" && !Origin(s).Valid()) {
				add("%s.meta.origin: not a known origin", p)
			}
		}
		source, _ := meta["files_source"].(string)
		if _, present := meta["files_source"]; present && source != "" && !contains(validFiles, source) {
			add("%s.meta.files_source: not a known source", p)
		}
		if files, present := meta["files"]; present && files != nil {
			list, ok := files.([]any)
			switch {
			case source != "git":
				add("%s.meta.files: only allowed with files_source git", p)
			case !ok:
				add("%s.meta.files: want an array", p)
			case len(list) > MaxFiles:
				add("%s.meta.files: more than %d paths", p, MaxFiles)
			default:
				for j, f := range list {
					s, ok := f.(string)
					if !ok || strings.TrimSpace(s) == "" || len(s) > MaxFilePathLen {
						add("%s.meta.files[%d]: want a non-blank string of at most %d bytes", p, j, MaxFilePathLen)
					}
				}
			}
		}
		var events []any
		switch e := sess["events"].(type) {
		case nil:
		case []any:
			events = e
		default:
			add("%s.events: want an array", p)
		}
		total += len(events)
		for j, ev := range events {
			ep := fmt.Sprintf("%s.events[%d]", p, j)
			for _, problem := range eventSchema.validate(ep, ev) {
				problems = append(problems, problem)
			}
			if obj, ok := ev.(map[string]any); ok {
				if obj["machine_id"] != machineID {
					add("%s.machine_id: does not match the batch machine", ep)
				}
				if obj["session_id"] != sessionID {
					add("%s.session_id: does not match the session", ep)
				}
			}
		}
	}
	if total > MaxEventsPerReq {
		add("batch: more than %d events", MaxEventsPerReq)
	}
	return joinProblems(problems)
}

// EventBatch wraps one sample event in a minimal ingest request for machine
// SampleMachineID and session SampleSessionID. The session's provider is the
// event's own when it is a known provider and "codex" otherwise, so the batch
// is invalid only because of what is wrong with the event.
func EventBatch(eventJSON []byte) ([]byte, error) {
	if !json.Valid(eventJSON) {
		return nil, errors.New("contract: event is not valid JSON")
	}
	var probe struct {
		Provider string `json:"provider"`
	}
	_ = json.Unmarshal(eventJSON, &probe)
	provider := probe.Provider
	if !event.Provider(provider).Valid() {
		provider = string(event.ProviderCodex)
	}
	return json.Marshal(map[string]any{
		"machine": map[string]string{"id": SampleMachineID},
		"sessions": []any{map[string]any{
			"meta":   map[string]string{"session_id": SampleSessionID, "provider": provider},
			"events": []json.RawMessage{eventJSON},
		}},
	})
}

func nonEmptyString(obj map[string]any, parent, key string) string {
	p, _ := obj[parent].(map[string]any)
	s, _ := p[key].(string)
	return s
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func joinProblems(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}
