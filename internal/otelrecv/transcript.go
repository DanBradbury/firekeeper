package otelrecv

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript/claude"
)

// SessionUsage is Firekeeper's view of one Claude Code session on disk.
// Transcript totals follow the transcript parser: each assistant message id
// counts once, across the main session file and its
// <session>/subagents/agent-*.jsonl files. Input excludes cache. ModelUsage
// is the latest cost-state snapshot on the main file only.
type SessionUsage struct {
	Found           bool
	Messages        int
	Input           int64
	Output          int64
	CacheRead       int64
	CacheWrite      int64
	ModelUsageFound bool
	ModelInput      int64
	ModelOutput     int64
	ModelCacheRead  int64
	ModelCacheWrite int64
	// SubagentFiles is how many agent-*.jsonl files were included.
	SubagentFiles int
	// RequestIDs are assistant-message request ids found in those files.
	RequestIDs map[string]bool
	// counted dedupes message ids across the main file and subagent files.
	counted map[string]bool
}

// Total is the transcript-derived token total.
func (s SessionUsage) Total() int64 {
	return s.Input + s.Output + s.CacheRead + s.CacheWrite
}

// ModelTotal is the latest cost-state modelUsage total.
func (s SessionUsage) ModelTotal() int64 {
	return s.ModelInput + s.ModelOutput + s.ModelCacheRead + s.ModelCacheWrite
}

// LoadTranscript reads the Claude Code transcript for sessionID.
// A session id that is not a UUID is not looked up. configDir overrides
// CLAUDE_CONFIG_DIR when set. Prompt text is not retained.
func LoadTranscript(sessionID, configDir string) (SessionUsage, error) {
	if !session.ValidUUID(sessionID) {
		return SessionUsage{}, nil
	}
	paths, err := (&claude.Source{ConfigDir: configDir}).Locate(session.Meta{ID: sessionID, Provider: "claude"})
	if err != nil {
		return SessionUsage{}, errors.New("transcript unreadable")
	}
	if len(paths) == 0 {
		return SessionUsage{}, nil
	}
	var path string
	var newest time.Time
	for _, candidate := range paths {
		info, statErr := os.Stat(candidate)
		if statErr != nil {
			continue
		}
		if path == "" || info.ModTime().After(newest) {
			newest = info.ModTime()
			path = candidate
		}
	}
	if path == "" {
		return SessionUsage{}, errors.New("transcript unreadable")
	}
	usage, err := scanTranscript(path)
	if err != nil {
		return SessionUsage{}, errors.New("transcript unreadable")
	}
	subs := subagentTranscripts(path)
	for _, sub := range subs {
		if err := scanTranscriptInto(sub, &usage, false); err != nil {
			return SessionUsage{}, errors.New("transcript unreadable")
		}
	}
	usage.SubagentFiles = len(subs)
	usage.Found = true
	return usage, nil
}

// subagentTranscripts lists <session>/subagents/agent-*.jsonl next to the
// main transcript. It does not read file contents.
func subagentTranscripts(main string) []string {
	dir := strings.TrimSuffix(main, ".jsonl")
	if dir == main {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, "subagents", "agent-*.jsonl"))
	if err != nil {
		return nil
	}
	var out []string
	for _, match := range matches {
		info, err := os.Stat(match)
		if err == nil && info.Mode().IsRegular() {
			out = append(out, match)
		}
	}
	return out
}

func scanTranscript(path string) (SessionUsage, error) {
	var usage SessionUsage
	if err := scanTranscriptInto(path, &usage, true); err != nil {
		return SessionUsage{}, err
	}
	return usage, nil
}

// scanTranscriptInto adds one file to usage. countModel records cost-state
// only for the main session file. Message ids are deduped across files.
func scanTranscriptInto(path string, usage *SessionUsage, countModel bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 256<<10)
	if usage.RequestIDs == nil {
		usage.RequestIDs = map[string]bool{}
	}
	if usage.counted == nil {
		usage.counted = map[string]bool{}
	}
	var modelFound bool
	var model SessionUsage
	for {
		line, readErr := readCappedLine(reader, maxBody)
		if len(line) > 0 {
			trackTranscriptLine(line, usage, &model, &modelFound)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if countModel && modelFound {
		usage.ModelUsageFound = true
		usage.ModelInput = model.ModelInput
		usage.ModelOutput = model.ModelOutput
		usage.ModelCacheRead = model.ModelCacheRead
		usage.ModelCacheWrite = model.ModelCacheWrite
	}
	return nil
}

type transcriptLine struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	Message   *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ModelUsage map[string]struct {
		Input         int64 `json:"inputTokens"`
		Output        int64 `json:"outputTokens"`
		CacheRead     int64 `json:"cacheReadInputTokens"`
		CacheCreation int64 `json:"cacheCreationInputTokens"`
	} `json:"modelUsage"`
}

func trackTranscriptLine(line []byte, usage *SessionUsage, model *SessionUsage, modelFound *bool) {
	if !bytes.Contains(line, []byte(`"usage"`)) && !bytes.Contains(line, []byte(`"modelUsage"`)) {
		return
	}
	var rec transcriptLine
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	switch rec.Type {
	case "assistant":
		if rec.Message == nil || rec.Message.Usage == nil {
			return
		}
		if rec.Message.ID != "" {
			if usage.counted == nil {
				usage.counted = map[string]bool{}
			}
			if usage.counted[rec.Message.ID] {
				return
			}
			usage.counted[rec.Message.ID] = true
		}
		u := rec.Message.Usage
		input, output := nonNeg(u.Input), nonNeg(u.Output)
		cacheRead, cacheWrite := nonNeg(u.CacheRead), nonNeg(u.CacheCreation)
		usage.Messages++
		usage.Input += input
		usage.Output += output
		usage.CacheRead += cacheRead
		usage.CacheWrite += cacheWrite
		if rec.RequestID != "" && usage.RequestIDs != nil {
			usage.RequestIDs[rec.RequestID] = true
		}
	case "cost-state":
		if len(rec.ModelUsage) == 0 {
			return
		}
		var next SessionUsage
		for _, u := range rec.ModelUsage {
			next.ModelInput += nonNeg(u.Input)
			next.ModelOutput += nonNeg(u.Output)
			next.ModelCacheRead += nonNeg(u.CacheRead)
			next.ModelCacheWrite += nonNeg(u.CacheCreation)
		}
		*model = next
		*modelFound = true
	}
}

// readCappedLine returns the next line, or nil when the line exceeds max.
// A too-long line is skipped so a large tool result does not stop the scan
// and is not kept in memory past max.
func readCappedLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > max {
			if errors.Is(err, bufio.ErrBufferFull) {
				for {
					_, err = r.ReadSlice('\n')
					if !errors.Is(err, bufio.ErrBufferFull) {
						break
					}
				}
			}
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
				return nil, err
			}
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}
			return nil, nil
		}
		buf = append(buf, chunk...)
		if err == nil || errors.Is(err, io.EOF) {
			return buf, err
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}
