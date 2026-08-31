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

// CampaignRuntime is the supported downstream embedding seam for canonical
// whole-Workflow execution. Callers may bound work per delivery, but they
// cannot select the next Node. Durable workers should Drive one transition per
// delivery and treat a successful typed wait as state, not as a transport
// error.
type CampaignRuntime interface {
	Preview(context.Context, CampaignRunRequest) (contractsv1.CampaignDrivePreview, error)
	Drive(context.Context, CampaignDriveCommand) (contractsv1.CampaignDriveReceipt, error)
	ReplayAt(context.Context, CampaignRef, ReceiptID, ReplayView) (CampaignReplay, error)
}

var _ CampaignRuntime = (*Engine)(nil)

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
	Signal         *CampaignSignal
}

type CampaignSignal struct {
	Name        contractsv1.Identifier
	PayloadHash contractsv1.SHA256
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
	resumingContext := false
	if preview.State.NextWorkflowRef != nil && preview.State.NextNodeId != nil {
		resumingContext = nodeState(preview.State, *preview.State.NextWorkflowRef, string(*preview.State.NextNodeId)).Status == contractsv1.CampaignNodeExecutionStatusNeedsContext
	}
	if (preview.NextAction != contractsv1.CampaignDrivePreviewNextActionRunNode && !resumingContext) || preview.State.NextWorkflowRef == nil || *preview.State.NextWorkflowRef != workflowRef || preview.State.NextNodeId == nil || string(*preview.State.NextNodeId) != request.NodeID {
		return RunResult{}, fmt.Errorf("node %q is not the Core-derived next ready Node", request.NodeID)
	}
	receipt, err := e.drive(ctx, CampaignDriveCommand{CampaignRunRequest: CampaignRunRequest{Job: request.Job, Campaign: request.Campaign, Workflow: request.Workflow}, MaxTransitions: 1})
	if err != nil {
		return RunResult{}, err
	}
	if receipt.NodeReplay == nil {
		if receipt.CampaignReplay != nil && len(receipt.CampaignReplay.Receipts) > 0 {
			last := receipt.CampaignReplay.Receipts[len(receipt.CampaignReplay.Receipts)-1]
			if last.ReceiptType == contractsv1.ReceiptReceiptTypeNeedsContext {
				var payload contractsv1.NeedsContextEventPayload
				if err := decodePayload(last.Payload, &payload); err == nil {
					reasons := make(map[string]string, len(payload.Reasons))
					for requirement, reason := range payload.Reasons {
						reasons[requirement] = string(reason)
					}
					return RunResult{}, &NeedsContextError{Requirements: []string(payload.Requirements), Reasons: reasons}
				}
			}
		}
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
		if err := e.rejectLegacyCampaignChildren(prepared); err != nil {
			return contractsv1.CampaignDriveReceipt{}, err
		}
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
		if !nodeUsesProvider(node.Definition) {
			advanced, err := e.driveCoreNode(command, state, *replay, prepared, workflow, node)
			if err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			if !advanced {
				break
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
			continue
		}
		if _, ok := e.ledger.(AtomicLedger); !ok {
			return contractsv1.CampaignDriveReceipt{}, errors.New("provider execution requires an AtomicLedger")
		}
		currentNode := nodeState(state, workflowRef, nodeID)
		resolved := resolvedContext{}
		if currentNode.ContextBundleHash != nil && currentNode.Status != contractsv1.CampaignNodeExecutionStatusNeedsContext {
			resolved, err = contextFromCampaignReplay(*replay, workflowRef, nodeID, *currentNode.ContextBundleHash)
		} else {
			resolved, err = resolveContext(ctx, e.registry, RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: nodeID}, workflow.compiled, node, workflow.compileReceipt)
		}
		if err != nil {
			var missing *NeedsContextError
			if !errors.As(err, &missing) {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			advanced, appendErr := e.recordNeedsContext(state, *replay, workflowRef, nodeID, missing)
			if appendErr != nil {
				return contractsv1.CampaignDriveReceipt{}, appendErr
			}
			if advanced {
				transitions++
			}
			break
		}
		if currentNode.Status == contractsv1.CampaignNodeExecutionStatusNeedsContext {
			if err := e.recordContextTransition(*replay, contractsv1.ReceiptReceiptTypeContextAvailable, workflowRef, nodeID, resolved, currentNode.BlockerFingerprint); err != nil {
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
			continue
		}
		if currentNode.ContextBundleHash == nil {
			if err := e.recordContextTransition(*replay, contractsv1.ReceiptReceiptTypeContextBound, workflowRef, nodeID, resolved, nil); err != nil {
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
			continue
		}
		remaining, blocker := e.remainingBudget(state, prepared.request.Campaign.Budget, workflowRef, node.Definition)
		if blocker != "" {
			if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, blocker, nil); err != nil {
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
			remaining, blocker = e.remainingBudget(state, prepared.request.Campaign.Budget, workflowRef, node.Definition)
			if blocker != "" {
				if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, blocker, nil); err != nil {
					return contractsv1.CampaignDriveReceipt{}, err
				}
				transitions++
				break
			}
		}
		startedAt := nodeState(state, workflowRef, nodeID).StartedAt
		run, runErr := e.runAgentNodeResolvedAt(ctx, RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: nodeID, BudgetOverride: &remaining}, startedAt, &resolved)
		if runErr != nil {
			var exceeded budgetExceededError
			if errors.As(runErr, &exceeded) {
				childID, idErr := executionID(RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: nodeID})
				if idErr != nil {
					return contractsv1.CampaignDriveReceipt{}, idErr
				}
				childReplay, replayErr := e.ledger.Replay(childID)
				if replayErr != nil {
					return contractsv1.CampaignDriveReceipt{}, replayErr
				}
				if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, exceeded.code, &childReplay); err != nil {
					return contractsv1.CampaignDriveReceipt{}, err
				}
				transitions++
				break
			}
			if errors.Is(runErr, ErrProviderNotReady) {
				break
			}
			if errors.Is(runErr, ErrProviderDeadline) {
				childID, idErr := executionID(RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: nodeID})
				if idErr != nil {
					return contractsv1.CampaignDriveReceipt{}, idErr
				}
				childReplay, replayErr := e.ledger.Replay(childID)
				if replayErr != nil {
					return contractsv1.CampaignDriveReceipt{}, replayErr
				}
				if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, "duration-budget-exhausted", &childReplay); err != nil {
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
			if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, blocker, &run.Replay); err != nil {
				return contractsv1.CampaignDriveReceipt{}, err
			}
			transitions++
			break
		}
		if _, blocker := e.remainingBudget(state, prepared.request.Campaign.Budget, workflowRef, node.Definition); blocker != "" {
			if err := e.exhaustCampaignNode(&state, *replay, workflowRef, nodeID, blocker, nil); err != nil {
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
	compileReceipt  contractsv1.Receipt
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
	isolation, err := e.providerIsolation()
	if err != nil {
		return preparedCampaign{}, err
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
	now := e.now()
	state := contractsv1.CampaignExecutionState{
		Kind: contractsv1.CampaignExecutionStateKindCampaignExecutionState, SchemaVersion: 3,
		AggregateId: aggregateID, JobId: request.Job.Id, CampaignId: request.Campaign.Id,
		JobHash: jobHash, CampaignHash: campaignHash, WorkflowHashes: contractsv1.CampaignExecutionStateWorkflowHashes{},
		Status: contractsv1.CampaignExecutionStateStatusAdmitted, StartedAt: now, UpdatedAt: now, ProviderIsolation: &isolation,
	}
	prepared := preparedCampaign{request: request, initial: state}
	for index, definition := range definitions {
		run.Workflow = definition
		compiled, compileReceipt, err := compileWorkflow(definition, e.registry, aggregateID, now)
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
		compiled, compileReceipt, err = compileWorkflow(definition, e.registry, aggregateID, admission.Receipt.OccurredAt)
		if err != nil {
			return preparedCampaign{}, err
		}
		compileReceipt, err = bindCompileReceiptToAdmission(compileReceipt, admission.Receipt.ReceiptHash)
		if err != nil {
			return preparedCampaign{}, err
		}
		prepared.workflows = append(prepared.workflows, preparedWorkflow{definition: definition, compiled: compiled, compileReceipt: compileReceipt, admission: admission, admissionReplay: admissionReplay})
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

func (e *Engine) rejectLegacyCampaignChildren(prepared preparedCampaign) error {
	for _, workflow := range prepared.workflows {
		for _, node := range workflow.compiled.Nodes {
			childID, err := executionID(RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: string(node.Definition.Id)})
			if err != nil {
				return err
			}
			if _, err := e.ledger.Replay(childID); err == nil {
				return errors.New("legacy v1 execution is read-only and cannot be adopted by a Campaign")
			} else if !errors.Is(err, ErrReplayEmpty) {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) admitCampaign(prepared preparedCampaign, state contractsv1.CampaignExecutionState) error {
	hash, err := Digest(state)
	if err != nil {
		return err
	}
	receiptSchema := 2
	if state.SchemaVersion >= 3 {
		receiptSchema = 3
	}
	receipt, err := sealReceiptVersion(receiptSchema, state.AggregateId, 1, contractsv1.ReceiptReceiptTypeCampaignAdmission, state.StartedAt, nil,
		campaignAdmissionInputs(prepared, state), []contractsv1.SHA256{contractsv1.SHA256(hash)}, map[string]any{"state": state})
	if err != nil {
		return err
	}
	return e.ledger.Append(receipt)
}

func campaignAdmissionInputs(prepared preparedCampaign, state contractsv1.CampaignExecutionState) []contractsv1.SHA256 {
	inputs := []contractsv1.SHA256{state.JobHash, state.CampaignHash}
	if state.SchemaVersion >= 3 && state.ProviderIsolation != nil {
		inputs = append(inputs, state.ProviderIsolation.EvidenceHash)
	}
	for _, workflow := range prepared.workflows {
		inputs = append(inputs, workflow.admission.Receipt.ReceiptHash, workflow.compiled.DefinitionHash, workflow.compiled.CompileHash)
	}
	return inputs
}

func (e *Engine) reserveCampaignAttempt(state *contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle, workflowRef contractsv1.WorkflowRef, nodeID string) error {
	now := e.now()
	return e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeAttemptReserved, now, []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"workflow_ref": workflowRef, "node_id": nodeID, "started_at": now})
}

func (e *Engine) completeCampaignNode(state *contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle, workflowRef contractsv1.WorkflowRef, nodeID string, material ReplayMaterial, childReplay contractsv1.ReplayBundle) error {
	result, ok, err := materializeProviderResult(childReplay)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("completed Node Replay has no provider result")
	}
	completedAt := e.now()
	startedAt := nodeState(*state, workflowRef, nodeID).StartedAt
	duration := 0
	if startedAt != nil && completedAt.After(*startedAt) {
		duration = int(math.Ceil(completedAt.Sub(*startedAt).Seconds()))
	}
	usage := contractsv1.CampaignExecutionUsage{Attempts: 1, Actions: len(material.Artifacts), Candidates: artifactCandidateCount(material.Artifacts, material.Invocation.Node.OutputSlots), DurationSeconds: duration}
	status := providerOutcome(result)
	route, err := providerRoute(result, material.Invocation.Node)
	if err != nil {
		return err
	}
	payload := map[string]any{"workflow_ref": workflowRef, "node_id": nodeID, "status": status, "route": route, "usage": usage, "completed_at": completedAt, "result_replay_hash": childReplay.BundleHash}
	if result.BlockerCode != nil {
		payload["blocker_code"] = *result.BlockerCode
	}
	return e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeNodeCompleted, completedAt, []contractsv1.SHA256{material.Invocation.Bundle.BundleHash}, []contractsv1.SHA256{childReplay.BundleHash}, payload)
}

func (e *Engine) exhaustCampaignNode(state *contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle, workflowRef contractsv1.WorkflowRef, nodeID string, code contractsv1.Identifier, childReplay *contractsv1.ReplayBundle) error {
	inputs := []contractsv1.SHA256{state.CampaignHash}
	var outputs []contractsv1.SHA256
	payload := map[string]any{"workflow_ref": workflowRef, "node_id": nodeID, "blocker_code": code}
	if childReplay != nil {
		invocation, err := MaterializeInvocation(*childReplay)
		if err != nil {
			return err
		}
		inputs, outputs = []contractsv1.SHA256{invocation.Bundle.BundleHash}, []contractsv1.SHA256{childReplay.BundleHash}
		payload["result_replay_hash"] = childReplay.BundleHash
	}
	return e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeBudgetExhausted, e.now(), inputs, outputs, payload)
}

func (e *Engine) completeCampaign(state *contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle) error {
	return e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeTerminal, e.now(), []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"state": "completed"})
}

