package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// cloudProviders maps a cloud provider name to the backfill flag that
// imports it. Cloud sessions have no files on this machine, so `backfill
// --all` cannot see them; each provider is fetched from its own service.
var cloudProviders = map[string]string{
	"claude": "--claude-cloud",
}

func cloudProviderNames() string {
	names := make([]string, 0, len(cloudProviders))
	for name := range cloudProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// runBackfillCloud is `firekeeper backfill-cloud <provider> [backfill flags]`.
// It runs the normal backfill (plan, confirm, upload) over one provider's
// cloud sessions, so every backfill flag works the same way.
func runBackfillCloud(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		w := stderr
		code := 2
		if len(args) > 0 {
			w, code = stdout, 0
		}
		fmt.Fprintf(w, "Usage: firekeeper backfill-cloud <provider> [--dry-run] [--since D | --after DATE] [--limit N] [--yes] [--server URL]\n\nProviders: %s\n", cloudProviderNames())
		return code
	}
	flag, ok := cloudProviders[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "firekeeper backfill-cloud: unknown cloud provider %q (supported: %s)\n", args[0], cloudProviderNames())
		return 2
	}
	return runBackfill(append([]string{flag}, args[1:]...), stdout, stderr)
}
