package workflow

import (
	"strings"
	"testing"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestCompilerRejectsCoreOwnedActionOutputs(t *testing.T) {
	kind := contractsv1.SlotArtifactKindActionArtifact
	schema := contractsv1.WorkflowRef("result@1")
	definition := contractsv1.WorkflowDefinition{Nodes: []contractsv1.NodeDefinition{{
		Id: "core", Kind: contractsv1.NodeDefinitionKindDeterministic,
		OutputSlots: []contractsv1.Slot{{Id: "result", ArtifactType: "result", ArtifactKind: &kind, ContentSchema: &schema, Consumers: []string{"workflow-output"}, MaxItems: 1}},
	}}}
	if err := validateSlotFlow(definition); err == nil || !strings.Contains(err.Error(), "core-owned") {
		t.Fatalf("core-owned output survived compile: %v", err)
	}
}

func TestCandidateSlotRejectsBatchedRecordsInOneArtifact(t *testing.T) {
	content := []any{map[string]any{"candidate": "a"}, map[string]any{"candidate": "b"}}
	hash, err := Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	kind := contractsv1.SlotArtifactKindActionArtifact
	schema := contractsv1.WorkflowRef("candidate@1")
	slot := contractsv1.Slot{ArtifactType: "candidate", MinItems: 1, MaxItems: 2, ArtifactKind: &kind, ContentSchema: &schema, CountsAsCandidates: true}
	invocation := Invocation{
		JobID: "job-a", CampaignID: "campaign-a", WorkflowRef: "workflow-a@1",
		Node:        contractsv1.NodeDefinition{Id: "select", OutputSlots: []contractsv1.Slot{slot}},
		InputHashes: []contractsv1.SHA256{repeatedSHA('6')},
		Budget:      contractsv1.Budget{MaxAttempts: 1, MaxActions: 2, MaxCandidates: 2}, BudgetEnforced: true,
	}
	artifact := contractsv1.ActionArtifact{
		Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "candidate-a", ArtifactType: "candidate",
		JobId: invocation.JobID, CampaignId: invocation.CampaignID, WorkflowRef: invocation.WorkflowRef, NodeId: invocation.Node.Id,
		InputHashes: invocation.InputHashes, Content: content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending,
	}
	err = validateArtifacts([]contractsv1.ActionArtifact{artifact}, invocation, OutputCatalog{"candidate@1": func(any) error { return nil }}, true)
	if err == nil || !strings.Contains(err.Error(), "one Action Artifact per candidate") {
		t.Fatalf("batched candidates crossed the budget boundary: %v", err)
	}
}

func TestInvocationRejectsUnapprovedArtifactInput(t *testing.T) {
	content := map[string]any{"candidate": "a"}
	hash, err := Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []contractsv1.ActionArtifactApprovalState{contractsv1.ActionArtifactApprovalStatePending, contractsv1.ActionArtifactApprovalStateRejected, contractsv1.ActionArtifactApprovalStateStale} {
		artifact := contractsv1.ActionArtifact{
			Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "candidate-a", ArtifactType: "candidate",
			JobId: "job-a", CampaignId: "campaign-a", WorkflowRef: "source@1", NodeId: "research",
			InputHashes: []contractsv1.SHA256{repeatedSHA('6')}, Content: content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: state,
		}
		invocation := Invocation{JobID: "job-a", CampaignID: "campaign-a", Inputs: []contractsv1.ActionArtifact{artifact}, InputHashes: []contractsv1.SHA256{repeatedSHA('0'), repeatedSHA('1'), repeatedSHA('2'), repeatedSHA('3'), repeatedSHA('4'), repeatedSHA('5'), contractsv1.SHA256(hash)}}
		if err := verifyInvocationInputBinding(invocation); err == nil || !strings.Contains(err.Error(), "authority") {
			t.Fatalf("%s artifact crossed the invocation boundary: %v", state, err)
		}
	}
}

func TestProviderCannotAssertArtifactAuthority(t *testing.T) {
	result := ProviderResult{Artifacts: []contractsv1.ActionArtifact{{ApprovalState: contractsv1.ActionArtifactApprovalStateApproved}}}
	normalized := normalizeProviderArtifactAuthority(result)
	if normalized.Artifacts[0].ApprovalState != contractsv1.ActionArtifactApprovalStatePending {
		t.Fatalf("provider authority survived normalization: %s", normalized.Artifacts[0].ApprovalState)
	}
}

func TestInvocationRejectsArtifactFromNonEarlierWorkflow(t *testing.T) {
	invocation := Invocation{
		WorkflowRef: "current@1",
		Inputs:      []contractsv1.ActionArtifact{{WorkflowRef: "later@1"}},
	}
	campaign := contractsv1.CampaignDefinition{WorkflowPlan: []contractsv1.WorkflowRef{"source@1", "current@1", "later@1"}}
	if err := validateInvocationProducerScope(invocation, campaign); err == nil || !strings.Contains(err.Error(), "earlier pinned") {
		t.Fatalf("later Workflow artifact crossed the Replay boundary: %v", err)
	}
}
