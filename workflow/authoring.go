package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

type ExecutorCatalog map[string]contractsv1.NodeDefinitionKind

type AuthoringCore struct {
	registry         *Registry
	executors        ExecutorCatalog
	capabilities     CapabilityCatalog
	outputs          OutputCatalog
	blockers         map[string]bool
	approvalPolicies map[string]bool
	ledger           Ledger
	sources          Ledger
}

func NewAuthoringCore(registry *Registry, executors ExecutorCatalog, capabilities CapabilityCatalog, outputs OutputCatalog, blockers, approvalPolicies []string, ledger Ledger) *AuthoringCore {
	return NewAuthoringCoreWithSources(registry, executors, capabilities, outputs, blockers, approvalPolicies, ledger, ledger)
}

func NewAuthoringCoreWithSources(registry *Registry, executors ExecutorCatalog, capabilities CapabilityCatalog, outputs OutputCatalog, blockers, approvalPolicies []string, ledger, sources Ledger) *AuthoringCore {
	core := &AuthoringCore{registry: registry, executors: ExecutorCatalog{}, capabilities: CapabilityCatalog{}, outputs: OutputCatalog{}, blockers: map[string]bool{}, approvalPolicies: map[string]bool{}, ledger: ledger, sources: sources}
	for ref, kind := range executors {
		core.executors[ref] = kind
	}
	for name, authority := range capabilities {
		core.capabilities[name] = authority
	}
	for ref, validator := range outputs {
		core.outputs[ref] = validator
	}
	for _, code := range blockers {
		core.blockers[code] = true
	}
	for _, policy := range approvalPolicies {
		core.approvalPolicies[policy] = true
	}
	return core
}

func (c *AuthoringCore) Catalog() (contractsv1.AuthoringCatalog, error) {
	if c == nil || c.registry == nil || c.ledger == nil || c.sources == nil {
		return contractsv1.AuthoringCatalog{}, errors.New("authoring registry and ledger are required")
	}
	catalog := contractsv1.AuthoringCatalog{Kind: contractsv1.AuthoringCatalogKindAuthoringCatalog, SchemaVersion: 1, Executors: []contractsv1.CatalogExecutor{}, Producers: []contractsv1.CatalogProducer{}, Capabilities: []contractsv1.CatalogCapability{}, OutputSchemas: []contractsv1.WorkflowRef{}, Blockers: []string{}, ApprovalPolicies: []string{}}
	for ref, kind := range c.executors {
		catalog.Executors = append(catalog.Executors, contractsv1.CatalogExecutor{Ref: contractsv1.WorkflowRef(ref), NodeKind: contractsv1.CatalogExecutorNodeKind(kind)})
	}
	for _, producer := range c.registry.producers {
		described, ok := producer.(authoringProducer)
		if !ok {
			continue
		}
		item, _ := described.authoringContract()
		catalog.Producers = append(catalog.Producers, item)
	}
	for name, authority := range c.capabilities {
		catalog.Capabilities = append(catalog.Capabilities, contractsv1.CatalogCapability{Name: contractsv1.Identifier(name), Authority: contractsv1.CatalogCapabilityAuthority(authority)})
	}
	for ref := range c.outputs {
		catalog.OutputSchemas = append(catalog.OutputSchemas, ref)
	}
	for code := range c.blockers {
		catalog.Blockers = append(catalog.Blockers, code)
	}
	for policy := range c.approvalPolicies {
		catalog.ApprovalPolicies = append(catalog.ApprovalPolicies, policy)
	}
	sort.Slice(catalog.Executors, func(i, j int) bool { return catalog.Executors[i].Ref < catalog.Executors[j].Ref })
	sort.Slice(catalog.Producers, func(i, j int) bool { return catalog.Producers[i].Selector < catalog.Producers[j].Selector })
	sort.Slice(catalog.Capabilities, func(i, j int) bool { return catalog.Capabilities[i].Name < catalog.Capabilities[j].Name })
	sort.Slice(catalog.OutputSchemas, func(i, j int) bool { return catalog.OutputSchemas[i] < catalog.OutputSchemas[j] })
	sort.Strings(catalog.Blockers)
	sort.Strings(catalog.ApprovalPolicies)
	hash, err := Digest(catalog)
	if err != nil {
		return contractsv1.AuthoringCatalog{}, err
	}
	catalog.CatalogHash = contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("AuthoringCatalog", catalog); err != nil {
		return contractsv1.AuthoringCatalog{}, err
	}
	return catalog, nil
}

