# ADR 0012: supported runtime embedding boundary

Status: accepted

## Context

Downstream products need to run Agent Workflow with their own durable storage,
provider isolation, context producers, and delivery systems. SEO Ops currently
imports only released conformance types and maintains a duplicate controller.

## Decision

`workflow.CampaignRuntime` is the single supported execution boundary:

- `Preview` derives the next legal action without mutation;
- `Drive` performs bounded canonical transitions;
- `ReplayAt` reconstructs one exact receipt prefix.

Downstream products provide existing `workflow` adapters: `AtomicLedger`,
`Provider`, context producers, output validators, clock, and approval authority
catalog. `AtomicLedger.AppendBatch` is the compare-and-append transaction: it
must validate aggregate version and previous hash and must not partially write.
Durable delivery calls `Drive` with one transition. Redelivery reuses canonical
state and provider identity; a typed wait is successful state, not a failed
delivery.

The supported import surface is `pkg/contractsv1`, `contracts`, `conformance`,
and `workflow`. CLI, GUI, file storage, and bundled provider processes are
reference adapters rather than required dependencies.

No Skill contract or runtime field is introduced by Runtime Convergence V0.9.

## Consequences

- Agent Workflow remains domain-neutral and never imports SEO Ops.
- A downstream product may replace infrastructure but not transition choice.
- Existing external-package runtime tests are compatibility tests for the
  embedding boundary.
- Historical receipts remain immutable; a migration cuts over only newly
  admitted Campaigns and retains legacy read/replay.
- Adding another runtime facade, scheduler, ledger, or provider abstraction
  requires a new ADR and concrete evidence that this boundary cannot hold.
