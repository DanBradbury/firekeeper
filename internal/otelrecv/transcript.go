package otelrecv

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript/claude"
)

// SessionUsage is Firekeeper's view of one Claude Code session on disk.
// Transcript totals follow the transcript parser: each assistant message id
// counts once, including sidechain lines in that file, and input excludes
// cache. ModelUsage is the latest cost-state snapshot, which Claude writes
// separately and which can include calls the transcript does not.
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
	usage.Found = true
	return usage, nil
}

func scanTranscript(path string) (SessionUsage, error) {
	file, err := os.Open(path)
	if err != nil {
		return SessionUsage{}, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 256<<10)
	seen := map[string]bool{}
	var usage SessionUsage
	var modelFound bool
	var model SessionUsage
	for {
		line, err := readCappedLine(reader, maxBody)
		if len(line) > 0 {
			trackTranscriptLine(line, &usage, &model, &modelFound, seen)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return SessionUsage{}, err
		}
	}
	if modelFound {
		usage.ModelUsageFound = true
		usage.ModelInput = model.ModelInput
		usage.ModelOutput = model.ModelOutput
		usage.ModelCacheRead = model.ModelCacheRead
		usage.ModelCacheWrite = model.ModelCacheWrite
	}
	return usage, nil
}

type transcriptLine struct {
	Type    string `json:"type"`
	Message *struct {
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

func trackTranscriptLine(line []byte, usage *SessionUsage, model *SessionUsage, modelFound *bool, seen map[string]bool) {
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
			if seen[rec.Message.ID] {
				return
			}
			seen[rec.Message.ID] = true
		}
		u := rec.Message.Usage
		input, output := nonNeg(u.Input), nonNeg(u.Output)
		cacheRead, cacheWrite := nonNeg(u.CacheRead), nonNeg(u.CacheCreation)
		usage.Messages++
		usage.Input += input
		usage.Output += output
		usage.CacheRead += cacheRead
		usage.CacheWrite += cacheWrite
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
