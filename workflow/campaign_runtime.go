package workflow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

// CampaignRuntime is the canonical whole-Workflow execution seam. Callers may
// bound work per delivery, but they cannot select the next Node.
type CampaignRuntime interface {
	Preview(context.Context, CampaignRunRequest) (contractsv1.CampaignDrivePreview, error)
	Drive(context.Context, CampaignDriveCommand) (contractsv1.CampaignDriveReceipt, error)
}

type CampaignRunRequest struct {
	Job       contractsv1.JobDefinition
	Campaign  contractsv1.CampaignDefinition
	Workflows []contractsv1.WorkflowDefinition
	// Workflow keeps the one-Workflow Go API source-compatible. New callers
	// that pin more than one Workflow must supply Workflows in plan order.
	Workflow contractsv1.WorkflowDefinition
}

type CampaignDriveCommand struct {
	CampaignRunRequest
	MaxTransitions int
}

type budgetExceededError struct{ code contractsv1.Identifier }

func (e budgetExceededError) Error() string { return string(e.code) }

// RunNode preserves the v1 entry point while refusing caller-selected work
// that is not the Core-derived next Node.
func (e *Engine) RunNode(ctx context.Context, request RunRequest) (RunResult, error) {
	aggregateID, err := executionID(request)
	if err != nil {
		return RunResult{}, err
	}
	childReplay, childErr := e.ledger.Replay(aggregateID)
	if childErr != nil && !errors.Is(childErr, ErrReplayEmpty) {
		return RunResult{}, childErr
	}
	campaignID, err := campaignExecutionID(request.Job.Id, request.Campaign.Id)
	if err != nil {
		return RunResult{}, err
	}
	_, campaignErr := e.ledger.Replay(campaignID)
	if campaignErr != nil && !errors.Is(campaignErr, ErrReplayEmpty) {
		return RunResult{}, campaignErr
	}
	if childErr == nil && errors.Is(campaignErr, ErrReplayEmpty) {
		// A child-only aggregate predates the Campaign driver. Keep it readable
		// without manufacturing a v2 parent history around it.
		if !nodeCompletedReplay(childReplay) {
			return RunResult{}, errors.New("incomplete legacy v1 execution is read-only and cannot be resumed")
		}
		return e.runAgentNode(ctx, request)
	}
	preview, err := e.Preview(ctx, CampaignRunRequest{Job: request.Job, Campaign: request.Campaign, Workflow: request.Workflow})
	if err != nil {
		return RunResult{}, err
	}
	if preview.State.BlockerCode != nil {
		if *preview.State.BlockerCode == "duration-budget-exhausted" {
			return RunResult{}, ErrProviderDeadline
		}
		if knownBudgetBlocker(*preview.State.BlockerCode) {
			return RunResult{}, budgetExceededError{code: *preview.State.BlockerCode}
		}
	}
	if childErr == nil {
		workflowRef := contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", request.Workflow.Id, request.Workflow.Version))
		for _, node := range preview.State.Nodes {
			if node.WorkflowRef == workflowRef && string(node.NodeId) == request.NodeID && (node.Status == contractsv1.CampaignNodeExecutionStatusCompleted || node.Status == contractsv1.CampaignNodeExecutionStatusCompletedNoAction) {
				return e.runAgentNode(ctx, request)
			}
		}
	}
	workflowRef := contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", request.Workflow.Id, request.Workflow.Version))
	if preview.NextAction != contractsv1.CampaignDrivePreviewNextActionRunNode || preview.State.NextWorkflowRef == nil || *preview.State.NextWorkflowRef != workflowRef || preview.State.NextNodeId == nil || string(*preview.State.NextNodeId) != request.NodeID {
		return RunResult{}, fmt.Errorf("node %q is not the Core-derived next ready Node", request.NodeID)
	}
	receipt, err := e.drive(ctx, CampaignDriveCommand{CampaignRunRequest: CampaignRunRequest{Job: request.Job, Campaign: request.Campaign, Workflow: request.Workflow}, MaxTransitions: 1})
	if err != nil {
		return RunResult{}, err
	}
	if receipt.NodeReplay == nil {
		if receipt.State.BlockerCode != nil {
			if *receipt.State.BlockerCode == "duration-budget-exhausted" {
				return RunResult{}, ErrProviderDeadline
			}
			if knownBudgetBlocker(*receipt.State.BlockerCode) {
				return RunResult{}, budgetExceededError{code: *receipt.State.BlockerCode}
			}
		}
		return RunResult{}, fmt.Errorf("Campaign driver did not produce a Node Replay (status=%s blocker=%v next=%v transitions=%d)", receipt.State.Status, receipt.State.BlockerCode, receipt.State.NextNodeId, receipt.Transitions)
	}
	material, err := MaterializeReplay(*receipt.NodeReplay, e.outputs)
	if err != nil {
		return RunResult{}, err
	}
	compiled, _, err := compileWorkflow(request.Workflow, e.registry, aggregateID, receipt.State.StartedAt)
	if err != nil {
		return RunResult{}, err
	}
	_, admissionReplay, err := e.admissionForRun(request)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Compiled: compiled, Bundle: material.Invocation.Bundle, Artifacts: material.Artifacts, AdmissionReplay: admissionReplay, Replay: *receipt.NodeReplay}, nil
}

