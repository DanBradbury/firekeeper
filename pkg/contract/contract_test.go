package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/pkg/event"
)

func mustFixture(t *testing.T, name string) []byte {
	t.Helper()
	data := Fixture(name)
	if data == nil {
		t.Fatalf("no fixture %q", name)
	}
	return data
}

func TestSchemaMatchesDocs(t *testing.T) {
	docs, err := os.ReadFile("../../docs/event.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(docs, Schema()) {
		t.Fatal("pkg/contract/event.schema.json differs from docs/event.schema.json; copy the docs file over it")
	}
}

func TestEverySchemaKeywordIsSupported(t *testing.T) {
	if _, err := compileSchema(Schema()); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"oneOf": []}`, `{"properties": {"a": {"pattern": "x"}}}`, `{"format": "email"}`} {
		if _, err := compileSchema([]byte(bad)); err == nil {
			t.Errorf("schema %s compiled; unsupported keywords must be rejected", bad)
		}
	}
}

func TestFixturesAreJSON(t *testing.T) {
	names := Names("")
	if len(names) < 50 {
		t.Fatalf("only %d fixtures", len(names))
	}
	for _, name := range names {
		if name == "batch.malformed" {
			if json.Valid(mustFixture(t, name)) {
				t.Errorf("%s must not be valid JSON", name)
			}
			continue
		}
		if !json.Valid(mustFixture(t, name)) {
			t.Errorf("%s is not valid JSON", name)
		}
	}
	if Fixture("nope") != nil {
		t.Error("unknown fixture returned data")
	}
}

func TestEventSamples(t *testing.T) {
	for _, name := range Names("event.valid.") {
		data := mustFixture(t, name)
		if err := ValidateEvent(data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		var e event.Event
		if err := json.Unmarshal(data, &e); err != nil || e.Validate() != nil {
			t.Errorf("%s: Go type disagrees: %v %v", name, err, e.Validate())
		}
		batch, err := EventBatch(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateBatch(batch); err != nil {
			t.Errorf("%s in a batch: %v", name, err)
		}
	}
	for _, name := range Names("event.invalid.") {
		data := mustFixture(t, name)
		if ValidateEvent(data) == nil {
			t.Errorf("%s: schema accepted it", name)
		}
		var e event.Event
		if err := json.Unmarshal(data, &e); err != nil || e.Validate() == nil {
			t.Errorf("%s: must decode and fail event.Validate (decode error %v)", name, err)
		}
		batch, _ := EventBatch(data)
		if ValidateBatch(batch) == nil {
			t.Errorf("%s: batch validation accepted it", name)
		}
	}
	for _, name := range Names("event.schema_invalid.") {
		if ValidateEvent(mustFixture(t, name)) == nil {
			t.Errorf("%s: schema accepted it", name)
		}
	}
	for _, kind := range []string{"event.valid.", "event.invalid.", "event.schema_invalid."} {
		if len(Names(kind)) == 0 {
			t.Errorf("no %s fixtures", kind)
		}
	}
	// Every provider and role appears in a valid sample.
	providers, roles := map[string]bool{}, map[string]bool{}
	for _, name := range Names("event.valid.") {
		var e event.Event
		_ = json.Unmarshal(mustFixture(t, name), &e)
		providers[string(e.Provider)], roles[string(e.Role)] = true, true
	}
	for _, p := range event.Providers() {
		if !providers[string(p)] {
			t.Errorf("no valid sample for provider %s", p)
		}
	}
	for _, r := range event.Roles() {
		if !roles[string(r)] {
			t.Errorf("no valid sample for role %s", r)
		}
	}
}

func TestBatchSamples(t *testing.T) {
	for _, name := range append(Names("batch.valid."), "ingest.request") {
		if err := ValidateBatch(mustFixture(t, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range Names("batch.invalid.") {
		if ValidateBatch(mustFixture(t, name)) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ValidateBatch(mustFixture(t, "batch.malformed")); err == nil || !strings.Contains(err.Error(), "malformed JSON") {
		t.Errorf("malformed batch: %v", err)
	}
	if ValidateBatch([]byte(`[]`)) == nil || ValidateBatch(nil) == nil {
		t.Error("non-object batches accepted")
	}
}

func TestTooManyEvents(t *testing.T) {
	var e map[string]any
	_ = json.Unmarshal(mustFixture(t, "event.valid.user"), &e)
	events := make([]any, MaxEventsPerReq+1)
	for i := range events {
		events[i] = e
	}
	body := func(n int) []byte {
		b, _ := json.Marshal(map[string]any{
			"machine":  map[string]string{"id": SampleMachineID},
			"sessions": []any{map[string]any{"meta": map[string]string{"session_id": SampleSessionID, "provider": "codex"}, "events": events[:n]}},
		})
		return b
	}
	if err := ValidateBatch(body(MaxEventsPerReq)); err != nil {
		t.Errorf("500 events: %v", err)
	}
	if err := ValidateBatch(body(MaxEventsPerReq + 1)); err == nil {
		t.Error("501 events accepted")
	}
}

func TestErrorsNeverContainValues(t *testing.T) {
	secret := "SECRET-TRANSCRIPT-TEXT"
	var e map[string]any
	_ = json.Unmarshal(mustFixture(t, "event.valid.user"), &e)
	e["provider"], e["text"], e["role"], e["ts"] = secret, 7, secret, secret
	data, _ := json.Marshal(e)
	err := ValidateEvent(data)
	if err == nil {
		t.Fatal("accepted")
	}
	batch, _ := EventBatch(data)
	for _, got := range []error{err, ValidateBatch(batch)} {
		if got == nil || strings.Contains(got.Error(), secret) {
			t.Errorf("error leaks a value or is nil: %v", got)
		}
	}
}

func TestIntegerAndMinimum(t *testing.T) {
	var e map[string]any
	_ = json.Unmarshal(mustFixture(t, "event.valid.user"), &e)
	for text, wantErr := range map[string]bool{"1.0": false, "1e2": false, "1.5": true, "-0": false, "-1": true, "9223372036854775808": false} {
		e["seq"] = json.RawMessage(text)
		data, _ := json.Marshal(e)
		if got := ValidateEvent(data) != nil; got != wantErr {
			t.Errorf("seq %s: error = %v, want %v", text, got, wantErr)
		}
	}
	if ValidateEvent([]byte(`{} {}`)) == nil {
		t.Error("trailing data accepted")
	}
}

func TestErrorFixtures(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range ErrorCases() {
		var env map[string]string
		if err := json.Unmarshal(mustFixture(t, c.Fixture()), &env); err != nil || len(env) != 2 || env["code"] != c.Code || env["error"] == "" {
			t.Errorf("%s: bad envelope %v (%v)", c.Fixture(), env, err)
		}
		seen[c.Fixture()] = true
	}
	for _, name := range Names("error.") {
		if !seen[name] {
			t.Errorf("fixture %s has no ErrorCase", name)
		}
	}
}

func TestEndpointFixtures(t *testing.T) {
	for _, ep := range Endpoints() {
		names := append([]string{}, ep.Responses...)
		if ep.Request != "" {
			names = append(names, ep.Request)
		}
		if len(ep.Responses) == 0 {
			t.Errorf("%s %s has no response fixture", ep.Method, ep.Path)
		}
		for _, name := range names {
			if !json.Valid(mustFixture(t, name)) {
				t.Errorf("%s is not JSON", name)
			}
		}
	}
}

func TestMatchShape(t *testing.T) {
	golden := []byte(`{"a": 1, "b": {"c": "x", "d": null}, "e": [{"f": true}], "g": []}`)
	tests := []struct {
		name, got string
		ok        bool
	}{
		{"same", `{"a": 1, "b": {"c": "x", "d": null}, "e": [{"f": true}], "g": []}`, true},
		{"other values", `{"a": 99, "b": {"c": "y", "d": "now set"}, "e": [{"f": false}, {"f": true}], "g": [1, "x"]}`, true},
		{"empty array", `{"a": 1, "b": {"c": "x", "d": null}, "e": [], "g": []}`, true},
		{"null matches", `{"a": null, "b": {"c": "x", "d": null}, "e": [], "g": []}`, true},
		{"missing key", `{"a": 1, "b": {"c": "x", "d": null}, "e": []}`, false},
		{"extra key", `{"a": 1, "z": 1, "b": {"c": "x", "d": null}, "e": [], "g": []}`, false},
		{"wrong type", `{"a": "1", "b": {"c": "x", "d": null}, "e": [], "g": []}`, false},
		{"nested missing", `{"a": 1, "b": {"d": null}, "e": [], "g": []}`, false},
		{"element mismatch", `{"a": 1, "b": {"c": "x", "d": null}, "e": [{"f": 1}], "g": []}`, false},
		{"not json", `{`, false},
	}
	for _, tt := range tests {
		err := MatchShape(golden, []byte(tt.got))
		if (err == nil) != tt.ok {
			t.Errorf("%s: err = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
	if err := MatchShape([]byte(`{"a": "secret-value"}`), []byte(`{"a": 5}`)); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Errorf("error = %v", err)
	}
}

var (
	endpointHeading = regexp.MustCompile("(?m)^#{2,4} `(GET|POST|PUT|PATCH|DELETE) (/[^` ]+)`\\s*$")
	errorRow        = regexp.MustCompile("^\\|\\s*(\\d{3})\\s*\\|([^|]+)\\|")
	codeToken       = regexp.MustCompile("`([a-z_]+)`")
)

// TestAPIDocIsCovered fails when docs/api.md lists an endpoint or an error
// code that has no fixture, and when a fixture or registry entry names
// something the document does not.
func TestAPIDocIsCovered(t *testing.T) {
	raw, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)

	var docEndpoints []string
	for _, m := range endpointHeading.FindAllStringSubmatch(doc, -1) {
		docEndpoints = append(docEndpoints, m[1]+" "+m[2])
	}
	var haveEndpoints []string
	for _, ep := range Endpoints() {
		haveEndpoints = append(haveEndpoints, ep.Method+" "+ep.Path)
		if ep.Request != "" && Fixture(ep.Request) == nil {
			t.Errorf("%s %s: request fixture %s is missing", ep.Method, ep.Path, ep.Request)
		}
		for _, r := range ep.Responses {
			if Fixture(r) == nil {
				t.Errorf("%s %s: response fixture %s is missing", ep.Method, ep.Path, r)
			}
		}
	}
	// The write endpoints the client calls must appear in the doc and must
	// not appear in the registry by accident of spelling.
	compare(t, "endpoint", docEndpoints, haveEndpoints)

	section := doc
	if _, after, ok := strings.Cut(doc, "\n## Errors"); ok {
		section = after
	}
	if before, _, ok := strings.Cut(section[1:], "\n## "); ok {
		section = before
	}
	var docCodes, haveCodes []string
	status := map[string]int{}
	for _, line := range strings.Split(section, "\n") {
		m := errorRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		code, _ := strconv.Atoi(m[1])
		for _, c := range codeToken.FindAllStringSubmatch(m[2], -1) {
			docCodes = append(docCodes, c[1])
			status[c[1]] = code
		}
	}
	for _, c := range ErrorCases() {
		haveCodes = append(haveCodes, c.Code)
		if status[c.Code] != c.Status {
			t.Errorf("error %s: docs say status %d, registry says %d", c.Code, status[c.Code], c.Status)
		}
	}
	if len(docCodes) < 10 {
		t.Fatalf("parsed only %d error codes from docs/api.md; has the table format changed?", len(docCodes))
	}
	compare(t, "error code", docCodes, haveCodes)
	if len(docEndpoints) < 6 {
		t.Fatalf("parsed only %d endpoints from docs/api.md; has the heading format changed?", len(docEndpoints))
	}
}

func compare(t *testing.T, what string, doc, have []string) {
	t.Helper()
	sort.Strings(doc)
	sort.Strings(have)
	in := func(list []string, s string) bool {
		i := sort.SearchStrings(list, s)
		return i < len(list) && list[i] == s
	}
	for _, d := range doc {
		if !in(have, d) {
			t.Errorf("docs/api.md lists %s %q but pkg/contract has no fixture for it", what, d)
		}
	}
	for _, h := range have {
		if !in(doc, h) {
			t.Errorf("pkg/contract has a fixture for %s %q that docs/api.md does not list", what, h)
		}
	}
}
