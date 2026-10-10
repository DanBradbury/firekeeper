// Package config reads Firekeeper's reporting configuration from
// ~/.firekeeper/config.toml (or the file FIREKEEPER_CONFIG names) and merges
// it with environment variables and command-line flags.
//
// Precedence, highest first: flags, environment, file, defaults. The
// environment supplies FIREKEEPER_SERVER, FIREKEEPER_TOKEN,
// FIREKEEPER_PROVIDERS (comma-separated), and FIREKEEPER_INTERVAL; the other
// settings come only from the file.
//
// The file never enables uploading on its own terms beyond what it says:
// with no providers listed anywhere, the reporter still uploads nothing.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// Environment variables.
const (
	EnvConfig    = "FIREKEEPER_CONFIG"
	EnvServer    = "FIREKEEPER_SERVER"
	EnvToken     = "FIREKEEPER_TOKEN"
	EnvProviders = "FIREKEEPER_PROVIDERS"
	EnvInterval  = "FIREKEEPER_INTERVAL"
)

// Price is a per-model price in currency units per million tokens.
type Price struct {
	Input  float64 `toml:"input"`
	Output float64 `toml:"output"`
	Cache  float64 `toml:"cache"`
}

// Config is the merged configuration.
type Config struct {
	Server    string
	Token     string
	Providers []transcript.Provider
	Interval  time.Duration
	// RedactPaths are extra directories scrubbed from every event, with "~"
	// already expanded.
	RedactPaths []string
	// Exclude holds directory globs (starting with "/" or "~", "~"
	// expanded) and Git remote patterns (anything else). See Excluded.
	Exclude []string
	// Prices is the per-model price table the dashboard uses for cost.
	Prices map[string]Price
	// RepoURLTemplate links changed files to their repository host.
	RepoURLTemplate string
	// TrustedProxies are the reverse proxies `serve` believes
	// X-Forwarded-For from: IP addresses or CIDR ranges. Empty means none.
	TrustedProxies []string

	// Path is the config file read, or "" when none was.
	Path string
	// Sources records where each setting came from: "flag", "env",
	// "file", or "default".
	Sources map[string]string
}

// file is the TOML layout.
type file struct {
	Server          *string                  `toml:"server"`
	Token           *string                  `toml:"token"`
	Providers       []string                 `toml:"providers"`
	Interval        *string                  `toml:"interval"`
	Redact          struct{ Paths []string } `toml:"redact"`
	Exclude         []string                 `toml:"exclude"`
	Prices          map[string]Price         `toml:"prices"`
	RepoURLTemplate *string                  `toml:"repo_url_template"`
	TrustedProxies  []string                 `toml:"trusted_proxies"`
}

// Options configures Load.
type Options struct {
	// Home is the user's home directory, used for the default path and to
	// expand "~". Empty means os.UserHomeDir.
	Home string
	// Getenv reads the environment. Nil means os.Getenv.
	Getenv func(string) string
	// Defaults are the values used when nothing else sets a setting.
	Defaults Config
}

// Overrides are values from command-line flags. Nil and empty fields do not
// override.
type Overrides struct {
	Server    *string
	Token     *string
	Providers []transcript.Provider
	Interval  *time.Duration
}

// DefaultPath returns home/.firekeeper/config.toml.
func DefaultPath(home string) string {
	return filepath.Join(home, ".firekeeper", "config.toml")
}

// Load reads the config file and environment over opts.Defaults. A missing
// file at the default path is not an error; a missing file named by
// FIREKEEPER_CONFIG is. Unknown keys are errors, so typos do not silently
// disable an exclusion.
func Load(opts Options) (Config, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	home := opts.Home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Config{}, errors.New("config: find home directory")
		}
		home = h
	}
	c := opts.Defaults
	c.Providers = append([]transcript.Provider(nil), c.Providers...)
	c.Sources = map[string]string{}
	for _, k := range []string{"server", "token", "providers", "interval"} {
		c.Sources[k] = "default"
	}

	path, explicit := strings.TrimSpace(getenv(EnvConfig)), true
	if path == "" {
		path, explicit = DefaultPath(home), false
	}
	path = expand(path, home)
	if err := c.readFile(path, home); err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			// No file: defaults and environment only.
		} else {
			return Config{}, err
		}
	}

	if v := strings.TrimSpace(getenv(EnvServer)); v != "" {
		c.Server, c.Sources["server"] = v, "env"
	}
	if v := getenv(EnvToken); v != "" {
		c.Token, c.Sources["token"] = v, "env"
	}
	if v := strings.TrimSpace(getenv(EnvProviders)); v != "" {
		ps, err := parseProviders(strings.Split(v, ","))
		if err != nil {
			return Config{}, fmt.Errorf("config: %s: %w", EnvProviders, err)
		}
		c.Providers, c.Sources["providers"] = ps, "env"
	}
	if v := strings.TrimSpace(getenv(EnvInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("config: %s %q is not a positive duration", EnvInterval, v)
		}
		c.Interval, c.Sources["interval"] = d, "env"
	}
	return c, nil
}

