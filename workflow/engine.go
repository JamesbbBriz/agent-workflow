package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

type CapabilityCatalog map[string]contractsv1.CapabilityManifestCapabilitiesElemAuthority

type OutputValidator func(any) error

type OutputCatalog map[contractsv1.WorkflowRef]OutputValidator

type Provider interface {
	Start(context.Context, Invocation) error
	Poll(context.Context, string) (ProviderResult, bool, error)
	Cancel(context.Context, string) error
}

type ProviderResult struct {
	IdempotencyKey string                       `json:"idempotency_key"`
	CompletedAt    time.Time                    `json:"completed_at"`
	Artifacts      []contractsv1.ActionArtifact `json:"artifacts"`
}

type Invocation struct {
	IdempotencyKey string                           `json:"idempotency_key"`
	JobID          contractsv1.Identifier           `json:"job_id"`
	CampaignID     contractsv1.Identifier           `json:"campaign_id"`
	WorkflowRef    contractsv1.WorkflowRef          `json:"workflow_ref"`
	Node           contractsv1.NodeDefinition       `json:"node"`
	Playbook       contractsv1.IntentCard           `json:"playbook"`
	IntentChain    contractsv1.ContextPackEdition   `json:"intent_chain"`
	Context        []contractsv1.ContextPackEdition `json:"context"`
	Bundle         contractsv1.ContextBundle        `json:"bundle"`
	Capabilities   contractsv1.CapabilityManifest   `json:"capabilities"`
	InputHashes    []contractsv1.SHA256             `json:"input_hashes"`
	Budget         contractsv1.Budget               `json:"budget"`
	BudgetEnforced bool                             `json:"budget_enforced,omitempty"`
	Deadline       time.Time                        `json:"deadline"`
}

type ReplayMaterial struct {
	Invocation Invocation                   `json:"invocation"`
	Artifacts  []contractsv1.ActionArtifact `json:"artifacts"`
}

type RunRequest struct {
	Job      contractsv1.JobDefinition
	Campaign contractsv1.CampaignDefinition
	Workflow contractsv1.WorkflowDefinition
	NodeID   string
	// BudgetOverride may only tighten the admitted Node budget. CampaignRuntime
	// uses it to enforce the remaining aggregate allowance before result acceptance.
	BudgetOverride *contractsv1.Budget
}

type RunResult struct {
	Compiled        CompiledWorkflow             `json:"compiled"`
	Bundle          contractsv1.ContextBundle    `json:"bundle"`
	Artifacts       []contractsv1.ActionArtifact `json:"artifacts"`
	AdmissionReplay contractsv1.ReplayBundle     `json:"admission_replay"`
	Replay          contractsv1.ReplayBundle     `json:"replay"`
}

type Engine struct {
	registry       *Registry
	capabilities   CapabilityCatalog
	outputs        OutputCatalog
	provider       Provider
	ledger         Ledger
	approvalActors map[string]map[string]bool
}

type Ledger interface {
	Append(contractsv1.Receipt) error
	Replay(string) (contractsv1.ReplayBundle, error)
}

// AtomicLedger is required for provider execution so accepted results and
// terminal state cannot be split by a crash. Basic Ledger implementations
// remain readable for historical Replay.
type AtomicLedger interface {
	Ledger
	AppendBatch([]contractsv1.Receipt) error
}

func NewEngine(registry *Registry, capabilities CapabilityCatalog, outputs OutputCatalog, provider Provider, ledger Ledger) *Engine {
	capabilityCopy := make(CapabilityCatalog, len(capabilities))
	for name, authority := range capabilities {
		capabilityCopy[name] = authority
	}
	outputCopy := make(OutputCatalog, len(outputs))
	for schema, validator := range outputs {
		outputCopy[schema] = validator
	}
	return &Engine{registry: registry, capabilities: capabilityCopy, outputs: outputCopy, provider: provider, ledger: ledger, approvalActors: map[string]map[string]bool{}}
}

func (e *Engine) WithApprovalAuthorities(authorities ApprovalAuthorityCatalog) *Engine {
	for policy, actors := range authorities {
		if e.approvalActors[policy] == nil {
			e.approvalActors[policy] = map[string]bool{}
		}
		for _, actor := range actors {
			if actor = strings.TrimSpace(actor); actor != "" {
				e.approvalActors[policy][actor] = true
			}
		}
	}
	return e
}

func (e *Engine) runAgentNode(ctx context.Context, request RunRequest) (RunResult, error) {
	return e.runAgentNodeAt(ctx, request, nil)
}

func (e *Engine) runAgentNodeAt(ctx context.Context, request RunRequest, reservedAt *time.Time) (RunResult, error) {
	return e.runAgentNodeResolvedAt(ctx, request, reservedAt, nil)
}

