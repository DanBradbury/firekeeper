// Package version holds the build information that release builds set at
// link time.
package version

import (
	"fmt"
	"runtime"
)

// Set at build time with
//
//	-ldflags "-X github.com/DanBradbury/firekeeper/internal/version.Version=...
//	-X github.com/DanBradbury/firekeeper/internal/version.Commit=...
//	-X github.com/DanBradbury/firekeeper/internal/version.Date=..."
//
// Plain go build and go run leave them at their defaults. The linker ignores
// an -X flag that names a variable that does not exist, so a wrong path fails
// silently and the binary reports "dev".
var (
	Version = "dev"
	Commit  = "dev"
	Date    = "dev"
)

// String is the one-line output of --version and the version command.
func String() string {
	return fmt.Sprintf("firekeeper %s (commit %s, built %s, %s/%s)", Version, Commit, Date, runtime.GOOS, runtime.GOARCH)
}
