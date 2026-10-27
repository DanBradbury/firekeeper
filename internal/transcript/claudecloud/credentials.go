// Package claudecloud imports Claude Code on the web ("cloud") sessions.
//
// Cloud sessions run on Anthropic's infrastructure, so no transcript exists
// on disk. This package fetches them through the same internal API the web
// app uses, writes each one as a JSONL file in a Firekeeper-owned cache
// directory (never in ~/.claude), and reads those files back with the Claude
// Code parser. From there they take the normal redact, batch and ingest path.
//
// The API is undocumented and may change without notice; see NOTES.md. It is
// reached only by `firekeeper backfill --claude-cloud` and by the daemon when
// the user turns on `[claude_cloud] enabled = true`; never by the dashboard,
// `report`, or any other command. It authenticates with the user's own
// Claude Code login, read from the macOS keychain or, on Linux and Windows,
// from Claude Code's ~/.claude/.credentials.json. The token is held in memory
// for one pass, sent only to the API host, and never logged, cached, written,
// refreshed, or uploaded.
package claudecloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Credentials authenticate API requests as the signed-in Claude Code user.
// The token is a secret: String, GoString and %v print only a placeholder.
type Credentials struct {
	Token   string
	OrgUUID string
	// Expires is when the access token stops working, or zero when the
	// stored login did not say.
	Expires time.Time
}

func (Credentials) String() string   { return "claudecloud.Credentials{redacted}" }
func (Credentials) GoString() string { return "claudecloud.Credentials{redacted}" }

// Credential errors. They carry no path contents, token, or command output.
var (
	// ErrNoLogin means no Claude Code login was found.
	ErrNoLogin = errors.New("claudecloud: no Claude Code login found; sign in with `claude` first")
	// ErrTokenExpired means the stored access token is past its expiry.
	// Firekeeper never refreshes it: that would rotate Claude Code's own
	// login. Running `claude` once refreshes the stored token.
	ErrTokenExpired = errors.New("claudecloud: the Claude Code login has expired; run `claude` once to refresh it, then try again")
	// ErrNoOrganization means ~/.claude.json has no organization id.
	ErrNoOrganization = errors.New("claudecloud: no organization id in ~/.claude.json; sign in with `claude` first")
	// ErrUnreadableLogin means the credentials file exists but could not be
	// read, for example because of its permissions.
	ErrUnreadableLogin = errors.New("claudecloud: the Claude Code credentials file could not be read")
)

const (
	// keychainService is the generic-password item Claude Code stores its
	// OAuth login in on macOS.
	keychainService = "Claude Code-credentials"
	// credentialsFile is where Claude Code keeps the same JSON on Linux and
	// Windows, inside its config directory.
	credentialsFile = ".credentials.json"
	envConfigDir    = "CLAUDE_CONFIG_DIR"
	// maxCredentialBytes bounds the keychain item and credentials file;
	// maxConfigBytes bounds ~/.claude.json, which also holds per-project
	// history and can be large.
	maxCredentialBytes = 1 << 20
	maxConfigBytes     = 64 << 20
	// expirySkew treats a token about to expire as expired, so a pass does
	// not start with a token that dies halfway.
	expirySkew = time.Minute
)

// CredentialOptions replace the real environment in tests. The zero value
// reads the real keychain, environment, and clock.
type CredentialOptions struct {
	// GOOS replaces runtime.GOOS.
	GOOS string
	// Getenv replaces os.Getenv.
	Getenv func(string) string
	// Keychain returns the macOS keychain item's secret, or an error when it
	// is absent. Nil runs `security find-generic-password`.
	Keychain func(context.Context) ([]byte, error)
	// Now replaces time.Now for the expiry check.
	Now func() time.Time
}

// LoadCredentials reads the user's Claude Code login. home is the user's
// home directory. Both files and the keychain item are read, never written.
//
// Token sources, first match wins:
//
//  1. macOS only: the keychain item "Claude Code-credentials".
//  2. CLAUDE_CONFIG_DIR/.credentials.json, default ~/.claude/.credentials.json,
//     which is where Claude Code keeps the login on Linux and Windows (and on
//     macOS when the keychain is unavailable).
//
// The organization id is oauthAccount.organizationUuid in
// CLAUDE_CONFIG_DIR/.claude.json when that exists, else ~/.claude.json.
//
// An expired token returns ErrTokenExpired; it is never refreshed.
func LoadCredentials(ctx context.Context, home string) (Credentials, error) {
	return LoadCredentialsWith(ctx, home, CredentialOptions{})
}