func (e *Engine) runAgentNodeResolvedAt(ctx context.Context, request RunRequest, reservedAt *time.Time, preparedContext *resolvedContext) (RunResult, error) {
	if e == nil || e.provider == nil || e.ledger == nil {
		return RunResult{}, errors.New("provider and ledger are required")
	}
	if err := contract.ValidateDefinition("JobDefinition", request.Job); err != nil {
		return RunResult{}, err
	}
	if err := contract.ValidateDefinition("CampaignDefinition", request.Campaign); err != nil {
		return RunResult{}, err
	}
	aggregateID, err := executionID(request)
	if err != nil {
		return RunResult{}, err
	}
	transitionAt := time.Now().UTC()
	if reservedAt != nil {
		transitionAt = reservedAt.UTC()
	}
	existingReplay, replayErr := e.ledger.Replay(aggregateID)
	if replayErr == nil {
		transitionAt = existingReplay.Receipts[0].OccurredAt
	} else if !errors.Is(replayErr, ErrReplayEmpty) {
		return RunResult{}, replayErr
	}
	admission, admissionReplay, err := e.admissionForRun(request)
	if err != nil {
		return RunResult{}, err
	}
	compiled, compileReceipt, err := compileWorkflow(request.Workflow, e.registry, aggregateID, transitionAt)
	if err != nil {
		return RunResult{}, err
	}
	if compiled.DefinitionHash != admission.DefinitionHash || compiled.CompileHash != admission.CompileHash {
		return RunResult{}, errors.New("admitted Workflow does not match the compiled execution contract")
	}
	compileReceipt, err = bindCompileReceiptToAdmission(compileReceipt, admission.Receipt.ReceiptHash)
	if err != nil {
		return RunResult{}, err
	}
	if err := validateRunBinding(request, compiled, transitionAt); err != nil {
		return RunResult{}, err
	}
	jobHash, campaignHash, err := aggregateDefinitionHashes(request)
	if err != nil {
		return RunResult{}, err
	}
	node, ok := compiledNodeByID(compiled, request.NodeID)
	if !ok {
		return RunResult{}, fmt.Errorf("node %q is not in the workflow", request.NodeID)
	}
	if node.Definition.Kind != contractsv1.NodeDefinitionKindAgent {
		return RunResult{}, fmt.Errorf("node %q is not an agent node", request.NodeID)
	}
	budgetEnforced := request.BudgetOverride != nil
	if budgetEnforced {
		definition, err := tightenNodeBudget(node.Definition, *request.BudgetOverride)
		if err != nil {
			return RunResult{}, err
		}
		node.Definition = definition
	}
	if err := validateOutputCatalog(node.Definition, e.outputs); err != nil {
		return RunResult{}, err
	}
	if replayErr == nil {
		replay := existingReplay
		if invocation, ok, err := materializeInvocation(replay); err != nil {
			return RunResult{}, err
		} else if ok {
			if err := validateReplayBinding(invocation, request, compiled, node, jobHash, campaignHash); err != nil {
				return RunResult{}, err
			}
			if hasReceipt(replay, contractsv1.ReceiptReceiptTypeTerminal) {
				if !hasReceipt(replay, contractsv1.ReceiptReceiptTypeResult) {
					return RunResult{}, ErrProviderDeadline
				}
				material, err := MaterializeReplay(replay, e.outputs)
				if err != nil {
					return RunResult{}, err
				}
				return RunResult{Compiled: compiled, Bundle: invocation.Bundle, Artifacts: material.Artifacts, AdmissionReplay: admissionReplay, Replay: replay}, nil
			}
			return e.resumeInvocation(ctx, aggregateID, transitionAt, compiled, invocation, admissionReplay, replay)
		}
	}
	if _, ok := e.ledger.(AtomicLedger); !ok {
		return RunResult{}, errors.New("provider execution requires an AtomicLedger")
	}
	resolved := resolvedContext{}
	if preparedContext == nil {
		resolved, err = resolveContext(ctx, e.registry, request, compiled, node, compileReceipt)
		if err != nil {
			return RunResult{}, err
		}
	} else {
		resolved = *preparedContext
		if err := validateRecordedContextForNode(resolved, node, request.Campaign.Scope, request.Campaign.EvidenceFrontier.Cutoff); err != nil {
			return RunResult{}, err
		}
	}
	manifest, err := e.capabilityManifest(node.Definition)
	if err != nil {
		return RunResult{}, err
	}
	invocationKey, err := Digest(struct {
		AggregateID  string
		NodeID       contractsv1.Identifier
		JobHash      contractsv1.SHA256
		CampaignHash contractsv1.SHA256
		CompileHash  contractsv1.SHA256
		BundleHash   contractsv1.SHA256
		Manifest     contractsv1.SHA256
	}{aggregateID, node.Definition.Id, jobHash, campaignHash, compiled.CompileHash, resolved.Bundle.BundleHash, manifest.ManifestHash})
	if err != nil {
		return RunResult{}, err
	}
	deadline := transitionAt
	duration := time.Duration(0)
	if node.Definition.DeadlineSeconds != nil {
		duration = time.Duration(*node.Definition.DeadlineSeconds) * time.Second
	} else {
		duration = time.Duration(*node.Definition.Budget.MaxDurationSeconds) * time.Second
	}
	deadline = deadline.Add(duration)
	intentChain, ok := contextPackByType(resolved.Packs, "intent-chain")
	if !ok {
		return RunResult{}, errors.New("compiled intent-chain context is missing")
	}
	invocation := Invocation{
		IdempotencyKey: invocationKey, JobID: request.Job.Id, CampaignID: request.Campaign.Id,
		WorkflowRef: compiled.WorkflowRef, Node: node.Definition, Playbook: request.Workflow.Intent,
		IntentChain: intentChain, Context: resolved.Packs,
		Bundle: resolved.Bundle, Capabilities: manifest, Budget: node.Definition.Budget, BudgetEnforced: budgetEnforced, Deadline: deadline,
		InputHashes: []contractsv1.SHA256{admission.Receipt.ReceiptHash, jobHash, campaignHash, compiled.CompileHash, resolved.Bundle.BundleHash, manifest.ManifestHash},
	}
	if err := validateJSONLimit("invocation material", invocation, maxReceiptMaterialBytes); err != nil {
		return RunResult{}, err
	}
	preReceipts, err := preExecutionReceipts(aggregateID, transitionAt, compileReceipt, invocation)
	if err != nil {
		return RunResult{}, err
	}
	for _, receipt := range preReceipts {
		if err := e.ledger.Append(receipt); err != nil {
			return RunResult{}, err
		}
	}
	replay, err := e.ledger.Replay(aggregateID)
	if err != nil {
		return RunResult{}, err
	}
	return e.resumeInvocation(ctx, aggregateID, transitionAt, compiled, invocation, admissionReplay, replay)
}

