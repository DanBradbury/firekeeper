package session

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// emulateLsof answers the lsof invocations discovery makes, for platforms
// without lsof. cwd reports a process's current directory.
//
// Two forms are used: `-a -d cwd -Fn -p <pids>`, which lists working
// directories, and `-Fn -p <pids>`, which lists every open file. Windows has
// no cheap way to list another process's open files, so the second form
// reports none; callers already treat an empty answer as "unknown" and fall
// back to command lines and working directories.
func emulateLsof(args []string, cwd func(pid int) string) []byte {
	var pids []int
	wantCWD := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d":
			if i+1 < len(args) && args[i+1] == "cwd" {
				wantCWD = true
			}
		case "-p":
			if i+1 < len(args) {
				i++
				for _, field := range strings.Split(args[i], ",") {
					if pid, err := strconv.Atoi(strings.TrimSpace(field)); err == nil && pid > 0 {
						pids = append(pids, pid)
					}
				}
			}
		}
	}
	if !wantCWD {
		return nil
	}
	var out bytes.Buffer
	for _, pid := range pids {
		dir := cwd(pid)
		if dir == "" {
			continue
		}
		fmt.Fprintf(&out, "p%d\nn%s\n", pid, strings.NewReplacer("\r", " ", "\n", " ").Replace(dir))
	}
	return out.Bytes()
}

// formatElapsed renders d the way ps prints etime: [[dd-]hh:]mm:ss.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int64(d / time.Second)
	days, hours, minutes, seconds := total/86400, total/3600%24, total/60%60, total%60
	switch {
	case days > 0:
		return fmt.Sprintf("%d-%02d:%02d:%02d", days, hours, minutes, seconds)
	case hours > 0:
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	default:
		return fmt.Sprintf("%02d:%02d", minutes, seconds)
	}
}

// psLine renders one process in the column layout parseProcesses reads:
// pid, ppid, tty, etime, command. Windows has no controlling TTY, which ps
// shows as "?".
func psLine(pid, ppid int, elapsed time.Duration, command string) string {
	command = strings.Join(strings.Fields(command), " ")
	return fmt.Sprintf("%d %d ? %s %s\n", pid, ppid, formatElapsed(elapsed), command)
}

// trimWindowsDir drops the trailing separator Windows keeps on a current
// directory, except on a drive root.
func trimWindowsDir(dir string) string {
	if len(dir) > 3 && strings.HasSuffix(dir, `\`) {
		return dir[:len(dir)-1]
	}
	return dir
}
