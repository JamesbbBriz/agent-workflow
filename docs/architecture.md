# Architecture

## Domain glossary

- **Job** — long-lived intent, coverage, budget, and non-goals created by a human.
- **Campaign** — one bounded hypothesis admitted against a frozen scope and evidence frontier.
- **Workflow** — an immutable, versioned DAG with completion, no-action, blocker, input, and output contracts.
- **Node** — the smallest context, capability, budget, execution, and acceptance boundary.
- **Context Pack** — immutable evidence with authority, scope, provenance, freshness, and a content hash.
- **Context Bundle** — the exact set of Pack editions resolved for one Node at one evidence cutoff.
- **Action Artifact** — a decision or proposed mutation bound to its Campaign, Workflow, inputs, output hash, and approval state.
- **Receipt** — an append-only proof of one accepted transition.
- **Replay** — an exact canonical prefix reconstructed at a chosen receipt cutoff.

## Authority

The Core owns definitions, compilation, admission, context resolution, invocation identity, budgets, output acceptance, approval, receipts, and Replay. Provider Agents perform bounded computation and return proposed artifacts. They cannot append canonical transitions.

IntentCards improve shared understanding but are not an authorization source. GUI state, MCP sessions, browser state, projections, and provider transcripts are not canonical business truth.

## Context injection

Workflow-level Context defaults are an authoring convenience. Compilation expands them into explicit Node requirements. Runtime selects exact editions only after checking Pack type, schema, producer, subject, scope, evidence cutoff, captured time, expiry, coverage, and hashes.

Missing required Context becomes `needs_context`. Missing optional Context may produce a degraded Context Bundle if the Node contract permits it. No Node may silently widen its context to an entire project or reinterpret later evidence.

The v1 compiler uses a static Go registry. `CatalogProducer` selects an immutable Pack edition as of the Campaign evidence cutoff; `IntentProducer` deterministically materializes the Job → Campaign → Workflow Intent chain from the compile receipt. Node outputs declare their downstream consumers; v1 executes Action Artifact outputs and rejects the reserved Context Pack output kind before provider work. Compilation also rejects unknown consumers and broken direct-dependency slot flows.

## Audit chain

```text
DefinitionHash
-> CompileReceipt
-> AdmissionReceipt
-> PackEditionReceipt
-> ContextBundleHash
-> InvocationHash
-> ProviderExecutionReceipt
-> OutputHash
-> Review/ApprovalReceipt
-> TerminalEvent
-> Replay
```

Every adapter must preserve correlation to this chain. An adapter may repair delivery projections but cannot invent a business transition.

Provider calls carry a stable idempotency key. Redelivery may call the provider seam again, but a conforming provider must converge on the same stored result for that key; Core receipt appends converge by aggregate version and hash.

The included file ledger is deliberately single-writer. It syncs each JSONL receipt, reloads exact JSON numbers, verifies schema and hash-chain continuity, and converges exact redelivery after a Core restart. Multi-process deployments should provide a database-backed `Ledger` rather than weakening that ownership rule.

## Adapters

- storage adapters persist the canonical ledger and immutable artifacts;
- producer adapters capture bounded evidence and return Context Pack candidates;
- provider adapters implement bounded Start/Poll/Cancel execution;
- CLI, GUI, MCP, and WebMCP expose Core operations without becoming state authorities.

The first implementation uses static Go registries. Dynamic code loading and plugin marketplaces require separate threat models and are intentionally absent.