func (c *AuthoringCore) Lint(definition contractsv1.WorkflowDefinition) contractsv1.WorkflowLintReport {
	report := contractsv1.WorkflowLintReport{Kind: contractsv1.WorkflowLintReportKindWorkflowLintReport, SchemaVersion: 1, Valid: true, Issues: []contractsv1.WorkflowLintIssue{}}
	add := func(code, path, message string) {
		report.Valid = false
		report.Issues = append(report.Issues, contractsv1.WorkflowLintIssue{Severity: contractsv1.WorkflowLintIssueSeverityError, Code: contractsv1.Identifier(code), Path: path, Message: message})
	}
	body, err := json.Marshal(definition)
	if err != nil {
		add("invalid-definition", "$", err.Error())
		return report
	}
	if _, err := contract.ValidateWorkflow(body); err != nil {
		add(lintCode(err), "$", err.Error())
		return report
	}
	for index, node := range definition.Nodes {
		path := fmt.Sprintf("$.nodes[%d]", index)
		kind, ok := c.executors[node.Executor]
		if !ok || kind != node.Kind {
			add("executor-unregistered", path+".executor", fmt.Sprintf("executor %q is not registered for %s Nodes", node.Executor, node.Kind))
		}
		for _, requirement := range append(append([]contractsv1.ContextRequirement(nil), definition.DefaultContext...), node.Context...) {
			producer, ok := c.registry.lookup(string(requirement.Selector))
			if !ok {
				add("producer-missing", path+".context", fmt.Sprintf("producer %q is not registered", requirement.Selector))
				continue
			}
			if _, authorable := producer.(authoringProducer); !authorable || !producer.Supports(string(requirement.PackType), requirement.SchemaVersion) {
				add("context-incompatible", path+".context", fmt.Sprintf("producer %q cannot author %s@%d", requirement.Selector, requirement.PackType, requirement.SchemaVersion))
			}
		}
		for _, capability := range node.Capabilities {
			authority, ok := c.capabilities[capability]
			if !ok {
				add("capability-unregistered", path+".capabilities", fmt.Sprintf("capability %q is not registered", capability))
				continue
			}
			if node.Kind == contractsv1.NodeDefinitionKindAgent && (authority == contractsv1.CapabilityManifestCapabilitiesElemAuthorityCanonicalMutation || authority == contractsv1.CapabilityManifestCapabilitiesElemAuthorityExternalMutation || authority == contractsv1.CapabilityManifestCapabilitiesElemAuthoritySystemRecovery) {
				add("capability-unsafe", path+".capabilities", fmt.Sprintf("Agent Node cannot hold %s authority", authority))
			}
		}
		for _, output := range node.OutputSlots {
			if output.ContentSchema == nil || c.outputs[*output.ContentSchema] == nil {
				add("output-unregistered", path+".output_slots", fmt.Sprintf("output schema for %q is not registered", output.Id))
			}
		}
		for _, code := range node.BlockerCodes {
			if !c.blockers[code] {
				add("blocker-unregistered", path+".blocker_codes", fmt.Sprintf("blocker %q is not registered", code))
			}
		}
		if node.Kind == contractsv1.NodeDefinitionKindApproval {
			if node.ApprovalPolicy == nil || !c.approvalPolicies[string(*node.ApprovalPolicy)] {
				add("approval-policy-missing", path+".approval_policy", "Approval Node requires a registered approval policy")
			}
		} else if node.ApprovalPolicy != nil {
			add("approval-policy-invalid", path+".approval_policy", "only Approval Nodes may declare an approval policy")
		}
	}
	for _, code := range definition.Blockers {
		if !c.blockers[code] {
			add("blocker-unregistered", "$.blockers", fmt.Sprintf("blocker %q is not registered", code))
		}
	}
	if report.Valid {
		if _, _, err := compileWorkflow(definition, c.registry, "lint", time.Unix(0, 0).UTC()); err != nil {
			add(lintCode(err), "$", err.Error())
		}
	}
	if report.Valid && !hasReachableTerminal(definition) {
		add("terminal-unreachable", "$.nodes", "every terminal branch must produce a declared Workflow output")
	}
	return report
}

