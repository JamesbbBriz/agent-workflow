package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestOfflineConformanceReportIsDeterministicAndHashBound(t *testing.T) {
	fixture := loadConformanceFixture(t, "generic")
	first, err := workflow.RunConformance(context.Background(), fixture, "test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := workflow.RunConformance(context.Background(), fixture, "test")
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("fixed fixture produced different conformance reports")
	}
	for _, check := range first.Checks {
		if check.Status == contractsv1.ConformanceCheckStatusPass && len(check.EvidenceHashes) == 0 {
			t.Fatalf("passing check %s has no hash-bound evidence", check.Id)
		}
	}
}

func TestGenericConformanceRunsAnExplicitBundledProvider(t *testing.T) {
	fixture := loadConformanceFixture(t, "generic")
	provider := &fixtureProvider{results: map[string]workflow.ProviderResult{}}
	report, err := workflow.RunConformanceWithProvider(context.Background(), fixture, "test", "codex", provider)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range report.Checks {
		if check.Id == "provider-codex" && check.Status == contractsv1.ConformanceCheckStatusPass && len(check.EvidenceHashes) > 1 && provider.starts > 0 {
			return
		}
	}
	t.Fatalf("explicit provider was not proven by normalized receipts: %+v", report.Checks)
}

type fixtureProvider struct {
	starts  int
	results map[string]workflow.ProviderResult
}

func (p *fixtureProvider) Start(_ context.Context, invocation workflow.Invocation) error {
	if _, ok := p.results[invocation.IdempotencyKey]; ok {
		return nil
	}
	p.starts++
	artifacts := make([]contractsv1.ActionArtifact, 0, len(invocation.Node.OutputSlots))
	for _, slot := range invocation.Node.OutputSlots {
		content := map[string]any{"fixture": string(slot.Id)}
		hash, err := workflow.Digest(content)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, contractsv1.ActionArtifact{
			Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "artifact-" + string(slot.Id), ArtifactType: slot.ArtifactType,
			JobId: invocation.JobID, CampaignId: invocation.CampaignID, WorkflowRef: invocation.WorkflowRef, NodeId: invocation.Node.Id,
			InputHashes: invocation.InputHashes, Content: content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending,
		})
	}
	p.results[invocation.IdempotencyKey] = workflow.ProviderResult{IdempotencyKey: invocation.IdempotencyKey, CompletedAt: invocation.Deadline.Add(-time.Second), Artifacts: artifacts}
	return nil
}

func (p *fixtureProvider) Poll(_ context.Context, key string) (workflow.ProviderResult, bool, error) {
	result, ok := p.results[key]
	return result, ok, nil
}

func (*fixtureProvider) Cancel(context.Context, string) error { return nil }

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
