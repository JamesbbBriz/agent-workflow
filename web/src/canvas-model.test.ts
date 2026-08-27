import { describe, expect, it } from "vitest";
import fixture from "../public/canvas.response.json";
import type { CanvasSnapshot } from "./generated/agent-workflow.v1";
import { buildGraph, compareBundles } from "./canvas-model";

const snapshot = fixture.data as unknown as CanvasSnapshot;

describe("canonical Canvas projection", () => {
  it("renders runtime entities only from recorded executions and outputs", () => {
    const graph = buildGraph(snapshot, "runtime");
    expect(graph.nodes.filter((node) => node.data.entityKind === "execution")).toHaveLength(snapshot.executions.length);
    expect(graph.nodes.filter((node) => node.data.entityKind === "artifact")).toHaveLength(snapshot.executions.flatMap((execution) => execution.outputs).length);
    expect(graph.nodes.some((node) => node.data.status === "eligible")).toBe(false);
  });

  it("compares exact bundle editions by hash", () => {
    const left = snapshot.executions[0].bundle;
    const right = structuredClone(left);
    right.entries[0].sha256 = `sha256:${"a".repeat(64)}`;
    expect(compareBundles(left, right)).toEqual([
      expect.objectContaining({ artifactType: right.entries[0].artifact_type, state: "changed" }),
    ]);
  });

  it("does not confuse identical Node IDs across Workflows", () => {
    const multi = structuredClone(snapshot);
    const second = structuredClone(multi.definition.workflows[0]);
    second.id = "second-review";
    multi.definition.workflows.push(second);
    multi.definition.campaign.workflow_plan.push("second-review@1");
    multi.next_safe_action = {
      kind: "start_node",
      workflow_ref: "second-review@1",
      node_id: "research",
      reason: "All declared dependencies are complete.",
    };

    const graph = buildGraph(multi, "runtime");
    expect(graph.nodes.find((node) => node.id === "node:research-review@1:research")?.data.status).toBe("completed");
    expect(graph.nodes.find((node) => node.id === "node:second-review@1:research")?.data.status).toBe("admitted");
  });
});
