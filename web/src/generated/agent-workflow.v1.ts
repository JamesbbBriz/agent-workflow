export interface AgentWorkflowV1 {
    action_artifact?:      ActionArtifact;
    campaign_definition?:  CampaignDefinition;
    canvas_snapshot?:      CanvasSnapshot;
    capability_manifest?:  CapabilityManifest;
    context_bundle?:       Bundle;
    context_pack_edition?: Edition;
    job_definition?:       Job;
    receipt?:              ReplayBundleReceipt;
    replay_bundle?:        ReplayBundleElement;
    workflow_definition?:  WorkflowDefinitionElement;
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
    definition:       Definition;
    executions:       ExecutionElement[];
    generated_at:     string;
    kind:             CanvasSnapshotKind;
    next_safe_action: NextSafeAction;
    replays:          ReplayBundleElement[];
    schema_version:   number;
}

export interface Definition {
    campaign:       CampaignDefinition;
    campaign_state: CampaignState;
    job:            Job;
    workflows:      [WorkflowDefinitionElement, ...WorkflowDefinitionElement[]];
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
    blocker_codes?:    string[];
    budget:            Budget;
    capabilities:      string[];
    context:           DefaultContextElement[];
    deadline_seconds?: number;
    depends_on:        string[];
    executor:          string;
    id:                string;
    input_slots:       OutputElement[];
    kind:              NodeKind;
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

export type NodeKind = "deterministic" | "agent" | "approval" | "wait" | "terminal";

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
    authority:              ContextPackEditionAuthority;
    captured_at:            string;
    content:                unknown[] | { [key: string]: unknown };
    content_sha256:         string;
    coverage:               Coverage;
    expires_at:             string;
    id:                     string;
    kind:                   ContextPackEditionKind;
    pack_schema_version:    number;
    pack_type:              string;
    provenance:             [ProvenanceElement, ...ProvenanceElement[]];
    schema_version:         number;
    scope:                  Scope;
    supersedes_edition_id?: string;
}

export type ContextPackEditionAuthority = "canonical" | "external_observation" | "derived";

export type Coverage = "complete" | "partial";

export type ContextPackEditionKind = "context_pack_edition";

export interface ProvenanceElement {
    artifact_type:  string;
    id:             string;
    kind:           ProvenanceKind;
    media_type:     string;
    schema_version: number;
    sha256:         string;
}

export type ProvenanceKind = "context_pack" | "action_artifact" | "receipt";

export type Status = "configured" | "resolved" | "missing" | "stale" | "partial" | "degraded" | "invalid";

export interface ExecutionReceipt {
    id:           string;
    occurred_at:  string;
    receipt_hash: string;
    receipt_type: ReceiptType;
}

export type ReceiptType = "compile" | "admission" | "pack_edition" | "invocation" | "provider_execution" | "result" | "approval" | "terminal";

export type CanvasSnapshotKind = "canvas_snapshot";

export interface NextSafeAction {
    kind:          NextSafeActionKind;
    node_id?:      string;
    reason:        string;
    workflow_ref?: string;
}

export type NextSafeActionKind = "none" | "start_node" | "request_context" | "request_approval" | "retry" | "terminal";

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

export type CapabilityAuthority = "read" | "local_mutation" | "canonical_mutation" | "external_mutation" | "system_recovery";

export type CapabilityManifestKind = "capability_manifest";