func (c *AuthoringCore) Preview(job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition, definition contractsv1.WorkflowDefinition, actor string) (contractsv1.WorkflowAdmissionPreview, contractsv1.WorkflowLintReport, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return contractsv1.WorkflowAdmissionPreview{}, contractsv1.WorkflowLintReport{}, errors.New("actor is required")
	}
	report := c.Lint(definition)
	if !report.Valid {
		return contractsv1.WorkflowAdmissionPreview{}, report, errors.New("workflow lint failed")
	}
	base, err := c.currentRevision(string(definition.Id))
	if err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	if definition.Version != base+1 {
		return contractsv1.WorkflowAdmissionPreview{}, report, fmt.Errorf("workflow version must be %d", base+1)
	}
	compiled, compileReceipt, err := compileWorkflow(definition, c.registry, "preview", time.Unix(0, 0).UTC())
	if err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	if err := contract.ValidateDefinition("JobDefinition", job); err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	if err := contract.ValidateDefinition("CampaignDefinition", campaign); err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	if err := validateCampaignWorkflowBinding(job, campaign, compiled.WorkflowRef); err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	for nodeIndex, node := range compiled.Nodes {
		for contextIndex, requirement := range node.Definition.Context {
			if !requirement.Required {
				continue
			}
			producer, _ := c.registry.lookup(string(requirement.Selector))
			pack, resolveErr := producer.Resolve(context.Background(), ProducerRequest{Requirement: requirement, Job: job, Campaign: campaign, Workflow: definition, WorkflowRef: compiled.WorkflowRef, CompileReceipt: compileReceipt, EvidenceCutoff: campaign.EvidenceFrontier.Cutoff})
			if resolveErr == nil {
				resolveErr = validatePack(pack, requirement, campaign.Scope, campaign.EvidenceFrontier.Cutoff)
			}
			if resolveErr != nil {
				report.Valid = false
				report.Issues = append(report.Issues, contractsv1.WorkflowLintIssue{Severity: contractsv1.WorkflowLintIssueSeverityError, Code: "context-unavailable", Path: fmt.Sprintf("$.nodes[%d].context[%d]", nodeIndex, contextIndex), Message: fmt.Sprintf("required context %q is unavailable for this Campaign", requirement.Id)})
			}
		}
	}
	if !report.Valid {
		return contractsv1.WorkflowAdmissionPreview{}, report, errors.New("workflow lint failed")
	}
	jobHash, campaignHash, err := aggregateDefinitionHashes(RunRequest{Job: job, Campaign: campaign})
	if err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	catalog, err := c.Catalog()
	if err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	preview := contractsv1.WorkflowAdmissionPreview{Kind: contractsv1.WorkflowAdmissionPreviewKindWorkflowAdmissionPreview, SchemaVersion: 1, Actor: actor, BaseRevision: base, Job: job, Campaign: campaign, Workflow: definition, JobHash: jobHash, CampaignHash: campaignHash, DefinitionHash: compiled.DefinitionHash, CompileHash: compiled.CompileHash, CatalogHash: catalog.CatalogHash, ExpandedNodes: []contractsv1.ExpandedNodeContract{}}
	for _, node := range compiled.Nodes {
		preview.ExpandedNodes = append(preview.ExpandedNodes, contractsv1.ExpandedNodeContract{Definition: node.Definition, ContextAuthorities: c.contextAuthorities(node.Definition)})
	}
	previewHash, err := Digest(preview)
	if err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	preview.PreviewHash = contractsv1.SHA256(previewHash)
	token, err := Digest(struct {
		Purpose string
		Preview contractsv1.SHA256
	}{"confirm-workflow", preview.PreviewHash})
	if err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	preview.CommitToken = contractsv1.SHA256(token)
	if err := contract.ValidateDefinition("WorkflowAdmissionPreview", preview); err != nil {
		return contractsv1.WorkflowAdmissionPreview{}, report, err
	}
	return preview, report, nil
}

