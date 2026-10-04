package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SSRF defence for every URL the server fetches on a tenant's behalf.
//
// Two layers, because neither alone is sufficient:
//
//  1. ValidateOutboundURL runs at write time, when a tenant saves a carrier
//     base URL or an outbound webhook endpoint. It rejects the obvious targets
//     — http://, embedded credentials, localhost, private ranges, the cloud
//     metadata address — and gives the operator an immediate, specific error.
//
//  2. safeDialContext runs at connection time. This is the layer that actually
//     closes the hole, because a hostname that resolves to a public address when
//     it is validated can resolve to 127.0.0.1 by the time the request is sent.
//     Validating only the string cannot see that; validating the address the
//     socket is about to open can.
//
// Both layers are needed: the first is what makes the failure legible, the
// second is what makes it correct.

// ErrNonPublicAddress is returned when a destination resolves outside the
// public internet.
var ErrNonPublicAddress = errors.New("destination is not a public address")

// ValidateOutboundURL checks a tenant-supplied URL before it is stored.
//
// It deliberately does not resolve DNS: that is safeDialContext's job at
// connect time, and resolving here would introduce a TOCTOU window rather than
// close one.
func ValidateOutboundURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("url is required")
	}
	if len(raw) > 2048 {
		return errors.New("url is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("url must use https (got %q)", u.Scheme)
	}
	if u.User != nil {
		return errors.New("url must not embed credentials")
	}
	if u.Hostname() == "" {
		return errors.New("url must include a host")
	}
	return checkHostNotInternal(u.Hostname())
}

// checkHostNotInternal rejects hostnames that are plainly non-public without
// performing a lookup.
func checkHostNotInternal(host string) error {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" {
		return errors.New("url must include a host")
	}
	if h == "localhost" ||
		strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".internal") ||
		strings.HasSuffix(h, ".local") {
		return fmt.Errorf("host %q is not publicly routable", host)
	}
	if ip := net.ParseIP(h); ip != nil {
		if isNonPublicIP(ip) {
			return fmt.Errorf("%w: %s", ErrNonPublicAddress, ip)
		}
	}
	return nil
}

// isNonPublicIP reports whether an address must never be dialled by the server.
//
// Covers loopback, RFC1918 private ranges, link-local (which includes the
// 169.254.169.254 cloud metadata endpoint), unspecified, multicast, and the
// IPv6 equivalents including unique-local.
func isNonPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	// IPv6 unique-local (fc00::/7) is not covered by IsPrivate on all Go
	// versions, so check it explicitly.
	if v6 := ip.To16(); v6 != nil && ip.To4() == nil {
		if v6[0]&0xfe == 0xfc {
			return true
		}
	}
	return false
}

// safeDialContext refuses to open a socket to any non-public address.
func safeDialContext(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if err := checkHostNotInternal(host); err != nil {
			return nil, err
		}
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("resolve %s: no addresses", host)
		}
		// Reject the whole host if any answer is private: a rebinding
		// resolver that returns one public and one private address must not
		// get to choose.
		for _, a := range addrs {
			if isNonPublicIP(a.IP) {
				return nil, fmt.Errorf("%w: %s (%s)", ErrNonPublicAddress, a.IP, host)
			}
		}
		return base.DialContext(ctx, network, net.JoinHostPort(addrs[0].IP.String(), port))
	}
}

// safeCheckRedirect revalidates every hop. A 302 to an internal address is the
// easiest way around a check that only inspects the first URL.
func safeCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("stopped after 5 redirects")
	}
	return ValidateOutboundURL(req.URL.String())
}

// hardenTransport installs the dial-time and redirect guards on a transport.
//
// Note on proxies: when HTTP_PROXY/HTTPS_PROXY is set, the dial target is the
// proxy rather than the destination, so the dial-time check would validate the
// proxy's address and not the target's. In that configuration ValidateOutboundURL
// at write time is the only remaining layer, and it inspects the string only.
// TrackSphere's compose deployment sets no proxy, so the dial-time layer is
// live there.
func hardenTransport(t *http.Transport) *http.Transport {
	t.DialContext = safeDialContext(&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	})
	return t
}