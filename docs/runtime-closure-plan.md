# Runtime closure and SEO Ops extraction plan

## Outcome

The intended product is a domain-neutral, auditable control plane:

```text
human-owned Job
  -> many independently admitted Campaigns
    -> pinned Workflow DAGs
      -> Core-selected Nodes
        -> exact Context Bundle + Capability Manifest + budget
          -> provider proposal
            -> Core result acceptance / human approval / wait / terminal
              -> Change Case coordination for shared mutation targets
              -> exact-cutoff Replay + Canvas readback
```

Context controls what an Agent knows. Capability controls what it may attempt. The Core alone decides what becomes canonical. A GUI, provider, MCP surface, or scheduler may request progress but may not choose an invalid transition.

## What is already dogfooded in SEO Ops

The missing public features are mostly extraction work, not speculative invention. The SEO Ops implementation is the behavioral reference, not source code to copy.

| Required behavior | SEO Ops evidence | Public Core status | Extraction decision |
| --- | --- | --- | --- |
| Job owns multiple Campaigns | generic Jobs project independent Campaigns per archetype | one Campaign in the Canvas/Builder shape | add Campaign collection/read model; keep definitions independent |
| Campaign contains multiple ordered Workflows | compiled Workflow plan records producer dependencies and advances one Campaign aggregate across Workflow refs | definitions contain a DAG, runtime executes one caller-selected Node | generalize dependency readiness behind `Drive` |
| Node dependency enforcement | `CampaignRunStateV2.ReadyNode` checks every `DependsOn` status | `RunNode` accepts a caller-selected Node | remove Node choice from the public runtime command |
| bounded attempts and work | Campaign budgets, `CanAttempt`, canonical attempt-budget-exhausted blocker, candidate/selection/promotion usage | budget is passed to the invocation but not enforced | reserve and record generalized usage in the execution aggregate |
| typed Context pause/resume | `NodeResult` has `needs_context`, exact requirements, blocker fingerprint, and resume condition | resolver returns an error only | make blocker and recovery canonical transitions |
| approval/effect authority | SEO effects are gated by canonical results, review, authorization, release, and readback receipts | approval can accept a result-only partial Replay | require atomic Node completion authority before approval |
| exact historical Replay | replay is built from an exact ledger prefix and cutoff event | only complete aggregate Replay is public | add `ReplayAt` with prefix verification |
| safe shareable Replay | SEO Ops projects bounded receipts/artifacts and applies redaction plus canary checks | raw payload is returned | add a separate redacted projection and proof |
| provider isolation | hosted SEO worker uses an exact staged workspace and a sandboxed provider process | arbitrary in-process Provider is trusted | define an isolation profile and ship a reference subprocess adapter; do not claim hostile-code isolation for in-process adapters |
| long waits | canonical Wakeup Plans plus durable runtime; cron only repairs delivery | no whole-Workflow wait driver | add wait/signal Nodes to the same execution aggregate; timer remains an adapter |
| shared-resource conflict resolution | Page Case groups proposals by Page identity/generation, holds an exclusive mutation lease, records conflict hashes, and uses preview/confirm resolver or human queues | no cross-Campaign mutation aggregate | add domain-neutral Change Case; keep page/CMS semantics in SEO Ops |

SEO-specific behavior remains outside the public Core: keyword/SERP/GSC contracts, Page Case and URL lineage, Content PR review, CMS publication, release-relative Day 7/14/28 measurement, operating Cycles, and portfolio scheduling policy.

## Canonical model

### Definitions

- `JobDefinition@v2`: stable intent and policy only.
- `CampaignDefinition@v2`: `job_id`, frozen scope/evidence frontier, Campaign budget, and ordered pinned Workflow refs.
- `WorkflowDefinition@v2`: reusable DAG and explicit Node contracts.
- `NodeContract@v2`: kind, dependencies, Context requirements, capabilities, output contracts, budget, completion/no-action/blocker semantics.

Adding a Campaign does not rewrite the Job. Updating a Workflow creates a new immutable version. An admitted Campaign keeps its pinned versions.

### Runtime aggregate

One Campaign execution aggregate owns:

- pinned Job/Campaign/Workflow hashes and admission receipt;
- Node state and accepted dependencies;
- Context Bundle editions and evidence cutoff;
- attempt and aggregate usage counters;
- pending waits and approval requests;
- accepted provider/result/output receipts;
- terminal outcome and next safe action.

Only the reducer determines `ReadyNode`. `Drive` performs at most the requested bounded number of transitions, reloads canonical state before each transition, and uses aggregate revision plus idempotency key for crash convergence.

### Budget meaning

- `max_attempts`: provider invocations reserved for one Node.
- `max_actions`: accepted Action Artifacts emitted by one Node.
- `max_candidates`: accepted candidate records across declared candidate outputs.
- `max_duration_seconds`: wall-clock deadline from the first reservation, not per redelivery.
- Campaign budget: sum/limit over Node usage and concurrent work.