func (e *Engine) appendCampaignEvent(replay contractsv1.ReplayBundle, receiptType contractsv1.ReceiptReceiptType, at time.Time, inputs, outputs []contractsv1.SHA256, payload map[string]any) error {
	previous := replay.Receipts[len(replay.Receipts)-1]
	receipt, err := sealReceiptVersion(2, replay.AggregateId, previous.AggregateVersion+1, receiptType, at, &previous.ReceiptHash, inputs, outputs, payload)
	if err != nil {
		return err
	}
	return e.ledger.Append(receipt)
}

func (e *Engine) recordNeedsContext(state contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle, workflowRef contractsv1.WorkflowRef, nodeID string, missing *NeedsContextError) (bool, error) {
	fingerprint, err := Digest(struct {
		WorkflowRef  contractsv1.WorkflowRef
		NodeID       string
		Requirements []string
		Reasons      map[string]string
	}{workflowRef, nodeID, missing.Requirements, missing.Reasons})
	if err != nil {
		return false, err
	}
	value := contractsv1.SHA256(fingerprint)
	current := nodeState(state, workflowRef, nodeID)
	if current.Status == contractsv1.CampaignNodeExecutionStatusNeedsContext && current.BlockerFingerprint != nil && *current.BlockerFingerprint == value {
		return false, nil
	}
	return true, e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeNeedsContext, e.now(), []contractsv1.SHA256{state.CampaignHash}, []contractsv1.SHA256{value}, map[string]any{
		"workflow_ref": workflowRef, "node_id": nodeID, "requirements": missing.Requirements, "reasons": missing.Reasons, "blocker_fingerprint": value,
	})
}

