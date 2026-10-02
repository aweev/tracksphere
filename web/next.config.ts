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

const nextConfig: NextConfig = {
  // Standalone output keeps the production image tiny (no node_modules copy).
  output: 'standalone',
  async rewrites() {
    return [{ source: '/api/:path*', destination: `${apiBase}/api/:path*` }];
  },
};

export default nextConfig;
