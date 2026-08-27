import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import fixture from "../public/canvas.response.json";
import { ApprovalPanel, BuilderPanel, mergeAdmissionReadback } from "./builder";
import type { CanvasSnapshot } from "./generated/agent-workflow.v1";

const snapshot = fixture.data as unknown as CanvasSnapshot;

describe("Workflow Builder surfaces", () => {
  it("renders only the exact draft Nodes and Core-gated actions", () => {
    const html = renderToStaticMarkup(<BuilderPanel snapshot={snapshot} onClose={() => undefined} onCanvas={() => undefined} />);
    expect(html).toContain("Build an immutable Workflow");
    expect(html).toContain("Job and Campaign boundary");
    expect(html).toContain("Add Node");
    expect(html).toContain("Research");
    expect(html).toContain("Review");
    expect(html).toContain("Compile &amp; preview");
    expect(html).toContain("Confirm admission");
  });

  it("shows evidence, trade-offs, risk, and the exact pending action", () => {
    const artifact = snapshot.executions[0].outputs[0];
    const html = renderToStaticMarkup(<ApprovalPanel snapshot={snapshot} artifact={artifact} onClose={() => undefined} onCanvas={() => undefined} />);
    expect(html).toContain("Human decision");
    expect(html).toContain("Evidence");
    expect(html).toContain("Options and trade-offs");
    expect(html).toContain("Exact proposed action");
    expect(html).toContain("Approve exact action");
  });

  it("keeps runtime evidence when an admission readback updates the definition", () => {
    const admitted = structuredClone(snapshot);
    admitted.executions = [];
    admitted.replays = [];
    admitted.definition.workflows[0].version = 2;
    admitted.definition.campaign.workflow_plan = ["research-review@2"];
    admitted.definition.workflow_states = { "research-review@2": "admitted" };
    admitted.admission_replays = snapshot.admission_replays;
    const merged = mergeAdmissionReadback(snapshot, admitted);
    expect(merged.executions).toEqual(snapshot.executions);
    expect(merged.replays).toEqual(snapshot.replays);
    expect(merged.definition.campaign.workflow_plan).toEqual(["research-review@2"]);
    expect(merged.definition.workflows[0].version).toBe(2);
    expect(merged.definition.workflow_states?.["research-review@2"]).toBe("admitted");
    expect(merged.admission_replays).toHaveLength(1);
  });
});
