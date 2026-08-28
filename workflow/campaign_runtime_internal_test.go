package workflow

import (
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestCampaignReducerRejectsAttemptBeforeDependencies(t *testing.T) {
	engine, prepared, state, replay := campaignReducerFixture(t)
	at, workflowRef, campaignHash := state.StartedAt, prepared.workflows[0].compiled.WorkflowRef, state.CampaignHash
	if err := engine.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeAttemptReserved, at.Add(time.Second), []contractsv1.SHA256{campaignHash}, nil, map[string]any{"workflow_ref": workflowRef, "node_id": "dependent", "started_at": at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	replay, err := engine.ledger.Replay(state.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.reduceCampaignReplay(replay, prepared); err == nil || !strings.Contains(err.Error(), "not eligible") {
		t.Fatalf("dependent attempt was accepted before its root: %v", err)
	}
}

func TestCampaignReducerRejectsReceiptBeforeCanonicalTime(t *testing.T) {
	engine, prepared, state, replay := campaignReducerFixture(t)
	at, workflowRef := state.StartedAt.Add(-time.Second), prepared.workflows[0].compiled.WorkflowRef
	if err := engine.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeAttemptReserved, at, []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"workflow_ref": workflowRef, "node_id": "root", "started_at": at}); err != nil {
		t.Fatal(err)
	}
	replay, err := engine.ledger.Replay(state.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.reduceCampaignReplay(replay, prepared); err == nil || !strings.Contains(err.Error(), "predates canonical state") {
		t.Fatalf("pre-admission receipt was accepted: %v", err)
	}
}

func TestCampaignReducerRejectsReservationPastAttemptBudget(t *testing.T) {
	engine, prepared, state, replay := campaignReducerFixtureWithMutation(t, func(state *contractsv1.CampaignExecutionState, prepared *preparedCampaign) {
		state.Nodes[1].Status = contractsv1.CampaignNodeExecutionStatusCompleted
		state.Status = contractsv1.CampaignExecutionStateStatusRunning
		state.Usage.Attempts = 1
		prepared.request.Campaign.Budget.MaxAttempts = 1
	})
	at, workflowRef := state.StartedAt.Add(time.Second), prepared.workflows[0].compiled.WorkflowRef
	if err := engine.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeAttemptReserved, at, []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"workflow_ref": workflowRef, "node_id": "dependent", "started_at": at}); err != nil {
		t.Fatal(err)
	}
	replay, err := engine.ledger.Replay(state.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.reduceCampaignReplay(replay, prepared); err == nil || !strings.Contains(err.Error(), "exceeds the canonical budget") {
		t.Fatalf("over-budget reservation was accepted: %v", err)
	}
}

func TestCampaignReducerRejectsFabricatedBudgetExhaustion(t *testing.T) {
	engine, prepared, state, replay := campaignReducerFixture(t)
	at, workflowRef := state.StartedAt.Add(time.Second), prepared.workflows[0].compiled.WorkflowRef
	if err := engine.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeBudgetExhausted, at, []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"workflow_ref": workflowRef, "node_id": "root", "blocker_code": "action-budget-exhausted"}); err != nil {
		t.Fatal(err)
	}
	replay, err := engine.ledger.Replay(state.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.reduceCampaignReplay(replay, prepared); err == nil || !strings.Contains(err.Error(), "no canonical condition") {
		t.Fatalf("fabricated budget exhaustion was accepted: %v", err)
	}
}

func TestCampaignResultRechecksLegacyArtifactsAgainstCurrentBudget(t *testing.T) {
	slot := contractsv1.Slot{ArtifactType: "candidate", CountsAsCandidates: true}
	artifacts := []contractsv1.ActionArtifact{{ArtifactType: "candidate"}, {ArtifactType: "candidate"}}
	if blocker := campaignResultBudgetBlocker(artifacts, []contractsv1.Slot{slot}, contractsv1.Budget{MaxActions: 1, MaxCandidates: 2}); blocker != "action-budget-exhausted" {
		t.Fatalf("legacy actions bypassed current Campaign budget: %q", blocker)
	}
	if blocker := campaignResultBudgetBlocker(artifacts, []contractsv1.Slot{slot}, contractsv1.Budget{MaxActions: 2, MaxCandidates: 1}); blocker != "candidate-budget-exhausted" {
		t.Fatalf("legacy candidates bypassed current Campaign budget: %q", blocker)
	}
}

