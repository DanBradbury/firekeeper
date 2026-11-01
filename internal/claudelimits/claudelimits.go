// Package claudelimits reads a Claude subscription's 5-hour and weekly usage
// limits, with the reset time of each.
//
// Claude Code does not store these locally, so this package does what
// Claude Code's own /usage view does: it reads the OAuth access token Claude
// Code already holds on this machine and asks Anthropic's usage endpoint.
// That endpoint is not documented and can change without notice, so every
// failure is an error the caller shows quietly, never a crash.
//
// The token is read only for the one request. It is never written, logged,
// refreshed, or included in an error. Callers must make this opt-in.
//
// Token sources, first match wins:
//
//  1. CLAUDE_CODE_OAUTH_TOKEN
//  2. the macOS Keychain item "Claude Code-credentials"
//  3. CLAUDE_CONFIG_DIR/.credentials.json (default ~/.claude), which is where
//     Claude Code keeps it on Linux and Windows
package claudelimits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// UsageURL is the endpoint Claude Code's /usage view reads.
const UsageURL = "https://api.anthropic.com/api/oauth/usage"

const (
	envOAuthToken = "CLAUDE_CODE_OAUTH_TOKEN"
	envConfigDir  = "CLAUDE_CONFIG_DIR"

	keychainService = "Claude Code-credentials"
	betaHeader      = "oauth-2025-04-20"
	requestTimeout  = 5 * time.Second
	maxBody         = 1 << 20
)

var (
	// ErrNoToken means no Claude Code sign-in was found on this machine.
	ErrNoToken = errors.New("no Claude Code sign-in found; run claude and log in")
	// ErrTokenExpired means the stored token is past its expiry. Firekeeper
	// does not refresh it: refreshing would rotate Claude Code's own token.
	ErrTokenExpired = errors.New("Claude Code sign-in has expired; open claude once to renew it")
	// ErrUnauthorized means the endpoint rejected the token.
	ErrUnauthorized = errors.New("Anthropic rejected the Claude Code sign-in; open claude and run /login")
)

// RateLimitedError means the endpoint answered HTTP 429. RetryAfter is the
// server's Retry-After delay, or zero when it sent none.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return "Anthropic usage endpoint is rate limiting requests (HTTP 429)"
}

// parseRetryAfter reads a delay-seconds Retry-After value. Anything else,
// including an HTTP date, is treated as absent.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// Window is one limit window. UsedPercent is 0-100; ResetsAt is zero when the
// endpoint did not say.
type Window struct {
	UsedPercent float64
	ResetsAt    time.Time
}

// Limits are the windows the endpoint reported. A nil window was not reported
// (for example, Opus-only weekly limits exist on some plans only).
type Limits struct {
	FiveHour        *Window
	SevenDay        *Window
	SevenDayOpus    *Window
	SevenDaySonnet  *Window
	SubscriptionTag string
}

// Any reports whether at least one window was returned.
func (l Limits) Any() bool {
	return l.FiveHour != nil || l.SevenDay != nil || l.SevenDayOpus != nil || l.SevenDaySonnet != nil
}

// Client reads limits. The zero value uses the real environment; tests set
// the fields.
type Client struct {
	URL        string
	HTTPClient *http.Client
	Getenv     func(string) string
	Home       string
	GOOS       string
	// Keychain returns the Keychain item's secret, or an error when absent.
	Keychain func() ([]byte, error)
	Now      func() time.Time
}

func (c Client) getenv(key string) string {
	if c.Getenv != nil {
		return c.Getenv(key)
	}
	return os.Getenv(key)
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Client) goos() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

