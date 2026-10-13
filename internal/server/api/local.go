package api

import "context"

// LocalStatus describes the machine a local-mode dashboard runs on and how
// its daemon reports, so the dashboard can say what to do next. It is only
// served to the built-in default account of a server listening on loopback.
type LocalStatus struct {
	// MachineID is this machine's id (~/.firekeeper/machine-id).
	MachineID string `json:"machine_id"`
	// Daemon is "running", "stopped" (installed, not running) or
	// "not_installed".
	Daemon string `json:"daemon"`
	// Foreground is true when the running daemon was started by hand, not
	// by the installed service.
	Foreground bool `json:"foreground,omitempty"`
	// Server is where the daemon sends sessions, as scheme://host[:port].
	Server string `json:"server"`
	// ReportsHere reports whether Server is this dashboard.
	ReportsHere bool `json:"reports_here"`
	// Providers are the providers the daemon uploads, when known.
	Providers []string `json:"providers"`
	// Detected are the providers found on this machine.
	Detected []string `json:"detected"`
	// Command is the command that makes the daemon report here, or empty
	// when it already does.
	Command string `json:"command,omitempty"`
}

// Daemon states in LocalStatus.
const (
	DaemonRunning      = "running"
	DaemonStopped      = "stopped"
	DaemonNotInstalled = "not_installed"
)

// WithLocal makes GET /v1/account include local, called per request, for
// the built-in default account. Use it only on a server that listens on
// loopback, since it describes the machine the server runs on.
func WithLocal(local func(context.Context) *LocalStatus) Option {
	return func(o *options) { o.local = local }
}
