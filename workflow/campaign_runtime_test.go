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
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger).
		WithApprovalAuthorities(workflow.ApprovalAuthorityCatalog{"human-confirm": []string{"human@example.com"}})

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
	policy := contractsv1.Identifier("human-confirm")
	brief := contractsv1.ApprovalBrief{Kind: contractsv1.ApprovalBriefKindApprovalBrief, SchemaVersion: 1, Title: "Accept the reviewed result?", Evidence: []contractsv1.ArtifactRef{{Id: result.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "result", SchemaVersion: 1, Sha256: result.ReceiptHash, MediaType: "application/json"}}, Options: []contractsv1.ApprovalOption{{Id: "approve", Label: "Approve", Decision: contractsv1.ApprovalOptionDecisionApprove, Tradeoffs: []string{"Accepts the exact reviewed result"}}, {Id: "reject", Label: "Reject", Decision: contractsv1.ApprovalOptionDecisionReject, Tradeoffs: []string{"Leaves the result unapproved"}}}, RecommendedOptionId: "approve", Recommendation: "Approve the exact result.", Risks: []string{"Changes are separately capability-gated."}, Action: material.Artifacts[0], ApprovalPolicy: &policy}
	core := workflow.NewAuthoringCore(registry, workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent}, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), []string{"provider-timeout"}, []string{"human-confirm"}, ledger).
		WithApprovalAuthorities(workflow.ApprovalAuthorityCatalog{"human-confirm": []string{"human@example.com"}})
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

func TestCampaignRuntimeWaitsForExactHumanApprovalAndResumes(t *testing.T) {
	t.Parallel()
	cutoff := time.Now().UTC().Add(-time.Hour)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	definition := loadExample(t)
	job := jobFixture(scope)
	campaign := campaignFixture(scope, cutoff)
	campaign.Budget = contractsv1.Budget{MaxAttempts: 2, MaxActions: 3, MaxCandidates: 4}
	ledger := workflow.NewMemoryLedger()
	admit(t, ledger, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
	provider := &dagProvider{results: map[string]workflow.ProviderResult{}}
	outputs := dagOutputCatalog()
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputs, provider, ledger).
		WithApprovalAuthorities(workflow.ApprovalAuthorityCatalog{"human-confirm": []string{"human@example.com"}})

	waiting, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}, MaxTransitions: 10})
	if err != nil {
		t.Fatal(err)
	}
	if waiting.State.Nodes[1].Status != contractsv1.CampaignNodeExecutionStatusAwaitingApproval || waiting.State.Nodes[1].ApprovalId == nil || provider.starts != 1 || waiting.NodeReplay == nil {
		t.Fatalf("approval Node did not stop at the human gate: state=%+v starts=%d", waiting.State, provider.starts)
	}
	material, err := workflow.MaterializeReplay(*waiting.NodeReplay, outputs)
	if err != nil {
		t.Fatal(err)
	}
	var result contractsv1.Receipt
	for _, receipt := range waiting.NodeReplay.Receipts {
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeResult {
			result = receipt
		}
	}
	policy := contractsv1.Identifier("human-confirm")
	brief := contractsv1.ApprovalBrief{Kind: contractsv1.ApprovalBriefKindApprovalBrief, SchemaVersion: 1, Title: "Accept the recommendation?", Evidence: []contractsv1.ArtifactRef{{Id: result.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "result", SchemaVersion: 1, Sha256: result.ReceiptHash, MediaType: "application/json"}}, Options: []contractsv1.ApprovalOption{{Id: "approve", Label: "Approve", Decision: contractsv1.ApprovalOptionDecisionApprove, Tradeoffs: []string{"Accept exact result"}}, {Id: "reject", Label: "Reject", Decision: contractsv1.ApprovalOptionDecisionReject, Tradeoffs: []string{"Do not proceed"}}, {Id: "revise", Label: "Revise", Decision: contractsv1.ApprovalOptionDecisionRevise, Tradeoffs: []string{"Request another revision"}}}, RecommendedOptionId: "approve", Recommendation: "Approve the exact result.", Risks: []string{"Human remains authoritative."}, Action: material.Artifacts[0], ApprovalPolicy: &policy}
	core := workflow.NewAuthoringCore(registry, workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent, "human-approval@1": contractsv1.NodeDefinitionKindApproval}, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputs, []string{"provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, ledger).
		WithApprovalAuthorities(workflow.ApprovalAuthorityCatalog{"human-confirm": []string{"human@example.com"}})
	preview, err := core.PreviewApproval(brief, "human@example.com", waiting.NodeReplay.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.ConfirmApproval(preview, "human@example.com", "approve", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	completed, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}, MaxTransitions: 10})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State.Status != contractsv1.CampaignExecutionStateStatusCompleted || completed.State.Nodes[1].Status != contractsv1.CampaignNodeExecutionStatusCompleted || provider.starts != 1 {
		t.Fatalf("approval did not resume the same Campaign without another provider effect: state=%+v starts=%d", completed.State, provider.starts)
	}
}

