import contract from "../../contracts/agent-workflow.v1.schema.json";
import type { CanvasSnapshot, CampaignDefinition, Job, WorkflowAdmissionPreview, WorkflowDefinitionElement } from "./generated/agent-workflow.v1";

type JSONSchema = Record<string, unknown>;
type ToolEnvironment = {
  document: Pick<Document, "modelContext">;
  location: Pick<Location, "origin" | "pathname" | "search" | "hash">;
  history: Pick<History, "pushState" | "replaceState">;
  addEventListener: Window["addEventListener"];
  removeEventListener: Window["removeEventListener"];
  fetch: typeof fetch;
  crypto: Pick<Crypto, "randomUUID" | "subtle">;
};

type WebMCPOptions = {
  signal?: AbortSignal;
  onCanvas(snapshot: CanvasSnapshot): void;
  onNavigateApproval(artifactID: string): void;
};

type Envelope<T> = { ok: boolean; data?: T; error?: string; code?: string };

const defs = (contract as unknown as { $defs: Record<string, JSONSchema> }).$defs;
const shape = (required: string[], properties: Record<string, JSONSchema>): JSONSchema => ({ type: "object", additionalProperties: false, required, properties });
const closed = (required: string[], properties: Record<string, JSONSchema>): JSONSchema => ({
  ...shape(required, properties), $defs: defs,
});

export const webMCPToolSchemas = {
  inspect_current_canvas: closed([], {
    entity: { enum: ["job", "campaign", "workflow", "node"] },
    id: { type: "string", minLength: 1, maxLength: 160 },
  }),
  explain_context_blockers: closed([], {
    node_id: { type: "string", minLength: 1, maxLength: 160 },
  }),
  preview_workflow_admission: closed(["job", "campaign", "workflow"], {
    job: { $ref: "#/$defs/JobDefinition" },
    campaign: { $ref: "#/$defs/CampaignDefinition" },
    workflow: { $ref: "#/$defs/WorkflowDefinition" },
  }),
  navigate_pending_approval: closed(["artifact_id"], {
    artifact_id: { type: "string", minLength: 1, maxLength: 160 },
  }),
  confirm_authorized_action: {
    ...shape(["preview"], { preview: { $ref: "#/$defs/WorkflowAdmissionPreview" } }),
    $defs: defs,
  },
} satisfies Record<string, JSONSchema>;

