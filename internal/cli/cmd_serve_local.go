package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/daemon"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/server"
	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/version"
)

// serveStdin and serveInteractive are replaced in tests so the local-mode
// prompt never reads the real terminal.
var (
	serveStdin       io.Reader = os.Stdin
	serveInteractive           = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

// localStatusTTL is how long one look at the daemon is reused. The page asks
// on every load, and each look may run launchctl or systemctl.
const localStatusTTL = 5 * time.Second

// configureLocal fills in the hooks server.Run uses in local mode: this
// machine's identity, the status of its daemon, and the offer to start
// reporting to the dashboard. Run ignores them unless the server starts
// with no account or token on a loopback address.
func configureLocal(cfg *server.Config, c config.Config, stdout, stderr io.Writer) {
	if home, err := configHome(); err == nil {
		if id, err := session.MachineID(home); err == nil {
			hostname, _ := os.Hostname()
			cfg.LocalMachine = &store.Machine{ID: id, Name: hostname, Hostname: hostname, OS: runtime.GOOS, Version: version.Version}
		} else {
			fmt.Fprintf(stderr, "firekeeper serve: this machine will not be listed until it reports: %v\n", err)
		}
	}
	probe := &localProbe{cfg: c}
	cfg.Local = probe.status
	cfg.OnLocal = func(dashboardURL string) {
		if !serveInteractive() {
			return
		}
		go offerLocalReporting(context.Background(), probe, dashboardURL, serveStdin, stdout, stderr)
	}
}

// localProbe builds api.LocalStatus, caching it for localStatusTTL.
type localProbe struct {
	cfg config.Config

	mu   sync.Mutex
	at   time.Time
	url  string
	last *api.LocalStatus
	// machineID is read once; it does not change while serve runs.
	machineID string
}

func (p *localProbe) status(ctx context.Context, dashboardURL string) *api.LocalStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last != nil && p.url == dashboardURL && time.Since(p.at) < localStatusTTL {
		return p.last
	}
	if p.machineID == "" {
		if home, err := configHome(); err == nil {
			p.machineID, _ = session.MachineID(home)
		}
	}
	var rep daemon.Reporting
	if svc, err := newService(nil, io.Discard); err == nil {
		rep = svc.Inspect(ctx, p.cfg.Server, p.cfg.Providers)
	} else {
		rep = daemon.Reporting{Server: p.cfg.Server, Providers: p.cfg.Providers}
	}
	detected, _ := detectDaemonProviders()
	st := buildLocalStatus(rep, detected, p.cfg.Server, dashboardURL)
	st.MachineID = p.machineID
	p.last, p.at, p.url = st, time.Now(), dashboardURL
	return st
}

// buildLocalStatus says how the daemon reports compared with the dashboard
// at dashboardURL, and what command would make it report there.
// configServer is the server the config file and environment set.
func buildLocalStatus(rep daemon.Reporting, detected []transcript.Provider, configServer, dashboardURL string) *api.LocalStatus {
	st := &api.LocalStatus{
		Daemon:      api.DaemonNotInstalled,
		Server:      displayServer(rep.Server),
		ReportsHere: sameServer(rep.Server, dashboardURL),
		Providers:   providerNames(rep.Providers),
		Detected:    providerNames(detected),
	}
	switch {
	case rep.Running:
		st.Daemon, st.Foreground = api.DaemonRunning, rep.Foreground
	case rep.Installed:
		st.Daemon = api.DaemonStopped
	}
	if st.Daemon == api.DaemonRunning && st.ReportsHere {
		return st
	}
	cmd := "firekeeper daemon install"
	// install falls back to the configured server, so name this one unless
	// that already is it.
	if configServer == "" {
		configServer = reporter.DefaultServer
	}
	if !sameServer(configServer, dashboardURL) {
		cmd += " --server " + strings.TrimSuffix(dashboardURL, "/")
	}
	st.Command = cmd
	return st
}

func providerNames(ps []transcript.Provider) []string {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = string(p)
	}
	return names
}

