package workflow

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestFileLedgerPreservesExactJSONNumbers(t *testing.T) {
	receipt, err := sealReceipt(
		"aggregate-a", 1, contractsv1.ReceiptReceiptTypeCompile,
		time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC), nil,
		[]contractsv1.SHA256{}, []contractsv1.SHA256{},
		map[string]any{"large": json.Number("9007199254740993")},
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	ledger, err := OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(receipt); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reopened.Replay("aggregate-a")
	if err != nil {
		t.Fatal(err)
	}
	if replay.Receipts[0].Payload["large"] != json.Number("9007199254740993") {
		t.Fatalf("large JSON number changed: %#v", replay.Receipts[0].Payload["large"])
	}
}
