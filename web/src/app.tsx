import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import {
  Background,
  Controls,
  Handle,
  Position,
  ReactFlow,
  type Edge,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  ArrowsLeftRight,
  Briefcase,
  ChartLineUp,
  CheckCircle,
  Clock,
  GitBranch,
  FileText,
  GearSix,
  ListChecks,
  LockKey,
  Package,
  PlayCircle,
  Robot,
  ShieldCheck,
  WarningCircle,
  Wrench,
  X,
} from "@phosphor-icons/react";
import type { CanvasPortfolioSnapshot, CanvasSnapshot, ChangeCaseCanvas, ContextPortElement, ControlPlaneSnapshot, EvidenceWindowReport, ExecutionElement, ProviderReadiness } from "./generated/agent-workflow.v1";
import { buildGraph, compareBundles, humanize, type CanvasGraphNode, type CanvasMode, type CanvasNodeData } from "./canvas-model";
import { ApprovalPanel, BuilderPanel } from "./builder";
import { installWebMCP } from "./webmcp";
import { Badge } from "./components/ui/badge";
import { Button } from "./components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./components/ui/card";
import { Separator } from "./components/ui/separator";
import { Skeleton } from "./components/ui/skeleton";
import "./styles.css";

type Selection = { kind: "node"; data: CanvasNodeData } | { kind: "port"; port: ContextPortElement };

interface ControlPlaneResponse {
  ok: boolean;
  data: ControlPlaneSnapshot;
}

interface EvidenceResponse {
  ok: boolean;
  data: EvidenceWindowReport;
}

type Page = "jobs" | "runs" | "approvals" | "changes" | "providers" | "evidence" | "audit";

