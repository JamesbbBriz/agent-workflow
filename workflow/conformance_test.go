package workflow_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestCommittedConformanceFixturesUseTheSameCoreRunner(t *testing.T) {
	for _, name := range []string{"generic", "seo-shaped"} {
		t.Run(name, func(t *testing.T) {
			fixture := loadConformanceFixture(t, name)
			report, err := workflow.RunConformance(context.Background(), fixture, "test")
			if err != nil || !report.Passed || report.FixtureSha256 == "" || report.ContractVersion != workflow.ConformanceContractVersion {
				t.Fatalf("fixture failed: report=%+v err=%v", report, err)
			}
			seen := map[string]bool{}
			for _, check := range report.Checks {
				seen[string(check.Id)] = true
			}
			for _, required := range []string{"definitions", "admission", "campaign-research-campaign", "provider-receipts-research-campaign", "context-research-campaign", "replay-research-campaign"} {
				if !seen[required] {
					t.Fatalf("report omitted %s", required)
				}
			}
			if name == "seo-shaped" && (!seen["context-recovery"] || !seen["change-case"] || !seen["campaign-comparison-campaign"]) {
				t.Fatalf("SEO-shaped report omitted closure checks: %+v", seen)
			}
		})
	}
}

func TestConformanceRejectsFixtureContractDrift(t *testing.T) {
	fixture := loadConformanceFixture(t, "generic")
	fixture.Campaigns[0].WorkflowPlan = []contractsv1.WorkflowRef{"missing@1"}
	if _, err := workflow.RunConformance(context.Background(), fixture, "test"); err == nil {
		t.Fatal("unknown pinned Workflow passed conformance")
	}
}

func loadConformanceFixture(t *testing.T, name string) contractsv1.ConformanceFixture {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "conformance", "fixtures", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture contractsv1.ConformanceFixture
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}