type AdmissionAudit struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Subject       string `json:"subject"`
	PageOrigin    string `json:"page_origin"`
	Tool          string `json:"tool"`
	InputsSHA256  string `json:"inputs_sha256"`
}

func (c *AuthoringCore) Confirm(preview contractsv1.WorkflowAdmissionPreview, actor string, occurredAt time.Time) (contractsv1.WorkflowAdmission, error) {
	return c.ConfirmWithAudit(preview, actor, occurredAt, nil)
}

func (c *AuthoringCore) ConfirmWithAudit(preview contractsv1.WorkflowAdmissionPreview, actor string, occurredAt time.Time, audit *AdmissionAudit) (contractsv1.WorkflowAdmission, error) {
	actor = strings.TrimSpace(actor)
	aggregate := workflowDefinitionAggregate(string(preview.Workflow.Id))
	if existing, ok := c.existingAdmission(aggregate, preview.PreviewHash); ok {
		if existing.Receipt.Actor == nil || *existing.Receipt.Actor != actor {
			return contractsv1.WorkflowAdmission{}, errors.New("confirmation actor does not match")
		}
		return existing, nil
	}
	current, err := c.currentRevision(string(preview.Workflow.Id))
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	if current != preview.BaseRevision {
		return contractsv1.WorkflowAdmission{}, errors.New("workflow preview is stale")
	}
	expected, _, err := c.Preview(preview.Job, preview.Campaign, preview.Workflow, actor)
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	if !reflect.DeepEqual(expected, preview) {
		return contractsv1.WorkflowAdmission{}, errors.New("workflow preview or commit token was altered")
	}
	var previous *contractsv1.SHA256
	if current > 0 {
		replay, err := c.ledger.Replay(aggregate)
		if err != nil {
			return contractsv1.WorkflowAdmission{}, err
		}
		hash := replay.Receipts[len(replay.Receipts)-1].ReceiptHash
		previous = &hash
	}
	payload := map[string]any{"job": preview.Job, "campaign": preview.Campaign, "workflow": preview.Workflow, "job_hash": preview.JobHash, "campaign_hash": preview.CampaignHash, "definition_hash": preview.DefinitionHash, "compile_hash": preview.CompileHash, "preview_hash": preview.PreviewHash}
	if audit != nil {
		payload["webmcp_audit"] = *audit
	}
	receipt, err := sealActorReceipt(aggregate, current+1, contractsv1.ReceiptReceiptTypeAdmission, actor, occurredAt, previous, []contractsv1.SHA256{preview.JobHash, preview.CampaignHash, preview.DefinitionHash, preview.CompileHash, preview.CatalogHash}, []contractsv1.SHA256{preview.PreviewHash}, payload)
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	if err := c.ledger.Append(receipt); err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	admission := contractsv1.WorkflowAdmission{Kind: contractsv1.WorkflowAdmissionKindWorkflowAdmission, SchemaVersion: 1, Job: preview.Job, Campaign: preview.Campaign, Workflow: preview.Workflow, Revision: current + 1, JobHash: preview.JobHash, CampaignHash: preview.CampaignHash, DefinitionHash: preview.DefinitionHash, CompileHash: preview.CompileHash, PreviewHash: preview.PreviewHash, Receipt: receipt}
	if err := contract.ValidateDefinition("WorkflowAdmission", admission); err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	return admission, nil
}

func (c *AuthoringCore) ReadWorkflow(id string, version int) (contractsv1.WorkflowAdmission, error) {
	replay, err := c.ledger.Replay(workflowDefinitionAggregate(id))
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	if version < 1 || version > len(replay.Receipts) {
		return contractsv1.WorkflowAdmission{}, errors.New("workflow version is not admitted")
	}
	return admissionFromReceipt(replay.Receipts[version-1])
}

func (c *AuthoringCore) AdmissionReplay(id string) (contractsv1.ReplayBundle, error) {
	return c.ledger.Replay(workflowDefinitionAggregate(id))
}

func (c *AuthoringCore) ApprovalReplay(id string) (contractsv1.ReplayBundle, error) {
	return c.ledger.Replay(approvalAggregate(id))
}

