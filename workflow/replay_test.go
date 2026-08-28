package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestReplayAtUsesExactCanonicalCutoffAndRedactsPrivateFields(t *testing.T) {
	ledger := NewMemoryLedger()
	aggregateID, err := campaignExecutionID("job-a", "campaign-a")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	first, err := sealActorReceipt(aggregateID, 1, contractsv1.ReceiptReceiptTypeCompile, "private-actor-canary", at, nil, nil, nil, map[string]any{"secret": "private-payload-canary"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealReceipt(aggregateID, 2, contractsv1.ReceiptReceiptTypeAdmission, at.Add(time.Second), &first.ReceiptHash, nil, nil, map[string]any{"approved": true})
	if err != nil {
		t.Fatal(err)
	}
	third, err := sealReceipt(aggregateID, 3, contractsv1.ReceiptReceiptTypeResult, at.Add(2*time.Second), &second.ReceiptHash, nil, nil, map[string]any{"future": "n-plus-one-canary"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.(AtomicLedger).AppendBatch([]contractsv1.Receipt{first, second, third}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{ledger: ledger}
	ref := CampaignRef{JobID: "job-a", CampaignID: "campaign-a"}
	raw, err := engine.ReplayAt(context.Background(), ref, ReceiptID(second.Id), ReplayViewRaw)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Raw == nil || len(raw.Raw.Receipts) != 2 || raw.Raw.CutoffReceiptHash != second.ReceiptHash {
		t.Fatalf("wrong exact prefix: %#v", raw.Raw)
	}
	raw.Raw.Receipts[0].Payload["secret"] = "caller-mutation"
	again, err := engine.ReplayAt(context.Background(), ref, ReceiptID(second.Id), ReplayViewRaw)
	if err != nil || again.Raw.Receipts[0].Payload["secret"] != "private-payload-canary" {
		t.Fatalf("raw Replay mutation escaped into canonical history: %#v err=%v", again.Raw, err)
	}
	redacted, err := engine.ReplayAt(context.Background(), ref, ReceiptID(second.Id), ReplayViewPublicMetadataV1)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(redacted.Redacted)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"private-actor-canary", "private-payload-canary", "n-plus-one-canary"} {
		if strings.Contains(string(body), canary) {
			t.Fatalf("redacted Replay leaked %q: %s", canary, body)
		}
	}
	if redacted.Redacted == nil || len(redacted.Redacted.Receipts) != 2 || redacted.Redacted.Receipts[1].ReceiptHash != second.ReceiptHash {
		t.Fatalf("redacted Replay lost canonical traceability: %#v", redacted.Redacted)
	}
	redacted.Redacted.Proof.ProofHash = contractsv1.SHA256("sha256:" + strings.Repeat("0", 64))
	if err := VerifyRedactedReplay(*redacted.Redacted, *raw.Raw); err == nil {
		t.Fatal("tampered redaction proof was accepted")
	}
}

func TestReplayAtRejectsUnknownCutoffAndPolicy(t *testing.T) {
	ledger := NewMemoryLedger()
	aggregateID, _ := campaignExecutionID("job-a", "campaign-a")
	receipt, err := sealReceipt(aggregateID, 1, contractsv1.ReceiptReceiptTypeCompile, time.Now().UTC(), nil, nil, nil, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(receipt); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{ledger: ledger}
	ref := CampaignRef{JobID: "job-a", CampaignID: "campaign-a"}
	if _, err := engine.ReplayAt(context.Background(), ref, "receipt-missing", ReplayViewRaw); err == nil {
		t.Fatal("unknown cutoff was accepted")
	}
	if _, err := engine.ReplayAt(context.Background(), ref, ReceiptID(receipt.Id), ReplayView("future-policy@9")); err == nil {
		t.Fatal("unknown Replay policy was accepted")
	}
}
