package daemon

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// rotatingFile appends to path. A write that would take the file past max
// bytes first renames it to path.1, shifting older files up to path.<keep>
// and deleting the oldest.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func openLog(path string, max int64, keep int) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: max, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", cause(err))
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("open daemon log: %w", cause(err))
	}
	r.f, r.size = f, info.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil && r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	if r.f == nil {
		// A failed rotation left no file open; try again.
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() error {
	r.f.Close()
	r.f = nil
	backup := func(i int) string { return fmt.Sprintf("%s.%d", r.path, i) }
	if err := os.Remove(backup(r.keep)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rotate daemon log: %w", cause(err))
	}
	for i := r.keep - 1; i >= 1; i-- {
		if err := os.Rename(backup(i), backup(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotate daemon log: %w", cause(err))
		}
	}
	if err := os.Rename(r.path, backup(1)); err != nil {
		return fmt.Errorf("rotate daemon log: %w", cause(err))
	}
	return r.open()
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
