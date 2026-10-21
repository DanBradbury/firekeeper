// Package redact scrubs likely secrets and private paths from transcript
// events before they leave a machine. Redaction is best effort: it catches
// common token shapes and configured paths, not every possible secret.
//
// This package is a stable public contract under semantic versioning. It is
// a thin entry point over Firekeeper's redaction engine, so a server that
// re-redacts on export applies exactly the rules the client applied on upload.
package redact

import (
	"github.com/DanBradbury/firekeeper/internal/redact"
	"github.com/DanBradbury/firekeeper/pkg/event"
)

type (
	// Options configures redaction: a home directory to rewrite to "~" and
	// absolute directories to replace with "[REDACTED:path]".
	Options = redact.Options
	// Counts records how many replacements were made per kind.
	Counts = redact.Counts
	// Kind names a class of scrubbed content, as in "[REDACTED:<kind>]".
	Kind = redact.Kind
)

// Event returns a copy of e with Text and every string in Raw redacted, plus
// the number of replacements per kind.
func Event(e event.Event, opts Options) (event.Event, Counts) {
	return redact.Event(e, opts)
}

// String redacts s and returns the result plus the number of replacements
// per kind.
func String(s string, opts Options) (string, Counts) {
	return redact.String(s, opts)
}
