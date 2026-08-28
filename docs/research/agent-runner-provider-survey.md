# Agent Runner Provider survey

Date: 2026-08-28

## Recommendation

Do not define one interface that mixes model APIs, coding-agent harnesses, and workflow frameworks. The useful domain-neutral seam is an **Agent Runner Provider**: it executes one already-authorized node and reports events and receipts; it does not choose Campaign nodes, resolve Change Cases, approve mutations, or own durable workflow state.

Ship only the adapters needed by real consumers:

1. **Bundled adapters:** Codex, Claude Code, Pi, OpenClaw, and Hermes Agent.
2. **Experimental candidates:** DeepSeek Harness remains gated because its public JSON-RPC protocol is pre-release and has no per-session cancel method.
3. **Model-only clients:** a model API is not an Agent Runner by itself.
4. **Framework bridges:** lower-level orchestration frameworks are deliberately not named as first-party product integrations. They may implement the public process protocol when a real consumer needs one.

The minimum common lifecycle is `Start`, `Events`, `Cancel`, and optional `Resume`. Capabilities must be advertised rather than emulated:

```text
AgentRunnerProvider
  Start(spec, idempotency_key) -> run_ref
  Events(run_ref, cursor?)      -> ordered provider events
  Inspect(run_ref)              -> status + usage + provider receipts
  Cancel(run_ref)               -> accepted/unsupported
  Resume(session_ref, input)    -> run_ref (optional)
```

`spec` should contain a declared working directory/isolation profile, model, instructions, an explicit capability/tool allowlist, approval routing mode, optional output JSON Schema, and provider-native session reference. Core must validate the terminal output itself; a provider's “structured output” success is evidence, not authority.

## Compatibility matrix

