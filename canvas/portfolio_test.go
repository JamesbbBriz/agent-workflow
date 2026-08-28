package canvas

import (
	"encoding/json"
	"os"
	"testing"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestProjectPortfolioKeepsCampaignsIndependent(t *testing.T) {
	first := canvasFixture(t)
	first.Definition.Job.Scope.SubjectIds = []string{"example-project", "second-project"}
	first.Definition.CampaignState = contractsv1.CanvasEntityStatusConfigured
	first.Definition.WorkflowStates = map[string]contractsv1.CanvasEntityStatus{}
	first.Executions, first.Replays = []contractsv1.CanvasExecution{}, []contractsv1.ReplayBundle{}
	first.AdmissionReplays, first.ApprovalReplays = nil, nil
	second := first
	second.Definition.Campaign.Id = "second-campaign"
	second.Definition.Campaign.Scope.SubjectIds = []string{"second-project"}
	second.Definition.Campaign.Budget.MaxAttempts = 7
	second.Definition.Campaign.WorkflowPlan = []contractsv1.WorkflowRef{"research-review@2"}
	second.Definition.Workflows = append([]contractsv1.WorkflowDefinition(nil), first.Definition.Workflows...)
	second.Definition.Workflows[0].Version = 2

	portfolio, err := ProjectPortfolio(first.Definition.Job, []contractsv1.CanvasSnapshot{second, first}, second.Definition.Campaign.Id)
	if err != nil {
		t.Fatal(err)
	}
	if portfolio.SchemaVersion != 2 || portfolio.SelectedCampaignId != "second-campaign" || len(portfolio.Campaigns) != 2 {
		t.Fatalf("portfolio did not preserve both Campaigns: %+v", portfolio)
	}
	if portfolio.Campaigns[0].Canvas.Definition.Campaign.Id != first.Definition.Campaign.Id || portfolio.Campaigns[1].Canvas.Definition.Campaign.WorkflowPlan[0] != "research-review@2" {
		t.Fatalf("Campaigns were not independently projected: %+v", portfolio.Campaigns)
	}
	if _, err := ProjectPortfolio(first.Definition.Job, []contractsv1.CanvasSnapshot{first, first}, first.Definition.Campaign.Id); err == nil {
		t.Fatal("portfolio accepted a duplicate Campaign")
	}
}

func canvasFixture(t *testing.T) contractsv1.CanvasSnapshot {
	t.Helper()
	body, err := os.ReadFile("../web/public/canvas.response.json")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}
