export interface AgentWorkflowV1 {
    action_artifact?:      ActionArtifact;
    campaign_definition?:  CampaignDefinition;
    capability_manifest?:  CapabilityManifest;
    context_bundle?:       ContextBundle;
    context_pack_edition?: ContextPackEdition;
    job_definition?:       JobDefinition;
    receipt?:              Receipt;
    replay_bundle?:        ReplayBundle;
    workflow_definition?:  WorkflowDefinition;
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

export interface ContextBundle {
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
    artifact_type:  string;
    id:             string;
    kind:           EntryKind;
    media_type:     string;
    schema_version: number;
    sha256:         string;
}

export type EntryKind = "context_pack";

export type ContextBundleKind = "context_bundle";

export interface ContextPackEdition {
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

export interface JobDefinition {
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

export interface Receipt {
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

export interface ReplayBundle {
    aggregate_id:        string;
    bundle_hash:         string;
    cutoff_receipt_hash: string;
    kind:                ReplayBundleKind;
    receipts:            Receipt[];
    schema_version:      number;
}

export type ReplayBundleKind = "replay_bundle";

export interface WorkflowDefinition {
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
