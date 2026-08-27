import { describe, expect, it, vi } from "vitest";
import contract from "../../contracts/agent-workflow.v1.schema.json";
import fixture from "../public/canvas.response.json";
import type { CanvasSnapshot } from "./generated/agent-workflow.v1";
import { installWebMCP, webMCPToolSchemas } from "./webmcp";

const snapshot = fixture.data as unknown as CanvasSnapshot;

describe("experimental WebMCP adapter", () => {
  it("does nothing when document.modelContext is absent", async () => {
    const fetcher = vi.fn();
    const cleanup = await installWebMCP(options(), environment(undefined, fetcher));
    cleanup();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("registers five page-scoped tools from canonical schemas and unregisters them together", async () => {
    const tools = new Map<string, ModelContextTool>();
    const signals: AbortSignal[] = [];
    const modelContext = fakeModelContext(tools, signals);
    const fetcher = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === "/v1/webmcp/session") return response({ token: "session-token", subject: "local-operator" });
      expect(init?.headers).toMatchObject({
        Authorization: "Bearer session-token",
        "X-Agent-Workflow-Page-Origin": "http://127.0.0.1:5173",
      });
      return response(snapshot);
    });
    const env = environment(modelContext, fetcher);
    const callbacks = options();
    const cleanup = await installWebMCP(callbacks, env);

    expect([...tools.keys()]).toEqual([
      "inspect_current_canvas", "explain_context_blockers", "preview_workflow_admission", "navigate_pending_approval", "confirm_authorized_action",
    ]);
    expect(webMCPToolSchemas.preview_workflow_admission).toMatchObject({ $defs: (contract as { $defs: object }).$defs });
    expect(tools.get("confirm_authorized_action")?.annotations?.readOnlyHint).toBe(false);
    expect(await tools.get("inspect_current_canvas")?.execute({ entity: "job", id: snapshot.definition.job.id })).toEqual(snapshot.definition.job);
    await expect(tools.get("navigate_pending_approval")?.execute({ artifact_id: "missing" })).rejects.toThrow("pending_approval_not_found");
    await tools.get("navigate_pending_approval")?.execute({ artifact_id: "example-recommendation" });
    expect(callbacks.onNavigateApproval).toHaveBeenCalledWith("example-recommendation");

    env.history.pushState({}, "", "/another-page");
    expect(tools.size).toBe(0);
    cleanup();
    expect(signals).toHaveLength(5);
    expect(signals.every((signal) => signal.aborted)).toBe(true);
  });

  it("surfaces safe auth and rate errors without inventing a result", async () => {
    const tools = new Map<string, ModelContextTool>();
    const fetcher = vi.fn(async (path: string) => path === "/v1/webmcp/session"
      ? response({ token: "session-token", subject: "local-operator" })
      : new Response(JSON.stringify({ ok: false, error: "hidden detail", code: "rate_limited" }), { status: 429, headers: { "Content-Type": "application/json" } }));
    await installWebMCP(options(), environment(fakeModelContext(tools), fetcher));
    await expect(tools.get("inspect_current_canvas")?.execute({})).rejects.toThrow("rate_limited");
  });

  it("cancels an in-flight tool request when the page adapter is removed", async () => {
    const tools = new Map<string, ModelContextTool>();
    const fetcher = vi.fn((path: string, init?: RequestInit) => {
      if (path === "/v1/webmcp/session") return Promise.resolve(response({ token: "session-token", subject: "local-operator" }));
      return new Promise<Response>((_, reject) => {
        const fail = () => reject(new DOMException("aborted", "AbortError"));
        if (init?.signal?.aborted) fail(); else init?.signal?.addEventListener("abort", fail);
      });
    });
    const cleanup = await installWebMCP(options(), environment(fakeModelContext(tools), fetcher));
    const operation = new AbortController();
    const pending = tools.get("inspect_current_canvas")?.execute({}, { signal: operation.signal });
    operation.abort();
    await expect(pending).rejects.toThrow("aborted");
    cleanup();
  });
});

function options() {
  return { onCanvas: vi.fn(), onNavigateApproval: vi.fn() };
}

function environment(modelContext: ModelContext | undefined, fetcher: typeof fetch | ReturnType<typeof vi.fn>) {
  const location = { origin: "http://127.0.0.1:5173", pathname: "/", search: "", hash: "" };
  return {
    document: { modelContext },
    location,
    history: {
      pushState: (_data: unknown, _unused: string, url?: string | URL | null) => { if (url) location.pathname = new URL(url, location.origin).pathname; },
      replaceState: (_data: unknown, _unused: string, url?: string | URL | null) => { if (url) location.pathname = new URL(url, location.origin).pathname; },
    },
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    fetch: fetcher as typeof fetch,
    crypto: globalThis.crypto,
  };
}

function fakeModelContext(tools: Map<string, ModelContextTool>, signals: AbortSignal[] = []): ModelContext {
  return { registerTool: async (tool, options) => {
    tools.set(tool.name, tool);
    if (options?.signal) {
      signals.push(options.signal);
      options.signal.addEventListener("abort", () => tools.delete(tool.name));
    }
  } };
}

function response(data: unknown) {
  return new Response(JSON.stringify({ ok: true, data }), { status: 200, headers: { "Content-Type": "application/json" } });
}