func (e *Engine) Preview(_ context.Context, request CampaignRunRequest) (contractsv1.CampaignDrivePreview, error) {
	prepared, err := e.prepareCampaign(request)
	if err != nil {
		return contractsv1.CampaignDrivePreview{}, err
	}
	state, _, err := e.campaignState(prepared)
	if err != nil {
		return contractsv1.CampaignDrivePreview{}, err
	}
	deriveNext(&state, prepared)
	preview := contractsv1.CampaignDrivePreview{Kind: contractsv1.CampaignDrivePreviewKindCampaignDrivePreview, SchemaVersion: 2, State: state, NextAction: nextAction(state)}
	if err := contract.ValidateDefinition("CampaignDrivePreview", preview); err != nil {
		return contractsv1.CampaignDrivePreview{}, err
	}
	return preview, nil
}

func (e *Engine) Drive(ctx context.Context, command CampaignDriveCommand) (contractsv1.CampaignDriveReceipt, error) {
	return e.drive(ctx, command)
}

func (e *Engine) drive(ctx context.Context, command CampaignDriveCommand) (contractsv1.CampaignDriveReceipt, error) {
	if command.MaxTransitions == 0 {
		command.MaxTransitions = 1
	}
	if command.MaxTransitions < 1 || command.MaxTransitions > 100 {
		return contractsv1.CampaignDriveReceipt{}, errors.New("max transitions must be between 1 and 100")
	}
	prepared, err := e.prepareCampaign(command.CampaignRunRequest)
	if err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	state, replay, err := e.campaignState(prepared)
	if err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	if replay == nil {
		if err := e.admitCampaign(prepared, state); err != nil {
			return contractsv1.CampaignDriveReceipt{}, err
		}
		bundle, err := e.ledger.Replay(state.AggregateId)
		if err != nil {
			return contractsv1.CampaignDriveReceipt{}, err
		}
		replay = &bundle
	}
	transitions := 0
	var nodeReplay *contractsv1.ReplayBundle
	for transitions < command.MaxTransitions {
		deriveNext(&state, prepared)
		if state.Status == contractsv1.CampaignExecutionStateStatusCompleted || state.Status == contractsv1.CampaignExecutionStateStatusBlocked || state.Status == contractsv1.CampaignExecutionStateStatusTerminal {
			break
		}
		if state.NextWorkflowRef == nil || state.NextNodeId == nil {
			if err := e.completeCampaign(&state, *replay); err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			transitions++
			bundle, err := e.ledger.Replay(state.AggregateId)
			if err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			replay = &bundle
			state, err = e.reduceCampaignReplay(bundle, prepared)
			if err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			break
		}
		workflowRef, nodeID := *state.NextWorkflowRef, string(*state.NextNodeId)
		workflow, node, ok := preparedNode(prepared, workflowRef, nodeID)
		if !ok {
			return contractsv1.CampaignDriveReceipt{}, errors.New("derived Node is absent from the compiled Campaign plan")
		}
		remaining, blocker := remainingBudget(state, prepared.request.Campaign.Budget, workflowRef, node.Definition)
		if blocker != "" {
			if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, blocker); err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			transitions++
			bundle, err := e.ledger.Replay(state.AggregateId)
			if err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			replay = &bundle
			state, err = e.reduceCampaignReplay(bundle, prepared)
			if err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			break
		}
		if nodeState(state, workflowRef, nodeID).Status == contractsv1.CampaignNodeExecutionStatusPending {
			if err := e.reserveCampaignAttempt(&state, *replay, workflowRef, nodeID); err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			bundle, err := e.ledger.Replay(state.AggregateId)
			if err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			replay = &bundle
			state, err = e.reduceCampaignReplay(bundle, prepared)
			if err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			remaining, blocker = remainingBudget(state, prepared.request.Campaign.Budget, workflowRef, node.Definition)
			if blocker != "" {
				if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, blocker); err != nil {
					return contractsv1.CampaignDriveReceipt{}, err
				}
				transitions++
				break
			}
		}
		startedAt := nodeState(state, workflowRef, nodeID).StartedAt
		run, runErr := e.runAgentNodeAt(ctx, RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: nodeID, BudgetOverride: &remaining}, startedAt)
		if runErr != nil {
			var exceeded budgetExceededError
			if errors.As(runErr, &exceeded) {
				if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, exceeded.code); err != nil {
					return contractsv1.CampaignDriveReceipt{}, err
				}
				transitions++
				break
			}
			if errors.Is(runErr, ErrProviderNotReady) {
				break
			}
			if errors.Is(runErr, ErrProviderDeadline) {
				if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, "duration-budget-exhausted"); err != nil {
					return contractsv1.CampaignDriveReceipt{}, err
				}
				transitions++
				break
			}
			return contractsv1.CampaignDriveReceipt{}, runErr
		}
		nodeReplay = &run.Replay
		material, err := MaterializeReplay(run.Replay, e.outputs)
		if err != nil {
			return contractsv1.CampaignDriveReceipt{}, err
		}
		if blocker := campaignResultBudgetBlocker(material.Artifacts, node.Definition.OutputSlots, remaining); blocker != "" {
			if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, blocker); err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			transitions++
			break
		}
		if _, blocker := remainingBudget(state, prepared.request.Campaign.Budget, workflowRef, node.Definition); blocker != "" {
			if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, blocker); err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			transitions++
			break
		}
		if err := e.completeCampaignNode(&state, *replay, workflowRef, nodeID, material, run.Replay); err != nil {
			return contractsv1.CampaignDriveReceipt{}, err
		}
		transitions++
		bundle, err := e.ledger.Replay(state.AggregateId)
		if err != nil {
			return contractsv1.CampaignDriveReceipt{}, err
		}
		replay = &bundle
		state, err = e.reduceCampaignReplay(bundle, prepared)
		if err != nil {
			return contractsv1.CampaignDriveReceipt{}, err
		}
	}
	latest, err := e.ledger.Replay(state.AggregateId)
	if err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	state, err = e.reduceCampaignReplay(latest, prepared)
	if err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	replay = &latest
	deriveNext(&state, prepared)
	receipt := contractsv1.CampaignDriveReceipt{Kind: contractsv1.CampaignDriveReceiptKindCampaignDriveReceipt, SchemaVersion: 2, State: state, Transitions: transitions, CampaignReplay: replay, NodeReplay: nodeReplay}
	if err := contract.ValidateDefinition("CampaignDriveReceipt", receipt); err != nil {
		return contractsv1.CampaignDriveReceipt{}, err
	}
	return receipt, nil
}

