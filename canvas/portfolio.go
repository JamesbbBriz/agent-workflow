package canvas

import (
	"errors"
	"reflect"
	"sort"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

// ProjectPortfolio groups independently verified Campaign projections without
// turning the Job definition into mutable runtime state.
func ProjectPortfolio(job contractsv1.JobDefinition, campaigns []contractsv1.CanvasSnapshot, selected contractsv1.Identifier) (contractsv1.CanvasPortfolioSnapshot, error) {
	if err := contract.ValidateDefinition("JobDefinition", job); err != nil {
		return contractsv1.CanvasPortfolioSnapshot{}, err
	}
	if len(campaigns) == 0 {
		return contractsv1.CanvasPortfolioSnapshot{}, errors.New("Canvas portfolio requires at least one Campaign")
	}
	items := make([]contractsv1.CanvasPortfolioCampaign, 0, len(campaigns))
	seen := make(map[contractsv1.Identifier]bool, len(items))
	generatedAt := campaigns[0].GeneratedAt
	for _, item := range campaigns {
		if err := contract.ValidateDefinition("CanvasSnapshot", item); err != nil {
			return contractsv1.CanvasPortfolioSnapshot{}, err
		}
		if !reflect.DeepEqual(item.Definition.Job, job) {
			return contractsv1.CanvasPortfolioSnapshot{}, errors.New("Campaign Canvas does not belong to the portfolio Job")
		}
		campaignID := item.Definition.Campaign.Id
		if seen[campaignID] {
			return contractsv1.CanvasPortfolioSnapshot{}, errors.New("Canvas portfolio contains a duplicate Campaign")
		}
		seen[campaignID] = true
		if item.GeneratedAt.After(generatedAt) {
			generatedAt = item.GeneratedAt
		}
		items = append(items, contractsv1.CanvasPortfolioCampaign{CampaignId: campaignID, State: campaignState(item), Canvas: item})
	}
	if !seen[selected] {
		return contractsv1.CanvasPortfolioSnapshot{}, errors.New("selected Campaign is not in the Canvas portfolio")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CampaignId < items[j].CampaignId })
	portfolio := contractsv1.CanvasPortfolioSnapshot{
		Kind: contractsv1.CanvasPortfolioSnapshotKindCanvasPortfolioSnapshot, SchemaVersion: 2,
		GeneratedAt: generatedAt, Job: job, SelectedCampaignId: selected, Campaigns: items,
	}
	if err := contract.ValidateDefinition("CanvasPortfolioSnapshot", portfolio); err != nil {
		return contractsv1.CanvasPortfolioSnapshot{}, err
	}
	return portfolio, nil
}

func campaignState(item contractsv1.CanvasSnapshot) contractsv1.CanvasEntityStatus {
	state := contractsv1.CanvasEntityStatusConfigured
	if len(item.AdmissionReplays) > 0 {
		state = contractsv1.CanvasEntityStatusAdmitted
	}
	if len(item.Executions) > 0 {
		state = contractsv1.CanvasEntityStatusRunning
	}
	terminal := false
	for _, execution := range item.Executions {
		if execution.BlockerCode != nil {
			return contractsv1.CanvasEntityStatusBlocked
		}
		if execution.ApprovalState == contractsv1.CanvasExecutionApprovalStatePending {
			return contractsv1.CanvasEntityStatusAwaitingHuman
		} else if execution.Status == contractsv1.CanvasEntityStatusTerminal {
			terminal = true
		}
	}
	if terminal {
		return contractsv1.CanvasEntityStatusTerminal
	}
	return state
}
