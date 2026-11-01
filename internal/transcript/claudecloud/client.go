package claudecloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the API the Claude web app talks to.
	DefaultBaseURL = "https://api.anthropic.com/v1"

	anthropicVersion = "2023-06-01"
	// betaHeader selects the cloud sessions API. Without it /sessions is a 404.
	betaHeader = "ccr-byoc-2025-07-29"

	// eventsPageSize is the most events one request returns.
	eventsPageSize = 1000
	// maxPages bounds a paged walk so a misbehaving server cannot loop it.
	maxPages = 10000
	// maxResponseBytes bounds one response body.
	maxResponseBytes = 128 << 20
	requestTimeout   = 90 * time.Second
	// maxRetryAfter caps a server's Retry-After hint.
	maxRetryAfter = 24 * time.Hour
)

// StatusError is a non-200 API reply. It carries the status only, never the
// body, which may echo request data.
type StatusError struct {
	Status int
	// RetryAfter is the server's Retry-After hint, or zero when it sent
	// none or one that could not be parsed.
	RetryAfter time.Duration
}

// transportError is a request that got no HTTP reply at all (DNS, TLS,
// connection reset, timeout).
type transportError struct{ err error }

func (e *transportError) Error() string { return "claudecloud: request failed: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// AccountWide reports whether err says the API or the login is unusable
// right now, as opposed to one session being unreadable: a refused login
// (401, 403), rate limiting (429), a request timeout (408), a server error
// (5xx), or no reply at all. A daemon pass that hits one stops downloading
// and backs off instead of trying the next session.
func AccountWide(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		switch {
		case se.Status == http.StatusUnauthorized, se.Status == http.StatusForbidden,
			se.Status == http.StatusRequestTimeout, se.Status == http.StatusTooManyRequests,
			se.Status >= 500:
			return true
		}
		return false
	}
	var te *transportError
	return errors.As(err, &te)
}

// RetryAfter returns the Retry-After hint carried by err, or zero.
func RetryAfter(err error) time.Duration {
	var se *StatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

// parseRetryAfter reads a Retry-After header: delay seconds or an HTTP date.
// Anything else, or a time in the past, is zero.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		if secs > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return min(t.Sub(now), maxRetryAfter)
	}
	return 0
}

func (e *StatusError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("claudecloud: API refused the Claude Code login (HTTP %d); sign in again with `claude`", e.Status)
	case http.StatusNotFound:
		return "claudecloud: API endpoint not found (HTTP 404); the internal API may have changed"
	case http.StatusTooManyRequests:
		return "claudecloud: API is rate limiting requests (HTTP 429)"
	}
	return fmt.Sprintf("claudecloud: API returned HTTP %d", e.Status)
}

// Client calls the cloud sessions API with a user's Claude Code login.
type Client struct {
	// BaseURL replaces DefaultBaseURL in tests.
	BaseURL string
	// HTTP replaces the default client in tests.
	HTTP *http.Client

	creds Credentials
}

