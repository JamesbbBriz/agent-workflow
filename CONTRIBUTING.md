# Contributing

## Before changing code

1. Start from an issue labelled `ready-for-agent`.
2. Keep one externally demonstrable behavior per pull request.
3. Treat public schemas, receipts, authority, storage, networking, security policy, and WebMCP as contract changes.
4. Never commit credentials, customer data, private corpora, raw provider responses, or production-like fixtures.

## Checks

```bash
go test ./... -count=1
go vet ./...
go build ./...
npm ci
npm run check:generated
npm run check:types
git diff --check
```

Tests should exercise the highest public seam: CLI, schema, generated contract, API, Canvas behavior, or exact Replay. Avoid tests coupled to private helpers.

Pull requests include the problem, behavior, security impact, compatibility notes, and exact commands run. A fixed-SHA Standards and Spec review must have no blocker or major finding before merge.
