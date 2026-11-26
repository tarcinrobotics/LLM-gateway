package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func mustParsePrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()

	prefixes, err := ParseTrustedProxyCIDRs(values)
	if err != nil {
		t.Fatalf("ParseTrustedProxyCIDRs(%v): %v", values, err)
	}

	return prefixes
}

func TestResolveClientIP(t *testing.T) {
	trusted := mustParsePrefixes(
		t,
		"10.0.0.0/8",
		"192.0.2.10/32",
	)

	tests := []struct {
		name      string
		remote    string
		forwarded []string
		want      string
	}{
		{
			name:      "direct request ignores spoofed header",
			remote:    "198.51.100.8:1234",
			forwarded: []string{"203.0.113.9"},
			want:      "198.51.100.8",
		},
		{
			name:      "untrusted proxy ignores forwarded header",
			remote:    "198.51.100.8:1234",
			forwarded: []string{"203.0.113.9"},
			want:      "198.51.100.8",
		},
		{
			name:      "trusted proxy accepts client address",
			remote:    "10.0.0.5:8080",
			forwarded: []string{"203.0.113.9"},
			want:      "203.0.113.9",
		},
		{
			name:      "trusted multi proxy chain",
			remote:    "10.0.0.5:8080",
			forwarded: []string{"203.0.113.9, 10.0.0.6"},
			want:      "203.0.113.9",
		},
		{
			name:      "spoofed value left of client is ignored",
			remote:    "10.0.0.5:8080",
			forwarded: []string{"198.51.100.77, 203.0.113.9, 10.0.0.6"},
			want:      "203.0.113.9",
		},
		{
			name:      "multiple header fields form one chain",
			remote:    "10.0.0.5:8080",
			forwarded: []string{"203.0.113.9", "10.0.0.6"},
			want:      "203.0.113.9",
		},
		{
			name:      "malformed address falls back to peer",
			remote:    "10.0.0.5:8080",
			forwarded: []string{"203.0.113.9, not-an-ip"},
			want:      "10.0.0.5",
		},
		{
			name:      "empty chain element falls back to peer",
			remote:    "10.0.0.5:8080",
			forwarded: []string{"203.0.113.9,,10.0.0.6"},
			want:      "10.0.0.5",
		},
		{
			name:      "all trusted hops fall back to direct peer",
			remote:    "10.0.0.5:8080",
			forwarded: []string{"10.0.0.6"},
			want:      "10.0.0.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveClientIP(
				tt.remote,
				tt.forwarded,
				trusted,
			)

			if got != tt.want {
				t.Errorf(
					"ResolveClientIP() = %q, want %q",
					got,
					tt.want,
				)
			}
		})
	}
}

func TestResolveClientIPIPv6(t *testing.T) {
	trusted := mustParsePrefixes(t, "2001:db8:ffff::/48")

	got := ResolveClientIP(
		"[2001:db8:ffff::1]:443",
		[]string{"2001:db8::25"},
		trusted,
	)

	if got != "2001:db8::25" {
		t.Fatalf("got %q, want %q", got, "2001:db8::25")
	}
}

func TestParseTrustedProxyCIDRsRejectsInvalidCIDR(t *testing.T) {
	_, err := ParseTrustedProxyCIDRs(
		[]string{"10.0.0.0/8", "not-a-cidr"},
	)

	if err == nil {
		t.Fatal("expected invalid CIDR to return an error")
	}
}

func TestLoggingMiddlewareSharesResolvedClientIPWithHandler(t *testing.T) {
	trusted := mustParsePrefixes(t, "10.0.0.0/8")

	var handlerIP string

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerIP = ClientIP(r)
		w.WriteHeader(http.StatusNoContent)
	})

	handler := LoggingMiddlewareWithTrustedProxies(next, trusted)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Add("X-Forwarded-For", "203.0.113.9")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if handlerIP != "203.0.113.9" {
		t.Fatalf(
			"handler saw client IP %q, want %q",
			handlerIP,
			"203.0.113.9",
		)
	}
}
