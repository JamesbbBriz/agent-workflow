import type { Edge, Node } from "@xyflow/react";
import type {
  Bundle,
  CampaignState,
  CanvasSnapshot,
  ActionArtifact,
  ContextPortElement,
  ExecutionElement,
  Intent,
  NodeElement,
} from "./generated/agent-workflow.v1";

export type CanvasMode = "definition" | "runtime";
export type CanvasEntityKind = "job" | "campaign" | "workflow" | "node" | "execution" | "context" | "artifact";

export interface CanvasNodeData extends Record<string, unknown> {
  entityKind: CanvasEntityKind;
  title: string;
  subtitle: string;
  status: CampaignState;
  intent?: Intent;
  definition?: NodeElement;
  execution?: ExecutionElement;
  contextPorts?: ContextPortElement[];
  hash?: string;
  artifact?: ActionArtifact;
}

export type CanvasGraphNode = Node<CanvasNodeData, "canvas">;

export interface CanvasGraph {
  nodes: CanvasGraphNode[];
  edges: Edge[];
}

export interface BundleDifference {
  artifactType: string;
  requirementId?: string;
  leftHash?: string;
  rightHash?: string;
  state: "added" | "removed" | "changed";
}

export function buildGraph(snapshot: CanvasSnapshot, mode: CanvasMode): CanvasGraph {
  const nodes: CanvasGraphNode[] = [];
  const edges: Edge[] = [];
  const jobNode = `job:${snapshot.definition.job.id}`;
  const campaignNode = `campaign:${snapshot.definition.campaign.id}`;

  nodes.push(graphNode(jobNode, 40, 220, "job", snapshot.definition.job.intent.title, snapshot.definition.job.scope.subject_ids.join(", "), "configured", snapshot.definition.job.intent));
  nodes.push(graphNode(campaignNode, 340, 220, "campaign", snapshot.definition.campaign.intent.title, snapshot.definition.campaign.archetype, snapshot.definition.campaign_state, snapshot.definition.campaign.intent));
  edges.push(graphEdge(jobNode, campaignNode, "contains"));

  snapshot.definition.workflows.forEach((workflow, workflowIndex) => {
    const workflowRef = `${workflow.id}@${workflow.version}`;
    const workflowNode = `workflow:${workflowRef}`;
    const y = 100 + workflowIndex * 360;
    nodes.push(graphNode(workflowNode, 640, y, "workflow", workflow.intent.title, workflowRef, snapshot.definition.workflow_states?.[workflowRef] ?? "configured", workflow.intent));
    edges.push(graphEdge(campaignNode, workflowNode, "runs"));

    workflow.nodes.forEach((definition, nodeIndex) => {
      const execution = mode === "runtime" ? snapshot.executions.find((item) => item.bundle.workflow_ref === workflowRef && item.node_id === definition.id) : undefined;
      const entityKind: CanvasEntityKind = execution ? "execution" : "node";
	  const status = execution?.status ?? (mode === "runtime" ? nextNodeStatus(snapshot, workflowRef, definition.id) : "configured");
      const nodeID = `node:${workflowRef}:${definition.id}`;
      nodes.push({
        ...graphNode(nodeID, 960 + nodeIndex * 300, y, entityKind, humanize(definition.id), definition.executor, status),
        data: {
          entityKind,
          title: humanize(definition.id),
          subtitle: definition.executor,
          status,
          definition,
          execution,
          intent: workflow.intent,
          contextPorts: execution?.context_ports ?? definition.context.map((requirement) => ({
            ...requirement,
            node_id: definition.id,
			status: "configured" as const,
            producer: requirement.selector,
            consumers: [`${workflowRef}/${definition.id}`],
            evidence_frontier: snapshot.definition.campaign.evidence_frontier,
          })),
        },
      });
      if (definition.depends_on.length === 0) {
        edges.push(graphEdge(workflowNode, nodeID, "starts"));
      }
      definition.depends_on.forEach((dependency) => {
        edges.push(graphEdge(`node:${workflowRef}:${dependency}`, nodeID, "then"));
      });

      if (mode === "runtime" && execution) {
        execution.context_ports.forEach((port, portIndex) => {
          if (!port.edition) return;
          const contextID = `context:${execution.aggregate_id}:${port.edition.id}`;
          nodes.push(graphNode(contextID, 960 + nodeIndex * 300, y + 170 + portIndex * 110, "context", humanize(port.pack_type), port.edition.id, port.status === "resolved" ? "completed" : "blocked", undefined, port.edition.content_sha256));
          edges.push(graphEdge(contextID, nodeID, port.required ? "required" : "optional"));
        });
        execution.outputs.forEach((artifact, artifactIndex) => {
          const artifactID = `artifact:${artifact.id}`;
          const artifactNode = graphNode(artifactID, 1260 + nodeIndex * 300, y + 190 + artifactIndex * 110, "artifact", humanize(artifact.artifact_type), artifact.approval_state, execution.status, undefined, artifact.content_sha256);
          artifactNode.data.artifact = artifact;
          artifactNode.data.execution = execution;
          nodes.push(artifactNode);
          edges.push(graphEdge(nodeID, artifactID, "produced"));
        });
      }
    });
  });
  return { nodes, edges };
}

export function compareBundles(left: Bundle, right: Bundle): BundleDifference[] {
	const leftEntries = new Map(left.entries.map((entry) => [entry.requirement_id ?? `${entry.artifact_type}:${entry.id}`, entry]));
	const rightEntries = new Map(right.entries.map((entry) => [entry.requirement_id ?? `${entry.artifact_type}:${entry.id}`, entry]));
  const keys = new Set([...leftEntries.keys(), ...rightEntries.keys()]);
  const differences: BundleDifference[] = [];
	for (const key of [...keys].sort()) {
		const before = leftEntries.get(key);
		const after = rightEntries.get(key);
		const artifactType = after?.artifact_type ?? before?.artifact_type ?? key;
		const requirementId = after?.requirement_id ?? before?.requirement_id;
		if (!before) differences.push({ artifactType, requirementId, rightHash: after?.sha256, state: "added" });
		else if (!after) differences.push({ artifactType, requirementId, leftHash: before.sha256, state: "removed" });
		else if (before.sha256 !== after.sha256) differences.push({ artifactType, requirementId, leftHash: before.sha256, rightHash: after.sha256, state: "changed" });
  }
  return differences;
}

function graphNode(id: string, x: number, y: number, entityKind: CanvasEntityKind, title: string, subtitle: string, status: CampaignState, intent?: Intent, hash?: string): CanvasGraphNode {
  return { id, type: "canvas", position: { x, y }, data: { entityKind, title, subtitle, status, intent, hash } };
}

function graphEdge(source: string, target: string, label: string): Edge {
  return { id: `${source}:${target}:${label}`, source, target, label, type: "smoothstep" };
}

function nextNodeStatus(snapshot: CanvasSnapshot, workflowRef: string, nodeID: string): CampaignState {
  if (snapshot.next_safe_action.workflow_ref !== workflowRef || snapshot.next_safe_action.node_id !== nodeID) return "configured";
  if (snapshot.next_safe_action.kind === "request_approval") return "awaiting_human";
  if (snapshot.next_safe_action.kind === "request_context") return "blocked";
  return "configured";
}

export function humanize(value: string): string {
  return value.replaceAll("-", " ").replace(/\b\w/g, (character) => character.toUpperCase());
}
