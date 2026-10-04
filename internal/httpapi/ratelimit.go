package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimiter is a small in-memory fixed-window limiter for unauthenticated
// endpoints. One instance per API process; state is intentionally local.
//
// Limits are deliberately conservative for auth/webhook/track because those run
// argon2id (64MiB x4), HMAC, or an unauthenticated database query.
//
// Two limits keep the memory bounded: buckets expire lazily, and the map is
// swept when it grows past maxBuckets so a long-running process cannot be
// grown without limit by rotating source addresses.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	maxKeys int
	sweptAt time.Time
}

type bucket struct {
	count     int
	windowEnd time.Time
}

// maxBuckets bounds the limiter's footprint. At 200k live buckets we start
// dropping expired ones; far beyond any legitimate client population.
const maxBuckets = 200_000

// NewRateLimiter constructs an empty limiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{buckets: map[string]*bucket{}}
}

// Check records one hit for key and reports whether it is within limit per
// window. When limited, retryAfter is how long to wait before retrying.
func (l *RateLimiter) Check(key string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.buckets == nil {
		l.buckets = map[string]*bucket{}
	}
	// Lazy sweep: cheap, amortised, and keeps a long-lived process bounded.
	if len(l.buckets) > maxBuckets || now.Sub(l.sweptAt) > 10*time.Minute {
		for k, b := range l.buckets {
			if now.After(b.windowEnd) {
				delete(l.buckets, k)
			}
		}
		l.sweptAt = now
	}

	b, ok := l.buckets[key]
	if !ok || !now.Before(b.windowEnd) {
		l.buckets[key] = &bucket{count: 1, windowEnd: now.Add(window)}
		return true, 0
	}
	if b.count < limit {
		b.count++
		return true, 0
	}
	return false, time.Until(b.windowEnd)
}

// Buckets reports the live bucket count, for metrics.
func (l *RateLimiter) Buckets() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// clientIP returns the caller's address for rate-limiting purposes.
//
// Forwarding headers are believed ONLY when the immediate peer is a configured
// trusted proxy. This matters: chi's RealIP middleware (and most naive
// implementations) unconditionally believe X-Forwarded-For from ANY client, so
// a caller could rotate that header per request and receive a fresh bucket
// every time — which makes the public tracking limiter, the thing standing
// between an attacker and enumeration of the whole tracking-number space,
// completely decorative.
func (s *Server) clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	if !s.cfg.IsTrustedProxy(peer) {
		return peer
	}
	// Peer is a trusted proxy: believe the outermost forwarded client.
	if s.cfg.TrustCloudflareHeaders {
		if v := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); v != "" {
			if ip := net.ParseIP(v); ip != nil {
				return ip.String()
			}
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		if ip := net.ParseIP(v); ip != nil {
			return ip.String()
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" {
		parts := strings.Split(v, ",")
		if ip := net.ParseIP(strings.TrimSpace(parts[0])); ip != nil {
			return ip.String()
		}
	}
	return peer
}

// clientKey derives a per-IP bucket key for a scope.
func (s *Server) clientKey(r *http.Request, scope string) string {
	return scope + "|" + s.clientIP(r)
}

// subjectHash is a stable, non-reversible fingerprint of a request subject
// (a tracking number, say). Bucketing on it as well as the IP means one abusive
// client cannot spread load over a pool by rotating addresses, and a wide
// enumeration attempt trips a single bucket.
func subjectHash(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// rateLimit wraps h with a fixed-window check. Limited requests get 429 +
// Retry-After so legitimate carriers/backoff loops behave.
func (s *Server) rateLimit(scope string, limit int, window time.Duration, h http.HandlerFunc) http.HandlerFunc {
	if s.limiter == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, retry := s.limiter.Check(s.clientKey(r, scope), limit, window); !ok {
			limitResponse(w, retry)
			return
		}
		h(w, r)
	}
}

// rateLimitSubject applies a second, independent budget keyed on the request
// subject rather than the source address. Used on the public tracking routes:
// enumeration is bounded by how fast a single tracking space can be walked, not
// by how many addresses an attacker controls.
func (s *Server) rateLimitSubject(scope string, limit int, window time.Duration, subject func(*http.Request) string, h http.HandlerFunc) http.HandlerFunc {
	if s.limiter == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if key := subjectHash(subject(r)); key != "" {
			if ok, retry := s.limiter.Check(scope+"|subject:"+key, limit, window); !ok {
				limitResponse(w, retry)
				return
			}
		}
		h(w, r)
	}
}

func limitResponse(w http.ResponseWriter, retry time.Duration) {
	secs := int(retry.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests, retry shortly")
}