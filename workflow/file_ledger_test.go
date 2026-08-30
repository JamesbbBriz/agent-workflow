package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
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

func TestFileLedgerRejectsSymlinkWithoutChangingItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "receipts.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileLedger(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked ledger was accepted: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "keep-me" {
		t.Fatalf("symlink target changed: body=%q err=%v", body, err)
	}
}

func TestFileLedgerReopensExactLimitReceipt(t *testing.T) {
	at := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	receipt, err := sealReceipt("aggregate-limit", 1, contractsv1.ReceiptReceiptTypeCompile, at, nil, nil, nil, map[string]any{"padding": ""})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = sealReceipt("aggregate-limit", 1, contractsv1.ReceiptReceiptTypeCompile, at, nil, nil, nil, map[string]any{"padding": strings.Repeat("x", contract.MaxDocumentBytes-len(body))})
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(receipt)
	if err != nil || len(body) != contract.MaxDocumentBytes {
		t.Fatalf("receipt bytes=%d err=%v", len(body), err)
	}
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	ledger, err := OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileLedger(path); err != nil {
		t.Fatalf("exact-limit receipt could not be reopened: %v", err)
	}
}

func TestFileLedgerSerializesConcurrentCoreWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	left, err := OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	right, err := OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	first, err := sealReceipt("aggregate-a", 1, contractsv1.ReceiptReceiptTypeCompile, at, nil, nil, nil, map[string]any{"writer": "first"})
	if err != nil {
		t.Fatal(err)
	}
	if err := left.Append(first); err != nil {
		t.Fatal(err)
	}
	secondA, err := sealReceipt("aggregate-a", 2, contractsv1.ReceiptReceiptTypeTerminal, at.Add(time.Second), &first.ReceiptHash, nil, nil, map[string]any{"state": "a"})
	if err != nil {
		t.Fatal(err)
	}
	secondB, err := sealReceipt("aggregate-a", 2, contractsv1.ReceiptReceiptTypeTerminal, at.Add(time.Second), &first.ReceiptHash, nil, nil, map[string]any{"state": "b"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, writer := range []struct {
		ledger  *FileLedger
		receipt contractsv1.Receipt
	}{{left, secondA}, {right, secondB}} {
		go func() {
			ready.Done()
			<-start
			errs <- writer.ledger.Append(writer.receipt)
		}()
	}
	ready.Wait()
	close(start)
	successes := 0
	for range 2 {
		if err := <-errs; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent writers committed %d competing heads", successes)
	}
	replay, err := left.Replay("aggregate-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Receipts) != 2 {
		t.Fatalf("ledger was corrupted by concurrent writers: %+v", replay)
	}
}

func TestFileLedgerListsMixedReceiptFamiliesWithoutCrossingAggregates(t *testing.T) {
	ledger, err := OpenFileLedger(filepath.Join(t.TempDir(), "receipts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	first, err := sealReceipt("execution-a", 1, contractsv1.ReceiptReceiptTypeCompile, at, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealReceipt("execution-a", 2, contractsv1.ReceiptReceiptTypeTerminal, at.Add(time.Second), &first.ReceiptHash, nil, nil, map[string]any{"state": "node_completed"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := sealReceipt("other-a", 1, contractsv1.ReceiptReceiptTypePackEdition, at, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range []contractsv1.Receipt{first, second, other} {
		if err := ledger.Append(receipt); err != nil {
			t.Fatal(err)
		}
	}
	replays, err := ledger.ReplaysByReceiptTypes(contractsv1.ReceiptReceiptTypeCompile, contractsv1.ReceiptReceiptTypeTerminal)
	if err != nil {
		t.Fatal(err)
	}
	if len(replays) != 1 || replays[0].AggregateId != "execution-a" || len(replays[0].Receipts) != 2 {
		t.Fatalf("mixed receipt family readback=%+v", replays)
	}
}

func TestFileLedgerRejectsZeroVersionWithoutPanicking(t *testing.T) {
	ledger, err := OpenFileLedger(filepath.Join(t.TempDir(), "receipts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(contractsv1.Receipt{}); err == nil {
		t.Fatal("zero-version receipt was accepted")
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
