package reader

import (
	"context"
	"os"
	"time"
)

// Info describes a transcript the way a file's size and modification time
// do. Size is in the units of Read offsets: bytes for JSONL sources.
type Info struct {
	Size    int64
	ModTime time.Time
}

// Stater is implemented by sources whose transcripts are not files of their
// own, such as one session among many in a shared SQLite database. Locate
// returns a path naming the session inside that file, and callers use Stat
// and Pending where they would otherwise stat the path and count its lines.
type Stater interface {
	// Stat returns the transcript's size in Read offset units, which is the
	// offset a Read to the end returns, and when it last changed. A
	// transcript that is gone returns an error matching os.ErrNotExist.
	Stat(path string) (Info, error)
	// Pending returns how many events a Read from offset would return and
	// roughly how many bytes they hold.
	Pending(ctx context.Context, path string, from int64) (events, bytes int64, err error)
}

// Stat describes the transcript at path, asking source when it is a Stater
// and the filesystem otherwise.
func Stat(source Source, path string) (Info, error) {
	if stater, ok := source.(Stater); ok {
		return stater.Stat(path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Info{}, err
	}
	return Info{Size: info.Size(), ModTime: info.ModTime()}, nil
}