func (e *Engine) recordContextTransition(replay contractsv1.ReplayBundle, receiptType contractsv1.ReceiptReceiptType, workflowRef contractsv1.WorkflowRef, nodeID string, resolved resolvedContext, previous *contractsv1.SHA256) error {
	payload := map[string]any{"workflow_ref": workflowRef, "node_id": nodeID, "bundle": resolved.Bundle, "packs": resolved.Packs}
	if previous != nil {
		payload["previous_blocker_fingerprint"] = *previous
	}
	return e.appendCampaignEvent(replay, receiptType, e.now(), []contractsv1.SHA256{resolved.Bundle.BundleHash}, []contractsv1.SHA256{resolved.Bundle.BundleHash}, payload)
}

func contextFromCampaignReplay(replay contractsv1.ReplayBundle, workflowRef contractsv1.WorkflowRef, nodeID string, bundleHash contractsv1.SHA256) (resolvedContext, error) {
	for index := len(replay.Receipts) - 1; index >= 0; index-- {
		receipt := replay.Receipts[index]
		if receipt.ReceiptType != contractsv1.ReceiptReceiptTypeContextBound && receipt.ReceiptType != contractsv1.ReceiptReceiptTypeContextAvailable {
			continue
		}
		var payload contractsv1.ContextTransitionEventPayload
		if err := decodePayload(receipt.Payload, &payload); err != nil {
			return resolvedContext{}, err
		}
		if payload.WorkflowRef == workflowRef && string(payload.NodeId) == nodeID && payload.Bundle.BundleHash == bundleHash {
			if err := VerifyContextBundle(payload.Bundle, payload.Packs); err != nil {
				return resolvedContext{}, err
			}
			return resolvedContext{Bundle: payload.Bundle, Packs: payload.Packs}, nil
		}
	}
	return resolvedContext{}, errors.New("canonical Context binding is absent from the Campaign Replay")
}

func (e *Engine) driveCoreNode(command CampaignDriveCommand, state contractsv1.CampaignExecutionState, replay contractsv1.ReplayBundle, prepared preparedCampaign, workflow preparedWorkflow, node CompiledNode) (bool, error) {
	ref, id := workflow.compiled.WorkflowRef, string(node.Definition.Id)
	current := nodeState(state, ref, id)
	now := e.now()
	switch node.Definition.Kind {
	case contractsv1.NodeDefinitionKindDeterministic, contractsv1.NodeDefinitionKindTerminal:
		status := contractsv1.CampaignNodeExecutionStatusCompleted
		if len(node.Definition.OutputSlots) == 0 {
			status = contractsv1.CampaignNodeExecutionStatusCompletedNoAction
		}
		return true, e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeCoreCompleted, now, []contractsv1.SHA256{state.CampaignHash}, nil, map[string]any{"workflow_ref": ref, "node_id": id, "status": status, "completed_at": now})
	case contractsv1.NodeDefinitionKindWait:
		if current.Status == contractsv1.CampaignNodeExecutionStatusPending {
			payload := map[string]any{"workflow_ref": ref, "node_id": id, "mode": *node.Definition.WaitMode, "started_at": now}
			if *node.Definition.WaitMode == contractsv1.NodeDefinitionWaitModeTime {
				payload["wake_at"] = now.Add(time.Duration(*node.Definition.WaitDelaySeconds) * time.Second)
			} else {
				payload["signal"] = *node.Definition.WaitSignal
			}
			return true, e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeWaitStarted, now, []contractsv1.SHA256{state.CampaignHash}, nil, payload)
		}
		if current.Status != contractsv1.CampaignNodeExecutionStatusWaiting {
			return false, errors.New("wait Node is in an invalid state")
		}
		payload := map[string]any{"workflow_ref": ref, "node_id": id, "resumed_at": now}
		inputs := []contractsv1.SHA256{state.CampaignHash}
		if current.WakeAt != nil {
			if now.Before(*current.WakeAt) {
				return false, nil
			}
		} else {
			if command.Signal == nil || current.Signal == nil || command.Signal.Name != *current.Signal {
				return false, nil
			}
			payload["signal_hash"] = command.Signal.PayloadHash
			payload["signal"] = command.Signal.Name
			inputs = []contractsv1.SHA256{command.Signal.PayloadHash}
		}
		return true, e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeWaitResumed, now, inputs, nil, payload)
	case contractsv1.NodeDefinitionKindApproval:
		if blocker := approvalActionBudgetBlocker(state, prepared.request.Campaign.Budget, ref, node.Definition); blocker != "" {
			return true, e.exhaustCampaignNode(&state, replay, ref, id, blocker, nil)
		}
		sourceReplay, action, result, err := e.approvalSource(prepared, workflow, node)
		if err != nil {
			return false, err
		}
		actionHash, err := Digest(action)
		if err != nil {
			return false, err
		}
		approvalID := contractsv1.Identifier(shortID("approval-", actionHash))
		if current.Status == contractsv1.CampaignNodeExecutionStatusPending {
			return true, e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeApprovalRequested, now, []contractsv1.SHA256{sourceReplay.BundleHash, result.ReceiptHash}, []contractsv1.SHA256{contractsv1.SHA256(actionHash)}, map[string]any{"workflow_ref": ref, "node_id": id, "approval_id": approvalID, "approval_policy": *node.Definition.ApprovalPolicy, "source_replay_hash": sourceReplay.BundleHash, "action_hash": actionHash})
		}
		approvalReplay, err := e.ledger.Replay(approvalAggregate(string(approvalID)))
		if errors.Is(err, ErrReplayEmpty) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		decision, receipt, err := verifiedApprovalDecision(approvalReplay, sourceReplay, action, result, node.Definition.ApprovalPolicy, e.approvalActors)
		if err != nil {
			return false, err
		}
		artifact, err := e.approvalDecisionArtifact(prepared.request, workflow, node, action, receipt, decision)
		if err != nil {
			return false, err
		}
		return true, e.appendCampaignEvent(replay, contractsv1.ReceiptReceiptTypeApprovalDecided, now, []contractsv1.SHA256{sourceReplay.BundleHash, receipt.ReceiptHash}, []contractsv1.SHA256{artifact.ContentSha256}, map[string]any{"workflow_ref": ref, "node_id": id, "approval_id": approvalID, "approval_receipt_hash": receipt.ReceiptHash, "decision": decision, "artifact": artifact})
	default:
		return false, fmt.Errorf("unsupported Node kind %q", node.Definition.Kind)
	}
}

