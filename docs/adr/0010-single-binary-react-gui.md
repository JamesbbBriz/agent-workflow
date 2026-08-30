# ADR 0010: Optional single-binary React control plane

- Status: accepted
- Date: 2026-08-30

## Decision

The CLI remains the primary product path. `agent-workflow serve` embeds the existing React/Vite control plane in the Go binary and serves it on the same loopback listener as the canonical Builder API. The browser reads the same receipt ledger through versioned API projections; it gains no direct mutation authority.

Production assets are generated from `web/`, committed under `internal/webassets/dist/`, and checked for drift in CI. Contributors keep the separate Vite development server. Running a release binary requires neither Node.js nor a second web process.

The public UI uses the repository's existing React, Radix, Tailwind, React Flow, and open shadcn-style primitives. Paid shadcnblocks source is not redistributed.

## Security boundary

The listener remains restricted to an explicit loopback IP. Static responses set a same-origin Content Security Policy, deny framing and MIME sniffing, and use no referrer. `/v1` and `/v2` requests are delegated unchanged to the Go API; unknown browser routes fall back to the embedded application.

## Consequences

There is one generated asset copy in git so release builds are reproducible without a Node runtime. `npm run web:embed` refreshes it, and `npm run check:embedded-web` rejects drift. A remote-hosted GUI, SSR framework, theme marketplace runtime, and browser-owned workflow state remain out of scope.
