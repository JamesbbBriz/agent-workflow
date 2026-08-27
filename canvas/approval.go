package canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func ApplyApproval(snapshot contractsv1.CanvasSnapshot, approval contractsv1.ReplayBundle) (contractsv1.CanvasSnapshot, error) {
	if err := workflow.VerifyReplay(approval); err != nil {
		return contractsv1.CanvasSnapshot{}, err
	}
	if len(approval.Receipts) != 1 || approval.Receipts[0].ReceiptType != contractsv1.ReceiptReceiptTypeApproval {
		return contractsv1.CanvasSnapshot{}, errors.New("approval Replay must contain one canonical decision")
	}
	receipt := approval.Receipts[0]
	var brief contractsv1.ApprovalBrief
	if err := decode(receipt.Payload["brief"], &brief); err != nil {
		return contractsv1.CanvasSnapshot{}, err
	}
	executionIndex, artifactIndex, err := approvalTarget(snapshot, brief)
	if err != nil {
		return contractsv1.CanvasSnapshot{}, err
	}
	selectedID, ok := receipt.Payload["selected_option_id"].(string)
	if !ok {
		return contractsv1.CanvasSnapshot{}, errors.New("approval decision is missing")
	}
	decision := contractsv1.ApprovalOptionDecision("")
	for _, option := range brief.Options {
		if string(option.Id) == selectedID {
			decision = option.Decision
		}
	}
	if decision == "" {
		return contractsv1.CanvasSnapshot{}, errors.New("approval decision is not in the brief")
	}

	body, _ := json.Marshal(snapshot)
	var next contractsv1.CanvasSnapshot
	if err := json.Unmarshal(body, &next); err != nil {
		return contractsv1.CanvasSnapshot{}, err
	}
	execution := &next.Executions[executionIndex]
	artifact := &execution.Outputs[artifactIndex]
	if decision == contractsv1.ApprovalOptionDecisionApprove {
		artifact.ApprovalState = contractsv1.ActionArtifactApprovalStateApproved
		execution.ApprovalState = contractsv1.CanvasExecutionApprovalStateApproved
	} else {
		artifact.ApprovalState = contractsv1.ActionArtifactApprovalStateRejected
		execution.ApprovalState = contractsv1.CanvasExecutionApprovalStateRejected
	}
	next.ApprovalReplays = append(next.ApprovalReplays, approval)
	if receipt.OccurredAt.After(next.GeneratedAt) {
		next.GeneratedAt = receipt.OccurredAt
	}
	if err := contract.ValidateDefinition("CanvasSnapshot", next); err != nil {
		return contractsv1.CanvasSnapshot{}, fmt.Errorf("approved Canvas: %w", err)
	}
	return next, nil
}

func ValidateApprovalTarget(snapshot contractsv1.CanvasSnapshot, brief contractsv1.ApprovalBrief) error {
	_, _, err := approvalTarget(snapshot, brief)
	return err
}

func approvalTarget(snapshot contractsv1.CanvasSnapshot, brief contractsv1.ApprovalBrief) (int, int, error) {
	if err := contract.ValidateDefinition("CanvasSnapshot", snapshot); err != nil {
		return 0, 0, err
	}
	for executionIndex := range snapshot.Executions {
		for artifactIndex := range snapshot.Executions[executionIndex].Outputs {
			artifact := snapshot.Executions[executionIndex].Outputs[artifactIndex]
			if artifact.Id != brief.Action.Id {
				continue
			}
			if !reflect.DeepEqual(artifact, brief.Action) || !approvalEvidenceMatches(brief, snapshot.Replays) {
				return 0, 0, errors.New("approval brief is not bound to the Canvas source Replay")
			}
			if artifact.ApprovalState != contractsv1.ActionArtifactApprovalStatePending {
				return 0, 0, errors.New("approval action is no longer pending")
			}
			return executionIndex, artifactIndex, nil
		}
	}
	return 0, 0, errors.New("approval action is not visible in this Canvas")
}

func approvalEvidenceMatches(brief contractsv1.ApprovalBrief, replays []contractsv1.ReplayBundle) bool {
	for _, replay := range replays {
		for _, receipt := range replay.Receipts {
			if receipt.ReceiptType != contractsv1.ReceiptReceiptTypeResult {
				continue
			}
			for _, evidence := range brief.Evidence {
				if evidence.Kind == contractsv1.ArtifactRefKindReceipt && evidence.Id == receipt.Id && evidence.Sha256 == receipt.ReceiptHash {
					return true
				}
			}
		}
	}
	return false
}

func decode(value any, target any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return errors.New("approval payload is invalid")
	}
	return nil
}
