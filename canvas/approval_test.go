package canvas_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/canvas"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestApplyApprovalProjectsOnlyCanonicalExactDecision(t *testing.T) {
	body, err := os.ReadFile("../web/public/canvas.response.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	snapshot := envelope.Data
	action := snapshot.Executions[0].Outputs[0]
	source := snapshot.Replays[0]
	var result contractsv1.Receipt
	for _, receipt := range source.Receipts {
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeResult {
			result = receipt
		}
	}
	brief := contractsv1.ApprovalBrief{Kind: contractsv1.ApprovalBriefKindApprovalBrief, SchemaVersion: 1, Title: "Approve exact action?", Evidence: []contractsv1.ArtifactRef{{Id: result.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "result", SchemaVersion: 1, Sha256: result.ReceiptHash, MediaType: "application/json"}}, Options: []contractsv1.ApprovalOption{{Id: "approve", Label: "Approve", Decision: contractsv1.ApprovalOptionDecisionApprove, Tradeoffs: []string{"Changes the target"}}, {Id: "reject", Label: "Reject", Decision: contractsv1.ApprovalOptionDecisionReject, Tradeoffs: []string{"No change"}}}, RecommendedOptionId: "approve", Recommendation: "Approve the reviewed action.", Risks: []string{"Public impact"}, Action: action}
	registry, _ := workflow.NewRegistry(workflow.NewIntentProducer())
	ledger := workflow.NewMemoryLedger()
	for _, receipt := range source.Receipts {
		if err := ledger.Append(receipt); err != nil {
			t.Fatal(err)
		}
	}
	core := workflow.NewAuthoringCore(registry, workflow.ExecutorCatalog{}, workflow.CapabilityCatalog{}, workflow.OutputCatalog{"recommendation@1": func(any) error { return nil }}, nil, nil, ledger)
	preview, err := core.PreviewApproval(brief, "reviewer@example.com", source.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.ConfirmApproval(preview, "reviewer@example.com", "approve", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	replay, err := core.ApprovalReplay(string(preview.Brief.Id))
	if err != nil {
		t.Fatal(err)
	}
	next, err := canvas.ApplyApproval(snapshot, replay)
	if err != nil {
		t.Fatal(err)
	}
	if next.Executions[0].Outputs[0].ApprovalState != contractsv1.ActionArtifactApprovalStateApproved || len(next.ApprovalReplays) != 1 {
		t.Fatalf("approval was not projected: %+v", next.Executions[0])
	}

	tampered := replay
	tampered.Receipts[0].Payload["selected_option_id"] = "reject"
	if _, err := canvas.ApplyApproval(snapshot, tampered); err == nil {
		t.Fatal("tampered approval Replay was accepted")
	}
}