func (e *Engine) resumeInvocation(ctx context.Context, aggregateID string, occurredAt time.Time, compiled CompiledWorkflow, invocation Invocation, admissionReplay, replay contractsv1.ReplayBundle) (RunResult, error) {
	if _, ok := e.ledger.(AtomicLedger); !ok {
		return RunResult{}, errors.New("provider execution requires an AtomicLedger")
	}
	if storedResult, ok, err := materializeProviderResult(replay); err != nil {
		return RunResult{}, err
	} else if ok {
		artifacts := storedResult.Artifacts
		if err := validateProviderResult(storedResult, invocation); err != nil {
			return RunResult{}, err
		}
		if err := validateArtifactContracts(artifacts, invocation, e.outputs); err != nil {
			return RunResult{}, err
		}
		if err := validateArtifactBudget(artifacts, invocation); err != nil {
			if terminalState(replay) == "budget_exhausted" {
				return RunResult{}, err
			}
			if terminalState(replay) == "" {
				if appendErr := e.finishRejectedInvocation(aggregateID, occurredAt, invocation, storedResult, replay); appendErr != nil {
					return RunResult{}, appendErr
				}
				return RunResult{}, err
			}
			return RunResult{}, errors.New("accepted provider result exceeds its recorded budget")
		}
		if err := e.finishInvocation(aggregateID, occurredAt, invocation, storedResult, replay); err != nil {
			return RunResult{}, err
		}
		completed, err := e.ledger.Replay(aggregateID)
		if err != nil {
			return RunResult{}, err
		}
		return RunResult{Compiled: compiled, Bundle: invocation.Bundle, Artifacts: artifacts, AdmissionReplay: admissionReplay, Replay: completed}, nil
	}
	remaining := time.Until(invocation.Deadline)
	if remaining > 0 {
		providerContext, cancel := context.WithTimeout(ctx, remaining)
		err := e.provider.Start(providerContext, invocation)
		cancel()
		if err != nil {
			return RunResult{}, fmt.Errorf("provider start: %w", err)
		}
	}
	pollWindow := 5 * time.Second
	if remaining > 0 && remaining < pollWindow {
		pollWindow = remaining
	}
	pollContext, cancel := context.WithTimeout(ctx, pollWindow)
	providerResult, ready, err := e.provider.Poll(pollContext, invocation.IdempotencyKey)
	cancel()
	if err != nil {
		return RunResult{}, fmt.Errorf("provider poll: %w", err)
	}
	if !ready {
		if time.Now().Before(invocation.Deadline) {
			return RunResult{}, ErrProviderNotReady
		}
		cancelContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = e.provider.Cancel(cancelContext, invocation.IdempotencyKey)
		cancel()
		if err := e.appendDeadlineTerminal(aggregateID, occurredAt, invocation, replay); err != nil {
			return RunResult{}, err
		}
		return RunResult{}, ErrProviderDeadline
	}
	if err := validateProviderResult(providerResult, invocation); err != nil {
		return RunResult{}, err
	}
	deadlinePassed := !time.Now().Before(invocation.Deadline)
	acknowledged := false
	if deadlinePassed {
		acknowledged, err = providerAcknowledged(replay, invocation, providerResult)
		if err != nil {
			return RunResult{}, err
		}
	}
	if providerResult.CompletedAt.After(invocation.Deadline) || (deadlinePassed && !acknowledged) {
		cancelContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = e.provider.Cancel(cancelContext, invocation.IdempotencyKey)
		cancel()
		if err := e.appendDeadlineTerminal(aggregateID, occurredAt, invocation, replay); err != nil {
			return RunResult{}, err
		}
		return RunResult{}, ErrProviderDeadline
	}
	artifacts := providerResult.Artifacts
	if err := validateArtifactContracts(artifacts, invocation, e.outputs); err != nil {
		return RunResult{}, err
	}
	if err := validateArtifactBudget(artifacts, invocation); err != nil {
		if appendErr := e.finishRejectedInvocation(aggregateID, occurredAt, invocation, providerResult, replay); appendErr != nil {
			return RunResult{}, appendErr
		}
		return RunResult{}, err
	}
	if err := e.finishInvocation(aggregateID, occurredAt, invocation, providerResult, replay); err != nil {
		return RunResult{}, err
	}
	replay, err = e.ledger.Replay(aggregateID)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Compiled: compiled, Bundle: invocation.Bundle, Artifacts: artifacts, AdmissionReplay: admissionReplay, Replay: replay}, nil
}

func providerAcknowledged(replay contractsv1.ReplayBundle, invocation Invocation, result ProviderResult) (bool, error) {
	receipt, ok := receiptByType(replay, contractsv1.ReceiptReceiptTypeProviderExecution)
	if !ok {
		return false, nil
	}
	var key string
	if err := decodePayload(receipt.Payload["idempotency_key"], &key); err != nil {
		return false, err
	}
	var completedAt time.Time
	if err := decodePayload(receipt.Payload["completed_at"], &completedAt); err != nil {
		return false, err
	}
	hashes, err := actionArtifactHashes(result.Artifacts)
	if err != nil {
		return false, err
	}
	return key == invocation.IdempotencyKey && completedAt.Equal(result.CompletedAt) && !completedAt.After(invocation.Deadline) && reflect.DeepEqual(receipt.OutputHashes, hashes), nil
}

func validateProviderResult(result ProviderResult, invocation Invocation) error {
	if result.IdempotencyKey != invocation.IdempotencyKey || result.CompletedAt.IsZero() {
		return errors.New("provider result identity or completion time is invalid")
	}
	return nil
}

var ErrProviderDeadline = errors.New("provider result unavailable after node deadline")
var ErrProviderNotReady = errors.New("provider result is not ready")

func (e *Engine) appendDeadlineTerminal(aggregateID string, occurredAt time.Time, invocation Invocation, replay contractsv1.ReplayBundle) error {
	previous := replay.Receipts[len(replay.Receipts)-1]
	receipt, err := sealReceipt(aggregateID, previous.AggregateVersion+1, contractsv1.ReceiptReceiptTypeTerminal, occurredAt, &previous.ReceiptHash,
		invocation.InputHashes, nil, map[string]any{"state": "deadline_expired"})
	if err != nil {
		return err
	}
	return e.ledger.Append(receipt)
}

func (e *Engine) finishInvocation(aggregateID string, occurredAt time.Time, invocation Invocation, providerResult ProviderResult, replay contractsv1.ReplayBundle) error {
	return e.finishInvocationWithState(aggregateID, occurredAt, invocation, providerResult, replay, true, "node_completed")
}

func (e *Engine) finishRejectedInvocation(aggregateID string, occurredAt time.Time, invocation Invocation, providerResult ProviderResult, replay contractsv1.ReplayBundle) error {
	return e.finishInvocationWithState(aggregateID, occurredAt, invocation, providerResult, replay, false, "budget_exhausted")
}

func (e *Engine) finishInvocationWithState(aggregateID string, occurredAt time.Time, invocation Invocation, providerResult ProviderResult, replay contractsv1.ReplayBundle, accepted bool, terminalState string) error {
	artifactHashes, err := actionArtifactHashes(providerResult.Artifacts)
	if err != nil {
		return err
	}
	invocationReceipt, ok := receiptByType(replay, contractsv1.ReceiptReceiptTypeInvocation)
	if !ok {
		return errors.New("replay has no invocation receipt")
	}
	receipts, err := postExecutionReceiptsWithState(aggregateID, occurredAt, invocationReceipt, invocation, providerResult, artifactHashes, accepted, terminalState)
	if err != nil {
		return err
	}
	if providerReceipt, exists := receiptByType(replay, contractsv1.ReceiptReceiptTypeProviderExecution); exists {
		acknowledged, err := providerAcknowledged(replay, invocation, providerResult)
		if err != nil || !acknowledged {
			return errors.New("provider execution receipt does not match the exact result")
		}
		resultAndTerminal, err := postResultReceiptsWithState(aggregateID, occurredAt, providerReceipt, providerResult, artifactHashes, accepted, terminalState)
		if err != nil {
			return err
		}
		return appendReceiptBatch(e.ledger, resultAndTerminal)
	}
	if err := e.ledger.Append(receipts[0]); err != nil {
		return err
	}
	return appendReceiptBatch(e.ledger, receipts[1:])
}

