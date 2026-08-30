package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

// FixtureRuntime gives local CLI clients a persistent path through the same
// Core used by conformance. It deliberately does not auto-approve.
type FixtureRuntime struct {
	runtime *conformanceRuntime
	now     func() time.Time
}

func NewFixtureRuntime(fixture contractsv1.ConformanceFixture, provider Provider, ledger Ledger) (*FixtureRuntime, error) {
	runtime, err := newConformanceRuntimeWithLedger(fixture, provider, ledger)
	if err != nil {
		return nil, err
	}
	return &FixtureRuntime{runtime: runtime, now: time.Now}, nil
}

func (r *FixtureRuntime) Admit() error {
	return r.runtime.admit()
}

func (r *FixtureRuntime) Preflight() error {
	check, err := newConformanceRuntimeWithLedger(r.runtime.fixture, r.runtime.engine.provider, NewMemoryLedger())
	if err != nil {
		return err
	}
	return check.admit()
}

func (r *FixtureRuntime) Preview(ctx context.Context, campaignID contractsv1.Identifier) (contractsv1.CampaignDrivePreview, error) {
	campaign, workflows, err := r.campaign(campaignID)
	if err != nil {
		return contractsv1.CampaignDrivePreview{}, err
	}
	return r.runtime.engine.Preview(ctx, CampaignRunRequest{Job: r.runtime.fixture.Job, Campaign: campaign, Workflows: workflows})
}

func (r *FixtureRuntime) Drive(ctx context.Context, campaignID contractsv1.Identifier, maxTransitions int) (contractsv1.CampaignDriveReceipt, error) {
	campaign, workflows, err := r.campaign(campaignID)
	if err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	return r.runtime.engine.Drive(ctx, CampaignDriveCommand{CampaignRunRequest: CampaignRunRequest{Job: r.runtime.fixture.Job, Campaign: campaign, Workflows: workflows}, MaxTransitions: maxTransitions})
}

func (r *FixtureRuntime) PreviewApproval(ctx context.Context, campaignID contractsv1.Identifier) (contractsv1.ApprovalPreview, error) {
	campaign, workflows, err := r.campaign(campaignID)
	if err != nil {
		return contractsv1.ApprovalPreview{}, err
	}
	request := CampaignRunRequest{Job: r.runtime.fixture.Job, Campaign: campaign, Workflows: workflows}
	preview, err := r.runtime.engine.Preview(ctx, request)
	if err != nil {
		return contractsv1.ApprovalPreview{}, err
	}
	prepared, err := r.runtime.engine.prepareCampaign(request)
	if err != nil {
		return contractsv1.ApprovalPreview{}, err
	}
	for _, node := range preview.State.Nodes {
		if node.Status != contractsv1.CampaignNodeExecutionStatusAwaitingApproval {
			continue
		}
		workflow, definition, ok := preparedNode(prepared, node.WorkflowRef, string(node.NodeId))
		if !ok || definition.Definition.ApprovalPolicy == nil {
			return contractsv1.ApprovalPreview{}, errors.New("approval Node has no policy")
		}
		source, action, result, err := r.runtime.engine.approvalSource(prepared, workflow, definition)
		if err != nil {
			return contractsv1.ApprovalPreview{}, err
		}
		brief := contractsv1.ApprovalBrief{
			Kind: contractsv1.ApprovalBriefKindApprovalBrief, SchemaVersion: 1,
			Title:    "Approve the exact local workflow result?",
			Evidence: []contractsv1.ArtifactRef{{Id: result.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "result", SchemaVersion: 1, Sha256: result.ReceiptHash, MediaType: "application/json"}},
			Options: []contractsv1.ApprovalOption{
				{Id: "approve", Label: "Approve", Decision: contractsv1.ApprovalOptionDecisionApprove, Tradeoffs: []string{"Accepts the exact result"}},
				{Id: "reject", Label: "Reject", Decision: contractsv1.ApprovalOptionDecisionReject, Tradeoffs: []string{"Stops this result"}},
			},
			RecommendedOptionId: "approve", Recommendation: "Approve the bounded local result.",
			Risks: []string{"This starter fixture does not perform production mutation."}, Action: action, ApprovalPolicy: definition.Definition.ApprovalPolicy,
		}
		return r.runtime.authoring.PreviewApproval(brief, "conformance-human", source.AggregateId)
	}
	return contractsv1.ApprovalPreview{}, errors.New("Campaign is not awaiting approval")
}

func (r *FixtureRuntime) ConfirmApproval(preview contractsv1.ApprovalPreview, option string) (contractsv1.Receipt, error) {
	return r.runtime.authoring.ConfirmApproval(preview, "conformance-human", option, r.now().UTC())
}

func (r *FixtureRuntime) ExistingApprovalOption(ctx context.Context, campaignID contractsv1.Identifier) (string, bool, error) {
	preview, err := r.Preview(ctx, campaignID)
	if err != nil {
		return "", false, err
	}
	for _, node := range preview.State.Nodes {
		if node.ApprovalId == nil {
			continue
		}
		replay, err := r.runtime.ledger.Replay(approvalAggregate(string(*node.ApprovalId)))
		if errors.Is(err, ErrReplayEmpty) {
			return "", false, nil
		}
		if err != nil || VerifyReplay(replay) != nil || len(replay.Receipts) != 1 || replay.Receipts[0].ReceiptType != contractsv1.ReceiptReceiptTypeApproval {
			return "", false, errors.New("existing approval is invalid")
		}
		var option string
		if err := decodePayload(replay.Receipts[0].Payload["selected_option_id"], &option); err != nil {
			return "", false, errors.New("existing approval option is invalid")
		}
		return option, true, nil
	}
	return "", false, nil
}

func (r *FixtureRuntime) Replay(campaignID contractsv1.Identifier) (contractsv1.ReplayBundle, error) {
	campaign, _, err := r.campaign(campaignID)
	if err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	aggregateID, err := campaignExecutionID(r.runtime.fixture.Job.Id, campaign.Id)
	if err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	return r.runtime.ledger.Replay(aggregateID)
}

func (r *FixtureRuntime) campaign(id contractsv1.Identifier) (contractsv1.CampaignDefinition, []contractsv1.WorkflowDefinition, error) {
	if id == "" && len(r.runtime.fixture.Campaigns) == 1 {
		id = r.runtime.fixture.Campaigns[0].Id
	}
	for _, campaign := range r.runtime.fixture.Campaigns {
		if campaign.Id == id {
			definitions, err := workflowsForCampaign(campaign, r.runtime.workflows)
			return campaign, definitions, err
		}
	}
	return contractsv1.CampaignDefinition{}, nil, fmt.Errorf("Campaign %q is not defined", id)
}
