# ADR 0003: Experimental page-scoped WebMCP adapter

Status: Accepted

## Decision

Agent Workflow may expose the current WebMCP Community Group draft through `document.modelContext` when the browser provides it. The adapter is optional, page-scoped, and disabled unless the local Builder is started with an exact `--web-origin`.

The browser registers five tools only:

- inspect the current canonical Canvas;
- explain visible Context blockers;
- prepare a Workflow admission preview;
- navigate to an existing pending approval;
- submit an unchanged Core-issued Workflow admission confirmation.

WebMCP is a presentation adapter, not another runtime. Every request passes through the existing Go handler and therefore preserves strict decoding, preview/confirm tokens, revision checks, rate limiting, canonical receipts, and the same Canvas readback. No tool writes the ledger directly.

The local adapter obtains a random process-lifetime bearer session for one exact page origin and a server-bound `local-operator` subject that browser input cannot replace. Tool calls bind that actor, a UUID request ID, input SHA-256, origin, tool name, outcome, preview/confirmation identity, and canonical receipt reference into a mode-0600 append-only audit log. A confirmation also embeds the same audit binding in its atomic canonical admission receipt, so an external audit completion-write failure cannot create an unaudited mutation. Registration and in-flight calls share an `AbortSignal`; navigation away removes the tools.

## Compatibility and limits

WebMCP remains an experimental Community Group draft. The application feature-detects `document.modelContext`, registers nothing when it is absent, and ships no polyfill or browser shim. It uses the draft's default same-origin exposure and does not request broader agent exposure.

The bearer session is a loopback Builder control, not hosted authentication. A hosted deployment must terminate real user authentication before this adapter and map the authenticated subject into the same Core request boundary.

References: [WebMCP draft](https://webmachinelearning.github.io/webmcp/), [WebMCP repository](https://github.com/webmachinelearning/webmcp).