func appendReceiptBatch(ledger Ledger, receipts []contractsv1.Receipt) error {
	atomic, ok := ledger.(AtomicLedger)
	if !ok {
		return errors.New("ledger does not support atomic receipt batches")
	}
	return atomic.AppendBatch(receipts)
}

func actionArtifactHashes(artifacts []contractsv1.ActionArtifact) ([]contractsv1.SHA256, error) {
	hashes := make([]contractsv1.SHA256, 0, len(artifacts))
	for _, artifact := range artifacts {
		hash, err := Digest(artifact)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, contractsv1.SHA256(hash))
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
	return hashes, nil
}

func hasReceipt(bundle contractsv1.ReplayBundle, receiptType contractsv1.ReceiptReceiptType) bool {
	_, ok := receiptByType(bundle, receiptType)
	return ok
}

func terminalState(bundle contractsv1.ReplayBundle) string {
	receipt, ok := receiptByType(bundle, contractsv1.ReceiptReceiptTypeTerminal)
	if !ok {
		return ""
	}
	var state string
	if decodePayload(receipt.Payload["state"], &state) != nil {
		return ""
	}
	return state
}

func receiptByType(bundle contractsv1.ReplayBundle, receiptType contractsv1.ReceiptReceiptType) (contractsv1.Receipt, bool) {
	for _, receipt := range bundle.Receipts {
		if receipt.ReceiptType == receiptType {
			return receipt, true
		}
	}
	return contractsv1.Receipt{}, false
}

func materializeInvocation(bundle contractsv1.ReplayBundle) (Invocation, bool, error) {
	if err := VerifyReplay(bundle); err != nil {
		return Invocation{}, false, err
	}
	for _, receipt := range bundle.Receipts {
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeInvocation {
			var invocation Invocation
			if err := decodePayload(receipt.Payload["invocation"], &invocation); err != nil {
				return Invocation{}, false, fmt.Errorf("materialize invocation: %w", err)
			}
			return invocation, true, nil
		}
	}
	return Invocation{}, false, nil
}

// MaterializeInvocation returns the exact invocation recorded by a verified Replay.
func MaterializeInvocation(bundle contractsv1.ReplayBundle) (Invocation, error) {
	invocation, ok, err := materializeInvocation(bundle)
	if err != nil {
		return Invocation{}, err
	}
	if !ok {
		return Invocation{}, errors.New("replay has no materialized invocation")
	}
	if err := VerifyContextBundle(invocation.Bundle, invocation.Context); err != nil {
		return Invocation{}, err
	}
	for _, pack := range invocation.Context {
		found := false
		for _, receipt := range bundle.Receipts {
			if receipt.ReceiptType != contractsv1.ReceiptReceiptTypePackEdition {
				continue
			}
			var recorded contractsv1.ContextPackEdition
			if err := decodePayload(receipt.Payload["pack"], &recorded); err != nil {
				return Invocation{}, err
			}
			if reflect.DeepEqual(recorded, pack) {
				found = true
				break
			}
		}
		if !found {
			return Invocation{}, fmt.Errorf("context pack %q has no exact edition receipt", pack.Id)
		}
	}
	intent, ok := contextPackByType(invocation.Context, "intent-chain")
	if !ok || !reflect.DeepEqual(intent, invocation.IntentChain) {
		return Invocation{}, errors.New("replay intent chain does not match invocation context")
	}
	if err := VerifyCapabilityManifest(invocation.Capabilities); err != nil {
		return Invocation{}, err
	}
	return invocation, nil
}

// VerifyDefinitionBinding preserves verification of historical pre-admission Replays.
func VerifyDefinitionBinding(bundle contractsv1.ReplayBundle, job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition, definition contractsv1.WorkflowDefinition) (Invocation, error) {
	return verifyDefinitionBinding(bundle, nil, job, campaign, definition)
}

// VerifyDefinitionBindingWithAdmission additionally proves the canonical Workflow admission.
func VerifyDefinitionBindingWithAdmission(bundle, admissionReplay contractsv1.ReplayBundle, job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition, definition contractsv1.WorkflowDefinition) (Invocation, error) {
	return verifyDefinitionBinding(bundle, &admissionReplay, job, campaign, definition)
}

func verifyDefinitionBinding(bundle contractsv1.ReplayBundle, admissionReplay *contractsv1.ReplayBundle, job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition, definition contractsv1.WorkflowDefinition) (Invocation, error) {
	invocation, err := MaterializeInvocation(bundle)
	if err != nil {
		return Invocation{}, err
	}
	jobHash, campaignHash, err := aggregateDefinitionHashes(RunRequest{Job: job, Campaign: campaign})
	if err != nil {
		return Invocation{}, err
	}
	body, err := json.Marshal(definition)
	if err != nil {
		return Invocation{}, err
	}
	identity, err := contract.ValidateWorkflow(body)
	if err != nil {
		return Invocation{}, err
	}
	compileReceipt, ok := receiptByType(bundle, contractsv1.ReceiptReceiptTypeCompile)
	if !ok || len(compileReceipt.OutputHashes) != 1 {
		return Invocation{}, errors.New("replay compile receipt is incomplete")
	}
	if admissionReplay == nil {
		if len(compileReceipt.InputHashes) != 1 || len(invocation.InputHashes) != 5 || invocation.InputHashes[0] != jobHash || invocation.InputHashes[1] != campaignHash ||
			invocation.InputHashes[2] != compileReceipt.OutputHashes[0] || invocation.InputHashes[3] != invocation.Bundle.BundleHash ||
			invocation.InputHashes[4] != invocation.Capabilities.ManifestHash || compileReceipt.InputHashes[0] != contractsv1.SHA256(identity.Hash) {
			return Invocation{}, errors.New("replay does not bind the displayed definitions")
		}
		return invocation, nil
	}
	if len(compileReceipt.InputHashes) != 2 {
		return Invocation{}, errors.New("replay compile receipt is incomplete")
	}
	admission, err := MaterializeAdmission(*admissionReplay, definition.Version)
	if err != nil || !reflect.DeepEqual(admission.Job, job) || !reflect.DeepEqual(admission.Campaign, campaign) || !reflect.DeepEqual(admission.Workflow, definition) || admission.Receipt.ReceiptHash != compileReceipt.InputHashes[1] {
		return Invocation{}, errors.New("replay does not bind a canonical Workflow admission")
	}
	if len(invocation.InputHashes) != 6 || invocation.InputHashes[0] != compileReceipt.InputHashes[1] || invocation.InputHashes[1] != jobHash || invocation.InputHashes[2] != campaignHash ||
		invocation.InputHashes[3] != compileReceipt.OutputHashes[0] || invocation.InputHashes[4] != invocation.Bundle.BundleHash ||
		invocation.InputHashes[5] != invocation.Capabilities.ManifestHash || compileReceipt.InputHashes[0] != contractsv1.SHA256(identity.Hash) {
		return Invocation{}, errors.New("replay does not bind the displayed definitions")
	}
	return invocation, nil
}

