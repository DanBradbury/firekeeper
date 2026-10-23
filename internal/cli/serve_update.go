package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/serverinstall"
	"github.com/DanBradbury/firekeeper/internal/update"
)

const (
	// noUpdateCheckEnv is the same switch as for the client's own update
	// check: it also silences this one.
	noUpdateCheckEnv = "FIREKEEPER_NO_UPDATE_CHECK"

	// updateLookupTimeout bounds the one request that asks for the latest
	// server release, so an offline launch is not held up.
	updateLookupTimeout = 2 * time.Second

	// statusNotCovered is the status the server's `license` command reports
	// for a build newer than the key's update period.
	statusNotCovered = "not covered by this build"
)

// serverUpdateState is remembered between runs, in
// ~/.firekeeper/server-update.json.
type serverUpdateState struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
	Skipped   string    `json:"skipped"`
}

func serverUpdateStatePath(home string) string {
	return filepath.Join(home, ".firekeeper", "server-update.json")
}

func loadServerUpdateState(home string) serverUpdateState {
	var s serverUpdateState
	if b, err := os.ReadFile(serverUpdateStatePath(home)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveServerUpdateState(home string, s serverUpdateState) {
	dir := filepath.Join(home, ".firekeeper")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	if b, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(serverUpdateStatePath(home), b, 0o600)
	}
}

// startsDashboard reports whether `serve args...` is about to start the web
// server, as opposed to printing help or running an operator command.
func startsDashboard(args []string) bool {
	if len(args) > 0 {
		switch args[0] {
		case "token", "account", "admin", "invite", "prices":
			return false
		}
	}
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--help" {
			return false
		}
	}
	return true
}

// offerServerUpdate is called when `firekeeper serve` has found a server. If a
// newer release of firekeeper-server exists, it asks whether to install it,
// the way the client asks about its own updates, and returns the path to run.
// It returns path unchanged when nothing is offered, the user declines, or the
// update fails.
//
// It is off whenever the install offer is off (no release source), when the
// session is not interactive, when CI, FIREKEEPER_NO_PROMPT or
// FIREKEEPER_NO_UPDATE_CHECK is set, and when FIREKEEPER_SERVER_BIN names the
// server explicitly. It sends one unauthenticated GET for the latest release
// and nothing about the machine, the user or any session, and downloads
// nothing without a "1" at the prompt.
func offerServerUpdate(path string, stderr io.Writer) string {
	l := defaultLookup
	if l.getenv(ServerBinEnv) != "" || l.getenv("CI") != "" || l.getenv(NoPromptEnv) != "" ||
		l.getenv(noUpdateCheckEnv) != "" || !installOffer.interactive() {
		return path
	}
	src, enabled, err := serverinstall.Resolve(l.getenv)
	if !enabled || err != nil {
		return path
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	current := installedServerVersion(ctx, path)
	if current == "" || current == "dev" {
		return path
	}

	home, _ := installOffer.userHome()
	state := loadServerUpdateState(home)
	lookupCtx, cancel := context.WithTimeout(ctx, updateLookupTimeout)
	tag, lookupErr := installOffer.newInstaller(src, io.Discard).Latest(lookupCtx)
	cancel()
	if home != "" {
		state.CheckedAt = time.Now()
		if lookupErr == nil {
			state.Latest = tag
		}
		saveServerUpdateState(home, state)
	}
	latest := strings.TrimPrefix(state.Latest, "v")
	if latest == "" || state.Latest == state.Skipped || !update.Newer(latest, current) {
		return path
	}

	fmt.Fprintf(stderr, "\nfirekeeper-server update available: %s -> %s\n", current, latest)
	fmt.Fprintln(stderr, "  1) Update now")
	fmt.Fprintln(stderr, "  2) Not now")
	fmt.Fprintln(stderr, "  3) Skip this version")
	fmt.Fprint(stderr, "Choose [2]: ")
	switch readChoice(installOffer.stdin) {
	case "1", "y", "yes":
	case "3", "s", "skip":
		state.Skipped = state.Latest
		saveServerUpdateState(home, state)
		fmt.Fprintln(stderr)
		return path
	default:
		fmt.Fprintln(stderr)
		return path
	}

	if serverinstall.IsHomebrew(real) {
		return updateWithBrew(path, stderr)
	}
	return updateInPlace(ctx, src, path, real, current, stderr)
}

// updateWithBrew upgrades a Homebrew-managed server through brew.
func updateWithBrew(path string, stderr io.Writer) string {
	brew, err := defaultLookup.lookPath("brew")
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: brew was not found. Run `brew upgrade %s` yourself. Continuing with the current version.\n", brewFormula)
		return path
	}
	if err := installOffer.runBrew(brew, "upgrade", stderr); err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: brew upgrade failed: %v\nContinuing with the current version.\n", err)
		return path
	}
	if p, err := resolveServer(defaultLookup); err == nil {
		fmt.Fprintln(stderr, "Updated firekeeper-server.")
		return p
	}
	return path
}

