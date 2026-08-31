# ADR 0013: provider-owned effects and outcome routing

Status: accepted

## Context

Some deterministic Nodes perform product-owned effects such as CMS delivery or
readback. Treating every deterministic Node as a Core no-op bypasses the
provider, while inferring `completed_no_action` from an empty artifact list
makes routing depend on output shape instead of the executor's decision.

## Decision

`NodeDefinition.execution_mode` may mark an `agent` or `deterministic` Node as
`provider` owned. Agent Nodes remain provider owned by default; omitted mode on
existing deterministic Nodes remains Core owned.

`ProviderResult` may explicitly return `completed`, `completed_no_action`, or
`blocked`, but the Node must declare that outcome in `outcome_routes`. Core,
not the provider, selects the declared `continue`, `complete_branch`, or `stop`
route. A blocked result must bind a blocker code admitted by the Node. Core
records the outcome, route, and blocker in the provider execution and Campaign
receipts and verifies them again from the exact child Replay. A
`complete_branch` route skips only dependent Nodes; independent DAG branches
continue. Omitted outcome preserves the v0.2 artifact inference and continue
route for historical providers.

Context recovery, pending provider polling, deadlines, wait Nodes, and approval
Nodes keep their existing Core-owned paths. This change does not introduce a
second retry or signal state machine.

## Consequences

- Product effect adapters can execute behind the supported `CampaignRuntime`.
- Outcome routing is auditable and cannot be manufactured by a delivery caller.
- Existing definitions and provider results retain their previous meaning.
- Dynamic waiting and retry remain represented by existing runtime primitives,
  not by terminal provider results.