type preparedWorkflow struct {
	definition      contractsv1.WorkflowDefinition
	compiled        CompiledWorkflow
	admission       contractsv1.WorkflowAdmission
	admissionReplay contractsv1.ReplayBundle
}

type preparedCampaign struct {
	request   CampaignRunRequest
	workflows []preparedWorkflow
	initial   contractsv1.CampaignExecutionState
}

func (e *Engine) prepareCampaign(request CampaignRunRequest) (preparedCampaign, error) {
	if e == nil || e.provider == nil || e.ledger == nil {
		return preparedCampaign{}, errors.New("provider and ledger are required")
	}
	definitions, err := campaignWorkflowDefinitions(request)
	if err != nil {
		return preparedCampaign{}, err
	}
	run := RunRequest{Job: request.Job, Campaign: request.Campaign, Workflow: definitions[0]}
	jobHash, campaignHash, err := aggregateDefinitionHashes(run)
	if err != nil {
		return preparedCampaign{}, err
	}
	aggregateID, err := campaignExecutionID(request.Job.Id, request.Campaign.Id)
	if err != nil {
		return preparedCampaign{}, err
	}
	now := time.Now().UTC()
	state := contractsv1.CampaignExecutionState{
		Kind: contractsv1.CampaignExecutionStateKindCampaignExecutionState, SchemaVersion: 2,
		AggregateId: aggregateID, JobId: request.Job.Id, CampaignId: request.Campaign.Id,
		JobHash: jobHash, CampaignHash: campaignHash, WorkflowHashes: contractsv1.CampaignExecutionStateWorkflowHashes{},
		Status: contractsv1.CampaignExecutionStateStatusAdmitted, StartedAt: now, UpdatedAt: now,
	}
	prepared := preparedCampaign{request: request, initial: state}
	for index, definition := range definitions {
		run.Workflow = definition
		compiled, _, err := compileWorkflow(definition, e.registry, aggregateID, now)
		if err != nil {
			return preparedCampaign{}, err
		}
		if compiled.WorkflowRef != request.Campaign.WorkflowPlan[index] {
			return preparedCampaign{}, errors.New("Campaign Workflow definitions do not match the pinned plan order")
		}
		if err := validateRunBinding(run, compiled, now); err != nil {
			return preparedCampaign{}, err
		}
		admission, admissionReplay, err := e.admissionForRun(run)
		if err != nil {
			return preparedCampaign{}, err
		}
		if admission.DefinitionHash != compiled.DefinitionHash || admission.CompileHash != compiled.CompileHash {
			return preparedCampaign{}, errors.New("admitted Workflow does not match the compiled Campaign contract")
		}
		prepared.workflows = append(prepared.workflows, preparedWorkflow{definition: definition, compiled: compiled, admission: admission, admissionReplay: admissionReplay})
		prepared.initial.WorkflowHashes[string(compiled.WorkflowRef)] = compiled.DefinitionHash
		for _, node := range compiled.Nodes {
			prepared.initial.Nodes = append(prepared.initial.Nodes, contractsv1.CampaignNodeExecution{WorkflowRef: compiled.WorkflowRef, NodeId: node.Definition.Id, Status: contractsv1.CampaignNodeExecutionStatusPending})
		}
	}
	return prepared, nil
}

