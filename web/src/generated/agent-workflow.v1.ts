export interface AgentWorkflowV1 {
    action_artifact?:                  ActionArtifact;
    approval_brief?:                   ApprovalBrief;
    approval_decided_event_payload?:   ApprovalDecidedEventPayload;
    approval_preview?:                 ApprovalPreview;
    approval_requested_event_payload?: ApprovalRequestedEventPayload;
    attempt_reserved_event_payload?:   AttemptReservedEventPayload;
    authoring_catalog?:                AuthoringCatalog;
    budget_exhausted_event_payload?:   BudgetExhaustedEventPayload;
    campaign_admission_event_payload?: CampaignAdmissionEventPayload;
    campaign_definition?:              CampaignDefinition;
    campaign_drive_preview?:           CampaignDrivePreview;
    campaign_drive_receipt?:           CampaignDriveReceipt;
    campaign_execution_state?:         CampaignExecutionStateClass;
    campaign_terminal_event_payload?:  CampaignTerminalEventPayload;
    canvas_portfolio_snapshot?:        CanvasPortfolioSnapshot;
    canvas_snapshot?:                  CanvasSnapshot;
    capability_manifest?:              CapabilityManifest;
    change_case_canvas?:               ChangeCaseCanvas;
    change_case_event_payload?:        ChangeCaseEventPayload;
    change_case_receipt_link?:         ChangeCaseReceiptLinkElement;
    change_case_state?:                ChangeCaseStateClass;
    change_proposal?:                  ChangeProposalElement;
    conflict_set?:                     Conflicts;
    context_bundle?:                   Bundle;
    context_pack_edition?:             Edition;
    context_transition_event_payload?: ContextTransitionEventPayload;
    core_completed_event_payload?:     CoreCompletedEventPayload;
    job_definition?:                   Job;
    mutation_evidence?:                ApplyEvidence;
    mutation_lease?:                   Lease;
    needs_context_event_payload?:      NeedsContextEventPayload;
    node_completed_event_payload?:     NodeCompletedEventPayload;
    proposal_replacement?:             Replacement;
    provider_execution_event_payload?: ProviderExecutionEventPayload;
    provider_isolation_evidence?:      ProviderIsolation;
    receipt?:                          ReplayBundleReceipt;
    redacted_replay?:                  RedactedReplay;
    replay_bundle?:                    CampaignReplay;
    resolution_artifact?:              Resolution;
    resource_ref?:                     Resource;
    wait_resumed_event_payload?:       WaitResumedEventPayload;
    wait_started_event_payload?:       WaitStartedEventPayload;
    workflow_admission?:               WorkflowAdmission;
    workflow_admission_preview?:       WorkflowAdmissionPreview;
    workflow_definition?:              WorkflowDefinitionElement;
    workflow_lint_report?:             WorkflowLintReport;
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
    approval_policy?:      string;
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

export type Decision = "approve" | "reject" | "revise";

export interface ApprovalDecidedEventPayload {
    approval_id:           string;
    approval_receipt_hash: string;
    artifact:              ActionArtifact;
    decision:              Decision;
    node_id:               string;
    workflow_ref:          string;
}

export interface ApprovalPreview {
    actor:               string;
    base_revision:       number;
    brief:               ApprovalBrief;
    brief_hash:          string;
    commit_token:        string;
    expires_at?:         string;
    kind:                ApprovalPreviewKind;
    preview_hash:        string;
    schema_version:      number;
    source_aggregate_id: string;
}

export type ApprovalPreviewKind = "approval_preview";

export interface ApprovalRequestedEventPayload {
    action_hash:        string;
    approval_id:        string;
    approval_policy?:   string;
    node_id:            string;
    source_replay_hash: string;
    workflow_ref:       string;
}

export interface AttemptReservedEventPayload {
    node_id:      string;
    started_at:   string;
    workflow_ref: string;
}

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

export interface BudgetExhaustedEventPayload {
    blocker_code:        string;
    node_id:             string;
    result_replay_hash?: string;
    workflow_ref:        string;
}

export interface CampaignAdmissionEventPayload {
    state: CampaignExecutionStateClass;
}

export interface CampaignExecutionStateClass {
    aggregate_id:        string;
    blocker_code?:       string;
    campaign_hash:       string;
    campaign_id:         string;
    job_hash:            string;
    job_id:              string;
    kind:                CampaignExecutionStateKind;
    next_node_id?:       string;
    next_workflow_ref?:  string;
    nodes:               [CampaignExecutionStateNode, ...CampaignExecutionStateNode[]];
    provider_isolation?: ProviderIsolation;
    schema_version:      number;
    started_at:          string;
    status:              CampaignExecutionStateStatus;
    updated_at:          string;
    usage:               Usage;
    workflow_hashes:     { [key: string]: string };
}

export type CampaignExecutionStateKind = "campaign_execution_state";

export interface CampaignExecutionStateNode {
    approval_id?:         string;
    blocker_code?:        string;
    blocker_fingerprint?: string;
    completed_at?:        string;
    context_bundle_hash?: string;
    node_id:              string;
    result_replay_hash?:  string;
    signal?:              string;
    started_at?:          string;
    status:               NodeStatus;
    usage:                Usage;
    wake_at?:             string;
    workflow_ref:         string;
}

export type NodeStatus = "pending" | "needs_context" | "running" | "awaiting_approval" | "waiting" | "completed" | "completed_no_action" | "blocked" | "budget_exhausted";

export interface Usage {
    actions:          number;
    attempts:         number;
    candidates:       number;
    duration_seconds: number;
}

export interface ProviderIsolation {
    declared_environment: string[];
    driver:               Driver;
    evidence_hash:        string;
    executable_sha256?:   string;
    kind:                 ProviderIsolationEvidenceKind;
    profile:              Profile;
    schema_version:       number;
    staged_root_sha256?:  string;
}

export type Driver = "in_process" | "sandbox_exec" | "bubblewrap";

export type ProviderIsolationEvidenceKind = "provider_isolation_evidence";

export type Profile = "trusted_in_process" | "staged_subprocess";

export type CampaignExecutionStateStatus = "admitted" | "running" | "blocked" | "completed" | "terminal";

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

export interface CampaignDrivePreview {
    kind:           CampaignDrivePreviewKind;
    next_action:    NextAction;
    schema_version: number;
    state:          CampaignExecutionStateClass;
}

export type CampaignDrivePreviewKind = "campaign_drive_preview";

export type NextAction = "run_node" | "wait" | "blocked" | "complete";

export interface CampaignDriveReceipt {
    campaign_replay?: CampaignReplay;
    kind:             CampaignDriveReceiptKind;
    node_replay?:     CampaignReplay;
    schema_version:   number;
    state:            CampaignExecutionStateClass;
    transitions:      number;
}

export interface CampaignReplay {
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
    receipt_type:          PurpleReceiptType;
    schema_version:        number;
}

export type ReceiptKind = "receipt";

export type PurpleReceiptType = "compile" | "admission" | "campaign_admission" | "context_bound" | "needs_context" | "context_available" | "attempt_reserved" | "pack_edition" | "invocation" | "provider_execution" | "result" | "node_completed" | "budget_exhausted" | "approval_requested" | "approval_decided" | "wait_started" | "wait_resumed" | "core_completed" | "approval" | "terminal" | "change_proposed" | "change_merged" | "conflict_detected" | "resolution_proposed" | "resolution_approved" | "mutation_lease_acquired" | "mutation_applied" | "mutation_readback" | "resource_generation_advanced";

export type CampaignDriveReceiptKind = "campaign_drive_receipt";

export interface CampaignTerminalEventPayload {
    state: StateEnum;
}

export type StateEnum = "completed";

export interface CanvasPortfolioSnapshot {
    campaigns:            [CanvasPortfolioCampaign, ...CanvasPortfolioCampaign[]];
    generated_at:         string;
    job:                  Job;
    kind:                 CanvasPortfolioSnapshotKind;
    schema_version:       number;
    selected_campaign_id: string;
}

export interface CanvasPortfolioCampaign {
    campaign_id: string;
    canvas:      CanvasSnapshot;
    state:       CampaignState;
}

export interface CanvasSnapshot {
    admission_replays?: CampaignReplay[];
    approval_replays?:  CampaignReplay[];
    definition:         Definition;
    executions:         ExecutionElement[];
    generated_at:       string;
    kind:               CanvasSnapshotKind;
    next_safe_action:   NextSafeAction;
    replays:            CampaignReplay[];
    schema_version:     number;
}

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
    nodes:            [DefinitionElement, ...DefinitionElement[]];
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

export interface DefinitionElement {
    approval_policy?:    string;
    blocker_codes?:      string[];
    budget:              Budget;
    capabilities:        string[];
    context:             DefaultContextElement[];
    deadline_seconds?:   number;
    depends_on:          string[];
    executor:            string;
    id:                  string;
    input_slots:         OutputElement[];
    kind:                NodeKindEnum;
    output_slots:        OutputElement[];
    wait_delay_seconds?: number;
    wait_mode?:          Mode;
    wait_signal?:        string;
}

export interface OutputElement {
    artifact_kind?:        ArtifactKind;
    artifact_type:         string;
    consumers?:            string[];
    content_schema?:       string;
    counts_as_candidates?: boolean;
    id:                    string;
    max_items:             number;
    min_items:             number;
}

export type ArtifactKind = "context_pack" | "action_artifact";

export type Mode = "time" | "signal";

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
    status:            ContextPortStatus;
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

export type ContextPortStatus = "configured" | "resolved" | "missing" | "stale" | "partial" | "degraded" | "invalid";

export interface ExecutionReceipt {
    id:           string;
    occurred_at:  string;
    receipt_hash: string;
    receipt_type: FluffyReceiptType;
}

export type FluffyReceiptType = "compile" | "admission" | "campaign_admission" | "context_bound" | "needs_context" | "context_available" | "attempt_reserved" | "pack_edition" | "invocation" | "provider_execution" | "result" | "node_completed" | "budget_exhausted" | "approval_requested" | "approval_decided" | "wait_started" | "wait_resumed" | "core_completed" | "approval" | "terminal";

export type CanvasSnapshotKind = "canvas_snapshot";

export interface NextSafeAction {
    kind:          NextSafeActionKind;
    node_id?:      string;
    reason:        string;
    workflow_ref?: string;
}

export type NextSafeActionKind = "none" | "start_node" | "request_context" | "request_approval" | "retry" | "terminal";

export type CanvasPortfolioSnapshotKind = "canvas_portfolio_snapshot";

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

export interface ChangeCaseCanvas {
    generated_at:   string;
    kind:           ChangeCaseCanvasKind;
    receipts:       ChangeCaseReceiptLinkElement[];
    schema_version: number;
    state:          ChangeCaseStateClass;
}

export type ChangeCaseCanvasKind = "change_case_canvas";

export interface ChangeCaseReceiptLinkElement {
    id:           string;
    occurred_at:  string;
    receipt_hash: string;
    receipt_type: ChangeCaseReceiptLinkReceiptType;
}

export type ChangeCaseReceiptLinkReceiptType = "change_proposed" | "change_merged" | "conflict_detected" | "resolution_proposed" | "resolution_approved" | "mutation_lease_acquired" | "mutation_applied" | "mutation_readback" | "resource_generation_advanced";

export interface ChangeCaseStateClass {
    apply_evidence?:           ApplyEvidence;
    blocker_code?:             BlockerCode;
    conflicts?:                Conflicts;
    id:                        string;
    kind:                      ChangeCaseStateKind;
    lease?:                    Lease;
    merged_change?:            unknown[] | { [key: string]: unknown };
    merged_change_hash?:       string;
    proposals:                 [ChangeProposalElement, ...ChangeProposalElement[]];
    readback_evidence?:        ApplyEvidence;
    resolution?:               Resolution;
    resolution_approval_hash?: string;
    resource:                  Resource;
    schema_version:            number;
    status:                    ChangeCaseStateStatus;
    updated_at:                string;
}

export interface ApplyEvidence {
    case_id:        string;
    change_hash:    string;
    evidence_hash:  string;
    kind:           MutationEvidenceKind;
    lease_hash:     string;
    observed_hash:  string;
    resource:       Resource;
    schema_version: number;
}

export type MutationEvidenceKind = "mutation_apply_evidence" | "mutation_readback_evidence";

export interface Resource {
    baseline_hash:     string;
    baseline_revision: string;
    generation:        number;
    kind:              ResourceRefKind;
    resource_id:       string;
    resource_type:     string;
    schema_version:    number;
}

export type ResourceRefKind = "resource_ref";

export type BlockerCode = "resource_generation_advanced" | "resolution_approval_required" | "mutation_lease_active";

export interface Conflicts {
    case_id:        string;
    conflict_hash:  string;
    items:          [ItemElement, ...ItemElement[]];
    kind:           ConflictSetKind;
    resource:       Resource;
    schema_version: number;
}

export interface ItemElement {
    path:         string;
    proposal_ids: [string, string, ...string[]];
    reason:       string;
}

export type ConflictSetKind = "conflict_set";

export type ChangeCaseStateKind = "change_case_state";

export interface Lease {
    acquired_at:           string;
    approval_receipt_hash: string;
    case_id:               string;
    change_hash:           string;
    expires_at:            string;
    id:                    string;
    kind:                  MutationLeaseKind;
    lease_hash:            string;
    resource:              Resource;
    schema_version:        number;
}

export type MutationLeaseKind = "mutation_lease";

export interface ChangeProposalElement {
    artifact_id:                  string;
    campaign_id:                  string;
    capability_hash:              string;
    case_id:                      string;
    change:                       unknown[] | { [key: string]: unknown };
    change_hash:                  string;
    evidence_hashes:              [string, ...string[]];
    id:                           string;
    job_id:                       string;
    kind:                         ChangeProposalKind;
    node_id:                      string;
    proposal_hash:                string;
    replacement?:                 Replacement;
    resource:                     Resource;
    schema_version:               number;
    source_campaign_aggregate_id: string;
    source_campaign_replay_hash:  string;
    source_result_aggregate_id:   string;
    source_result_replay_hash:    string;
    workflow_ref:                 string;
}

export type ChangeProposalKind = "change_proposal";

export interface Replacement {
    proposal_id: string;
    reason:      ProposalReplacementReason;
}

export type ProposalReplacementReason = "rebase" | "resolver" | "human_implementation";

export interface Resolution {
    case_id:                string;
    conflict_hash:          string;
    id:                     string;
    kind:                   ResolutionArtifactKind;
    resolution_hash:        string;
    resolution_proposal_id: string;
    resolved_change:        unknown[] | { [key: string]: unknown };
    resolved_change_hash:   string;
    schema_version:         number;
    source_proposal_ids:    [string, string, ...string[]];
}

export type ResolutionArtifactKind = "resolution_artifact";

export type ChangeCaseStateStatus = "proposed" | "ready" | "conflicted" | "awaiting_resolution_approval" | "leased" | "applied" | "completed" | "blocked";

export interface ChangeCaseEventPayload {
    state: ChangeCaseStateClass;
}

export interface ContextTransitionEventPayload {
    bundle:                        Bundle;
    node_id:                       string;
    packs:                         Edition[];
    previous_blocker_fingerprint?: string;
    workflow_ref:                  string;
}

export interface CoreCompletedEventPayload {
    completed_at: string;
    node_id:      string;
    status:       CoreCompletedEventPayloadStatus;
    workflow_ref: string;
}

export type CoreCompletedEventPayloadStatus = "completed" | "completed_no_action";

export interface NeedsContextEventPayload {
    blocker_fingerprint: string;
    node_id:             string;
    reasons:             { [key: string]: ReasonValue };
    requirements:        [string, ...string[]];
    workflow_ref:        string;
}

export type ReasonValue = "unavailable" | "unusable";

export interface NodeCompletedEventPayload {
    completed_at:       string;
    node_id:            string;
    result_replay_hash: string;
    status:             CoreCompletedEventPayloadStatus;
    usage:              Usage;
    workflow_ref:       string;
}

export interface ProviderExecutionEventPayload {
    completed_at:    string;
    idempotency_key: string;
    isolation:       ProviderIsolation;
    node_id:         string;
}

export interface RedactedReplay {
    aggregate_id:        string;
    cutoff_receipt_hash: string;
    cutoff_receipt_id:   string;
    kind:                RedactedReplayKind;
    proof:               Proof;
    receipts:            [RedactedReplayReceipt, ...RedactedReplayReceipt[]];
    schema_version:      number;
}

export type RedactedReplayKind = "redacted_replay";

export interface Proof {
    cutoff_receipt_hash: string;
    excluded_classes:    [ExcludedClass, ExcludedClass, ...ExcludedClass[]];
    kind:                ProofKind;
    policy:              Policy;
    proof_hash:          string;
    schema_version:      number;
    source_bundle_hash:  string;
}

export type ExcludedClass = "actor" | "payload";

export type ProofKind = "replay_redaction_proof";

export type Policy = "public_metadata@1";

export interface RedactedReplayReceipt {
    aggregate_version:     number;
    id:                    string;
    input_hashes:          string[];
    occurred_at:           string;
    output_hashes:         string[];
    previous_receipt_hash: null | string;
    receipt_hash:          string;
    receipt_type:          FluffyReceiptType;
}

export interface WaitResumedEventPayload {
    node_id:      string;
    resumed_at:   string;
    signal?:      string;
    signal_hash?: string;
    workflow_ref: string;
}

export interface WaitStartedEventPayload {
    mode:         Mode;
    node_id:      string;
    signal?:      string;
    started_at:   string;
    wake_at?:     string;
    workflow_ref: string;
}

export interface WorkflowAdmission {
    campaign:        CampaignDefinition;
    campaign_hash:   string;
    compile_hash:    string;
    definition_hash: string;
    job:             Job;
    job_hash:        string;
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
    campaign:        CampaignDefinition;
    campaign_hash:   string;
    catalog_hash:    string;
    commit_token:    string;
    compile_hash:    string;
    definition_hash: string;
    expanded_nodes:  [ExpandedNodeElement, ...ExpandedNodeElement[]];
    job:             Job;
    job_hash:        string;
    kind:            WorkflowAdmissionPreviewKind;
    preview_hash:    string;
    schema_version:  number;
    workflow:        WorkflowDefinitionElement;
}

export interface ExpandedNodeElement {
    context_authorities: AuthorityElement[];
    definition:          DefinitionElement;
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

// Stable v1 export aliases. Quicktype names shared schemas by first use, so
// adding a new catalog entry must not rename existing downstream imports.
export type NodeElement = DefinitionElement;
export type ReplayBundleElement = CampaignReplay;
export type Status = ContextPortStatus;