Provider-reported usage is evidence, not authority. Core counts accepted attempts and output objects itself. Any paid-token or external-call amount that cannot be independently verified is recorded as reported usage and may tighten, but never widen, the Core limit.

### Context state

Context resolution has three canonical outcomes:

1. `context.bound`: exact verified Bundle; Node may run.
2. `node.needs_context`: exact missing/stale/partial/invalid requirements and a stable blocker fingerprint; no provider call.
3. `context.available`: a new verified edition changes the blocker fingerprint/frontier and makes the same Node eligible again.

No generic retry loop represents missing Context.

### Approval state

An approval request binds:

- completed source Node and accepted result receipt;
- exact Action Artifact and evidence refs;
- comparison, recommendation, risks, and proposed action;
- actor, authority class, revision, expiry, and commit token.

The decision becomes canonical before Canvas readback. Reject, revise, and approve are distinct outcomes. Approval never executes the external effect; it authorizes a later capability-bound Node.

### Shared-resource conflicts

Campaign isolation is not sufficient when two Campaigns propose changes to the same page, file, account, deployment, or other mutable resource. The public Core therefore uses a separate Change Case aggregate:

```text
Campaign A completed Node -- Change Proposal A --\
                                                -> Change Case(Resource Ref, baseline)
Campaign B completed Node -- Change Proposal B --/        |
                                                          +-- deterministic merge
                                                          +-- Conflict Set
                                                                -> resolver/human Resolution Artifact
                                                                -> review and exact approval
                                                          -> Mutation Lease
                                                          -> apply receipt
                                                          -> public/system readback
```

The generic contract includes:

- `ResourceRef {type, id, generation, baseline_revision, baseline_hash}`;
- `ChangeProposal {source_campaign, source_node_result, resource_ref, change_set, preserve_set, evidence, risk, capability}`;
- `ChangeCase` head, accepted proposal set, merge result or exact Conflict Set;
- `ResolutionArtifact` bound to the exact baseline, Conflict Set, proposals, and allowed fields;
- typed replacement lineage for rebase, resolver, or human implementation successors;
- `MutationLease` and apply/readback receipts bound to one Resource generation.

The Core validates identities, generations, hashes, lineage, exclusivity, and authority. A registered merge adapter may understand a domain-specific change format and deterministically return merged bytes or conflicts; it cannot mutate the resource or accept its own result. A resolver Agent can propose a Resolution Artifact, but human or declared reviewer authority accepts it. If the Resource generation advances, affected Campaigns stop with `resource_generation_advanced`; they never follow a new URL or target silently.

Cross-aggregate crash safety is recovery-based: a Change Case proposal event binds an already accepted Campaign Node result. If the process crashes between the two appends, reconciliation may materialize that exact missing link idempotently. Merge/apply eligibility requires both ledgers, so an orphan proposal can never be consumed early.

### Replay and audit

The full chain is:

```text
Definition hashes
-> Compile receipt
-> Admission receipt
-> Context editions / Bundle hash
-> Budget reservation
-> Invocation / provider receipt
-> accepted Node result and outputs
-> review / approval / wait receipts
-> effect and readback receipts when applicable
-> Campaign terminal receipt
-> Replay cutoff and redaction proof
```

Raw Replay is authorized diagnostic data. Redacted Replay uses a versioned policy, preserves canonical IDs/hashes, exposes no private provider references or secrets, and lists excluded classes. Future events never affect an earlier cutoff.

## Delivery plan

These are seven product slices. The final conformance slice requires one PR in each repository, so implementation is eight repository PRs.

### PR A — Campaign execution aggregate and bounded driver ([#12](https://github.com/JamesbbBriz/agent-workflow/issues/12))

- add v2 execution/event/receipt schemas and generated bindings;
- add reducer and `Preview`/`Drive` interface;
- derive Node readiness from dependencies;
- atomically reserve attempts and enforce Node/Campaign budgets;
- adapt `RunNode` through the new driver;
- reject direct execution of an unready Node.

Acceptance: a two-branch DAG cannot run a dependent Node early; redelivery does not duplicate provider work; attempt/action/candidate/duration limits produce canonical typed exhaustion.

### PR B — Node semantics, Context recovery, and approval authority ([#13](https://github.com/JamesbbBriz/agent-workflow/issues/13))

- support deterministic, Agent, approval, wait/signal, and terminal Nodes through internal handlers;
- persist `needs_context` and exact resume requirements;
- require completed source Node authority for approval;
- make result acceptance and Node completion one atomic transition;
- project next safe action from canonical state.

Acceptance: missing Context does not invoke a provider; new Context resumes the same Node; result-only partial Replay cannot be approved; timers/signals and human decisions converge after restart.

### PR C — Multi-Campaign Job authoring and Canvas ([#14](https://github.com/JamesbbBriz/agent-workflow/issues/14))

- add Job portfolio and Campaign collection response schemas without embedding mutable Campaigns in Job definitions;
- let Builder draft/select multiple Campaigns and their pinned Workflow plans;
- show configured, admitted, running, blocked, and terminal as distinct Canvas states;
- keep v1 single-Campaign read compatibility.