func campaignWorkflowDefinitions(request CampaignRunRequest) ([]contractsv1.WorkflowDefinition, error) {
	definitions := append([]contractsv1.WorkflowDefinition(nil), request.Workflows...)
	if len(definitions) == 0 && request.Workflow.Id != "" {
		definitions = []contractsv1.WorkflowDefinition{request.Workflow}
	}
	if len(definitions) != len(request.Campaign.WorkflowPlan) || len(definitions) == 0 {
		return nil, errors.New("Campaign execution requires every pinned Workflow definition in plan order")
	}
	return definitions, nil
}

func campaignExecutionID(jobID, campaignID contractsv1.Identifier) (string, error) {
	hash, err := Digest(struct{ JobID, CampaignID contractsv1.Identifier }{jobID, campaignID})
	if err != nil {
		return "", err
	}
	return shortID("campaign-run-", hash), nil
}

func (e *Engine) campaignState(prepared preparedCampaign) (contractsv1.CampaignExecutionState, *contractsv1.ReplayBundle, error) {
	replay, err := e.ledger.Replay(prepared.initial.AggregateId)
	if errors.Is(err, ErrReplayEmpty) {
		return prepared.initial, nil, nil
	}
	if err != nil {
		return contractsv1.CampaignExecutionState{}, nil, err
	}
	state, err := e.reduceCampaignReplay(replay, prepared)
	if err != nil {
		return contractsv1.CampaignExecutionState{}, nil, err
	}
	return state, &replay, nil
}

func (e *Engine) admitCampaign(prepared preparedCampaign, state contractsv1.CampaignExecutionState) error {
	hash, err := Digest(state)
	if err != nil {
		return err
	}
	receipt, err := sealReceiptVersion(2, state.AggregateId, 1, contractsv1.ReceiptReceiptTypeCampaignAdmission, state.StartedAt, nil,
		campaignAdmissionInputs(prepared, state), []contractsv1.SHA256{contractsv1.SHA256(hash)}, map[string]any{"state": state})
	if err != nil {
		return err
	}
	return e.ledger.Append(receipt)
}

func campaignAdmissionInputs(prepared preparedCampaign, state contractsv1.CampaignExecutionState) []contractsv1.SHA256 {
	inputs := []contractsv1.SHA256{state.JobHash, state.CampaignHash}
	for _, workflow := range prepared.workflows {
		inputs = append(inputs, workflow.admission.Receipt.ReceiptHash, workflow.compiled.DefinitionHash, workflow.compiled.CompileHash)
	}
	return inputs
}

func (e *Engine) reserveCampaignAttempt(state *contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle, workflowRef contractsv1.WorkflowRef, nodeID string) error {
	now := time.Now().UTC()
	return e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeAttemptReserved, now, []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"workflow_ref": workflowRef, "node_id": nodeID, "started_at": now})
}

func (e *Engine) completeCampaignNode(state *contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle, workflowRef contractsv1.WorkflowRef, nodeID string, material ReplayMaterial, childReplay contractsv1.ReplayBundle) error {
	_, ok, err := materializeProviderResult(childReplay)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("completed Node Replay has no provider result")
	}
	completedAt := time.Now().UTC()
	startedAt := nodeState(*state, workflowRef, nodeID).StartedAt
	duration := 0
	if startedAt != nil && completedAt.After(*startedAt) {
		duration = int(math.Ceil(completedAt.Sub(*startedAt).Seconds()))
	}
	usage := contractsv1.CampaignExecutionUsage{Attempts: 1, Actions: len(material.Artifacts), Candidates: artifactCandidateCount(material.Artifacts, material.Invocation.Node.OutputSlots), DurationSeconds: duration}
	status := contractsv1.CampaignNodeExecutionStatusCompleted
	if len(material.Artifacts) == 0 {
		status = contractsv1.CampaignNodeExecutionStatusCompletedNoAction
	}
	return e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeNodeCompleted, completedAt, []contractsv1.SHA256{material.Invocation.Bundle.BundleHash}, []contractsv1.SHA256{childReplay.BundleHash}, map[string]any{"workflow_ref": workflowRef, "node_id": nodeID, "status": status, "usage": usage, "completed_at": completedAt, "result_replay_hash": childReplay.BundleHash})
}

func (e *Engine) exhaustCampaignNode(state *contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle, workflowRef contractsv1.WorkflowRef, nodeID string, code contractsv1.Identifier) error {
	return e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeBudgetExhausted, time.Now().UTC(), []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"workflow_ref": workflowRef, "node_id": nodeID, "blocker_code": code})
}

func (e *Engine) completeCampaign(state *contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle) error {
	return e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeTerminal, time.Now().UTC(), []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"state": "completed"})
}

