//go:build darwin

package session

import (
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

// readProcessArgs reads a process's argument list with the kern.procargs2
// sysctl. Only the argc arguments are read; the environment that follows
// them is never parsed or kept.
func readProcessArgs(pid int) ([]string, error) {
	block, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	return parseProcargs2(block)
}

// parseProcargs2 decodes a kern.procargs2 block: a native-endian int32
// argc, the executable path, NUL padding, then argc NUL-terminated
// arguments, then the environment, which is ignored.
func parseProcargs2(block []byte) ([]string, error) {
	if len(block) < 4 {
		return nil, errors.New("short procargs2 block")
	}
	argc := int(int32(binary.NativeEndian.Uint32(block[:4])))
	if argc <= 0 || argc > 1<<16 {
		return nil, errors.New("invalid procargs2 argc")
	}
	rest := block[4:]
	for len(rest) > 0 && rest[0] != 0 {
		rest = rest[1:] // the executable path
	}
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:] // padding
	}
	args := splitNULArgs(rest, argc)
	if len(args) != argc {
		return nil, errors.New("truncated procargs2 arguments")
	}
	return args, nil
}
