//go:build linux || darwin

package session

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/testenv"
)

// The scan needs BSD or Linux ps, hence the build constraint.
func TestDiscoverProcessesPreservesLongCopilotCommand(t *testing.T) {
	bash := testenv.NeedTool(t, "bash")
	t.Setenv("COLUMNS", "80")
	command := "node /opt/" + strings.Repeat("long-installation-path/", 12) + "@github/copilot/index.js --resume=" + testCopilotSessionID
	child := exec.Command(bash, "-c", `exec -a "$1" sleep 60`, "firekeeper-test", command)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	d := standalone()
	// Wait for bash to replace itself with the synthetic runtime.
	deadline := time.Now().Add(3 * time.Second)
	for {
		output, err := d.discoverProcesses()
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range d.parseProcesses(string(output)) {
			if process.PID == child.Process.Pid && process.Tool == "Copilot" && process.Command == command+" 60" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("process scan did not preserve the long Copilot command and session ID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
