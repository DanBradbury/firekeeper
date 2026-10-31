// Package antigravity reads Google Antigravity CLI's main JSONL transcript.
// Desktop markdown and transcript_full.jsonl duplicates are not included.
//
// Envelope evidence:
// https://github.com/jazzyalex/agent-sessions/tree/main/Resources/Fixtures/stage0/agents/antigravity
package antigravity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/fsutil"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

const maxToolText = 64 << 10
const maxLineBytes = 8 << 20

func init() { reader.Register(transcript.ProviderAntigravity, &Source{}) }

// Source.Dir overrides the antigravity-cli root, not its parent .gemini.
type Source struct{ Dir string }

func (s *Source) dir() (string, error) {
	if s.Dir != "" {
		return s.Dir, nil
	}
	return session.AntigravityDir()
}

func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderAntigravity) {
		return nil, nil
	}
	if !session.ValidAntigravitySessionID(meta.ID) {
		return nil, nil
	}
	if meta.RolloutPath != "" {
		if id, ok := session.AntigravityTranscriptID(meta.RolloutPath); ok && id == meta.ID {
			if _, err := session.ReadAntigravityTranscript(meta.RolloutPath); err == nil {
				return []string{meta.RolloutPath}, nil
			}
		}
	}
	dir, err := s.dir()
	if err != nil {
		return nil, err
	}
	if path := session.FindAntigravityTranscript(dir, meta.ID); path != "" {
		if _, err := session.ReadAntigravityTranscript(path); err == nil {
			return []string{path}, nil
		}
	}
	return nil, nil
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > maxLineBytes {
			return nil, errors.New("antigravity: transcript record exceeds read limit")
		}
		line = append(line, part...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

// Read emits exactly one event per complete source line. Prior lines are parsed
// to recover model selection and line-index seq; incomplete tails wait for the
// next read. A returned offset is always a newline boundary.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset < 0 {
		return nil, 0, errors.New("antigravity: negative offset")
	}
	id, ok := session.AntigravityTranscriptID(path)
	if !ok {
		return nil, fromOffset, errors.New("antigravity: invalid transcript layout")
	}
	file, err := fsutil.Open(path)
	if err != nil {
		return nil, fromOffset, errors.New("antigravity: open transcript")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fromOffset, errors.New("antigravity: stat transcript")
	}
	if fromOffset > info.Size() {
		return nil, fromOffset, errors.New("antigravity: offset is past end; transcript was truncated")
	}
	r := bufio.NewReaderSize(file, 64<<10)
	var offset, seq int64
	model := ""
	var events []transcript.Event
	for {
		line, err := readLine(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if offset < fromOffset {
					return nil, fromOffset, errors.New("antigravity: offset is not a line boundary")
				}
				return events, offset, nil
			}
			return events, offset, errors.New("antigravity: read transcript failed or record exceeds limit")
		}
		end := offset + int64(len(line))
		if offset < fromOffset && end > fromOffset {
			return nil, fromOffset, errors.New("antigravity: offset is not a line boundary")
		}
		event := parse(bytes.TrimRight(line, "\r\n"), id, seq, &model)
		if offset >= fromOffset {
			events = append(events, event)
		}
		offset, seq = end, seq+1
	}
}

func parse(line []byte, id string, seq int64, model *string) transcript.Event {
	e := transcript.Event{SessionID: id, Provider: transcript.ProviderAntigravity,
		Seq: seq, Role: transcript.RoleMeta}
	if !json.Valid(line) {
		// JSON strings retain malformed source bytes without breaking Raw's
		// valid-JSON contract or printing the source in errors.
		e.Raw, _ = json.Marshal(string(line))
		return e
	}
	e.Raw = append(json.RawMessage(nil), line...)
	var r session.AntigravityRecord
	if json.Unmarshal(line, &r) != nil || r.StepIndex == nil || *r.StepIndex < 0 || r.Type == "" {
		return e
	}
	if ts, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err == nil {
		e.TS = &ts
	}
	content := ""
	if r.Content != nil {
		content = *r.Content
	}
	e.Text = content
	switch r.Type {
	case "USER_INPUT":
		e.Role = transcript.RoleUser
		e.Text = session.UnwrapAntigravityUserRequest(content)
		if selected := session.AntigravityModelFromUserInput(content); selected != "" {
			*model = selected
		}
	case "PLANNER_RESPONSE":
		e.Role = transcript.RoleAssistant
		if r.Content == nil {
			e.Text = r.Thinking
		}
		if r.InputTokens >= 0 && r.OutputTokens >= 0 && r.CacheReadTokens >= 0 {
			e.Tokens = transcript.Tokens{Input: r.InputTokens, Output: r.OutputTokens, Cache: r.CacheReadTokens}
		}
	case "RUN_COMMAND", "VIEW_FILE", "LIST_DIRECTORY", "CODE_ACTION", "SEARCH_WEB", "GREP_SEARCH", "GENERIC":
		e.Role = transcript.RoleToolResult
	}
	if r.Content != nil {
		for _, field := range r.TruncatedFields {
			if field == "content" && e.Text != "" {
				e.Text += "\n\n[content truncated by Antigravity CLI]"
				break
			}
		}
	}
	if e.Role == transcript.RoleToolResult && len(e.Text) > maxToolText {
		n := maxToolText
		for n > 0 && !utf8.RuneStart(e.Text[n]) {
			n--
		}
		e.Text = e.Text[:n] + fmt.Sprintf("\n[truncated %d bytes]", len(e.Text)-n)
	}
	if *model != "" {
		selected := *model
		e.Model = &selected
	}
	return e
}