export function App() {
  const [controlPlane, setControlPlane] = useState<ControlPlaneSnapshot>();
  const [evidence, setEvidence] = useState<EvidenceWindowReport | null>();
  const [evidenceError, setEvidenceError] = useState<string>();
  const [error, setError] = useState<string>();
  const [page, setPage] = useState<Page>("jobs");
  const [mode, setMode] = useState<CanvasMode>("runtime");
  const [selection, setSelection] = useState<Selection>();
  const [comparing, setComparing] = useState(false);
  const [building, setBuilding] = useState(false);
  const [approving, setApproving] = useState<CanvasNodeData>();
  const portfolio = controlPlane?.portfolios.find((item) => item.job.id === controlPlane.selected_job_id);
  const selectedCampaign = portfolio?.campaigns.find((item) => item.campaign_id === portfolio.selected_campaign_id);
  const snapshot = selectedCampaign?.canvas;
  const snapshotRef = useRef(snapshot);
  snapshotRef.current = snapshot;

  const setSnapshot = (_next: CanvasSnapshot) => {
    void loadControlPlane().then((next) => { setControlPlane(next); setError(undefined); }).catch((reason: unknown) => setError(reason instanceof Error ? reason.message : "Canonical control-plane refresh failed."));
  };

  const selectPage = (next: Page) => {
    setPage(next);
    if (next === "evidence") refreshEvidence();
  };

  const refreshEvidence = () => {
    void loadEvidenceReport().then((report) => { setEvidence(report); setEvidenceError(undefined); }).catch((reason: unknown) => { setEvidence(undefined); setEvidenceError(reason instanceof Error ? reason.message : "Evidence refresh failed."); });
  };

  useEffect(() => {
    void loadControlPlane().then(setControlPlane).catch((reason: unknown) => setError(reason instanceof Error ? reason.message : "Control plane data is unavailable."));
    refreshEvidence();
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
  }, [snapshot?.definition.job.id, snapshot?.definition.campaign.id]);

  const graph = useMemo(() => snapshot ? buildGraph(snapshot, mode, selectedCampaign?.state) : { nodes: [], edges: [] }, [snapshot, mode, selectedCampaign?.state]);
  const nodes = useMemo(() => graph.nodes.map((node) => ({
    ...node,
	data: {
	  ...node.data,
	  onSelect: () => setSelection({ kind: "node", data: node.data }),
	  onPortSelect: (port: ContextPortElement) => setSelection({ kind: "port", port }),
	},
  })), [graph.nodes]);

  if (error) return <StateScreen title="Control plane unavailable" detail={error} action={() => { setError(undefined); void loadControlPlane().then(setControlPlane).catch((reason: unknown) => setError(reason instanceof Error ? reason.message : "Canonical control-plane refresh failed.")); }} />;
  if (!snapshot || !portfolio || !controlPlane) return <LoadingControlPlane />;

  const canCompare = snapshot.executions.length > 1;
  return (
    <main className="control-plane-shell">
      <AppSidebar page={page} onPage={selectPage} />
      <section className="control-plane-main">
        <ControlPlaneHeader snapshot={snapshot} portfolio={portfolio} portfolios={controlPlane.portfolios} onJob={(jobID) => setControlPlane({ ...controlPlane, selected_job_id: jobID })} onCampaign={(campaignID) => setControlPlane({ ...controlPlane, portfolios: selectCampaign(controlPlane.portfolios, portfolio.job.id, campaignID) })} onBuild={() => setBuilding(true)} />
        {page === "jobs" && <JobsPage snapshot={snapshot} campaignState={selectedCampaign?.state} mode={mode} onMode={setMode} nodes={nodes} edges={graph.edges} canCompare={canCompare} onCompare={() => setComparing(true)} />}
        {page === "runs" && <RunsPage portfolio={portfolio} onSelect={(canvas, execution) => { setControlPlane({ ...controlPlane, portfolios: selectCampaign(controlPlane.portfolios, portfolio.job.id, canvas.definition.campaign.id) }); setSelection({ kind: "node", data: executionNodeData(canvas, execution) }); }} />}
        {page === "approvals" && <ApprovalsPage portfolio={portfolio} onReview={(canvas, execution) => { const output = execution.outputs.find((item) => item.approval_state === "pending"); if (output) { setControlPlane({ ...controlPlane, portfolios: selectCampaign(controlPlane.portfolios, portfolio.job.id, canvas.definition.campaign.id) }); setApproving({ ...executionNodeData(canvas, execution), artifact: output }); } }} />}
        {page === "changes" && <ChangeCasesPage cases={controlPlane.change_cases} />}
        {page === "providers" && <ProvidersPage providers={controlPlane.providers} />}
        {page === "evidence" && <EvidencePage report={evidence} error={evidenceError} onRetry={refreshEvidence} />}
        {page === "audit" && <AuditPage portfolio={portfolio} cases={controlPlane.change_cases} />}
      </section>

      {selection && <DetailPanel selection={selection} onClose={() => setSelection(undefined)} onApprove={(data) => { setSelection(undefined); setApproving(data); }} />}
      {comparing && canCompare && <ComparePanel snapshot={snapshot} onClose={() => setComparing(false)} />}
      {building && <BuilderPanel snapshot={snapshot} onClose={() => setBuilding(false)} onCanvas={setSnapshot} />}
      {approving?.artifact && <ApprovalPanel snapshot={snapshot} artifact={approving.artifact} onClose={() => setApproving(undefined)} onCanvas={(next) => { setSnapshot(next); setApproving(undefined); }} />}
    </main>
  );
}

async function loadControlPlane(): Promise<ControlPlaneSnapshot> {
  const response = await fetch("/v1/control-plane");
  if (!response.ok) throw new Error("The canonical control-plane API is unavailable.");
  const body = await response.json() as ControlPlaneResponse;
  if (!body.ok) throw new Error("Control-plane data was rejected by the Core.");
  return body.data;
}

async function loadEvidenceReport(): Promise<EvidenceWindowReport | null> {
  const response = await fetch("/v1/evidence-report");
  if (response.status === 404) return null;
  if (!response.ok) throw new Error("Evidence is unavailable.");
  const body = await response.json() as EvidenceResponse;
  if (!body.ok) throw new Error("Evidence was rejected by the Core.");
  return body.data;
}

function selectCampaign(portfolios: ControlPlaneSnapshot["portfolios"], jobID: string, campaignID: string): ControlPlaneSnapshot["portfolios"] {
  return portfolios.map((item) => item.job.id === jobID ? { ...item, selected_campaign_id: campaignID } : item) as ControlPlaneSnapshot["portfolios"];
}

