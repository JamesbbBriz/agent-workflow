# ADR 0002: Local Builder API

Status: accepted

The visual Builder needs a real canonical preview/confirm boundary, but PR 4 does not introduce hosted identity or remote mutation authority. The reference server therefore binds loopback only and exposes a small standard-library JSON API over the generated v1 contracts. Drafts remain browser-local. Only the Go `AuthoringCore` can append admission or approval receipts.

The admission preview and receipt freeze the complete Job, Campaign, Workflow, catalog, and compiled contract. The execution engine resolves that admission from its trusted ledger before provider work and carries the admission receipt hash into the compile and invocation chain. Approval accepts only an execution aggregate ID and resolves the source Replay from the same trusted ledger at preview and confirm time; callers cannot submit a self-authored Replay as authority.

The API rejects oversized, non-JSON, unknown-field, stale-token, altered-preview, and actor-mismatch requests. A remote listener, authentication, multi-tenant storage, and WebMCP discovery are outside this decision; PR 5 may adapt the same Core without becoming a second authority.