func (e *Engine) approvalSource(prepared preparedCampaign, workflow preparedWorkflow, node CompiledNode) (contractsv1.ReplayBundle, contractsv1.ActionArtifact, contractsv1.Receipt, error) {
	acceptedTypes := make(map[contractsv1.Identifier]bool, len(node.Definition.InputSlots))
	for _, slot := range node.Definition.InputSlots {
		if slot.ArtifactKind != nil && *slot.ArtifactKind == contractsv1.SlotArtifactKindActionArtifact {
			acceptedTypes[slot.ArtifactType] = true
		}
	}
	var matches []struct {
		replay contractsv1.ReplayBundle
		action contractsv1.ActionArtifact
		result contractsv1.Receipt
	}
	for _, dependency := range node.Definition.DependsOn {
		childID, err := executionID(RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: dependency})
		if err != nil {
			return contractsv1.ReplayBundle{}, contractsv1.ActionArtifact{}, contractsv1.Receipt{}, err
		}
		replay, err := e.ledger.Replay(childID)
		if err != nil || !nodeCompletedReplay(replay) {
			return contractsv1.ReplayBundle{}, contractsv1.ActionArtifact{}, contractsv1.Receipt{}, errors.New("approval dependency has no completed canonical Replay")
		}
		material, err := MaterializeReplay(replay, e.outputs)
		if err != nil {
			return contractsv1.ReplayBundle{}, contractsv1.ActionArtifact{}, contractsv1.Receipt{}, err
		}
		result, ok := receiptByType(replay, contractsv1.ReceiptReceiptTypeResult)
		if !ok {
			return contractsv1.ReplayBundle{}, contractsv1.ActionArtifact{}, contractsv1.Receipt{}, errors.New("approval dependency has no result receipt")
		}
		for _, artifact := range material.Artifacts {
			if artifact.ApprovalState == contractsv1.ActionArtifactApprovalStatePending && acceptedTypes[artifact.ArtifactType] {
				matches = append(matches, struct {
					replay contractsv1.ReplayBundle
					action contractsv1.ActionArtifact
					result contractsv1.Receipt
				}{replay, artifact, result})
			}
		}
	}
	if len(matches) != 1 {
		return contractsv1.ReplayBundle{}, contractsv1.ActionArtifact{}, contractsv1.Receipt{}, errors.New("approval Node requires exactly one pending dependency action")
	}
	return matches[0].replay, matches[0].action, matches[0].result, nil
}

func verifiedApprovalDecision(replay, source contractsv1.ReplayBundle, action contractsv1.ActionArtifact, result contractsv1.Receipt, approvalPolicy *contractsv1.Identifier, approvalActors map[string]map[string]bool) (contractsv1.ApprovalOptionDecision, contractsv1.Receipt, error) {
	if err := VerifyReplay(replay); err != nil || len(replay.Receipts) != 1 {
		return "", contractsv1.Receipt{}, errors.New("approval decision Replay is invalid")
	}
	if err := VerifyReplay(source); err != nil || !nodeCompletedReplay(source) {
		return "", contractsv1.Receipt{}, errors.New("approval source Replay is invalid")
	}
	receipt := replay.Receipts[0]
	if receipt.ReceiptType != contractsv1.ReceiptReceiptTypeApproval || receipt.Actor == nil || receipt.AggregateVersion != 1 || receipt.PreviousReceiptHash != nil {
		return "", contractsv1.Receipt{}, errors.New("approval decision receipt is invalid")
	}
	var brief contractsv1.ApprovalBrief
	if err := decodePayload(receipt.Payload["brief"], &brief); err != nil || !reflect.DeepEqual(brief.Action, action) || !containsEvidenceReceipt(brief.Evidence, result) || approvalPolicy == nil || !approvalActorAllowed(approvalActors, approvalPolicy, *receipt.Actor) {
		return "", contractsv1.Receipt{}, errors.New("approval decision brief is invalid")
	}
	terminal, _ := receiptByType(source, contractsv1.ReceiptReceiptTypeTerminal)
	if _, current := receipt.Payload["preview"]; !current {
		if contract.ValidateDefinition("ApprovalBrief", brief) != nil || brief.ApprovalPolicy != nil || !validLegacyApprovalReceipt(receipt, brief, source.AggregateId, terminal.OccurredAt, action) {
			return "", contractsv1.Receipt{}, errors.New("legacy approval decision authority is invalid")
		}
		return selectedApprovalDecision(brief, receipt)
	}
	var preview contractsv1.ApprovalPreview
	if err := decodePayload(receipt.Payload["preview"], &preview); err != nil || !reflect.DeepEqual(preview.Brief, brief) || brief.ApprovalPolicy == nil || *brief.ApprovalPolicy != *approvalPolicy || preview.Actor != *receipt.Actor || preview.SourceAggregateId != source.AggregateId || preview.BaseRevision != 0 || preview.ExpiresAt == nil || receipt.OccurredAt.Before(terminal.OccurredAt) || receipt.OccurredAt.After(*preview.ExpiresAt) {
		return "", contractsv1.Receipt{}, errors.New("approval decision is not bound to the exact action and result")
	}
	if err := contract.ValidateDefinition("ApprovalPreview", preview); err != nil {
		return "", contractsv1.Receipt{}, errors.New("approval decision preview is invalid")
	}
	unsigned := preview
	unsigned.PreviewHash, unsigned.CommitToken = "", ""
	previewHash, err := Digest(unsigned)
	if err != nil || contractsv1.SHA256(previewHash) != preview.PreviewHash || fmt.Sprint(receipt.Payload["preview_hash"]) != string(preview.PreviewHash) {
		return "", contractsv1.Receipt{}, errors.New("approval decision preview hash is invalid")
	}
	token, err := Digest(struct {
		Purpose string
		Preview contractsv1.SHA256
	}{"confirm-approval", preview.PreviewHash})
	briefHash, briefErr := Digest(brief)
	if err != nil || briefErr != nil || contractsv1.SHA256(token) != preview.CommitToken || preview.BriefHash != contractsv1.SHA256(briefHash) || receipt.AggregateId != approvalAggregate(string(brief.Id)) || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{preview.BriefHash, action.ContentSha256}) || !reflect.DeepEqual(receipt.OutputHashes, []contractsv1.SHA256{preview.PreviewHash}) {
		return "", contractsv1.Receipt{}, errors.New("approval decision authority is invalid")
	}
	return selectedApprovalDecision(brief, receipt)
}

func validLegacyApprovalReceipt(receipt contractsv1.Receipt, brief contractsv1.ApprovalBrief, sourceAggregateID string, terminalAt time.Time, action contractsv1.ActionArtifact) bool {
	briefHash, err := Digest(brief)
	expiresAt := terminalAt.Add(30 * 24 * time.Hour).UTC()
	preview := contractsv1.ApprovalPreview{Kind: contractsv1.ApprovalPreviewKindApprovalPreview, SchemaVersion: 1, Actor: *receipt.Actor, BaseRevision: 0, SourceAggregateId: sourceAggregateID, Brief: brief, BriefHash: contractsv1.SHA256(briefHash), ExpiresAt: &expiresAt}
	previewHash, hashErr := Digest(preview)
	return err == nil && hashErr == nil && fmt.Sprint(receipt.Payload["preview_hash"]) == previewHash && receipt.AggregateId == approvalAggregate(string(brief.Id)) && !receipt.OccurredAt.Before(terminalAt) && !receipt.OccurredAt.After(expiresAt) && reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{contractsv1.SHA256(briefHash), action.ContentSha256}) && reflect.DeepEqual(receipt.OutputHashes, []contractsv1.SHA256{contractsv1.SHA256(previewHash)})
}