const navigation: { id: Page; label: string; icon: typeof Briefcase }[] = [
  { id: "jobs", label: "Jobs", icon: Briefcase },
  { id: "runs", label: "Runs", icon: PlayCircle },
  { id: "approvals", label: "Approvals", icon: ShieldCheck },
  { id: "changes", label: "Change Cases", icon: GitBranch },
  { id: "providers", label: "Providers", icon: Robot },
  { id: "evidence", label: "Evidence", icon: ChartLineUp },
  { id: "audit", label: "Audit trail", icon: ListChecks },
];

function AppSidebar({ page, onPage }: { page: Page; onPage: (page: Page) => void }) {
  return <aside className="cp-sidebar">
    <div className="cp-brand"><span><Package size={18} weight="fill" /></span><div><strong>Agent Workflow</strong><small>Control plane</small></div></div>
    <nav aria-label="Control plane">
      {navigation.map((item) => <button key={item.id} className={page === item.id ? "is-active" : ""} onClick={() => onPage(item.id)}><item.icon size={18} /><span>{item.label}</span></button>)}
    </nav>
    <div className="cp-sidebar-foot"><span className="status-dot" /> Core connected</div>
  </aside>;
}

function ControlPlaneHeader({ snapshot, portfolio, portfolios, onJob, onCampaign, onBuild }: { snapshot: CanvasSnapshot; portfolio: CanvasPortfolioSnapshot; portfolios: ControlPlaneSnapshot["portfolios"]; onJob: (id: string) => void; onCampaign: (id: string) => void; onBuild: () => void }) {
  return <header className="cp-header">
    <div><p className="cp-kicker">{snapshot.definition.job.id}</p><h1>{snapshot.definition.job.intent.title}</h1></div>
    <div className="cp-header-actions">
      <label className="cp-campaign-select"><span>Job</span><select aria-label="Select Job" value={portfolio.job.id} onChange={(event) => onJob(event.target.value)}>{portfolios.map((item) => <option key={item.job.id} value={item.job.id}>{item.job.intent.title}</option>)}</select></label>
      <label className="cp-campaign-select"><span>Campaign</span><select aria-label="Select Campaign" value={snapshot.definition.campaign.id} onChange={(event) => onCampaign(event.target.value)}>{portfolio.campaigns.map((item) => <option key={item.campaign_id} value={item.campaign_id}>{item.canvas.definition.campaign.intent.title}</option>)}</select></label>
      <Button variant="outline" aria-label="Build Workflow" onClick={onBuild}><Wrench size={17} />Workflow Studio</Button>
    </div>
  </header>;
}

function JobsPage({ snapshot, campaignState, mode, onMode, nodes, edges, canCompare, onCompare }: { snapshot: CanvasSnapshot; campaignState?: CanvasPortfolioSnapshot["campaigns"][number]["state"]; mode: CanvasMode; onMode: (mode: CanvasMode) => void; nodes: CanvasGraphNode[]; edges: Edge[]; canCompare: boolean; onCompare: () => void }) {
  return <div className="cp-page cp-jobs-page">
    <section className="cp-summary-grid">
      <SummaryCard label="Campaign" value={snapshot.definition.campaign.intent.title} detail={snapshot.definition.campaign.archetype} icon={ChartLineUp} />
      <SummaryCard label="Workflows" value={String(snapshot.definition.workflows.length)} detail={`${snapshot.definition.workflows.reduce((sum, item) => sum + item.nodes.length, 0)} total nodes`} icon={GitBranch} />
      <SummaryCard label="Active runs" value={String(snapshot.executions.filter((item) => item.status === "running").length)} detail={`${snapshot.executions.length} recorded`} icon={PlayCircle} />
      <SummaryCard label="Next safe action" value={humanize(snapshot.next_safe_action.kind)} detail={snapshot.next_safe_action.reason} icon={ShieldCheck} />
    </section>
    <Card className="cp-canvas-card">
      <CardHeader className="cp-canvas-header"><div><CardTitle>Workflow canvas</CardTitle><CardDescription>Definition, Context Packs, receipts and live execution state.</CardDescription></div><div className="cp-canvas-tools"><div className="view-switch" aria-label="Canvas view"><button className={mode === "definition" ? "is-active" : ""} onClick={() => onMode("definition")}>Definition</button><button className={mode === "runtime" ? "is-active" : ""} onClick={() => onMode("runtime")}>Runtime</button></div><Button variant="outline" size="sm" disabled={!canCompare} title={canCompare ? "Compare Context Bundles" : "Two executions are required"} onClick={onCompare}><ArrowsLeftRight size={15} />Compare Context</Button></div></CardHeader>
      <CardContent className="cp-canvas-content"><section className="canvas-stage" aria-label={`${mode} workflow Canvas`}><ReactFlow nodes={nodes} edges={edges} nodeTypes={canvasNodeTypes} nodesDraggable={false} nodesConnectable={false} fitView fitViewOptions={{ padding: 0.16 }} minZoom={0.35} maxZoom={1.5}><Background gap={24} size={1} /><Controls showInteractive={false} /></ReactFlow></section></CardContent>
    </Card>
    <div className="cp-safe-action"><ShieldCheck size={17} /><div><strong>{humanize(snapshot.next_safe_action.kind)}</strong><span>{snapshot.next_safe_action.reason}</span></div><Badge variant={statusVariant(campaignState ?? snapshot.definition.campaign_state)}>{humanize(campaignState ?? snapshot.definition.campaign_state)}</Badge></div>
  </div>;
}

