package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParseTrustedProxies reads proxy addresses and CIDR ranges such as
// "10.0.0.5", "172.29.77.0/24" or "fd00::/8". An empty list is valid and
// means no proxy is trusted. A prefix that covers every address ("0.0.0.0/0"
// or "::/0") is refused: it would let any client forge its own address.
func ParseTrustedProxies(specs []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("trusted proxy: empty value")
		}
		var p netip.Prefix
		if strings.Contains(s, "/") {
			var err error
			if p, err = netip.ParsePrefix(s); err != nil {
				return nil, fmt.Errorf("trusted proxy %q: not an IP address or CIDR range", s)
			}
		} else {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: not an IP address or CIDR range", s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), unmapBits(p)).Masked()
		if p.Bits() == 0 {
			return nil, fmt.Errorf("trusted proxy %q covers every address; list the proxy's own address or network", s)
		}
		out = append(out, p)
	}
	return out, nil
}

// unmapBits keeps the prefix length meaningful when an IPv4-mapped IPv6
// prefix such as ::ffff:10.0.0.0/104 is turned into plain IPv4.
func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() {
		if b := p.Bits() - 96; b >= 0 {
			return b
		}
		return 0
	}
	return p.Bits()
}

// WithTrustedProxies declares the reverse proxies whose X-Forwarded-For
// header is believed. With none (the default) the header is ignored and the
// connection's own address identifies the client.
func WithTrustedProxies(p []netip.Prefix) Option {
	return func(m *Middleware) { m.proxies = append([]netip.Prefix(nil), p...) }
}

func (m *Middleware) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range m.proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP returns the address that identifies the caller to the limiters.
//
// The peer is r.RemoteAddr. Only when the peer is a trusted proxy is
// X-Forwarded-For consulted: its entries, across every header line, are
// walked from the right, and the first one that is not itself a trusted
// proxy is the client. Entries further left are supplied by the client and
// never believed. A missing or unparsable header, or one made only of
// trusted proxies, falls back to the peer.
//
// The result is a limiter key: IPv4-mapped IPv6 is unmapped, and an IPv6
// address is reduced to its /64 because a single subscriber normally holds a
// whole /64 and could otherwise rotate through addresses to dodge the limit.
func (m *Middleware) clientIP(r *http.Request) string {
	peer, ok := parseHostPort(r.RemoteAddr)
	if !ok {
		// Unknown peer: keep the old behaviour of keying on the raw string.
		return r.RemoteAddr
	}
	if len(m.proxies) > 0 && m.trusted(peer) {
		var hops []string
		for _, h := range r.Header.Values("X-Forwarded-For") {
			hops = append(hops, strings.Split(h, ",")...)
		}
		for i := len(hops) - 1; i >= 0; i-- {
			a, ok := parseHostPort(strings.TrimSpace(hops[i]))
			if !ok {
				// A garbled hop means the chain cannot be trusted past it.
				break
			}
			if !m.trusted(a) {
				return limiterKey(a)
			}
		}
	}
	return limiterKey(peer)
}

// parseHostPort parses "ip", "ip:port", "[ip]:port", "[ip]" and IPv6 zones.
func parseHostPort(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.WithZone("").Unmap(), true
}

func limiterKey(a netip.Addr) string {
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}
