import type { EvidenceFrontier, OutputElement, ReplayBundleReceipt } from "./generated/agent-workflow.v1";

const frontier: EvidenceFrontier = {
  cutoff: "2026-08-28T00:00:00Z",
  source_hashes: [],
};

const receipt: Pick<ReplayBundleReceipt, "occurred_at"> = {
  occurred_at: "2026-08-28T00:00:00Z",
};

const legacyOutput: OutputElement = {
  artifact_type: "recommendation",
  id: "recommendation",
  max_items: 1,
  min_items: 0,
};

void [frontier, receipt, legacyOutput];