func selectedApprovalDecision(brief contractsv1.ApprovalBrief, receipt contractsv1.Receipt) (contractsv1.ApprovalOptionDecision, contractsv1.Receipt, error) {
	selected := fmt.Sprint(receipt.Payload["selected_option_id"])
	for _, option := range brief.Options {
		if string(option.Id) == selected {
			return option.Decision, receipt, nil
		}
	}
	return "", contractsv1.Receipt{}, errors.New("approval decision option is invalid")
}

func (e *Engine) approvalDecisionArtifact(request CampaignRunRequest, workflow preparedWorkflow, node CompiledNode, action contractsv1.ActionArtifact, receipt contractsv1.Receipt, decision contractsv1.ApprovalOptionDecision) (contractsv1.ActionArtifact, error) {
	if len(node.Definition.OutputSlots) != 1 {
		return contractsv1.ActionArtifact{}, errors.New("approval Node requires exactly one output slot")
	}
	content := map[string]any{"decision": string(decision), "source_action_id": action.Id, "approval_receipt_hash": receipt.ReceiptHash}
	contentHash, err := Digest(content)
	if err != nil {
		return contractsv1.ActionArtifact{}, err
	}
	state := contractsv1.ActionArtifactApprovalStateApproved
	if decision == contractsv1.ApprovalOptionDecisionReject {
		state = contractsv1.ActionArtifactApprovalStateRejected
	} else if decision == contractsv1.ApprovalOptionDecisionRevise {
		state = contractsv1.ActionArtifactApprovalStateStale
	}
	artifact := contractsv1.ActionArtifact{Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: shortID("action-", contentHash), ArtifactType: node.Definition.OutputSlots[0].ArtifactType, JobId: request.Job.Id, CampaignId: request.Campaign.Id, WorkflowRef: workflow.compiled.WorkflowRef, NodeId: node.Definition.Id, InputHashes: []contractsv1.SHA256{action.ContentSha256, receipt.ReceiptHash}, Content: content, ContentSha256: contractsv1.SHA256(contentHash), ApprovalState: state}
	if err := contract.ValidateDefinition("ActionArtifact", artifact); err != nil {
		return contractsv1.ActionArtifact{}, err
	}
	if node.Definition.OutputSlots[0].ContentSchema == nil {
		return contractsv1.ActionArtifact{}, errors.New("approval output schema is required")
	}
	validator, ok := e.outputs[*node.Definition.OutputSlots[0].ContentSchema]
	if !ok {
		return contractsv1.ActionArtifact{}, errors.New("approval output schema is not registered")
	}
	if err := validator(content); err != nil {
		return contractsv1.ActionArtifact{}, fmt.Errorf("approval output: %w", err)
	}
	return artifact, nil
}

