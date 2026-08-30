package workflow

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestEvidenceWindowReportSeparatesAvailableAndInvokedRoles(t *testing.T) {
	start := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	ledger := NewMemoryLedger()
	var previous *contractsv1.SHA256
	for index, receiptType := range []contractsv1.ReceiptReceiptType{
		contractsv1.ReceiptReceiptTypePackEdition,
		contractsv1.ReceiptReceiptTypeInvocation,
		contractsv1.ReceiptReceiptTypeApproval,
		contractsv1.ReceiptReceiptTypeTerminal,
	} {
		receipt, err := sealReceipt("synthetic-run", index+1, receiptType, start.Add(time.Duration(index)*time.Minute), previous, nil, nil, map[string]any{"fixture": true})
		if err != nil {
			t.Fatal(err)
		}
		if err := ledger.Append(receipt); err != nil {
			t.Fatal(err)
		}
		hash := receipt.ReceiptHash
		previous = &hash
	}
	replay, err := ledger.Replay("synthetic-run")
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildEvidenceWindowReport(DefaultAgentRoleCatalog(), []contractsv1.ReplayBundle{replay}, start, start.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.AvailableRoleIds) != 3 || len(report.InvokedRoleIds) != 2 {
		t.Fatalf("role distinction missing: available=%v invoked=%v", report.AvailableRoleIds, report.InvokedRoleIds)
	}
	if report.Counts.ContextRefreshes != 1 || report.Counts.AgentInvocations != 1 || report.Counts.Approvals != 1 || report.Counts.Effects != 0 || report.Counts.Readbacks != 0 || report.Counts.Outcomes != 1 || report.Counts.Receipts != 4 || report.Counts.Replays != 1 {
		t.Fatalf("unexpected counts: %+v", report.Counts)
	}
	markdown, err := RenderEvidenceWindowMarkdown(report)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := RenderEvidenceWindowMarkdown(report)
	if err != nil || markdown != repeated || !strings.Contains(markdown, "| Agent invocations | 1 |") || !strings.Contains(markdown, "synthetic-run:4") {
		t.Fatalf("Markdown is not deterministic or complete: err=%v\n%s", err, markdown)
	}
}

func TestEvidenceWindowFixtureMatchesMarkdownProjection(t *testing.T) {
	catalogBody, err := os.ReadFile("../examples/agent-role-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog contractsv1.AgentRoleCatalog
	if err := json.Unmarshal(catalogBody, &catalog); err != nil || !reflect.DeepEqual(catalog, DefaultAgentRoleCatalog()) {
		t.Fatalf("role catalog fixture drifted: err=%v catalog=%+v", err, catalog)
	}
	body, err := os.ReadFile("../examples/evidence-window-report.json")
	if err != nil {
		t.Fatal(err)
	}
	var report contractsv1.EvidenceWindowReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	markdown, err := RenderEvidenceWindowMarkdown(report)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../examples/evidence-window-report.md")
	if err != nil || markdown != string(want) {
		t.Fatalf("fixture projection drifted: err=%v\n%s", err, markdown)
	}
}

func TestEvidenceWindowReportRejectsInvalidWindowAndReplay(t *testing.T) {
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	if _, err := BuildEvidenceWindowReport(DefaultAgentRoleCatalog(), nil, now, now.Add(-time.Second)); err == nil {
		t.Fatal("backwards window was accepted")
	}
	if _, err := BuildEvidenceWindowReport(DefaultAgentRoleCatalog(), []contractsv1.ReplayBundle{{AggregateId: "bad"}}, now, now); err == nil {
		t.Fatal("invalid Replay was accepted")
	}
	ledger := NewMemoryLedger()
	receipt, err := sealReceipt("duplicate", 1, contractsv1.ReceiptReceiptTypeTerminal, now, nil, nil, nil, map[string]any{"fixture": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(receipt); err != nil {
		t.Fatal(err)
	}
	replay, err := ledger.Replay("duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildEvidenceWindowReport(DefaultAgentRoleCatalog(), []contractsv1.ReplayBundle{replay, replay}, now, now); err == nil {
		t.Fatal("duplicate Replay aggregate inflated the report")
	}
}
