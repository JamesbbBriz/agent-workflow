# Compatibility policy

Public JSON contracts use an integer `schema_version`. Readers reject unknown versions and unknown fields unless a schema explicitly marks an extension object.

- Backward-compatible additions require optional fields and unchanged existing semantics.
- Required-field, identity, authority, receipt, and state-machine changes require a new schema version.
- Existing immutable Workflow versions and receipts are never reinterpreted under a newer contract.
- Generated Go and TypeScript bindings must be regenerated in the same pull request as a schema change.
- Historical Replay uses the contract and canonical prefix recorded at the original cutoff.

Pre-1.0 releases may add new v1 definitions, but they may not silently widen an existing capability or context requirement.
