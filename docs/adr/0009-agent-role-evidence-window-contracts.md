# ADR 0009: Agent role and evidence-window contracts

## Status

Accepted.

## Decision

`AgentRoleCatalog@1` is descriptive metadata. A role lists the canonical
receipt types that prove it was invoked; it cannot grant capabilities, select
Nodes, or authorize effects.

`EvidenceWindowReport@1` is a deterministic projection of verified Replays for
an inclusive UTC window. It keeps available roles separate from roles actually
evidenced, counts context refreshes, invocations, approvals, effects, readbacks,
and outcomes, and links every counted receipt and Replay by canonical hash.

JSON is canonical. Markdown is a deterministic human-readable view. The local
`report` command opens the ledger read-only and never repairs or appends data.

## Consequences

- Vertical products may publish their own role catalogs without changing Core
  authority.
- Marketing claims can reference a stable evidence window instead of inferred
  activity or elapsed time alone.
- New receipt semantics require a versioned contract change; renderers do not
  become another source of truth.