func MaterializeAdmission(replay contractsv1.ReplayBundle, version int) (contractsv1.WorkflowAdmission, error) {
	if err := VerifyReplay(replay); err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	if version < 1 || version > len(replay.Receipts) {
		return contractsv1.WorkflowAdmission{}, errors.New("workflow admission version is not present")
	}
	return admissionFromReceipt(replay.Receipts[version-1])
}

func (c *AuthoringCore) PreviewApproval(brief contractsv1.ApprovalBrief, actor, sourceAggregateID string) (contractsv1.ApprovalPreview, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return contractsv1.ApprovalPreview{}, errors.New("actor is required")
	}
	source, err := c.sources.Replay(sourceAggregateID)
	if err != nil {
		return contractsv1.ApprovalPreview{}, errors.New("approval source is not canonical")
	}
	actionHash, err := Digest(brief.Action)
	if err != nil {
		return contractsv1.ApprovalPreview{}, err
	}
	canonicalID := contractsv1.Identifier(shortID("approval-", actionHash))
	if brief.Id != "" && brief.Id != canonicalID {
		return contractsv1.ApprovalPreview{}, errors.New("approval id does not match the exact action")
	}
	brief.Id = canonicalID
	if err := contract.ValidateDefinition("ApprovalBrief", brief); err != nil {
		return contractsv1.ApprovalPreview{}, err
	}
	if brief.Action.ApprovalState != contractsv1.ActionArtifactApprovalStatePending {
		return contractsv1.ApprovalPreview{}, errors.New("approval action must be pending")
	}
	if !nodeCompletedReplay(source) {
		return contractsv1.ApprovalPreview{}, errors.New("approval source has not reached canonical node_completed terminal state")
	}
	material, err := MaterializeReplay(source, c.outputs)
	if err != nil {
		return contractsv1.ApprovalPreview{}, fmt.Errorf("approval source Replay: %w", err)
	}
	bound := false
	for _, artifact := range material.Artifacts {
		if artifact.Id == brief.Action.Id && reflect.DeepEqual(artifact, brief.Action) {
			bound = true
		}
	}
	if !bound {
		return contractsv1.ApprovalPreview{}, errors.New("approval action is not present in the canonical source Replay")
	}
	result, ok := receiptByType(source, contractsv1.ReceiptReceiptTypeResult)
	if !ok || !containsEvidenceReceipt(brief.Evidence, result) {
		return contractsv1.ApprovalPreview{}, errors.New("approval evidence does not bind the canonical result receipt")
	}
	found := false
	for _, option := range brief.Options {
		if option.Id == brief.RecommendedOptionId {
			found = true
		}
	}
	if !found {
		return contractsv1.ApprovalPreview{}, errors.New("recommended approval option is missing")
	}
	base := 0
	if replay, err := c.ledger.Replay(approvalAggregate(string(brief.Id))); err == nil {
		if len(replay.Receipts) > 0 {
			return contractsv1.ApprovalPreview{}, errors.New("approval was already decided")
		}
	} else if !errors.Is(err, ErrReplayEmpty) {
		return contractsv1.ApprovalPreview{}, err
	}
	briefHash, err := Digest(brief)
	if err != nil {
		return contractsv1.ApprovalPreview{}, err
	}
	terminal, _ := receiptByType(source, contractsv1.ReceiptReceiptTypeTerminal)
	expiresAt := terminal.OccurredAt.Add(30 * 24 * time.Hour).UTC()
	preview := contractsv1.ApprovalPreview{Kind: contractsv1.ApprovalPreviewKindApprovalPreview, SchemaVersion: 1, Actor: actor, BaseRevision: base, SourceAggregateId: sourceAggregateID, Brief: brief, BriefHash: contractsv1.SHA256(briefHash), ExpiresAt: &expiresAt}
	previewHash, err := Digest(preview)
	if err != nil {
		return contractsv1.ApprovalPreview{}, err
	}
	preview.PreviewHash = contractsv1.SHA256(previewHash)
	token, err := Digest(struct {
		Purpose string
		Preview contractsv1.SHA256
	}{"confirm-approval", preview.PreviewHash})
	if err != nil {
		return contractsv1.ApprovalPreview{}, err
	}
	preview.CommitToken = contractsv1.SHA256(token)
	if err := contract.ValidateDefinition("ApprovalPreview", preview); err != nil {
		return contractsv1.ApprovalPreview{}, err
	}
	return preview, nil
}

