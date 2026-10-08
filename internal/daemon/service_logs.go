package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// tailWindow bounds how much of the log Tail reads.
const tailWindow = 1 << 20

// Tail writes the last n lines of the log at path to w. A missing log is
// reported as os.ErrNotExist. It returns the offset just past what it
// wrote, for Follow.
func Tail(path string, n int, w io.Writer) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open daemon log: %w", cause(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("read daemon log: %w", cause(err))
	}
	size := info.Size()
	start := max(size-tailWindow, 0)
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("read daemon log: %w", cause(err))
	}
	if start > 0 {
		// Drop the partial first line.
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	if n <= 0 {
		return size, nil
	}
	// Count back n newlines, ignoring the one that ends the last line.
	end := len(buf)
	if end > 0 && buf[end-1] == '\n' {
		end--
	}
	cut := 0
	for i, seen := end-1, 0; i >= 0; i-- {
		if buf[i] == '\n' {
			seen++
			if seen == n {
				cut = i + 1
				break
			}
		}
	}
	if _, err := w.Write(buf[cut:]); err != nil {
		return 0, err
	}
	return size, nil
}

// Follow writes what is appended to the log at path after offset until ctx
// is cancelled, polling every interval. When the daemon rotates the log it
// finishes the old file and continues at the start of the new one.
func Follow(ctx context.Context, path string, offset int64, interval time.Duration, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", cause(err))
	}
	defer func() { f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("read daemon log: %w", cause(err))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		if rotated(f, path) {
			// Drain anything written to the old file before the rename.
			if _, err := io.Copy(w, f); err != nil {
				return err
			}
			next, err := os.Open(path)
			if err == nil {
				f.Close()
				f = next
				continue
			}
		} else if pos, err := f.Seek(0, io.SeekCurrent); err == nil {
			if info, err := f.Stat(); err == nil && info.Size() < pos {
				// Truncated in place: start over.
				f.Seek(0, io.SeekStart)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func rotated(f *os.File, path string) bool {
	open, err := f.Stat()
	if err != nil {
		return false
	}
	current, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !os.SameFile(open, current)
}
