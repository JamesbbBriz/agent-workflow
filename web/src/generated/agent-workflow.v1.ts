export interface AgentWorkflowV1 {
    action_artifact?:            ActionArtifact;
    approval_brief?:             ApprovalBrief;
    approval_preview?:           ApprovalPreview;
    authoring_catalog?:          AuthoringCatalog;
    campaign_definition?:        CampaignDefinition;
    canvas_snapshot?:            CanvasSnapshot;
    capability_manifest?:        CapabilityManifest;
    context_bundle?:             Bundle;
    context_pack_edition?:       Edition;
    job_definition?:             Job;
    receipt?:                    ReplayBundleReceipt;
    replay_bundle?:              ReplayBundleElement;
    workflow_admission?:         WorkflowAdmission;
    workflow_admission_preview?: WorkflowAdmissionPreview;
    workflow_definition?:        WorkflowDefinitionElement;
    workflow_lint_report?:       WorkflowLintReport;
}

export interface ActionArtifact {
    approval_state: ApprovalState;
    artifact_type:  string;
    campaign_id:    string;
    content:        unknown[] | { [key: string]: unknown };
    content_sha256: string;
    id:             string;
    input_hashes:   [string, ...string[]];
    job_id:         string;
    kind:           ActionArtifactKind;
    node_id:        string;
    schema_version: number;
    workflow_ref:   string;
}

export type ApprovalState = "not_required" | "pending" | "approved" | "rejected" | "stale";

export type ActionArtifactKind = "action_artifact";

export interface ApprovalBrief {
    action:                ActionArtifact;
    evidence:              [EvidenceElement, ...EvidenceElement[]];
    id:                    string;
    kind:                  ApprovalBriefKind;
    options:               [OptionElement, OptionElement, ...OptionElement[]];
    recommendation:        string;
    recommended_option_id: string;
    risks:                 [string, ...string[]];
    schema_version:        number;
    title:                 string;
}

export interface EvidenceElement {
    artifact_type:  string;
    id:             string;
    kind:           EvidenceKind;
    media_type:     string;
    schema_version: number;
    sha256:         string;
}

export type EvidenceKind = "context_pack" | "action_artifact" | "receipt";

export type ApprovalBriefKind = "approval_brief";

export interface OptionElement {
    decision:  Decision;
    id:        string;
    label:     string;
    tradeoffs: [string, ...string[]];
}

export type Decision = "approve" | "reject";

export interface ApprovalPreview {
    actor:          string;
    base_revision:  number;
    brief:          ApprovalBrief;
    brief_hash:     string;
    commit_token:   string;
    kind:           ApprovalPreviewKind;
    preview_hash:   string;
    schema_version: number;
}

export type ApprovalPreviewKind = "approval_preview";

export interface AuthoringCatalog {
    approval_policies: string[];
    blockers:          string[];
    capabilities:      CapabilityElement[];
    catalog_hash:      string;
    executors:         ExecutorElement[];
    kind:              AuthoringCatalogKind;
    output_schemas:    string[];
    producers:         ProducerElement[];
    schema_version:    number;
}

export interface CapabilityElement {
    authority: CapabilityAuthority;
    name:      string;
}

export type CapabilityAuthority = "read" | "local_mutation" | "canonical_mutation" | "external_mutation" | "system_recovery";

export interface ExecutorElement {
    node_kind: NodeKindEnum;
    ref:       string;
}

export type NodeKindEnum = "deterministic" | "agent" | "approval" | "wait" | "terminal";

export type AuthoringCatalogKind = "authoring_catalog";

export interface ProducerElement {
    pack_type:      string;
    schema_version: number;
    selector:       string;
}

export interface CampaignDefinition {
    archetype:         string;
    budget:            Budget;
    definition_hash?:  string;
    evidence_frontier: EvidenceFrontier;
    id:                string;
    intent:            Intent;
    job_id:            string;
    kind:              CampaignDefinitionKind;
    schema_version:    number;
    scope:             Scope;
    workflow_plan:     [string, ...string[]];
}

export interface Budget {
    max_actions:           number;
    max_attempts:          number;
    max_candidates:        number;
    max_duration_seconds?: number;
}

export interface EvidenceFrontier {
    cutoff:        string;
    source_hashes: string[];
}

