// Package serverinstall downloads and installs the firekeeper-server binary
// from a release repository, after the user has said yes.
//
// It is standard library only. Nothing in it runs on its own: the caller
// (`firekeeper serve`) asks first, and the feature is off unless a release
// source is configured (see Resolve).
package serverinstall

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

const (
	// EnvURL names the environment variable that sets the release base URL
	// at run time. Setting it also enables the feature.
	EnvURL = "FIREKEEPER_SERVER_RELEASES_URL"

	// ProductionBase is the intended public, binaries-only release
	// repository. It is a constant for the owner to switch on, not a default.
	ProductionBase = "https://github.com/DanBradbury/firekeeper-server-releases"

	// BinaryName is the only executable the archives carry.
	BinaryName = "firekeeper-server"
)

// ReleasesURL is the build-time release base URL, set with
//
//	-ldflags "-X github.com/DanBradbury/firekeeper/internal/serverinstall.ReleasesURL=..."
//
// Empty means the feature is disabled. Release builds leave it empty.
var ReleasesURL = ""

// Source says where releases come from.
//
// Base is either ProductionBase, which maps to GitHub's API and download
// URLs, or any other base URL B, whose layout is:
//
//	B/latest                        JSON with a "tag_name" field
//	B/download/<tag>/<asset>        each release asset
type Source struct {
	Base string
	// AllowLoopbackHTTP permits plain http, but only to a loopback host. It
	// is set only for a base that came from EnvURL.
	AllowLoopbackHTTP bool
}

// Resolve returns the configured source. ok is false when the feature is
// disabled. The environment variable wins over the build-time value; an
// invalid value is an error, never a silent fallback.
func Resolve(getenv func(string) string) (src Source, ok bool, err error) {
	if v := strings.TrimSpace(getenv(EnvURL)); v != "" {
		src = Source{Base: strings.TrimRight(v, "/"), AllowLoopbackHTTP: true}
	} else if ReleasesURL != "" {
		src = Source{Base: strings.TrimRight(ReleasesURL, "/")}
	} else {
		return Source{}, false, nil
	}
	if err := src.validate(); err != nil {
		return Source{}, true, err
	}
	return src, true, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// urlAllowed reports whether u may be fetched: https always, http only to a
// loopback host and only when allowed.
func urlAllowed(u *url.URL, allowLoopbackHTTP bool) bool {
	switch u.Scheme {
	case "https":
		return u.Hostname() != ""
	case "http":
		return allowLoopbackHTTP && isLoopbackHost(u.Hostname())
	}
	return false
}

func (s Source) validate() error {
	u, err := url.Parse(s.Base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid release URL %q", s.Base)
	}
	if !urlAllowed(u, s.AllowLoopbackHTTP) {
		if u.Scheme == "http" {
			return errors.New("release URL must use https (plain http is accepted only for 127.0.0.1, ::1 or localhost, via " + EnvURL + ")")
		}
		return fmt.Errorf("release URL must use https: %q", s.Base)
	}
	return nil
}

// Host is the host shown to the user in the prompt.
func (s Source) Host() string {
	u, err := url.Parse(s.Base)
	if err != nil {
		return s.Base
	}
	return u.Host
}

// latestURL and downloadURL build the two kinds of request.
func (s Source) latestURL() string {
	if s.Base == ProductionBase {
		return "https://api.github.com/repos/DanBradbury/firekeeper-server-releases/releases/latest"
	}
	return s.Base + "/latest"
}

func (s Source) downloadURL(tag, asset string) string {
	// Production is GitHub's own layout: <repo>/releases/download/<tag>/<asset>.
	if s.Base == ProductionBase {
		return s.Base + "/releases/download/" + tag + "/" + asset
	}
	return s.Base + "/download/" + tag + "/" + asset
}
