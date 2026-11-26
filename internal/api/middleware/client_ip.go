package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type clientIPContextKey struct{}

// ParseTrustedProxyCIDRs validates configured trusted-proxy CIDR ranges.
func ParseTrustedProxyCIDRs(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf(
				"invalid trusted proxy CIDR %q: %w",
				value,
				err,
			)
		}

		prefixes = append(prefixes, prefix.Masked())
	}

	return prefixes, nil
}

func isTrustedProxy(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

func parsePeerAddress(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}

	return addr.Unmap(), true
}

// ResolveClientIP returns the direct peer unless that peer is explicitly
// trusted. For trusted peers, it validates every X-Forwarded-For address,
// then walks the chain right-to-left and returns the first untrusted address.
// Any malformed chain falls back to the direct peer.
func ResolveClientIP(
	remoteAddr string,
	forwardedHeaders []string,
	trusted []netip.Prefix,
) string {
	peer, ok := parsePeerAddress(remoteAddr)
	if !ok {
		return remoteAddr
	}

	directIP := peer.String()

	if !isTrustedProxy(peer, trusted) || len(forwardedHeaders) == 0 {
		return directIP
	}

	var chain []netip.Addr

	// Validate the entire forwarded chain before trusting any element.
	for _, header := range forwardedHeaders {
		for _, item := range strings.Split(header, ",") {
			item = strings.TrimSpace(item)

			if item == "" {
				return directIP
			}

			addr, err := netip.ParseAddr(item)
			if err != nil {
				return directIP
			}

			chain = append(chain, addr.Unmap())
		}
	}

	if len(chain) == 0 {
		return directIP
	}

	currentPeer := peer

	// Walk from the nearest proxy towards the original client.
	for i := len(chain) - 1; i >= 0; i-- {
		if !isTrustedProxy(currentPeer, trusted) {
			break
		}

		currentPeer = chain[i]

		if !isTrustedProxy(currentPeer, trusted) {
			return currentPeer.String()
		}
	}

	// No untrusted client boundary was identified.
	return directIP
}

// ClientIP retrieves the resolved client IP from request context.
func ClientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPContextKey{}).(string); ok && ip != "" {
		return ip
	}

	if addr, ok := parsePeerAddress(r.RemoteAddr); ok {
		return addr.String()
	}

	return r.RemoteAddr
}

func withClientIP(r *http.Request, ip string) *http.Request {
	ctx := context.WithValue(r.Context(), clientIPContextKey{}, ip)
	return r.WithContext(ctx)
}
