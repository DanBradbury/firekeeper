//go:build linux

package session

import (
	"errors"
	"os"
	"strconv"
)

// readProcessArgs reads a process's argument list from /proc/<pid>/cmdline.
func readProcessArgs(pid int) ([]string, error) {
	block, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	args := splitNULArgs(block, -1)
	if len(args) == 0 {
		return nil, errors.New("empty cmdline")
	}
	return args, nil
}
