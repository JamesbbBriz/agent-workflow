import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import {
  Background,
  Controls,
  Handle,
  Position,
  ReactFlow,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  ArrowsLeftRight,
  CheckCircle,
  Clock,
  FileText,
  GearSix,
  LockKey,
  Package,
  WarningCircle,
  Wrench,
  X,
} from "@phosphor-icons/react";
import type { CanvasSnapshot, ContextPortElement } from "./generated/agent-workflow.v1";
import { buildGraph, compareBundles, humanize, type CanvasGraphNode, type CanvasMode, type CanvasNodeData } from "./canvas-model";
import { ApprovalPanel, BuilderPanel } from "./builder";
import { installWebMCP } from "./webmcp";
import "./styles.css";

type Selection = { kind: "node"; data: CanvasNodeData } | { kind: "port"; port: ContextPortElement };

interface CanvasResponse {
  ok: boolean;
  data: CanvasSnapshot;
}

export function App() {
  const [snapshot, setSnapshot] = useState<CanvasSnapshot>();
  const [error, setError] = useState<string>();
  const [mode, setMode] = useState<CanvasMode>("runtime");
  const [selection, setSelection] = useState<Selection>();
  const [comparing, setComparing] = useState(false);
  const [building, setBuilding] = useState(false);
  const [approving, setApproving] = useState<CanvasNodeData>();
  const snapshotRef = useRef(snapshot);
  snapshotRef.current = snapshot;

  useEffect(() => {
    fetch("/v1/canvas")
      .then((response) => {
        if (!response.ok) return fetch("/canvas.response.json");
        return response;
      })
      .then((response) => {
        if (!response.ok) throw new Error("Canvas data is unavailable.");
        return response.json() as Promise<CanvasResponse>;
      })
      .then((response) => {
        if (!response.ok) throw new Error("Canvas data was rejected by the Core.");
        setSnapshot(response.data);
      })
      .catch((reason: unknown) => setError(reason instanceof Error ? reason.message : "Canvas data is unavailable."));
  }, []);

  useEffect(() => {
    if (!snapshot) return;
    let disposed = false;
    let uninstall: () => void = () => undefined;
    const lifecycle = new AbortController();
    void installWebMCP({
      signal: lifecycle.signal,
      onCanvas: setSnapshot,
      onNavigateApproval: (artifactID) => {
        const current = snapshotRef.current;
        const node = current && buildGraph(current, "runtime").nodes.find((item) => item.data.artifact?.id === artifactID);
        if (node) setApproving(node.data);
      },
    }).then((cleanup) => disposed ? cleanup() : (uninstall = cleanup)).catch(() => undefined);
    return () => { disposed = true; lifecycle.abort(); uninstall(); };
  }, [snapshot?.definition.job.id]);

  const graph = useMemo(() => snapshot ? buildGraph(snapshot, mode) : { nodes: [], edges: [] }, [snapshot, mode]);
  const nodes = useMemo(() => graph.nodes.map((node) => ({
    ...node,
	data: {
	  ...node.data,
	  onSelect: () => setSelection({ kind: "node", data: node.data }),
	  onPortSelect: (port: ContextPortElement) => setSelection({ kind: "port", port }),
	},
  })), [graph.nodes]);

  if (error) return <StateScreen title="Canvas unavailable" detail={error} />;
  if (!snapshot) return <StateScreen title="Loading canonical graph" detail="Reading the generated Canvas snapshot." loading />;

  const canCompare = snapshot.executions.length > 1;
  return (
    <main className="app-shell">
      <header className="topbar">
        <div className="brand"><Package size={22} weight="duotone" /><strong>Agent Workflow</strong></div>
        <label className="job-picker">
          <span>Job</span>
          <select aria-label="Select Job" value={snapshot.definition.job.id} onChange={() => undefined}>
            <option value={snapshot.definition.job.id}>{snapshot.definition.job.intent.title}</option>
          </select>
        </label>
        <div className="view-switch" aria-label="Canvas view">
          <button className={mode === "definition" ? "is-active" : ""} onClick={() => setMode("definition")}>Definition</button>
          <button className={mode === "runtime" ? "is-active" : ""} onClick={() => setMode("runtime")}>Runtime</button>
        </div>
        <button className="compare-button" disabled={!canCompare} title={canCompare ? "Compare two Context Bundles" : "Two executions are required"} onClick={() => setComparing(true)}>
          <ArrowsLeftRight size={18} /> Compare Context
        </button>
        <button className="builder-button" onClick={() => setBuilding(true)}><Wrench size={18} /> Build Workflow</button>
      </header>

      <section className="canvas-stage" aria-label={`${mode} workflow Canvas`}>
        <ReactFlow
          nodes={nodes}
          edges={graph.edges}
          nodeTypes={canvasNodeTypes}
          nodesDraggable={false}
          nodesConnectable={false}
          fitView
          fitViewOptions={{ padding: 0.16 }}
          minZoom={0.35}
          maxZoom={1.5}
        >
          <Background gap={24} size={1} />
          <Controls showInteractive={false} />
        </ReactFlow>
      </section>

      {selection && <DetailPanel selection={selection} onClose={() => setSelection(undefined)} onApprove={(data) => { setSelection(undefined); setApproving(data); }} />}
      {comparing && canCompare && <ComparePanel snapshot={snapshot} onClose={() => setComparing(false)} />}
      {building && <BuilderPanel snapshot={snapshot} onClose={() => setBuilding(false)} onCanvas={setSnapshot} />}
      {approving?.artifact && <ApprovalPanel snapshot={snapshot} artifact={approving.artifact} onClose={() => setApproving(undefined)} onCanvas={(next) => { setSnapshot(next); setApproving(undefined); }} />}
      <footer className="next-action" aria-label="Next safe action">
        <strong>{humanize(snapshot.next_safe_action.kind)}</strong>
        <span>{snapshot.next_safe_action.reason}</span>
      </footer>
    </main>
  );
}