export interface Intent {
    completion:       [string, ...string[]];
    consumers?:       string[];
    descriptor_hash?: string;
    kind:             IntentKind;
    no_action_when:   [string, ...string[]];
    non_goals:        [string, ...string[]];
    objective:        string;
    schema_version:   number;
    success_signals:  [string, ...string[]];
    summary:          string;
    tags?:            string[];
    title:            string;
}

export type IntentKind = "job" | "campaign" | "workflow";

export type CampaignDefinitionKind = "campaign_definition";

export interface Scope {
    labels?:      { [key: string]: string };
    subject_ids:  [string, ...string[]];
    subject_type: string;
}

export interface CanvasSnapshot {
    admission_replays?: ReplayBundleElement[];
    approval_replays?:  ReplayBundleElement[];
    definition:         Definition;
    executions:         ExecutionElement[];
    generated_at:       string;
    kind:               CanvasSnapshotKind;
    next_safe_action:   NextSafeAction;
    replays:            ReplayBundleElement[];
    schema_version:     number;
}

export interface ReplayBundleElement {
    aggregate_id:        string;
    bundle_hash:         string;
    cutoff_receipt_hash: string;
    kind:                ReplayBundleKind;
    receipts:            ReplayBundleReceipt[];
    schema_version:      number;
}

export type ReplayBundleKind = "replay_bundle";

export interface ReplayBundleReceipt {
    actor?:                string;
    aggregate_id:          string;
    aggregate_version:     number;
    id:                    string;
    input_hashes:          string[];
    kind:                  ReceiptKind;
    occurred_at:           string;
    output_hashes:         string[];
    payload?:              { [key: string]: unknown };
    previous_receipt_hash: null | string;
    receipt_hash:          string;
    receipt_type:          ReceiptType;
    schema_version:        number;
}

export type ReceiptKind = "receipt";

export type ReceiptType = "compile" | "admission" | "pack_edition" | "invocation" | "provider_execution" | "result" | "approval" | "terminal";

export interface Definition {
    campaign:         CampaignDefinition;
    campaign_state:   CampaignState;
    job:              Job;
    workflow_states?: { [key: string]: CampaignState };
    workflows:        [WorkflowDefinitionElement, ...WorkflowDefinitionElement[]];
}

export type CampaignState = "configured" | "eligible" | "admitted" | "running" | "awaiting_human" | "blocked" | "completed" | "terminal";

export interface Job {
    budget:              Budget;
    campaign_archetypes: [string, ...string[]];
    definition_hash?:    string;
    id:                  string;
    intent:              Intent;
    kind:                JobDefinitionKind;
    schema_version:      number;
    scope:               Scope;
}

export type JobDefinitionKind = "job_definition";

export interface WorkflowDefinitionElement {
    blockers:         string[];
    completion:       [string, ...string[]];
    default_context:  DefaultContextElement[];
    definition_hash?: string;
    id:               string;
    intent:           Intent;
    kind:             WorkflowDefinitionKind;
    nodes:            [NodeElement, ...NodeElement[]];
    outputs:          OutputElement[];
    schema_version:   number;
    version:          number;
}

export interface DefaultContextElement {
    allow_partial:    boolean;
    edition_id?:      string;
    id:               string;
    max_age_seconds?: number;
    pack_type:        string;
    required:         boolean;
    schema_version:   number;
    selector:         string;
    subject_key?:     string;
}

export type WorkflowDefinitionKind = "workflow_definition";

export interface NodeElement {
    approval_policy?:  string;
    blocker_codes?:    string[];
    budget:            Budget;
    capabilities:      string[];
    context:           DefaultContextElement[];
    deadline_seconds?: number;
    depends_on:        string[];
    executor:          string;
    id:                string;
    input_slots:       OutputElement[];
    kind:              NodeKindEnum;
    output_slots:      OutputElement[];
}

export interface OutputElement {
    artifact_kind?:  ArtifactKind;
    artifact_type:   string;
    consumers?:      string[];
    content_schema?: string;
    id:              string;
    max_items:       number;
    min_items:       number;
}

export type ArtifactKind = "context_pack" | "action_artifact";