| Surface | Lifecycle and identity | Structured output / tool control | Usage and receipts | Isolation and caveats | Integration |
|---|---|---|---|---|---|
| **OpenAI Codex SDK** | Python exposes thread start/resume/fork/list/archive; a turn handle supports stream, steer, interrupt, and completion. TypeScript exposes start/resume plus `run`/`runStreamed`; cancellation is an `AbortSignal`. | Per-turn JSON Schema; approval mode and sandbox can be set at thread and turn boundaries. | Turn result carries ID, status, timestamps, duration, items, error, and token usage. | Native `read_only`, `workspace_write`, and `full_access` sandboxes. Python currently has the richer public lifecycle. | **Direct**, prefer the Python SDK or generated app-server protocol until TypeScript reaches parity. |
| **Claude Agent SDK / Claude Code** | `query()` is an async event stream; V1 `Query` supports `interrupt()`, and `resume` continues a session ID. Claude Code CLI also has print mode and resume by ID. | `allowedTools`/`disallowedTools`, permission modes/hooks, MCP servers, and `outputFormat: json_schema`. | Result messages expose session/result metadata, cost and usage fields; retain the provider result event as the receipt. | Local Claude Code subprocess owns filesystem/process access. Interrupt and structured-output behavior have active edge-case reports, so normalize cancellation only from terminal result metadata and independently validate JSON. | **Direct**, pinned SDK/CLI version with conformance tests. |
| **Pi (`@mariozechner/pi-coding-agent`)** | SDK `AgentSession.prompt()` waits for completion, `subscribe()` streams events, `abort()` cancels, and session ID/file support resume. CLI also offers JSON and RPC modes. | Caller supplies the tool list; CLI has allow/exclude/no-tool flags; extensions can change active tools. No first-class provider-neutral final JSON Schema contract was found. | Events and stored JSONL contain assistant/tool traffic; model usage is available in messages/events and context usage is exposed. | Built-in file/bash tools execute in the selected local cwd; Pi does not itself establish the Core isolation boundary. Persisted session files are local artifacts. | **Direct**, using the SDK rather than scraping CLI text. Require Core-side result validation. |
| **Hermes Agent** | Official API server has asynchronous Runs endpoints: start, inspect, SSE events, approval, and stop. Session APIs support create/read/history/fork and streamed chat. | Toolsets are discoverable and platform-configurable; Runs can pause for approval. The API does not provide a portable JSON-Schema final-output guarantee. | Sessions store full messages/tool calls, token counts, timestamps, model/config; Runs events are the execution receipt stream. | A Hermes profile is a powerful long-lived agent with terminal, browser, messaging, memory, and other tools. Bind locally or authenticate the API server; isolate each tenant/profile and allowlist toolsets. | **Direct HTTP adapter** to `/v1/runs`, not private Python internals. |
| **DeepSeek Harness** | SDK stdio JSON-RPC supports initialize, durable `session/prompt`, `session.event`, `session.status`, and shutdown. Reusing `sessionId` continues a conversation. The in-process Agent API has send/steer/inject/cancel/whenIdle. | Plugin-based tool registry and model adapters; durable events include turns, steps, assistant chunks, and tools. No stable provider-neutral final JSON Schema facility was found. | Full session-log envelopes are the durable facts. `session/prompt` returns a queued message ID, not a terminal run result; the client must correlate owned activity intervals. | Officially developer preview with compatibility-breaking changes. Current SDK wire has no cancel/session-close; abandoning a turn closes the runtime process. Notifications are runtime-wide and require client-side session filtering. | **Experimental direct adapter** only, one runtime process per active run/session until scoped cancel and protocol versioning exist. |
| **DeepSeek model API** | OpenAI-compatible chat API supports streaming but not a durable agent session/run lifecycle. Conversation state and cancellation belong to the caller. | Function calling and JSON Output are model features, not a sandboxed agent harness. | API responses carry token usage; no filesystem/tool execution receipt chain exists unless the caller builds it. | No execution isolation because the API only performs inference. | **Model adapter**, not an Agent Runner adapter. |
| **LangChain / LangGraph** | `invoke`/`stream`; checkpointers persist graph state by `thread_id`; interrupts pause and resume with `Command`. Runtime exposes thread ID, run ID, and attempt. Hosted LangGraph adds run APIs and operational controls. | Typed state/tools, middleware, interrupts, and model structured output can enforce a bounded graph contract. | Run identity and streamed graph events are available; model usage depends on model messages/callbacks or hosted tracing. | The graph/checkpointer is its own workflow authority. A direct mapping would create two schedulers and two replay models. | **Bridge**: invoke one registered graph/agent as a single Core node and translate only its boundary events/receipt. |
| **Mastra** | Agent `generate`/`stream`; workflow runs stream and persist snapshots; suspended workflows resume, including `resumeStream`. | Zod schemas on tools/workflow inputs and outputs; tool approval and suspend/resume are supported. | Run IDs, stream parts, final status, traces, and step results are available. | Mastra workflows already own branching, retries, snapshots, and replay. Do not let a Mastra workflow advance the Core Campaign DAG. | **Bridge** around one Agent or committed Workflow invocation. |
| **LlamaIndex** | Agent/Workflow `run()` returns a handler whose events can be streamed; `Context` can be serialized and restored for continuation. | Explicit tool lists and Pydantic `output_cls` / structured-output functions exist. Workflow events support human-in-the-loop patterns. | Workflow events and handler result are available, but a universal cost/usage receipt is model-integration dependent. | `Context` is coupled to the concrete Workflow; custom Memory may need separate persistence. It is not a portable Core session artifact. | **Bridge** around one AgentWorkflow/Workflow invocation; store only an opaque provider session reference plus Core receipts. |

## Primary-source notes

### OpenAI Codex

