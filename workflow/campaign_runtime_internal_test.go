package workflow

import (
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestCampaignReducerRejectsAttemptBeforeDependencies(t *testing.T) {
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
		request: CampaignRunRequest{}, initial: state,
		workflows: []preparedWorkflow{{
			compiled: CompiledWorkflow{WorkflowRef: workflowRef, DefinitionHash: definitionHash, CompileHash: compileHash, Nodes: []CompiledNode{
				{Definition: contractsv1.NodeDefinition{Id: "dependent", DependsOn: []string{"root"}}},
				{Definition: contractsv1.NodeDefinition{Id: "root"}},
			}},
			admission: contractsv1.WorkflowAdmission{Receipt: contractsv1.Receipt{ReceiptHash: admissionHash}},
		}},
	}
	ledger := NewMemoryLedger()
	engine := &Engine{ledger: ledger}
	if err := engine.admitCampaign(prepared, state); err != nil {
		t.Fatal(err)
	}
	replay, err := ledger.Replay(state.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeAttemptReserved, at.Add(time.Second), []contractsv1.SHA256{campaignHash}, nil, map[string]any{"workflow_ref": workflowRef, "node_id": "dependent", "started_at": at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	replay, err = ledger.Replay(state.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.reduceCampaignReplay(replay, prepared); err == nil || !strings.Contains(err.Error(), "not eligible") {
		t.Fatalf("dependent attempt was accepted before its root: %v", err)
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