func (c *AuthoringCore) ConfirmApproval(preview contractsv1.ApprovalPreview, actor, optionID string, occurredAt time.Time) (contractsv1.Receipt, error) {
	if strings.TrimSpace(actor) != preview.Actor {
		return contractsv1.Receipt{}, errors.New("confirmation actor does not match")
	}
	if preview.ExpiresAt != nil && occurredAt.After(*preview.ExpiresAt) {
		return contractsv1.Receipt{}, errors.New("approval preview has expired")
	}
	if replay, err := c.ledger.Replay(approvalAggregate(string(preview.Brief.Id))); err == nil {
		for _, receipt := range replay.Receipts {
			if fmt.Sprint(receipt.Payload["preview_hash"]) == string(preview.PreviewHash) && fmt.Sprint(receipt.Payload["selected_option_id"]) == optionID && receipt.Actor != nil && *receipt.Actor == actor {
				return receipt, nil
			}
		}
	}
	expected, err := c.PreviewApproval(preview.Brief, actor, preview.SourceAggregateId)
	if err != nil || !reflect.DeepEqual(expected, preview) {
		return contractsv1.Receipt{}, errors.New("approval preview is stale or altered")
	}
	unsigned := preview
	unsigned.PreviewHash = ""
	unsigned.CommitToken = ""
	hash, err := Digest(unsigned)
	if err != nil || contractsv1.SHA256(hash) != preview.PreviewHash {
		return contractsv1.Receipt{}, errors.New("approval preview is stale or altered")
	}
	token, err := Digest(struct {
		Purpose string
		Preview contractsv1.SHA256
	}{"confirm-approval", preview.PreviewHash})
	if err != nil || contractsv1.SHA256(token) != preview.CommitToken {
		return contractsv1.Receipt{}, errors.New("approval preview is stale or altered")
	}
	selected := false
	for _, option := range preview.Brief.Options {
		if string(option.Id) == optionID {
			selected = true
		}
	}
	if !selected {
		return contractsv1.Receipt{}, errors.New("approval option is invalid")
	}
	receipt, err := sealActorReceipt(approvalAggregate(string(preview.Brief.Id)), 1, contractsv1.ReceiptReceiptTypeApproval, actor, occurredAt, nil, []contractsv1.SHA256{preview.BriefHash, preview.Brief.Action.ContentSha256}, []contractsv1.SHA256{preview.PreviewHash}, map[string]any{"brief": preview.Brief, "preview_hash": preview.PreviewHash, "selected_option_id": optionID})
	if err != nil {
		return contractsv1.Receipt{}, err
	}
	if err := c.ledger.Append(receipt); err != nil {
		return contractsv1.Receipt{}, err
	}
	return receipt, nil
}

func (c *AuthoringCore) currentRevision(id string) (int, error) {
	replay, err := c.ledger.Replay(workflowDefinitionAggregate(id))
	if errors.Is(err, ErrReplayEmpty) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return len(replay.Receipts), nil
}

func (c *AuthoringCore) existingAdmission(aggregate string, previewHash contractsv1.SHA256) (contractsv1.WorkflowAdmission, bool) {
	replay, err := c.ledger.Replay(aggregate)
	if err != nil {
		return contractsv1.WorkflowAdmission{}, false
	}
	for _, receipt := range replay.Receipts {
		if fmt.Sprint(receipt.Payload["preview_hash"]) == string(previewHash) {
			admission, err := admissionFromReceipt(receipt)
			return admission, err == nil
		}
	}
	return contractsv1.WorkflowAdmission{}, false
}

