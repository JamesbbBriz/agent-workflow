# ADR 0008: Control-plane read model and UI

Status: accepted

The product GUI needs one stable read boundary for Jobs, Campaigns, Runs, approvals, Change Cases, Provider readiness, and audit receipts. Those views must not rebuild authority from browser state or introduce a second workflow store.

`GET /v1/control-plane` therefore returns the generated `ControlPlaneSnapshot@1`: the canonical Canvas portfolio, projected Change Cases, and live bundled-provider readiness. Existing preview/confirm endpoints remain the only write surfaces. The server validates the response against the JSON Schema before returning it, and the generated Go and TypeScript bindings are the shared contract.

The web application composes open-source shadcn/ui-style primitives, Radix, Tailwind, and the existing React Flow Canvas. Paid shadcnblocks source is not copied into this public repository; it may inform layout only. Technical hashes remain available in details and audit views, while primary screens use Job, Campaign, Workflow, approval, and next-action language.
