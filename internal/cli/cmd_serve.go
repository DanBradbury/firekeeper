package cli

import (
	"fmt"
	"io"
)

// runServe is a stub. The dashboard server is not part of this program: it
// ships as the separate firekeeper-server binary.
func runServe(_ []string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "firekeeper serve: the dashboard server is a separate program, firekeeper-server, and is not part of this binary.")
	return 2
}
