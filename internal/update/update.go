// Package update checks GitHub for a newer Firekeeper release and, if the
// user agrees, installs it.
//
// The check is one unauthenticated GET for the latest release on each launch,
// and sends nothing about the machine, the user, or any session. It is
// skipped for development builds, when stdin or stdout is not a terminal, and
// when FIREKEEPER_NO_UPDATE_CHECK is set.
package update

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Environment variables.
const (
	EnvDisable = "FIREKEEPER_NO_UPDATE_CHECK"
	EnvURL     = "FIREKEEPER_UPDATE_URL"
)

const (
	defaultURL   = "https://api.github.com/repos/DanBradbury/firekeeper/releases/latest"
	scriptURLFmt = "https://raw.githubusercontent.com/DanBradbury/firekeeper/%s/install.sh"
	brewFormula  = "DanBradbury/tap/firekeeper"
	fetchTimeout = 2 * time.Second
	maxBody      = 1 << 20
)

var tagPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// Checker looks up and installs releases. The zero value is not usable; set
// Current and Dir.
type Checker struct {
	Current string       // running version, such as "0.2.1"; "dev" disables the check
	Dir     string       // state directory, normally ~/.firekeeper
	URL     string       // latest-release endpoint; defaults to GitHub
	Client  *http.Client // defaults to a client with a short timeout
	Now     func() time.Time
}

type state struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
	Skipped   string    `json:"skipped"`
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) statePath() string { return filepath.Join(c.Dir, "update.json") }

func (c *Checker) load() state {
	var s state
	if b, err := os.ReadFile(c.statePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (c *Checker) save(s state) {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return
	}
	if b, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(c.statePath(), b, 0o600)
	}
}

// Available returns the newest release when it is newer than the running
// version and the user has not skipped it. It asks GitHub on every call and
// falls back to the last cached answer when the lookup fails, so an offline
// launch can still offer a release seen earlier.
func (c *Checker) Available(ctx context.Context) (string, bool) {
	if c.Current == "" || c.Current == "dev" || os.Getenv(EnvDisable) != "" {
		return "", false
	}
	s := c.load()
	s.CheckedAt = c.now()
	if latest, err := c.fetch(ctx); err == nil {
		s.Latest = latest
	}
	c.save(s)
	if s.Latest == "" || s.Latest == s.Skipped || !Newer(s.Latest, c.Current) {
		return "", false
	}
	return s.Latest, true
}

// Skip stops offering version until a newer one is released.
func (c *Checker) Skip(version string) {
	s := c.load()
	s.Skipped = version
	c.save(s)
}

func (c *Checker) fetch(ctx context.Context) (string, error) {
	url := c.URL
	if url == "" {
		url = os.Getenv(EnvURL)
	}
	if url == "" {
		url = defaultURL
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "firekeeper")
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release lookup: HTTP %d", resp.StatusCode)
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&rel); err != nil {
		return "", err
	}
	if rel.Prerelease || !tagPattern.MatchString(rel.Tag) {
		return "", errors.New("release lookup: unusable tag")
	}
	return strings.TrimPrefix(rel.Tag, "v"), nil
}

// Newer reports whether latest is a higher version than current. Versions are
// MAJOR.MINOR.PATCH with an optional "v" prefix and "-prerelease" suffix; a
// prerelease sorts below its release. Unparseable input is never newer.
func Newer(latest, current string) bool {
	l, lpre, ok1 := parse(latest)
	c, cpre, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return cpre && !lpre
}

func parse(v string) (n [3]int, pre, ok bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		pre = v[i] == '-'
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return n, false, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return n, false, false
		}
		n[i] = x
	}
	return n, pre, true
}

// IsHomebrew reports whether exe lives in a Homebrew Cellar, resolved or not.
func IsHomebrew(exe string) bool {
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return strings.Contains(exe, "/Cellar/")
}

// Prompt offers the update on out and reads the answer from in. It returns
// true when the update ran, so the caller should exit and let the user
// restart. Enter or any unknown answer means "not now".
func (c *Checker) Prompt(ctx context.Context, latest string, in io.Reader, out, errOut io.Writer) bool {
	fmt.Fprintf(out, "\nFirekeeper update available: %s -> %s\n", c.Current, latest)
	fmt.Fprintln(out, "  1) Update now")
	fmt.Fprintln(out, "  2) Not now")
	fmt.Fprintln(out, "  3) Skip this version")
	fmt.Fprintf(out, "Choose [2]: ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.TrimSpace(line) {
	case "1", "y", "Y", "yes":
		exe, _ := os.Executable()
		if err := Install(ctx, latest, exe, out, errOut); err != nil {
			fmt.Fprintf(errOut, "firekeeper: update failed: %v\n", err)
			fmt.Fprintln(errOut, "Continuing with the current version.")
			return false
		}
		fmt.Fprintf(out, "Updated to %s. Run firekeeper again to use it.\n", latest)
		return true
	case "3", "s", "S", "skip":
		c.Skip(latest)
	}
	fmt.Fprintln(out)
	return false
}

// Install upgrades to version. Homebrew installs go through brew; others run
// the release install script for that exact tag, which checks the download's
// SHA-256, into the directory holding exe.
func Install(ctx context.Context, version, exe string, out, errOut io.Writer) error {
	tag := "v" + strings.TrimPrefix(version, "v")
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("invalid version %q", version)
	}
	if IsHomebrew(exe) {
		// Homebrew is chatty (auto-update news, tap trust and Command Line
		// Tools warnings, cleanup hints). Keep it quiet unless the upgrade
		// fails, then show everything it said.
		var buf bytes.Buffer
		cmd := exec.CommandContext(ctx, "brew", "upgrade", brewFormula)
		cmd.Stdout, cmd.Stderr = &buf, &buf
		cmd.Env = append(os.Environ(), "HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_INSTALL_CLEANUP=1")
		if err := cmd.Run(); err != nil {
			_, _ = errOut.Write(buf.Bytes())
			return err
		}
		return nil
	}
	script, err := fetchScript(ctx, fmt.Sprintf(scriptURLFmt, tag))
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "sh", "-s", "--", "--version", tag)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.Env = append(os.Environ(), "FIREKEEPER_INSTALL_DIR="+filepath.Dir(exe))
	return cmd.Run()
}

func fetchScript(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download install script: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download install script: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return string(b), err
}
