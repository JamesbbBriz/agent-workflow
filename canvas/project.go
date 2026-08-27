package canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

type ExecutionInput struct {
	Replay  contractsv1.ReplayBundle
	Outputs workflow.OutputCatalog
}

func Project(job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition, definitions []contractsv1.WorkflowDefinition, inputs ...ExecutionInput) (contractsv1.CanvasSnapshot, error) {
	return ProjectWithAdmissions(job, campaign, definitions, nil, inputs...)
}

func ProjectWithAdmissions(job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition, definitions []contractsv1.WorkflowDefinition, admissions []contractsv1.ReplayBundle, inputs ...ExecutionInput) (contractsv1.CanvasSnapshot, error) {
	if err := contract.ValidateDefinition("JobDefinition", job); err != nil {
		return contractsv1.CanvasSnapshot{}, err
	}
	if err := contract.ValidateDefinition("CampaignDefinition", campaign); err != nil {
		return contractsv1.CanvasSnapshot{}, err
	}
	if campaign.JobId != job.Id {
		return contractsv1.CanvasSnapshot{}, errors.New("campaign does not belong to the job")
	}

	workflows, err := orderWorkflows(campaign.WorkflowPlan, definitions)
	if err != nil {
		return contractsv1.CanvasSnapshot{}, err
	}
	workflowByRef := make(map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition, len(workflows))
	workflowStates := map[string]contractsv1.CanvasEntityStatus{}
	admissionByRef := map[contractsv1.WorkflowRef]contractsv1.ReplayBundle{}
	for _, definition := range workflows {
		body, err := json.Marshal(definition)
		if err != nil {
			return contractsv1.CanvasSnapshot{}, err
		}
		if _, err := contract.ValidateWorkflow(body); err != nil {
			return contractsv1.CanvasSnapshot{}, err
		}
		workflowByRef[contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version))] = definition
	}
	for _, replay := range admissions {
		for version := range replay.Receipts {
			admission, err := workflow.MaterializeAdmission(replay, version+1)
			if err != nil {
				return contractsv1.CanvasSnapshot{}, err
			}
			if !reflect.DeepEqual(admission.Job, job) || !reflect.DeepEqual(admission.Campaign, campaign) {
				return contractsv1.CanvasSnapshot{}, errors.New("Canvas definitions do not match their Workflow admission")
			}
			ref := contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", admission.Workflow.Id, admission.Workflow.Version))
			definition, ok := workflowByRef[ref]
			if !ok || definition.Id != admission.Workflow.Id || definition.Version != admission.Workflow.Version {
				continue
			}
			body, _ := json.Marshal(definition)
			identity, err := contract.ValidateWorkflow(body)
			if err != nil || identity.Hash != string(admission.DefinitionHash) {
				return contractsv1.CanvasSnapshot{}, errors.New("Canvas definition does not match its admission receipt")
			}
			workflowStates[string(ref)] = contractsv1.CanvasEntityStatusAdmitted
			admissionByRef[ref] = replay
		}
	}
	executions := make([]contractsv1.CanvasExecution, 0, len(inputs))
	replays := make([]contractsv1.ReplayBundle, 0, len(inputs))
	seenExecutions := make(map[string]bool, len(inputs))
	generatedAt := campaign.EvidenceFrontier.Cutoff
	for _, input := range inputs {
		if seenExecutions[input.Replay.AggregateId] {
			return contractsv1.CanvasSnapshot{}, fmt.Errorf("execution %q is duplicated in the Canvas projection", input.Replay.AggregateId)
		}
		seenExecutions[input.Replay.AggregateId] = true
		candidate, err := workflow.MaterializeInvocation(input.Replay)
		if err != nil {
			return contractsv1.CanvasSnapshot{}, err
		}
		definition, ok := workflowByRef[candidate.WorkflowRef]
		if !ok {
			return contractsv1.CanvasSnapshot{}, errors.New("execution workflow is not pinned by the Campaign")
		}
		admissionReplay, ok := admissionByRef[candidate.WorkflowRef]
		if !ok {
			return contractsv1.CanvasSnapshot{}, errors.New("execution has no canonical Workflow admission")
		}
		invocation, err := workflow.VerifyDefinitionBinding(input.Replay, admissionReplay, job, campaign, definition)
		if err != nil {
			return contractsv1.CanvasSnapshot{}, err
		}
		execution, err := projectExecution(job, campaign, definition, invocation, input)
		if err != nil {
			return contractsv1.CanvasSnapshot{}, err
		}
		executions = append(executions, execution)
		replays = append(replays, input.Replay)
		for _, receipt := range input.Replay.Receipts {
			if receipt.OccurredAt.After(generatedAt) {
				generatedAt = receipt.OccurredAt
			}
		}
	}
	sort.Slice(executions, func(i, j int) bool { return executions[i].AggregateId < executions[j].AggregateId })

	snapshot := contractsv1.CanvasSnapshot{
		Kind: contractsv1.CanvasSnapshotKindCanvasSnapshot, SchemaVersion: 1, GeneratedAt: generatedAt,
		Definition: contractsv1.CanvasDefinitionGraph{
			Job: job, Campaign: campaign, CampaignState: contractsv1.CanvasEntityStatusConfigured, WorkflowStates: workflowStates, Workflows: workflows,
		},
		Executions: executions, Replays: replays, AdmissionReplays: admissions,
		NextSafeAction: contractsv1.CanvasNextSafeAction{Kind: contractsv1.CanvasNextSafeActionKindNone, Reason: "No canonical next action is recorded in this read-only projection."},
	}
	if err := contract.ValidateDefinition("CanvasSnapshot", snapshot); err != nil {
		return contractsv1.CanvasSnapshot{}, fmt.Errorf("canvas snapshot: %w", err)
	}
	return snapshot, nil
}

