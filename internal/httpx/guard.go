package httpx

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
)

// GuardUpstreamURL provides a basic SSRF guard: only http(s) URLs are allowed and
// the host must not resolve to a loopback/private/link-local address. It is
// applied as defence in depth before the proxy fetches a resolved stream.
func GuardUpstreamURL(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid upstream URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("upstream URL must be http(s)")
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("upstream URL missing host")
	}

	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip.Unmap()}
	} else {
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return fmt.Errorf("resolve upstream host: %w", err)
		}
		ips = make([]netip.Addr, 0, len(resolved))
		for _, ip := range resolved {
			ips = append(ips, ip.Unmap())
		}
	}

	if slices.ContainsFunc(ips, DisallowedIP) {
		return fmt.Errorf("upstream host resolves to a disallowed address")
	}
	return nil
}

// DisallowedIP reports whether an upstream IP is in a forbidden range. It is an
// exported package var (not a plain function) so tests — including those in other
// packages that run a loopback httptest upstream — can relax it.
var DisallowedIP = func(ip netip.Addr) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
