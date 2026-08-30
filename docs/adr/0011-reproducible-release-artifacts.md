# ADR 0011: Reproducible release artifacts

## Status

Accepted.

## Decision

An existing strict SemVer lightweight tag builds one archive for Linux amd64/arm64, macOS amd64/arm64,
and Windows amd64. Every archive contains the CLI, all five bundled provider
bridges, and the license/security/compatibility documents. GitHub Actions
publishes the archives plus one `SHA256SUMS` file and GitHub OIDC build
provenance for every published file.

The release script uses the Go toolchain directly with `CGO_ENABLED=0`,
`-trimpath`, a linker-supplied version, and a small Go-standard-library archiver
that normalizes ordering, ownership, modes, and timestamps. It does not
introduce a packaging framework or change runtime authority. Provider bridges
retain their existing platform isolation checks; distributing a bridge does
not make it ready.

## Consequences

Release binaries need no Node.js runtime because the reviewed React assets are
already embedded. The repository owner sends the narrowly typed `release`
repository dispatch; the first job rejects every other sender. GitHub loads
that privileged workflow from the protected default branch and supplies a
strict SemVer lightweight tag only as event data.
The workflow resolves the tag once, requires its commit to be on `main`, and
checks out that exact commit for verification and packaging. Immediately before
publication, it verifies that the remote tag still equals the resolved commit.
Repository rulesets require reviewed, verified changes on `main`; ruleset
`immutable release tags` blocks updates and deletion for `v*`, and GitHub
immutable releases lock the published tag and assets. The workflow uploads every
artifact to a draft and publishes it only after a second tag check. Release
publication fails when any target cannot build or any artifact is missing.