func TestCampaignRuntimeResumesTimeAndSignalWaitsExactlyOnce(t *testing.T) {
	for _, mode := range []contractsv1.NodeDefinitionWaitMode{contractsv1.NodeDefinitionWaitModeTime, contractsv1.NodeDefinitionWaitModeSignal} {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			cutoff := time.Now().UTC().Add(-time.Hour)
			scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
			registry, err := workflow.NewRegistry(workflow.NewIntentProducer())
			if err != nil {
				t.Fatal(err)
			}
			definition := loadExample(t)
			wait := definition.Nodes[0]
			wait.Id, wait.Kind, wait.Executor = "wait", contractsv1.NodeDefinitionKindWait, "core-wait@1"
			wait.Context, wait.Capabilities, wait.InputSlots, wait.OutputSlots, wait.DependsOn = []contractsv1.ContextRequirement{}, []string{}, []contractsv1.Slot{}, []contractsv1.Slot{}, []string{}
			wait.WaitMode = &mode
			if mode == contractsv1.NodeDefinitionWaitModeTime {
				delay := 1
				wait.WaitDelaySeconds = &delay
			} else {
				signal := contractsv1.Identifier("human-ready")
				wait.WaitSignal = &signal
			}
			terminal := wait
			terminal.Id, terminal.Kind, terminal.Executor = "finish", contractsv1.NodeDefinitionKindTerminal, "core-terminal@1"
			terminal.DependsOn = []string{"wait"}
			terminal.WaitMode, terminal.WaitDelaySeconds, terminal.WaitSignal = nil, nil, nil
			definition.Nodes, definition.DefaultContext, definition.Outputs = []contractsv1.NodeDefinition{wait, terminal}, []contractsv1.ContextRequirement{}, []contractsv1.Slot{}
			job := jobFixture(scope)
			campaign := campaignFixture(scope, cutoff)
			ledger := workflow.NewMemoryLedger()
			core := workflow.NewAuthoringCore(registry, workflow.ExecutorCatalog{"core-wait@1": contractsv1.NodeDefinitionKindWait, "core-terminal@1": contractsv1.NodeDefinitionKindTerminal}, nil, nil, []string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, nil, ledger)
			preview, lint, err := core.Preview(job, campaign, definition, "operator")
			if err != nil {
				t.Fatalf("%v: %+v", err, lint.Issues)
			}
			if _, err := core.Confirm(preview, "operator", cutoff); err != nil {
				t.Fatal(err)
			}
			provider := &dagProvider{results: map[string]workflow.ProviderResult{}}
			engine := workflow.NewEngine(registry, nil, nil, provider, ledger)
			request := workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}
			waiting, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: request, MaxTransitions: 10})
			if err != nil {
				t.Fatal(err)
			}
			if waiting.State.Nodes[0].Status != contractsv1.CampaignNodeExecutionStatusWaiting || waiting.Transitions != 1 || provider.starts != 0 {
				t.Fatalf("wait did not pause without provider work: %+v starts=%d", waiting, provider.starts)
			}
			command := workflow.CampaignDriveCommand{CampaignRunRequest: request, MaxTransitions: 10}
			if mode == contractsv1.NodeDefinitionWaitModeTime {
				time.Sleep(1100 * time.Millisecond)
			} else {
				wrong := workflow.CampaignSignal{Name: "wrong", PayloadHash: contractsv1.SHA256(zeroHash)}
				command.Signal = &wrong
				unchanged, err := engine.Drive(context.Background(), command)
				if err != nil || unchanged.Transitions != 0 {
					t.Fatalf("wrong signal resumed wait: receipt=%+v err=%v", unchanged, err)
				}
				right := workflow.CampaignSignal{Name: "human-ready", PayloadHash: contractsv1.SHA256(zeroHash)}
				command.Signal = &right
			}
			completed, err := engine.Drive(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			if completed.State.Status != contractsv1.CampaignExecutionStateStatusCompleted || completed.State.Nodes[0].Status != contractsv1.CampaignNodeExecutionStatusCompletedNoAction || provider.starts != 0 {
				t.Fatalf("wait did not resume exactly once: %+v starts=%d", completed, provider.starts)
			}
		})
	}
}

