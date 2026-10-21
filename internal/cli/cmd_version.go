package cli

import (
	"fmt"
	"io"

	"github.com/DanBradbury/firekeeper/internal/version"
)

func runVersion(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "firekeeper version: unexpected argument %q\n", args[0])
		return 2
	}
	fmt.Fprintln(stdout, version.String())
	return 0
}
