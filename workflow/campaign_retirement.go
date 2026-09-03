package workflow

import (
	"context"
	"errors"
	"strings"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

// SettleExpiredCampaignInvocations cancels only recorded, expired invocations of
// an exact blocked Campaign. Cancel success must mean provider quiescence; the
// host must settle external effects first. This never dispatches new work.
func (e *Engine) SettleExpiredCampaignInvocations(ctx context.Context, request CampaignRunRequest, retirement contractsv1.CampaignRetirementRequest) ([]contractsv1.ReplayBundle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := contract.ValidateDefinition("CampaignRetirementRequest", retirement); err != nil {
		return nil, err
	}
	if strings.TrimSpace(retirement.Actor) == "" || strings.TrimSpace(retirement.Reason) == "" || retirement.JobId != request.Job.Id || retirement.CampaignId != request.Campaign.Id {
		return nil, errors.New("settlement identity, actor and reason must match")
	}
	prepared, err := e.prepareCampaign(request)
	if err != nil {
		return nil, err
	}
	state, parent, err := e.campaignState(prepared)
	if err != nil {
		return nil, err
	}
	if parent == nil || state.Status != contractsv1.CampaignExecutionStateStatusBlocked || parent.Receipts[len(parent.Receipts)-1].ReceiptHash != retirement.ExpectedReceiptHash {
		return nil, errors.New("settlement requires the exact blocked Campaign head")
	}
	var settled []contractsv1.ReplayBundle
	for _, node := range state.Nodes {
		wf, ok := preparedWorkflowByRef(prepared, node.WorkflowRef)
		if !ok {
			return settled, errors.New("settlement Workflow is not admitted")
		}
		id, err := executionID(RunRequest{Job: request.Job, Campaign: request.Campaign, Workflow: wf.definition, NodeID: string(node.NodeId)})
		if err != nil {
			return settled, err
		}
		child, err := e.ledger.Replay(id)
		if errors.Is(err, ErrReplayEmpty) && node.ResultReplayHash == nil {
			continue
		}
		if err != nil {
			return settled, err
		}
		invocation, err := VerifyDefinitionBindingWithAdmission(child, wf.admissionReplay, request.Job, request.Campaign, wf.definition)
		if err != nil {
			return settled, err
		}
		if child.Receipts[len(child.Receipts)-1].ReceiptType == contractsv1.ReceiptReceiptTypeTerminal {
			continue
		}
		if hasReceipt(child, contractsv1.ReceiptReceiptTypeProviderExecution) || hasReceipt(child, contractsv1.ReceiptReceiptTypeResult) {
			return settled, errors.New("recorded provider result must reconcile before expired settlement")
		}
		at := e.now()
		if invocation.Node.Id != node.NodeId || invocation.Deadline.IsZero() || at.Before(invocation.Deadline) || at.Before(state.UpdatedAt) {
			return settled, errors.New("unfinished child is not eligible for expired settlement")
		}
		if err := e.validateInvocationIsolation(invocation); err != nil {
			return settled, err
		}
		if err := e.cancelDeadlineInvocation(ctx, id, at, invocation, child); err != nil {
			return settled, err
		}
		child, err = e.ledger.Replay(id)
		if err != nil {
			return settled, err
		}
		settled = append(settled, child)
	}
	return settled, nil
}

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
		if sameRetirement(last, retirement) {
			return result, nil
		}
		return result, errors.New("Campaign has a different terminal decision")
	}
	if retirement.ExpectedReceiptHash != last.ReceiptHash {
		return result, errors.New("retirement preview is stale")
	}
	children, err := e.retirementChildren(state, prepared, nil)
	if err != nil {
		return result, err
	}
	at := e.now()
	if at.Before(state.UpdatedAt) {
		return result, errors.New("retirement clock predates canonical state")
	}
	if err := e.appendCampaignEvent(*replay, contractsv1.ReceiptReceiptTypeTerminal, at, []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"state": "retired", "retirement": retirement, "child_replays": children}); err != nil {
		current, committed, readErr := e.campaignState(prepared)
		if readErr == nil && committed != nil && current.Status == contractsv1.CampaignExecutionStateStatusTerminal && sameRetirement(committed.Receipts[len(committed.Receipts)-1], retirement) {
			result.State, result.CampaignReplay = current, committed
			return result, nil
		}
		return result, err
	}
	state, replay, err = e.campaignState(prepared)
	if err != nil {
		return result, err
	}
	result.State, result.CampaignReplay, result.Transitions = state, replay, 1
	return result, nil
}

func sameRetirement(receipt contractsv1.Receipt, request contractsv1.CampaignRetirementRequest) bool {
	var previous contractsv1.CampaignRetirementRequest
	if decodePayload(receipt.Payload["retirement"], &previous) != nil {
		return false
	}
	priorHash, priorErr := Digest(previous)
	requestHash, requestErr := Digest(request)
	return priorErr == nil && requestErr == nil && priorHash == requestHash
}

func (e *Engine) retirementChildren(state contractsv1.CampaignExecutionState, prepared preparedCampaign, frozen map[string]string) (map[string]string, error) {
	if state.Status != contractsv1.CampaignExecutionStateStatusBlocked {
		return nil, errors.New("only a blocked Campaign may be retired")
	}
	if frozen != nil && len(frozen) != len(state.Nodes) {
		return nil, errors.New("retirement child bindings do not cover admitted nodes")
	}
	children := make(map[string]string, len(state.Nodes))
	for _, node := range state.Nodes {
		if node.Status == contractsv1.CampaignNodeExecutionStatusRunning {
			return nil, errors.New("active Campaign invocation must settle before retirement")
		}
		workflow, ok := preparedWorkflowByRef(prepared, node.WorkflowRef)
		if !ok {
			return nil, errors.New("retirement Workflow is not admitted")
		}
		childID, err := executionID(RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: string(node.NodeId)})
		if err != nil {
			return nil, err
		}
		if frozen != nil {
			cutoff, exists := frozen[childID]
			if !exists || cutoff == "" {
				return nil, errors.New("retirement child cutoff is missing")
			}
			if cutoff == "absent" {
				if node.ResultReplayHash != nil {
					return nil, errors.New("retirement hides a recorded child result")
				}
				children[childID] = cutoff
				continue
			}
		}
		child, err := e.ledger.Replay(childID)
		if errors.Is(err, ErrReplayEmpty) && frozen == nil {
			if node.ResultReplayHash != nil {
				return nil, errors.New("retirement child result is unavailable")
			}
			children[childID] = "absent"
			continue
		}
		if err != nil {
			return nil, err
		}
		if frozen != nil {
			child, err = replayAtBundleHash(child, contractsv1.SHA256(frozen[childID]))
			if err != nil {
				return nil, err
			}
		}
		if err := VerifyReplay(child); err != nil {
			return nil, err
		}
		if len(child.Receipts) == 0 || child.Receipts[len(child.Receipts)-1].ReceiptType != contractsv1.ReceiptReceiptTypeTerminal {
			return nil, errors.New("unfinished child execution must settle before retirement")
		}
		children[childID] = string(child.BundleHash)
	}
	return children, nil
}
