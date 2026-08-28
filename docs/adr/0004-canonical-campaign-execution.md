# ADR 0004: Canonical Campaign execution kernel

Status: proposed

## Context

The initial slices proved versioned definitions, Context compilation, one bounded Agent Node, admission, Canvas, Builder, and WebMCP. They did not yet provide the canonical runtime that advances a whole Workflow DAG, enforces aggregate budgets, records recoverable Context blockers, or owns multiple Campaigns under one Job.

SEO Ops already dogfoods these responsibilities, but its reducers and scheduling policy also encode SEO-specific evidence, publication, release, and measurement rules. Copying that state machine would make the public Core an SEO product with renamed types.

## Decision

Add one deep Core module with this external interface:

```go
type CampaignRuntime interface {
    Preview(ctx context.Context, ref CampaignRef) (DrivePreview, error)
    Drive(ctx context.Context, command DriveCommand) (DriveReceipt, error)
    ReplayAt(ctx context.Context, ref CampaignRef, cutoff ReceiptID, view ReplayView) (Replay, error)
}
```

`Drive` owns readiness, dependency ordering, Context resolution, budget reservation, Node dispatch, result acceptance, waits, approvals, and terminal closure. Callers may bound how many transitions one delivery performs, but may not select an unready Node or manufacture a transition.

Job, Campaign, and Workflow remain independent immutable definition aggregates. A Job does not embed mutable Campaign history. A Campaign references its Job and pins a Workflow plan. The Campaign execution aggregate records runtime transitions for that admitted Campaign.

Conflicts over a shared mutation target do not belong to either Campaign aggregate. A separate Change Case aggregate is keyed by a stable Resource Ref (`type`, `id`, `generation`, and baseline revision/hash). Campaign Nodes may submit immutable Change Proposals to it, but only the Change Case can accept a merge, Conflict Set, Resolution Artifact, Mutation Lease, apply authorization, or readback. This is the domain-neutral extraction of SEO Ops Page Case; page URLs, CMS fields, and SEO merge policy remain adapters.

The minimum canonical transition vocabulary is:

- Campaign admitted;
- Node invocation reserved;
- Context Bundle bound or Node blocked with exact resume requirements;
- provider execution accepted;
- Node result accepted and Node terminal state committed atomically;
- wait started and signal/time wake accepted;
- approval requested and exact decision accepted;
- Campaign completed, completed-no-action, blocked-terminal, or failed-terminal.

Readiness is derived from the pinned DAG plus accepted receipts; it is not caller input. `max_attempts` is reserved before provider Start. `max_actions` limits accepted Action Artifacts; `max_candidates` counts records only from output Slots that explicitly set `counts_as_candidates`. Duration and Campaign totals are checked before every external effect and before result acceptance. Exhaustion records a typed blocker rather than returning an endlessly retryable error.

Approval authority requires a completed upstream Node transition. A provider result that was written without the matching canonical Node completion cannot be previewed or approved.

A stale Resource generation never silently retargets a Campaign. It records a typed `resource_generation_advanced` blocker and requires an explicit refresh or rebase. Resolver output is only a proposal: it must bind the exact Conflict Set hash, baseline, source proposals, scope, and capability receipt. Replacement lineage is a typed Core event with reason-specific invariants, never an evidence string supplied by a caller.

Raw receipts remain immutable. `ReplayAt` selects an exact canonical prefix. Redaction is an authorized projection that preserves receipt IDs and hashes while replacing or omitting classified fields and emitting a redaction proof.

## Compatibility

Existing v1 definitions, admissions, one-Node Replays, and Builder ledgers remain readable. New required identities and state-machine fields use v2 schemas. A compatibility adapter may project a valid v1 one-Node execution as a completed v2 Campaign execution, but it may not reinterpret or append to the old aggregate.

The existing `RunNode` entry point remains temporarily as a compatibility adapter over `Drive`; it cannot remain an independent execution path.

## Consequences

- The public Core gains a real whole-Workflow runtime without importing SEO policy.
- CLI, Canvas, Builder, MCP, WebMCP, and future storage adapters share one execution interface.
- SEO Ops can prove conformance through exported public fixtures while retaining its own scheduler, release, and measurement policy.
- Cross-Campaign mutation conflicts have one auditable coordination path without turning every Campaign into a global lock.
- Runtime work is larger than the initial single-Node slice, but complexity stays behind one interface instead of spreading across adapters.