func (e *Engine) appendCampaignEvent(replay contractsv1.ReplayBundle, receiptType contractsv1.ReceiptReceiptType, at time.Time, inputs, outputs []contractsv1.SHA256, payload map[string]any) error {
	previous := replay.Receipts[len(replay.Receipts)-1]
	receipt, err := sealReceiptVersion(2, replay.AggregateId, previous.AggregateVersion+1, receiptType, at, &previous.ReceiptHash, inputs, outputs, payload)
	if err != nil {
		return err
	}
	return e.ledger.Append(receipt)
}

func (e *Engine) reduceCampaignReplay(replay contractsv1.ReplayBundle, prepared preparedCampaign) (contractsv1.CampaignExecutionState, error) {
	if err := VerifyReplay(replay); err != nil {
		return contractsv1.CampaignExecutionState{}, err
	}
	if len(replay.Receipts) == 0 || replay.Receipts[0].ReceiptType != contractsv1.ReceiptReceiptTypeCampaignAdmission || replay.Receipts[0].SchemaVersion != 2 {
		return contractsv1.CampaignExecutionState{}, errors.New("Campaign Replay does not start with a v2 admission")
	}
	var state contractsv1.CampaignExecutionState
	if err := decodePayload(replay.Receipts[0].Payload["state"], &state); err != nil {
		return contractsv1.CampaignExecutionState{}, err
	}
	expected := prepared.initial
	expected.StartedAt, expected.UpdatedAt = state.StartedAt, state.UpdatedAt
	wantInputs := campaignAdmissionInputs(prepared, state)
	stateHash, err := Digest(state)
	if err != nil {
		return contractsv1.CampaignExecutionState{}, err
	}
	if !state.StartedAt.Equal(replay.Receipts[0].OccurredAt) || !state.UpdatedAt.Equal(state.StartedAt) || !reflect.DeepEqual(state, expected) || !reflect.DeepEqual(replay.Receipts[0].InputHashes, wantInputs) || !reflect.DeepEqual(replay.Receipts[0].OutputHashes, []contractsv1.SHA256{contractsv1.SHA256(stateHash)}) {
		return contractsv1.CampaignExecutionState{}, errors.New("Campaign Replay definition binding does not match")
	}
	for _, receipt := range replay.Receipts[1:] {
		if receipt.SchemaVersion != 2 {
			return state, errors.New("Campaign Replay contains a non-v2 receipt")
		}
		if receipt.OccurredAt.Before(state.UpdatedAt) {
			return state, errors.New("Campaign Replay receipt predates canonical state")
		}
		switch receipt.ReceiptType {
		case contractsv1.ReceiptReceiptTypeAttemptReserved:
			var workflowRef contractsv1.WorkflowRef
			var nodeID string
			var startedAt time.Time
			if err := decodePayload(receipt.Payload["workflow_ref"], &workflowRef); err != nil {
				return state, err
			}
			if err := decodePayload(receipt.Payload["node_id"], &nodeID); err != nil {
				return state, err
			}
			if err := decodePayload(receipt.Payload["started_at"], &startedAt); err != nil {
				return state, err
			}
			if !startedAt.Equal(receipt.OccurredAt) || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{state.CampaignHash}) || len(receipt.OutputHashes) != 0 {
				return state, errors.New("Campaign attempt reservation binding is invalid")
			}
			node := nodeStatePtr(&state, workflowRef, nodeID)
			if node == nil || node.Status != contractsv1.CampaignNodeExecutionStatusPending || !campaignNodeReady(state, prepared, workflowRef, nodeID) {
				return state, errors.New("Campaign attempt reservation is not eligible")
			}
			node.Status = contractsv1.CampaignNodeExecutionStatusRunning
			node.StartedAt = &startedAt
			node.Usage.Attempts++
			state.Usage.Attempts++
			state.Status = contractsv1.CampaignExecutionStateStatusRunning
		case contractsv1.ReceiptReceiptTypeNodeCompleted:
			var workflowRef contractsv1.WorkflowRef
			var nodeID string
			var status contractsv1.CampaignNodeExecutionStatus
			var usage contractsv1.CampaignExecutionUsage
			var completedAt time.Time
			var replayHash contractsv1.SHA256
			if err := decodePayload(receipt.Payload["workflow_ref"], &workflowRef); err != nil {
				return state, err
			}
			if err := decodePayload(receipt.Payload["node_id"], &nodeID); err != nil {
				return state, err
			}
			if err := decodePayload(receipt.Payload["status"], &status); err != nil {
				return state, err
			}
			if err := decodePayload(receipt.Payload["usage"], &usage); err != nil {
				return state, err
			}
			if err := decodePayload(receipt.Payload["completed_at"], &completedAt); err != nil {
				return state, err
			}
			if err := decodePayload(receipt.Payload["result_replay_hash"], &replayHash); err != nil {
				return state, err
			}
			node := nodeStatePtr(&state, workflowRef, nodeID)
			if node == nil || node.Status != contractsv1.CampaignNodeExecutionStatusRunning || (status != contractsv1.CampaignNodeExecutionStatusCompleted && status != contractsv1.CampaignNodeExecutionStatusCompletedNoAction) || usage.Attempts != 1 {
				return state, errors.New("Campaign Node completion is not eligible")
			}
			workflow, ok := preparedWorkflowByRef(prepared, workflowRef)
			if !ok {
				return state, errors.New("Campaign Node completion Workflow is not pinned")
			}
			childID, err := executionID(RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: nodeID})
			if err != nil {
				return state, err
			}
			childHead, err := e.ledger.Replay(childID)
			if err != nil || !reflect.DeepEqual(receipt.OutputHashes, []contractsv1.SHA256{replayHash}) {
				return state, errors.New("Campaign Node completion has no exact child Replay")
			}
			childReplay, err := replayAtBundleHash(childHead, replayHash)
			if err != nil || !nodeCompletedReplay(childReplay) {
				return state, errors.New("Campaign Node completion has no exact child Replay")
			}
			invocation, err := VerifyDefinitionBindingWithAdmission(childReplay, workflow.admissionReplay, prepared.request.Job, prepared.request.Campaign, workflow.definition)
			if err != nil || string(invocation.Node.Id) != nodeID || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{invocation.Bundle.BundleHash}) {
				return state, errors.New("Campaign Node completion child binding is invalid")
			}
			material, err := MaterializeReplay(childReplay, e.outputs)
			if err != nil {
				return state, err
			}
			_, hasResult, err := materializeProviderResult(childReplay)
			duration := 0
			if node.StartedAt != nil && completedAt.After(*node.StartedAt) {
				duration = int(math.Ceil(completedAt.Sub(*node.StartedAt).Seconds()))
			}
			if err != nil || !hasResult || !completedAt.Equal(receipt.OccurredAt) || usage.Actions != len(material.Artifacts) || usage.Candidates != artifactCandidateCount(material.Artifacts, invocation.Node.OutputSlots) || usage.DurationSeconds != duration {
				return state, errors.New("Campaign Node completion usage does not match the child Replay")
			}
			node.Status, node.CompletedAt, node.ResultReplayHash = status, &completedAt, &replayHash
			node.Usage.Actions, node.Usage.Candidates, node.Usage.DurationSeconds = usage.Actions, usage.Candidates, usage.DurationSeconds
			state.Usage.Actions += usage.Actions
			state.Usage.Candidates += usage.Candidates
			state.Usage.DurationSeconds += usage.DurationSeconds
		case contractsv1.ReceiptReceiptTypeBudgetExhausted:
			var workflowRef contractsv1.WorkflowRef
			var nodeID string
			var blocker contractsv1.Identifier
			if err := decodePayload(receipt.Payload["workflow_ref"], &workflowRef); err != nil {
				return state, err
			}
			if err := decodePayload(receipt.Payload["node_id"], &nodeID); err != nil {
				return state, err
			}
			if err := decodePayload(receipt.Payload["blocker_code"], &blocker); err != nil {
				return state, err
			}
			if !knownBudgetBlocker(blocker) || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{state.CampaignHash}) || len(receipt.OutputHashes) != 0 {
				return state, errors.New("Campaign budget exhaustion binding is invalid")
			}
			node := nodeStatePtr(&state, workflowRef, nodeID)
			if node == nil || (node.Status != contractsv1.CampaignNodeExecutionStatusRunning && (node.Status != contractsv1.CampaignNodeExecutionStatusPending || !campaignNodeReady(state, prepared, workflowRef, nodeID))) {
				return state, errors.New("Campaign budget exhaustion is not eligible")
			}
			node.Status, node.BlockerCode = contractsv1.CampaignNodeExecutionStatusBudgetExhausted, &blocker
			state.Status, state.BlockerCode = contractsv1.CampaignExecutionStateStatusBlocked, &blocker
		case contractsv1.ReceiptReceiptTypeTerminal:
			var terminal string
			if err := decodePayload(receipt.Payload["state"], &terminal); err != nil {
				return state, err
			}
			if terminal != "completed" || !allNodesCompleted(state) || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{state.CampaignHash}) || len(receipt.OutputHashes) != 0 {
				return state, errors.New("Campaign terminal receipt is not eligible")
			}
			state.Status = contractsv1.CampaignExecutionStateStatusCompleted
		default:
			return state, fmt.Errorf("unsupported Campaign receipt type %q", receipt.ReceiptType)
		}
		state.UpdatedAt = receipt.OccurredAt
	}
	if err := contract.ValidateDefinition("CampaignExecutionState", state); err != nil {
		return state, err
	}
	return state, nil
}