func TestCampaignCompletionRequiresChildTerminalReceipt(t *testing.T) {
	at := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	result, err := sealReceipt("child-a", 1, contractsv1.ReceiptReceiptTypeResult, at, nil, nil, nil, map[string]any{"state": "result"})
	if err != nil {
		t.Fatal(err)
	}
	partial, err := replayBundle("child-a", []contractsv1.Receipt{result})
	if err != nil {
		t.Fatal(err)
	}
	if nodeCompletedReplay(partial) {
		t.Fatal("result-only child Replay authorized parent completion")
	}
	terminal, err := sealReceipt("child-a", 2, contractsv1.ReceiptReceiptTypeTerminal, at.Add(time.Second), &result.ReceiptHash, nil, nil, map[string]any{"state": "node_completed"})
	if err != nil {
		t.Fatal(err)
	}
	complete, err := replayBundle("child-a", []contractsv1.Receipt{result, terminal})
	if err != nil {
		t.Fatal(err)
	}
	if !nodeCompletedReplay(complete) {
		t.Fatal("terminal child Replay was rejected")
	}
}

func repeatedSHA(value byte) contractsv1.SHA256 {
	return contractsv1.SHA256("sha256:" + strings.Repeat(string(value), 64))
}

func campaignReducerFixture(t *testing.T) (*Engine, preparedCampaign, contractsv1.CampaignExecutionState, contractsv1.ReplayBundle) {
	return campaignReducerFixtureWithMutation(t, nil)
}

func campaignReducerFixtureWithMutation(t *testing.T, mutate func(*contractsv1.CampaignExecutionState, *preparedCampaign)) (*Engine, preparedCampaign, contractsv1.CampaignExecutionState, contractsv1.ReplayBundle) {
	t.Helper()
	at := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	workflowRef := contractsv1.WorkflowRef("ordered-workflow@1")
	jobHash, campaignHash := repeatedSHA('1'), repeatedSHA('2')
	definitionHash, compileHash, admissionHash := repeatedSHA('3'), repeatedSHA('4'), repeatedSHA('5')
	state := contractsv1.CampaignExecutionState{
		Kind: contractsv1.CampaignExecutionStateKindCampaignExecutionState, SchemaVersion: 2,
		AggregateId: "campaign-run-a", JobId: "job-a", CampaignId: "campaign-a", JobHash: jobHash, CampaignHash: campaignHash,
		WorkflowHashes: contractsv1.CampaignExecutionStateWorkflowHashes{string(workflowRef): definitionHash}, Status: contractsv1.CampaignExecutionStateStatusAdmitted,
		Nodes: []contractsv1.CampaignNodeExecution{
			{WorkflowRef: workflowRef, NodeId: "dependent", Status: contractsv1.CampaignNodeExecutionStatusPending},
			{WorkflowRef: workflowRef, NodeId: "root", Status: contractsv1.CampaignNodeExecutionStatusPending},
		},
		StartedAt: at, UpdatedAt: at,
	}
	prepared := preparedCampaign{
		request: CampaignRunRequest{Campaign: contractsv1.CampaignDefinition{Budget: contractsv1.Budget{MaxAttempts: 2, MaxActions: 2, MaxCandidates: 2}}},
		workflows: []preparedWorkflow{{
			compiled: CompiledWorkflow{WorkflowRef: workflowRef, DefinitionHash: definitionHash, CompileHash: compileHash, Nodes: []CompiledNode{
				{Definition: contractsv1.NodeDefinition{Id: "dependent", DependsOn: []string{"root"}, Budget: contractsv1.Budget{MaxAttempts: 1, MaxActions: 1, MaxCandidates: 1}}},
				{Definition: contractsv1.NodeDefinition{Id: "root", Budget: contractsv1.Budget{MaxAttempts: 1, MaxActions: 1, MaxCandidates: 1}}},
			}},
			admission: contractsv1.WorkflowAdmission{Receipt: contractsv1.Receipt{ReceiptHash: admissionHash}},
		}},
	}
	if mutate != nil {
		mutate(&state, &prepared)
	}
	prepared.initial = state
	ledger := NewMemoryLedger()
	engine := &Engine{ledger: ledger}
	if err := engine.admitCampaign(prepared, state); err != nil {
		t.Fatal(err)
	}
	replay, err := ledger.Replay(state.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	return engine, prepared, state, replay
}
