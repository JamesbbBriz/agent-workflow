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

Upstream CLIs must be installed in `/usr/local/bin`, `/usr/bin`, or `/bin`.
Those are the only executable roots exposed by the Linux Bubblewrap profile, so
`provider doctor` deliberately does not report a user-local PATH install as
ready. Codex, Claude Code, and Pi receive their native read-only tool flags.
Hermes receives only its read-only `vision` toolset; structured Context evidence
is already embedded in the invocation prompt, so its `file` toolset is rejected
because it also grants write and patch tools.

OpenClaw additionally requires a dedicated agent entry in
`input/providers/<config_ref>.json`. The entry must use the admitted
`config_ref` as its ID, set `workspace` to `/workspace`, and set the exact tool
allowlist to `read`. The bridge selects that agent and passes the same
file as `OPENCLAW_CONFIG_PATH`; it rejects a broader or missing profile before
starting OpenClaw.

For OpenClaw, pass the same `--staged-root` and `--config-ref` used by the
executor so doctor validates the exact agent profile. Use `agent-workflow
provider doctor` before execution and
`agent-workflow provider conformance` to run the shared admitted fixture. An
adapter is never discovered dynamically and an unavailable profile never
falls back to another provider.
