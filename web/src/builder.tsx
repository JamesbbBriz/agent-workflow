import { useEffect, useMemo, useState } from "react";
import { FloppyDisk, GitMerge, Plus, ShieldCheck, X } from "@phosphor-icons/react";
import type { ActionArtifact, ApprovalBrief, ApprovalPreview, AuthoringCatalog, CanvasSnapshot, ReplayBundleReceipt, WorkflowAdmissionPreview, WorkflowDefinitionElement, WorkflowLintReport } from "./generated/agent-workflow.v1";
import { humanize } from "./canvas-model";

interface Envelope<T> { ok: boolean; data?: T; error?: string }
type BuilderDraft = { job: CanvasSnapshot["definition"]["job"]; campaign: CanvasSnapshot["definition"]["campaign"]; workflow: WorkflowDefinitionElement };

export function BuilderPanel({ snapshot, onClose, onCanvas }: { snapshot: CanvasSnapshot; onClose: () => void; onCanvas: (snapshot: CanvasSnapshot) => void }) {
  const initial = snapshot.definition.workflows[0];
  const storageKey = `agent-workflow:draft:${initial.id}`;
  const [catalog, setCatalog] = useState<AuthoringCatalog>();
  const [draft, setDraft] = useState<BuilderDraft>(() => loadDraft(storageKey, { job: snapshot.definition.job, campaign: snapshot.definition.campaign, workflow: initial }));
  const [actor, setActor] = useState("operator@example.com");
  const [lint, setLint] = useState<WorkflowLintReport>();
  const [preview, setPreview] = useState<WorkflowAdmissionPreview>();
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  useEffect(() => { request<AuthoringCatalog>("/v1/catalog").then(setCatalog).catch((reason) => setError(reason.message)); }, []);
  const nodeKinds = useMemo(() => [...new Set(catalog?.executors.map((item) => item.node_kind) ?? [])], [catalog]);

  const update = (next: BuilderDraft) => {
    setDraft(next); setPreview(undefined); setLint(undefined); setError(undefined);
    localStorage.setItem(storageKey, JSON.stringify(next));
  };
  const updateWorkflow = (workflow: WorkflowDefinitionElement) => update({ ...draft, workflow });
  const addNode = () => {
    const executor = catalog?.executors[0];
    const outputSchema = catalog?.output_schemas[0];
    if (!executor || !outputSchema) return;
    const nodes = [...draft.workflow.nodes, {
      id: `node-${draft.workflow.nodes.length + 1}`, kind: executor.node_kind, executor: executor.ref,
      depends_on: [], context: [], capabilities: [], input_slots: [], blocker_codes: [],
      output_slots: [{ id: "output", artifact_type: "recommendation", artifact_kind: "action_artifact" as const, content_schema: outputSchema, min_items: 0, max_items: 1 }],
      budget: { max_attempts: 1, max_actions: 0, max_candidates: 1 },
    }] as WorkflowDefinitionElement["nodes"];
    updateWorkflow({ ...draft.workflow, nodes });
  };
  const compile = async () => {
    setBusy(true); setError(undefined);
    try {
      const initialRef = `${initial.id}@${initial.version}`;
      const workflowRef = `${draft.workflow.id}@${draft.workflow.version}`;
      const campaign = { ...draft.campaign, job_id: draft.job.id, workflow_plan: draft.campaign.workflow_plan.map((ref) => ref === initialRef || ref.startsWith(`${draft.workflow.id}@`) ? workflowRef : ref) };
      const result = await request<{ preview: WorkflowAdmissionPreview; lint: WorkflowLintReport }>("/v1/workflows/preview", { actor, job: draft.job, campaign, workflow: draft.workflow });
      setPreview(result.preview); setLint(result.lint);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Preview failed"); }
    finally { setBusy(false); }
  };
  const confirm = async () => {
    if (!preview) return;
    setBusy(true); setError(undefined);
    try {
      const result = await request<{ canvas: CanvasSnapshot }>("/v1/workflows/confirm", { actor, preview });
      onCanvas(mergeAdmissionReadback(snapshot, result.canvas)); localStorage.removeItem(storageKey); onClose();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Confirmation failed"); }
    finally { setBusy(false); }
  };

  return (
    <aside className="builder-panel" aria-label="Workflow Builder">
      <header className="builder-header">
        <div><span className="eyebrow">Local draft</span><h2>Build an immutable Workflow</h2><p>Drafts stay in this browser. The Go Core alone can admit a version.</p></div>
        <button className="icon-button" aria-label="Close Builder" onClick={onClose}><X size={18} /></button>
      </header>
      <div className="builder-body">
        <section className="builder-section">
          <h3>Job and Campaign boundary</h3>
          <div className="form-grid">
            <Field label="Job ID"><input value={draft.job.id} onChange={(event) => update({ ...draft, job: { ...draft.job, id: event.target.value } })} /></Field>
            <Field label="Job title"><input value={draft.job.intent.title} onChange={(event) => update({ ...draft, job: { ...draft.job, intent: { ...draft.job.intent, title: event.target.value } } })} /></Field>
            <Field label="Campaign ID"><input value={draft.campaign.id} onChange={(event) => update({ ...draft, campaign: { ...draft.campaign, id: event.target.value } })} /></Field>
            <Field label="Campaign title"><input value={draft.campaign.intent.title} onChange={(event) => update({ ...draft, campaign: { ...draft.campaign, intent: { ...draft.campaign.intent, title: event.target.value } } })} /></Field>
            <Field label="Job objective" wide><textarea value={draft.job.intent.objective} onChange={(event) => update({ ...draft, job: { ...draft.job, intent: { ...draft.job.intent, objective: event.target.value } } })} /></Field>
            <Field label="Campaign objective" wide><textarea value={draft.campaign.intent.objective} onChange={(event) => update({ ...draft, campaign: { ...draft.campaign, intent: { ...draft.campaign.intent, objective: event.target.value } } })} /></Field>
            <Field label="Job attempts"><input type="number" min={1} value={draft.job.budget.max_attempts} onChange={(event) => update({ ...draft, job: { ...draft.job, budget: { ...draft.job.budget, max_attempts: Number(event.target.value) } } })} /></Field>
            <Field label="Job actions"><input type="number" min={0} value={draft.job.budget.max_actions} onChange={(event) => update({ ...draft, job: { ...draft.job, budget: { ...draft.job.budget, max_actions: Number(event.target.value) } } })} /></Field>
          </div>
        </section>
        <section className="builder-section">
          <h3>Intent and identity</h3>
          <div className="form-grid">
            <Field label="Workflow ID"><input value={draft.workflow.id} onChange={(event) => updateWorkflow({ ...draft.workflow, id: event.target.value })} /></Field>
            <Field label="Version"><input type="number" min={1} value={draft.workflow.version} onChange={(event) => updateWorkflow({ ...draft.workflow, version: Number(event.target.value) })} /></Field>
            <Field label="Title"><input value={draft.workflow.intent.title} onChange={(event) => updateWorkflow({ ...draft.workflow, intent: { ...draft.workflow.intent, title: event.target.value } })} /></Field>
            <Field label="Confirming actor"><input value={actor} onChange={(event) => { setActor(event.target.value); setPreview(undefined); }} /></Field>
            <Field label="Objective" wide><textarea value={draft.workflow.intent.objective} onChange={(event) => updateWorkflow({ ...draft.workflow, intent: { ...draft.workflow.intent, objective: event.target.value } })} /></Field>
          </div>
        </section>

        <section className="builder-section">
          <h3>Node graph <button type="button" disabled={!catalog} onClick={addNode}><Plus size={15} /> Add Node</button></h3>
          <div className="builder-nodes">
            {draft.workflow.nodes.map((node, index) => {
              const executors = catalog?.executors.filter((item) => item.node_kind === node.kind) ?? [];
              return <article className="builder-node" key={node.id}>
                <div className="builder-node-title"><span>{index + 1}</span><strong>{humanize(node.id)}</strong><code>{node.executor}</code></div>
                <div className="form-grid compact">
                  <Field label="Node ID"><input value={node.id} onChange={(event) => updateNode(draft.workflow, index, { id: event.target.value }, updateWorkflow)} /></Field>
                  <Field label="Kind"><select value={node.kind} onChange={(event) => updateNode(draft.workflow, index, { kind: event.target.value as typeof node.kind }, updateWorkflow)}>{nodeKinds.map((kind) => <option key={kind}>{kind}</option>)}</select></Field>
                  <Field label="Executor"><select value={node.executor} onChange={(event) => updateNode(draft.workflow, index, { executor: event.target.value }, updateWorkflow)}>{executors.map((item) => <option key={item.ref}>{item.ref}</option>)}</select></Field>
                  <Field label="Depends on"><select multiple value={node.depends_on} onChange={(event) => updateNode(draft.workflow, index, { depends_on: [...event.target.selectedOptions].map((item) => item.value) }, updateWorkflow)}>{draft.workflow.nodes.filter((_, candidate) => candidate !== index).map((candidate) => <option key={candidate.id}>{candidate.id}</option>)}</select></Field>
                  <Field label="Approval policy"><select disabled={node.kind !== "approval"} value={node.approval_policy ?? ""} onChange={(event) => updateNode(draft.workflow, index, { approval_policy: event.target.value || undefined }, updateWorkflow)}><option value="">None</option>{catalog?.approval_policies.map((policy) => <option key={policy}>{policy}</option>)}</select></Field>
                  <Field label="Capabilities" wide><ChoiceList values={catalog?.capabilities.map((item) => item.name) ?? []} selected={node.capabilities} onChange={(capabilities) => updateNode(draft.workflow, index, { capabilities }, updateWorkflow)} /></Field>
                  <Field label="Blockers" wide><ChoiceList values={catalog?.blockers ?? []} selected={node.blocker_codes ?? []} onChange={(blocker_codes) => updateNode(draft.workflow, index, { blocker_codes }, updateWorkflow)} /></Field>
                  <Field label="Context producers" wide>
                    {node.context.map((context, contextIndex) => <select key={context.id} value={context.selector} onChange={(event) => { const producer = catalog?.producers.find((item) => item.selector === event.target.value); if (!producer) return; const contexts = [...node.context]; contexts[contextIndex] = { ...context, selector: producer.selector, pack_type: producer.pack_type, schema_version: producer.schema_version }; updateNode(draft.workflow, index, { context: contexts }, updateWorkflow); }}>{catalog?.producers.map((producer) => <option key={`${producer.selector}:${producer.pack_type}`}>{producer.selector}</option>)}</select>)}
                    {node.context.length === 0 && <span className="muted">Uses expanded Workflow defaults</span>}
                    <button type="button" disabled={!catalog?.producers[0]} onClick={() => { const producer = catalog?.producers[0]; if (!producer) return; const contexts = [...node.context, { id: `context-${node.context.length + 1}`, selector: producer.selector, pack_type: producer.pack_type, schema_version: producer.schema_version, required: true, allow_partial: false }] as typeof node.context; updateNode(draft.workflow, index, { context: contexts }, updateWorkflow); }}><Plus size={14} /> Add Context</button>
                  </Field>
                  <Field label="Output schemas" wide>{node.output_slots.map((slot, slotIndex) => <select key={slot.id} value={slot.content_schema ?? ""} onChange={(event) => { const slots = [...node.output_slots]; slots[slotIndex] = { ...slot, content_schema: event.target.value }; updateNode(draft.workflow, index, { output_slots: slots }, updateWorkflow); }}>{catalog?.output_schemas.map((schema) => <option key={schema}>{schema}</option>)}</select>)}</Field>
                  <Field label="Attempts"><input type="number" min={1} value={node.budget.max_attempts} onChange={(event) => updateNode(draft.workflow, index, { budget: { ...node.budget, max_attempts: Number(event.target.value) } }, updateWorkflow)} /></Field>
                  <Field label="Actions"><input type="number" min={0} value={node.budget.max_actions} onChange={(event) => updateNode(draft.workflow, index, { budget: { ...node.budget, max_actions: Number(event.target.value) } }, updateWorkflow)} /></Field>
                </div>
              </article>;
            })}
          </div>
        </section>

        <section className="builder-section preview-section">
          <h3>Core preview</h3>
          {error && <div className="builder-error" role="alert">{error}</div>}
          {lint && !lint.valid && <ul className="lint-list">{lint.issues.map((issue) => <li key={`${issue.code}:${issue.path}`}><strong>{humanize(issue.code)}</strong><span>{issue.path} — {issue.message}</span></li>)}</ul>}
          {preview ? <div className="preview-receipt">
            <ShieldCheck size={24} /><div><strong>Ready to admit version {preview.workflow.version}</strong><p>{preview.expanded_nodes.length} fully expanded Nodes</p></div>
            <div className="builder-nodes">{preview.expanded_nodes.map(({ definition, context_authorities }) => <article className="builder-node" key={definition.id}>
              <div className="builder-node-title"><strong>{humanize(definition.id)}</strong><code>{definition.kind} · {definition.executor}</code></div>
              <p>Depends on: {definition.depends_on.join(", ") || "start"}</p>
              <p>Context: {definition.context.map((item) => `${item.selector}/${item.pack_type}@${item.schema_version} · ${item.required ? "required" : "optional"}`).join("; ") || "none"}</p>
              <p>Authorities: {context_authorities.join(", ") || "unresolved"}</p>
              <p>Capabilities: {definition.capabilities.join(", ") || "none"}</p>
              <p>Budget: {definition.budget.max_attempts} attempts · {definition.budget.max_actions} actions · {definition.budget.max_candidates} candidates{definition.deadline_seconds ? ` · ${definition.deadline_seconds}s deadline` : ""}</p>
              <p>Outputs: {definition.output_slots.map((slot) => `${slot.id}:${slot.content_schema ?? slot.artifact_type}`).join(", ") || "none"}</p>
            </article>)}</div>
            <p><strong>Completion:</strong> {preview.workflow.completion.join(" · ")}</p>
            <p><strong>No action when:</strong> {preview.workflow.intent.no_action_when.join(" · ")}</p>
            <code>{preview.job_hash}</code><code>{preview.campaign_hash}</code><code>{preview.definition_hash}</code><code>{preview.compile_hash}</code><code>{preview.catalog_hash}</code><code>{preview.preview_hash}</code><code>{preview.commit_token}</code>
          </div> : <p className="muted">Compile to see exact expanded contracts, authority classes, budgets, completion rules, hashes, and the revision-bound token.</p>}
        </section>
      </div>
      <footer className="builder-actions">
        <span><FloppyDisk size={17} /> Draft saved locally</span>
        <button disabled={busy || !catalog} onClick={compile}><GitMerge size={17} /> {busy ? "Checking…" : "Compile & preview"}</button>
        <button className="primary" disabled={busy || !preview} onClick={confirm}><ShieldCheck size={17} /> Confirm admission</button>
      </footer>
    </aside>
  );
}

export function mergeAdmissionReadback(current: CanvasSnapshot, admitted: CanvasSnapshot): CanvasSnapshot {
  if (current.definition.job.id !== admitted.definition.job.id || current.definition.campaign.id !== admitted.definition.campaign.id) return admitted;
  if (JSON.stringify(current.definition.job) !== JSON.stringify(admitted.definition.job) || JSON.stringify(current.definition.campaign) !== JSON.stringify(admitted.definition.campaign)) return admitted;
  const admittedWorkflow = admitted.definition.workflows[0];
  const candidates = admittedWorkflow
    ? (current.definition.workflows.some((workflow) => workflow.id === admittedWorkflow.id)
      ? current.definition.workflows.map((workflow) => workflow.id === admittedWorkflow.id ? admittedWorkflow : workflow)
      : [...current.definition.workflows, admittedWorkflow]) as typeof current.definition.workflows
    : current.definition.workflows;
  const planned = new Set(admitted.definition.campaign.workflow_plan);
  const workflows = candidates.filter((workflow) => planned.has(`${workflow.id}@${workflow.version}`)) as typeof current.definition.workflows;
  const admissionReplays = [...(current.admission_replays ?? [])];
  for (const replay of admitted.admission_replays ?? []) {
    if (!admissionReplays.some((item) => item.bundle_hash === replay.bundle_hash)) admissionReplays.push(replay);
  }
  return {
    ...current,
    generated_at: admitted.generated_at > current.generated_at ? admitted.generated_at : current.generated_at,
    definition: { ...current.definition, job: admitted.definition.job, campaign: admitted.definition.campaign, workflows, workflow_states: { ...current.definition.workflow_states, ...admitted.definition.workflow_states } },
    admission_replays: admissionReplays,
  };
}

export function ApprovalPanel({ snapshot, artifact, onClose, onCanvas }: { snapshot: CanvasSnapshot; artifact: ActionArtifact; onClose: () => void; onCanvas: (snapshot: CanvasSnapshot) => void }) {
  const source = snapshot.replays.find((replay) => replay.aggregate_id === snapshot.executions.find((execution) => execution.outputs.some((item) => item.id === artifact.id))?.aggregate_id);
  const result = source?.receipts.find((receipt) => receipt.receipt_type === "result");
  const [actor, setActor] = useState("reviewer@example.com");
  const [recommendation, setRecommendation] = useState("Approve the exact reviewed action.");
  const [risk, setRisk] = useState("This action changes an external or canonical system.");
  const [preview, setPreview] = useState<ApprovalPreview>();
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  const brief = source && result ? approvalBrief(artifact, result, recommendation, risk) : undefined;
  const prepare = async () => {
    if (!brief || !source) return;
    setBusy(true); setError(undefined);
    try { setPreview(await request<ApprovalPreview>("/v1/approvals/preview", { actor, brief, source_aggregate_id: source.aggregate_id })); }
    catch (reason) { setError(reason instanceof Error ? reason.message : "Approval preview failed"); }
    finally { setBusy(false); }
  };
  const decide = async (option_id: "approve" | "reject") => {
    if (!preview) return;
    setBusy(true); setError(undefined);
    try { const result = await request<{ canvas: CanvasSnapshot }>("/v1/approvals/confirm", { actor, option_id, preview }); onCanvas(result.canvas); }
    catch (reason) { setError(reason instanceof Error ? reason.message : "Approval failed"); }
    finally { setBusy(false); }
  };

  return <aside className="approval-panel" aria-label="Human approval">
    <header className="builder-header"><div><span className="eyebrow">Human decision</span><h2>{humanize(artifact.artifact_type)}</h2><p>The receipt binds this exact evidence, recommendation, option, actor, and action hash.</p></div><button className="icon-button" aria-label="Close approval" onClick={onClose}><X size={18} /></button></header>
    <div className="approval-body">
      {!brief && <div className="builder-error">The canonical source Replay is unavailable.</div>}
      {brief && <>
        <section><h3>Evidence</h3><code>{result?.receipt_hash}</code><code>{artifact.content_sha256}</code></section>
        <section><h3>Recommendation</h3><textarea value={recommendation} onChange={(event) => { setRecommendation(event.target.value); setPreview(undefined); }} /><label>Risk<textarea value={risk} onChange={(event) => { setRisk(event.target.value); setPreview(undefined); }} /></label></section>
        <section><h3>Options and trade-offs</h3>{brief.options.map((option) => <article className="approval-option" key={option.id}><strong>{option.label}</strong><span>{option.tradeoffs.join(" · ")}</span></article>)}</section>
        <section><h3>Exact proposed action</h3><code>{JSON.stringify(artifact.content, null, 2)}</code></section>
        <label className="approval-actor">Actor<input value={actor} onChange={(event) => { setActor(event.target.value); setPreview(undefined); }} /></label>
        {error && <div className="builder-error" role="alert">{error}</div>}
        {preview && <div className="preview-receipt"><ShieldCheck size={24} /><div><strong>Decision token ready</strong><p>Any change makes this token stale.</p></div><code>{preview.commit_token}</code></div>}
      </>}
    </div>
    <footer className="builder-actions"><button disabled={!brief || busy} onClick={prepare}>Preview decision</button><button disabled={!preview || busy} onClick={() => decide("reject")}>Reject</button><button className="primary" disabled={!preview || busy} onClick={() => decide("approve")}>Approve exact action</button></footer>
  </aside>;
}

function approvalBrief(action: ActionArtifact, result: ReplayBundleReceipt, recommendation: string, risk: string): ApprovalBrief {
  return {
    kind: "approval_brief", schema_version: 1, id: "", title: `Approve ${humanize(action.artifact_type)}?`, action,
    evidence: [{ id: result.id, kind: "receipt", artifact_type: "result", schema_version: 1, sha256: result.receipt_hash, media_type: "application/json" }],
    options: [
      { id: "approve", label: "Approve exact action", decision: "approve", tradeoffs: ["Executes only the hash-bound proposal"] },
      { id: "reject", label: "Reject", decision: "reject", tradeoffs: ["Leaves the target unchanged"] },
    ],
    recommended_option_id: "approve", recommendation, risks: [risk],
  };
}

function Field({ label, wide = false, children }: { label: string; wide?: boolean; children: React.ReactNode }) { return <label className={wide ? "wide" : ""}><span>{label}</span>{children}</label>; }

function ChoiceList({ values, selected, onChange }: { values: string[]; selected: string[]; onChange: (values: string[]) => void }) {
  return <div className="choice-list">{values.map((value) => <label key={value}><input type="checkbox" checked={selected.includes(value)} onChange={(event) => onChange(event.target.checked ? [...selected, value] : selected.filter((item) => item !== value))} /><span>{humanize(value)}</span></label>)}</div>;
}

function updateNode(draft: WorkflowDefinitionElement, index: number, patch: Partial<WorkflowDefinitionElement["nodes"][number]>, update: (draft: WorkflowDefinitionElement) => void) {
  const nodes = [...draft.nodes] as WorkflowDefinitionElement["nodes"]; nodes[index] = { ...nodes[index], ...patch }; update({ ...draft, nodes });
}

function loadDraft(key: string, fallback: BuilderDraft): BuilderDraft {
  try {
    const stored = localStorage.getItem(key);
    if (!stored) return structuredClone(fallback);
    const parsed = JSON.parse(stored) as BuilderDraft | WorkflowDefinitionElement;
    return "workflow" in parsed ? parsed : { ...structuredClone(fallback), workflow: parsed };
  }
  catch { return structuredClone(fallback); }
}

async function request<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, body === undefined ? undefined : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const envelope = await response.json() as Envelope<T>;
  if (!response.ok || !envelope.ok || envelope.data === undefined) throw new Error(envelope.error ?? "The Go Core rejected this request.");
  return envelope.data;
}
