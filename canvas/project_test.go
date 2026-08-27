package canvas

import (
	"testing"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
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
