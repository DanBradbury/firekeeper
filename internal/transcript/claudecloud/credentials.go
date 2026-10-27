// Package claudecloud backfills Claude Code on the web ("cloud") sessions.
//
// Cloud sessions run on Anthropic's infrastructure, so no transcript exists
// on disk. This package fetches them through the same internal API the web
// app uses, writes each one as a JSONL file in a Firekeeper-owned cache
// directory (never in ~/.claude), and reads those files back with the Claude
// Code parser. From there they take the normal redact, batch and ingest path.
//
// The API is undocumented and may change without notice; see NOTES.md. It is
// reached only by `firekeeper backfill --claude-cloud`, never by the
// dashboard, the daemon, or any other command, and it authenticates with the
// user's own Claude Code login. The token is held in memory for the run, sent
// only to the API host, and never logged, cached, or uploaded.
package claudecloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Credentials authenticate API requests as the signed-in Claude Code user.
// The token is a secret: String, GoString and %v print only a placeholder.
type Credentials struct {
	Token   string
	OrgUUID string
}

func (Credentials) String() string   { return "claudecloud.Credentials{redacted}" }
func (Credentials) GoString() string { return "claudecloud.Credentials{redacted}" }

// Credential errors. They carry no path, token, or command output.
var (
	// ErrUnsupportedPlatform means Claude Code's login cannot be read here.
	ErrUnsupportedPlatform = errors.New("claudecloud: reading the Claude Code login is supported on macOS only")
	// ErrNoLogin means no Claude Code login was found.
	ErrNoLogin = errors.New("claudecloud: no Claude Code login found; sign in with `claude` first")
	// ErrNoOrganization means ~/.claude.json has no organization id.
	ErrNoOrganization = errors.New("claudecloud: no organization id in ~/.claude.json; sign in with `claude` first")
)

const (
	// keychainService is the generic-password item Claude Code stores its
	// OAuth login in on macOS.
	keychainService = "Claude Code-credentials"
	// maxCredentialBytes bounds the keychain item; maxConfigBytes bounds
	// ~/.claude.json, which also holds per-project history and can be large.
	maxCredentialBytes = 1 << 20
	maxConfigBytes     = 64 << 20
)

// goos, readKeychain, and the home lookup are replaced in tests so no test
// reads the real keychain or home directory.
var (
	goos         = runtime.GOOS
	readKeychain = securityFindPassword
)

// LoadCredentials reads the Claude Code OAuth token from the macOS keychain
// and the organization id from home/.claude.json. Both belong to the user and
// are read, never written. home is the user's home directory.
func LoadCredentials(ctx context.Context, home string) (Credentials, error) {
	if goos != "darwin" {
		return Credentials{}, ErrUnsupportedPlatform
	}
	raw, err := readKeychain(ctx, os.Getenv("USER"))
	if err != nil {
		if ctx.Err() != nil {
			return Credentials{}, ctx.Err()
		}
		return Credentials{}, ErrNoLogin
	}
	token := tokenFromKeychain(raw)
	if token == "" {
		return Credentials{}, ErrNoLogin
	}
	org, err := organizationFromConfig(filepath.Join(home, ".claude.json"))
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{Token: token, OrgUUID: org}, nil
}

// securityFindPassword runs macOS `security` with an argument array and
// returns its standard output. Failures drop the command's output, which may
// hold the secret.
func securityFindPassword(ctx context.Context, account string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-a", account, "-s", keychainService, "-w")
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("keychain lookup failed")
	}
	if len(out) > maxCredentialBytes {
		return nil, errors.New("keychain item too large")
	}
	return out, nil
}

// tokenFromKeychain extracts claudeAiOauth.accessToken from the keychain
// item, or returns "" when it is missing or malformed.
func tokenFromKeychain(raw []byte) string {
	var item struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &item) != nil {
		return ""
	}
	return item.OAuth.AccessToken
}

func organizationFromConfig(path string) (string, error) {
	data, err := readCapped(path)
	if err != nil {
		return "", ErrNoOrganization
	}
	var cfg struct {
		Account struct {
			OrganizationUUID string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(data, &cfg) != nil || cfg.Account.OrganizationUUID == "" {
		return "", ErrNoOrganization
	}
	if !safeID(cfg.Account.OrganizationUUID) {
		return "", fmt.Errorf("%w (unexpected format)", ErrNoOrganization)
	}
	return cfg.Account.OrganizationUUID, nil
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigBytes {
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
