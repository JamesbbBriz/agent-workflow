package workflow

import (
	"context"
	"errors"
	"strings"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

// RetireBlockedCampaign is an operator-authorized embedding operation. The host
// must settle its external effects first; this operation never cancels providers.
func (e *Engine) RetireBlockedCampaign(ctx context.Context, request CampaignRunRequest, retirement contractsv1.CampaignRetirementRequest) (contractsv1.CampaignDriveReceipt, error) {
	if err := ctx.Err(); err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	if err := contract.ValidateDefinition("CampaignRetirementRequest", retirement); err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	if strings.TrimSpace(retirement.Actor) == "" || strings.TrimSpace(retirement.Reason) == "" || retirement.JobId != request.Job.Id || retirement.CampaignId != request.Campaign.Id {
		return contractsv1.CampaignDriveReceipt{}, errors.New("retirement identity, actor and reason must match")
	}
	prepared, err := e.prepareCampaign(request)
	if err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	state, replay, err := e.campaignState(prepared)
	if err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	if replay == nil {
		return contractsv1.CampaignDriveReceipt{}, errors.New("unadmitted Campaign cannot be retired")
	}
	last := replay.Receipts[len(replay.Receipts)-1]
	result := contractsv1.CampaignDriveReceipt{Kind: contractsv1.CampaignDriveReceiptKindCampaignDriveReceipt, SchemaVersion: 2, State: state, CampaignReplay: replay}
	if state.Status == contractsv1.CampaignExecutionStateStatusTerminal {
		var previous contractsv1.CampaignRetirementRequest
		if decodePayload(last.Payload["retirement"], &previous) == nil {
			priorHash, priorErr := Digest(previous)
			requestHash, requestErr := Digest(retirement)
			if priorErr == nil && requestErr == nil && priorHash == requestHash {
				return result, nil
			}
		}
		return result, errors.New("Campaign has a different terminal decision")
	}
	if retirement.ExpectedReceiptHash != last.ReceiptHash {
		return result, errors.New("retirement preview is stale")
	}
	if err := e.retirementEligible(state, prepared); err != nil {
		return result, err
	}
	at := e.now()
	if at.Before(state.UpdatedAt) {
		return result, errors.New("retirement clock predates canonical state")
	}
	if err := e.appendCampaignEvent(*replay, contractsv1.ReceiptReceiptTypeTerminal, at, []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"state": "retired", "retirement": retirement}); err != nil {
		return result, err
	}
	state, replay, err = e.campaignState(prepared)
	if err != nil {
		return result, err
	}
	result.State, result.CampaignReplay, result.Transitions = state, replay, 1
	return result, nil
}

func (e *Engine) retirementEligible(state contractsv1.CampaignExecutionState, prepared preparedCampaign) error {
	if state.Status != contractsv1.CampaignExecutionStateStatusBlocked {
		return errors.New("only a blocked Campaign may be retired")
	}
	for _, node := range state.Nodes {
		if node.Status == contractsv1.CampaignNodeExecutionStatusRunning {
			return errors.New("active Campaign invocation must settle before retirement")
		}
		workflow, ok := preparedWorkflowByRef(prepared, node.WorkflowRef)
		if !ok {
			return errors.New("retirement Workflow is not admitted")
		}
		childID, err := executionID(RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: string(node.NodeId)})
		if err != nil {
			return err
		}
		child, err := e.ledger.Replay(childID)
		if errors.Is(err, ErrReplayEmpty) {
			continue
		}
		if err != nil {
			return err
		}
		if err := VerifyReplay(child); err != nil {
			return err
		}
		if len(child.Receipts) == 0 || child.Receipts[len(child.Receipts)-1].ReceiptType != contractsv1.ReceiptReceiptTypeTerminal {
			return errors.New("unfinished child execution must settle before retirement")
		}
	}
	return nil
}
