package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/canvas"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestCanvasProjectionUsesOnlyCanonicalRuntimeReceipts(t *testing.T) {
	definition := loadExampleWorkflow(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	result, err := executeDemo(definition, cutoff)
	if err != nil {
		t.Fatal(err)
	}

	job, campaign := demoDefinitions(definition, cutoff)
	snapshot, err := canvas.ProjectWithAdmissions(job, campaign, []contractsv1.WorkflowDefinition{definition}, []contractsv1.ReplayBundle{result.AdmissionReplay}, canvas.ExecutionInput{
		Replay:  result.Replay,
		Outputs: workflow.OutputCatalog{"recommendation@1": validateRecommendation},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Executions) != 1 || snapshot.Executions[0].NodeId != "research" {
		t.Fatalf("expected only the canonically invoked node, got %+v", snapshot.Executions)
	}
	if snapshot.Executions[0].Status != "completed" || len(snapshot.Executions[0].ContextPorts) != 2 {
		t.Fatalf("unexpected execution projection: %+v", snapshot.Executions[0])
	}
	if len(snapshot.Definition.Workflows) != 1 || len(snapshot.Definition.Workflows[0].Nodes) != 2 || snapshot.Definition.CampaignState != "configured" {
		t.Fatalf("definition graph was not preserved: %+v", snapshot.Definition)
	}
	if snapshot.NextSafeAction.Kind != "none" || snapshot.NextSafeAction.NodeId != nil {
		t.Fatalf("Canvas fabricated a next action: %+v", snapshot.NextSafeAction)
	}
}

func TestCanvasCommandReturnsGeneratedSnapshot(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(current), "..", "..", "examples", "research-review.workflow.json")
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"canvas", "--file", path, "--at", "2026-08-27T00:00:00Z"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("canvas exited %d: %s", exit, stderr.String())
	}
	var response struct {
		OK   bool                       `json:"ok"`
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Data.Kind != "canvas_snapshot" || len(response.Data.Executions) != 1 {
		t.Fatalf("unexpected canvas response: %s", stdout.String())
	}
}

func TestCanvasProjectionRejectsTamperedReplay(t *testing.T) {
	definition := loadExampleWorkflow(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	result, err := executeDemo(definition, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	result.Replay.Receipts[0].ReceiptHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	job, campaign := demoDefinitions(definition, cutoff)
	if _, err := canvas.ProjectWithAdmissions(job, campaign, []contractsv1.WorkflowDefinition{definition}, []contractsv1.ReplayBundle{result.AdmissionReplay}, canvas.ExecutionInput{
		Replay:  result.Replay,
		Outputs: workflow.OutputCatalog{"recommendation@1": validateRecommendation},
	}); err == nil {
		t.Fatal("expected tampered replay to be rejected")
	}
}

func TestCanvasProjectionRejectsWorkflowOutsideCampaignPlan(t *testing.T) {
	definition := loadExampleWorkflow(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	result, err := executeDemo(definition, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	job, campaign := demoDefinitions(definition, cutoff)
	campaign.WorkflowPlan = []contractsv1.WorkflowRef{"other-workflow@1"}
	if _, err := canvas.ProjectWithAdmissions(job, campaign, []contractsv1.WorkflowDefinition{definition}, []contractsv1.ReplayBundle{result.AdmissionReplay}, canvas.ExecutionInput{
		Replay:  result.Replay,
		Outputs: workflow.OutputCatalog{"recommendation@1": validateRecommendation},
	}); err == nil {
		t.Fatal("expected workflow outside the campaign plan to be rejected")
	}
}

func TestCanvasProjectionRejectsDefinitionsNotBoundByReplay(t *testing.T) {
	definition := loadExampleWorkflow(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	result, err := executeDemo(definition, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	job, campaign := demoDefinitions(definition, cutoff)
	job.Intent.Title = "Altered Job"
	if _, err := canvas.ProjectWithAdmissions(job, campaign, []contractsv1.WorkflowDefinition{definition}, []contractsv1.ReplayBundle{result.AdmissionReplay}, canvas.ExecutionInput{Replay: result.Replay, Outputs: workflow.OutputCatalog{"recommendation@1": validateRecommendation}}); err == nil {
		t.Fatal("expected altered Job definition to be rejected")
	}
	job, campaign = demoDefinitions(definition, cutoff)
	definition.Intent.Title = "Altered Workflow"
	if _, err := canvas.ProjectWithAdmissions(job, campaign, []contractsv1.WorkflowDefinition{definition}, []contractsv1.ReplayBundle{result.AdmissionReplay}, canvas.ExecutionInput{Replay: result.Replay, Outputs: workflow.OutputCatalog{"recommendation@1": validateRecommendation}}); err == nil {
		t.Fatal("expected altered Workflow definition to be rejected")
	}
}

func TestCanvasProjectionShowsConfiguredGraphWithoutInventingExecution(t *testing.T) {
	definition := loadExampleWorkflow(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	job, campaign := demoDefinitions(definition, cutoff)

	snapshot, err := canvas.Project(job, campaign, []contractsv1.WorkflowDefinition{definition})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Executions) != 0 || len(snapshot.Replays) != 0 {
		t.Fatalf("configured definitions must not fabricate runtime state: %+v", snapshot)
	}
	if snapshot.Definition.CampaignState != "configured" || snapshot.NextSafeAction.Kind != "none" || snapshot.NextSafeAction.NodeId != nil {
		t.Fatalf("unexpected configured graph: %+v", snapshot)
	}
}

func TestCanvasProjectionDoesNotCompleteBeforeTerminalReceipt(t *testing.T) {
	definition := loadExampleWorkflow(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	result, err := executeDemo(definition, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := workflow.ReplayPrefix(result.Replay, len(result.Replay.Receipts)-1)
	if err != nil {
		t.Fatal(err)
	}
	job, campaign := demoDefinitions(definition, cutoff)
	snapshot, err := canvas.ProjectWithAdmissions(job, campaign, []contractsv1.WorkflowDefinition{definition}, []contractsv1.ReplayBundle{result.AdmissionReplay}, canvas.ExecutionInput{Replay: partial, Outputs: workflow.OutputCatalog{"recommendation@1": validateRecommendation}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Executions[0].Status != "running" {
		t.Fatalf("result without terminal receipt was presented as %q", snapshot.Executions[0].Status)
	}
}

func loadExampleWorkflow(t *testing.T) contractsv1.WorkflowDefinition {
	t.Helper()
	_, current, _, _ := runtime.Caller(0)
	body, err := os.ReadFile(filepath.Join(filepath.Dir(current), "..", "..", "examples", "research-review.workflow.json"))
	if err != nil {
		t.Fatal(err)
	}
	var definition contractsv1.WorkflowDefinition
	if err := json.Unmarshal(body, &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}
