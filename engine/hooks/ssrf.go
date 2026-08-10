package hooks

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// SSRF guard for HTTP hooks.
//
// Blocks private, link-local, and other non-routable address ranges to prevent
// project-configured HTTP hooks from reaching cloud metadata endpoints
// (169.254.169.254) or internal infrastructure.
//
// Loopback (127.0.0.0/8, ::1) is intentionally ALLOWED — local dev policy
// servers are a primary HTTP hook use case.
//
// Reference: src/utils/hooks/ssrfGuard.ts (294 lines)

// IsBlockedAddress returns true if the address is in a range that HTTP hooks
// should not reach.
//
// Blocked IPv4:
//
//	0.0.0.0/8        "this" network
//	10.0.0.0/8       private
//	100.64.0.0/10    shared address space / CGNAT
//	169.254.0.0/16   link-local (cloud metadata)
//	172.16.0.0/12    private
//	192.168.0.0/16   private
//
// Blocked IPv6:
//
//	::               unspecified
//	fc00::/7         unique local
//	fe80::/10        link-local
//	::ffff:<v4>      mapped IPv4 in a blocked range
//
// Allowed (returns false):
//
//	127.0.0.0/8      loopback (local dev hooks)
//	::1              loopback
//	everything else
func IsBlockedAddress(address string) bool {
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}

	if v4 := ip.To4(); v4 != nil {
		return isBlockedV4(v4)
	}
	return isBlockedV6(ip)
}

func isBlockedV4(ip net.IP) bool {
	a := ip[0]
	b := ip[1]

	// Loopback explicitly allowed
	if a == 127 {
		return false
	}

	// 0.0.0.0/8
	if a == 0 {
		return true
	}
	// 10.0.0.0/8
	if a == 10 {
		return true
	}
	// 169.254.0.0/16 — link-local, cloud metadata
	if a == 169 && b == 254 {
		return true
	}
	// 172.16.0.0/12
	if a == 172 && b >= 16 && b <= 31 {
		return true
	}
	// 100.64.0.0/10 — shared address space (RFC 6598, CGNAT)
	if a == 100 && b >= 64 && b <= 127 {
		return true
	}
	// 192.168.0.0/16
	if a == 192 && b == 168 {
		return true
	}

	return false
}

func isBlockedV6(ip net.IP) bool {
	// Ensure we have a 16-byte representation
	ip = ip.To16()
	if ip == nil {
		return false
	}

	// ::1 loopback explicitly allowed
	if ip.Equal(net.IPv6loopback) {
		return false
	}

	// :: unspecified
	if ip.Equal(net.IPv6unspecified) {
		return true
	}

	// IPv4-mapped IPv6 (::ffff:X.X.X.X)
	if v4 := ip.To4(); v4 != nil {
		return isBlockedV4(v4)
	}

	// fc00::/7 — unique local addresses (fc00:: through fdff::)
	if ip[0] == 0xfc || ip[0] == 0xfd {
		return true
	}

	// fe80::/10 — link-local
	if ip[0] == 0xfe && (ip[1]&0xc0) == 0x80 {
		return true
	}

	return false
}

// SSRFError is returned when an HTTP hook target resolves to a blocked address.
type SSRFError struct {
	Hostname string
	Address  string
}

func (e *SSRFError) Error() string {
	return fmt.Sprintf(
		"HTTP hook blocked: %s resolves to %s (private/link-local address). "+
			"Loopback (127.0.0.1, ::1) is allowed for local dev.",
		e.Hostname, e.Address,
	)
}

// SSRFGuardedDialer returns a net.Dialer-compatible DialContext function that
// validates resolved addresses against the SSRF blocklist before connecting.
// This ensures the validated IP is the one the socket connects to — no
// rebinding window between validation and connection.
func SSRFGuardedDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
			port = ""
		}

		// If host is already an IP literal, validate directly
		if ip := net.ParseIP(host); ip != nil {
			if IsBlockedAddress(host) {
				return nil, &SSRFError{Hostname: host, Address: host}
			}
			return dialer.DialContext(ctx, network, addr)
		}

		// Resolve hostname and check all addresses
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return nil, err
		}

		if len(addrs) == 0 {
			return nil, fmt.Errorf("SSRF guard: no addresses found for %s", host)
		}

		for _, a := range addrs {
			if IsBlockedAddress(a) {
				return nil, &SSRFError{Hostname: host, Address: a}
			}
		}

		// Connect to the first resolved address to prevent TOCTOU
		connectAddr := addrs[0]
		if port != "" {
			connectAddr = net.JoinHostPort(addrs[0], port)
		}
		return dialer.DialContext(ctx, network, connectAddr)
	}
}

// ValidateHookURL checks if a hook URL targets a blocked address.
// Returns nil if the URL is safe, or an error if blocked.
func ValidateHookURL(rawURL string) error {
	host := extractHost(rawURL)
	if host == "" {
		return nil
	}

	// Check if host is a direct IP
	if ip := net.ParseIP(host); ip != nil {
		if IsBlockedAddress(host) {
			return &SSRFError{Hostname: host, Address: host}
		}
		return nil
	}

	// Resolve and check
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if IsBlockedAddress(a) {
			return &SSRFError{Hostname: host, Address: a}
		}
	}
	return nil
}

func extractHost(rawURL string) string {
	// Strip scheme
	url := rawURL
	if idx := strings.Index(url, "://"); idx >= 0 {
		url = url[idx+3:]
	}
	// Strip path
	if idx := strings.IndexAny(url, "/?#"); idx >= 0 {
		url = url[:idx]
	}
	// Strip userinfo
	if idx := strings.LastIndex(url, "@"); idx >= 0 {
		url = url[idx+1:]
	}
	// Strip port
	host, _, err := net.SplitHostPort(url)
	if err != nil {
		return url
	}
	return host
}