func projectExecution(job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition, definition contractsv1.WorkflowDefinition, invocation workflow.Invocation, input ExecutionInput) (contractsv1.CanvasExecution, error) {
	wantRef := contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version))
	if !containsWorkflowRef(campaign.WorkflowPlan, wantRef) {
		return contractsv1.CanvasExecution{}, errors.New("workflow is not pinned by the campaign")
	}
	if invocation.JobID != job.Id || invocation.CampaignID != campaign.Id || invocation.WorkflowRef != wantRef {
		return contractsv1.CanvasExecution{}, errors.New("execution does not belong to the definition graph")
	}

	status := contractsv1.CanvasEntityStatusRunning
	artifacts := []contractsv1.ActionArtifact{}
	if receiptTypePresent(input.Replay, contractsv1.ReceiptReceiptTypeResult) {
		material, err := workflow.MaterializeReplay(input.Replay, input.Outputs)
		if err != nil {
			return contractsv1.CanvasExecution{}, err
		}
		artifacts = material.Artifacts
		if terminalStatePresent(input.Replay, "node_completed") {
			status = contractsv1.CanvasEntityStatusCompleted
		}
	} else if receiptTypePresent(input.Replay, contractsv1.ReceiptReceiptTypeTerminal) {
		status = contractsv1.CanvasEntityStatusTerminal
	}
	ports := make([]contractsv1.CanvasContextPort, 0, len(invocation.Node.Context))
	for _, requirement := range invocation.Node.Context {
		port := contractsv1.CanvasContextPort{
			Id: requirement.Id, NodeId: invocation.Node.Id, Selector: requirement.Selector,
			PackType: requirement.PackType, SchemaVersion: requirement.SchemaVersion,
			Required: requirement.Required, AllowPartial: requirement.AllowPartial,
			Status: contractsv1.CanvasContextStatusMissing, Producer: requirement.Selector,
			Consumers: []string{executionKey(invocation.WorkflowRef, invocation.Node.Id)}, EvidenceFrontier: campaign.EvidenceFrontier,
		}
		edition, missingOptional, err := matchingEdition(requirement, invocation)
		if err != nil {
			return contractsv1.CanvasExecution{}, err
		}
		if edition != nil {
			port.Edition = edition
			port.Status = contractsv1.CanvasContextStatusResolved
			if edition.Coverage == contractsv1.ContextPackEditionCoveragePartial {
				port.Status = contractsv1.CanvasContextStatusPartial
			}
		} else if missingOptional || !requirement.Required {
			port.Status = contractsv1.CanvasContextStatusDegraded
		}
		ports = append(ports, port)
	}

	links := make([]contractsv1.CanvasReceiptLink, 0, len(input.Replay.Receipts))
	for _, receipt := range input.Replay.Receipts {
		links = append(links, contractsv1.CanvasReceiptLink{
			Id: receipt.Id, ReceiptType: contractsv1.CanvasReceiptLinkReceiptType(receipt.ReceiptType),
			ReceiptHash: receipt.ReceiptHash, OccurredAt: receipt.OccurredAt,
		})
	}
	var blockerCode *contractsv1.Identifier
	var blockerMessage *string
	if status == contractsv1.CanvasEntityStatusTerminal {
		blockerCode, blockerMessage = terminalBlocker(input.Replay)
	}
	return contractsv1.CanvasExecution{
		AggregateId: input.Replay.AggregateId, NodeId: invocation.Node.Id, Status: status,
		Deadline: invocation.Deadline, Bundle: invocation.Bundle, ContextPorts: ports,
		Outputs: artifacts, Receipts: links, ApprovalState: artifactApprovalState(artifacts),
		BlockerCode: blockerCode, BlockerMessage: blockerMessage,
	}, nil
}

