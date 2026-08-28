package canvas_test

import (
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/canvas"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestChangeCaseCanvasProjectsOnlyCanonicalStateAndReceipts(t *testing.T) {
	zero := contractsv1.SHA256("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	resource := contractsv1.ResourceRef{Kind: "resource_ref", SchemaVersion: 2, ResourceType: "document", ResourceId: "doc-1", Generation: 1, BaselineRevision: "revision-1", BaselineHash: zero}
	resourceHash, err := workflow.Digest(resource)
	if err != nil {
		t.Fatal(err)
	}
	caseID := "change-case-" + resourceHash[len("sha256:"):len("sha256:")+20]
	change := map[string]any{"value": "a"}
	changeHash, err := workflow.Digest(change)
	if err != nil {
		t.Fatal(err)
	}
	proposal := contractsv1.ChangeProposal{Kind: "change_proposal", SchemaVersion: 2, CaseId: caseID, Resource: resource, JobId: "job-a", CampaignId: "campaign-a", WorkflowRef: "workflow-a@1", NodeId: "node-a", SourceCampaignAggregateId: "campaign-run-a", SourceCampaignReplayHash: zero, SourceResultAggregateId: "node-run-a", SourceResultReplayHash: zero, ArtifactId: "artifact-a", Change: change, ChangeHash: contractsv1.SHA256(changeHash), CapabilityHash: zero, EvidenceHashes: []contractsv1.SHA256{zero}}
	proposalHash, err := workflow.Digest(proposal)
	if err != nil {
		t.Fatal(err)
	}
	proposal.Id, proposal.ProposalHash = contractsv1.Identifier("proposal-"+proposalHash[len("sha256:"):len("sha256:")+20]), contractsv1.SHA256(proposalHash)
	state := contractsv1.ChangeCaseState{Kind: "change_case_state", SchemaVersion: 2, Id: caseID, Resource: resource, Status: contractsv1.ChangeCaseStateStatusProposed, Proposals: []contractsv1.ChangeProposal{proposal}, UpdatedAt: time.Now().UTC()}
	receipt := contractsv1.Receipt{Kind: contractsv1.ReceiptKindReceipt, SchemaVersion: 4, Id: "receipt-a", ReceiptType: contractsv1.ReceiptReceiptTypeChangeProposed, AggregateId: state.Id, AggregateVersion: 1, OccurredAt: state.UpdatedAt, InputHashes: []contractsv1.SHA256{zero}, OutputHashes: []contractsv1.SHA256{zero}, PreviousReceiptHash: nil, Payload: map[string]any{"state": state}}
	hash, err := workflow.Digest(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.ReceiptHash = contractsv1.SHA256(hash)
	replay := contractsv1.ReplayBundle{Kind: contractsv1.ReplayBundleKindReplayBundle, SchemaVersion: 1, AggregateId: state.Id, CutoffReceiptHash: receipt.ReceiptHash, Receipts: []contractsv1.Receipt{receipt}}
	bundleHash, err := workflow.Digest(replay)
	if err != nil {
		t.Fatal(err)
	}
	replay.BundleHash = contractsv1.SHA256(bundleHash)

	projected, err := canvas.ProjectChangeCase(replay, state.UpdatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if projected.State.Id != state.Id || len(projected.State.Proposals) != 1 || len(projected.Receipts) != 1 || projected.Receipts[0].ReceiptHash != receipt.ReceiptHash {
		t.Fatalf("Canvas did not preserve canonical Change Case state: %+v", projected)
	}
	replay.Receipts[0].Payload["invented"] = true
	if _, err := canvas.ProjectChangeCase(replay, time.Now()); err == nil {
		t.Fatal("Canvas accepted a tampered Replay")
	}
}
