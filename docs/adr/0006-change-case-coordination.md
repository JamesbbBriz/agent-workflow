# ADR 0006: Coordinate shared mutations with Change Cases

Status: accepted

## Context

Campaigns are independent execution aggregates, but their accepted Nodes may
propose changes to the same mutable resource. Applying those proposals directly
would permit stale targets, lost updates, conflicting writes, and duplicate
authorized effects.

## Decision

Use one domain-neutral Change Case aggregate keyed by exact `ResourceRef`
identity: resource type, stable ID, generation, baseline revision, and baseline
hash.

The Core materializes every Change Proposal from a completed canonical Campaign
Node Replay and its validated Action Artifact. Callers cannot supply evidence or
capability hashes. A registered pure merge adapter receives the accepted
proposals in hash order and returns either merged content or a typed Conflict
Set; it has no mutation authority.

Conflicts require a Resolution Artifact materialized from another completed
canonical Campaign Node proposal and an exact preview/confirm approval.
All accepted changes, including non-conflicting merges, require approval and one
time-bounded Mutation Lease before apply. A separate mutation adapter receives
that lease as its idempotency authority and must return an observed hash; Core
records apply and matching readback evidence. An advanced generation or changed
baseline fails closed.

Proposal replacement is explicit and reason typed (`rebase`, `resolver`, or
`human_implementation`). It is audit lineage only: it never suppresses another
Campaign's proposal or inherits authority. Conflicts still require a Resolution
Artifact and exact approval.

Change Case receipts use schema version 4 so historical Campaign and Workflow
receipts retain their existing semantics. Canvas is a read-only projection of
the Change Case Replay.

## Consequences

- Domains provide resource, merge, and mutation adapters without adding domain
  fields to the Core.
- A crash after proposal append is repaired by reconciliation; merge eligibility
  always rechecks the proposal's canonical source Replays.
- Apply redelivery reuses the same lease, and readback can resume from a
  canonical applied receipt without repeating the external effect.
- The Core does not provide a scheduler, resource store, CMS, or automatic
  approval policy.
