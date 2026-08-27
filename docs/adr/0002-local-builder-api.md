# ADR 0002: Local Builder API

Status: accepted

The visual Builder needs a real canonical preview/confirm boundary, but PR 4 does not introduce hosted identity or remote mutation authority. The reference server therefore binds loopback only and exposes a small standard-library JSON API over the generated v1 contracts. Drafts remain browser-local. Only the Go `AuthoringCore` can append admission or approval receipts.

The API rejects oversized, non-JSON, unknown-field, stale-token, altered-preview, and actor-mismatch requests. A remote listener, authentication, multi-tenant storage, and WebMCP discovery are outside this decision; PR 5 may adapt the same Core without becoming a second authority.
