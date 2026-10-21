//go:build unix

package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The stand-in executable below is a shell script, hence the unix build
// constraint.

// recorder captures Skipf and Fatalf instead of stopping the test, so
// NeedTool's decisions can be checked directly.
type recorder struct {
	testing.TB
	skipped, failed string
}

func (r *recorder) Helper()                          {}
func (r *recorder) Skipf(format string, args ...any) { r.skipped = fmt.Sprintf(format, args...) }
func (r *recorder) Fatalf(format string, args ...any) {
	r.failed = fmt.Sprintf(format, args...)
}

func TestNeedTool(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "firekeeper-test-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	tests := []struct {
		name, tool, ci       string
		wantPath             string
		wantSkip, wantFailed bool
	}{
		{name: "found", tool: "firekeeper-test-tool", wantPath: tool},
		{name: "found in CI", tool: "firekeeper-test-tool", ci: "true", wantPath: tool},
		{name: "missing locally skips", tool: "firekeeper-missing-tool", wantSkip: true},
		{name: "missing in CI fails", tool: "firekeeper-missing-tool", ci: "true", wantFailed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CI", tt.ci)
			r := &recorder{}
			got := NeedTool(r, tt.tool)
			if got != tt.wantPath {
				t.Errorf("path = %q, want %q", got, tt.wantPath)
			}
			if (r.skipped != "") != tt.wantSkip {
				t.Errorf("skipped = %q, want skip %v", r.skipped, tt.wantSkip)
			}
			if (r.failed != "") != tt.wantFailed {
				t.Errorf("failed = %q, want failure %v", r.failed, tt.wantFailed)
			}
		})
	}
}
