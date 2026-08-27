# Agent instructions

- Work from a `ready-for-agent` issue and preserve its blocking edges.
- Use red-green TDD at the highest public seam.
- The Go Core is canonical authority; provider Agents and presentation adapters never append business transitions directly.
- Public contracts are versioned JSON Schema with generated Go and TypeScript bindings.
- Context Pack evidence and Action Artifact decisions are different contracts.
- Workflow defaults compile into explicit Node requirements; runtime has no hidden Context inheritance.
- Unknown versions, unknown fields, stale confirmation, missing required Context, hash mismatch, and scope mismatch fail closed.
- Do not add speculative interfaces, dynamic plugins, a second workflow runtime, ambient credentials, or arbitrary executable uploads.
- Every PR runs tests, generation drift checks, vet, build, diff check, and independent Standards plus Spec review.
