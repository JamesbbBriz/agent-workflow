import type { EvidenceFrontier, Receipt } from "./generated/agent-workflow.v1";

const frontier: EvidenceFrontier = {
  cutoff: "2026-08-28T00:00:00Z",
  source_hashes: [],
};

const receipt: Pick<Receipt, "occurred_at"> = {
  occurred_at: "2026-08-28T00:00:00Z",
};

void [frontier, receipt];
