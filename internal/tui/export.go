package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/redact"
	"github.com/DanBradbury/firekeeper/internal/serverbin"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
	"github.com/DanBradbury/firekeeper/pkg/event"
	tea "github.com/charmbracelet/bubbletea"
)

// export.go saves the open chat's session as one standalone HTML page, the
// same page the dashboard server's Export HTML button gives. The page is
// rendered by `firekeeper-server share`, which reads the session on stdin
// and opens no database or network; this binary has no renderer of its own.
// Nothing is downloaded or installed: without the server the export fails
// with a message. The file is written to ~/Downloads, or to
// ~/.firekeeper/exports when there is no Downloads folder.

// exportTimeout bounds one export, reading the transcript included.
const exportTimeout = 2 * time.Minute

// exportStderrLimit is how much of the server's stderr an error keeps.
const exportStderrLimit = 512

// Replaced in tests so nothing finds a developer's real server or writes
// into the real home directory.
var (
	exportLookup = serverbin.Default()
	exportHome   = os.UserHomeDir
)

type exportResultMsg struct {
	path string
	err  error
}

// errExportServerMissing is returned when no firekeeper-server is installed.
var errExportServerMissing = errors.New("HTML export needs firekeeper-server; run `firekeeper serve` to install it")

// errExportServerOld is returned when the server has no share command.
var errExportServerOld = errors.New("this firekeeper-server cannot export; run `firekeeper serve` to update it")

// shareSession is the session line `firekeeper-server share` reads first,
// with the field names of the server's read API.
type shareSession struct {
	SessionID      string        `json:"session_id"`
	Provider       string        `json:"provider"`
	Origin         string        `json:"origin"`
	CWD            string        `json:"cwd"`
	Project        string        `json:"project"`
	Branch         string        `json:"branch"`
	Model          string        `json:"model"`
	State          session.State `json:"state"`
	Title          string        `json:"title"`
	StartedAt      *time.Time    `json:"started_at"`
	LastActivityAt *time.Time    `json:"last_activity_at"`
	EventCount     int64         `json:"event_count"`
	Tokens         event.Tokens  `json:"tokens"`
}

// shareFile is one changed file in the share header.
type shareFile struct {
	Path     string `json:"path"`
	Absolute bool   `json:"absolute"`
	FirstSeq int64  `json:"first_seq"`
	LastSeq  int64  `json:"last_seq"`
	Changes  int64  `json:"changes"`
}

type shareHeader struct {
	Session shareSession `json:"session"`
	Files   []shareFile  `json:"files"`
}

// exportChat starts an export of the open chat's session.
func (m *model) exportChat() tea.Cmd {
	if !m.chat.open || m.chat.exporting {
		return nil
	}
	m.chat.exporting = true
	m.terminalStatus = "exporting session as HTML…"
	return exportSession(m.chat.meta)
}

func (m *model) applyExportResult(msg exportResultMsg) {
	m.chat.exporting = false
	if msg.err != nil {
		m.terminalStatus = "export failed: " + msg.err.Error()
		return
	}
	m.exportedPath = msg.path
	m.terminalStatus = exportSavedStatus(msg.path)
}

func exportSavedStatus(path string) string {
	return "saved " + displayPath(path, exportHome) + " · o or click to open"
}

// openShownExport opens the last export while its notice shows, and clears
// the notice. It returns nil when there is no notice to act on.
func (m *model) openShownExport() tea.Cmd {
	if !m.exportNoticeShown() {
		return nil
	}
	m.terminalStatus = ""
	return openExport(m.exportedPath)
}

// exportNoticeShown reports whether the footer shows the notice for the last
// export, which o or a click opens.
func (m model) exportNoticeShown() bool {
	return m.exportedPath != "" && m.terminalStatus == exportSavedStatus(m.exportedPath) &&
		!m.helpOpen && (m.chat.open || m.activeTab == processesTab)
}

// exportOpenedMsg reports how opening an exported page went.
type exportOpenedMsg struct{ err error }

// openLocalFile opens path in the system's default app for it, without
// waiting for that app. Tests replace it.
var openLocalFile = func(path string) error {
	name, args := openFileCommand(runtime.GOOS, path)
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// openFileCommand is the command that opens path on goos. path is absolute,
// so no opener reads it as a flag.
func openFileCommand(goos, path string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{path}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", path}
	default:
		return "xdg-open", []string{path}
	}
}

func openExport(path string) tea.Cmd {
	return func() tea.Msg { return exportOpenedMsg{err: openLocalFile(path)} }
}

// exportSession reads the whole transcript, has the server render it, and
// writes the page next to the user's other downloads.
func exportSession(meta session.Meta) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
		defer cancel()
		path, err := writeSessionExport(ctx, meta)
		return exportResultMsg{path: path, err: err}
	}
}

func writeSessionExport(ctx context.Context, meta session.Meta) (string, error) {
	server, err := serverbin.Resolve(exportLookup)
	if errors.Is(err, serverbin.ErrNotFound) {
		return "", errExportServerMissing
	}
	if err != nil {
		return "", err
	}
	home, err := exportHome()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	header, events, err := readSessionForExport(meta, home)
	if err != nil {
		return "", err
	}
	dir, err := exportDir(home)
	if err != nil {
		return "", err
	}
	out, err := os.CreateTemp(dir, ".firekeeper-export-*.html")
	if err != nil {
		return "", fmt.Errorf("create export file: %w", err)
	}
	defer func() {
		if out != nil {
			out.Close()
			os.Remove(out.Name())
		}
	}()
	if err := runShare(ctx, server, header, events, out); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("write export file: %w", err)
	}
	final := filepath.Join(dir, exportFilename(meta))
	if err := os.Rename(out.Name(), final); err != nil {
		return "", fmt.Errorf("save export file: %w", err)
	}
	out = nil
	return final, nil
}