// credentials is the part of Claude Code's credential JSON that is read.
type credentials struct {
	ClaudeAiOauth struct {
		AccessToken      string `json:"accessToken"`
		ExpiresAt        int64  `json:"expiresAt"` // Unix milliseconds
		SubscriptionType string `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

type token struct {
	value   string
	expires time.Time
	plan    string
}

// HasToken reports whether a sign-in is present, without making a request or
// returning the secret. The CLI uses it for status.
func (c Client) HasToken() bool {
	tok, err := c.findToken()
	return err == nil && tok.value != ""
}

func (c Client) findToken() (token, error) {
	if v := strings.TrimSpace(c.getenv(envOAuthToken)); v != "" {
		return token{value: v}, nil
	}
	if c.goos() == "darwin" {
		keychain := c.Keychain
		if keychain == nil {
			keychain = readKeychain
		}
		if data, err := keychain(); err == nil {
			if tok, ok := parseCredentials(data); ok {
				return tok, nil
			}
		}
	}
	dir := strings.TrimSpace(c.getenv(envConfigDir))
	if dir == "" {
		home := c.Home
		if home == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return token{}, ErrNoToken
			}
			home = h
		}
		dir = filepath.Join(home, ".claude")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return token{}, ErrNoToken
		}
		return token{}, errors.New("read Claude Code credentials file")
	}
	if tok, ok := parseCredentials(data); ok {
		return tok, nil
	}
	return token{}, ErrNoToken
}

func parseCredentials(data []byte) (token, bool) {
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil || creds.ClaudeAiOauth.AccessToken == "" {
		return token{}, false
	}
	tok := token{value: creds.ClaudeAiOauth.AccessToken, plan: creds.ClaudeAiOauth.SubscriptionType}
	if creds.ClaudeAiOauth.ExpiresAt > 0 {
		tok.expires = time.UnixMilli(creds.ClaudeAiOauth.ExpiresAt)
	}
	return tok, true
}

func readKeychain() ([]byte, error) {
	security, err := exec.LookPath("security")
	if err != nil {
		return nil, err
	}
	return exec.Command(security, "find-generic-password", "-s", keychainService, "-w").Output()
}

type wireWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type wireLimits struct {
	FiveHour       *wireWindow `json:"five_hour"`
	SevenDay       *wireWindow `json:"seven_day"`
	SevenDayOpus   *wireWindow `json:"seven_day_opus"`
	SevenDaySonnet *wireWindow `json:"seven_day_sonnet"`
}

// Fetch returns the account's current limits.
func (c Client) Fetch(ctx context.Context) (Limits, error) {
	tok, err := c.findToken()
	if err != nil {
		return Limits{}, err
	}
	if !tok.expires.IsZero() && !tok.expires.After(c.now()) {
		return Limits{}, ErrTokenExpired
	}
	url := c.URL
	if url == "" {
		url = UsageURL
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Limits{}, errors.New("create Claude limits request")
	}
	request.Header.Set("Authorization", "Bearer "+tok.value)
	request.Header.Set("anthropic-beta", betaHeader)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "firekeeper")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		// The url.Error text names the URL, not the token, but keep it short.
		return Limits{}, errors.New("reach Anthropic usage endpoint")
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return Limits{}, ErrUnauthorized
	case response.StatusCode == http.StatusTooManyRequests:
		return Limits{}, &RateLimitedError{RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"))}
	case response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices:
		return Limits{}, fmt.Errorf("Anthropic usage endpoint returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return Limits{}, errors.New("read Anthropic usage response")
	}
	limits, err := decode(body)
	if err != nil {
		return Limits{}, err
	}
	limits.SubscriptionTag = tok.plan
	return limits, nil
}

func decode(body []byte) (Limits, error) {
	var wire wireLimits
	if err := json.Unmarshal(body, &wire); err != nil {
		return Limits{}, errors.New("Anthropic usage response was not understood")
	}
	limits := Limits{
		FiveHour:       convert(wire.FiveHour),
		SevenDay:       convert(wire.SevenDay),
		SevenDayOpus:   convert(wire.SevenDayOpus),
		SevenDaySonnet: convert(wire.SevenDaySonnet),
	}
	if !limits.Any() {
		return Limits{}, errors.New("Anthropic usage response had no limit windows")
	}
	return limits, nil
}

func convert(w *wireWindow) *Window {
	if w == nil || w.Utilization == nil {
		return nil
	}
	out := &Window{UsedPercent: min(max(*w.Utilization, 0), 100)}
	if w.ResetsAt != nil {
		if t, err := time.Parse(time.RFC3339, *w.ResetsAt); err == nil {
			out.ResetsAt = t
		}
	}
	return out
}