func admissionFromReceipt(receipt contractsv1.Receipt) (contractsv1.WorkflowAdmission, error) {
	if receipt.ReceiptType != contractsv1.ReceiptReceiptTypeAdmission {
		return contractsv1.WorkflowAdmission{}, errors.New("receipt is not a Workflow admission")
	}
	var workflow contractsv1.WorkflowDefinition
	if err := decodePayload(receipt.Payload["workflow"], &workflow); err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	var job contractsv1.JobDefinition
	if err := decodePayload(receipt.Payload["job"], &job); err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	var campaign contractsv1.CampaignDefinition
	if err := decodePayload(receipt.Payload["campaign"], &campaign); err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	readHash := func(key string) (contractsv1.SHA256, error) {
		value := fmt.Sprint(receipt.Payload[key])
		if value == "" || value == "<nil>" {
			return "", fmt.Errorf("admission %s is invalid", key)
		}
		return contractsv1.SHA256(value), nil
	}
	definitionHash, err := readHash("definition_hash")
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	compileHash, err := readHash("compile_hash")
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	previewHash, err := readHash("preview_hash")
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	jobHash, err := readHash("job_hash")
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	campaignHash, err := readHash("campaign_hash")
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	admission := contractsv1.WorkflowAdmission{Kind: contractsv1.WorkflowAdmissionKindWorkflowAdmission, SchemaVersion: 1, Job: job, Campaign: campaign, Workflow: workflow, Revision: receipt.AggregateVersion, JobHash: jobHash, CampaignHash: campaignHash, DefinitionHash: definitionHash, CompileHash: compileHash, PreviewHash: previewHash, Receipt: receipt}
	if err := contract.ValidateDefinition("WorkflowAdmission", admission); err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	body, err := json.Marshal(workflow)
	if err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	identity, err := contract.ValidateWorkflow(body)
	if err != nil || identity.Hash != string(definitionHash) {
		return contractsv1.WorkflowAdmission{}, errors.New("admission Workflow does not match its definition hash")
	}
	actualJobHash, actualCampaignHash, err := aggregateDefinitionHashes(RunRequest{Job: job, Campaign: campaign})
	if err != nil || actualJobHash != jobHash || actualCampaignHash != campaignHash {
		return contractsv1.WorkflowAdmission{}, errors.New("admission target definitions do not match their hashes")
	}
	if err := validateCampaignWorkflowBinding(job, campaign, contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", workflow.Id, workflow.Version))); err != nil {
		return contractsv1.WorkflowAdmission{}, err
	}
	return admission, nil
}

func (c *AuthoringCore) contextAuthorities(node contractsv1.NodeDefinition) []contractsv1.ExpandedNodeContractContextAuthoritiesElem {
	seen := map[contractsv1.ExpandedNodeContractContextAuthoritiesElem]bool{}
	for _, requirement := range node.Context {
		if producer, ok := c.registry.lookup(string(requirement.Selector)); ok {
			if described, ok := producer.(authoringProducer); ok {
				_, authorities := described.authoringContract()
				for _, authority := range authorities {
					seen[authority] = true
				}
			}
		}
	}
	result := make([]contractsv1.ExpandedNodeContractContextAuthoritiesElem, 0, len(seen))
	for authority := range seen {
		result = append(result, authority)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func hasReachableTerminal(definition contractsv1.WorkflowDefinition) bool {
	dependents := map[string]int{}
	workflowOutputs := map[string]bool{}
	for _, output := range definition.Outputs {
		workflowOutputs[string(output.Id)] = true
	}
	for _, node := range definition.Nodes {
		for _, dependency := range node.DependsOn {
			dependents[dependency]++
		}
	}
	for _, node := range definition.Nodes {
		if dependents[string(node.Id)] != 0 {
			continue
		}
		if node.Kind == contractsv1.NodeDefinitionKindTerminal {
			return true
		}
		for _, output := range node.OutputSlots {
			if workflowOutputs[string(output.Id)] {
				return true
			}
		}
	}
	return false
}

func lintCode(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "cycle"):
		return "dependency-cycle"
	case strings.Contains(message, "producer"):
		return "producer-missing"
	case strings.Contains(message, "deadline"):
		return "budget-missing"
	case strings.Contains(message, "output"):
		return "output-undeclared"
	default:
		return "invalid-definition"
	}
}

func workflowDefinitionAggregate(id string) string { return "workflow-definition/" + id }
func approvalAggregate(id string) string           { return "approval/" + id }

func containsEvidenceReceipt(evidence []contractsv1.ArtifactRef, receipt contractsv1.Receipt) bool {
	for _, ref := range evidence {
		if ref.Kind == contractsv1.ArtifactRefKindReceipt && ref.Id == receipt.Id && ref.Sha256 == receipt.ReceiptHash {
			return true
		}
	}
	return false
}