// displayServer shortens a server URL to scheme://host[:port], dropping any
// user info, path or query.
func displayServer(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// sameServer reports whether two server URLs reach the same listener on
// this machine: the same scheme and port, and the same host or two
// loopback names such as localhost and 127.0.0.1.
func sameServer(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil || ua.Host == "" || ub.Host == "" || !strings.EqualFold(ua.Scheme, ub.Scheme) {
		return false
	}
	if serverPort(ua) != serverPort(ub) {
		return false
	}
	ha, hb := strings.ToLower(ua.Hostname()), strings.ToLower(ub.Hostname())
	return ha == hb || (loopbackHost(ha) && loopbackHost(hb))
}

func serverPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// offerLocalReporting asks once, on the terminal serve runs in, whether to
// install the daemon so this machine reports to the dashboard. Uploading
// stays opt-in: nothing is installed without a yes.
func offerLocalReporting(ctx context.Context, probe *localProbe, dashboardURL string, in io.Reader, stdout, stderr io.Writer) {
	if !daemon.Supported(runtime.GOOS) {
		return
	}
	st := probe.status(ctx, dashboardURL)
	if st.Command == "" {
		fmt.Fprintf(stdout, "this machine reports %s to this dashboard\n", humanList(st.Providers))
		return
	}
	// Switching an existing daemon keeps its providers; a new one gets
	// what is installed here.
	providers := st.Providers
	if len(providers) == 0 || st.Daemon == api.DaemonNotInstalled {
		providers = st.Detected
	}
	if len(providers) == 0 {
		fmt.Fprintln(stdout, "no Codex, Copilot CLI, Kimi Code, Claude Code, OpenCode or Gemini CLI data found on this machine, so there is nothing to report yet")
		return
	}

	var question string
	switch {
	case st.Foreground:
		// A daemon started in a terminal holds the lock; a service would
		// only fail to start beside it.
		fmt.Fprintf(stdout, "a firekeeper daemon is running in a terminal and reports to %s; stop it and run: %s\n", st.Server, st.Command)
		return
	case st.Daemon == api.DaemonNotInstalled:
		question = fmt.Sprintf("Report %s sessions from this machine to this dashboard?\n"+
			"This installs the firekeeper daemon as a login service, which uploads their transcripts here.", humanList(providers))
	case st.ReportsHere:
		question = fmt.Sprintf("The firekeeper daemon is installed but not running. Start it to report %s here?", humanList(providers))
	case st.Daemon == api.DaemonStopped:
		question = fmt.Sprintf("The firekeeper daemon is installed but not running, and is set to report to %s.\n"+
			"Start it to report %s here instead?", st.Server, humanList(providers))
	default:
		question = fmt.Sprintf("The firekeeper daemon reports to %s, not this dashboard.\n"+
			"Switch it to report %s here instead?", st.Server, humanList(providers))
	}
	fmt.Fprintf(stdout, "\n%s [y/N] ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		fmt.Fprintf(stdout, "not reporting; run %s whenever you want to start\n", st.Command)
		return
	}
	args := []string{"--server", strings.TrimSuffix(dashboardURL, "/")}
	for _, p := range providers {
		args = append(args, "--provider", p)
	}
	if runDaemonInstall(args, stdout, stderr) == 0 {
		fmt.Fprintln(stdout, "this machine now reports to the dashboard; it shows as online after the first heartbeat")
	}
	probe.forget()
}

func (p *localProbe) forget() {
	p.mu.Lock()
	p.last = nil
	p.mu.Unlock()
}

// humanList names providers for a sentence: "Codex, Copilot CLI and
// Claude Code".
func humanList(names []string) string {
	pretty := make([]string, len(names))
	for i, n := range names {
		switch transcript.Provider(n) {
		case transcript.ProviderCodex:
			pretty[i] = "Codex"
		case transcript.ProviderCopilot:
			pretty[i] = "Copilot CLI"
		case transcript.ProviderKimi:
			pretty[i] = "Kimi Code"
		case transcript.ProviderClaude:
			pretty[i] = "Claude Code"
		case transcript.ProviderOpenCode:
			pretty[i] = "OpenCode"
		case transcript.ProviderGemini:
			pretty[i] = "Gemini CLI"
		default:
			pretty[i] = n
		}
	}
	switch len(pretty) {
	case 0:
		return "no providers"
	case 1:
		return pretty[0]
	}
	return strings.Join(pretty[:len(pretty)-1], ", ") + " and " + pretty[len(pretty)-1]
}
