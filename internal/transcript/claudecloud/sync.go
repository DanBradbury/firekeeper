package claudecloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// cacheSchema is the layout version of cached metadata. Raising it makes the
// next Sync download every session again. 1 added Repo.
const cacheSchema = 1

// SyncOptions configures one Sync.
type SyncOptions struct {
	// Dir is the cache directory, owned by Firekeeper.
	Dir string
	// After skips sessions last updated at or before it. Zero keeps all.
	After time.Time
	// Progress receives one line per session fetched. Nil discards them.
	Progress io.Writer
}

// SyncResult counts what one Sync did. It holds no transcript data.
type SyncResult struct {
	// Listed is the number of sessions the API reported.
	Listed int
	// Fetched sessions had their events downloaded; Current were already
	// cached at the same update time; Skipped fell before After.
	Fetched, Current, Skipped int
	// Failed holds one message per session that could not be cached. The
	// rest of the pass still ran.
	Failed []string
}

// Sync caches every cloud session newer than opts.After as JSONL files in
// opts.Dir: <id>.jsonl holds the user and assistant events, <id>.json the
// metadata. A session is downloaded again only when the API reports a newer
// update time, and its file is rewritten with the same leading lines, so
// offsets a reporter saved stay valid and a rerun uploads only what is new.
func Sync(ctx context.Context, c *Client, opts SyncOptions) (SyncResult, error) {
	var res SyncResult
	if opts.Dir == "" {
		return res, errors.New("claudecloud: no cache directory")
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return res, errors.New("claudecloud: create cache directory")
	}
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return res, err
	}
	res.Listed = len(sessions)
	for _, info := range sessions {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if !opts.After.IsZero() && !info.UpdatedAt.After(opts.After) {
			res.Skipped++
			continue
		}
		if cached, ok := readCached(opts.Dir, info.ID); ok && cached.UpdatedAt.Equal(info.UpdatedAt) {
			res.Current++
			continue
		}
		events, err := c.Events(ctx, info.ID)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", info.ID, err))
			continue
		}
		lines := transcriptLines(events)
		info.Events = len(lines)
		info.Schema = cacheSchema
		if err := writeCached(opts.Dir, info, lines); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", info.ID, err))
			continue
		}
		res.Fetched++
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "fetched cloud session %s (%d events)\n", info.ID, info.Events)
		}
	}
	return res, nil
}

// transcriptLines converts API events into Claude Code transcript lines.
// Only user and assistant events are conversation; the rest is session
// plumbing (control messages, rate limits, environment logs) and is dropped.
// Each line is the event as received, with `timestamp` filled from
// `created_at` when absent (prompts arrive without one) and sub-agent
// messages marked `isSidechain`. Key order is the encoder's, so the same
// event always produces the same bytes.
func transcriptLines(events []Event) [][]byte {
	var lines [][]byte
	for _, e := range events {
		if e.Type != "user" && e.Type != "assistant" {
			continue
		}
		var rec map[string]json.RawMessage
		if json.Unmarshal(e.Raw, &rec) != nil {
			continue
		}
		if !hasString(rec["timestamp"]) && e.CreatedAt != "" {
			ts, _ := json.Marshal(e.CreatedAt)
			rec["timestamp"] = ts
		}
		if raw, ok := rec["parent_tool_use_id"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && len(raw) > 0 {
			rec["isSidechain"] = json.RawMessage("true")
		}
		line, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func hasString(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && s != ""
}

func transcriptPath(dir, id string) string { return filepath.Join(dir, id+".jsonl") }
func metaPath(dir, id string) string       { return filepath.Join(dir, id+".json") }

func readCached(dir, id string) (SessionInfo, bool) {
	if !safeID(id) {
		return SessionInfo{}, false
	}
	data, err := os.ReadFile(metaPath(dir, id))
	if err != nil {
		return SessionInfo{}, false
	}
	var info SessionInfo
	if json.Unmarshal(data, &info) != nil || info.ID != id || info.Schema != cacheSchema {
		return SessionInfo{}, false
	}
	if _, err := os.Stat(transcriptPath(dir, id)); err != nil {
		return SessionInfo{}, false
	}
	return info, true
}

// writeCached writes the transcript, then the metadata that marks it
// complete, each through a rename so a crash never leaves a torn file.
func writeCached(dir string, info SessionInfo, lines [][]byte) error {
	var body bytes.Buffer
	for _, line := range lines {
		body.Write(line)
		body.WriteByte('\n')
	}
	if err := writeAtomic(transcriptPath(dir, info.ID), body.Bytes(), info.UpdatedAt); err != nil {
		return err
	}
	meta, err := json.Marshal(info)
	if err != nil {
		return errors.New("encode cache metadata")
	}
	return writeAtomic(metaPath(dir, info.ID), meta, info.UpdatedAt)
}

func writeAtomic(path string, data []byte, mtime time.Time) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return errors.New("write cache file")
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := os.Chmod(name, 0o600); err != nil {
		tmp.Close()
		return errors.New("write cache file")
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return errors.New("write cache file")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("write cache file")
	}
	if !mtime.IsZero() {
		_ = os.Chtimes(name, mtime, mtime)
	}
	if err := os.Rename(name, path); err != nil {
		return errors.New("write cache file")
	}
	return nil
}