func materializeProviderResult(bundle contractsv1.ReplayBundle) (ProviderResult, bool, error) {
	receipt, ok := receiptByType(bundle, contractsv1.ReceiptReceiptTypeResult)
	if !ok {
		return ProviderResult{}, false, nil
	}
	var result ProviderResult
	if err := decodePayload(receipt.Payload["provider_result"], &result); err != nil {
		return ProviderResult{}, false, fmt.Errorf("materialize provider result: %w", err)
	}
	return result, true, nil
}

func contextPackByType(packs []contractsv1.ContextPackEdition, packType string) (contractsv1.ContextPackEdition, bool) {
	for _, pack := range packs {
		if string(pack.PackType) == packType {
			return pack, true
		}
	}
	return contractsv1.ContextPackEdition{}, false
}

func validateReplayBinding(invocation Invocation, request RunRequest, compiled CompiledWorkflow, node CompiledNode, jobHash, campaignHash contractsv1.SHA256) error {
	if invocation.JobID != request.Job.Id || invocation.CampaignID != request.Campaign.Id || invocation.WorkflowRef != compiled.WorkflowRef || invocation.Node.Id != node.Definition.Id {
		return errors.New("recorded invocation identity does not match redelivery")
	}
	if len(invocation.InputHashes) != 6 || invocation.InputHashes[1] != jobHash || invocation.InputHashes[2] != campaignHash || invocation.InputHashes[3] != compiled.CompileHash {
		return errors.New("recorded invocation inputs do not match redelivery")
	}
	return nil
}

func executionID(request RunRequest) (string, error) {
	hash, err := Digest(struct {
		JobID      contractsv1.Identifier
		CampaignID contractsv1.Identifier
		Workflow   string
		NodeID     string
	}{request.Job.Id, request.Campaign.Id, fmt.Sprintf("%s@%d", request.Workflow.Id, request.Workflow.Version), request.NodeID})
	if err != nil {
		return "", err
	}
	return shortID("run-", hash), nil
}

func validateRunBinding(request RunRequest, compiled CompiledWorkflow, transitionAt time.Time) error {
	if err := validateCampaignWorkflowBinding(request.Job, request.Campaign, compiled.WorkflowRef); err != nil {
		return err
	}
	if request.Campaign.EvidenceFrontier.Cutoff.After(transitionAt) {
		return errors.New("campaign evidence cutoff is in the future")
	}
	return nil
}

func validateCampaignWorkflowBinding(job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition, workflowRef contractsv1.WorkflowRef) error {
	if job.Intent.Kind != contractsv1.IntentCardKindJob || campaign.Intent.Kind != contractsv1.IntentCardKindCampaign {
		return errors.New("job or campaign intent kind is invalid")
	}
	if campaign.JobId != job.Id {
		return errors.New("campaign is not bound to the job")
	}
	if !scopeWithin(job.Scope, campaign.Scope) {
		return errors.New("campaign scope is outside the job")
	}
	found := false
	for _, candidate := range campaign.WorkflowPlan {
		if candidate == workflowRef {
			found = true
			break
		}
	}
	if !found {
		return errors.New("workflow is not pinned by the campaign")
	}
	return nil
}

func scopeWithin(job, campaign contractsv1.Scope) bool {
	if job.SubjectType != campaign.SubjectType {
		return false
	}
	allowed := make(map[string]bool, len(job.SubjectIds))
	for _, id := range job.SubjectIds {
		allowed[id] = true
	}
	for _, id := range campaign.SubjectIds {
		if !allowed[id] {
			return false
		}
	}
	for key, value := range job.Labels {
		if candidate, ok := campaign.Labels[key]; !ok || candidate != value {
			return false
		}
	}
	return true
}

func (e *Engine) admissionForRun(request RunRequest) (contractsv1.WorkflowAdmission, contractsv1.ReplayBundle, error) {
	replay, err := e.ledger.Replay(workflowDefinitionAggregate(string(request.Workflow.Id)))
	if err != nil {
		return contractsv1.WorkflowAdmission{}, contractsv1.ReplayBundle{}, errors.New("Workflow is not canonically admitted")
	}
	admission, err := MaterializeAdmission(replay, request.Workflow.Version)
	if err != nil {
		return contractsv1.WorkflowAdmission{}, contractsv1.ReplayBundle{}, err
	}
	if !reflect.DeepEqual(admission.Job, request.Job) || !reflect.DeepEqual(admission.Campaign, request.Campaign) || !reflect.DeepEqual(admission.Workflow, request.Workflow) {
		return contractsv1.WorkflowAdmission{}, contractsv1.ReplayBundle{}, errors.New("Workflow admission does not bind this Job and Campaign")
	}
	return admission, replay, nil
}

func bindCompileReceiptToAdmission(receipt contractsv1.Receipt, admissionHash contractsv1.SHA256) (contractsv1.Receipt, error) {
	if len(receipt.InputHashes) != 1 {
		return contractsv1.Receipt{}, errors.New("compile receipt is invalid")
	}
	return sealReceipt(receipt.AggregateId, receipt.AggregateVersion, receipt.ReceiptType, receipt.OccurredAt, nil,
		[]contractsv1.SHA256{receipt.InputHashes[0], admissionHash}, receipt.OutputHashes, receipt.Payload)
}

