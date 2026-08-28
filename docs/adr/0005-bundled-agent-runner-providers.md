# ADR 0005: Bundled Agent Runner providers

Status: accepted

## Context

The Core already has a small `Provider` seam, but an interface alone is not an
open-source product. A new user should be able to run an admitted Agent Node
with a commonly used agent harness without writing an integration first.

Agent harnesses have different lifecycle, event, cancellation, tool, session,
and isolation behavior. Importing each SDK into the Go Core would couple the
runtime to several release cycles and would let provider-specific state leak
into canonical Campaign execution.

## Decision

Keep one provider-neutral Core contract and ship the adapters in this
repository.

The first supported provider set is:

- Codex;
- Claude Code;
- Pi;
- OpenClaw;
- Hermes Agent.

These are execution providers for one already-authorized Node. They may use
their own internal planning, tools, memory, or workflow implementation, but
they cannot choose the next Core Node, satisfy a dependency, approve an
Action Artifact, resolve a Change Case, or append canonical state.

The Core binds every invocation to an immutable `ExecutorProfile` containing
the provider kind and version, model/config references, declared capabilities,
isolation profile, tool/capability allowlist, and a configuration hash. Secret
values never enter the profile or Replay. A provider or model change creates a
new attempt and receipt; it cannot occur silently within one invocation.

Use one versioned, language-neutral process protocol for bundled adapters:

```text
describe -> ProviderDescriptor
start    -> ProviderRunRef
events   -> ordered ProviderEvent page
inspect  -> ProviderObservation
cancel   -> accepted | already_terminal | unsupported
```

The protocol uses bounded NDJSON over stdio. Stdout is protocol-only; bounded
stderr is diagnostic evidence. Every request carries a protocol version,
request ID, invocation ID, idempotency key, deadline, staged workspace, input
manifest hash, and output contract hash. Provider session and run references
are opaque adapter data, never Campaign or Node identity.

The repository contains:

- the Go subprocess adapter and static provider registry;
- versioned protocol schemas and conformance fixtures;
- one thin bridge/profile for each supported provider;
- `provider list`, `provider doctor`, and `provider conformance` CLI commands;
- an example that runs the same admitted Workflow with every available
  provider without changing Job, Campaign, Workflow, Context, or output
  contracts.

The five thin profiles are compiled into the static Go registry. Their
executables are deliberately separate distributables named
`agent-workflow-codex`, `agent-workflow-claude-code`, `agent-workflow-pi`,
`agent-workflow-openclaw`, and `agent-workflow-hermes`. The Core discovers no
plugins and never falls back between them.

Provider dependencies remain inside their adapter directories. The default Go
build does not require Node, Python, provider credentials, or every provider
binary. An unavailable adapter reports a typed readiness error; it does not
fall back to another provider.

OpenClaw and Hermes are powerful long-lived agents. Their adapters must create
or select an isolated tenant/profile, restrict tools to the Node Capability
Manifest, and retain only opaque session references plus normalized receipts.
Codex, Claude Code, and Pi run inside the same staged-workspace isolation
boundary owned by the subprocess adapter. Provider-native sandbox claims are
recorded as evidence but do not replace the Core isolation profile.

The bundled profiles initially support one exact Node authority:
`read-evidence`. The bridge rejects any different Capability Manifest or tool
allowlist, then maps that authority to each CLI's native read-only flags.
OpenClaw additionally selects a dedicated staged agent profile whose workspace
and tool allowlist are verified before launch. Hermes runs without user config
or durable session state; its file toolset is contained by the read-only staged
input mount and single writable result file.

Readiness and execution resolve upstream CLIs from the same system roots exposed
inside Bubblewrap (`/usr/local/bin`, `/usr/bin`, and `/bin`) and include the
isolation probe. User-local PATH entries are not reported ready. Before launching
an upstream, the subprocess adapter durably reserves the exact invocation in the
staged output directory. A restarted Core may recover an exact hash-bound result;
if the external attempt is uncertain, it blocks instead of launching it again.

## Compatibility

The current in-process `Provider` remains a trusted test compatibility adapter.
Existing v1 executions remain readable. New executor identity, descriptor,
capability, and isolation fields use versioned v2 contracts.

## Consequences

- A user can select a supported Agent and run the example after satisfying that
  provider's own installation/authentication check.
- Adding another Agent requires one bridge plus the same conformance suite, not
  a Core change or a plugin framework.
- Provider-specific sessions, workflow engines, memory, and tool semantics stay
  behind the adapter boundary.
- Framework integrations and a provider marketplace are not part of the first
  release. They can implement the public process protocol without being named
  or depended on by the Core.
