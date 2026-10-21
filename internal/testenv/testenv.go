// Package testenv holds helpers for tests that depend on the environment they
// run in. Locally, a test may skip when an optional tool is missing; in CI the
// same condition fails the test, so CI never passes by silently skipping.
package testenv

import (
	"os"
	"os/exec"
	"testing"
)

// InCI reports whether the tests run in CI. GitHub Actions sets CI=true.
func InCI() bool { return os.Getenv("CI") != "" }

// NeedTool returns the path of the executable name. When name is not on PATH
// it skips the test locally and fails it in CI.
func NeedTool(t testing.TB, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err == nil {
		return path
	}
	if InCI() {
		t.Fatalf("%s is required in CI: %v", name, err)
		return ""
	}
	t.Skipf("%s unavailable: %v", name, err)
	return ""
}