func aggregateDefinitionHashes(request RunRequest) (contractsv1.SHA256, contractsv1.SHA256, error) {
	jobHash, jobIntentHash, err := contract.DefinitionHashes(request.Job)
	if err != nil {
		return "", "", err
	}
	if request.Job.DefinitionHash != nil && *request.Job.DefinitionHash != contractsv1.SHA256(jobHash) {
		return "", "", errors.New("job definition_hash does not match")
	}
	if request.Job.Intent.DescriptorHash != nil && *request.Job.Intent.DescriptorHash != contractsv1.SHA256(jobIntentHash) {
		return "", "", errors.New("job intent descriptor_hash does not match")
	}
	campaignHash, campaignIntentHash, err := contract.DefinitionHashes(request.Campaign)
	if err != nil {
		return "", "", err
	}
	if request.Campaign.DefinitionHash != nil && *request.Campaign.DefinitionHash != contractsv1.SHA256(campaignHash) {
		return "", "", errors.New("campaign definition_hash does not match")
	}
	if request.Campaign.Intent.DescriptorHash != nil && *request.Campaign.Intent.DescriptorHash != contractsv1.SHA256(campaignIntentHash) {
		return "", "", errors.New("campaign intent descriptor_hash does not match")
	}
	return contractsv1.SHA256(jobHash), contractsv1.SHA256(campaignHash), nil
}

