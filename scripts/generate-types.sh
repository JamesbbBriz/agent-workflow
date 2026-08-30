#!/bin/sh
set -eu

out="$1"
./node_modules/.bin/quicktype \
  --lang typescript \
  --just-types \
  --no-date-times \
  --src contracts/codegen.v1.schema.json \
  --src-lang schema \
  --out "$out"

cat >> "$out" <<'EOF'

// Stable v1 export aliases. Quicktype names shared schemas by first use, so
// adding a new catalog entry must not rename existing downstream imports.
export type NodeElement = DefinitionElement;
export type ReplayBundleElement = CampaignReplay;
export type Status = ContextPortStatus;
export type Mode = WaitModeEnum;
EOF