func replayAtBundleHash(head contractsv1.ReplayBundle, hash contractsv1.SHA256) (contractsv1.ReplayBundle, error) {
	if err := VerifyReplay(head); err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	// ponytail: execution histories are bounded; add a receipt-hash index if
	// profiling shows prefix reconstruction dominates long-lived aggregates.
	for count := len(head.Receipts); count > 0; count-- {
		prefix, err := ReplayPrefix(head, count)
		if err != nil {
			return contractsv1.ReplayBundle{}, err
		}
		if prefix.BundleHash == hash {
			return prefix, nil
		}
	}
	return contractsv1.ReplayBundle{}, errors.New("Replay bundle hash is not in canonical history")
}

func knownBudgetBlocker(code contractsv1.Identifier) bool {
	switch code {
	case "attempt-budget-exhausted", "action-budget-exhausted", "candidate-budget-exhausted", "duration-budget-exhausted":
		return true
	default:
		return false
	}
}

func preparedWorkflowByRef(prepared preparedCampaign, ref contractsv1.WorkflowRef) (preparedWorkflow, bool) {
	for _, workflow := range prepared.workflows {
		if workflow.compiled.WorkflowRef == ref {
			return workflow, true
		}
	}
	return preparedWorkflow{}, false
}

func preparedNode(prepared preparedCampaign, ref contractsv1.WorkflowRef, nodeID string) (preparedWorkflow, CompiledNode, bool) {
	workflow, ok := preparedWorkflowByRef(prepared, ref)
	if !ok {
		return preparedWorkflow{}, CompiledNode{}, false
	}
	for _, node := range workflow.compiled.Nodes {
		if string(node.Definition.Id) == nodeID {
			return workflow, node, true
		}
	}
	return preparedWorkflow{}, CompiledNode{}, false
}

