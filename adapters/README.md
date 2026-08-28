# Bundled Agent Runner profiles

The Go registry bundles five profiles: Codex, Claude Code, Pi, OpenClaw, and
Hermes Agent. Each profile names a separately installed adapter executable:

| Provider | Executable |
| --- | --- |
| Codex | `agent-workflow-codex` |
| Claude Code | `agent-workflow-claude-code` |
| Pi | `agent-workflow-pi` |
| OpenClaw | `agent-workflow-openclaw` |
| Hermes Agent | `agent-workflow-hermes` |

Every executable implements the same generated `ProviderProtocolRequest` /
`ProviderProtocolResponse` NDJSON contract. It owns provider-specific SDK or
CLI dependencies; stdout is protocol-only. The Core launches one isolated
adapter process per invocation, allows only declared environment variables,
and accepts only `output/result` whose hash matches the terminal observation.

Use `agent-workflow provider doctor` before execution and
`agent-workflow provider conformance` to run the shared admitted fixture. An
adapter is never discovered dynamically and an unavailable profile never
falls back to another provider.
