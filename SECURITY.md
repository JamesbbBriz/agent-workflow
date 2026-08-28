# Security policy

## Supported versions

Until the first tagged release, only the latest `main` commit receives security fixes.

## Reporting

Report vulnerabilities privately through GitHub Security Advisories for this repository. Do not include credentials, customer data, production corpora, or exploit payloads in public issues.

## Trust boundary

- The Go Core is the only canonical mutation authority.
- Provider Agents receive exact staged inputs and a Capability Manifest. This library does not sandbox arbitrary in-process Go implementations of `Provider`; deployments must put provider code behind a sandboxed adapter that exposes no ambient shell, filesystem, database, browser, or credential authority.
- Context and artifact bytes are size-bounded, strictly decoded, root-confined where applicable, and SHA-256 verified before use.
- Unknown schema versions and fields fail closed.
- Consequential actions require explicit preview/confirm or human approval bound to exact hashes and revisions.
- MCP, WebMCP, GUI, storage, and provider implementations are adapters and cannot bypass Core policy.
- Replays redact private provider references while preserving receipt identity.

Experimental WebMCP is disabled by default. When enabled for the loopback Builder, it accepts one exact page origin, a random process-lifetime bearer session, actor-bound request hashes, and a bounded per-subject request rate. Its mode-0600 audit log records identities and hashes, never request bodies or credentials. This loopback session is not a substitute for hosted user authentication.

The reference Builder trusts processes running as the same OS user: such a process can already replace the binary or canonical ledger. Loopback approval is therefore a single-user development boundary, not protection from same-user malware. Deployments with untrusted local processes or remote users must put an authenticated identity adapter in front of the Core and bind its verified subject to approval policy.

The public schema describes authority classes, but a label is not enforcement. Implementations must classify capabilities from actual side effects and verify that the execution adapter provides no broader authority.