func (e *Engine) capabilityManifest(node contractsv1.NodeDefinition) (contractsv1.CapabilityManifest, error) {
	names := sortedStrings(node.Capabilities)
	manifest := contractsv1.CapabilityManifest{Kind: contractsv1.CapabilityManifestKindCapabilityManifest, SchemaVersion: 1, Capabilities: []contractsv1.CapabilityManifestCapabilitiesElem{}}
	for _, name := range names {
		authority, ok := e.capabilities[name]
		if !ok {
			return manifest, fmt.Errorf("capability %q is not registered", name)
		}
		manifest.Capabilities = append(manifest.Capabilities, contractsv1.CapabilityManifestCapabilitiesElem{Name: contractsv1.Identifier(name), Authority: authority})
	}
	identityHash, err := Digest(manifest.Capabilities)
	if err != nil {
		return manifest, err
	}
	manifest.Id = shortID("capability-", identityHash)
	hash, err := Digest(manifest)
	if err != nil {
		return manifest, err
	}
	manifest.ManifestHash = contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("CapabilityManifest", manifest); err != nil {
		return manifest, err
	}
	if err := VerifyCapabilityManifest(manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func VerifyCapabilityManifest(manifest contractsv1.CapabilityManifest) error {
	if err := contract.ValidateDefinition("CapabilityManifest", manifest); err != nil {
		return err
	}
	expected := manifest.ManifestHash
	manifest.ManifestHash = ""
	hash, err := Digest(manifest)
	if err != nil || contractsv1.SHA256(hash) != expected {
		return errors.New("capability manifest hash does not match")
	}
	return nil
}

func validateOutputCatalog(node contractsv1.NodeDefinition, outputs OutputCatalog) error {
	for _, slot := range node.OutputSlots {
		if slot.ContentSchema == nil {
			return fmt.Errorf("output slot %q has no content schema", slot.Id)
		}
		if outputs[*slot.ContentSchema] == nil {
			return fmt.Errorf("output schema %q is not registered", *slot.ContentSchema)
		}
	}
	return nil
}

func validateArtifacts(artifacts []contractsv1.ActionArtifact, invocation Invocation, outputs OutputCatalog) error {
	if err := validateArtifactContracts(artifacts, invocation, outputs); err != nil {
		return err
	}
	return validateArtifactBudget(artifacts, invocation)
}

func validateArtifactContracts(artifacts []contractsv1.ActionArtifact, invocation Invocation, outputs OutputCatalog) error {
	if err := validateJSONLimit("action artifact set", artifacts, maxReceiptMaterialBytes); err != nil {
		return err
	}
	expectedInputs := invocation.InputHashes
	counts := make(map[contractsv1.Identifier]int)
	slots := make(map[contractsv1.Identifier]contractsv1.Slot, len(invocation.Node.OutputSlots))
	for _, slot := range invocation.Node.OutputSlots {
		slots[slot.ArtifactType] = slot
	}
	ids := make(map[string]struct{})
	for _, artifact := range artifacts {
		if err := validateBoundedJSON("action artifact content", artifact.Content); err != nil {
			return err
		}
		if err := contract.ValidateDefinition("ActionArtifact", artifact); err != nil {
			return err
		}
		if artifact.Kind != contractsv1.ActionArtifactKindActionArtifact || artifact.SchemaVersion != 1 {
			return errors.New("provider returned an unknown action artifact contract")
		}
		if artifact.JobId != invocation.JobID || artifact.CampaignId != invocation.CampaignID || artifact.WorkflowRef != invocation.WorkflowRef || artifact.NodeId != invocation.Node.Id {
			return errors.New("provider artifact identity does not match the invocation")
		}
		if !reflect.DeepEqual(artifact.InputHashes, expectedInputs) {
			return errors.New("provider artifact inputs do not match the invocation")
		}
		if _, exists := ids[artifact.Id]; exists {
			return fmt.Errorf("provider artifact id %q is duplicated", artifact.Id)
		}
		ids[artifact.Id] = struct{}{}
		hash, err := Digest(artifact.Content)
		if err != nil {
			return err
		}
		if contractsv1.SHA256(hash) != artifact.ContentSha256 {
			return fmt.Errorf("provider artifact %q content hash does not match", artifact.Id)
		}
		slot, ok := slots[artifact.ArtifactType]
		if !ok || slot.ArtifactKind == nil || *slot.ArtifactKind != contractsv1.SlotArtifactKindActionArtifact || slot.ContentSchema == nil {
			return fmt.Errorf("provider artifact %q does not match an Action Artifact output slot", artifact.Id)
		}
		value := reflect.ValueOf(artifact.Content)
		if slot.CountsAsCandidates && value.IsValid() && (value.Kind() == reflect.Array || value.Kind() == reflect.Slice) {
			return fmt.Errorf("provider artifact %q batches candidate records; emit one Action Artifact per candidate", artifact.Id)
		}
		if err := outputs[*slot.ContentSchema](artifact.Content); err != nil {
			return fmt.Errorf("provider artifact %q content schema: %w", artifact.Id, err)
		}
		counts[artifact.ArtifactType]++
	}
	for _, slot := range invocation.Node.OutputSlots {
		count := counts[slot.ArtifactType]
		if count < slot.MinItems || count > slot.MaxItems {
			return fmt.Errorf("provider output %q has %d items outside [%d,%d]", slot.ArtifactType, count, slot.MinItems, slot.MaxItems)
		}
		delete(counts, slot.ArtifactType)
	}
	if len(counts) > 0 {
		return errors.New("provider returned an undeclared artifact type")
	}
	return nil
}

func validateArtifactBudget(artifacts []contractsv1.ActionArtifact, invocation Invocation) error {
	if !invocation.BudgetEnforced {
		return nil
	}
	if len(artifacts) > invocation.Budget.MaxActions {
		return budgetExceededError{code: "action-budget-exhausted"}
	}
	if candidates := artifactCandidateCount(artifacts, invocation.Node.OutputSlots); candidates > invocation.Budget.MaxCandidates {
		return budgetExceededError{code: "candidate-budget-exhausted"}
	}
	return nil
}

func artifactCandidateCount(artifacts []contractsv1.ActionArtifact, slots []contractsv1.Slot) int {
	candidateTypes := make(map[contractsv1.Identifier]bool, len(slots))
	for _, slot := range slots {
		if slot.CountsAsCandidates {
			candidateTypes[slot.ArtifactType] = true
		}
	}
	total := 0
	for _, artifact := range artifacts {
		if !candidateTypes[artifact.ArtifactType] {
			continue
		}
		total++
	}
	return total
}

func tightenNodeBudget(node contractsv1.NodeDefinition, remaining contractsv1.Budget) (contractsv1.NodeDefinition, error) {
	originalBudget := node.Budget
	if remaining.MaxAttempts < 1 || remaining.MaxAttempts > originalBudget.MaxAttempts || remaining.MaxActions < 0 || remaining.MaxActions > originalBudget.MaxActions || remaining.MaxCandidates < 0 || remaining.MaxCandidates > originalBudget.MaxCandidates {
		return contractsv1.NodeDefinition{}, errors.New("budget override may only tighten the admitted Node budget")
	}
	var admittedDuration int
	if node.DeadlineSeconds != nil {
		admittedDuration = *node.DeadlineSeconds
	} else if originalBudget.MaxDurationSeconds != nil {
		admittedDuration = *originalBudget.MaxDurationSeconds
	} else {
		return contractsv1.NodeDefinition{}, errors.New("admitted Node duration budget is missing")
	}
	duration := admittedDuration
	if remaining.MaxDurationSeconds != nil && *remaining.MaxDurationSeconds < duration {
		duration = *remaining.MaxDurationSeconds
	}
	node.Budget = remaining
	node.DeadlineSeconds = &duration
	node.Budget.MaxDurationSeconds = &duration
	return node, nil
}

func preExecutionReceipts(aggregateID string, occurredAt time.Time, compileReceipt contractsv1.Receipt, invocation Invocation) ([]contractsv1.Receipt, error) {
	receipts := []contractsv1.Receipt{compileReceipt}
	previous := compileReceipt.ReceiptHash
	for _, pack := range invocation.Context {
		packHash, err := Digest(pack)
		if err != nil {
			return nil, err
		}
		packReceipt, err := sealReceipt(aggregateID, len(receipts)+1, contractsv1.ReceiptReceiptTypePackEdition, occurredAt, &previous,
			[]contractsv1.SHA256{pack.ContentSha256}, []contractsv1.SHA256{contractsv1.SHA256(packHash)}, map[string]any{"pack": pack})
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, packReceipt)
		previous = packReceipt.ReceiptHash
	}
	invocationReceipt, err := sealReceipt(aggregateID, len(receipts)+1, contractsv1.ReceiptReceiptTypeInvocation, occurredAt, &previous,
		invocation.InputHashes, nil,
		map[string]any{"invocation": invocation})
	if err != nil {
		return nil, err
	}
	return append(receipts, invocationReceipt), nil
}

func postExecutionReceipts(aggregateID string, occurredAt time.Time, previousReceipt contractsv1.Receipt, invocation Invocation, providerResult ProviderResult, artifactHashes []contractsv1.SHA256) ([]contractsv1.Receipt, error) {
	return postExecutionReceiptsWithState(aggregateID, occurredAt, previousReceipt, invocation, providerResult, artifactHashes, true, "node_completed")
}

func postExecutionReceiptsWithState(aggregateID string, occurredAt time.Time, previousReceipt contractsv1.Receipt, invocation Invocation, providerResult ProviderResult, artifactHashes []contractsv1.SHA256, accepted bool, terminalState string) ([]contractsv1.Receipt, error) {
	version := previousReceipt.AggregateVersion + 1
	previous := previousReceipt.ReceiptHash
	providerReceipt, err := sealReceipt(aggregateID, version, contractsv1.ReceiptReceiptTypeProviderExecution, occurredAt, &previous,
		[]contractsv1.SHA256{contractsv1.SHA256(invocation.IdempotencyKey)}, artifactHashes,
		map[string]any{"node_id": invocation.Node.Id, "idempotency_key": providerResult.IdempotencyKey, "completed_at": providerResult.CompletedAt})
	if err != nil {
		return nil, err
	}
	resultAndTerminal, err := postResultReceiptsWithState(aggregateID, occurredAt, providerReceipt, providerResult, artifactHashes, accepted, terminalState)
	if err != nil {
		return nil, err
	}
	return append([]contractsv1.Receipt{providerReceipt}, resultAndTerminal...), nil
}

func postResultReceiptsWithState(aggregateID string, occurredAt time.Time, providerReceipt contractsv1.Receipt, providerResult ProviderResult, artifactHashes []contractsv1.SHA256, accepted bool, terminalState string) ([]contractsv1.Receipt, error) {
	previous := providerReceipt.ReceiptHash
	resultReceipt, err := sealReceipt(aggregateID, providerReceipt.AggregateVersion+1, contractsv1.ReceiptReceiptTypeResult, occurredAt, &previous,
		artifactHashes, artifactHashes, map[string]any{"accepted": accepted, "provider_result": providerResult})
	if err != nil {
		return nil, err
	}
	previous = resultReceipt.ReceiptHash
	terminalReceipt, err := sealReceipt(aggregateID, providerReceipt.AggregateVersion+2, contractsv1.ReceiptReceiptTypeTerminal, occurredAt, &previous,
		artifactHashes, nil, map[string]any{"state": terminalState})
	if err != nil {
		return nil, err
	}
	return []contractsv1.Receipt{resultReceipt, terminalReceipt}, nil
}

type memoryLedger struct {
	mu       sync.Mutex
	receipts map[string][]contractsv1.Receipt
}

func NewMemoryLedger() Ledger {
	return &memoryLedger{receipts: make(map[string][]contractsv1.Receipt)}
}

func (l *memoryLedger) Append(receipt contractsv1.Receipt) error {
	return l.AppendBatch([]contractsv1.Receipt{receipt})
}

func (l *memoryLedger) AppendBatch(receipts []contractsv1.Receipt) error {
	return l.appendBatch(receipts, nil)
}

func (l *memoryLedger) AppendAdmission(receipt contractsv1.Receipt, job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition) error {
	return l.appendBatch([]contractsv1.Receipt{receipt}, func(all map[string][]contractsv1.Receipt) error {
		return validateAdmissionDefinitionBindings(all, job, campaign)
	})
}

func (l *memoryLedger) appendBatch(receipts []contractsv1.Receipt, validate func(map[string][]contractsv1.Receipt) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if validate != nil {
		if err := validate(l.receipts); err != nil {
			return err
		}
	}
	next := make(map[string][]contractsv1.Receipt, len(l.receipts))
	for aggregate, current := range l.receipts {
		next[aggregate] = append([]contractsv1.Receipt(nil), current...)
	}
	for _, receipt := range receipts {
		current := next[receipt.AggregateId]
		index := receipt.AggregateVersion - 1
		if index < len(current) {
			if current[index].ReceiptHash != receipt.ReceiptHash {
				return fmt.Errorf("receipt version %d conflicts with canonical history", receipt.AggregateVersion)
			}
			continue
		}
		if err := validateNextReceipt(current, receipt); err != nil {
			return err
		}
		next[receipt.AggregateId] = append(current, receipt)
	}
	l.receipts = next
	return nil
}

func (l *memoryLedger) Replay(aggregateID string) (contractsv1.ReplayBundle, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return replayBundle(aggregateID, append([]contractsv1.Receipt(nil), l.receipts[aggregateID]...))
}

func replayBundle(aggregateID string, receipts []contractsv1.Receipt) (contractsv1.ReplayBundle, error) {
	if len(receipts) == 0 {
		return contractsv1.ReplayBundle{}, ErrReplayEmpty
	}
	bundle := contractsv1.ReplayBundle{
		Kind: contractsv1.ReplayBundleKindReplayBundle, SchemaVersion: 1, AggregateId: aggregateID,
		CutoffReceiptHash: receipts[len(receipts)-1].ReceiptHash, Receipts: receipts,
	}
	hash, err := replayDigest(bundle)
	if err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	bundle.BundleHash = contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("ReplayBundle", bundle); err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	if err := VerifyReplay(bundle); err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	return bundle, nil
}

var ErrReplayEmpty = errors.New("replay aggregate is empty")

func ReplayPrefix(bundle contractsv1.ReplayBundle, receiptCount int) (contractsv1.ReplayBundle, error) {
	if err := VerifyReplay(bundle); err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	if receiptCount < 1 || receiptCount > len(bundle.Receipts) {
		return contractsv1.ReplayBundle{}, errors.New("replay prefix length is invalid")
	}
	return replayBundle(bundle.AggregateId, append([]contractsv1.Receipt(nil), bundle.Receipts[:receiptCount]...))
}

func MaterializeReplay(bundle contractsv1.ReplayBundle, outputs OutputCatalog) (ReplayMaterial, error) {
	invocation, err := MaterializeInvocation(bundle)
	if err != nil {
		return ReplayMaterial{}, err
	}
	material := ReplayMaterial{Invocation: invocation}
	var providerResult ProviderResult
	for _, receipt := range bundle.Receipts {
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeResult {
			if err := decodePayload(receipt.Payload["provider_result"], &providerResult); err != nil {
				return ReplayMaterial{}, fmt.Errorf("materialize artifacts: %w", err)
			}
			material.Artifacts = providerResult.Artifacts
		}
	}
	if err := validateProviderResult(providerResult, material.Invocation); err != nil {
		return ReplayMaterial{}, err
	}
	if providerResult.CompletedAt.After(material.Invocation.Deadline) {
		return ReplayMaterial{}, errors.New("provider result completed after the node deadline")
	}
	if err := validateArtifacts(material.Artifacts, material.Invocation, outputs); err != nil {
		return ReplayMaterial{}, err
	}
	return material, nil
}

func decodePayload(value any, target any) error {
	if value == nil {
		return errors.New("receipt payload is missing")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func previousReceiptHash(value any) contractsv1.SHA256 {
	switch value := value.(type) {
	case contractsv1.SHA256:
		return value
	case string:
		return contractsv1.SHA256(value)
	default:
		return ""
	}
}

func VerifyReplay(bundle contractsv1.ReplayBundle) error {
	if bundle.Kind != contractsv1.ReplayBundleKindReplayBundle || bundle.SchemaVersion != 1 || len(bundle.Receipts) == 0 {
		return errors.New("replay bundle contract is invalid")
	}
	var previous contractsv1.SHA256
	for index, receipt := range bundle.Receipts {
		if receipt.AggregateId != bundle.AggregateId || receipt.AggregateVersion != index+1 {
			return errors.New("replay receipt identity or sequence is invalid")
		}
		if index == 0 {
			if receipt.PreviousReceiptHash != nil {
				return errors.New("first replay receipt has a predecessor")
			}
		} else if previousReceiptHash(receipt.PreviousReceiptHash) != previous {
			return errors.New("replay receipt hash chain is invalid")
		}
		expected := receipt.ReceiptHash
		receipt.ReceiptHash = ""
		hash, err := receiptDigest(receipt)
		if err != nil || contractsv1.SHA256(hash) != expected {
			return errors.New("replay receipt hash is invalid")
		}
		previous = expected
	}
	if bundle.CutoffReceiptHash != previous {
		return errors.New("replay cutoff does not match the receipt chain")
	}
	expected := bundle.BundleHash
	bundle.BundleHash = ""
	hash, err := replayDigest(bundle)
	if err != nil || contractsv1.SHA256(hash) != expected {
		return errors.New("replay bundle hash is invalid")
	}
	return nil
}

func replayDigest(bundle contractsv1.ReplayBundle) (string, error) {
	body, err := json.Marshal(bundle)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var wire any
	if err := decoder.Decode(&wire); err != nil {
		return "", err
	}
	return Digest(wire)
}
