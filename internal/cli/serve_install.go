package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/DanBradbury/firekeeper/internal/serverinstall"
)

const (
	// NoPromptEnv, when set to anything, stops `firekeeper serve` from ever
	// asking to install the server.
	NoPromptEnv = "FIREKEEPER_NO_PROMPT"

	brewFormula = "DanBradbury/tap/firekeeper-server"
)

// installEnv is everything the install offer reads from the outside world.
// Tests replace it.
type installEnv struct {
	interactive  func() bool // stdin and stderr are both terminals
	stdin        io.Reader
	userHome     func() (string, error)
	newInstaller func(src serverinstall.Source, log io.Writer) *serverinstall.Installer
	runBrew      func(brewPath string, stderr io.Writer) error
}

var defaultInstallEnv = installEnv{
	interactive: func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
	},
	stdin:    os.Stdin,
	userHome: os.UserHomeDir,
	newInstaller: func(src serverinstall.Source, log io.Writer) *serverinstall.Installer {
		return &serverinstall.Installer{Source: src, Log: log}
	},
	runBrew: func(brewPath string, stderr io.Writer) error {
		cmd := exec.Command(brewPath, "install", brewFormula)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stderr, stderr
		return cmd.Run()
	},
}

var installOffer = defaultInstallEnv

// offerInstall is called when no server was found. It returns the path of a
// freshly installed server, or "" when the caller should print the usual
// instructions (the offer is off, not possible, declined, or failed).
//
// It touches the network only after an explicit "y" or "yes" at the prompt,
// and never asks when the feature is off or the session is not interactive.
func offerInstall(stderr io.Writer) string {
	l := defaultLookup
	src, enabled, err := serverinstall.Resolve(l.getenv)
	if !enabled {
		return ""
	}
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: %v\n", err)
		return ""
	}
	if l.getenv("CI") != "" || l.getenv(NoPromptEnv) != "" || !installOffer.interactive() {
		return ""
	}

	exe := ""
	if p, err := l.executable(); err == nil {
		exe = p
		if real, err := filepath.EvalSymlinks(p); err == nil {
			exe = real
		}
	}

	if exe != "" && serverinstall.IsHomebrew(exe) {
		if brew, err := l.lookPath("brew"); err == nil {
			return offerBrew(brew, stderr)
		}
	}

	home, _ := installOffer.userHome()
	dir, fallback, err := serverinstall.ChooseDir(exe, home)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: %v\n", err)
		return ""
	}
	fmt.Fprintf(stderr, "firekeeper-server is not installed. Download it from %s and install it to %s? [y/N] ", src.Host(), dir)
	if !readYes(installOffer.stdin) {
		fmt.Fprintln(stderr)
		return ""
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	path, err := installOffer.newInstaller(src, stderr).Install(ctx, dir)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: install failed: %v\n", err)
		return ""
	}
	if fallback && !serverinstall.OnPath(dir, l.getenv("PATH")) {
		fmt.Fprintf(stderr, "Note: %s is not on your PATH. Add it, for example:\n  export PATH=\"%s:$PATH\"\n", dir, dir)
	}
	return path
}

// offerBrew handles a firekeeper that Homebrew owns: it never writes into
// the Homebrew prefix itself, and offers `brew install` instead.
func offerBrew(brew string, stderr io.Writer) string {
	fmt.Fprintf(stderr, "firekeeper-server is not installed. firekeeper is managed by Homebrew; run `brew install %s`? [y/N] ", brewFormula)
	if !readYes(installOffer.stdin) {
		fmt.Fprintf(stderr, "\nTo install it yourself: brew install %s\n", brewFormula)
		return ""
	}
	if err := installOffer.runBrew(brew, stderr); err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: brew install failed: %v\n", err)
		return ""
	}
	p, err := resolveServer(defaultLookup)
	if err != nil {
		fmt.Fprintf(stderr, "firekeeper serve: brew finished but %s is still not found\n", serverBinName)
		return ""
	}
	return p
}

// readYes reads one line and reports whether it is y or yes (any case).
// EOF, errors and everything else are no.
func readYes(r io.Reader) bool {
	line, _ := bufio.NewReader(r).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