func TestCampaignRuntimeExecutesEveryPinnedWorkflowInOrder(t *testing.T) {
	t.Parallel()
	cutoff := time.Now().UTC().Add(-time.Hour)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	first := loadExample(t)
	first.Nodes = first.Nodes[:1]
	first.Nodes[0].OutputSlots[0].Consumers = []string{"workflow-output"}
	first.Outputs = append([]contractsv1.Slot(nil), first.Nodes[0].OutputSlots...)
	first.Outputs[0].Consumers = append([]string(nil), first.Intent.Consumers...)
	second := first
	second.Id = "follow-up-review"
	second.Intent.Title = "Follow-up review"
	job := jobFixture(scope)
	campaign := campaignFixture(scope, cutoff)
	campaign.WorkflowPlan = []contractsv1.WorkflowRef{"research-review@1", "follow-up-review@1"}
	campaign.Budget = contractsv1.Budget{MaxAttempts: 2, MaxActions: 2, MaxCandidates: 2}
	ledger := workflow.NewMemoryLedger()
	admit(t, ledger, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: first, NodeID: "research"})
	admit(t, ledger, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: second, NodeID: "research"})
	provider := &dagProvider{results: map[string]workflow.ProviderResult{}}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger)

	receipt, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflows: []contractsv1.WorkflowDefinition{first, second}}, MaxTransitions: 3})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State.Status != contractsv1.CampaignExecutionStateStatusCompleted || receipt.Transitions != 3 || provider.starts != 2 || len(receipt.State.Nodes) != 2 {
		t.Fatalf("Campaign did not execute both Workflows and close: state=%+v transitions=%d starts=%d", receipt.State, receipt.Transitions, provider.starts)
	}
	if receipt.State.Nodes[0].WorkflowRef != "research-review@1" || receipt.State.Nodes[1].WorkflowRef != "follow-up-review@1" {
		t.Fatalf("same-named Nodes lost their Workflow identity: %+v", receipt.State.Nodes)
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
	canonical := workflow.NewMemoryLedger()
	admit(t, canonical, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
	ledger := &failOnceLedger{Ledger: canonical, failType: contractsv1.ReceiptReceiptTypeTerminal}
	provider := &dagProvider{results: map[string]workflow.ProviderResult{}, artifactsPerNode: 2}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger)

	request := workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}}
	if _, err := engine.Drive(context.Background(), request); err == nil {
		t.Fatal("injected rejected-result terminal failure was ignored")
	}
	receipt, err := engine.Drive(context.Background(), request)
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

func TestCampaignRuntimeDoesNotResetDeadlineAfterReservationCrash(t *testing.T) {
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
	job := jobFixture(scope)
	campaign := campaignFixture(scope, cutoff)
	campaign.Budget.MaxDurationSeconds = &one
	canonical := workflow.NewMemoryLedger()
	admit(t, canonical, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
	ledger := &failOnceLedger{Ledger: canonical, failType: contractsv1.ReceiptReceiptTypeCompile}
	provider := &dagProvider{results: map[string]workflow.ProviderResult{}}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger)
	request := workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}}
	if _, err := engine.Drive(context.Background(), request); err == nil {
		t.Fatal("injected crash window was ignored")
	}
	time.Sleep(1100 * time.Millisecond)
	receipt, err := engine.Drive(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State.BlockerCode == nil || *receipt.State.BlockerCode != "duration-budget-exhausted" || provider.starts != 0 {
		t.Fatalf("reservation deadline reset after crash: state=%+v starts=%d", receipt.State, provider.starts)
	}
}

func TestRunNodeNeverAppendsToIncompleteLegacyV1History(t *testing.T) {
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
	definition.Outputs = append([]contractsv1.Slot(nil), definition.Nodes[0].OutputSlots...)
	definition.Outputs[0].Consumers = append([]string(nil), definition.Intent.Consumers...)
	job := jobFixture(scope)
	campaign := campaignFixture(scope, cutoff)
	source := workflow.NewMemoryLedger()
	admission := admit(t, source, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
	provider := &dagProvider{results: map[string]workflow.ProviderResult{}}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, source)
	driven, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for index, receipt := range driven.NodeReplay.Receipts {
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeInvocation {
			count = index + 1
			break
		}
	}
	partial, err := workflow.ReplayPrefix(*driven.NodeReplay, count)
	if err != nil {
		t.Fatal(err)
	}
	target := workflow.NewMemoryLedger()
	admissionReplay, err := source.Replay(admission.Receipt.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range append(admissionReplay.Receipts, partial.Receipts...) {
		if err := target.Append(receipt); err != nil {
			t.Fatal(err)
		}
	}
	restartedProvider := &dagProvider{results: map[string]workflow.ProviderResult{}}
	restarted := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), restartedProvider, target)
	request := workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"}
	if _, err := restarted.RunNode(context.Background(), request); err == nil || !strings.Contains(err.Error(), "legacy v1 execution is read-only") {
		t.Fatalf("incomplete v1 history was resumed: %v", err)
	}
	after, err := target.Replay(partial.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Receipts) != len(partial.Receipts) || restartedProvider.starts != 0 {
		t.Fatalf("legacy history changed: receipts=%d want=%d starts=%d", len(after.Receipts), len(partial.Receipts), restartedProvider.starts)
	}

	driveTarget := workflow.NewMemoryLedger()
	for _, receipt := range append(admissionReplay.Receipts, partial.Receipts...) {
		if err := driveTarget.Append(receipt); err != nil {
			t.Fatal(err)
		}
	}
	driveProvider := &dagProvider{results: map[string]workflow.ProviderResult{}}
	driveEngine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), driveProvider, driveTarget)
	if _, err := driveEngine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}}); err == nil || !strings.Contains(err.Error(), "legacy v1 execution is read-only") {
		t.Fatalf("Campaign Drive resumed incomplete v1 history: %v", err)
	}
	afterDrive, err := driveTarget.Replay(partial.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterDrive.Receipts) != len(partial.Receipts) || driveProvider.starts != 0 {
		t.Fatalf("Drive changed legacy history: receipts=%d want=%d starts=%d", len(afterDrive.Receipts), len(partial.Receipts), driveProvider.starts)
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
