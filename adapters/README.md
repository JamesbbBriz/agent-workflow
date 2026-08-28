# Bundled Agent Runner profiles

The repository bundles five thin Go bridges: Codex, Claude Code, Pi, OpenClaw,
and Hermes Agent. Install them into `GOBIN` with:

```sh
./adapters/install.sh
```

Each bridge implements the same bounded NDJSON protocol and invokes the named
upstream CLI:

| Provider | Bridge | Upstream CLI |
| --- | --- | --- |
| Codex | `agent-workflow-codex` | `codex` |
| Claude Code | `agent-workflow-claude-code` | `claude` |
| Pi | `agent-workflow-pi` | `pi` |
| OpenClaw | `agent-workflow-openclaw` | `openclaw` |
| Hermes Agent | `agent-workflow-hermes` | `hermes` |

The shared bridge owns no provider SDK dependency. It translates the admitted
invocation into each CLI's documented one-shot form, normalizes its structured
output, and keeps stdout protocol-only. The Core launches one isolated adapter
process per invocation, allows only declared environment variables, and accepts
only `output/result` whose hash matches the terminal observation.

Use `agent-workflow provider doctor` before execution and
`agent-workflow provider conformance` to run the shared admitted fixture. An
adapter is never discovered dynamically and an unavailable profile never
falls back to another provider.
