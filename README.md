# Agent Workflow

Agent Workflow is an auditable control plane for context-bound Agent work. Humans define a long-lived **Job**, admit bounded **Campaigns**, and run immutable **Workflow** DAGs. Every **Node** receives an exact Context Bundle and Capability Manifest; every accepted transition produces a hash-bound receipt and Replay trail.

It is being field-tested by a separate commercial SEO product in a live 200-hour run across 4 Jobs, 4 keyword clusters, and 30 canonical keywords. The completed evidence report—not the runtime duration alone—will support the public proof claim.

This repository is the domain-neutral Core extracted from patterns dogfooded in [SEO Ops](https://github.com/JamesbbBriz/seo-ops). It does not copy SEO workflows or create another SEO state machine.

For positioning, the separate commercial SEO Ops dogfood story, current evidence, claim boundaries, and selective generic contract extraction, see the [SEO Ops dogfood GTM pack](docs/go-to-market/seo-ops-dogfood-gtm-pack.md).

## Control model

An Agent is constrained by all of the following:

1. an immutable Node contract;
2. an exact Context Bundle;
3. a Capability Manifest classified by real side effects;
4. a budget and deadline;
5. declared output schemas and approval gates.

Context determines what the Agent can know. Capabilities determine what it can do. The Workflow determines when it runs and what counts as completion. Receipts determine what becomes canonical truth.

## Try the public contract

Run the complete local CLI path first. It uses the bundled non-production
fixture and demo provider with a private append-only ledger:

```bash
go run ./cmd/agent-workflow init --dir /tmp/agent-workflow-demo
go run ./cmd/agent-workflow doctor --dir /tmp/agent-workflow-demo
go run ./cmd/agent-workflow run --dir /tmp/agent-workflow-demo
go run ./cmd/agent-workflow approval confirm --dir /tmp/agent-workflow-demo
go run ./cmd/agent-workflow replay --dir /tmp/agent-workflow-demo
```

`run` stops at the exact human approval gate. `approval confirm` appends the
approval receipt and resumes the same Campaign; `status` and `replay` recover
the canonical state after process restart. The bundled fixture never performs
production mutation.

```bash
go run ./cmd/agent-workflow validate \
  --file examples/research-review.workflow.json \
  --json
```

The command validates the closed v1 JSON Schema, DAG semantics, slot cardinality, and prints a deterministic Workflow identity hash.

Run the synthetic Context-bound execution demo:

```bash
go run ./cmd/agent-workflow demo \
  --file examples/research-review.workflow.json \
  --at 2026-08-27T00:00:00Z \
  --json
```

The demo expands Workflow Context defaults into the `research` Node, resolves an exact catalog Pack and a derived Intent-chain Pack, builds a Capability Manifest, invokes one bounded provider, validates its Action Artifact, and returns a seven-receipt Replay. It uses the process-local ledger; `OpenFileLedger` provides a small single-writer, crash-durable JSONL option.

Project the same canonical definitions and Replay into the generated read-only Canvas API:

```bash
go run ./cmd/agent-workflow canvas \
  --file examples/research-review.workflow.json \
  --at 2026-08-27T00:00:00Z
```

The application shell exposes Jobs and the React Flow Canvas, Runs, Approvals, Change Cases, bundled Provider readiness, and the append-only Audit trail. `GET /v1/control-plane` returns the generated `ControlPlaneSnapshot` contract that backs those views. Definition mode shows the exact configured graph; Runtime mode adds only executions, Context Pack editions, Action Artifacts, blockers, approval gates, and receipts present in the Core projection. The GUI never invents canonical state; Builder and approval actions still round-trip through the Go Core.

Generate the initial Canvas fixture, then run the local Core and web application:

```bash
npm run fixture:canvas
go run ./cmd/agent-workflow builder \
  --listen 127.0.0.1:4321 \
  --ledger .agent-workflow/builder.jsonl \
  --canvas web/public/canvas.response.json
npm run web:dev
```

The browser keeps unfinished drafts in local storage. The Builder reads the Go Core catalog, lints and expands the exact Node contracts, then requires a revision-bound preview token before the Core admits an immutable Workflow version. Pending Action Artifacts can be opened from the Runtime Canvas for a separate preview/confirm human decision. Only the Core writes admission and approval receipts; the GUI projects their Replay.

The UI uses the open-source shadcn/ui composition model, Radix primitives, and Tailwind. Paid block source is not vendored, so the public repository stays redistributable.

To opt into the browser's experimental `document.modelContext` API for this exact local page origin:

```bash
go run ./cmd/agent-workflow builder \
  --listen 127.0.0.1:4321 \
  --ledger .agent-workflow/builder.jsonl \
  --canvas web/public/canvas.response.json \
  --web-origin http://127.0.0.1:5173 \
  --webmcp-audit .agent-workflow/webmcp-audit.jsonl
```

The adapter registers five page-scoped inspect, explain, preview, navigation, and exact-confirm tools. It feature-detects the current WebMCP Community Group draft, uses no polyfill, and remains absent when the browser does not implement `document.modelContext`. All calls still cross the Go Core and produce a correlated local audit trail; see [ADR 0003](docs/adr/0003-experimental-webmcp-adapter.md).

Generate Go and TypeScript bindings from the canonical schema:

```bash
npm ci
npm run generate
npm run check:generated
```

Build the five bundled bridges, then inspect the bridge and upstream CLI
requirements:

```bash
./adapters/install.sh
go run ./cmd/agent-workflow provider list
go run ./cmd/agent-workflow provider doctor --id codex
go run ./cmd/agent-workflow provider doctor --id openclaw --staged-root ./staged --config-ref tenant-a
```

Each available adapter is checked through the same admitted fixture with
`provider conformance` (default `conformance/fixtures/generic.json`). The Core speaks the versioned NDJSON protocol, binds
the exact executor profile and staged isolation evidence into Replay, and
fails closed instead of selecting another provider.

Run the public conformance contract without a GUI, browser, production data, or
network access:

```bash
npm run conformance
# or consume an external vertical fixture
go run ./cmd/agent-workflow conformance --file path/to/fixture.json
```

Both committed fixtures use the same command. The JSON report identifies the
contract/tool versions and fixture hash; bundled providers are either typed
unavailable/skipped or left for the explicit credentialed `provider
conformance` command. See [ADR 0007](docs/adr/0007-public-conformance-contract.md).

## Architecture boundary

```text
Job -> Campaign -> Workflow@version -> Node Contract
                                      + Context Bundle
                                      + Capability Manifest
                                                |
                                                v
                                         Provider Agent
                                                |
                                                v
                                    Output validation / approval
                                                |
                                                v
                                      Receipt ledger / Replay
```

The Go Core is the only canonical mutation authority. Storage, provider Agents, CLI, GUI, MCP, and WebMCP are adapters. Context Packs contain evidence; Action Artifacts contain decisions or proposed mutations.

`CampaignRuntime.ReplayAt` reads one exact receipt prefix as raw diagnostic data or as the `public_metadata@1` redacted projection. `ChangeCaseCore` coordinates completed Campaign Node proposals against one exact Resource generation, then requires deterministic merge/conflict handling, exact approval, one Mutation Lease, apply evidence, and matching readback. `NewSubprocessProvider` is the Linux reference production isolation seam; its staged root must contain read-only `input/` and the single bounded writable file `output/result`. Ordinary in-process providers remain explicitly trusted/testing adapters, and a production Engine can reject them with `RequireProviderIsolation(staged_subprocess)` before Campaign admission. Platforms without Bubblewrap PID-namespace containment fail closed.

See [Architecture](docs/architecture.md), [Security](SECURITY.md), [Compatibility](COMPATIBILITY.md), and [Contributing](CONTRIBUTING.md).

## Status

The v1 definitions plus versioned Campaign execution, Context recovery, approvals/waits, multi-Campaign Builder/Canvas, exact-cutoff redacted Replay, reference provider isolation, domain-neutral Change Case coordination, and five bundled Agent Runner bridges are implemented. The SEO Ops conformance consumer remains release work; see the [runtime closure plan](docs/runtime-closure-plan.md). WebMCP is not a Core dependency.

## License

Apache-2.0.