export async function installWebMCP(options: WebMCPOptions, environment: ToolEnvironment = {
  document, location, history, addEventListener: window.addEventListener.bind(window), removeEventListener: window.removeEventListener.bind(window),
  fetch: globalThis.fetch.bind(globalThis), crypto: globalThis.crypto,
}): Promise<() => void> {
  const modelContext = environment.document.modelContext;
  if (!modelContext) return () => undefined;
  const pageOrigin = environment.location.origin;
  const pageRoute = currentRoute(environment.location);
  const controller = new AbortController();
  const originalPushState = environment.history.pushState;
  const originalReplaceState = environment.history.replaceState;
  const routeChanged = () => {
    if (environment.location.origin !== pageOrigin || currentRoute(environment.location) !== pageRoute) teardown();
  };
  const wrappedPushState: History["pushState"] = function(data, unused, url) { originalPushState.call(environment.history, data, unused, url); routeChanged(); };
  const wrappedReplaceState: History["replaceState"] = function(data, unused, url) { originalReplaceState.call(environment.history, data, unused, url); routeChanged(); };
  const teardown = () => {
    controller.abort();
    if (environment.history.pushState === wrappedPushState) environment.history.pushState = originalPushState;
    if (environment.history.replaceState === wrappedReplaceState) environment.history.replaceState = originalReplaceState;
    environment.removeEventListener("popstate", routeChanged);
    environment.removeEventListener("hashchange", routeChanged);
    options.signal?.removeEventListener("abort", teardown);
  };
  environment.history.pushState = wrappedPushState;
  environment.history.replaceState = wrappedReplaceState;
  environment.addEventListener("popstate", routeChanged);
  environment.addEventListener("hashchange", routeChanged);
  options.signal?.addEventListener("abort", teardown);
  if (options.signal?.aborted) teardown();
  let session: { token: string; subject: string };
  try {
    session = await api(environment, controller.signal, pageOrigin, "", "", "", "/v1/webmcp/session");
  } catch (error) {
    teardown();
    throw error;
  }

  const call = async <T>(tool: keyof typeof webMCPToolSchemas, path: string, input: unknown, method: "GET" | "POST" = "GET", executionSignal?: AbortSignal) => {
    if (environment.location.origin !== pageOrigin || currentRoute(environment.location) !== pageRoute) throw new Error("page_scope_changed");
    const body = method === "POST" ? JSON.stringify(input) : "";
    const inputHash = await sha256(environment.crypto, method === "POST" ? body : JSON.stringify(input));
    const signal = executionSignal ? AbortSignal.any([controller.signal, executionSignal]) : controller.signal;
    return api<T>(environment, signal, pageOrigin, session.token, session.subject, tool, path, method, body, inputHash, JSON.stringify(input));
  };
  const register = (name: keyof typeof webMCPToolSchemas, description: string, readOnly: boolean, execute: ModelContextTool["execute"]) =>
    modelContext.registerTool({
      name, title: name.split("_").map(capitalize).join(" "), description, inputSchema: webMCPToolSchemas[name],
      annotations: { readOnlyHint: readOnly, untrustedContentHint: true }, execute,
    }, { signal: controller.signal });

  try {
    await Promise.all([
    register("inspect_current_canvas", "Inspect the exact Job, Campaign, Workflow, or Node currently projected by the Go Core.", true, async (value, execution) => {
      const input = value as { entity?: string; id?: string };
      const snapshot = await call<CanvasSnapshot>("inspect_current_canvas", "/v1/canvas", input, "GET", execution?.signal);
      if (!input.entity) return snapshot;
      if (input.entity === "job") return snapshot.definition.job.id === input.id || !input.id ? snapshot.definition.job : notFound();
      if (input.entity === "campaign") return snapshot.definition.campaign.id === input.id || !input.id ? snapshot.definition.campaign : notFound();
      if (input.entity === "workflow") return snapshot.definition.workflows.find((item) => !input.id || item.id === input.id) ?? notFound();
      if (input.entity === "node") return snapshot.definition.workflows.flatMap((item) => item.nodes).find((item) => item.id === input.id) ?? notFound();
      throw new Error("invalid_entity");
    }),
    register("explain_context_blockers", "Explain only unresolved Context ports and canonical blockers visible in the current Canvas.", true, async (value, execution) => {
      const input = value as { node_id?: string };
      const snapshot = await call<CanvasSnapshot>("explain_context_blockers", "/v1/canvas", input, "GET", execution?.signal);
      const executions = snapshot.executions.filter((item) => !input.node_id || item.node_id === input.node_id);
      return executions.map((item) => ({
        aggregate_id: item.aggregate_id, node_id: item.node_id, blocker_code: item.blocker_code, blocker_message: item.blocker_message,
        unresolved_context: item.context_ports.filter((port) => port.status !== "resolved"),
      })).filter((item) => item.blocker_code || item.unresolved_context.length > 0);
    }),
    register("preview_workflow_admission", "Ask the Go Core for an immutable, hash-bound Workflow admission preview without mutating canonical state.", true, (value, execution) => {
      const input = value as { job: Job; campaign: CampaignDefinition; workflow: WorkflowDefinitionElement };
      return call<{ preview: WorkflowAdmissionPreview }>("preview_workflow_admission", "/v1/workflows/preview", { actor: session.subject, ...input }, "POST", execution?.signal);
    }),
    register("navigate_pending_approval", "Navigate to an exact pending Action Artifact already visible in the canonical Canvas.", true, async (value, execution) => {
      const input = value as { artifact_id: string };
      const snapshot = await call<CanvasSnapshot>("navigate_pending_approval", "/v1/canvas", input, "GET", execution?.signal);
      const artifact = snapshot.executions.flatMap((item) => item.outputs).find((item) => item.id === input.artifact_id && item.approval_state === "pending");
      if (!artifact) throw new Error("pending_approval_not_found");
      options.onNavigateApproval(artifact.id);
      return { artifact_id: artifact.id, approval_state: artifact.approval_state };
    }),
    register("confirm_authorized_action", "Submit only an exact, unmodified Core preview token for Workflow admission.", false, async (value, execution) => {
      const input = value as { preview: WorkflowAdmissionPreview };
      const result = await call<{ canvas: CanvasSnapshot }>("confirm_authorized_action", "/v1/workflows/confirm", { actor: session.subject, ...input }, "POST", execution?.signal);
      options.onCanvas(result.canvas);
      return result;
    }),
    ]);
  } catch (error) {
    teardown();
    throw error;
  }
  return teardown;
}

async function api<T>(environment: ToolEnvironment, signal: AbortSignal, origin: string, token: string, subject: string, tool: string, path: string, method: "GET" | "POST" = "GET", body = "", inputHash = "", input = "") {
  const headers: Record<string, string> = { "X-Agent-Workflow-Page-Origin": origin };
  if (token) Object.assign(headers, {
    Authorization: `Bearer ${token}`, "X-Agent-Workflow-Subject": subject, "X-Agent-Workflow-Tool": tool,
    "X-Agent-Workflow-Request-ID": environment.crypto.randomUUID(), "X-Agent-Workflow-Input-SHA256": inputHash, "X-Agent-Workflow-Input": input,
  });
  if (method === "POST") headers["Content-Type"] = "application/json";
  const response = await environment.fetch(path, { method, headers, body: method === "POST" ? body : undefined, credentials: "same-origin", signal });
  const envelope = await response.json() as Envelope<T>;
  if (!response.ok || !envelope.ok || envelope.data === undefined) throw new Error(envelope.code ?? "webmcp_request_failed");
  return envelope.data;
}

async function sha256(crypto: Pick<Crypto, "subtle">, value: string) {
  const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return `sha256:${[...new Uint8Array(hash)].map((byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}

function capitalize(value: string) { return value.charAt(0).toUpperCase() + value.slice(1); }
function notFound(): never { throw new Error("canvas_entity_not_found"); }
function currentRoute(location: Pick<Location, "pathname" | "search" | "hash">) { return `${location.pathname}${location.search}${location.hash}`; }
