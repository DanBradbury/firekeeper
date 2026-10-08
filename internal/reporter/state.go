package reporter

import (
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
