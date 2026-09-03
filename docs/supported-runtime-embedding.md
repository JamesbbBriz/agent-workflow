# Supported runtime embedding

Import these packages from a tagged Agent Workflow release:

```go
import (
    contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
    "github.com/JamesbbBriz/agent-workflow/workflow"
)
```

Construct one `workflow.Engine` with downstream implementations of the existing
provider, context, output, and atomic-ledger contracts. Treat it as a
`workflow.CampaignRuntime`; call `Preview` for a read-only decision, `Drive`
with `MaxTransitions: 1` from each durable delivery, and `ReplayAt` for an exact
audit cutoff.

For operator-authorized retirement, retain that same `*workflow.Engine` and
call `RetireBlockedCampaign` with a versioned `CampaignRetirementRequest` after
authenticating the operator and settling external effects. Preview the blocked
Campaign and confirm its exact last receipt hash. This additive supported
embedding method (ADR 0015) does not widen the existing `CampaignRuntime`
scheduling interface or cancel providers. A running or unfinished child is a
blocker, not permission to discard it. Its external-package compatibility test
is `workflow/campaign_retirement_test.go`.

The downstream `AtomicLedger` owns transaction serialization. Its
`AppendBatch` must compare aggregate version and previous receipt hash and
either commit every receipt or none. Provider `Start`, `Poll`, and `Cancel`
must preserve invocation identity across redelivery. Typed waits and blockers
are successful runtime state and must not be promoted into transport retries.

The compatibility boundary is exercised from the external `workflow_test`
package in `workflow/campaign_runtime_test.go`, `workflow/engine_test.go`, and
`workflow/replay_test.go`. Those tests cover bounded drive, atomic refusal,
context recovery, approval/wait resumption, provider redelivery, and exact
replay without importing internal packages.

The CLI, local file ledger, GUI, and bundled provider bridges are optional
reference adapters. PostgreSQL, SQLite, Restate, CMS, GSC, SERP, and
product-specific policy remain downstream concerns.