// updateInPlace replaces the server file with the latest release. It keeps the
// old file until the new server has been checked against the license: a build
// newer than the key's update period would refuse to start, so that update is
// undone and the old version kept.
func updateInPlace(ctx context.Context, src serverinstall.Source, path, real, current string, stderr io.Writer) string {
	dir := filepath.Dir(real)
	if filepath.Base(real) != serverinstall.BinaryName {
		fmt.Fprintf(stderr, "firekeeper serve: %s is not named %s, so it was not updated. Continuing with the current version.\n", real, serverinstall.BinaryName)
		return path
	}
	if !serverinstall.DirWritable(dir) {
		fmt.Fprintf(stderr, "firekeeper serve: %s is not writable, so firekeeper-server was not updated. Continuing with the current version.\n", dir)
		return path
	}
	backup := filepath.Join(dir, ".firekeeper-server.previous")
	if err := copyFile(real, backup); err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: cannot keep a copy of the current server (%v), so it was not updated. Continuing with the current version.\n", err)
		return path
	}
	defer os.Remove(backup)

	installed, err := installOffer.newInstaller(src, stderr).Install(ctx, dir)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: update failed: %v\nContinuing with the current version.\n", err)
		return path
	}
	if until, covered := licenseCoverage(ctx, installed); !covered {
		if err := os.Rename(backup, installed); err != nil {
			fmt.Fprintf(stderr, "firekeeper serve: the new server is not covered by your license and the old one could not be restored: %v\n", err)
			return installed
		}
		fmt.Fprintf(stderr, "The new firekeeper-server is not covered by your license, which includes updates only until %s.\n"+
			"Kept version %s. To use newer versions, renew the license: dan.luckydaisy@gmail.com\n", until, current)
		return path
	}
	fmt.Fprintf(stderr, "Updated firekeeper-server %s -> %s.\n", current, installedServerVersion(ctx, installed))
	return installed
}

// licenseCoverage runs `<server> license --json` and reports whether the build
// is outside the key's update period. Any other outcome (a covered build, no
// key, an unreadable answer) counts as covered: only an explicit "not covered"
// undoes an update.
func licenseCoverage(ctx context.Context, server string) (until string, covered bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, server, "license", "--json")
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	_ = cmd.Run() // exits 3 when the license does not allow starting
	var rep struct {
		Status       string `json:"status"`
		UpdatesUntil string `json:"updates_until"`
	}
	if json.Unmarshal(out.Bytes(), &rep) != nil || rep.Status != statusNotCovered {
		return "", true
	}
	return rep.UpdatesUntil, false
}

// installedServerVersion runs `<server> version` and returns the version in
// its first line ("firekeeper-server 0.4.0 (commit ...)"), or "" if it cannot
// be read.
func installedServerVersion(ctx context.Context, server string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, server, "version")
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return ""
	}
	fields := strings.Fields(out.String())
	if len(fields) < 2 || fields[0] != serverinstall.BinaryName {
		return ""
	}
	return strings.TrimPrefix(fields[1], "v")
}

// copyFile copies src to dst, keeping src's permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// readChoice reads one line and returns it trimmed and lower-cased.
func readChoice(r io.Reader) string {
	var b [1]byte
	var line []byte
	for len(line) < 64 {
		n, err := r.Read(b[:])
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if err != nil {
			break
		}
	}
	return strings.ToLower(strings.TrimSpace(string(line)))
}
