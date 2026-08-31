# ADR 0013: Reusable Workflow admissions

Status: accepted

## Decision

A canonical Workflow admission authorizes one immutable Workflow definition
version. It is reusable by multiple Jobs and Campaigns that independently pin
that exact version.

Each execution still validates the current Job and Campaign definitions, binds
their hashes into the compile and invocation receipts, and validates the
Campaign's Workflow plan. The Job and Campaign retained in the admission are
the original authoring context and are not execution authority for later runs.

## Consequences

- A retry does not require a new Workflow version.
- Multiple Campaigns can use the same admitted Workflow version.
- A changed Workflow body at the same ID and version remains rejected.
