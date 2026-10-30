//go:build !windows

package session

// nativeCommand answers a command in-process where the platform has no
// equivalent tool. Everything but Windows has ps and lsof, so it never does.
func nativeCommand(name string, args []string) (output []byte, handled bool, err error) {
	return nil, false, nil
}
