# Agent Workflow

Agent Workflow is an auditable control plane for context-bound Agent work. Humans define a long-lived **Job**, admit bounded **Campaigns**, and run immutable **Workflow** DAGs. Every **Node** receives an exact Context Bundle and Capability Manifest; every accepted transition produces a hash-bound receipt and Replay trail.

This repository is the domain-neutral Core extracted from patterns dogfooded in [SEO Ops](https://github.com/JamesbbBriz/seo-ops). It does not copy SEO workflows or create another SEO state machine.

## Control model

An Agent is constrained by all of the following:

1. an immutable Node contract;
2. an exact Context Bundle;
3. a Capability Manifest classified by real side effects;
4. a budget and deadline;
5. declared output schemas and approval gates.

Context determines what the Agent can know. Capabilities determine what it can do. The Workflow determines when it runs and what counts as completion. Receipts determine what becomes canonical truth.

## Try the public contract

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

Open the full-screen React Flow Canvas:

```bash
npm run fixture:canvas
npm run web:dev
```

Definition mode shows the exact configured graph. Runtime mode adds only executions, Context Pack editions, Action Artifacts, blockers, approval gates, and receipts present in the Core projection. The GUI cannot create, approve, or mutate canonical state.

Generate Go and TypeScript bindings from the canonical schema:

```bash
npm ci
npm run generate
npm run check:generated
```

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

See [Architecture](docs/architecture.md), [Security](SECURITY.md), [Compatibility](COMPATIBILITY.md), and [Contributing](CONTRIBUTING.md).

## Status

The v1 public contract is under active development. WebMCP support is planned as an optional experimental browser adapter and is not a Core dependency.

## License

Apache-2.0.