// LoadCredentialsWith is LoadCredentials with injected dependencies.
func LoadCredentialsWith(ctx context.Context, home string, opts CredentialOptions) (Credentials, error) {
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	configDir := strings.TrimSpace(getenv(envConfigDir))

	var (
		tok   login
		found bool
	)
	if goos == "darwin" {
		keychain := opts.Keychain
		if keychain == nil {
			keychain = securityFindPassword
		}
		raw, err := keychain(ctx)
		if err != nil && ctx.Err() != nil {
			return Credentials{}, ctx.Err()
		}
		if err == nil {
			tok, found = parseLogin(raw)
		}
	}
	if !found {
		dir := configDir
		if dir == "" {
			dir = filepath.Join(home, ".claude")
		}
		raw, err := readCapped(filepath.Join(dir, credentialsFile), maxCredentialBytes)
		switch {
		case err == nil:
			tok, found = parseLogin(raw)
		case errors.Is(err, fs.ErrNotExist):
		default:
			return Credentials{}, ErrUnreadableLogin
		}
	}
	if !found {
		return Credentials{}, ErrNoLogin
	}
	if !tok.expires.IsZero() && !tok.expires.After(now().Add(expirySkew)) {
		return Credentials{}, ErrTokenExpired
	}
	org, err := organization(home, configDir)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{Token: tok.token, OrgUUID: org, Expires: tok.expires}, nil
}

// securityFindPassword runs macOS `security` with an argument array and
// returns its standard output. Failures drop the command's output, which may
// hold the secret.
func securityFindPassword(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-a", os.Getenv("USER"), "-s", keychainService, "-w")
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("keychain lookup failed")
	}
	if len(out) > maxCredentialBytes {
		return nil, errors.New("keychain item too large")
	}
	return out, nil
}

// login is the part of Claude Code's stored login that is used.
type login struct {
	token   string
	expires time.Time
}

// parseLogin reads claudeAiOauth.accessToken and claudeAiOauth.expiresAt
// (Unix milliseconds) from the JSON Claude Code stores in the keychain item
// and in .credentials.json. The refresh token in the same object is never
// read. A missing or malformed login returns false.
func parseLogin(raw []byte) (login, bool) {
	var item struct {
		OAuth *struct {
			AccessToken string          `json:"accessToken"`
			ExpiresAt   json.RawMessage `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &item) != nil || item.OAuth == nil {
		return login{}, false
	}
	token := strings.TrimSpace(item.OAuth.AccessToken)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return login{}, false
	}
	out := login{token: token}
	var ms float64
	if len(item.OAuth.ExpiresAt) > 0 && json.Unmarshal(item.OAuth.ExpiresAt, &ms) == nil && ms > 0 {
		out.expires = time.UnixMilli(int64(ms))
	}
	return out, true
}

// organization reads the organization id from Claude Code's global config.
func organization(home, configDir string) (string, error) {
	paths := []string{filepath.Join(home, ".claude.json")}
	if configDir != "" {
		paths = append([]string{filepath.Join(configDir, ".claude.json")}, paths...)
	}
	for _, path := range paths {
		data, err := readCapped(path, maxConfigBytes)
		if err != nil {
			continue
		}
		var cfg struct {
			Account struct {
				OrganizationUUID string `json:"organizationUuid"`
			} `json:"oauthAccount"`
		}
		if json.Unmarshal(data, &cfg) != nil || cfg.Account.OrganizationUUID == "" {
			continue
		}
		if !safeID(cfg.Account.OrganizationUUID) {
			return "", fmt.Errorf("%w (unexpected format)", ErrNoOrganization)
		}
		return cfg.Account.OrganizationUUID, nil
	}
	return "", ErrNoOrganization
}

// readCapped reads at most limit bytes of path. It accepts any file mode:
// Claude Code writes .credentials.json as 0600, but a looser mode is the
// user's choice and does not stop the import.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file too large")
	}
	return data, nil
}

// safeID reports whether id is safe to use as a file name and header value:
// 1 to 128 ASCII letters, digits, '-' or '_'.
func safeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
