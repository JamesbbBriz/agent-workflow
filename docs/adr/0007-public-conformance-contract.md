# ADR 0007: Public conformance contract

Status: accepted

## Decision

`ConformanceFixture@1` is the vertical-neutral interchange boundary. The single
`agent-workflow conformance --file ...` command validates it and executes the
same Authoring, Campaign, approval, Change Case, exact-cutoff Replay, and
redaction Core used by applications. `ConformanceReport@1` records the contract
version, tool version, fixture hash, and hash-bound checks.

Bundled provider readiness is reported without fallback. Real provider
execution remains explicit through `provider conformance`, because offline CI
must not acquire credentials or network authority implicitly.

The SEO-shaped fixture uses generic public fields only. SEO scheduling,
publishing, measurement, and portfolio policy remain outside this repository.

## Consequences

- External projects map only public JSON fields and pin a released CLI version.
- Fixture and report schema changes follow the normal versioning policy.
- CI regenerates both committed fixtures and fails on schema, binding, or
  vulnerability drift.