func deriveNext(state *contractsv1.CampaignExecutionState, prepared preparedCampaign) {
	state.NextWorkflowRef = nil
	state.NextNodeId = nil
	if state.Status == contractsv1.CampaignExecutionStateStatusBlocked || state.Status == contractsv1.CampaignExecutionStateStatusCompleted || state.Status == contractsv1.CampaignExecutionStateStatusTerminal {
		return
	}
	for _, workflow := range prepared.workflows {
		ref := workflow.compiled.WorkflowRef
		if workflowCompleted(*state, ref) {
			continue
		}
		for _, compiledNode := range workflow.compiled.Nodes {
			node := nodeState(*state, ref, string(compiledNode.Definition.Id))
			if node.Status == contractsv1.CampaignNodeExecutionStatusRunning {
				workflowRef, id := ref, node.NodeId
				state.NextWorkflowRef, state.NextNodeId = &workflowRef, &id
				return
			}
		}
		for _, compiledNode := range workflow.compiled.Nodes {
			if campaignNodeReady(*state, prepared, ref, string(compiledNode.Definition.Id)) {
				workflowRef, id := ref, compiledNode.Definition.Id
				state.NextWorkflowRef, state.NextNodeId = &workflowRef, &id
				return
			}
		}
		return
	}
	if allNodesCompleted(*state) {
		state.Status = contractsv1.CampaignExecutionStateStatusRunning
	}
}

func campaignNodeReady(state contractsv1.CampaignExecutionState, prepared preparedCampaign, ref contractsv1.WorkflowRef, nodeID string) bool {
	for _, workflow := range prepared.workflows {
		if workflowCompleted(state, workflow.compiled.WorkflowRef) {
			continue
		}
		if workflow.compiled.WorkflowRef != ref {
			return false
		}
		_, node, ok := preparedNode(prepared, ref, nodeID)
		if !ok || nodeState(state, ref, nodeID).Status != contractsv1.CampaignNodeExecutionStatusPending {
			return false
		}
		for _, dependency := range node.Definition.DependsOn {
			status := nodeState(state, ref, dependency).Status
			if status != contractsv1.CampaignNodeExecutionStatusCompleted && status != contractsv1.CampaignNodeExecutionStatusCompletedNoAction {
				return false
			}
		}
		return true
	}
	return false
}

func workflowCompleted(state contractsv1.CampaignExecutionState, ref contractsv1.WorkflowRef) bool {
	found := false
	for _, node := range state.Nodes {
		if node.WorkflowRef != ref {
			continue
		}
		found = true
		if node.Status != contractsv1.CampaignNodeExecutionStatusCompleted && node.Status != contractsv1.CampaignNodeExecutionStatusCompletedNoAction {
			return false
		}
	}
	return found
}

func nextAction(state contractsv1.CampaignExecutionState) contractsv1.CampaignDrivePreviewNextAction {
	if state.Status == contractsv1.CampaignExecutionStateStatusBlocked || state.Status == contractsv1.CampaignExecutionStateStatusTerminal {
		return contractsv1.CampaignDrivePreviewNextActionBlocked
	}
	if state.Status == contractsv1.CampaignExecutionStateStatusCompleted || (state.NextNodeId == nil && allNodesCompleted(state)) {
		return contractsv1.CampaignDrivePreviewNextActionComplete
	}
	if state.NextNodeId != nil {
		return contractsv1.CampaignDrivePreviewNextActionRunNode
	}
	return contractsv1.CampaignDrivePreviewNextActionWait
}

