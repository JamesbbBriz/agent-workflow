# Runtime Convergence V0.9

Status: accepted

Date: 2026-08-31

## Outcome

Agent Workflow becomes the single generic Workflow runtime imported by SEO
Ops. SEO Ops remains the commercial product and owns SEO policy, data sources,
effects, measurement, and operational delivery. This milestone does not add a
Skill contract or Skill runtime behavior.

## Product boundary

```text
Agent Workflow
  contracts, compilation, Campaign/Node reducer, invocation identity,
  provider protocol, generic receipts, Change Case, Replay
                    |
                    v
SEO Ops
  SEO definitions and policy, Context producers, storage adapters,
  Restate delivery, CMS effects/readback, GSC/SERP, Page Case policy,
  portfolio admission, reports, Day 7/14/28 measurement
```

Agent Workflow never imports SEO Ops. SEO Ops does not fork, vendor, subtree,
or copy the generic reducer. Infrastructure can vary behind existing adapters;
transition authority cannot.

## Canonical execution seam

`workflow.CampaignRuntime` is the only execution seam:

- `Preview` derives the next action without mutation;
- `Drive` performs bounded canonical transitions;
- `ReplayAt` reconstructs an exact receipt prefix.

One `Drive(MaxTransitions: 1)` call is one durable-delivery unit. Restate may
redeliver a unit. The atomic ledger and provider invocation identity make that
redelivery safe. Typed waits, including missing or stale Context, are committed
runtime state; they are not HTTP failures or panic/retry signals.

## Supported downstream adapters

SEO Ops supplies implementations of the existing contracts:

- `AtomicLedger`: compare aggregate version and previous hash, then commit the
  complete receipt batch or none;
- `Provider`: preserve Start/Poll/Cancel identity across redelivery;
- Context producers and output validators;
- clock and approval authority catalog;
- projections from generic receipts into SEO Ops read models.

No second scheduler, queue, ledger, provider system, runtime facade, or
permanent feature flag is introduced.

## State crosswalk

| Agent Workflow | SEO Ops | Rule |
|---|---|---|
| Campaign execution aggregate | CampaignRun | one writable authority |
| WorkflowDefinition | pinned Workflow registry entry | exact version/hash |
| NodeDefinition | NodeContract | every cutover transition mapped |
| IntentCard / Intent Chain | IntentCard / IntentChain | immutable input |
| ContextPackEdition / ContextBundle | Context Pack / ContextBundleV2 | exact edition/hash |
| CapabilityManifest | permissions/capability manifest | no authority widening |
| Invocation | NodeInvocation | stable redelivery identity |
| Receipt / Replay | run events, receipts, Replay | exact cutoff preserved |

An unmapped status, transition, wait, blocker, budget meter, receipt, or
terminal state blocks cutover. Matching final status or similar logs is not
semantic parity.

## Five pull requests

### PR 1: supported embeddable runtime

Repository: Agent Workflow.

- publish this design and ADR 0012;
- declare `CampaignRuntime` as the supported import boundary;
- document atomic compare-and-append and durable redelivery obligations;
- rely on the existing external-package tests for Preview, bounded Drive,
  Context recovery, approval/wait, provider retry, and exact Replay;
- let deterministic product effects opt into provider execution and bind an
  explicit completed, no-action, or blocked outcome plus its Core-declared
  route to the child Replay;
- preserve all existing hashes, fixtures, CLI, GUI, and provider behavior.

### PR 2: read-only shadow parity

Repository: SEO Ops.

- import the released Agent Workflow runtime;
- translate the deterministic synthetic fixture in memory;
- run both reducers from identical immutable inputs;
- compare next action, Node, typed wait/blocker, usage, invocation identity,
  accepted output hashes, receipts, terminal state, and Replay cutoff;
- fail closed with a machine-readable divergence report;
- grant the shadow path no network, provider, project-data, scheduling, ledger,
  or mutation authority.

### PR 3: production adapters, dark

Repository: SEO Ops.

- implement the atomic ledger adapter over the authoritative repository
  transaction;
- connect existing provider, Context, output, clock, approval, and Restate
  delivery seams;
- project generic receipts into existing SEO Ops read models;
- keep the path dark: no admitted production Campaign selects it.

### PR 4: new-Campaign cutover

Repository: SEO Ops.

- make Agent Workflow the only authority for newly admitted Campaigns;
- leave historical Campaigns immutable and replayable through the legacy
  reader;
- stop and explicitly exclude the polluted evidence window;
- admit four bounded Kinmed Jobs and start a fresh 200-hour evidence window;
- verify preflight, redelivery, Context recovery, provider execution, artifact
  acceptance, reporting, and rollback before unattended scheduling resumes.

Rollback stops new admission and reverts the binary. It does not reinterpret or
dual-write a Campaign already admitted by Agent Workflow.

### PR 5: duplicate Controller deletion

Repository: SEO Ops.

- remove the old writable generic Campaign/Node reducer, invocation identity,
  and duplicate generic receipt/Replay code;
- retain only the minimum immutable historical read/replay projection;
- keep SEO policy and production adapters downstream;
- prove CLI, API, GUI, scheduler, Restate, reports, and Replay all project the
  imported runtime's canonical state.

Migration is incomplete until this deletion merges.

## Compatibility and release

- SEO Ops imports a tagged Agent Workflow release; no local `replace` is
  allowed in a production commit.
- Existing Workflow, invocation, receipt, and Replay hashes do not change.
- Existing Campaign receipts are never reinterpreted under the new runtime.
- Unknown versions, fields, references, hashes, or authority mappings fail
  before provider execution.
- Every PR runs repository generation checks, full tests, vet, build, and diff
  checks from a clean checkout.

## Review gate

Each fixed head SHA receives Standards and Spec review. A reproducible
Blocker/Critical/P1 blocks merge. Ordinary non-blocking P2 findings are recorded
as debt and do not cause an unbounded review loop. Security, data loss,
canonical or Replay divergence, privilege expansion, and irreversible migration
are blocking regardless of the assigned label.

## Exit evidence

Runtime convergence is complete only when:

1. all five PRs are merged;
2. new Campaign admission reaches only Agent Workflow;
3. historical replay remains exact and read-only;
4. preflight and Restate runtime readback are healthy;
5. a fresh four-Job evidence window records the exact runtime version;
6. the prior polluted window remains retained but excluded from outcome claims.

Skill Layer V1 is a separate future decision and is not an exit condition.
