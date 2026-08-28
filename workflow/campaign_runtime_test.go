package workflow_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestCampaignRuntimeDerivesDependenciesAndCompletesTheDAG(t *testing.T) {
	t.Parallel()
	cutoff := time.Now().UTC().Add(-time.Hour)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	definition := loadExample(t)
	definition.Nodes[1].Kind = contractsv1.NodeDefinitionKindAgent
	definition.Nodes[1].Executor = "bounded-agent@1"
	definition.Nodes[1].ApprovalPolicy = nil
	job := jobFixture(scope)
	campaign := campaignFixture(scope, cutoff)
	campaign.Budget = contractsv1.Budget{MaxAttempts: 3, MaxActions: 2, MaxCandidates: 4}
	ledger := workflow.NewMemoryLedger()
	admit(t, ledger, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
	provider := &dagProvider{results: map[string]workflow.ProviderResult{}}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger)

	preview, err := engine.Preview(context.Background(), workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition})
	if err != nil {
		t.Fatal(err)
	}
	if preview.State.NextNodeId == nil || *preview.State.NextNodeId != "research" {
		t.Fatalf("Core did not derive the root Node: %+v", preview.State)
	}
	if _, err := engine.RunNode(context.Background(), workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "review"}); err == nil || !strings.Contains(err.Error(), "not the Core-derived") || provider.starts != 0 {
		t.Fatalf("caller selected a dependent Node: err=%v starts=%d", err, provider.starts)
	}

	driven, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}, MaxTransitions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if driven.Transitions != 2 || provider.starts != 2 || driven.State.Usage.Attempts != 2 || driven.State.Usage.Actions != 2 {
		t.Fatalf("DAG did not advance exactly two Nodes: %+v starts=%d", driven.State, provider.starts)
	}
	for _, node := range driven.State.Nodes {
		if node.Status != contractsv1.CampaignNodeExecutionStatusCompleted {
			t.Fatalf("Node %s is not complete: %+v", node.NodeId, node)
		}
	}
	material, err := workflow.MaterializeReplay(*driven.NodeReplay, dagOutputCatalog())
	if err != nil {
		t.Fatal(err)
	}
	var result contractsv1.Receipt
	for _, receipt := range driven.NodeReplay.Receipts {
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeResult {
			result = receipt
		}
	}
	brief := contractsv1.ApprovalBrief{Kind: contractsv1.ApprovalBriefKindApprovalBrief, SchemaVersion: 1, Title: "Accept the reviewed result?", Evidence: []contractsv1.ArtifactRef{{Id: result.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "result", SchemaVersion: 1, Sha256: result.ReceiptHash, MediaType: "application/json"}}, Options: []contractsv1.ApprovalOption{{Id: "approve", Label: "Approve", Decision: contractsv1.ApprovalOptionDecisionApprove, Tradeoffs: []string{"Accepts the exact reviewed result"}}, {Id: "reject", Label: "Reject", Decision: contractsv1.ApprovalOptionDecisionReject, Tradeoffs: []string{"Leaves the result unapproved"}}}, RecommendedOptionId: "approve", Recommendation: "Approve the exact result.", Risks: []string{"Changes are separately capability-gated."}, Action: material.Artifacts[0]}
	core := workflow.NewAuthoringCore(registry, workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent}, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), []string{"provider-timeout"}, []string{"human-confirm"}, ledger)
	approval, err := core.PreviewApproval(brief, "human@example.com", driven.NodeReplay.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.ConfirmApproval(approval, "human@example.com", "approve", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	terminal, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}, MaxTransitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.State.Status != contractsv1.CampaignExecutionStateStatusCompleted || terminal.Transitions != 1 {
		t.Fatalf("Campaign did not close canonically: %+v", terminal)
	}
}

func TestCampaignRuntimeRecordsOutputBudgetExhaustionBeforeAcceptance(t *testing.T) {
	t.Parallel()
	cutoff := time.Now().UTC().Add(-time.Hour)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	definition := loadExample(t)
	definition.Nodes = definition.Nodes[:1]
	definition.Nodes[0].OutputSlots[0].Consumers = []string{"workflow-output"}
	definition.Nodes[0].OutputSlots[0].MaxItems = 2
	definition.Outputs = append([]contractsv1.Slot(nil), definition.Nodes[0].OutputSlots...)
	definition.Outputs[0].Consumers = append([]string(nil), definition.Intent.Consumers...)
	job := jobFixture(scope)
	campaign := campaignFixture(scope, cutoff)
	campaign.WorkflowPlan = []contractsv1.WorkflowRef{"research-review@1"}
	ledger := workflow.NewMemoryLedger()
	admit(t, ledger, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
	provider := &dagProvider{results: map[string]workflow.ProviderResult{}, artifactsPerNode: 2}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger)

	receipt, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State.Status != contractsv1.CampaignExecutionStateStatusBlocked || receipt.State.BlockerCode == nil || *receipt.State.BlockerCode != "action-budget-exhausted" {
		t.Fatalf("oversized result was not blocked canonically: %+v", receipt.State)
	}
	if receipt.State.Usage.Actions != 0 || receipt.NodeReplay != nil {
		t.Fatalf("oversized output was accepted: %+v", receipt)
	}
}

