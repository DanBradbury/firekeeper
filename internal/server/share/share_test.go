package share

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func doc(raw bool) Doc {
	return Doc{
		Session: store.Session{
			UID: "m1:s1", MachineID: "m1", SessionID: "s1", Provider: "codex",
			Title: `Fix <b>"bug"</b> & </title>`, State: "ENDED",
			Snippets: []store.Snippet{{Seq: 0, Text: "search only"}},
		},
		Files:       []store.SessionFile{{Path: "main.go", FirstSeq: 1, LastSeq: 1, Changes: 1}},
		GeneratedAt: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC),
		Version:     "v1.2.3",
		Raw:         raw,
	}
}

func event(seq int64, role transcript.Role, text, raw string) transcript.Event {
	return transcript.Event{
		MachineID: "m1", SessionID: "s1", Provider: transcript.ProviderCodex,
		Seq: seq, Role: role, Text: text, Raw: json.RawMessage(raw),
	}
}

// render writes a closed page holding evs.
func render(t *testing.T, d Doc, evs ...transcript.Event) string {
	t.Helper()
	var buf bytes.Buffer
	pw, err := Begin(&buf, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if err := pw.Event(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// block returns the text of the script element with id.
func block(t *testing.T, page, id string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<script type="[^"]+" id="` + id + `">(.*?)</script>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no %s block", id)
	}
	return m[1]
}

