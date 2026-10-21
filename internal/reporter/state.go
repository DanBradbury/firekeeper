package reporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileState is the read position for one transcript file.
//
// Offset is where the next read starts and only moves once every event
// before it has been uploaded. LastSeq is the highest seq the server has
// confirmed, or -1 when none has been; it advances after each batch, so a
// run interrupted partway through a file resumes without re-sending the
// batches that already succeeded. Size is the file size when Offset was
// recorded; a smaller file means truncation or rotation.
type FileState struct {
	Offset  int64     `json:"offset"`
	Size    int64     `json:"size"`
	Mtime   time.Time `json:"mtime"`
	LastSeq int64     `json:"last_seq"`
}

// State is the contents of ~/.firekeeper/state.json.
type State struct {
	Files map[string]FileState `json:"files"`
}

// newFileState is the position of a file that has never been read.
func newFileState() FileState {
	return FileState{LastSeq: -1}
}

// LoadState reads path. A missing file is an empty state.
func LoadState(path string) (*State, error) {
	state := &State{Files: map[string]FileState{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read reporter state: %w", cause(err))
	}
	if err := json.Unmarshal(data, state); err != nil {
		return nil, errors.New("read reporter state: file is not valid JSON")
	}
	if state.Files == nil {
		state.Files = map[string]FileState{}
	}
	return state, nil
}

// Save writes the state atomically, so a process killed mid-write leaves
// the previous state intact.
func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("write reporter state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("write reporter state: %w", cause(err))
	}
	f, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("write reporter state: %w", cause(err))
	}
	tmp := f.Name()
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write reporter state: %w", cause(err))
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write reporter state: %w", cause(err))
	}
	return nil
}

// cause drops the path from filesystem errors so messages stay free of
// machine-specific locations.
func cause(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return err
}

// DefaultLockWait is how long a pass waits for state.lock before failing.
const DefaultLockWait = 30 * time.Second

// lockPollInterval is how often a waiting pass retries state.lock.
const lockPollInterval = 50 * time.Millisecond

// ErrStateLocked means another Firekeeper process held state.lock for the
// whole wait.
var ErrStateLocked = errors.New("another firekeeper process (report, daemon, or backfill) is holding state.lock; try again when it finishes")

// withStateLock runs fn while holding the lock file next to statePath,
// ~/.firekeeper/state.lock by default. Every pass that saves state.json takes
// it around each load-merge-save, so a daemon and a backfill running at once
// never overwrite each other's offsets. It waits up to wait for the lock.
func withStateLock(ctx context.Context, statePath string, wait time.Duration, fn func() error) error {
	dir := filepath.Dir(statePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("open state lock: %w", cause(err))
	}
	path := filepath.Join(dir, "state.lock")
	deadline := time.Now().Add(wait)
	for {
		unlock, ok, err := tryLock(path)
		if err != nil {
			return err
		}
		if ok {
			defer unlock()
			return fn()
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w (waited %s)", ErrStateLocked, wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(lockPollInterval, time.Until(deadline))):
		}
	}
}

// mergeFileState combines the position a pass wants to save, ours, with
// the one another pass saved since, disk. Offsets never move backwards:
// Offset and LastSeq each only grow, since each records a prefix the server
// has confirmed. The exception is a file ours restarted after truncation or
// rotation: when disk is the stale entry ours replaced, or records a file
// larger than the one now on disk, ours wins outright.
func mergeFileState(ours, disk FileState, diskKnown bool, replaced *FileState, currentSize int64) FileState {
	if !diskKnown {
		return ours
	}
	if replaced != nil && sameFileState(disk, *replaced) {
		return ours
	}
	if currentSize >= 0 && disk.Size > currentSize {
		return ours
	}
	merged := ours
	if disk.Offset > ours.Offset {
		merged.Offset, merged.Size, merged.Mtime = disk.Offset, disk.Size, disk.Mtime
	}
	merged.LastSeq = max(ours.LastSeq, disk.LastSeq)
	return merged
}

func sameFileState(a, b FileState) bool {
	return a.Offset == b.Offset && a.Size == b.Size && a.LastSeq == b.LastSeq && a.Mtime.Equal(b.Mtime)
}
