# Security policy

## Supported versions

Until the first tagged release, only the latest `main` commit receives security fixes.

## Reporting

Report vulnerabilities privately through GitHub Security Advisories for this repository. Do not include credentials, customer data, production corpora, or exploit payloads in public issues.

## Trust boundary

- The Go Core is the only canonical mutation authority.
- Provider Agents receive exact staged inputs and a Capability Manifest; they do not receive ambient shell, filesystem, database, browser, or credential authority.
- Context and artifact bytes are size-bounded, strictly decoded, root-confined where applicable, and SHA-256 verified before use.
- Unknown schema versions and fields fail closed.
- Consequential actions require explicit preview/confirm or human approval bound to exact hashes and revisions.
- MCP, WebMCP, GUI, storage, and provider implementations are adapters and cannot bypass Core policy.
- Replays redact private provider references while preserving receipt identity.

The public schema describes authority classes, but a label is not enforcement. Implementations must classify capabilities from actual side effects and verify that the execution adapter provides no broader authority.