func terminalBlocker(replay contractsv1.ReplayBundle) (*contractsv1.Identifier, *string) {
	for _, receipt := range replay.Receipts {
		if receipt.ReceiptType != contractsv1.ReceiptReceiptTypeTerminal {
			continue
		}
		if state, _ := receipt.Payload["state"].(string); state == "deadline_expired" {
			code := contractsv1.Identifier("deadline-expired")
			message := "Provider deadline expired before a canonical result."
			return &code, &message
		}
		code := contractsv1.Identifier("terminal")
		message := "Execution reached a canonical terminal state."
		return &code, &message
	}
	return nil, nil
}

func containsWorkflowRef(plan []contractsv1.WorkflowRef, want contractsv1.WorkflowRef) bool {
	for _, ref := range plan {
		if ref == want {
			return true
		}
	}
	return false
}

func receiptTypePresent(replay contractsv1.ReplayBundle, receiptType contractsv1.ReceiptReceiptType) bool {
	for _, receipt := range replay.Receipts {
		if receipt.ReceiptType == receiptType {
			return true
		}
	}
	return false
}

func terminalStatePresent(replay contractsv1.ReplayBundle, state string) bool {
	for _, receipt := range replay.Receipts {
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeTerminal && receipt.Payload["state"] == state {
			return true
		}
	}
	return false
}

func matchingEdition(requirement contractsv1.ContextRequirement, invocation workflow.Invocation) (*contractsv1.ContextPackEdition, bool, error) {
	for index, entry := range invocation.Bundle.Entries {
		if entry.RequirementId != nil && *entry.RequirementId == requirement.Id {
			edition := invocation.Context[index]
			if edition.PackType != requirement.PackType || edition.PackSchemaVersion != requirement.SchemaVersion ||
				(requirement.EditionId != nil && edition.Id != *requirement.EditionId) {
				return nil, false, fmt.Errorf("Context requirement %q does not match its recorded edition", requirement.Id)
			}
			return &edition, false, nil
		}
	}
	for _, missing := range invocation.Bundle.MissingOptional {
		if missing == string(requirement.Id) {
			return nil, true, nil
		}
	}
	var match *contractsv1.ContextPackEdition
	for index, edition := range invocation.Context {
		if edition.PackType != requirement.PackType || edition.PackSchemaVersion != requirement.SchemaVersion ||
			(requirement.EditionId != nil && edition.Id != *requirement.EditionId) {
			continue
		}
		if match != nil {
			return nil, false, fmt.Errorf("legacy Context requirement %q has an ambiguous edition binding", requirement.Id)
		}
		match = &invocation.Context[index]
	}
	return match, false, nil
}

func artifactApprovalState(artifacts []contractsv1.ActionArtifact) contractsv1.CanvasExecutionApprovalState {
	state := contractsv1.CanvasExecutionApprovalStateNotRequired
	for _, artifact := range artifacts {
		switch artifact.ApprovalState {
		case contractsv1.ActionArtifactApprovalStateRejected:
			return contractsv1.CanvasExecutionApprovalStateRejected
		case contractsv1.ActionArtifactApprovalStateStale:
			return contractsv1.CanvasExecutionApprovalStateStale
		case contractsv1.ActionArtifactApprovalStatePending:
			state = contractsv1.CanvasExecutionApprovalStatePending
		case contractsv1.ActionArtifactApprovalStateApproved:
			if state == contractsv1.CanvasExecutionApprovalStateNotRequired {
				state = contractsv1.CanvasExecutionApprovalStateApproved
			}
		}
	}
	return state
}

func executionKey(workflowRef contractsv1.WorkflowRef, nodeID contractsv1.Identifier) string {
	return string(workflowRef) + "/" + string(nodeID)
}

func orderWorkflows(plan []contractsv1.WorkflowRef, definitions []contractsv1.WorkflowDefinition) ([]contractsv1.WorkflowDefinition, error) {
	byRef := make(map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition, len(definitions))
	for _, definition := range definitions {
		ref := contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version))
		if !containsWorkflowRef(plan, ref) {
			return nil, fmt.Errorf("workflow %q is not pinned by the Campaign", ref)
		}
		if _, exists := byRef[ref]; exists {
			return nil, fmt.Errorf("workflow %q is duplicated in the Canvas projection", ref)
		}
		byRef[ref] = definition
	}
	ordered := make([]contractsv1.WorkflowDefinition, 0, len(plan))
	for _, ref := range plan {
		definition, ok := byRef[ref]
		if !ok {
			return nil, fmt.Errorf("campaign workflow %q is missing from the Canvas projection", ref)
		}
		ordered = append(ordered, definition)
	}
	return ordered, nil
}
