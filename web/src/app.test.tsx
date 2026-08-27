import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import fixture from "../public/canvas.response.json";
import { NodeCardContent } from "./app";
import type { CanvasSnapshot } from "./generated/agent-workflow.v1";

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
    expect(html).toContain("Research, Completed");
    expect(html).toContain("Project Brief Context, Resolved");
    expect(html).toContain("Required");
  });
});
