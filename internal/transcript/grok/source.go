// Package grok reads the official xAI Grok Build append-only updates.jsonl
// store. Rewritten chat_history.jsonl caches are deliberately never read.
package grok

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/fsutil"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

func init() { reader.Register(transcript.ProviderGrok, &Source{}) }

// Source's zero value honors GROK_HOME, falling back to ~/.grok.
type Source struct {
	Home string
}

func (s *Source) home() (string, error) {
	if s.Home != "" {
		return s.Home, nil
	}
	return session.GrokHome()
}

func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderGrok) {
		return nil, nil
	}
	if !session.ValidUUID(meta.ID) {
		return nil, nil
	}
	if strings.EqualFold(session.GrokTranscriptID(meta.RolloutPath), meta.ID) {
		if info, err := os.Stat(meta.RolloutPath); err == nil && info.Mode().IsRegular() {
			return []string{meta.RolloutPath}, nil
		}
	}
	home, err := s.home()
	if err != nil {
		return nil, err
	}
	if path := session.FindGrokTranscript(home, meta.ID); path != "" {
		return []string{path}, nil
	}
	return nil, nil
}

// Read replays earlier lines only for tool/model state. It emits exactly one
// event per complete source line, preserving append-only line-index seqs.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	if fromOffset < 0 {
		return nil, 0, errors.New("grok: negative offset")
	}
	if session.GrokTranscriptID(path) == "" {
		return nil, fromOffset, errors.New("grok: invalid primary transcript layout")
	}
	f, err := fsutil.Open(path)
	if err != nil {
		return nil, fromOffset, fmt.Errorf("grok: open transcript: %w", cause(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, fromOffset, fmt.Errorf("grok: stat transcript: %w", cause(err))
	}
	if fromOffset > info.Size() {
		return nil, fromOffset, errors.New("grok: offset past end of transcript; file was truncated or rotated")
	}
	p := parser{sessionID: session.GrokTranscriptID(path), tools: map[string]string{}}
	r := bufio.NewReaderSize(f, 256<<10)
	var offset int64
	for offset < fromOffset {
		line, err := readLine(r)
		offset += int64(len(line))
		if err != nil || offset > fromOffset {
			return nil, fromOffset, errors.New("grok: offset is not at a line boundary")
		}
		p.parse(bytes.TrimRight(line, "\r\n"))
		p.seq++
	}
	var events []transcript.Event
	for {
		line, err := readLine(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return events, offset, nil
			}
			return events, offset, fmt.Errorf("grok: read transcript: %w", cause(err))
		}
		events = append(events, p.parse(bytes.TrimRight(line, "\r\n")))
		p.seq++
		offset += int64(len(line))
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	const maxRecordBytes = 16 << 20
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > maxRecordBytes {
			return nil, errors.New("grok: transcript record exceeds read limit")
		}
		line = append(line, part...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func cause(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func rawJSON(line []byte) json.RawMessage {
	if json.Valid(line) {
		return bytes.Clone(line)
	}
	raw, _ := json.Marshal(string(line))
	return raw
}
