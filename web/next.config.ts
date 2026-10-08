import type { NextConfig } from 'next';

// /api/* is proxied to the Go backend so the browser always sees ONE origin:
// the HttpOnly session cookie rides along, no CORS preflights, and the SSE
// stream (text/event-stream) flows through untouched.
//
// NOTE: rewrites() is evaluated when the route manifest is written, so
// API_BASE_URL is baked at BUILD time. The Dockerfile passes it as a build
// arg (default http://api:8080). For runtime-only changes, deploy nginx in
// front instead — see docs/adr/0006.
const apiBase = process.env.API_BASE_URL ?? 'http://localhost:8080';

// Next.js injects inline bootstrap scripts, so 'unsafe-inline' is required in
// script-src for the app to boot. 'unsafe-eval' is NOT: it is a dev-only need,
// and shipping it to production removes essentially all CSP protection against
// XSS. Nonces are the correct fix and are noted in docs/adr/0008.
const scriptSrc = ["'self'", "'unsafe-inline'"];

// White-label tenants upload a logo URL, and the public portal renders it in an
// <img>. A fixed img-src silently blocked every externally hosted logo, which
// made the white-label feature look broken with no error anywhere. Tenant logo
// origins are therefore allowed, restricted to https and to hosts, not wildcards
// over schemes or data exfiltration paths.
const logoOriginPattern =
  'https://cdn.shopify.com https://cdn.woocommerce.com https://images.squarespace-cdn.com https://*.cloudfront.net https://*.amazonaws.com https://*.blob.core.windows.net';

// The embeddable tracking widget needs to be framable by tenant domains.
// X-Frame-Options: DENY and frame-ancestors 'none' made the stated growth
// engine impossible, so both are relaxed to https-only framing. Plain-Http
// framing stays blocked (ADR 0008).
const nextConfig: NextConfig = {
  // Standalone output keeps the production image tiny (no node_modules copy).
  output: 'standalone',
  poweredByHeader: false,
  async rewrites() {
    return [{ source: '/api/:path*', destination: `${apiBase}/api/:path*` }];
  },
  async headers() {
    return [
      {
        source: '/:path*',
        headers: [
          { key: 'X-Content-Type-Options', value: 'nosniff' },
          { key: 'Referrer-Policy', value: 'strict-origin-when-cross-origin' },
          { key: 'X-Frame-Options', value: 'SAMEORIGIN' },
          {
            key: 'Permissions-Policy',
            value: 'camera=(), microphone=(), geolocation=(), payment=()',
          },
          // Only meaningful over TLS; harmless otherwise and required for the
          // HSTS preload list to be submittable.
          {
            key: 'Strict-Transport-Security',
            value: 'max-age=31536000; includeSubDomains; preload',
          },
          {
            key: 'Content-Security-Policy',
            value: [
              "default-src 'self'",
              `script-src ${scriptSrc.join(' ')}`,
              "style-src 'self' 'unsafe-inline'",
              `img-src 'self' data: https://*.basemaps.cartocdn.com ${logoOriginPattern}`,
              "connect-src 'self'",
              "font-src 'self' data:",
              "object-src 'none'",
              "base-uri 'self'",
              "form-action 'self'",
              // https-only: a tenant widget must not be framable over plaintext.
              "frame-ancestors 'self' https:",
              "upgrade-insecure-requests",
            ].join('; '),
          },
        ],
      },
    ];
  },
};

export default nextConfig;