// NewClient returns a client for creds. It never follows redirects, so the
// token cannot be sent anywhere but the API host.
func NewClient(creds Credentials) *Client {
	return &Client{creds: creds}
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

// SessionInfo is the metadata the list endpoint returns for one session.
type SessionInfo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	CWD       string    `json:"cwd"`
	Model     string    `json:"model"`
	Branch    string    `json:"branch"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Repo is the GitHub repository the session worked on, "owner/name", or
	// empty when it had none or the API gave an unusable value.
	Repo string `json:"repo"`
	// Events is how many transcript lines the cache holds; set by Sync.
	Events int `json:"events"`
	// Schema is the cache layout version; entries from an older one are
	// downloaded again. Set by Sync.
	Schema int `json:"schema"`
}

// listItem decodes only the fields used; the API sends many more and adds
// fields without notice.
type listItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"session_status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Summary   *struct {
		Title string `json:"title"`
	} `json:"post_turn_summary"`
	Context *struct {
		CWD     string `json:"cwd"`
		Model   string `json:"model"`
		Sources []struct {
			URL string `json:"url"`
		} `json:"sources"`
		Outcomes []struct {
			Git *struct {
				Repo     string   `json:"repo"`
				Branches []string `json:"branches"`
			} `json:"git_info"`
		} `json:"outcomes"`
	} `json:"session_context"`
	External *struct {
		Branches map[string]string `json:"current_branches"`
		Model    string            `json:"last_served_model"`
	} `json:"external_metadata"`
	Configured string `json:"configured_model"`
}

type page[T any] struct {
	Data    []T    `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// Sessions lists every cloud session, newest update first. Items with an id
// that is not safe as a file name are dropped.
func (c *Client) Sessions(ctx context.Context) ([]SessionInfo, error) {
	var out []SessionInfo
	after := ""
	for range maxPages {
		q := url.Values{}
		if after != "" {
			q.Set("after_id", after)
		}
		var p page[listItem]
		if err := c.get(ctx, "/sessions", q, &p); err != nil {
			return nil, err
		}
		for _, item := range p.Data {
			if info, ok := item.info(); ok {
				out = append(out, info)
			}
		}
		if !p.HasMore || p.LastID == "" || p.LastID == after {
			break
		}
		after = p.LastID
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (item listItem) info() (SessionInfo, bool) {
	if !safeID(item.ID) {
		return SessionInfo{}, false
	}
	info := SessionInfo{ID: item.ID, Title: item.Title, Status: item.Status}
	if info.Title == "" && item.Summary != nil {
		info.Title = item.Summary.Title
	}
	info.Model = item.Configured
	var outcomeBranch string
	if item.Context != nil {
		info.CWD = item.Context.CWD
		info.Model = firstNonEmpty(item.Context.Model, info.Model)
		for _, src := range item.Context.Sources {
			if info.Repo = repoFromURL(src.URL); info.Repo != "" {
				break
			}
		}
		for _, out := range item.Context.Outcomes {
			if out.Git == nil {
				continue
			}
			if info.Repo == "" {
				info.Repo = repoFromName(out.Git.Repo)
			}
			if len(out.Git.Branches) > 0 && outcomeBranch == "" {
				outcomeBranch = out.Git.Branches[0]
			}
		}
	}
	if item.External != nil {
		info.Model = firstNonEmpty(item.External.Model, info.Model)
		info.Branch = pickBranch(item.External.Branches)
	}
	info.Branch = firstNonEmpty(info.Branch, outcomeBranch)
	info.CreatedAt, _ = time.Parse(time.RFC3339Nano, item.CreatedAt)
	info.UpdatedAt, _ = time.Parse(time.RFC3339Nano, item.UpdatedAt)
	if info.UpdatedAt.IsZero() {
		info.UpdatedAt = info.CreatedAt
	}
	return info, true
}

// pickBranch returns the branch of the first repository by name, since a
// session may span several and the dashboard shows one.
func pickBranch(branches map[string]string) string {
	names := make([]string, 0, len(branches))
	for name := range branches {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if branches[name] != "" {
			return branches[name]
		}
	}
	return ""
}

// repoFromURL returns "owner/name" from a repository URL such as
// https://github.com/owner/name(.git), or "" when the URL has another shape.
func repoFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return repoFromName(u.Path)
}

// repoFromName normalizes "owner/name", with optional slashes around it and
// an optional ".git" suffix, and rejects anything that could be misused as a
// path: each part is limited to letters, digits, '.', '-' and '_', and may not
// be "." or "..".
func repoFromName(raw string) string {
	parts := strings.Split(strings.TrimSuffix(strings.Trim(raw, "/"), ".git"), "/")
	if len(parts) != 2 {
		return ""
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || len(p) > 100 {
			return ""
		}
		for _, c := range p {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
				return ""
			}
		}
	}
	return parts[0] + "/" + parts[1]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Event is one raw cloud event: the whole JSON object, plus its type.
type Event struct {
	Type      string
	UUID      string
	CreatedAt string
	Raw       json.RawMessage
}

// Events returns every event of a session in the API's order, oldest first.
func (c *Client) Events(ctx context.Context, sessionID string) ([]Event, error) {
	if !safeID(sessionID) {
		return nil, errors.New("claudecloud: invalid session id")
	}
	var out []Event
	after := ""
	for range maxPages {
		q := url.Values{"limit": {fmt.Sprint(eventsPageSize)}}
		if after != "" {
			q.Set("after_id", after)
		}
		var p page[json.RawMessage]
		if err := c.get(ctx, "/sessions/"+sessionID+"/events", q, &p); err != nil {
			return nil, err
		}
		for _, raw := range p.Data {
			var head struct {
				Type      string `json:"type"`
				UUID      string `json:"uuid"`
				CreatedAt string `json:"created_at"`
			}
			if json.Unmarshal(raw, &head) != nil {
				continue
			}
			out = append(out, Event{Type: head.Type, UUID: head.UUID, CreatedAt: head.CreatedAt, Raw: raw})
		}
		if !p.HasMore || p.LastID == "" || p.LastID == after {
			return out, nil
		}
		after = p.LastID
	}
	return nil, errors.New("claudecloud: too many event pages")
}

func (c *Client) get(ctx context.Context, path string, q url.Values, into any) error {
	u := c.base() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errors.New("claudecloud: build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.creds.token)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("x-organization-uuid", c.creds.OrgUUID)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The URL error text can include the request URL; keep the cause only.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return &transportError{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return &StatusError{Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return errors.New("claudecloud: read response")
	}
	if len(body) > maxResponseBytes {
		return errors.New("claudecloud: response too large")
	}
	if err := json.Unmarshal(body, into); err != nil {
		return errors.New("claudecloud: unexpected response format; the internal API may have changed")
	}
	return nil
}
