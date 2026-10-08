package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/session"
)

// requestTimeout bounds the one-shot calls logout and whoami make.
const requestTimeout = 15 * time.Second

// linkPollInterval overrides the polling interval the server asks for. Tests
// shorten it; zero means the server's.
var linkPollInterval time.Duration

// runLogin links this machine to a dashboard server with the device-code
// flow and stores the resulting ingest token in the config file. It never
// enables a provider: uploading stays opt-in.
func runLogin(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper login"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", reporter.DefaultServer, "dashboard server URL (default $"+config.EnvServer+" or the config file)")
	machineName := fs.String("name", "", "machine name to offer on the approval page (default: this host's name)")
	if code, ok := parseNoArgs(name, fs, args, stderr); !ok {
		return code
	}
	c, err := loadConfig(configFlags{fs: fs, server: server})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 2
	}
	home, err := configHome()
	if err != nil {
		fmt.Fprintf(stderr, "%s: find home directory\n", name)
		return 1
	}
	path, err := config.ResolvePath(home, nil)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	machineID, err := session.MachineID(home)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if *machineName == "" {
		*machineName, _ = os.Hostname()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := reporter.Link(ctx, reporter.LinkOptions{
		Server: c.Server, MachineID: machineID, MachineName: *machineName, Out: stdout, Interval: linkPollInterval,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "%s: cancelled; nothing was saved\n", name)
		} else {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
		}
		return 1
	}
	if err := config.SaveCredentials(path, res.Server, res.Token); err != nil {
		// The token exists on the server but could not be kept here; ask
		// the person to revoke it rather than printing it.
		fmt.Fprintf(stderr, "%s: linked, but the token could not be saved: %v\n", name, err)
		fmt.Fprintf(stderr, "Revoke the new token for %q on the server's Tokens page, then try again.\n", res.MachineName)
		return 1
	}
	fmt.Fprintf(stdout, "Linked %q to %s as %s.\n", res.MachineName, res.Server, res.AccountEmail)
	fmt.Fprintf(stdout, "Token saved to %s (mode 0600).\n", path)
	if c.Sources["token"] == "file" && c.Token != "" {
		fmt.Fprintln(stdout, "An earlier stored token was replaced; revoke it on the Tokens page if it is no longer needed.")
	}
	if len(c.Providers) == 0 {
		fmt.Fprintln(stdout, "No providers are enabled, so nothing is uploaded yet. Preview with `firekeeper report --provider NAME --dry-run`, then upload with `firekeeper report --provider NAME` or `firekeeper daemon install`.")
	}
	return 0
}

// runWhoami prints the server, account, and machine the stored token
// belongs to. It never prints the token.
func runWhoami(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper whoami"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, ok := parseNoArgs(name, fs, args, stderr); !ok {
		return code
	}
	c, err := loadConfig(configFlags{})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 2
	}
	if c.Token == "" {
		fmt.Fprintf(stderr, "%s: not logged in; run `firekeeper login --server URL`\n", name)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	id, err := reporter.Whoami(ctx, c.Server, c.Token, nil)
	if err != nil {
		if errors.Is(err, reporter.ErrUnauthorized) {
			fmt.Fprintf(stderr, "%s: %s does not accept the stored token; it may have been revoked. Run `firekeeper login` again.\n", name, c.Server)
		} else {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
		}
		return 1
	}
	machine := id.Name
	if machine == "" {
		machine = "(unnamed)"
	}
	fmt.Fprintf(stdout, "server:  %s\naccount: %s\nmachine: %s\n", c.Server, id.AccountEmail, machine)
	return 0
}

// runLogout revokes the stored token on the server when it is reachable and
// removes it from the config file either way.
func runLogout(args []string, stdout, stderr io.Writer) int {
	const name = "firekeeper logout"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, ok := parseNoArgs(name, fs, args, stderr); !ok {
		return code
	}
	c, err := loadConfig(configFlags{})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 2
	}
	if c.Token == "" {
		fmt.Fprintln(stdout, "Not logged in; no token is stored.")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if err := reporter.RevokeSelf(ctx, c.Server, c.Token, nil); err != nil {
		// Offline is expected; the local token is still removed below.
		fmt.Fprintf(stderr, "%s: could not revoke the token on the server (%v); it stays valid until revoked on the Tokens page\n", name, err)
	} else {
		fmt.Fprintf(stdout, "Revoked the token on %s.\n", c.Server)
	}
	switch c.Sources["token"] {
	case "file":
		if _, err := config.ClearToken(c.Path); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 1
		}
		fmt.Fprintf(stdout, "Removed the token from %s.\n", c.Path)
	default:
		fmt.Fprintf(stdout, "The token comes from %s, not the config file; unset it there.\n", config.EnvToken)
	}
	return 0
}
