package session

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// lsof exits 1 with no output when none of the processes it was asked about
// can be listed (they exited, or may not be inspected). That is not a
// metadata failure; a missing lsof still is.
func TestOpenFileLookupsTreatLsofExitAsNothingOpen(t *testing.T) {
	exitErr := &exec.ExitError{}
	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"lsof exit status", exitErr, false},
		{"wrapped lsof exit status", errors.Join(errors.New("context"), exitErr), false},
		{"lsof missing", exec.ErrNotFound, true},
		{"timeout", context.DeadlineExceeded, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &discoverer{ctx: context.Background(), opts: Options{Home: t.TempDir(), Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "lsof" {
					t.Fatalf("unexpected command %s", name)
				}
				return nil, tt.err
			}}}

			rollouts, err := d.discoverOpenRollouts([]int{4242})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Codex lookup error = %v, want error %v", err, tt.wantErr)
			}
			if !tt.wantErr && len(rollouts) != 0 {
				t.Fatalf("Codex rollouts = %v, want none", rollouts)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "run lsof") {
				t.Fatalf("Codex error %q lacks context", err)
			}

			files, err := d.discoverOpenCopilotSessions([]int{4242})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Copilot lookup error = %v, want error %v", err, tt.wantErr)
			}
			if !tt.wantErr && len(files) != 0 {
				t.Fatalf("Copilot files = %v, want none", files)
			}
		})
	}
}