Acceptance: one Job can show two independently admitted Campaigns with different scopes, budgets, Workflow versions, Context, blockers, and history; neither Campaign can mutate or satisfy the other's execution.

### PR D — exact-cutoff Replay and provider isolation profile ([#15](https://github.com/JamesbbBriz/agent-workflow/issues/15))

- add `ReplayAt` and canonical prefix verification;
- add versioned redaction projection and proof;
- add isolation profile to provider execution receipts;
- ship a reference subprocess adapter with explicit working directory, empty-by-default environment, input/output roots, deadline, and cancellation;
- retain in-process Provider only as a trusted/testing profile.

Acceptance: cutoff N never includes N+1; raw private values do not enter a redacted Replay; production-profile admission rejects a provider without the required isolation profile; the reference provider cannot read a canary outside its staged root.

### PR E — Change Case and conflict resolution ([#18](https://github.com/JamesbbBriz/agent-workflow/issues/18))

- add Resource Ref, Change Proposal, Change Case, Conflict Set, Resolution Artifact, Mutation Lease, apply, and readback schemas;
- admit only proposals backed by completed canonical Node results;
- support deterministic no-conflict merge through a registered adapter;
- route conflicts to resolver or human approval without granting either direct mutation authority;
- enforce exact baseline/generation, one-writer lease, typed replacement lineage, and dual-ledger admission;
- project Change Case and conflict/approval state in the Canvas.

Acceptance: two Campaigns targeting one Resource produce one Change Case; non-conflicting changes merge deterministically; conflicting changes cannot apply before exact resolution approval; forged lineage/evidence cannot inherit authority; a generation change blocks rather than silently retargets; crash between Campaign result and Change Case link cannot make an orphan proposal eligible.

### PR F — bundled Agent Runner providers ([#19](https://github.com/JamesbbBriz/agent-workflow/issues/19))

- add versioned Provider Descriptor, Executor Profile, run/event/observation, cancellation, usage, and isolation contracts;
- add one bounded NDJSON subprocess protocol and a static provider registry;
- ship thin adapters/profiles for Codex, Claude Code, Pi, OpenClaw, and Hermes;
- keep every adapter dependency outside the default Go build;
- add `provider list`, `provider doctor`, and `provider conformance`;
- run one admitted fixture unchanged against each installed provider.

Acceptance: a fresh checkout can discover and diagnose every bundled adapter; available providers execute the same admitted Node and emit the same normalized receipt chain; unavailable providers fail with typed readiness without fallback; provider/model changes require a new attempt; no adapter can advance the Campaign, approve an Action Artifact, or resolve/apply a Change Case.

### PR G — conformance kit and release gates in `agent-workflow` ([#16](https://github.com/JamesbbBriz/agent-workflow/issues/16))

- add generic and SEO-shaped fixtures containing no production data;
- add CLI/package conformance for definitions, Context producer, provider, execution, approval, and Replay;
- pin the Go toolchain to a patched release and run vulnerability checks in CI;
- publish a machine-readable conformance report.

Acceptance: a third-party adapter can prove the same receipt chain without the GUI; generic and SEO-shaped fixtures pass identical Core gates.

### PR H — SEO Ops consumer gate ([#17](https://github.com/JamesbbBriz/agent-workflow/issues/17))

- map SEO Ops public Job/Campaign/Workflow/Node/Context/Capability/receipt shapes into the released conformance contracts;
- export deterministic non-production fixtures from SEO Ops;
- run the `agent-workflow` conformance CLI in SEO Ops CI against a pinned version;
- keep all SEO orchestration and business state in SEO Ops.

Acceptance: the Bonnet-like fixture proves multi-Campaign, multi-Workflow, exact Context, typed blocker, bounded attempt, approval, terminal, and exact-cutoff redacted Replay compatibility. Contract drift fails SEO Ops CI without importing either runtime into the other.

## Explicit non-goals

- no second SEO runtime or copied SEO reducer;
- no generic portfolio optimizer, CMS, release, or measurement policy;
- no dynamic plugin loader or marketplace;
- no separate budget service, context database, or scheduler product;
- no direct Node selection by GUI, MCP, WebMCP, cron, or provider;
- no claim that an in-process provider is sandboxed;
- no automatic approval.

## Completion gate

The parent control-plane spec is complete only when all of these hold together:

1. one Job owns multiple independently admitted Campaigns;
2. the Core advances complete pinned DAGs and all supported Node kinds;
3. Context, capability, budget, output, approval, wait, and terminal authority are canonical and crash-safe;
4. shared-resource proposals converge through one Change Case, typed conflict resolution, exclusive apply, and readback trail;
5. exact-cutoff raw and redacted Replay reconstruct the full trail;
6. Canvas and Builder read the same canonical state without inventing progress;
7. a generic example and the live SEO Ops consumer pass the same conformance gates.
