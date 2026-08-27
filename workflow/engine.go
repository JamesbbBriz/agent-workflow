package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

type CapabilityCatalog map[string]contractsv1.CapabilityManifestCapabilitiesElemAuthority

type OutputValidator func(any) error

type OutputCatalog map[contractsv1.WorkflowRef]OutputValidator

type Provider interface {
	Execute(context.Context, Invocation) ([]contractsv1.ActionArtifact, error)
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
	Deadline       time.Time                        `json:"deadline"`
}

type ReplayMaterial struct {
	Invocation Invocation                   `json:"invocation"`
	Artifacts  []contractsv1.ActionArtifact `json:"artifacts"`
}

type RunRequest struct {
	Job        contractsv1.JobDefinition
	Campaign   contractsv1.CampaignDefinition
	Workflow   contractsv1.WorkflowDefinition
	NodeID     string
	OccurredAt time.Time
}

type RunResult struct {
	Compiled  CompiledWorkflow             `json:"compiled"`
	Bundle    contractsv1.ContextBundle    `json:"bundle"`
	Artifacts []contractsv1.ActionArtifact `json:"artifacts"`
	Replay    contractsv1.ReplayBundle     `json:"replay"`
}

type Engine struct {
	registry     *Registry
	capabilities CapabilityCatalog
	outputs      OutputCatalog
	provider     Provider
	ledger       Ledger
}

type Ledger interface {
	Append(contractsv1.Receipt) error
	Replay(string) (contractsv1.ReplayBundle, error)
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
	return &Engine{registry: registry, capabilities: capabilityCopy, outputs: outputCopy, provider: provider, ledger: ledger}
}

func (e *Engine) RunNode(ctx context.Context, request RunRequest) (RunResult, error) {
	if e == nil || e.provider == nil || e.ledger == nil {
		return RunResult{}, errors.New("provider and ledger are required")
	}
	if request.OccurredAt.IsZero() {
		return RunResult{}, errors.New("occurred_at is required")
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
	compiled, compileReceipt, err := compileWorkflow(request.Workflow, e.registry, aggregateID, request.OccurredAt)
	if err != nil {
		return RunResult{}, err
	}
	if err := validateRunBinding(request, compiled); err != nil {
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
	if err := validateOutputCatalog(node.Definition, e.outputs); err != nil {
		return RunResult{}, err
	}
	if replay, err := e.ledger.Replay(aggregateID); err == nil {
		if invocation, ok, err := materializeInvocation(replay); err != nil {
			return RunResult{}, err
		} else if ok {
			if err := validateReplayBinding(invocation, request, compiled, node, jobHash, campaignHash); err != nil {
				return RunResult{}, err
			}
			if hasReceipt(replay, contractsv1.ReceiptReceiptTypeTerminal) {
				material, err := MaterializeReplay(replay, e.outputs)
				if err != nil {
					return RunResult{}, err
				}
				return RunResult{Compiled: compiled, Bundle: invocation.Bundle, Artifacts: material.Artifacts, Replay: replay}, nil
			}
			return e.resumeInvocation(ctx, aggregateID, request.OccurredAt, compiled, invocation, replay)
		}
	} else if !errors.Is(err, ErrReplayEmpty) {
		return RunResult{}, err
	}
	resolved, err := resolveContext(ctx, e.registry, request, compiled, node, compileReceipt)
	if err != nil {
		return RunResult{}, err
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
	deadline := request.OccurredAt
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
		Bundle: resolved.Bundle, Capabilities: manifest, Budget: node.Definition.Budget, Deadline: deadline,
		InputHashes: []contractsv1.SHA256{jobHash, campaignHash, compiled.CompileHash, resolved.Bundle.BundleHash, manifest.ManifestHash},
	}
	preReceipts, err := preExecutionReceipts(aggregateID, request.OccurredAt, compileReceipt, invocation)
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
	return e.resumeInvocation(ctx, aggregateID, request.OccurredAt, compiled, invocation, replay)
}

func (e *Engine) resumeInvocation(ctx context.Context, aggregateID string, occurredAt time.Time, compiled CompiledWorkflow, invocation Invocation, replay contractsv1.ReplayBundle) (RunResult, error) {
	if artifacts, ok, err := materializeArtifacts(replay); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := validateArtifacts(artifacts, invocation, e.outputs); err != nil {
			return RunResult{}, err
		}
		if err := e.finishInvocation(aggregateID, occurredAt, invocation, artifacts, replay); err != nil {
			return RunResult{}, err
		}
		completed, err := e.ledger.Replay(aggregateID)
		if err != nil {
			return RunResult{}, err
		}
		return RunResult{Compiled: compiled, Bundle: invocation.Bundle, Artifacts: artifacts, Replay: completed}, nil
	}
	duration := time.Until(invocation.Deadline)
	if duration <= 0 {
		duration = time.Nanosecond
	}
	providerContext, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	artifacts, err := e.provider.Execute(providerContext, invocation)
	if err != nil {
		return RunResult{}, fmt.Errorf("provider execution: %w", err)
	}
	if err := validateArtifacts(artifacts, invocation, e.outputs); err != nil {
		return RunResult{}, err
	}
	if err := e.finishInvocation(aggregateID, occurredAt, invocation, artifacts, replay); err != nil {
		return RunResult{}, err
	}
	replay, err = e.ledger.Replay(aggregateID)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Compiled: compiled, Bundle: invocation.Bundle, Artifacts: artifacts, Replay: replay}, nil
}