- The official [Codex Python SDK API reference](https://github.com/openai/codex/blob/main/sdk/python/docs/api-reference.md) defines explicit thread lifecycle, streamed turn handles, interrupt/steer, structured output, usage, and sandbox presets.
- The official [TypeScript `Thread` implementation](https://github.com/openai/codex/blob/main/sdk/typescript/src/thread.ts) shows `runStreamed`, thread IDs, `AbortSignal`, output-schema handling, sandbox/approval arguments, and terminal usage events.
- The lower-level [Codex app-server protocol](https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md) can generate version-matched TypeScript or JSON Schema, which is safer than handwritten protocol types.

### Anthropic Claude

- The official [Claude Code CLI reference](https://docs.anthropic.com/en/docs/claude-code/cli-usage) documents headless print mode, continuation, session-ID resume, tool allow/deny flags, permission modes, and JSON output formats.
- Anthropic's official [Agent SDK session-store example](https://github.com/anthropics/claude-agent-sdk-typescript/tree/main/examples/session-stores) demonstrates session resume through `query({ options: { resume } })`.
- Open upstream reports document why the adapter must be conservative around [interrupt terminal classification](https://github.com/anthropics/claude-agent-sdk-typescript/issues/405) and [structured-output completion](https://github.com/anthropics/claude-agent-sdk-typescript/issues/277).

### Pi

- The official [Pi SDK guide](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/sdk.md) defines `AgentSession`, prompt, event subscription, abort, session identity, configurable tools, and model/session controls.
- The official [coding-agent README](https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent) documents JSON/RPC modes, session resume, and tool allow/exclude flags.

### Hermes Agent

- The official [API server implementation](https://github.com/NousResearch/hermes-agent/blob/main/gateway/platforms/api_server.py) declares Runs start/status/events/approval/stop endpoints and persisted-session endpoints.
- The official [session guide](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/sessions.md) documents session IDs, resume, stored messages/tool calls, token counts, model/config snapshots, and lineage.
- The official [tools guide](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/tools.md) shows why toolset allowlisting and isolation are mandatory.

### DeepSeek

- The official [DeepSeek Harness SDK protocol](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/sdk/protocol/README.md) defines its request/notification surface and explicitly records the absence of cancel and protocol-version negotiation.
- The official [core Agent contract](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/core/agent/README.md) defines send/steer/inject/cancel/whenIdle and durable event semantics.
- The repository labels Harness a compatibility-breaking [developer preview](https://github.com/deepseek-ai/deepseek-harness).

### Framework bridges

- LangGraph documents [interrupt/resume and `thread_id`](https://docs.langchain.com/oss/python/langgraph/interrupts), [checkpoint persistence](https://docs.langchain.com/oss/python/langgraph/persistence), and [runtime execution identity](https://docs.langchain.com/oss/python/langchain/runtime).
- Mastra documents typed tools and MCP in its [agent tools guide](https://mastra.ai/docs/agents/mcp-guide), and durable [workflow snapshots](https://mastra.ai/en/reference/workflows/snapshots) used for suspend/resume.
- LlamaIndex's official [agent memory/context guide](https://github.com/run-llama/llama_index/blob/main/docs/src/content/docs/framework/module_guides/deploying/agents/memory.mdx) distinguishes serializable Workflow `Context` from agent Memory; its official [AgentWorkflow implementation](https://github.com/run-llama/llama_index/blob/main/llama-index-core/llama_index/core/agent/workflow/multi_agent_workflow.py) exposes tool lists, structured outputs, run IDs, and resumable context.

## Contract consequences

- Treat `provider_session_ref` and `provider_run_ref` as opaque, versioned references. Never make them the Campaign or Node identity.
- Persist normalized events and the raw provider terminal envelope/hash. Provider history is useful for resume, but Core receipts remain authoritative for Replay.
- `Cancel` must return `accepted`, `already_terminal`, or `unsupported`; killing a shared runtime is not a valid emulation of scoped cancellation.
- Capability negotiation should include at least: streaming, polling, scoped cancel, resume, structured output, tool allowlist, interactive approval, usage, event cursor, and isolation profiles.
- Do not import framework state/checkpoints into the generic domain model. A bridge may persist them in provider-owned storage and return an opaque reference.
- Start with Codex and one second real consumer. Add adapters only when a consumer exercises the same conformance suite; no speculative adapter packages.
