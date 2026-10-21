//go:build !unix

package cli

import "errors"

var errExecUnsupported = errors.New("exec is not supported on this platform")

// execReplace is unavailable here, so the caller runs the server as a child.
func execReplace(string, []string, []string) error { return errExecUnsupported }
