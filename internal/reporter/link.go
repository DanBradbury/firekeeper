package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client side of machine linking and token self-service: the device-code
// flow behind `firekeeper login`, `whoami`, and `logout`. None of these
// functions put a token or device code into an error or a log line.

var (
	// ErrLinkExpired means the code outlived its ten minutes.
	ErrLinkExpired = errors.New("the link code expired before it was approved")
	// ErrUnauthorized means the server refused the token: revoked, wrong
	// server, or its account was disabled.
	ErrUnauthorized = errors.New("the server does not accept this token")
)

// maxLinkResponse bounds what the client reads from the server.
const maxLinkResponse = 1 << 20

// CleanServer validates a dashboard base URL and returns it without a
// trailing slash. A server that is not on this machine must use https, so a
// token never crosses a network in the clear.
func CleanServer(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid server URL %q", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopbackHost(u.Hostname()) {
			return "", fmt.Errorf("server %q must use https unless it is on this machine", raw)
		}
	default:
		return "", fmt.Errorf("invalid server URL %q", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// noRedirects keeps a bearer token on the URL the user named.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func linkClient(c *http.Client) *http.Client {
	if c == nil {
		return &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirects}
	}
	return c
}

// apiError is a refusal from the server. Its text is the server's own
// message, which never carries credentials.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("server said %d %s: %s", e.Status, e.Code, e.Message)
}

// call sends one JSON request and decodes a 2xx body into out. A non-2xx
// answer becomes an *apiError.
func call(ctx context.Context, c *http.Client, method, endpoint, token string, in, out any) (http.Header, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("build request")
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// url.Error would repeat the URL; the host is enough to act on.
		return nil, fmt.Errorf("could not reach %s", req.URL.Host)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxLinkResponse))
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("%s redirected the request; use the server's final URL", req.URL.Host)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct{ Error, Code string }
		_ = json.Unmarshal(raw, &e)
		if len(e.Error) > 200 {
			e.Error = e.Error[:200]
		}
		return resp.Header, &apiError{Status: resp.StatusCode, Code: e.Code, Message: e.Error}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, errors.New("the server sent a reply this client cannot read")
		}
	}
	return resp.Header, nil
}

// LinkOptions configures Link.
type LinkOptions struct {
	// Server is the dashboard base URL; see CleanServer.
	Server string
	// MachineID is this machine's id (~/.firekeeper/machine-id).
	MachineID string
	// MachineName is the name offered on the approval page.
	MachineName string
	Client      *http.Client
	// Out receives the instructions. Nil discards them.
	Out io.Writer
	// Interval overrides the polling interval the server asks for.
	Interval time.Duration
}

// LinkResult is a completed link.
type LinkResult struct {
	Server       string
	Token        string
	AccountEmail string
	MachineID    string
	MachineName  string
}

// Link runs the device-code flow: it asks the server for a code, tells the
// person where to approve it, and polls until the server hands over an
// ingest token for this machine.
func Link(ctx context.Context, o LinkOptions) (LinkResult, error) {
	server, err := CleanServer(o.Server)
	if err != nil {
		return LinkResult{}, err
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	c := linkClient(o.Client)

	var start struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	if _, err := call(ctx, c, http.MethodPost, server+"/v1/link/start", "", map[string]string{"machine_id": o.MachineID}, &start); err != nil {
		return LinkResult{}, err
	}
	if start.DeviceCode == "" || start.UserCode == "" {
		return LinkResult{}, errors.New("the server sent an incomplete link code")
	}

	frag := url.Values{"code": {start.UserCode}}
	if o.MachineName != "" {
		frag.Set("name", o.MachineName)
	}
	minutes := (start.ExpiresIn + 59) / 60
	fmt.Fprintf(out, "To link this machine, sign in and open:\n\n  %s/link#%s\n\n", server, frag.Encode())
	fmt.Fprintf(out, "Check that the page shows the code %s, name the machine, and approve.\n", start.UserCode)
	fmt.Fprintf(out, "Waiting for approval (the code expires in %d minutes)...\n", minutes)

	interval := o.Interval
	if interval <= 0 {
		interval = time.Duration(start.Interval) * time.Second
		if interval <= 0 {
			interval = 2 * time.Second
		}
	}
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return LinkResult{}, ctx.Err()
		case <-time.After(interval):
		}
		var poll struct {
			Status  string `json:"status"`
			Token   string `json:"token"`
			Account struct {
				Email string `json:"email"`
			} `json:"account"`
			Machine struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"machine"`
		}
		hdr, err := call(ctx, c, http.MethodPost, server+"/v1/link/poll", "", map[string]string{"device_code": start.DeviceCode}, &poll)
		var ae *apiError
		switch {
		case err == nil:
			fails = 0
			if poll.Status != "approved" {
				continue
			}
			if poll.Token == "" {
				return LinkResult{}, errors.New("the server approved the link but sent no token")
			}
			return LinkResult{Server: server, Token: poll.Token, AccountEmail: poll.Account.Email,
				MachineID: poll.Machine.ID, MachineName: poll.Machine.Name}, nil
		case errors.As(err, &ae) && ae.Code == "link_expired":
			return LinkResult{}, ErrLinkExpired
		case errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests:
			if s, perr := strconv.Atoi(hdr.Get("Retry-After")); perr == nil && s > 0 && s <= 120 {
				select {
				case <-ctx.Done():
					return LinkResult{}, ctx.Err()
				case <-time.After(time.Duration(s) * time.Second):
				}
			}
		case errors.As(err, &ae):
			return LinkResult{}, err
		default:
			// A dropped connection should not lose a code the person is
			// about to approve; give up only after several in a row.
			if fails++; fails >= 5 {
				return LinkResult{}, err
			}
		}
	}
}

// Identity is who a token reports as.
type Identity struct {
	AccountEmail string
	MachineID    string
	// Name is the machine name the token was created with.
	Name  string
	Scope string
}

// Whoami asks the server who token belongs to.
func Whoami(ctx context.Context, server, token string, client *http.Client) (Identity, error) {
	server, err := CleanServer(server)
	if err != nil {
		return Identity{}, err
	}
	var r struct {
		Email string `json:"email"`
		Token *struct {
			Name      string `json:"name"`
			Scope     string `json:"scope"`
			MachineID string `json:"machine_id"`
		} `json:"token"`
	}
	if _, err := call(ctx, linkClient(client), http.MethodGet, server+"/v1/account", token, nil, &r); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			return Identity{}, ErrUnauthorized
		}
		return Identity{}, err
	}
	id := Identity{AccountEmail: r.Email}
	if r.Token != nil {
		id.Name, id.Scope, id.MachineID = r.Token.Name, r.Token.Scope, r.Token.MachineID
	}
	return id, nil
}

// RevokeSelf asks the server to revoke the token it is called with. A token
// the server already rejects counts as revoked.
func RevokeSelf(ctx context.Context, server, token string, client *http.Client) error {
	server, err := CleanServer(server)
	if err != nil {
		return err
	}
	_, err = call(ctx, linkClient(client), http.MethodDelete, server+"/v1/tokens/current", token, nil, nil)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		return nil
	}
	return err
}
