#!/bin/sh
set -eu

destination="web/public/canvas.response.json"
mkdir -p "$(dirname "$destination")"
temporary="$(mktemp "${destination}.XXXXXX")"
trap 'rm -f "$temporary"' EXIT

go run ./cmd/agent-workflow canvas \
  --file examples/research-review.workflow.json \
  --at 2026-08-27T00:00:00Z >"$temporary"
mv "$temporary" "$destination"
