# ADR 0016: Expired invocation settlement

Status: Accepted for implementation (#58)

A Campaign duration limit can block its parent before an already dispatched
child reaches its terminal receipt. Retirement must not hide that invocation.
Add `Engine.SettleExpiredCampaignInvocations`, using the existing versioned
retirement request to bind operator authority and the exact blocked parent head.
It verifies each child's recorded admission and deadline, then asks the existing
Provider to cancel. Only successful cancellation permits the existing
`deadline_expired` terminal receipt. It never starts or polls a provider,
resolves new Context, admits a Workflow, or fabricates a result.

Provider `Cancel` success means execution is quiescent, not merely that a kill
request was queued. Errors leave the child unfinished. The shared deadline path
must honor the same guarantee. Ledger compare-and-append prevents concurrent
results being overwritten; partial settlement is recoverable by exact retry.
Completed children are unchanged. Unexpired children and stale or mismatched
parent requests fail closed. The host still settles vertical external effects
before invoking either settlement or retirement. Parent retirement remains a
separate explicit call and preserves the original failure.

No new receipt version, listener, scheduler, or CampaignRuntime interface is
needed: this is an additive operator API on the same Engine.