function GraphNodeCard({ data, selected }: NodeProps<CanvasGraphNode>) {
  return <><Handle type="target" position={Position.Left} /><NodeCardContent data={data} selected={selected} onSelect={data.onSelect as (() => void) | undefined} /><Handle type="source" position={Position.Right} /></>;
}

const canvasNodeTypes = { canvas: GraphNodeCard };

export function NodeCardContent({ data, selected = false, onSelect }: { data: CanvasNodeData; selected?: boolean; onSelect?: () => void }) {
  const onPortSelect = data.onPortSelect as ((port: ContextPortElement) => void) | undefined;
  return (
    <article className={`graph-node graph-node-${data.entityKind} ${selected ? "is-selected" : ""}`} role="button" tabIndex={0} aria-label={`${data.title}, ${humanize(data.status)}`} onClick={onSelect} onKeyDown={(event) => activateCard(event, onSelect)}>
      <div className="node-heading">
        <span className="node-icon" aria-hidden="true">{entityIcon(data.entityKind)}</span>
        <div><strong>{data.title}</strong><small>{data.subtitle}</small></div>
      </div>
      <div className="status-label">{statusIcon(data.status)}<span>{humanize(data.status)}</span></div>
      {data.contextPorts && data.contextPorts.length > 0 && (
        <div className="context-ports" aria-label="Context ports">
          {data.contextPorts.map((port) => (
            <button key={port.id} className={`context-port status-${port.status}`} aria-label={`${humanize(port.pack_type)} Context, ${humanize(port.status)}`} onClick={(event) => { event.stopPropagation(); onPortSelect?.(port); }}>
              <span>{port.required ? "Required" : "Optional"}</span>
              <strong>{humanize(port.pack_type)}</strong>
              <small>{humanize(port.status)}</small>
            </button>
          ))}
        </div>
      )}
    </article>
  );
}

function activateCard(event: KeyboardEvent<HTMLElement>, onSelect?: () => void) {
  if (event.currentTarget !== event.target || (event.key !== "Enter" && event.key !== " ")) return;
  event.preventDefault();
  onSelect?.();
}

export function DetailPanel({ selection, onClose, onApprove }: { selection: Selection; onClose: () => void; onApprove?: (data: CanvasNodeData) => void }) {
  const isPort = selection.kind === "port";
  const title = isPort ? humanize(selection.port.pack_type) : selection.data.title;
  return (
    <aside className="detail-panel" aria-label={`${title} details`}>
      <button className="icon-button" aria-label="Close details" onClick={onClose}><X size={18} /></button>
      <h2>{title}</h2>
      {isPort ? <PortDetails port={selection.port} /> : <NodeDetails data={selection.data} onApprove={onApprove} />}
    </aside>
  );
}

function PortDetails({ port }: { port: ContextPortElement }) {
  return (
    <dl className="fact-grid">
      <Fact label="State" value={humanize(port.status)} />
      <Fact label="Contract" value={`${port.pack_type}@${port.schema_version}`} />
      <Fact label="Producer" value={port.producer} />
      <Fact label="Requirement" value={port.required ? "Required" : "Optional"} />
      <Fact label="Evidence cutoff" value={formatTime(port.evidence_frontier.cutoff)} />
      <Fact label="Evidence sources" value={port.evidence_frontier.source_hashes.join("\n") || "No source hashes recorded"} mono />
      {port.edition && <>
        <Fact label="Edition" value={port.edition.id} />
        <Fact label="Authority" value={humanize(port.edition.authority)} />
        <Fact label="Scope" value={`${port.edition.scope.subject_type}: ${port.edition.scope.subject_ids.join(", ")}`} />
        <Fact label="Captured" value={formatTime(port.edition.captured_at)} />
        <Fact label="Expires" value={formatTime(port.edition.expires_at)} />
        <Fact label="Content hash" value={port.edition.content_sha256} mono />
        <Fact label="Provenance" value={port.edition.provenance.map((item) => `${item.artifact_type}: ${item.sha256}`).join("\n")} mono />
      </>}
      <Fact label="Consumers" value={port.consumers.join(", ")} />
    </dl>
  );
}

