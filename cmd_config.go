package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/daemon"
	"github.com/DanBradbury/firekeeper/internal/reporter"
)

// configHome locates the home directory the config file is read from. It
// is replaced in tests so they never read a real config.
var configHome = os.UserHomeDir

// configFlags are the reporting flags that override the config file. Each
// pointer is nil for a command that lacks the flag.
type configFlags struct {
	fs        *flag.FlagSet
	server    *string
	token     *string
	providers *providerList
	interval  *time.Duration
}

// applyReporterConfig copies the merged reporting settings into cfg.
func applyReporterConfig(cfg *reporter.Config, c config.Config) {
	cfg.Server = c.Server
	cfg.Token = c.Token
	cfg.Providers = c.Providers
	cfg.Exclude = c.Exclude
	cfg.RedactPaths = c.RedactPaths
}

// tokenFlag registers --token.
func tokenFlag(fs *flag.FlagSet) *string {
	return fs.String("token", "", "ingest token for the dashboard server (default $"+config.EnvToken+" or the config file)")
}

// loadConfig merges defaults, the config file, the environment, and the
// flags actually given on the command line, in that order of precedence.
func loadConfig(f configFlags) (config.Config, error) {
	home, err := configHome()
	if err != nil {
		return config.Config{}, fmt.Errorf("find home directory: %w", err)
	}
	c, err := config.Load(config.Options{
		Home:     home,
		Defaults: config.Config{Server: reporter.DefaultServer, Interval: daemon.DefaultInterval},
	})
	if err != nil {
		return c, err
	}
	var o config.Overrides
	if f.fs != nil {
		f.fs.Visit(func(fl *flag.Flag) {
			switch fl.Name {
			case "server":
				o.Server = f.server
			case "token":
				o.Token = f.token
			case "provider":
				o.Providers = *f.providers
			case "interval":
				o.Interval = f.interval
			}
		})
	}
	return c.Apply(o), nil
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "show" {
		fmt.Fprintln(stderr, "Usage:\n  firekeeper config show   print the merged configuration with the token masked")
		if len(args) == 0 {
			return 2
		}
		if args[0] == "-h" || args[0] == "--help" {
			return 0
		}
		fmt.Fprintf(stderr, "firekeeper config: unknown command %q\n", args[0])
		return 2
	}
	const name = "firekeeper config show"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, ok := parseNoArgs(name, fs, args[1:], stderr); !ok {
		return code
	}
	c, err := loadConfig(configFlags{})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	writeConfig(stdout, c)
	return 0
}

// writeConfig prints c as TOML-like text. The token is masked, and each
// setting with a layered source notes where it came from.
func writeConfig(w io.Writer, c config.Config) {
	if c.Path != "" {
		fmt.Fprintf(w, "# file: %s\n", c.Path)
	} else {
		fmt.Fprintln(w, "# file: none (using defaults and environment)")
	}
	src := func(key string) string { return "  # " + c.Sources[key] }
	fmt.Fprintf(w, "server = %q%s\n", c.Server, src("server"))
	fmt.Fprintf(w, "token = %q%s\n", config.MaskToken(c.Token), src("token"))
	names := make([]string, len(c.Providers))
	for i, p := range c.Providers {
		names[i] = string(p)
	}
	fmt.Fprintf(w, "providers = %s%s\n", tomlList(names), src("providers"))
	fmt.Fprintf(w, "interval = %q%s\n", c.Interval.String(), src("interval"))
	fmt.Fprintf(w, "exclude = %s\n", tomlList(c.Exclude))
	fmt.Fprintf(w, "repo_url_template = %q\n", c.RepoURLTemplate)
	fmt.Fprintf(w, "\n[redact]\npaths = %s\n", tomlList(c.RedactPaths))
	models := make([]string, 0, len(c.Prices))
	for m := range c.Prices {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		p := c.Prices[m]
		fmt.Fprintf(w, "\n[prices.%q]\ninput = %g\noutput = %g\ncache = %g\n", m, p.Input, p.Output, p.Cache)
	}
}

func tomlList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