func TestCampaignRuntimeRecordsCandidateBudgetExhaustionBeforeAcceptance(t *testing.T) {
	t.Parallel()
	cutoff := time.Now().UTC().Add(-time.Hour)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	definition := loadExample(t)
	definition.Nodes = definition.Nodes[:1]
	definition.Nodes[0].OutputSlots[0].Consumers = []string{"workflow-output"}
	definition.Nodes[0].OutputSlots[0].CountsAsCandidates = true
	definition.Nodes[0].OutputSlots[0].MaxItems = 2
	definition.Nodes[0].Budget.MaxActions = 2
	definition.Nodes[0].Budget.MaxCandidates = 1
	definition.Outputs = append([]contractsv1.Slot(nil), definition.Nodes[0].OutputSlots...)
	definition.Outputs[0].Consumers = append([]string(nil), definition.Intent.Consumers...)
	job := jobFixture(scope)
	campaign := campaignFixture(scope, cutoff)
	campaign.Budget.MaxActions = 2
	campaign.Budget.MaxCandidates = 1
	ledger := workflow.NewMemoryLedger()
	admit(t, ledger, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
	provider := &dagProvider{results: map[string]workflow.ProviderResult{}, artifactsPerNode: 2}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger)

	receipt, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State.Status != contractsv1.CampaignExecutionStateStatusBlocked || receipt.State.BlockerCode == nil || *receipt.State.BlockerCode != "candidate-budget-exhausted" || receipt.State.Usage.Candidates != 0 || receipt.NodeReplay != nil {
		t.Fatalf("oversized candidate output was accepted: %+v", receipt)
	}
}

func TestCampaignRuntimeDurationSurvivesRestart(t *testing.T) {
	cutoff := time.Now().UTC().Add(-time.Hour)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	definition := loadExample(t)
	definition.Nodes = definition.Nodes[:1]
	definition.Nodes[0].OutputSlots[0].Consumers = []string{"workflow-output"}
	definition.Outputs = append([]contractsv1.Slot(nil), definition.Nodes[0].OutputSlots...)
	definition.Outputs[0].Consumers = append([]string(nil), definition.Intent.Consumers...)
	one := 1
	definition.Nodes[0].DeadlineSeconds = &one
	definition.Nodes[0].Budget.MaxAttempts = 1
	job := jobFixture(scope)
	campaign := campaignFixture(scope, cutoff)
	campaign.Budget.MaxAttempts = 1
	campaign.Budget.MaxDurationSeconds = &one
	ledger := workflow.NewMemoryLedger()
	admit(t, ledger, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
	provider := &runtimePendingProvider{keys: map[string]struct{}{}}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger)
	if _, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	restarted := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger)
	receipt, err := restarted.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State.Status != contractsv1.CampaignExecutionStateStatusBlocked || receipt.State.BlockerCode == nil || *receipt.State.BlockerCode != "duration-budget-exhausted" || provider.starts != 1 {
		t.Fatalf("duration budget reset or provider duplicated after restart: state=%+v starts=%d", receipt.State, provider.starts)
	}
}

type dagProvider struct {
	starts           int
	artifactsPerNode int
	results          map[string]workflow.ProviderResult
}

func (p *dagProvider) Start(_ context.Context, invocation workflow.Invocation) error {
	if _, exists := p.results[invocation.IdempotencyKey]; exists {
		return nil
	}
	p.starts++
	count := p.artifactsPerNode
	if count == 0 {
		count = 1
	}
	artifactType, content := "recommendation", map[string]any{"recommendation": "keep the bounded workflow"}
	if invocation.Node.Id == "review" {
		artifactType, content = "review-decision", map[string]any{"decision": "approve"}
	}
	artifacts := make([]contractsv1.ActionArtifact, 0, count)
	for index := 0; index < count; index++ {
		hash, err := workflow.Digest(content)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, contractsv1.ActionArtifact{Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: fmtID(artifactType, index), ArtifactType: contractsv1.Identifier(artifactType), JobId: invocation.JobID, CampaignId: invocation.CampaignID, WorkflowRef: invocation.WorkflowRef, NodeId: invocation.Node.Id, InputHashes: invocation.InputHashes, Content: content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending})
	}
	p.results[invocation.IdempotencyKey] = workflow.ProviderResult{IdempotencyKey: invocation.IdempotencyKey, CompletedAt: time.Now().UTC(), Artifacts: artifacts}
	return nil
}

func (p *dagProvider) Poll(_ context.Context, key string) (workflow.ProviderResult, bool, error) {
	result, ok := p.results[key]
	return result, ok, nil
}

func (*dagProvider) Cancel(context.Context, string) error { return nil }

type runtimePendingProvider struct {
	starts int
	keys   map[string]struct{}
}

func (p *runtimePendingProvider) Start(_ context.Context, invocation workflow.Invocation) error {
	if _, ok := p.keys[invocation.IdempotencyKey]; !ok {
		p.keys[invocation.IdempotencyKey] = struct{}{}
		p.starts++
	}
	return nil
}

func (*runtimePendingProvider) Poll(context.Context, string) (workflow.ProviderResult, bool, error) {
	return workflow.ProviderResult{}, false, nil
}

func (*runtimePendingProvider) Cancel(context.Context, string) error { return nil }

func dagOutputCatalog() workflow.OutputCatalog {
	return workflow.OutputCatalog{
		"recommendation@1": func(value any) error {
			object, ok := value.(map[string]any)
			if !ok || object["recommendation"] == "" {
				return errors.New("recommendation is required")
			}
			return nil
		},
		"review-decision@1": func(value any) error {
			object, ok := value.(map[string]any)
			if !ok || object["decision"] == "" {
				return errors.New("decision is required")
			}
			return nil
		},
	}
}

func fmtID(prefix string, index int) string {
	if index == 0 {
		return prefix + "-artifact"
	}
	return prefix + "-artifact-" + string(rune('a'+index))
}
