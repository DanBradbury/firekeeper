//go:build !darwin && !linux

package session

import "errors"

// readProcessArgs has no argument list source here. On Windows the command
// line comes from the process listing (nativeCommand) and is split with
// CommandLineToArgvW's rules by fallbackArgs.
func readProcessArgs(pid int) ([]string, error) {
	return nil, errors.New("argument list not available")
}