function SummaryCard({ label, value, detail, icon: Icon }: { label: string; value: string; detail: string; icon: typeof Briefcase }) {
  return <Card><CardContent className="cp-summary-card"><span className="cp-summary-icon"><Icon size={18} /></span><div><p>{label}</p><strong>{value}</strong><small>{detail}</small></div></CardContent></Card>;
}

function RunsPage({ portfolio, onSelect }: { portfolio: CanvasPortfolioSnapshot; onSelect: (snapshot: CanvasSnapshot, execution: ExecutionElement) => void }) {
  const runs = portfolio.campaigns.flatMap((campaign) => campaign.canvas.executions.map((execution) => ({ campaign, execution })));
  return <ListPage title="Runs" description="Every admitted execution, its Context Bundle, deadline and canonical receipt frontier." empty="No executions have been recorded yet.">
    {runs.map(({ campaign, execution }) => <button className="cp-list-row" key={execution.aggregate_id} onClick={() => onSelect(campaign.canvas, execution)}><span className="cp-list-icon"><PlayCircle size={18} /></span><div className="cp-list-main"><strong>{humanize(execution.node_id)}</strong><span>{campaign.canvas.definition.campaign.intent.title} · {execution.bundle.workflow_ref}</span></div><div className="cp-list-meta"><Badge variant={statusVariant(execution.status)}>{humanize(execution.status)}</Badge><time>{formatTime(execution.deadline)}</time></div></button>)}
  </ListPage>;
}

function ApprovalsPage({ portfolio, onReview }: { portfolio: CanvasPortfolioSnapshot; onReview: (snapshot: CanvasSnapshot, execution: ExecutionElement) => void }) {
  const approvals = portfolio.campaigns.flatMap(({ canvas }) => canvas.executions.filter((execution) => execution.outputs.some((output) => output.approval_state === "pending")).map((execution) => ({ canvas, execution })));
  return <ListPage title="Approvals" description="Human decisions are bound to exact action and evidence hashes." empty="No action is waiting for human approval.">
    {approvals.map(({ canvas, execution }) => <button className="cp-list-row" key={execution.aggregate_id} onClick={() => onReview(canvas, execution)}><span className="cp-list-icon attention"><ShieldCheck size={18} /></span><div className="cp-list-main"><strong>{execution.outputs.find((item) => item.approval_state === "pending")?.artifact_type}</strong><span>{canvas.definition.campaign.intent.title} · {humanize(execution.node_id)}</span></div><div className="cp-list-meta"><Badge>Needs review</Badge><span>Open decision</span></div></button>)}
  </ListPage>;
}

