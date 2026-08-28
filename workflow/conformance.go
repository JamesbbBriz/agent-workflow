package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

const ConformanceContractVersion = "agent-workflow.v1"

// RunConformance executes a fixture through the same public Core seams used by
// applications. It performs no network calls and never selects a real provider.
func RunConformance(ctx context.Context, fixture contractsv1.ConformanceFixture, toolVersion string) (contractsv1.ConformanceReport, error) {
	if toolVersion == "" {
		return contractsv1.ConformanceReport{}, errors.New("conformance tool version is required")
	}
	if err := contract.ValidateDefinition("ConformanceFixture", fixture); err != nil {
		return contractsv1.ConformanceReport{}, err
	}
	fixtureHash, err := Digest(fixture)
	if err != nil {
		return contractsv1.ConformanceReport{}, err
	}
	runtime, err := newConformanceRuntime(fixture)
	if err != nil {
		return contractsv1.ConformanceReport{}, err
	}
	report := contractsv1.ConformanceReport{
		Kind: contractsv1.ConformanceReportKindConformanceReport, SchemaVersion: 1,
		ContractVersion: ConformanceContractVersion, ToolVersion: toolVersion, Profile: fixture.Profile,
		FixtureSha256: contractsv1.SHA256(fixtureHash), Checks: []contractsv1.ConformanceCheck{}, Passed: true,
	}
	addCheck := func(id, code string, hashes ...contractsv1.SHA256) {
		if hashes == nil {
			hashes = []contractsv1.SHA256{}
		}
		report.Checks = append(report.Checks, contractsv1.ConformanceCheck{Id: contractsv1.Identifier(id), Status: contractsv1.ConformanceCheckStatusPass, Code: contractsv1.Identifier(code), EvidenceHashes: hashes})
	}
	addCheck("definitions", "validated", contractsv1.SHA256(fixtureHash))
	for _, descriptor := range BundledProviderDescriptors() {
		readiness, err := InspectProviderReadiness(descriptor.Id)
		if err != nil {
			return contractsv1.ConformanceReport{}, err
		}
		code := "provider-unavailable"
		if readiness.Ready {
			code = "explicit-execution-required"
		}
		readinessHash, err := Digest(readiness)
		if err != nil {
			return contractsv1.ConformanceReport{}, err
		}
		report.Checks = append(report.Checks, contractsv1.ConformanceCheck{
			Id: contractsv1.Identifier("provider-" + string(descriptor.Id)), Status: contractsv1.ConformanceCheckStatusSkipped,
			Code: contractsv1.Identifier(code), EvidenceHashes: []contractsv1.SHA256{contractsv1.SHA256(readinessHash)},
		})
	}

	if err := runtime.admit(); err != nil {
		return contractsv1.ConformanceReport{}, err
	}
	addCheck("admission", "canonical", runtime.admissionHashes...)

	var sources []ChangeProposalSource
	for index, campaign := range fixture.Campaigns {
		workflows, err := workflowsForCampaign(campaign, runtime.workflows)
		if err != nil {
			return contractsv1.ConformanceReport{}, err
		}
		if fixture.Profile == contractsv1.ConformanceFixtureProfileSeoShaped && index == 1 {
			runtime.setContextsAvailable(false)
			blocked, err := runtime.engine.Drive(ctx, CampaignDriveCommand{CampaignRunRequest: CampaignRunRequest{Job: fixture.Job, Campaign: campaign, Workflows: workflows}, MaxTransitions: 1})
			if err != nil {
				return contractsv1.ConformanceReport{}, err
			}
			if blocked.State.NextWorkflowRef == nil || blocked.State.NextNodeId == nil || nodeState(blocked.State, *blocked.State.NextWorkflowRef, string(*blocked.State.NextNodeId)).Status != contractsv1.CampaignNodeExecutionStatusNeedsContext {
				return contractsv1.ConformanceReport{}, errors.New("SEO-shaped fixture did not produce typed needs_context")
			}
			addCheck("context-recovery", "needs-context")
			runtime.setContextsAvailable(true)
		}
		approvalsBefore := runtime.approvals
		result, err := runtime.driveCampaign(ctx, campaign, workflows)
		if err != nil {
			return contractsv1.ConformanceReport{}, err
		}
		if result.State.Status != contractsv1.CampaignExecutionStateStatusCompleted || result.State.Usage.Attempts > campaign.Budget.MaxAttempts || result.State.Usage.Actions > campaign.Budget.MaxActions {
			return contractsv1.ConformanceReport{}, errors.New("Campaign did not complete within its admitted budget")
		}
		addCheck("campaign-"+string(campaign.Id), "terminal", result.CampaignReplay.BundleHash)
		addCheck("dependency-order-"+string(campaign.Id), "core-derived")
		addCheck("budget-"+string(campaign.Id), "bounded")
		if runtime.approvals > approvalsBefore {
			addCheck("approval-"+string(campaign.Id), "exact-human-confirmation")
		}
		campaignSources, err := runtime.proposalSources(campaign, workflows)
		if err != nil {
			return contractsv1.ConformanceReport{}, err
		}
		sources = append(sources, campaignSources...)
		var resultHashes, contextHashes []contractsv1.SHA256
		for _, source := range campaignSources {
			replay, err := runtime.ledger.Replay(source.ResultAggregateID)
			if err != nil {
				return contractsv1.ConformanceReport{}, err
			}
			material, err := MaterializeReplay(replay, runtime.outputs)
			if err != nil || VerifyContextBundle(material.Invocation.Bundle, material.Invocation.Context) != nil {
				return contractsv1.ConformanceReport{}, errors.New("provider receipt chain has invalid Context authority")
			}
			if !runtime.manifestHashes[material.Invocation.Capabilities.ManifestHash] {
				return contractsv1.ConformanceReport{}, errors.New("provider receipt chain does not bind a fixture Capability Manifest")
			}
			resultHashes = append(resultHashes, replay.BundleHash)
			contextHashes = append(contextHashes, material.Invocation.Bundle.BundleHash)
		}
		addCheck("provider-receipts-"+string(campaign.Id), "normalized", resultHashes...)
		addCheck("context-"+string(campaign.Id), "exact-bundle", contextHashes...)

		cutoff := result.CampaignReplay.Receipts[len(result.CampaignReplay.Receipts)-1].Id
		raw, err := runtime.engine.ReplayAt(ctx, CampaignRef{JobID: fixture.Job.Id, CampaignID: campaign.Id}, ReceiptID(cutoff), ReplayViewRaw)
		if err != nil || raw.Raw == nil || VerifyReplay(*raw.Raw) != nil {
			return contractsv1.ConformanceReport{}, errors.New("exact-cutoff raw Replay failed verification")
		}
		redacted, err := runtime.engine.ReplayAt(ctx, CampaignRef{JobID: fixture.Job.Id, CampaignID: campaign.Id}, ReceiptID(cutoff), ReplayViewPublicMetadataV1)
		if err != nil || redacted.Redacted == nil {
			return contractsv1.ConformanceReport{}, fmt.Errorf("exact-cutoff redacted Replay failed: %w", err)
		}
		if err := VerifyRedactedReplay(*redacted.Redacted, *result.CampaignReplay); err != nil {
			return contractsv1.ConformanceReport{}, fmt.Errorf("exact-cutoff redacted Replay failed verification: %w", err)
		}
		addCheck("replay-"+string(campaign.Id), "exact-cutoff-redacted", raw.Raw.BundleHash, redacted.Redacted.Proof.ProofHash)
	}

	if fixture.Profile == contractsv1.ConformanceFixtureProfileSeoShaped {
		if len(fixture.Campaigns) < 2 || len(fixture.Workflows) < 2 || len(sources) < 3 {
			return contractsv1.ConformanceReport{}, errors.New("SEO-shaped fixture requires multiple Campaigns, Workflows, and proposal sources")
		}
		changeHash, err := runtime.runChangeCase(ctx, sources[:3])
		if err != nil {
			return contractsv1.ConformanceReport{}, err
		}
		addCheck("change-case", "conflict-resolved-and-applied", changeHash)
	}
	if err := contract.ValidateDefinition("ConformanceReport", report); err != nil {
		return contractsv1.ConformanceReport{}, err
	}
	return report, nil
}

