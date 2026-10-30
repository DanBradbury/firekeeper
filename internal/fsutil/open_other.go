//go:build !windows

package fsutil

import "os"

// Open opens name for reading. Unix lets another process rename or delete a
// file that is open, so this is os.Open.
func Open(name string) (*os.File, error) { return os.Open(name) }
