# ADR 0001 — Go for the backend, Next.js for the frontend

**Status:** accepted · **Date:** 2026-10-01

## Context
TrackSphere ingests high-volume carrier scan events, fans them out to browsers,
and runs background work (rules, notifications, ETA). The team has enterprise
experience and prefers strict typing and long-term scalability. Hosting is
Hetzner/DigitalOcean with a strong "reduce heavy technologies" constraint.

## Decision
- Backend: **Go**, single module, standard library HTTP with the `chi` router.
  Two entrypoints from one image: `cmd/api` and `cmd/worker`.
- Frontend: **Next.js 15 (App Router) + React 19 + TypeScript in strict mode**.
  Next (not plain React/Vite) because the app needs SSR for the public tracking
  page (SEO, fast first paint for customers with no session) and because the
  `/api/*` rewrite gives the browser a single origin, so the HttpOnly session
  cookie works with no CORS configuration and the SSE stream passes through
  untouched.

## Consequences
- `CGO_ENABLED=0` static binaries → ~17 MB images, instant cold starts, trivial
  horizontal scaling.
- One language for all event processing keeps the pipeline readable.
- Next adds a Node runtime to the stack; accepted for the SSR + same-origin gains.
  The dashboard is a client component tree, so Next is used mostly as a router
  and build tool there.
- `rewrites()` is baked at build time, so `API_BASE_URL` is a build arg (see ADR
  0006). Runtime-configurable routing is a nginx concern.

## Alternatives considered
- **Plain React + Vite SPA**: simpler, but loses SSR for the public portal and
  needs a separate reverse proxy to unify origins.
- **Java/Spring or .NET**: strong enterprise fit; heavier memory footprint and
  slower to a small, cheap deployment target than Go.
