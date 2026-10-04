package httpclient

import (
	"net"
	"testing"
)

// ValidateOutboundURL must reject every tenant-to-server SSRF shape while
// accepting ordinary public HTTPS endpoints. These cases exist so the audit
// findings behind them (carriers.go baseURL, webhooks_out.go endpoint URL)
// cannot regress: each row is an attack that worked before the validator.
func TestValidateOutboundURL(t *testing.T) {
	cases := []struct {
		name  string
		url   string
		valid bool
	}{
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"plain http", "http://example.com/track", false},
		{"gopher", "gopher://example.com/", false},
		{"file", "file:///etc/passwd", false},
		{"no scheme", "example.com/track", false},
		{"no host", "https://", false},
		{"embedded credentials", "https://user:pass@example.com/", false},
		{"localhost", "https://localhost:6379/", false},
		{"localhost uppercase", "https://LOCALHOST/", false},
		{"localhost subdomain", "https://x.localhost/", false},
		{"loopback v4", "https://127.0.0.1:5432/", false},
		{"loopback v6", "https://[::1]/", false},
		{"unspecified", "https://0.0.0.0/", false},
		{"rfc1918 10/8", "https://10.0.0.1/", false},
		{"rfc1918 192.168/16", "https://192.168.1.1/", false},
		{"rfc1918 172.16/12", "https://172.16.0.1/", false},
		{"link-local metadata", "https://169.254.169.254/latest/meta-data/", false},
		{"ipv6 ULA", "https://[fc00::1]/", false},
		{"ipv6 link-local", "https://[fe80::1]/", false},
		{".internal", "https://panel.internal/", false},
		{".local", "https://printer.local/", false},
		{"multicast", "https://224.0.0.1/", false},
		{"public https", "https://example.com/track?number=MAEU1", true},
		{"public https with port", "https://hooks.example.com:443/wh", true},
		{"trims whitespace", "  https://example.com/wh  ", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOutboundURL(tc.url)
			if tc.valid && err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("expected rejection of %q, got nil", tc.url)
			}
		})
	}
}

func TestIsNonPublicIP(t *testing.T) {
	private := []string{
		"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "::", "224.0.0.1", "fc00::1", "fe80::1",
	}
	for _, s := range private {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("cannot parse %q", s)
		}
		if !isNonPublicIP(ip) {
			t.Errorf("expected %s to be non-public", s)
		}
	}
	public := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"}
	for _, s := range public {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("cannot parse %q", s)
		}
		if isNonPublicIP(ip) {
			t.Errorf("expected %s to be public", s)
		}
	}
}
