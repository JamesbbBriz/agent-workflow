package workflow

import (
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestLegacyApprovalReceiptRemainsConsumable(t *testing.T) {
	at := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	content := map[string]any{"recommendation": "keep the bounded workflow"}
	contentHash, err := Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	action := contractsv1.ActionArtifact{Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "action-1", ArtifactType: "recommendation", JobId: "job-a", CampaignId: "campaign-a", WorkflowRef: "review@1", NodeId: "research", InputHashes: []contractsv1.SHA256{contractsv1.SHA256(contentHash)}, Content: content, ContentSha256: contractsv1.SHA256(contentHash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending}
	result, err := sealReceipt("run-1", 1, contractsv1.ReceiptReceiptTypeResult, at, nil, nil, []contractsv1.SHA256{action.ContentSha256}, map[string]any{"accepted": true})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := sealReceipt("run-1", 2, contractsv1.ReceiptReceiptTypeTerminal, at, &result.ReceiptHash, nil, nil, map[string]any{"state": "node_completed"})
	if err != nil {
		t.Fatal(err)
	}
	sourceLedger := NewMemoryLedger()
	for _, receipt := range []contractsv1.Receipt{result, terminal} {
		if err := sourceLedger.Append(receipt); err != nil {
			t.Fatal(err)
		}
	}
	source, err := sourceLedger.Replay("run-1")
	if err != nil {
		t.Fatal(err)
	}
	actionHash, _ := Digest(action)
	brief := contractsv1.ApprovalBrief{Kind: contractsv1.ApprovalBriefKindApprovalBrief, SchemaVersion: 1, Id: contractsv1.Identifier(shortID("approval-", actionHash)), Title: "Approve exact action?", Evidence: []contractsv1.ArtifactRef{{Id: result.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "result", SchemaVersion: 1, Sha256: result.ReceiptHash, MediaType: "application/json"}}, Options: []contractsv1.ApprovalOption{{Id: "approve", Label: "Approve", Decision: contractsv1.ApprovalOptionDecisionApprove, Tradeoffs: []string{"accept exact action"}}, {Id: "reject", Label: "Reject", Decision: contractsv1.ApprovalOptionDecisionReject, Tradeoffs: []string{"do not apply action"}}}, RecommendedOptionId: "approve", Recommendation: "Approve.", Risks: []string{"external change"}, Action: action}
	briefHash, _ := Digest(brief)
	expiresAt := terminal.OccurredAt.Add(30 * 24 * time.Hour)
	preview := contractsv1.ApprovalPreview{Kind: contractsv1.ApprovalPreviewKindApprovalPreview, SchemaVersion: 1, Actor: "legacy@example.com", BaseRevision: 0, SourceAggregateId: source.AggregateId, Brief: brief, BriefHash: contractsv1.SHA256(briefHash), ExpiresAt: &expiresAt}
	previewHash, _ := Digest(preview)
	approval, err := sealActorReceipt(approvalAggregate(string(brief.Id)), 1, contractsv1.ReceiptReceiptTypeApproval, "legacy@example.com", at.Add(time.Minute), nil, []contractsv1.SHA256{contractsv1.SHA256(briefHash), action.ContentSha256}, []contractsv1.SHA256{contractsv1.SHA256(previewHash)}, map[string]any{"brief": brief, "preview_hash": contractsv1.SHA256(previewHash), "selected_option_id": "approve"})
	if err != nil {
		t.Fatal(err)
	}
	approvalLedger := NewMemoryLedger()
	if err := approvalLedger.Append(approval); err != nil {
		t.Fatal(err)
	}
	replay, _ := approvalLedger.Replay(approval.AggregateId)
	policy := contractsv1.Identifier("human-confirm")
	decision, _, err := verifiedApprovalDecision(replay, source, action, result, &policy, map[string]map[string]bool{"human-confirm": {"legacy@example.com": true}})
	if err != nil || decision != contractsv1.ApprovalOptionDecisionApprove {
		t.Fatalf("legacy approval receipt was not consumed: decision=%q err=%v", decision, err)
	}
	invalidBrief := brief
	invalidBrief.Options = invalidBrief.Options[:1]
	invalidBriefHash, _ := Digest(invalidBrief)
	invalidPreview := preview
	invalidPreview.Brief = invalidBrief
	invalidPreview.BriefHash = contractsv1.SHA256(invalidBriefHash)
	invalidPreviewHash, _ := Digest(invalidPreview)
	invalidApproval, _ := sealActorReceipt(approvalAggregate(string(invalidBrief.Id)), 1, contractsv1.ReceiptReceiptTypeApproval, "legacy@example.com", at.Add(time.Minute), nil, []contractsv1.SHA256{contractsv1.SHA256(invalidBriefHash), action.ContentSha256}, []contractsv1.SHA256{contractsv1.SHA256(invalidPreviewHash)}, map[string]any{"brief": invalidBrief, "preview_hash": contractsv1.SHA256(invalidPreviewHash), "selected_option_id": "approve"})
	invalidLedger := NewMemoryLedger()
	if err := invalidLedger.Append(invalidApproval); err != nil {
		t.Fatal(err)
	}
	invalidReplay, _ := invalidLedger.Replay(invalidApproval.AggregateId)
	if _, _, err := verifiedApprovalDecision(invalidReplay, source, action, result, &policy, map[string]map[string]bool{"human-confirm": {"legacy@example.com": true}}); err == nil {
		t.Fatal("legacy adapter accepted an ApprovalBrief the old Core could not produce")
	}
}