func remainingBudget(state contractsv1.CampaignExecutionState, campaign contractsv1.Budget, workflowRef contractsv1.WorkflowRef, node contractsv1.NodeDefinition) (contractsv1.Budget, contractsv1.Identifier) {
	current := nodeState(state, workflowRef, string(node.Id))
	nodeUsage := current.Usage
	attempts := minInt(node.Budget.MaxAttempts-nodeUsage.Attempts, campaign.MaxAttempts-state.Usage.Attempts)
	if current.Status == contractsv1.CampaignNodeExecutionStatusRunning {
		// Redelivery resumes the already-reserved logical attempt with the same
		// idempotency key; it does not consume a second attempt.
		attempts = 1
	}
	actions := minInt(node.Budget.MaxActions-nodeUsage.Actions, campaign.MaxActions-state.Usage.Actions)
	candidates := minInt(node.Budget.MaxCandidates-nodeUsage.Candidates, campaign.MaxCandidates-state.Usage.Candidates)
	if attempts < 1 {
		return contractsv1.Budget{}, "attempt-budget-exhausted"
	}
	if actions < requiredOutputCount(node.OutputSlots, false) {
		return contractsv1.Budget{}, "action-budget-exhausted"
	}
	if candidates < requiredOutputCount(node.OutputSlots, true) {
		return contractsv1.Budget{}, "candidate-budget-exhausted"
	}
	var duration *int
	if campaign.MaxDurationSeconds != nil {
		campaignStartedAt, started := firstCampaignReservation(state)
		if started && !time.Now().Before(campaignStartedAt.Add(time.Duration(*campaign.MaxDurationSeconds)*time.Second)) {
			return contractsv1.Budget{}, "duration-budget-exhausted"
		}
		allocated := *campaign.MaxDurationSeconds
		if started && current.StartedAt != nil {
			allocated = int(math.Floor(campaignStartedAt.Add(time.Duration(*campaign.MaxDurationSeconds) * time.Second).Sub(*current.StartedAt).Seconds()))
		}
		if allocated < 1 {
			return contractsv1.Budget{}, "duration-budget-exhausted"
		}
		duration = &allocated
	}
	return contractsv1.Budget{MaxAttempts: attempts, MaxActions: actions, MaxCandidates: candidates, MaxDurationSeconds: duration}, ""
}

func firstCampaignReservation(state contractsv1.CampaignExecutionState) (time.Time, bool) {
	var first time.Time
	for _, node := range state.Nodes {
		if node.StartedAt != nil && (first.IsZero() || node.StartedAt.Before(first)) {
			first = *node.StartedAt
		}
	}
	return first, !first.IsZero()
}

func requiredOutputCount(slots []contractsv1.Slot, candidatesOnly bool) int {
	total := 0
	for _, slot := range slots {
		if candidatesOnly && !slot.CountsAsCandidates {
			continue
		}
		total += slot.MinItems
	}
	return total
}

func campaignResultBudgetBlocker(artifacts []contractsv1.ActionArtifact, slots []contractsv1.Slot, budget contractsv1.Budget) contractsv1.Identifier {
	if len(artifacts) > budget.MaxActions {
		return "action-budget-exhausted"
	}
	if artifactCandidateCount(artifacts, slots) > budget.MaxCandidates {
		return "candidate-budget-exhausted"
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func nodeState(state contractsv1.CampaignExecutionState, workflowRef contractsv1.WorkflowRef, nodeID string) contractsv1.CampaignNodeExecution {
	for _, node := range state.Nodes {
		if node.WorkflowRef == workflowRef && string(node.NodeId) == nodeID {
			return node
		}
	}
	return contractsv1.CampaignNodeExecution{}
}

func nodeStatePtr(state *contractsv1.CampaignExecutionState, workflowRef contractsv1.WorkflowRef, nodeID string) *contractsv1.CampaignNodeExecution {
	for index := range state.Nodes {
		if state.Nodes[index].WorkflowRef == workflowRef && string(state.Nodes[index].NodeId) == nodeID {
			return &state.Nodes[index]
		}
	}
	return nil
}

func nodeCompletedReplay(replay contractsv1.ReplayBundle) bool {
	if len(replay.Receipts) == 0 {
		return false
	}
	last := replay.Receipts[len(replay.Receipts)-1]
	if last.ReceiptType != contractsv1.ReceiptReceiptTypeTerminal {
		return false
	}
	var state string
	return decodePayload(last.Payload["state"], &state) == nil && state == "node_completed"
}

func allNodesCompleted(state contractsv1.CampaignExecutionState) bool {
	for _, node := range state.Nodes {
		if node.Status != contractsv1.CampaignNodeExecutionStatusCompleted && node.Status != contractsv1.CampaignNodeExecutionStatusCompletedNoAction {
			return false
		}
	}
	return true
}
