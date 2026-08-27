package canvas

import (
	"testing"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestTerminalBlockerUsesOnlyCanonicalTerminalReceipt(t *testing.T) {
	replay := contractsv1.ReplayBundle{Receipts: []contractsv1.Receipt{{
		ReceiptType: contractsv1.ReceiptReceiptTypeTerminal,
		Payload:     contractsv1.ReceiptPayload{"state": "deadline_expired"},
	}}}
	code, message := terminalBlocker(replay)
	if code == nil || *code != "deadline-expired" || message == nil || *message == "" {
		t.Fatalf("terminal receipt was not projected as a blocker: %v %v", code, message)
	}
}

func TestContextPortsBindDuplicatePackTypesByRequirement(t *testing.T) {
	requirementA := contractsv1.Identifier("brief-a")
	requirementB := contractsv1.Identifier("brief-b")
	invocation := workflow.Invocation{
		Bundle: contractsv1.ContextBundle{Entries: []contractsv1.ContextPackRef{
			{Id: "edition-a", RequirementId: &requirementA},
			{Id: "edition-b", RequirementId: &requirementB},
		}},
		Context: []contractsv1.ContextPackEdition{
			{Id: "edition-a", PackType: "project-brief", PackSchemaVersion: 1},
			{Id: "edition-b", PackType: "project-brief", PackSchemaVersion: 1},
		},
	}
	edition, _, err := matchingEdition(contractsv1.ContextRequirement{Id: requirementB, PackType: "project-brief", SchemaVersion: 1}, invocation)
	if err != nil {
		t.Fatal(err)
	}
	if edition == nil || edition.Id != "edition-b" {
		t.Fatalf("requirement brief-b resolved to the wrong edition: %+v", edition)
	}
}