function NodeDetails({ data, onApprove }: { data: CanvasNodeData; onApprove?: (data: CanvasNodeData) => void }) {
  return (
    <dl className="fact-grid">
      <Fact label="State" value={humanize(data.status)} />
      {data.intent && <>
        <Fact label="Purpose" value={data.intent.summary} />
        <Fact label="Objective" value={data.intent.objective} />
      </>}
      {data.definition && <>
        <Fact label="Executor" value={data.definition.executor} />
        <Fact label="Capabilities" value={data.definition.capabilities.join(", ") || "None"} />
        <Fact label="Budget" value={`${data.definition.budget.max_attempts} attempts, ${data.definition.budget.max_actions} actions`} />
        <Fact label="Inputs" value={data.definition.input_slots.map((slot) => slot.artifact_type).join(", ") || "None"} />
        <Fact label="Outputs" value={data.definition.output_slots.map((slot) => slot.artifact_type).join(", ") || "None"} />
      </>}
      {data.execution && <>
        <Fact label="Execution" value={data.execution.aggregate_id} mono />
        <Fact label="Deadline" value={formatTime(data.execution.deadline)} />
        <Fact label="Evidence cutoff" value={formatTime(data.execution.bundle.evidence_cutoff)} />
        <Fact label="Approval" value={humanize(data.execution.approval_state)} />
        {data.execution.blocker_code && <Fact label="Blocker" value={`${humanize(data.execution.blocker_code)}: ${data.execution.blocker_message ?? "No detail recorded"}`} />}
        <Fact label="Bundle hash" value={data.execution.bundle.bundle_hash} mono />
        <Fact label="Audit trail" value={data.execution.receipts.map((receipt) => `${receipt.receipt_type}: ${receipt.receipt_hash}`).join("\n")} mono />
      </>}
      {data.hash && <Fact label="Exact hash" value={data.hash} mono />}
      {data.artifact?.approval_state === "pending" && <button className="approval-open" onClick={() => onApprove?.(data)}>Review exact action</button>}
    </dl>
  );
}

function ComparePanel({ snapshot, onClose }: { snapshot: CanvasSnapshot; onClose: () => void }) {
  const [leftIndex, setLeftIndex] = useState(0);
  const [rightIndex, setRightIndex] = useState(1);
  const differences = compareBundles(snapshot.executions[leftIndex].bundle, snapshot.executions[rightIndex].bundle);
  return (
    <aside className="detail-panel compare-panel" aria-label="Context Bundle comparison">
      <button className="icon-button" aria-label="Close comparison" onClick={onClose}><X size={18} /></button>
      <h2>Context Bundle comparison</h2>
      <div className="compare-selectors">
        <BundleSelect label="Earlier" value={leftIndex} snapshot={snapshot} onChange={setLeftIndex} />
        <BundleSelect label="Later" value={rightIndex} snapshot={snapshot} onChange={setRightIndex} />
      </div>
      {differences.length === 0 ? <p className="empty-copy">The exact Context editions are unchanged.</p> : differences.map((item) => (
        <section className="difference" key={`${item.requirementId ?? "legacy"}:${item.artifactType}`}>
		  <strong>{humanize(item.artifactType)}{item.requirementId ? ` · ${item.requirementId}` : ""}</strong><span>{humanize(item.state)}</span>
          {item.leftHash && <code>{item.leftHash}</code>}
          {item.rightHash && <code>{item.rightHash}</code>}
        </section>
      ))}
    </aside>
  );
}

function BundleSelect({ label, value, snapshot, onChange }: { label: string; value: number; snapshot: CanvasSnapshot; onChange: (value: number) => void }) {
  return <label><span>{label}</span><select value={value} onChange={(event) => onChange(Number(event.target.value))}>{snapshot.executions.map((execution, index) => <option key={execution.aggregate_id} value={index}>{execution.node_id}: {execution.bundle.id}</option>)}</select></label>;
}

function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div><dt>{label}</dt><dd className={mono ? "mono" : ""}>{value}</dd></div>;
}

function StateScreen({ title, detail, loading = false }: { title: string; detail: string; loading?: boolean }) {
  return <main className="state-screen" aria-live="polite">{loading ? <GearSix className="loading-icon" size={28} /> : <WarningCircle size={28} />}<h1>{title}</h1><p>{detail}</p></main>;
}

function entityIcon(kind: CanvasNodeData["entityKind"]) {
  if (kind === "context") return <Package size={18} />;
  if (kind === "artifact") return <FileText size={18} />;
  if (kind === "execution") return <GearSix size={18} />;
  return <LockKey size={18} />;
}

function statusIcon(status: string) {
  if (status === "completed") return <CheckCircle size={15} weight="fill" />;
  if (status === "blocked" || status === "terminal") return <WarningCircle size={15} weight="fill" />;
  return <Clock size={15} />;
}

function formatTime(value: string): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" }).format(new Date(value));
}