export interface ExecutionElement {
    aggregate_id:     string;
    approval_state:   ApprovalState;
    blocker_code?:    string;
    blocker_message?: string;
    bundle:           Bundle;
    context_ports:    ContextPortElement[];
    deadline:         string;
    node_id:          string;
    outputs:          ActionArtifact[];
    receipts:         ExecutionReceipt[];
    status:           CampaignState;
}

export interface Bundle {
    bundle_hash:       string;
    campaign_id:       string;
    degraded:          boolean;
    entries:           EntryElement[];
    evidence_cutoff:   string;
    id:                string;
    job_id:            string;
    kind:              ContextBundleKind;
    missing_optional?: string[];
    node_id:           string;
    schema_version:    number;
    workflow_ref:      string;
}

export interface EntryElement {
    artifact_type:   string;
    id:              string;
    kind:            EntryKind;
    media_type:      string;
    requirement_id?: string;
    schema_version:  number;
    sha256:          string;
}

export type EntryKind = "context_pack";

export type ContextBundleKind = "context_bundle";

export interface ContextPortElement {
    allow_partial:     boolean;
    consumers:         string[];
    edition?:          Edition;
    evidence_frontier: EvidenceFrontier;
    id:                string;
    node_id:           string;
    pack_type:         string;
    producer:          string;
    required:          boolean;
    schema_version:    number;
    selector:          string;
    status:            Status;
}

export interface Edition {
    authority:              AuthorityElement;
    captured_at:            string;
    content:                unknown[] | { [key: string]: unknown };
    content_sha256:         string;
    coverage:               Coverage;
    expires_at:             string;
    id:                     string;
    kind:                   ContextPackEditionKind;
    pack_schema_version:    number;
    pack_type:              string;
    provenance:             [EvidenceElement, ...EvidenceElement[]];
    schema_version:         number;
    scope:                  Scope;
    supersedes_edition_id?: string;
}

export type AuthorityElement = "canonical" | "external_observation" | "derived";

export type Coverage = "complete" | "partial";

export type ContextPackEditionKind = "context_pack_edition";

export type Status = "configured" | "resolved" | "missing" | "stale" | "partial" | "degraded" | "invalid";

export interface ExecutionReceipt {
    id:           string;
    occurred_at:  string;
    receipt_hash: string;
    receipt_type: ReceiptType;
}

export type CanvasSnapshotKind = "canvas_snapshot";

export interface NextSafeAction {
    kind:          NextSafeActionKind;
    node_id?:      string;
    reason:        string;
    workflow_ref?: string;
}

export type NextSafeActionKind = "none" | "start_node" | "request_context" | "request_approval" | "retry" | "terminal";

export interface CapabilityManifest {
    capabilities:   Capability[];
    id:             string;
    kind:           CapabilityManifestKind;
    manifest_hash:  string;
    schema_version: number;
}

export interface Capability {
    authority: CapabilityAuthority;
    name:      string;
}

export type CapabilityManifestKind = "capability_manifest";

export interface WorkflowAdmission {
    compile_hash:    string;
    definition_hash: string;
    kind:            WorkflowAdmissionKind;
    preview_hash:    string;
    receipt:         ReplayBundleReceipt;
    revision:        number;
    schema_version:  number;
    workflow:        WorkflowDefinitionElement;
}

export type WorkflowAdmissionKind = "workflow_admission";

export interface WorkflowAdmissionPreview {
    actor:           string;
    base_revision:   number;
    catalog_hash:    string;
    commit_token:    string;
    compile_hash:    string;
    definition_hash: string;
    expanded_nodes:  [ExpandedNodeElement, ...ExpandedNodeElement[]];
    kind:            WorkflowAdmissionPreviewKind;
    preview_hash:    string;
    schema_version:  number;
    workflow:        WorkflowDefinitionElement;
}

export interface ExpandedNodeElement {
    context_authorities: AuthorityElement[];
    definition:          NodeElement;
}

export type WorkflowAdmissionPreviewKind = "workflow_admission_preview";

export interface WorkflowLintReport {
    issues:         IssueElement[];
    kind:           WorkflowLintReportKind;
    schema_version: number;
    valid:          boolean;
}

export interface IssueElement {
    code:     string;
    message:  string;
    path:     string;
    severity: Severity;
}

export type Severity = "error" | "warning";

export type WorkflowLintReportKind = "workflow_lint_report";