func (c *Config) readFile(path, home string) error {
	var f file
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return err
		}
		var perr toml.ParseError
		if errors.As(err, &perr) {
			// The message names the line, never the value.
			return fmt.Errorf("config: %s: line %d: invalid TOML", path, perr.Position.Line)
		}
		return fmt.Errorf("config: %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return fmt.Errorf("config: %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	c.Path = path
	if f.Server != nil {
		c.Server, c.Sources["server"] = strings.TrimSpace(*f.Server), "file"
	}
	if f.Token != nil {
		c.Token, c.Sources["token"] = *f.Token, "file"
	}
	if md.IsDefined("providers") {
		ps, err := parseProviders(f.Providers)
		if err != nil {
			return fmt.Errorf("config: %s: providers: %w", path, err)
		}
		c.Providers, c.Sources["providers"] = ps, "file"
	}
	if f.Interval != nil {
		d, err := time.ParseDuration(strings.TrimSpace(*f.Interval))
		if err != nil || d <= 0 {
			return fmt.Errorf("config: %s: interval %q is not a positive duration such as \"30s\"", path, *f.Interval)
		}
		c.Interval, c.Sources["interval"] = d, "file"
	}
	for _, p := range f.Redact.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = expand(p, home)
		if !filepath.IsAbs(p) {
			return fmt.Errorf("config: %s: redact.paths entry %q must be absolute or start with ~", path, p)
		}
		c.RedactPaths = append(c.RedactPaths, filepath.Clean(p))
	}
	for _, e := range f.Exclude {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if isDirPattern(e) {
			e = filepath.Clean(expand(e, home))
			if _, err := filepath.Match(e, ""); err != nil {
				return fmt.Errorf("config: %s: exclude glob %q is malformed", path, e)
			}
		} else if _, err := filepath.Match(normalizeRemote(e), ""); err != nil {
			return fmt.Errorf("config: %s: exclude remote pattern %q is malformed", path, e)
		}
		c.Exclude = append(c.Exclude, e)
	}
	if len(f.Prices) > 0 {
		c.Prices = map[string]Price{}
		for model, p := range f.Prices {
			if p.Input < 0 || p.Output < 0 || p.Cache < 0 {
				return fmt.Errorf("config: %s: prices.%s must not be negative", path, model)
			}
			c.Prices[model] = p
		}
	}
	if f.RepoURLTemplate != nil {
		c.RepoURLTemplate = strings.TrimSpace(*f.RepoURLTemplate)
	}
	for _, p := range f.TrustedProxies {
		if p = strings.TrimSpace(p); p != "" {
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}
	return nil
}

// Apply returns c with flag overrides applied.
func (c Config) Apply(o Overrides) Config {
	src := map[string]string{}
	for k, v := range c.Sources {
		src[k] = v
	}
	c.Sources = src
	if o.Server != nil {
		c.Server, c.Sources["server"] = *o.Server, "flag"
	}
	if o.Token != nil {
		c.Token, c.Sources["token"] = *o.Token, "flag"
	}
	if len(o.Providers) > 0 {
		c.Providers, c.Sources["providers"] = o.Providers, "flag"
	}
	if o.Interval != nil {
		c.Interval, c.Sources["interval"] = *o.Interval, "flag"
	}
	return c
}

func parseProviders(names []string) ([]transcript.Provider, error) {
	out := []transcript.Provider{}
	seen := map[transcript.Provider]bool{}
	for _, n := range names {
		p := transcript.Provider(strings.ToLower(strings.TrimSpace(n)))
		if p == "" {
			continue
		}
		if !p.Valid() {
			return nil, fmt.Errorf("unknown provider %q", n)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// expand replaces a leading "~" with home.
func expand(p, home string) string {
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}

// MaskToken hides a token, keeping its last four characters when it is long
// enough that they reveal nothing useful.
func MaskToken(t string) string {
	switch {
	case t == "":
		return ""
	case len(t) < 16:
		return "********"
	}
	return "********" + t[len(t)-4:]
}