func (e *Engine) reduceCampaignReplay(replay contractsv1.ReplayBundle, prepared preparedCampaign) (contractsv1.CampaignExecutionState, error) {
	if err := VerifyReplay(replay); err != nil {
		return contractsv1.CampaignExecutionState{}, err
	}
	if len(replay.Receipts) == 0 || replay.Receipts[0].ReceiptType != contractsv1.ReceiptReceiptTypeCampaignAdmission || (replay.Receipts[0].SchemaVersion != 2 && replay.Receipts[0].SchemaVersion != 3) {
		return contractsv1.CampaignExecutionState{}, errors.New("Campaign Replay does not start with a supported admission")
	}
	var state contractsv1.CampaignExecutionState
	if err := decodePayload(replay.Receipts[0].Payload["state"], &state); err != nil {
		return contractsv1.CampaignExecutionState{}, err
	}
	expected := prepared.initial
	if replay.Receipts[0].SchemaVersion == 2 {
		expected.SchemaVersion = 2
		expected.ProviderIsolation = nil
	}
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
		case contractsv1.ReceiptReceiptTypeContextBound, contractsv1.ReceiptReceiptTypeContextAvailable:
			var payload contractsv1.ContextTransitionEventPayload
			if err := decodePayload(receipt.Payload, &payload); err != nil {
				return state, err
			}
			node := nodeStatePtr(&state, payload.WorkflowRef, string(payload.NodeId))
			_, definition, exists := preparedNode(prepared, payload.WorkflowRef, string(payload.NodeId))
			resolved := resolvedContext{Bundle: payload.Bundle, Packs: payload.Packs}
			if node == nil || !exists || !campaignNodeReadyForContext(state, prepared, payload.WorkflowRef, string(payload.NodeId)) || !nodeUsesProvider(definition.Definition) || validateRecordedContextForNode(resolved, definition, prepared.request.Campaign.Scope, prepared.request.Campaign.EvidenceFrontier.Cutoff) != nil || payload.Bundle.JobId != state.JobId || payload.Bundle.CampaignId != state.CampaignId || payload.Bundle.WorkflowRef != payload.WorkflowRef || payload.Bundle.NodeId != payload.NodeId || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{payload.Bundle.BundleHash}) || !reflect.DeepEqual(receipt.OutputHashes, []contractsv1.SHA256{payload.Bundle.BundleHash}) {
				return state, errors.New("Campaign Context transition binding is invalid")
			}
			if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeContextBound {
				if node.Status != contractsv1.CampaignNodeExecutionStatusPending || node.ContextBundleHash != nil || payload.PreviousBlockerFingerprint != nil {
					return state, errors.New("Campaign Context binding is not eligible")
				}
			} else {
				if node.Status != contractsv1.CampaignNodeExecutionStatusNeedsContext || node.BlockerFingerprint == nil || payload.PreviousBlockerFingerprint == nil || *node.BlockerFingerprint != *payload.PreviousBlockerFingerprint {
					return state, errors.New("Campaign Context availability does not advance the exact blocker")
				}
				node.Status, node.BlockerFingerprint = contractsv1.CampaignNodeExecutionStatusPending, nil
			}
			hash := payload.Bundle.BundleHash
			node.ContextBundleHash = &hash
		case contractsv1.ReceiptReceiptTypeNeedsContext:
			var payload contractsv1.NeedsContextEventPayload
			if err := decodePayload(receipt.Payload, &payload); err != nil {
				return state, err
			}
			node := nodeStatePtr(&state, payload.WorkflowRef, string(payload.NodeId))
			_, definition, exists := preparedNode(prepared, payload.WorkflowRef, string(payload.NodeId))
			reasons := make(map[string]string, len(payload.Reasons))
			for requirement, reason := range payload.Reasons {
				reasons[requirement] = string(reason)
			}
			fingerprint, err := Digest(struct {
				WorkflowRef  contractsv1.WorkflowRef
				NodeID       string
				Requirements []string
				Reasons      map[string]string
			}{payload.WorkflowRef, string(payload.NodeId), []string(payload.Requirements), reasons})
			if err != nil || node == nil || !exists || !nodeUsesProvider(definition.Definition) || !campaignNodeReadyForContext(state, prepared, payload.WorkflowRef, string(payload.NodeId)) || contractsv1.SHA256(fingerprint) != payload.BlockerFingerprint || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{state.CampaignHash}) || !reflect.DeepEqual(receipt.OutputHashes, []contractsv1.SHA256{payload.BlockerFingerprint}) {
				return state, errors.New("Campaign needs_context binding is invalid")
			}
			node.Status, node.BlockerFingerprint, node.ContextBundleHash = contractsv1.CampaignNodeExecutionStatusNeedsContext, &payload.BlockerFingerprint, nil
		case contractsv1.ReceiptReceiptTypeApprovalRequested:
			var payload contractsv1.ApprovalRequestedEventPayload
			if err := decodePayload(receipt.Payload, &payload); err != nil {
				return state, err
			}
			node := nodeStatePtr(&state, payload.WorkflowRef, string(payload.NodeId))
			workflow, definition, exists := preparedNode(prepared, payload.WorkflowRef, string(payload.NodeId))
			if node == nil || !exists || definition.Definition.Kind != contractsv1.NodeDefinitionKindApproval || node.Status != contractsv1.CampaignNodeExecutionStatusPending || !campaignNodeReady(state, prepared, payload.WorkflowRef, string(payload.NodeId)) {
				return state, errors.New("Campaign approval request is not eligible")
			}
			source, action, result, err := e.approvalSource(prepared, workflow, definition)
			actionHash, hashErr := Digest(action)
			if err != nil || hashErr != nil || definition.Definition.ApprovalPolicy == nil || payload.ApprovalPolicy != nil && *payload.ApprovalPolicy != *definition.Definition.ApprovalPolicy || payload.SourceReplayHash != source.BundleHash || payload.ActionHash != contractsv1.SHA256(actionHash) || payload.ApprovalId != contractsv1.Identifier(shortID("approval-", actionHash)) || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{source.BundleHash, result.ReceiptHash}) || !reflect.DeepEqual(receipt.OutputHashes, []contractsv1.SHA256{payload.ActionHash}) {
				return state, errors.New("Campaign approval request binding is invalid")
			}
			node.Status, node.ApprovalId = contractsv1.CampaignNodeExecutionStatusAwaitingApproval, &payload.ApprovalId
			state.Status = contractsv1.CampaignExecutionStateStatusRunning
		case contractsv1.ReceiptReceiptTypeApprovalDecided:
			var payload contractsv1.ApprovalDecidedEventPayload
			if err := decodePayload(receipt.Payload, &payload); err != nil {
				return state, err
			}
			node := nodeStatePtr(&state, payload.WorkflowRef, string(payload.NodeId))
			workflow, definition, exists := preparedNode(prepared, payload.WorkflowRef, string(payload.NodeId))
			if node == nil || !exists || node.Status != contractsv1.CampaignNodeExecutionStatusAwaitingApproval || node.ApprovalId == nil || *node.ApprovalId != payload.ApprovalId || approvalActionBudgetBlocker(state, prepared.request.Campaign.Budget, payload.WorkflowRef, definition.Definition) != "" {
				return state, errors.New("Campaign approval decision is not eligible")
			}
			source, action, result, err := e.approvalSource(prepared, workflow, definition)
			approvalReplay, replayErr := e.ledger.Replay(approvalAggregate(string(payload.ApprovalId)))
			decision, approvalReceipt, decisionErr := verifiedApprovalDecision(approvalReplay, source, action, result, definition.Definition.ApprovalPolicy, e.approvalActors)
			expectedArtifact, artifactErr := e.approvalDecisionArtifact(prepared.request, workflow, definition, action, approvalReceipt, decision)
			expectedArtifactHash, expectedHashErr := Digest(expectedArtifact)
			payloadArtifactHash, payloadHashErr := Digest(payload.Artifact)
			if err != nil || replayErr != nil || decisionErr != nil || artifactErr != nil || expectedHashErr != nil || payloadHashErr != nil {
				return state, errors.New("Campaign approval decision sources are invalid")
			}
			if string(decision) != string(payload.Decision) || approvalReceipt.ReceiptHash != payload.ApprovalReceiptHash || expectedArtifactHash != payloadArtifactHash {
				return state, errors.New("Campaign approval decision payload is invalid")
			}
			if !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{source.BundleHash, approvalReceipt.ReceiptHash}) || !reflect.DeepEqual(receipt.OutputHashes, []contractsv1.SHA256{payload.Artifact.ContentSha256}) {
				return state, errors.New("Campaign approval decision receipt binding is invalid")
			}
			now := receipt.OccurredAt
			node.Status, node.CompletedAt = contractsv1.CampaignNodeExecutionStatusCompleted, &now
			node.Usage.Actions, state.Usage.Actions = 1, state.Usage.Actions+1
		case contractsv1.ReceiptReceiptTypeWaitStarted:
			var payload contractsv1.WaitStartedEventPayload
			if err := decodePayload(receipt.Payload, &payload); err != nil {
				return state, err
			}
			node := nodeStatePtr(&state, payload.WorkflowRef, string(payload.NodeId))
			_, definition, exists := preparedNode(prepared, payload.WorkflowRef, string(payload.NodeId))
			if node == nil || !exists || node.Status != contractsv1.CampaignNodeExecutionStatusPending || definition.Definition.Kind != contractsv1.NodeDefinitionKindWait || !campaignNodeReady(state, prepared, payload.WorkflowRef, string(payload.NodeId)) || !payload.StartedAt.Equal(receipt.OccurredAt) || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{state.CampaignHash}) || len(receipt.OutputHashes) != 0 {
				return state, errors.New("Campaign wait start binding is invalid")
			}
			if payload.Mode == contractsv1.WaitStartedEventPayloadModeTime {
				if definition.Definition.WaitMode == nil || *definition.Definition.WaitMode != contractsv1.NodeDefinitionWaitModeTime || payload.WakeAt == nil || !payload.WakeAt.Equal(payload.StartedAt.Add(time.Duration(*definition.Definition.WaitDelaySeconds)*time.Second)) || payload.Signal != nil {
					return state, errors.New("Campaign time wait binding is invalid")
				}
			} else if definition.Definition.WaitMode == nil || *definition.Definition.WaitMode != contractsv1.NodeDefinitionWaitModeSignal || payload.Signal == nil || definition.Definition.WaitSignal == nil || *payload.Signal != *definition.Definition.WaitSignal || payload.WakeAt != nil {
				return state, errors.New("Campaign signal wait binding is invalid")
			}
			node.Status, node.StartedAt, node.WakeAt, node.Signal = contractsv1.CampaignNodeExecutionStatusWaiting, &payload.StartedAt, payload.WakeAt, payload.Signal
			state.Status = contractsv1.CampaignExecutionStateStatusRunning
		case contractsv1.ReceiptReceiptTypeWaitResumed:
			var payload contractsv1.WaitResumedEventPayload
			if err := decodePayload(receipt.Payload, &payload); err != nil {
				return state, err
			}
			node := nodeStatePtr(&state, payload.WorkflowRef, string(payload.NodeId))
			if node == nil || node.Status != contractsv1.CampaignNodeExecutionStatusWaiting || !payload.ResumedAt.Equal(receipt.OccurredAt) || len(receipt.OutputHashes) != 0 {
				return state, errors.New("Campaign wait resume binding is invalid")
			}
			if node.WakeAt != nil {
				if payload.ResumedAt.Before(*node.WakeAt) || payload.SignalHash != nil || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{state.CampaignHash}) {
					return state, errors.New("Campaign time wait resumed early")
				}
			} else if node.Signal == nil || payload.Signal == nil || *payload.Signal != *node.Signal || payload.SignalHash == nil || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{*payload.SignalHash}) {
				return state, errors.New("Campaign signal wait has no exact signal")
			}
			node.Status, node.CompletedAt = contractsv1.CampaignNodeExecutionStatusCompletedNoAction, &payload.ResumedAt
		case contractsv1.ReceiptReceiptTypeCoreCompleted:
			var payload contractsv1.CoreCompletedEventPayload
			if err := decodePayload(receipt.Payload, &payload); err != nil {
				return state, err
			}
			node := nodeStatePtr(&state, payload.WorkflowRef, string(payload.NodeId))
			_, definition, exists := preparedNode(prepared, payload.WorkflowRef, string(payload.NodeId))
			if node == nil || !exists || node.Status != contractsv1.CampaignNodeExecutionStatusPending || (definition.Definition.Kind != contractsv1.NodeDefinitionKindDeterministic && definition.Definition.Kind != contractsv1.NodeDefinitionKindTerminal) || !campaignNodeReady(state, prepared, payload.WorkflowRef, string(payload.NodeId)) || !payload.CompletedAt.Equal(receipt.OccurredAt) || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{state.CampaignHash}) || len(receipt.OutputHashes) != 0 {
				return state, errors.New("Campaign Core completion binding is invalid")
			}
			if len(definition.Definition.OutputSlots) == 0 && payload.Status != contractsv1.CoreCompletedEventPayloadStatusCompletedNoAction || len(definition.Definition.OutputSlots) > 0 && payload.Status != contractsv1.CoreCompletedEventPayloadStatusCompleted {
				return state, errors.New("Campaign Core completion status is invalid")
			}
			node.Status, node.CompletedAt = contractsv1.CampaignNodeExecutionStatus(payload.Status), &payload.CompletedAt
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
			_, definition, exists := preparedNode(prepared, workflowRef, nodeID)
			if node == nil || !exists || node.Status != contractsv1.CampaignNodeExecutionStatusPending || !campaignNodeReady(state, prepared, workflowRef, nodeID) {
				return state, errors.New("Campaign attempt reservation is not eligible")
			}
			if _, blocker := remainingBudgetAt(state, prepared.request.Campaign.Budget, workflowRef, definition.Definition, receipt.OccurredAt); blocker != "" {
				return state, errors.New("Campaign attempt reservation exceeds the canonical budget")
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
			if node == nil || node.Status != contractsv1.CampaignNodeExecutionStatusRunning || (status != contractsv1.CampaignNodeExecutionStatusCompleted && status != contractsv1.CampaignNodeExecutionStatusCompletedNoAction && status != contractsv1.CampaignNodeExecutionStatusBlocked) || usage.Attempts != 1 {
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
			if err != nil || !invocation.BudgetEnforced || string(invocation.Node.Id) != nodeID || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{invocation.Bundle.BundleHash}) {
				return state, errors.New("Campaign Node completion child binding is invalid")
			}
			material, err := MaterializeReplay(childReplay, e.outputs)
			if err != nil {
				return state, err
			}
			providerResult, hasResult, err := materializeProviderResult(childReplay)
			route, routeErr := providerRoute(providerResult, invocation.Node)
			duration := 0
			if node.StartedAt != nil && completedAt.After(*node.StartedAt) {
				duration = int(math.Ceil(completedAt.Sub(*node.StartedAt).Seconds()))
			}
			if err != nil || !hasResult || routeErr != nil || validateProviderResult(providerResult, invocation) != nil || providerOutcome(providerResult) != status || !completedAt.Equal(receipt.OccurredAt) || usage.Actions != len(material.Artifacts) || usage.Candidates != artifactCandidateCount(material.Artifacts, invocation.Node.OutputSlots) || usage.DurationSeconds != duration {
				return state, errors.New("Campaign Node completion usage does not match the child Replay")
			}
			if raw, exists := receipt.Payload["route"]; exists {
				var recorded contractsv1.NodeOutcomeRoute
				if err := decodePayload(raw, &recorded); err != nil || recorded != route {
					return state, errors.New("Campaign Node route does not match the child Replay")
				}
			} else if providerResult.Outcome != "" {
				return state, errors.New("legacy Campaign completion cannot acquire an explicit route")
			}
			if status == contractsv1.CampaignNodeExecutionStatusBlocked {
				var blocker contractsv1.Identifier
				if err := decodePayload(receipt.Payload["blocker_code"], &blocker); err != nil || providerResult.BlockerCode == nil || blocker != *providerResult.BlockerCode {
					return state, errors.New("Campaign Node blocker does not match the child Replay")
				}
				node.BlockerCode = &blocker
				state.Status, state.BlockerCode = contractsv1.CampaignExecutionStateStatusBlocked, &blocker
			}
			node.Status, node.CompletedAt, node.ResultReplayHash = status, &completedAt, &replayHash
			node.Usage.Actions, node.Usage.Candidates, node.Usage.DurationSeconds = usage.Actions, usage.Candidates, usage.DurationSeconds
			state.Usage.Actions += usage.Actions
			state.Usage.Candidates += usage.Candidates
			state.Usage.DurationSeconds += usage.DurationSeconds
			if route == contractsv1.NodeOutcomeRouteCompleteBranch {
				markCampaignBranchSkipped(&state, prepared, workflowRef, nodeID)
			}
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
			if !knownBudgetBlocker(blocker) {
				return state, errors.New("Campaign budget exhaustion binding is invalid")
			}
			node := nodeStatePtr(&state, workflowRef, nodeID)
			workflow, definition, exists := preparedNode(prepared, workflowRef, nodeID)
			approvalBudgetNode := node != nil && exists && definition.Definition.Kind == contractsv1.NodeDefinitionKindApproval && (node.Status == contractsv1.CampaignNodeExecutionStatusPending || node.Status == contractsv1.CampaignNodeExecutionStatusAwaitingApproval)
			approvalWaiting := approvalBudgetNode && node.Status == contractsv1.CampaignNodeExecutionStatusAwaitingApproval
			if node == nil || !exists || (!approvalWaiting && node.Status != contractsv1.CampaignNodeExecutionStatusRunning && (node.Status != contractsv1.CampaignNodeExecutionStatusPending || !campaignNodeReady(state, prepared, workflowRef, nodeID))) {
				return state, errors.New("Campaign budget exhaustion is not eligible")
			}
			var resultReplayHash contractsv1.SHA256
			if raw, ok := receipt.Payload["result_replay_hash"]; ok {
				if err := decodePayload(raw, &resultReplayHash); err != nil {
					return state, err
				}
			}
			if resultReplayHash == "" {
				_, actual := remainingBudgetAt(state, prepared.request.Campaign.Budget, workflowRef, definition.Definition, receipt.OccurredAt)
				if approvalBudgetNode {
					actual = approvalActionBudgetBlocker(state, prepared.request.Campaign.Budget, workflowRef, definition.Definition)
				}
				if actual != blocker || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{state.CampaignHash}) || len(receipt.OutputHashes) != 0 {
					return state, errors.New("Campaign budget exhaustion has no canonical condition")
				}
			} else {
				childID, err := executionID(RunRequest{Job: prepared.request.Job, Campaign: prepared.request.Campaign, Workflow: workflow.definition, NodeID: nodeID})
				if err != nil {
					return state, err
				}
				childHead, err := e.ledger.Replay(childID)
				if err != nil || !reflect.DeepEqual(receipt.OutputHashes, []contractsv1.SHA256{resultReplayHash}) {
					return state, errors.New("Campaign budget exhaustion has no exact child Replay")
				}
				childReplay, err := replayAtBundleHash(childHead, resultReplayHash)
				childTerminal := terminalState(childReplay)
				if err != nil || (childTerminal != "budget_exhausted" && childTerminal != "deadline_expired") {
					return state, errors.New("Campaign budget exhaustion has no exact rejected child result")
				}
				invocation, err := VerifyDefinitionBindingWithAdmission(childReplay, workflow.admissionReplay, prepared.request.Job, prepared.request.Campaign, workflow.definition)
				if err != nil || !invocation.BudgetEnforced || string(invocation.Node.Id) != nodeID || !reflect.DeepEqual(receipt.InputHashes, []contractsv1.SHA256{invocation.Bundle.BundleHash}) {
					return state, errors.New("Campaign budget exhaustion child binding is invalid")
				}
				if childTerminal == "deadline_expired" {
					if blocker != "duration-budget-exhausted" || receipt.OccurredAt.Before(invocation.Deadline) {
						return state, errors.New("Campaign budget exhaustion does not match the child deadline")
					}
				} else {
					result, ok, err := materializeProviderResult(childReplay)
					if err != nil || !ok || validateProviderResult(result, invocation) != nil || validateArtifactContracts(result.Artifacts, invocation, e.outputs, providerOutcome(result) != contractsv1.CampaignNodeExecutionStatusBlocked) != nil {
						return state, errors.New("Campaign budget exhaustion child result is invalid")
					}
					remaining, actual := remainingBudgetAt(state, prepared.request.Campaign.Budget, workflowRef, definition.Definition, receipt.OccurredAt)
					if actual != "" || campaignResultBudgetBlocker(result.Artifacts, definition.Definition.OutputSlots, remaining) != blocker {
						return state, errors.New("Campaign budget exhaustion does not match the child result")
					}
				}
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
			if node.Status == contractsv1.CampaignNodeExecutionStatusRunning || node.Status == contractsv1.CampaignNodeExecutionStatusNeedsContext || node.Status == contractsv1.CampaignNodeExecutionStatusAwaitingApproval || node.Status == contractsv1.CampaignNodeExecutionStatusWaiting {
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

func campaignNodeReadyForContext(state contractsv1.CampaignExecutionState, prepared preparedCampaign, ref contractsv1.WorkflowRef, nodeID string) bool {
	node := nodeState(state, ref, nodeID)
	if node.Status != contractsv1.CampaignNodeExecutionStatusPending && node.Status != contractsv1.CampaignNodeExecutionStatusNeedsContext {
		return false
	}
	copy := state
	copy.Nodes = append([]contractsv1.CampaignNodeExecution(nil), state.Nodes...)
	for index := range copy.Nodes {
		if copy.Nodes[index].WorkflowRef == ref && string(copy.Nodes[index].NodeId) == nodeID {
			copy.Nodes[index].Status = contractsv1.CampaignNodeExecutionStatusPending
		}
	}
	return campaignNodeReady(copy, prepared, ref, nodeID)
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
		if node.Status != contractsv1.CampaignNodeExecutionStatusCompleted && node.Status != contractsv1.CampaignNodeExecutionStatusCompletedNoAction && node.Status != contractsv1.CampaignNodeExecutionStatusSkipped {
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
		node := nodeState(state, *state.NextWorkflowRef, string(*state.NextNodeId))
		if node.Status == contractsv1.CampaignNodeExecutionStatusNeedsContext || node.Status == contractsv1.CampaignNodeExecutionStatusAwaitingApproval || node.Status == contractsv1.CampaignNodeExecutionStatusWaiting {
			return contractsv1.CampaignDrivePreviewNextActionWait
		}
		return contractsv1.CampaignDrivePreviewNextActionRunNode
	}
	return contractsv1.CampaignDrivePreviewNextActionWait
}

func (e *Engine) remainingBudget(state contractsv1.CampaignExecutionState, campaign contractsv1.Budget, workflowRef contractsv1.WorkflowRef, node contractsv1.NodeDefinition) (contractsv1.Budget, contractsv1.Identifier) {
	return remainingBudgetAt(state, campaign, workflowRef, node, e.now())
}

func approvalActionBudgetBlocker(state contractsv1.CampaignExecutionState, campaign contractsv1.Budget, workflowRef contractsv1.WorkflowRef, node contractsv1.NodeDefinition) contractsv1.Identifier {
	current := nodeState(state, workflowRef, string(node.Id))
	if current.Usage.Actions >= node.Budget.MaxActions || state.Usage.Actions >= campaign.MaxActions {
		return "action-budget-exhausted"
	}
	return ""
}

func remainingBudgetAt(state contractsv1.CampaignExecutionState, campaign contractsv1.Budget, workflowRef contractsv1.WorkflowRef, node contractsv1.NodeDefinition, now time.Time) (contractsv1.Budget, contractsv1.Identifier) {
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
		if started && !now.Before(campaignStartedAt.Add(time.Duration(*campaign.MaxDurationSeconds)*time.Second)) {
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
		if node.Status != contractsv1.CampaignNodeExecutionStatusCompleted && node.Status != contractsv1.CampaignNodeExecutionStatusCompletedNoAction && node.Status != contractsv1.CampaignNodeExecutionStatusSkipped {
			return false
		}
	}
	return true
}

func markCampaignBranchSkipped(state *contractsv1.CampaignExecutionState, prepared preparedCampaign, ref contractsv1.WorkflowRef, completedNodeID string) {
	skipped := map[string]bool{completedNodeID: true}
	for changed := true; changed; {
		changed = false
		for _, workflow := range prepared.workflows {
			if workflow.compiled.WorkflowRef != ref {
				continue
			}
			for _, compiled := range workflow.compiled.Nodes {
				id := string(compiled.Definition.Id)
				if skipped[id] {
					continue
				}
				for _, dependency := range compiled.Definition.DependsOn {
					if skipped[dependency] {
						node := nodeStatePtr(state, ref, id)
						if node != nil && node.Status == contractsv1.CampaignNodeExecutionStatusPending {
							node.Status = contractsv1.CampaignNodeExecutionStatusSkipped
						}
						skipped[id], changed = true, true
						break
					}
				}
			}
		}
	}
}
