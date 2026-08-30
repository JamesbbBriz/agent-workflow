import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import fixture from "../public/canvas.response.json";
import { EvidencePage, NodeCardContent } from "./app";
import type { CanvasSnapshot, EvidenceWindowReport } from "./generated/agent-workflow.v1";

const snapshot = fixture.data as unknown as CanvasSnapshot;

describe("Canvas node accessibility", () => {
  it("labels the Node and every Context port without relying on color", () => {
    const execution = snapshot.executions[0];
    const html = renderToStaticMarkup(<NodeCardContent data={{
      entityKind: "execution",
      title: "Research",
      subtitle: "bounded-agent@1",
      status: execution.status,
      execution,
      contextPorts: execution.context_ports,
    }} />);
    expect(html).toContain('tabindex="0"');
	expect(html).toContain('role="button"');
    expect(html).toContain("Research, Completed");
    expect(html).toContain("Project Brief Context, Resolved");
    expect(html).toContain("Required");
  });

  it("opens Node details with Enter", () => {
	let selected = false;
	const card = NodeCardContent({ data: { entityKind: "node", title: "Review", subtitle: "human", status: "configured" }, onSelect: () => { selected = true; } });
	const target = {};
	card.props.onKeyDown({ key: "Enter", target, currentTarget: target, preventDefault: () => undefined });
	expect(selected).toBe(true);
  });
});

describe("Evidence window", () => {
  it("distinguishes available roles from roles proven by receipts", () => {
    const report: EvidenceWindowReport = {
      kind: "evidence_window_report",
      schema_version: 1,
      window: { started_at: "2026-08-30T00:00:00Z", ended_at: "2026-08-30T01:00:00Z", duration_seconds: 3600 },
      available_role_ids: ["context_researcher", "analyst_reviewer", "effect_executor"],
      invoked_role_ids: ["context_researcher"],
      counts: { agent_invocations: 1, approvals: 0, context_refreshes: 1, effects: 0, outcomes: 0, readbacks: 0, receipts: 2, replays: 1 },
      evidence: [],
    };
    const html = renderToStaticMarkup(<EvidencePage report={report} />);
    expect(html).toContain("Evidence window");
    expect(html).toContain("1 of 3 roles evidenced");
    expect(html).toContain("Context Researcher");
  });

  it("does not present a failed refresh as empty evidence", () => {
    const html = renderToStaticMarkup(<EvidencePage error="Canonical ledger could not be read." />);
    expect(html).toContain("Evidence refresh failed.");
    expect(html).not.toContain("No evidence receipts");
  });
});