func (e *Engine) finishInvocation(aggregateID string, occurredAt time.Time, invocation Invocation, artifacts []contractsv1.ActionArtifact, replay contractsv1.ReplayBundle) error {
	artifactHashes := make([]contractsv1.SHA256, 0, len(artifacts))
	for _, artifact := range artifacts {
		hash, err := Digest(artifact)
		if err != nil {
			return err
		}
		artifactHashes = append(artifactHashes, contractsv1.SHA256(hash))
	}
	sort.Slice(artifactHashes, func(i, j int) bool { return artifactHashes[i] < artifactHashes[j] })
	invocationReceipt, ok := receiptByType(replay, contractsv1.ReceiptReceiptTypeInvocation)
	if !ok {
		return errors.New("replay has no invocation receipt")
	}
	receipts, err := postExecutionReceipts(aggregateID, occurredAt, invocationReceipt, invocation, artifacts, artifactHashes)
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		if err := e.ledger.Append(receipt); err != nil {
			return err
		}
	}
	return nil
}

func hasReceipt(bundle contractsv1.ReplayBundle, receiptType contractsv1.ReceiptReceiptType) bool {
	_, ok := receiptByType(bundle, receiptType)
	return ok
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

func materializeArtifacts(bundle contractsv1.ReplayBundle) ([]contractsv1.ActionArtifact, bool, error) {
	receipt, ok := receiptByType(bundle, contractsv1.ReceiptReceiptTypeResult)
	if !ok {
		return nil, false, nil
	}
	var artifacts []contractsv1.ActionArtifact
	if err := decodePayload(receipt.Payload["artifacts"], &artifacts); err != nil {
		return nil, false, fmt.Errorf("materialize artifacts: %w", err)
	}
	return artifacts, true, nil
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
	if len(invocation.InputHashes) < 3 || invocation.InputHashes[0] != jobHash || invocation.InputHashes[1] != campaignHash || invocation.InputHashes[2] != compiled.CompileHash {
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

func validateRunBinding(request RunRequest, compiled CompiledWorkflow) error {
	if request.Job.Intent.Kind != contractsv1.IntentCardKindJob || request.Campaign.Intent.Kind != contractsv1.IntentCardKindCampaign {
		return errors.New("job or campaign intent kind is invalid")
	}
	if request.Campaign.JobId != request.Job.Id {
		return errors.New("campaign is not bound to the job")
	}
	if !reflect.DeepEqual(request.Job.Scope, request.Campaign.Scope) {
		return errors.New("campaign scope does not match the job")
	}
	found := false
	for _, workflowRef := range request.Campaign.WorkflowPlan {
		if workflowRef == compiled.WorkflowRef {
			found = true
			break
		}
	}
	if !found {
		return errors.New("workflow is not pinned by the campaign")
	}
	if request.OccurredAt.Before(request.Campaign.EvidenceFrontier.Cutoff) {
		return errors.New("delivery predates the campaign evidence cutoff")
	}
	return nil
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

func postExecutionReceipts(aggregateID string, occurredAt time.Time, previousReceipt contractsv1.Receipt, invocation Invocation, artifacts []contractsv1.ActionArtifact, artifactHashes []contractsv1.SHA256) ([]contractsv1.Receipt, error) {
	version := previousReceipt.AggregateVersion + 1
	previous := previousReceipt.ReceiptHash
	providerReceipt, err := sealReceipt(aggregateID, version, contractsv1.ReceiptReceiptTypeProviderExecution, occurredAt, &previous,
		[]contractsv1.SHA256{contractsv1.SHA256(invocation.IdempotencyKey)}, artifactHashes,
		map[string]any{"node_id": invocation.Node.Id})
	if err != nil {
		return nil, err
	}
	receipts := []contractsv1.Receipt{providerReceipt}
	previous = providerReceipt.ReceiptHash
	resultReceipt, err := sealReceipt(aggregateID, version+1, contractsv1.ReceiptReceiptTypeResult, occurredAt, &previous,
		artifactHashes, artifactHashes, map[string]any{"accepted": true, "artifacts": artifacts})
	if err != nil {
		return nil, err
	}
	receipts = append(receipts, resultReceipt)
	previous = resultReceipt.ReceiptHash
	terminalReceipt, err := sealReceipt(aggregateID, version+2, contractsv1.ReceiptReceiptTypeTerminal, occurredAt, &previous,
		artifactHashes, nil, map[string]any{"state": "node_completed"})
	if err != nil {
		return nil, err
	}
	return append(receipts, terminalReceipt), nil
}

type memoryLedger struct {
	mu       sync.Mutex
	receipts map[string][]contractsv1.Receipt
}

func NewMemoryLedger() Ledger {
	return &memoryLedger{receipts: make(map[string][]contractsv1.Receipt)}
}

func (l *memoryLedger) Append(receipt contractsv1.Receipt) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	current := l.receipts[receipt.AggregateId]
	index := receipt.AggregateVersion - 1
	if index < len(current) {
		if current[index].ReceiptHash != receipt.ReceiptHash {
			return fmt.Errorf("receipt version %d conflicts with canonical history", receipt.AggregateVersion)
		}
		return nil
	}
	if err := validateNextReceipt(current, receipt); err != nil {
		return err
	}
	l.receipts[receipt.AggregateId] = append(current, receipt)
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
	hash, err := Digest(bundle)
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

func MaterializeReplay(bundle contractsv1.ReplayBundle, outputs OutputCatalog) (ReplayMaterial, error) {
	if err := VerifyReplay(bundle); err != nil {
		return ReplayMaterial{}, err
	}
	var material ReplayMaterial
	for _, receipt := range bundle.Receipts {
		switch receipt.ReceiptType {
		case contractsv1.ReceiptReceiptTypeInvocation:
			if err := decodePayload(receipt.Payload["invocation"], &material.Invocation); err != nil {
				return ReplayMaterial{}, fmt.Errorf("materialize invocation: %w", err)
			}
		case contractsv1.ReceiptReceiptTypeResult:
			if err := decodePayload(receipt.Payload["artifacts"], &material.Artifacts); err != nil {
				return ReplayMaterial{}, fmt.Errorf("materialize artifacts: %w", err)
			}
		}
	}
	if material.Invocation.IdempotencyKey == "" {
		return ReplayMaterial{}, errors.New("replay has no materialized invocation")
	}
	if err := VerifyContextBundle(material.Invocation.Bundle, material.Invocation.Context); err != nil {
		return ReplayMaterial{}, err
	}
	for _, pack := range material.Invocation.Context {
		found := false
		for _, receipt := range bundle.Receipts {
			if receipt.ReceiptType != contractsv1.ReceiptReceiptTypePackEdition {
				continue
			}
			var recorded contractsv1.ContextPackEdition
			if err := decodePayload(receipt.Payload["pack"], &recorded); err != nil {
				return ReplayMaterial{}, err
			}
			if reflect.DeepEqual(recorded, pack) {
				found = true
				break
			}
		}
		if !found {
			return ReplayMaterial{}, fmt.Errorf("context pack %q has no exact edition receipt", pack.Id)
		}
	}
	intent, ok := contextPackByType(material.Invocation.Context, "intent-chain")
	if !ok || !reflect.DeepEqual(intent, material.Invocation.IntentChain) {
		return ReplayMaterial{}, errors.New("replay intent chain does not match invocation context")
	}
	if err := VerifyCapabilityManifest(material.Invocation.Capabilities); err != nil {
		return ReplayMaterial{}, err
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
	hash, err := Digest(bundle)
	if err != nil || contractsv1.SHA256(hash) != expected {
		return errors.New("replay bundle hash is invalid")
	}
	return nil
}