func pageEvents(t *testing.T, page string) []transcript.Event {
	t.Helper()
	var out []transcript.Event
	for _, line := range strings.Split(block(t, page, "fk-events"), "\n") {
		if line == "" {
			continue
		}
		var ev transcript.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event line is not JSON: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

func TestPageCarriesSessionAndEvents(t *testing.T) {
	page := render(t, doc(true),
		event(0, transcript.RoleUser, "hello", `{"type":"msg"}`),
		event(1, transcript.RoleToolCall, "edit main.go", `{"type":"call"}`))

	var got Doc
	if err := json.Unmarshal([]byte(block(t, page, "fk-doc")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Session.UID != "m1:s1" || got.Version != "v1.2.3" || !got.Raw || len(got.Files) != 1 {
		t.Fatalf("wrong doc: %+v", got)
	}
	if got.Session.Snippets != nil {
		t.Fatal("search snippets leaked into the page")
	}
	evs := pageEvents(t, page)
	if len(evs) != 2 || evs[0].Seq != 0 || evs[1].Seq != 1 || evs[1].Text != "edit main.go" {
		t.Fatalf("wrong events: %+v", evs)
	}
	if string(evs[0].Raw) != `{"type":"msg"}` {
		t.Fatalf("raw not kept: %s", evs[0].Raw)
	}
	if !strings.Contains(page, `id="fk-end"`) {
		t.Fatal("closed page has no end marker")
	}
	if !strings.Contains(page, "<title>Fix &lt;b&gt;&#34;bug&#34;&lt;/b&gt; &amp; &lt;/title&gt; · Firekeeper</title>") {
		t.Fatal("title not escaped")
	}
}

func TestPageOmitsRawUnlessAsked(t *testing.T) {
	page := render(t, doc(false), event(0, transcript.RoleUser, "hi", `{"secret_shape":"kept out"}`))
	if strings.Contains(page, "kept out") {
		t.Fatal("raw record written without Raw")
	}
	if evs := pageEvents(t, page); len(evs) != 1 || string(evs[0].Raw) != "null" {
		t.Fatalf("raw should be null: %+v", evs)
	}
}

// Transcript text must never be able to end its script block or inject
// markup, however it is spelled.
func TestPageEscapesHostileText(t *testing.T) {
	hostile := "</script><script>alert(1)</script><!-- \u2028 </SCRIPT >"
	d := doc(true)
	d.Session.Title = hostile
	d.Session.CWD = hostile
	page := render(t, d, event(0, transcript.RoleAssistant, hostile, `{"x":"`+strings.ReplaceAll(hostile, "\u2028", `\u2028`)+`"}`))

	// Outside the viewer, every "</script" in the page is one this package
	// wrote: closing the viewer and the three data blocks.
	if n := strings.Count(strings.ToLower(page), "</script"); n != 4 {
		t.Fatalf("found %d closing script tags, want 4", n)
	}
	if i := strings.Index(page, "<!--"); i >= 0 {
		t.Fatalf("unescaped comment opener: %q", page[max(0, i-40):i+10])
	}
	for _, id := range []string{"fk-doc", "fk-events"} {
		if strings.Contains(block(t, page, id), "\u2028") {
			t.Fatalf("unescaped line separator in %s", id)
		}
	}
	evs := pageEvents(t, page)
	if len(evs) != 1 || evs[0].Text != hostile {
		t.Fatalf("text did not round-trip: %+v", evs)
	}
}

func TestPageRedactsAgain(t *testing.T) {
	secret := "ghp_" + strings.Repeat("aB3dE", 7) + "x"
	page := render(t, doc(true), event(0, transcript.RoleToolResult, "token "+secret, `{"out":"`+secret+`"}`))
	if strings.Contains(page, secret) {
		t.Fatal("secret survived export")
	}
	if !strings.Contains(page, "[REDACTED:") {
		t.Fatal("no redaction marker")
	}
}

func TestUnclosedPageHasNoEndMarker(t *testing.T) {
	var buf bytes.Buffer
	pw, err := Begin(&buf, doc(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := pw.Event(event(0, transcript.RoleUser, "hi", `{}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `id="fk-end"`) {
		t.Fatal("unfinished page marked complete")
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pw.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("second Close: %v", err)
	}
	if err := pw.Event(event(1, transcript.RoleUser, "late", `{}`)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Event after Close: %v", err)
	}
}

type failWriter struct{ after int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("disk full")
	}
	f.after--
	return len(p), nil
}

func TestWriteErrorsStick(t *testing.T) {
	pw, err := Begin(&failWriter{after: 3}, doc(false))
	if err == nil {
		err = pw.Event(event(0, transcript.RoleUser, "hi", `{}`))
	}
	if err == nil {
		t.Fatal("write error not reported")
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// The page's policy must allow exactly the inline stylesheet and viewer it
// carries, and nothing on the network.
func TestCSPMatchesInlineAssets(t *testing.T) {
	page := render(t, doc(false))
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(page)
	script := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindStringSubmatch(page)
	if style == nil || script == nil {
		t.Fatal("inline assets missing")
	}
	for _, want := range []string{"script-src " + sha(script[1]), "style-src " + sha(style[1]), "default-src 'none'"} {
		if !strings.Contains(CSP, want) {
			t.Fatalf("CSP %q lacks %q", CSP, want)
		}
	}
	if !strings.Contains(page, `http-equiv="Content-Security-Policy"`) {
		t.Fatal("page has no CSP meta tag")
	}
	for _, bad := range []string{"connect-src", "http:", "https:", "'unsafe-inline'", "'unsafe-eval'"} {
		if strings.Contains(CSP, bad) {
			t.Fatalf("CSP allows %s", bad)
		}
	}
}

func TestAssetsCannotEndTheirElements(t *testing.T) {
	if strings.Contains(strings.ToLower(viewerJS), "</script") || strings.Contains(viewerJS, "<!--") {
		t.Fatal("viewer.js would end its script element")
	}
	if strings.Contains(strings.ToLower(viewerCSS), "</style") {
		t.Fatal("viewer.css would end its style element")
	}
}

func TestFilename(t *testing.T) {
	for _, tc := range []struct {
		provider, id, want string
	}{
		{"codex", "019a-bc", "firekeeper-codex-019a-bc.html"},
		{"claude", `../../etc/"pass wd"`, "firekeeper-claude-..-..-etc-pass-wd-.html"},
		{"", "", "firekeeper-session.html"},
		{"co\"dex", strings.Repeat("a", 100), "firekeeper-codex-" + strings.Repeat("a", 64) + ".html"},
	} {
		if got := Filename(store.Session{Provider: tc.provider, SessionID: tc.id}); got != tc.want {
			t.Errorf("Filename(%q, %q) = %q, want %q", tc.provider, tc.id, got, tc.want)
		}
		if got := Filename(store.Session{Provider: tc.provider, SessionID: tc.id}); strings.ContainsAny(got, `/\"`+"\r\n ") {
			t.Errorf("unsafe filename %q", got)
		}
	}
}

// The page must work offline and send nothing, so its assets name no
// network location.
func TestAssetsHaveNoExternalReferences(t *testing.T) {
	urlRe := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)
	cssRe := regexp.MustCompile(`(?i)@import|url\(`)
	for name, s := range map[string]string{"viewer.js": viewerJS, "viewer.css": viewerCSS} {
		if m := urlRe.FindString(s); m != "" {
			t.Errorf("%s references external URL %q", name, m)
		}
		if m := cssRe.FindString(s); m != "" {
			t.Errorf("%s loads CSS from elsewhere: %q", name, m)
		}
		if strings.Contains(s, "fetch(") || strings.Contains(s, "XMLHttpRequest") || strings.Contains(s, "sendBeacon") {
			t.Errorf("%s makes network requests", name)
		}
	}
}
