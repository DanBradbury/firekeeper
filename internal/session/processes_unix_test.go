//go:build linux || darwin

package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/pkg/event"
)

// The scan needs BSD or Linux ps, hence the build constraint. A stand-in
// Copilot process in a long installation path keeps its whole command line
// and session id through ps and the real argument list.
func TestDiscoverProcessesPreservesLongCopilotCommand(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	dir := filepath.Join(t.TempDir(), strings.Repeat("long-installation-path/", 12), "bin")
	child := startHelper(t, dir, "copilot", "--resume="+testCopilotSessionID)
	want := filepath.Join(dir, "copilot") + " --resume=" + testCopilotSessionID
	d := standalone()
	deadline := time.Now().Add(3 * time.Second)
	for {
		output, err := d.discoverProcesses()
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range d.parseProcesses(string(output)) {
			if process.PID == child.Process.Pid && process.Provider == event.ProviderCopilot && process.Command == want {
				if id, ok := (copilotAdapter{}).SessionID(process.Invocation); !ok || id != testCopilotSessionID {
					t.Fatalf("session id = %q, %v", id, ok)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("process scan did not preserve the long Copilot command and session ID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