type conformanceRuntime struct {
	fixture         contractsv1.ConformanceFixture
	producers       []*conformanceProducer
	ledger          Ledger
	authoring       *AuthoringCore
	engine          *Engine
	outputs         OutputCatalog
	workflows       map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition
	admissionHashes []contractsv1.SHA256
	approvals       int
	manifestHashes  map[contractsv1.SHA256]bool
}

func newConformanceRuntime(fixture contractsv1.ConformanceFixture) (*conformanceRuntime, error) {
	capabilities := CapabilityCatalog{}
	manifestHashes := map[contractsv1.SHA256]bool{}
	for _, manifest := range fixture.CapabilityManifests {
		if err := contract.ValidateDefinition("CapabilityManifest", manifest); err != nil {
			return nil, err
		}
		if err := VerifyCapabilityManifest(manifest); err != nil {
			return nil, err
		}
		manifestHashes[manifest.ManifestHash] = true
		for _, capability := range manifest.Capabilities {
			name := string(capability.Name)
			if previous, exists := capabilities[name]; exists && previous != capability.Authority {
				return nil, fmt.Errorf("capability %q has conflicting authority", name)
			}
			capabilities[name] = capability.Authority
		}
	}
	packByType := map[string][]contractsv1.ContextPackEdition{}
	for _, pack := range fixture.ContextPacks {
		if err := contract.ValidateDefinition("ContextPackEdition", pack); err != nil {
			return nil, err
		}
		packByType[string(pack.PackType)] = append(packByType[string(pack.PackType)], pack)
	}
	selectors := map[string]contractsv1.ContextRequirement{}
	workflows := map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition{}
	executors, outputs := ExecutorCatalog{}, OutputCatalog{}
	for _, definition := range fixture.Workflows {
		ref := contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version))
		if _, exists := workflows[ref]; exists {
			return nil, fmt.Errorf("Workflow %q is duplicated", ref)
		}
		workflows[ref] = definition
		for _, requirement := range append(append([]contractsv1.ContextRequirement{}, definition.DefaultContext...), nodeContexts(definition)...) {
			if requirement.Selector != "intent-chain" {
				selectors[string(requirement.Selector)] = requirement
			}
		}
		for _, node := range definition.Nodes {
			executors[string(node.Executor)] = node.Kind
			for _, slot := range node.OutputSlots {
				if slot.ContentSchema != nil {
					outputs[*slot.ContentSchema] = acceptConformanceOutput
				}
			}
		}
	}
	producers := make([]Producer, 0, len(selectors)+1)
	toggles := make([]*conformanceProducer, 0, len(selectors))
	for selector, requirement := range selectors {
		catalog := NewCatalogProducer(selector, string(requirement.PackType), requirement.SchemaVersion, packByType[string(requirement.PackType)]...)
		toggle := &conformanceProducer{CatalogProducer: catalog, available: true}
		producers, toggles = append(producers, toggle), append(toggles, toggle)
	}
	producers = append(producers, NewIntentProducer())
	registry, err := NewRegistry(producers...)
	if err != nil {
		return nil, err
	}
	ledger := NewMemoryLedger()
	actors := ApprovalAuthorityCatalog{}
	for _, policy := range fixture.ApprovalPolicies {
		actors[policy] = []string{"conformance-human"}
	}
	authoring := NewAuthoringCore(registry, executors, capabilities, outputs, fixture.BlockerCodes, fixture.ApprovalPolicies, ledger).WithApprovalAuthorities(actors)
	provider := &conformanceProvider{results: map[string]ProviderResult{}}
	engine := NewEngine(registry, capabilities, outputs, provider, ledger).WithApprovalAuthorities(actors)
	return &conformanceRuntime{fixture: fixture, producers: toggles, ledger: ledger, authoring: authoring, engine: engine, outputs: outputs, workflows: workflows, manifestHashes: manifestHashes}, nil
}