function ChangeCasesPage({ cases }: { cases: ChangeCaseCanvas[] }) {
  return <ListPage title="Change Cases" description="Conflicting proposals, resolution evidence, mutation leases and readback." empty="No resource conflict has produced a Change Case.">
    {cases.map((item) => <Card key={item.state.id} className="cp-case-card"><CardHeader><div className="cp-card-title-row"><CardTitle>{item.state.resource.resource_type} · {item.state.resource.resource_id}</CardTitle><Badge variant={statusVariant(item.state.status)}>{humanize(item.state.status)}</Badge></div><CardDescription>{item.state.id}</CardDescription></CardHeader><CardContent><div className="cp-case-facts"><span>{item.state.proposals.length} proposals</span><span>{item.state.conflicts?.items.length ?? 0} conflicts</span><span>{item.receipts.length} receipts</span><span>generation {item.state.resource.generation}</span></div>{item.state.blocker_code && <p className="cp-warning"><WarningCircle size={16} />{humanize(item.state.blocker_code)}</p>}</CardContent></Card>)}
  </ListPage>;
}

function ProvidersPage({ providers }: { providers: ProviderReadiness[] }) {
  return <ListPage title="Providers" description="Bundled runner adapters and the exact local prerequisites required to execute them." empty="Provider readiness is unavailable in the static demo.">
    {providers.length > 0 ? <div className="cp-provider-grid">{providers.map((provider) => <Card key={provider.descriptor.id}><CardHeader><div className="cp-card-title-row"><span className="cp-provider-icon"><Robot size={18} /></span><Badge variant={provider.ready ? "default" : "secondary"}>{provider.code === "profile_required" ? "Profile required" : provider.ready ? "Ready" : "Unavailable"}</Badge></div><CardTitle>{provider.descriptor.display_name}</CardTitle><CardDescription>{provider.descriptor.id} · protocol v{provider.descriptor.protocol_version}</CardDescription></CardHeader><CardContent><div className="cp-tags">{provider.descriptor.capabilities.map((item) => <Badge key={item} variant="outline">{humanize(item)}</Badge>)}</div>{provider.missing.length > 0 ? <div className="cp-provider-missing"><strong>Missing</strong>{provider.missing.map((item) => <code key={item}>{item}</code>)}</div> : provider.code === "profile_required" ? <p className="cp-provider-ready"><ShieldCheck size={16} />Select an admitted Executor Profile to assess readiness.</p> : <p className="cp-provider-ready"><CheckCircle size={16} weight="fill" />All declared requirements are available.</p>}</CardContent></Card>)}</div> : []}
  </ListPage>;
}

export function EvidencePage({ report, error, onRetry }: { report?: EvidenceWindowReport | null; error?: string; onRetry?: () => void }) {
  if (error) return <div className="cp-page"><div className="cp-page-heading"><h2>Evidence window</h2><p>Proof derived from the same canonical receipt ledger.</p></div><Separator /><Card className="cp-empty"><CardContent><WarningCircle size={24} /><strong>Evidence refresh failed.</strong><span>{error}</span>{onRetry && <Button variant="outline" onClick={onRetry}>Retry evidence readback</Button>}</CardContent></Card></div>;
  if (!report) return <div className="cp-page"><div className="cp-page-heading"><h2>Evidence window</h2><p>Proof derived from the same canonical receipt ledger.</p></div><Separator /><EmptyState title="No evidence receipts have been recorded." /></div>;
  const roleSummary = `${report.invoked_role_ids.length} of ${report.available_role_ids.length} roles evidenced`;
  return <div className="cp-page">
    <div className="cp-page-heading"><h2>Evidence window</h2><p>{roleSummary} · {formatTime(report.window.started_at)} to {formatTime(report.window.ended_at)}</p></div>
    <Separator />
    <section className="cp-summary-grid">
      <SummaryCard label="Agent invocations" value={String(report.counts.agent_invocations)} detail={`${report.counts.context_refreshes} context refreshes`} icon={Robot} />
      <SummaryCard label="Effects" value={String(report.counts.effects)} detail={`${report.counts.readbacks} verified readbacks`} icon={CheckCircle} />
      <SummaryCard label="Outcomes" value={String(report.counts.outcomes)} detail={`${report.counts.approvals} approvals`} icon={ShieldCheck} />
      <SummaryCard label="Audit evidence" value={String(report.counts.receipts)} detail={`${report.counts.replays} replay bundles`} icon={ListChecks} />
    </section>
    <Card><CardHeader><CardTitle>Agent roles</CardTitle><CardDescription>{roleSummary}. Availability never implies execution.</CardDescription></CardHeader><CardContent><div className="cp-tags">{report.available_role_ids.map((role) => <Badge key={role} variant={report.invoked_role_ids.includes(role) ? "default" : "outline"}>{humanize(role)}</Badge>)}</div></CardContent></Card>
  </div>;
}

