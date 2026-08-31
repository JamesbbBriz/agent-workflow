# ADR 0014: Hash-bound cross-Workflow artifact inputs

## Status

Accepted.

## Decision

`WorkflowDefinition.inputs` declares Action Artifact slots supplied by an
earlier Workflow in the same pinned Campaign plan. A Node may consume a slot
from a direct dependency or from its Workflow's declared inputs. The compiler
rejects undeclared consumers, incompatible slots, and ambiguous producers.

Campaign Runtime resolves the exact completed producer Replay, materializes
its Action Artifacts, and places them in `Invocation.inputs`. Their content
hashes participate in the idempotency key, `input_hashes`, redelivery checks,
and public Replay verification. Only `approved` and `not_required` artifacts
may reach a provider. Pending artifacts remain reserved for an Approval Node;
rejected and stale artifacts fail before provider start.
Core-owned deterministic, wait, and terminal Nodes cannot declare Action
Artifact outputs because they have no canonical artifact materialization path.

The field is optional in the v1 JSON contract so existing Workflow documents
and historical input-free Replays remain valid. No second artifact store or
transport is introduced: the canonical Ledger and Replay remain authoritative.

## Consequences

Multi-Workflow Campaigns can pass bounded research and decisions without
converting them into ambient Context Packs. A Workflow can only read artifacts
that its own contract names and that an earlier pinned Workflow exported.
