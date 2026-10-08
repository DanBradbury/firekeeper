// Package config reads Firekeeper's optional config file and merges it with
// environment variables and defaults. Flags are applied by the caller on top
// of the result, giving the precedence flags > environment > file > defaults.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	DefaultServer   = "http://127.0.0.1:7777"
	DefaultInterval = 30 * time.Second
	// EnvPath overrides the config file location.
	EnvPath = "FIREKEEPER_CONFIG"
)

// Price is a per-model token price, in dollars per million tokens.
type Price struct {
	Input  float64 `toml:"input"`
	Output float64 `toml:"output"`
	Cache  float64 `toml:"cache"`
}

// Redact holds extra redaction settings.
type Redact struct {
	Paths []string `toml:"paths"`
}

// Config is the merged configuration.
type Config struct {
	Server          string           `toml:"server"`
	Token           string           `toml:"token"`
	Providers       []string         `toml:"providers"`
	Interval        Duration         `toml:"interval"`
	Redact          Redact           `toml:"redact"`
	Exclude         []string         `toml:"exclude"`
	Prices          map[string]Price `toml:"prices"`
	RepoURLTemplate string           `toml:"repo_url_template"`
}

// Duration is a time.Duration that reads from TOML as a string such as "30s".
type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(text)))
	if err != nil {
		return fmt.Errorf("invalid duration %q", text)
	}
	if v < 0 {
		return errors.New("duration must not be negative")
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{Server: DefaultServer, Interval: Duration(DefaultInterval)}
}

// Path returns the config file path: env[FIREKEEPER_CONFIG] or
// <home>/.firekeeper/config.toml.
func Path(getenv func(string) string, home string) string {
	if p := getenv(EnvPath); p != "" {
		return p
	}
	return filepath.Join(home, ".firekeeper", "config.toml")
}

// Load merges defaults, the config file, and environment variables. A missing
// file is not an error. Errors never include file contents.
func Load(getenv func(string) string, home string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Defaults()
	path := Path(getenv, home)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var file Config
		md, derr := toml.Decode(string(data), &file)
		if derr != nil {
			return cfg, fmt.Errorf("parse config %s: %s", path, tomlReason(derr))
		}
		cfg.merge(file, md)
	case errors.Is(err, fs.ErrNotExist):
	default:
		return cfg, fmt.Errorf("read config %s: %w", path, errors.Unwrap(err))
	}
	if err := cfg.applyEnv(getenv); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func tomlReason(err error) string {
	var perr toml.ParseError
	if errors.As(err, &perr) {
		return fmt.Sprintf("line %d: %s", perr.Position.Line, perr.Message)
	}
	return "invalid value"
}

func (c *Config) merge(f Config, md toml.MetaData) {
	if md.IsDefined("server") {
		c.Server = f.Server
	}
	if md.IsDefined("token") {
		c.Token = f.Token
	}
	if md.IsDefined("providers") {
		c.Providers = f.Providers
	}
	if md.IsDefined("interval") {
		c.Interval = f.Interval
	}
	if md.IsDefined("redact", "paths") {
		c.Redact.Paths = f.Redact.Paths
	}
	if md.IsDefined("exclude") {
		c.Exclude = f.Exclude
	}
	if md.IsDefined("prices") {
		c.Prices = f.Prices
	}
	if md.IsDefined("repo_url_template") {
		c.RepoURLTemplate = f.RepoURLTemplate
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *Config) applyEnv(getenv func(string) string) error {
	if v := getenv("FIREKEEPER_SERVER"); v != "" {
		c.Server = v
	}
	if v := getenv("FIREKEEPER_TOKEN"); v != "" {
		c.Token = v
	}
	if v := getenv("FIREKEEPER_PROVIDERS"); v != "" {
		c.Providers = splitList(v)
	}
	if v := getenv("FIREKEEPER_INTERVAL"); v != "" {
		if err := c.Interval.UnmarshalText([]byte(v)); err != nil {
			return fmt.Errorf("FIREKEEPER_INTERVAL: %w", err)
		}
	}
	if v := getenv("FIREKEEPER_REDACT_PATHS"); v != "" {
		c.Redact.Paths = splitList(v)
	}
	if v := getenv("FIREKEEPER_EXCLUDE"); v != "" {
		c.Exclude = splitList(v)
	}
	if v := getenv("FIREKEEPER_REPO_URL_TEMPLATE"); v != "" {
		c.RepoURLTemplate = v
	}
	return nil
}

// MaskToken hides all but the last four characters of a token.
func MaskToken(token string) string {
	switch {
	case token == "":
		return ""
	case len(token) <= 8:
		return "****"
	}
	return "****" + token[len(token)-4:]
}

// Show writes the merged config as TOML with the token masked.
func (c Config) Show(w io.Writer) error {
	q := strconv.Quote
	list := func(items []string) string {
		quoted := make([]string, len(items))
		for i, s := range items {
			quoted[i] = q(s)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}
	fmt.Fprintf(w, "server = %s\n", q(c.Server))
	fmt.Fprintf(w, "token = %s\n", q(MaskToken(c.Token)))
	fmt.Fprintf(w, "providers = %s\n", list(c.Providers))
	fmt.Fprintf(w, "interval = %s\n", q(time.Duration(c.Interval).String()))
	fmt.Fprintf(w, "exclude = %s\n", list(c.Exclude))
	fmt.Fprintf(w, "repo_url_template = %s\n", q(c.RepoURLTemplate))
	fmt.Fprintf(w, "\n[redact]\npaths = %s\n", list(c.Redact.Paths))
	models := make([]string, 0, len(c.Prices))
	for m := range c.Prices {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		p := c.Prices[m]
		fmt.Fprintf(w, "\n[prices.%s]\ninput = %g\noutput = %g\ncache = %g\n", q(m), p.Input, p.Output, p.Cache)
	}
	return nil
}