function AuditPage({ portfolio, cases }: { portfolio: CanvasPortfolioSnapshot; cases: ChangeCaseCanvas[] }) {
  const rows = portfolio.campaigns.flatMap(({ canvas }) => [...canvas.admission_replays ?? [], ...canvas.approval_replays ?? [], ...canvas.replays].flatMap((replay) => replay.receipts.map((receipt) => ({ ...receipt, campaign: canvas.definition.campaign.intent.title }))));
  const changes = cases.flatMap((item) => item.receipts.map((receipt) => ({ ...receipt, campaign: item.state.resource.resource_id, aggregate_id: item.state.id, aggregate_version: 0, previous_receipt_hash: null, input_hashes: [], output_hashes: [], kind: "receipt" as const, schema_version: 1 })));
  return <ListPage title="Audit trail" description="Append-only canonical receipts. Technical hashes stay available without dominating normal work." empty="No canonical receipts have been recorded.">
    {[...rows, ...changes].sort((a, b) => b.occurred_at.localeCompare(a.occurred_at)).map((receipt) => <div className="cp-audit-row" key={`${receipt.aggregate_id}:${receipt.id}:${receipt.receipt_hash}`}><span className="cp-audit-line" /><div><strong>{humanize(receipt.receipt_type)}</strong><span>{receipt.campaign} · {formatTime(receipt.occurred_at)}</span><details><summary>Receipt evidence</summary><code>{receipt.receipt_hash}</code></details></div></div>)}
  </ListPage>;
}

function ListPage({ title, description, empty, children }: { title: string; description: string; empty: string; children: React.ReactNode }) {
  const hasChildren = Array.isArray(children) ? children.length > 0 : Boolean(children);
  return <div className="cp-page"><div className="cp-page-heading"><h2>{title}</h2><p>{description}</p></div><Separator />{hasChildren ? <div className="cp-list">{children}</div> : <EmptyState title={empty} />}</div>;
}

function EmptyState({ title }: { title: string }) { return <Card className="cp-empty"><CardContent><Package size={24} /><strong>{title}</strong><span>The Core will project this view when canonical records exist.</span></CardContent></Card>; }

function LoadingControlPlane() { return <main className="cp-loading"><Skeleton className="h-screen w-56 rounded-none" /><div><Skeleton className="h-10 w-72" /><div className="grid grid-cols-4 gap-4">{[0, 1, 2, 3].map((item) => <Skeleton key={item} className="h-28" />)}</div><Skeleton className="h-[60vh]" /></div></main>; }

function executionNodeData(snapshot: CanvasSnapshot, execution: ExecutionElement): CanvasNodeData {
  const definition = snapshot.definition.workflows.flatMap((workflow) => workflow.nodes).find((node) => node.id === execution.node_id);
  return { entityKind: "execution", title: humanize(execution.node_id), subtitle: execution.bundle.workflow_ref, status: execution.status, execution, definition, contextPorts: execution.context_ports };
}

function statusVariant(status: string): "default" | "secondary" | "outline" | "destructive" {
  if (["completed", "ready", "approved", "running"].includes(status)) return "default";
  if (["blocked", "terminal", "rejected", "conflicted"].includes(status)) return "destructive";
  return "secondary";
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

function StateScreen({ title, detail, loading = false, action }: { title: string; detail: string; loading?: boolean; action?: () => void }) {
  return <main className="state-screen" aria-live="polite">{loading ? <GearSix className="loading-icon" size={28} /> : <WarningCircle size={28} />}<h1>{title}</h1><p>{detail}</p>{action && <Button variant="outline" onClick={action}>Retry canonical readback</Button>}</main>;
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
