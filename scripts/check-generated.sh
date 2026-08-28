#!/bin/sh
set -eu

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

go run github.com/atombender/go-jsonschema@v0.24.1 \
  --package contractsv1 \
  --struct-name-from-title \
  --only-models \
  --tags json \
  --output "$tmp_dir/contracts.go" \
  contracts/codegen.v1.schema.json

sh scripts/generate-types.sh "$tmp_dir/agent-workflow.v1.ts"

cmp "$tmp_dir/contracts.go" pkg/contractsv1/zz_generated.go
cmp "$tmp_dir/agent-workflow.v1.ts" web/src/generated/agent-workflow.v1.ts