func (r *conformanceRuntime) admit() error {
	for _, campaign := range r.fixture.Campaigns {
		definitions, err := workflowsForCampaign(campaign, r.workflows)
		if err != nil {
			return err
		}
		for _, definition := range definitions {
			preview, lint, err := r.authoring.Preview(r.fixture.Job, campaign, definition, "conformance-operator")
			if err != nil {
				return fmt.Errorf("Workflow %s lint failed (%v): %w", definition.Id, lint.Issues, err)
			}
			receipt, err := r.authoring.Confirm(preview, "conformance-operator", campaign.EvidenceFrontier.Cutoff)
			if err != nil {
				return err
			}
			r.admissionHashes = append(r.admissionHashes, receipt.Receipt.ReceiptHash)
		}
	}
	return nil
}

func (r *conformanceRuntime) setContextsAvailable(available bool) {
	for _, producer := range r.producers {
		producer.available = available
	}
}

func (r *conformanceRuntime) driveCampaign(ctx context.Context, campaign contractsv1.CampaignDefinition, workflows []contractsv1.WorkflowDefinition) (contractsv1.CampaignDriveReceipt, error) {
	command := CampaignDriveCommand{CampaignRunRequest: CampaignRunRequest{Job: r.fixture.Job, Campaign: campaign, Workflows: workflows}, MaxTransitions: 64}
	result, err := r.engine.Drive(ctx, command)
	if err != nil {
		return result, err
	}
	for result.State.Status != contractsv1.CampaignExecutionStateStatusCompleted {
		var awaiting *contractsv1.CampaignNodeExecution
		for index := range result.State.Nodes {
			if result.State.Nodes[index].Status == contractsv1.CampaignNodeExecutionStatusAwaitingApproval {
				awaiting = &result.State.Nodes[index]
				break
			}
		}
		if awaiting == nil || result.NodeReplay == nil {
			return result, errors.New("Campaign stopped without a resolvable approval gate")
		}
		if err := r.approve(*awaiting, *result.NodeReplay, campaign); err != nil {
			return result, err
		}
		result, err = r.engine.Drive(ctx, command)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (r *conformanceRuntime) approve(node contractsv1.CampaignNodeExecution, replay contractsv1.ReplayBundle, campaign contractsv1.CampaignDefinition) error {
	material, err := MaterializeReplay(replay, r.outputs)
	if err != nil || len(material.Artifacts) == 0 {
		return errors.New("approval source has no canonical action artifact")
	}
	result, ok := receiptByType(replay, contractsv1.ReceiptReceiptTypeResult)
	if !ok {
		return errors.New("approval source has no result receipt")
	}
	definition := r.workflows[node.WorkflowRef]
	var policy *contractsv1.Identifier
	for _, candidate := range definition.Nodes {
		if candidate.Id == node.NodeId {
			policy = candidate.ApprovalPolicy
		}
	}
	if policy == nil {
		return errors.New("approval Node has no policy")
	}
	brief := contractsv1.ApprovalBrief{
		Kind: contractsv1.ApprovalBriefKindApprovalBrief, SchemaVersion: 1, Title: "Approve the exact conformance action?",
		Evidence:            []contractsv1.ArtifactRef{{Id: result.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "result", SchemaVersion: 1, Sha256: result.ReceiptHash, MediaType: "application/json"}},
		Options:             []contractsv1.ApprovalOption{{Id: "approve", Label: "Approve", Decision: contractsv1.ApprovalOptionDecisionApprove, Tradeoffs: []string{"Accepts the exact fixture result"}}, {Id: "reject", Label: "Reject", Decision: contractsv1.ApprovalOptionDecisionReject, Tradeoffs: []string{"Leaves the fixture unchanged"}}},
		RecommendedOptionId: "approve", Recommendation: "Approve the bounded fixture result.", Risks: []string{"Conformance mutation is in-memory only."}, Action: material.Artifacts[0], ApprovalPolicy: policy,
	}
	preview, err := r.authoring.PreviewApproval(brief, "conformance-human", replay.AggregateId)
	if err != nil {
		return err
	}
	terminal, ok := receiptByType(replay, contractsv1.ReceiptReceiptTypeTerminal)
	if !ok {
		return errors.New("approval source has no terminal receipt")
	}
	_, err = r.authoring.ConfirmApproval(preview, "conformance-human", "approve", terminal.OccurredAt.Add(time.Second))
	if err == nil {
		r.approvals++
	}
	return err
}

func (r *conformanceRuntime) proposalSources(campaign contractsv1.CampaignDefinition, workflows []contractsv1.WorkflowDefinition) ([]ChangeProposalSource, error) {
	var sources []ChangeProposalSource
	campaignID, err := campaignExecutionID(r.fixture.Job.Id, campaign.Id)
	if err != nil {
		return nil, err
	}
	for _, definition := range workflows {
		for _, node := range definition.Nodes {
			if node.Kind != contractsv1.NodeDefinitionKindAgent {
				continue
			}
			aggregateID, err := executionID(RunRequest{Job: r.fixture.Job, Campaign: campaign, Workflow: definition, NodeID: string(node.Id)})
			if err != nil {
				return nil, err
			}
			replay, err := r.ledger.Replay(aggregateID)
			if err != nil {
				return nil, err
			}
			material, err := MaterializeReplay(replay, r.outputs)
			if err != nil || len(material.Artifacts) == 0 {
				return nil, errors.New("completed agent Node has no proposal artifact")
			}
			sources = append(sources, ChangeProposalSource{CampaignAggregateID: campaignID, ResultAggregateID: aggregateID, ArtifactID: string(material.Artifacts[0].Id)})
		}
	}
	return sources, nil
}

func (r *conformanceRuntime) runChangeCase(ctx context.Context, sources []ChangeProposalSource) (contractsv1.SHA256, error) {
	baseline := map[string]any{"fixture": "baseline"}
	baselineHash, err := Digest(baseline)
	if err != nil {
		return "", err
	}
	resource := contractsv1.ResourceRef{Kind: "resource_ref", SchemaVersion: 2, ResourceType: "document", ResourceId: "conformance-document", Generation: 1, BaselineRevision: "fixture-1", BaselineHash: contractsv1.SHA256(baselineHash)}
	authority := &conformanceResource{current: resource}
	mutation := &conformanceMutation{}
	resolverReplay, err := r.ledger.Replay(sources[2].ResultAggregateID)
	if err != nil {
		return "", err
	}
	resolverMaterial, err := MaterializeReplay(resolverReplay, r.outputs)
	if err != nil {
		return "", err
	}
	at := r.fixture.Campaigns[0].EvidenceFrontier.Cutoff.Add(time.Hour)
	now := at
	catalog := ChangeCaseCatalog{
		Mergers: map[contractsv1.Identifier]ChangeMergeAdapter{"document": conformanceMerger{}}, Resources: map[contractsv1.Identifier]ResourceAuthority{"document": authority}, Mutations: map[contractsv1.Identifier]MutationAdapter{"document": mutation},
		ApprovalActors:    map[contractsv1.Identifier][]string{"document": {"conformance-human"}},
		ResolutionSources: map[contractsv1.Identifier][]ResolutionSourceAuthority{"document": {{WorkflowRef: resolverMaterial.Invocation.WorkflowRef, NodeID: resolverMaterial.Invocation.Node.Id, Reason: contractsv1.ProposalReplacementReasonResolver}}},
		Clock:             func() time.Time { return now },
	}
	core := NewChangeCaseCore(NewMemoryLedger(), r.ledger, r.outputs, catalog)
	if _, err := core.SubmitProposal(ctx, resource, sources[0], at); err != nil {
		return "", err
	}
	conflicted, err := core.SubmitProposal(ctx, resource, sources[1], at.Add(time.Second))
	if err != nil || conflicted.Status != contractsv1.ChangeCaseStateStatusConflicted {
		return "", errors.New("Change Case did not detect the shared-resource conflict")
	}
	sources[2].Replacement = &contractsv1.ProposalReplacement{ProposalId: conflicted.Proposals[0].Id, Reason: contractsv1.ProposalReplacementReasonResolver}
	resolved, err := core.ProposeResolution(conflicted.Id, sources[2], at.Add(2*time.Second))
	if err != nil {
		return "", err
	}
	preview, err := core.PreviewApproval(resolved.Id, "conformance-human", at.Add(time.Hour))
	if err != nil {
		return "", err
	}
	now = at.Add(3 * time.Second)
	approved, err := core.ConfirmApproval(preview, now)
	if err != nil {
		return "", err
	}
	now = at.Add(4 * time.Second)
	if _, err := core.AcquireLease(ctx, approved.Id, time.Minute, now); err != nil {
		return "", err
	}
	now = at.Add(5 * time.Second)
	completed, err := core.Apply(ctx, approved.Id, now)
	if err != nil || completed.Status != contractsv1.ChangeCaseStateStatusCompleted || mutation.applies != 1 || mutation.readbacks != 1 {
		return "", errors.New("Change Case did not apply and read back exactly once")
	}
	replay, err := core.Replay(completed.Id)
	if err != nil {
		return "", err
	}
	return replay.BundleHash, nil
}

func workflowsForCampaign(campaign contractsv1.CampaignDefinition, catalog map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition) ([]contractsv1.WorkflowDefinition, error) {
	definitions := make([]contractsv1.WorkflowDefinition, 0, len(campaign.WorkflowPlan))
	for _, ref := range campaign.WorkflowPlan {
		definition, ok := catalog[ref]
		if !ok {
			return nil, fmt.Errorf("Campaign pins unknown Workflow %q", ref)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func nodeContexts(definition contractsv1.WorkflowDefinition) []contractsv1.ContextRequirement {
	var contexts []contractsv1.ContextRequirement
	for _, node := range definition.Nodes {
		contexts = append(contexts, node.Context...)
	}
	return contexts
}

type conformanceProducer struct {
	*CatalogProducer
	available bool
}

func (p *conformanceProducer) Resolve(ctx context.Context, request ProducerRequest) (contractsv1.ContextPackEdition, error) {
	if !p.available {
		return contractsv1.ContextPackEdition{}, ErrContextUnavailable
	}
	return p.CatalogProducer.Resolve(ctx, request)
}

type conformanceProvider struct {
	results map[string]ProviderResult
}

func (p *conformanceProvider) Start(_ context.Context, invocation Invocation) error {
	if _, exists := p.results[invocation.IdempotencyKey]; exists {
		return nil
	}
	artifacts := make([]contractsv1.ActionArtifact, 0, len(invocation.Node.OutputSlots))
	for _, slot := range invocation.Node.OutputSlots {
		content := map[string]any{"fixture": string(invocation.CampaignID), "node": string(invocation.Node.Id), "slot": string(slot.Id)}
		hash, err := Digest(content)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, contractsv1.ActionArtifact{
			Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: fmt.Sprintf("artifact-%s-%s", invocation.CampaignID, slot.Id), ArtifactType: slot.ArtifactType,
			JobId: invocation.JobID, CampaignId: invocation.CampaignID, WorkflowRef: invocation.WorkflowRef, NodeId: invocation.Node.Id,
			InputHashes: invocation.InputHashes, Content: content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending,
		})
	}
	p.results[invocation.IdempotencyKey] = ProviderResult{IdempotencyKey: invocation.IdempotencyKey, CompletedAt: invocation.Deadline.Add(-time.Second), Artifacts: artifacts}
	return nil
}

func (p *conformanceProvider) Poll(_ context.Context, key string) (ProviderResult, bool, error) {
	result, ok := p.results[key]
	return result, ok, nil
}

func (*conformanceProvider) Cancel(context.Context, string) error { return nil }

func acceptConformanceOutput(any) error { return nil }

type conformanceMerger struct{}

func (conformanceMerger) Merge(_ context.Context, _ contractsv1.ResourceRef, proposals []contractsv1.ChangeProposal) (MergeDecision, error) {
	if len(proposals) == 1 {
		return MergeDecision{Change: proposals[0].Change}, nil
	}
	return MergeDecision{Conflicts: []contractsv1.ConflictItem{{Path: "/fixture", ProposalIds: []contractsv1.Identifier{proposals[0].Id, proposals[1].Id}, Reason: "fixture proposals differ"}}}, nil
}

type conformanceResource struct{ current contractsv1.ResourceRef }

func (r *conformanceResource) Current(context.Context, string) (contractsv1.ResourceRef, error) {
	return r.current, nil
}

type conformanceMutation struct {
	applies, readbacks int
	observed           contractsv1.SHA256
}

func (m *conformanceMutation) Apply(_ context.Context, _ contractsv1.MutationLease, change any) (contractsv1.SHA256, error) {
	m.applies++
	hash, err := Digest(change)
	m.observed = contractsv1.SHA256(hash)
	return m.observed, err
}

func (m *conformanceMutation) Readback(context.Context, contractsv1.MutationLease) (contractsv1.SHA256, error) {
	m.readbacks++
	return m.observed, nil
}
