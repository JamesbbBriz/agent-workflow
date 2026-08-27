package workflow

import (
	"encoding/json"
	"os"
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

func TestFileLedgerRecoversOnlyAnUnterminatedTail(t *testing.T) {
	receipt, err := sealReceipt("aggregate-a", 1, contractsv1.ReceiptReceiptTypeCompile, time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC), nil, nil, nil, nil)
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
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"kind":`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Replay("aggregate-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(mustJSON(t, receipt), []byte("\n{bad}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileLedger(path); err == nil {
		t.Fatal("newline-terminated corruption was silently removed")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
