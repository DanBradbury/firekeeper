// Package share renders one stored session as a standalone HTML page that a
// reader can open from disk, with no server and no network, and filter.
//
// The page is a single file: its stylesheet, viewer script, and data are
// inline. A Content-Security-Policy meta tag allows only that stylesheet and
// script (by hash) and no network fetches, so opening a shared page never
// contacts anyone. Data is embedded as JSON in non-executable script blocks
// and read with textContent; transcript text is never parsed as HTML.
//
// The page is written as a stream: Begin writes the head and the session,
// Event appends one event, and Close finishes the page. A page that is never
// closed still opens; the viewer marks it incomplete.
package share

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"time"

	"github.com/DanBradbury/firekeeper/internal/redact"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

//go:embed viewer.css
var viewerCSS string

//go:embed viewer.js
var viewerJS string

// CSP is the policy the page carries in its meta tag: the inline stylesheet
// and viewer script by hash, data: images, and nothing else. Links still
// navigate; nothing loads or is sent in the background.
var CSP = "default-src 'none'; script-src " + hash(viewerJS) + "; style-src " + hash(viewerCSS) +
	"; img-src data:; base-uri 'none'; form-action 'none'"

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// Doc is everything the page shows above the transcript.
type Doc struct {
	Session store.Session       `json:"session"`
	Files   []store.SessionFile `json:"files"`
	// GeneratedAt is when the page was written.
	GeneratedAt time.Time `json:"generated_at"`
	// Version is the Firekeeper version that wrote the page.
	Version string `json:"version"`
	// Raw says whether events keep their original records. Without it, every
	// event's raw is null.
	Raw bool `json:"raw"`
}

// Writer streams one page. It is not safe for concurrent use.
type Writer struct {
	w      io.Writer
	raw    bool
	err    error
	closed bool
}

// ErrClosed is returned by Event and Close after Close.
var ErrClosed = errors.New("share: page already closed")

// Begin writes everything before the first event: the head, the viewer, and
// the session.
func Begin(w io.Writer, d Doc) (*Writer, error) {
	d.Session.Snippets = nil
	if d.Files == nil {
		d.Files = []store.SessionFile{}
	}
	title := d.Session.Title
	if title == "" {
		title = d.Session.SessionID
	}
	doc, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("share: encode session: %w", err)
	}
	pw := &Writer{w: w, raw: d.Raw}
	pw.write(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="` + html.EscapeString(CSP) + `">
<meta name="referrer" content="no-referrer">
<meta name="generator" content="Firekeeper">
<title>` + html.EscapeString(title) + ` · Firekeeper</title>
<style>` + viewerCSS + `</style>
<script>` + viewerJS + `</script>
</head>
<body>
<main id="app"><noscript><p>This page needs JavaScript to show the session.</p></noscript></main>
<script type="application/json" id="fk-doc">`)
	pw.write(string(doc))
	pw.write("</script>\n<script type=\"application/x-ndjson\" id=\"fk-events\">\n")
	return pw, pw.err
}

// Event appends ev. Text and raw pass through redaction again, since the
// page leaves the server; raw is dropped unless the Doc asked for it.
func (pw *Writer) Event(ev transcript.Event) error {
	if pw.closed {
		return ErrClosed
	}
	if pw.err != nil {
		return pw.err
	}
	if !pw.raw {
		ev.Raw = nil
	}
	ev, _ = redact.Event(ev, redact.Options{})
	// encoding/json escapes <, >, and & (and U+2028/2029), so no event can
	// end the script block it sits in. One event per line.
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("share: encode event %d: %w", ev.Seq, err)
	}
	pw.write(string(b))
	pw.write("\n")
	return pw.err
}

// Close finishes the page and marks it complete.
func (pw *Writer) Close() error {
	if pw.closed {
		return ErrClosed
	}
	pw.closed = true
	pw.write("</script>\n<script type=\"application/json\" id=\"fk-end\">{\"complete\":true}</script>\n</body>\n</html>\n")
	return pw.err
}

func (pw *Writer) write(s string) {
	if pw.err == nil {
		_, pw.err = io.WriteString(pw.w, s)
	}
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Filename suggests a download name for s's page, safe in a
// Content-Disposition header and on any file system.
func Filename(s store.Session) string {
	id := unsafeName.ReplaceAllString(s.SessionID, "-")
	if len(id) > 64 {
		id = id[:64]
	}
	if id == "" {
		id = "session"
	}
	name := "firekeeper"
	if p := unsafeName.ReplaceAllString(s.Provider, ""); p != "" {
		name += "-" + p
	}
	return name + "-" + id + ".html"
}