// readSessionForExport reads every event of the session's transcript,
// redacted, and the header describing it.
func readSessionForExport(meta session.Meta, home string) (shareHeader, []event.Event, error) {
	source, ok := reader.For(transcript.Provider(meta.Provider))
	if !ok {
		return shareHeader{}, nil, fmt.Errorf("no transcript reader for %s sessions", meta.Provider)
	}
	paths, err := source.Locate(meta)
	if err != nil {
		return shareHeader{}, nil, err
	}
	if len(paths) == 0 {
		return shareHeader{}, nil, errors.New("no transcript found for this session")
	}
	var events []event.Event
	for _, path := range paths {
		read, _, err := source.Read(path, 0)
		if err != nil {
			return shareHeader{}, nil, err
		}
		events = append(events, read...)
	}

	header := shareHeader{Session: shareSession{
		SessionID: meta.ID,
		Provider:  meta.Provider,
		Origin:    "local",
		CWD:       meta.CWD,
		Project:   exportProject(meta),
		Branch:    meta.GitBranch,
		Model:     meta.Model,
		State:     meta.State,
		Title:     meta.Name,
	}, Files: []shareFile{}}
	files := map[string]int{}
	opts := redact.Options{HomeDir: home}
	for i, e := range events {
		// Changed files come from the original record; redaction may
		// rewrite the paths it holds.
		for _, path := range event.TouchedFiles(e) {
			path, _ = redact.String(path, opts)
			index, seen := files[path]
			if !seen {
				index = len(header.Files)
				files[path] = index
				header.Files = append(header.Files, shareFile{Path: path, Absolute: filepath.IsAbs(path), FirstSeq: e.Seq})
			}
			header.Files[index].LastSeq = e.Seq
			header.Files[index].Changes++
		}
		e.SessionID = meta.ID
		e, _ = redact.Event(e, opts)
		events[i] = e

		s := &header.Session
		if e.TS != nil {
			if s.StartedAt == nil || e.TS.Before(*s.StartedAt) {
				s.StartedAt = e.TS
			}
			if s.LastActivityAt == nil || e.TS.After(*s.LastActivityAt) {
				s.LastActivityAt = e.TS
			}
		}
		if e.Model != nil && *e.Model != "" {
			s.Model = *e.Model
		}
		s.Tokens.Input += e.Tokens.Input
		s.Tokens.Output += e.Tokens.Output
		s.Tokens.Cache += e.Tokens.Cache
	}
	header.Session.EventCount = int64(len(events))
	header.Session.CWD, _ = redact.String(header.Session.CWD, opts)
	header.Session.Title, _ = redact.String(header.Session.Title, opts)
	return header, events, nil
}

// runShare streams the header and events to `firekeeper-server share` and
// its page to out.
func runShare(ctx context.Context, server string, header shareHeader, events []event.Event, out io.Writer) error {
	cmd := exec.CommandContext(ctx, server, "share")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = out
	stderr := &limitedBuffer{limit: exportStderrLimit}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("run firekeeper-server: %w", err)
	}
	writeErr := writeShareInput(stdin, header, events)
	stdin.Close()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return errors.New("export timed out")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// 2 is a usage error: a server from before share treats it as an
		// unknown command.
		if exit.ExitCode() == 2 {
			return errExportServerOld
		}
		if msg := firstLine(stderr.String()); msg != "" {
			return errors.New(sanitizeProcessCommand(msg))
		}
		return fmt.Errorf("firekeeper-server share exited with status %d", exit.ExitCode())
	}
	if err != nil {
		return fmt.Errorf("run firekeeper-server: %w", err)
	}
	if writeErr != nil {
		return fmt.Errorf("send session to firekeeper-server: %w", writeErr)
	}
	return nil
}

// writeShareInput writes the header and then one event per line.
func writeShareInput(w io.Writer, header shareHeader, events []event.Event) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	if err := enc.Encode(header); err != nil {
		return err
	}
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// exportDir is ~/Downloads when it exists, otherwise ~/.firekeeper/exports.
func exportDir(home string) (string, error) {
	downloads := filepath.Join(home, "Downloads")
	if info, err := os.Stat(downloads); err == nil && info.IsDir() {
		return downloads, nil
	}
	dir := filepath.Join(home, ".firekeeper", "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create export folder: %w", err)
	}
	return dir, nil
}

var unsafeExportName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// exportFilename matches the name the server's Export HTML button suggests.
func exportFilename(meta session.Meta) string {
	id := unsafeExportName.ReplaceAllString(meta.ID, "-")
	if len(id) > 64 {
		id = id[:64]
	}
	if id == "" {
		id = "session"
	}
	name := "firekeeper"
	if p := unsafeExportName.ReplaceAllString(meta.Provider, ""); p != "" {
		name += "-" + p
	}
	return name + "-" + id + ".html"
}

func exportProject(meta session.Meta) string {
	switch {
	case meta.Project != "":
		return meta.Project
	case meta.Repository != "":
		return filepath.Base(meta.Repository)
	case meta.CWD != "":
		return filepath.Base(meta.CWD)
	}
	return ""
}

// displayPath shortens a path under the home directory to ~/...
func displayPath(path string, home func() (string, error)) string {
	if h, err := home(); err == nil && h != "" {
		if rel, err := filepath.Rel(h, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// limitedBuffer keeps the first limit bytes written to it.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
