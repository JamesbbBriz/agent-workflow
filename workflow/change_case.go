package workflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

type MergeDecision struct {
	Change    any
	Conflicts []contractsv1.ConflictItem
}

// ChangeMergeAdapter is pure: Core owns every canonical receipt and mutation.
type ChangeMergeAdapter interface {
	Merge(context.Context, contractsv1.ResourceRef, []contractsv1.ChangeProposal) (MergeDecision, error)
}

type ResourceAuthority interface {
	Current(context.Context, string) (contractsv1.ResourceRef, error)
}

type MutationAdapter interface {
	Apply(context.Context, contractsv1.MutationLease, any) (contractsv1.SHA256, error)
	Readback(context.Context, contractsv1.MutationLease) (contractsv1.SHA256, error)
}

type ResolutionSourceAuthority struct {
	WorkflowRef contractsv1.WorkflowRef
	NodeID      contractsv1.Identifier
	Reason      contractsv1.ProposalReplacementReason
}

type ChangeCaseCatalog struct {
	Mergers           map[contractsv1.Identifier]ChangeMergeAdapter
	Resources         map[contractsv1.Identifier]ResourceAuthority
	Mutations         map[contractsv1.Identifier]MutationAdapter
	ApprovalActors    map[contractsv1.Identifier][]string
	ResolutionSources map[contractsv1.Identifier][]ResolutionSourceAuthority
	Clock             func() time.Time
}

type ChangeCaseCore struct {
	ledger            Ledger
	sources           Ledger
	outputs           OutputCatalog
	mergers           map[contractsv1.Identifier]ChangeMergeAdapter
	resources         map[contractsv1.Identifier]ResourceAuthority
	mutations         map[contractsv1.Identifier]MutationAdapter
	approvalActors    map[contractsv1.Identifier]map[string]bool
	resolutionSources map[contractsv1.Identifier]map[ResolutionSourceAuthority]bool
	now               func() time.Time
}

var ErrResourceGenerationAdvanced = errors.New("resource_generation_advanced")

type ChangeProposalSource struct {
	CampaignAggregateID string
	ResultAggregateID   string
	ArtifactID          string
	Replacement         *contractsv1.ProposalReplacement
}

type ResolutionApprovalPreview struct {
	CaseID         string             `json:"case_id"`
	CaseReplayHash contractsv1.SHA256 `json:"case_replay_hash"`
	Actor          string             `json:"actor"`
	ChangeHash     contractsv1.SHA256 `json:"change_hash"`
	ExpiresAt      time.Time          `json:"expires_at"`
	PreviewHash    contractsv1.SHA256 `json:"preview_hash"`
	CommitToken    contractsv1.SHA256 `json:"commit_token"`
}

func NewChangeCaseCore(ledger, sources Ledger, outputs OutputCatalog, catalog ChangeCaseCatalog) *ChangeCaseCore {
	core := &ChangeCaseCore{
		ledger: ledger, sources: sources, outputs: outputs,
		mergers: catalog.Mergers, resources: catalog.Resources, mutations: catalog.Mutations,
		approvalActors:    make(map[contractsv1.Identifier]map[string]bool, len(catalog.ApprovalActors)),
		resolutionSources: make(map[contractsv1.Identifier]map[ResolutionSourceAuthority]bool, len(catalog.ResolutionSources)),
		now:               catalog.Clock,
	}
	for resourceType, sources := range catalog.ResolutionSources {
		core.resolutionSources[resourceType] = map[ResolutionSourceAuthority]bool{}
		for _, source := range sources {
			core.resolutionSources[resourceType][source] = true
		}
	}
	if core.now == nil {
		core.now = time.Now
	}
	for resourceType, actors := range catalog.ApprovalActors {
		core.approvalActors[resourceType] = map[string]bool{}
		for _, actor := range actors {
			if actor = strings.TrimSpace(actor); actor != "" {
				core.approvalActors[resourceType][actor] = true
			}
		}
	}
	return core
}

func (c *ChangeCaseCore) SubmitProposal(ctx context.Context, resource contractsv1.ResourceRef, source ChangeProposalSource, occurredAt time.Time) (contractsv1.ChangeCaseState, error) {
	if err := contract.ValidateDefinition("ResourceRef", resource); err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	caseID, err := changeCaseID(resource)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	proposal, err := c.materializeProposal(resource, caseID, source)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	state, replay, err := c.load(caseID)
	if err != nil && !errors.Is(err, ErrReplayEmpty) {
		return contractsv1.ChangeCaseState{}, err
	}
	if replay != nil {
		if !sameDigest(state.Resource, resource) {
			return contractsv1.ChangeCaseState{}, errors.New("change case resource does not match canonical history")
		}
		for _, existing := range state.Proposals {
			if existing.ProposalHash == proposal.ProposalHash {
				return c.Reconcile(ctx, caseID, occurredAt)
			}
		}
		if proposal.Replacement != nil && !proposalIDPresent(state.Proposals, proposal.Replacement.ProposalId) {
			return contractsv1.ChangeCaseState{}, errors.New("replacement does not reference a canonical proposal")
		}
		if state.Status == contractsv1.ChangeCaseStateStatusLeased || state.Status == contractsv1.ChangeCaseStateStatusApplied || state.Status == contractsv1.ChangeCaseStateStatusCompleted {
			return contractsv1.ChangeCaseState{}, errors.New("change case no longer accepts proposals")
		}
	} else {
		if proposal.Replacement != nil {
			return contractsv1.ChangeCaseState{}, errors.New("first proposal cannot be a replacement")
		}
		state = newChangeCaseState(caseID, resource, occurredAt)
	}
	state.Proposals = append(state.Proposals, proposal)
	state.Status = contractsv1.ChangeCaseStateStatusProposed
	state.UpdatedAt = occurredAt.UTC()
	clearChangeDecision(&state)
	if _, err := c.appendState(replay, contractsv1.ReceiptReceiptTypeChangeProposed, state, occurredAt, proposal.EvidenceHashes, []contractsv1.SHA256{proposal.ProposalHash}, ""); err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	return c.Reconcile(ctx, caseID, occurredAt)
}

func (c *ChangeCaseCore) Reconcile(ctx context.Context, caseID string, occurredAt time.Time) (contractsv1.ChangeCaseState, error) {
	state, replay, err := c.load(caseID)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	if state.Status != contractsv1.ChangeCaseStateStatusProposed {
		return state, nil
	}
	if err := c.checkCurrent(ctx, state.Resource); err != nil {
		if !errors.Is(err, ErrResourceGenerationAdvanced) {
			return contractsv1.ChangeCaseState{}, err
		}
		code := contractsv1.ChangeCaseStateBlockerCodeResourceGenerationAdvanced
		state.Status, state.BlockerCode, state.UpdatedAt = contractsv1.ChangeCaseStateStatusBlocked, &code, occurredAt.UTC()
		_, appendErr := c.appendState(replay, contractsv1.ReceiptReceiptTypeResourceGenerationAdvanced, state, occurredAt, nil, nil, "")
		if appendErr != nil {
			return contractsv1.ChangeCaseState{}, appendErr
		}
		return state, nil
	}
	for _, proposal := range state.Proposals {
		if err := c.verifyProposal(proposal); err != nil {
			return contractsv1.ChangeCaseState{}, fmt.Errorf("proposal %s is not canonical: %w", proposal.Id, err)
		}
	}
	merger := c.mergers[state.Resource.ResourceType]
	if merger == nil {
		return contractsv1.ChangeCaseState{}, errors.New("resource type has no merge adapter")
	}
	proposals := append([]contractsv1.ChangeProposal(nil), state.Proposals...)
	sort.Slice(proposals, func(i, j int) bool { return proposals[i].ProposalHash < proposals[j].ProposalHash })
	firstInput, err := cloneProposals(proposals)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	decision, err := merger.Merge(ctx, state.Resource, firstInput)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	secondInput, err := cloneProposals(proposals)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	confirmation, err := merger.Merge(ctx, state.Resource, secondInput)
	if err != nil || !sameDigest(decision, confirmation) {
		return contractsv1.ChangeCaseState{}, errors.New("merge adapter is not deterministic")
	}
	state.UpdatedAt = occurredAt.UTC()
	if len(decision.Conflicts) > 0 {
		conflicts, err := buildConflictSet(state, decision.Conflicts)
		if err != nil {
			return contractsv1.ChangeCaseState{}, err
		}
		state.Status, state.Conflicts = contractsv1.ChangeCaseStateStatusConflicted, &conflicts
		_, err = c.appendState(replay, contractsv1.ReceiptReceiptTypeConflictDetected, state, occurredAt, proposalHashes(proposals), []contractsv1.SHA256{conflicts.ConflictHash}, "")
		return state, err
	}
	if decision.Change == nil {
		return contractsv1.ChangeCaseState{}, errors.New("merge adapter returned neither change nor conflicts")
	}
	if err := validateBoundedJSON("merged change", decision.Change); err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	changeHash, err := Digest(decision.Change)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	state.Status, state.MergedChange = contractsv1.ChangeCaseStateStatusReady, decision.Change
	mergedHash := contractsv1.SHA256(changeHash)
	state.MergedChangeHash = &mergedHash
	_, err = c.appendState(replay, contractsv1.ReceiptReceiptTypeChangeMerged, state, occurredAt, proposalHashes(proposals), []contractsv1.SHA256{mergedHash}, "")
	return state, err
}

func (c *ChangeCaseCore) ProposeResolution(caseID string, source ChangeProposalSource, occurredAt time.Time) (contractsv1.ChangeCaseState, error) {
	state, replay, err := c.load(caseID)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	if source.Replacement == nil || (source.Replacement.Reason != contractsv1.ProposalReplacementReasonResolver && source.Replacement.Reason != contractsv1.ProposalReplacementReasonHumanImplementation) || !conflictContainsProposal(state.Conflicts, source.Replacement.ProposalId) {
		return contractsv1.ChangeCaseState{}, errors.New("resolution source has no typed canonical conflict lineage")
	}
	proposal, err := c.materializeProposal(state.Resource, caseID, source)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	if state.Resolution != nil && state.Resolution.ResolutionProposalId == proposal.Id {
		return state, nil
	}
	if !c.resolutionSources[state.Resource.ResourceType][ResolutionSourceAuthority{WorkflowRef: proposal.WorkflowRef, NodeID: proposal.NodeId, Reason: source.Replacement.Reason}] || proposalResultPresent(state.Proposals, proposal.SourceResultAggregateId) {
		return contractsv1.ChangeCaseState{}, errors.New("resolution proposal is not from a registered distinct resolver or human implementation Node")
	}
	if state.Status != contractsv1.ChangeCaseStateStatusConflicted || state.Conflicts == nil {
		return contractsv1.ChangeCaseState{}, errors.New("change case has no unresolved conflict")
	}
	state.Proposals = append(state.Proposals, proposal)
	resolution := contractsv1.ResolutionArtifact{Kind: "resolution_artifact", SchemaVersion: 2, CaseId: caseID, ConflictHash: state.Conflicts.ConflictHash, ResolvedChange: proposal.Change, ResolvedChangeHash: proposal.ChangeHash, SourceProposalIds: proposalIDs(state.Proposals), ResolutionProposalId: proposal.Id}
	hash, err := Digest(resolution)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	resolution.Id, resolution.ResolutionHash = contractsv1.Identifier(shortID("resolution-", hash)), contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("ResolutionArtifact", resolution); err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	state.Status, state.Resolution, state.MergedChange = contractsv1.ChangeCaseStateStatusAwaitingResolutionApproval, &resolution, proposal.Change
	state.MergedChangeHash, state.UpdatedAt = &resolution.ResolvedChangeHash, occurredAt.UTC()
	_, err = c.appendState(replay, contractsv1.ReceiptReceiptTypeResolutionProposed, state, occurredAt, []contractsv1.SHA256{state.Conflicts.ConflictHash, proposal.ProposalHash}, []contractsv1.SHA256{resolution.ResolutionHash}, "")
	return state, err
}

func (c *ChangeCaseCore) PreviewApproval(caseID, actor string, expiresAt time.Time) (ResolutionApprovalPreview, error) {
	state, replay, err := c.load(caseID)
	if err != nil {
		return ResolutionApprovalPreview{}, err
	}
	if state.MergedChangeHash == nil || (state.Status != contractsv1.ChangeCaseStateStatusReady && state.Status != contractsv1.ChangeCaseStateStatusAwaitingResolutionApproval) {
		return ResolutionApprovalPreview{}, errors.New("change case is not ready for approval")
	}
	actor = strings.TrimSpace(actor)
	if !c.approvalActors[state.Resource.ResourceType][actor] {
		return ResolutionApprovalPreview{}, errors.New("actor is not authorized for this resource type")
	}
	if !expiresAt.After(c.now().UTC()) {
		return ResolutionApprovalPreview{}, errors.New("approval preview expiry must be in the future")
	}
	preview := ResolutionApprovalPreview{CaseID: caseID, CaseReplayHash: replay.BundleHash, Actor: actor, ChangeHash: *state.MergedChangeHash, ExpiresAt: expiresAt.UTC()}
	hash, err := Digest(preview)
	if err != nil {
		return ResolutionApprovalPreview{}, err
	}
	preview.PreviewHash = contractsv1.SHA256(hash)
	token, err := Digest(struct {
		Purpose string
		Preview contractsv1.SHA256
	}{"approve-change-case", preview.PreviewHash})
	if err != nil {
		return ResolutionApprovalPreview{}, err
	}
	preview.CommitToken = contractsv1.SHA256(token)
	return preview, nil
}

func (c *ChangeCaseCore) ConfirmApproval(preview ResolutionApprovalPreview, occurredAt time.Time) (contractsv1.ChangeCaseState, error) {
	state, replay, err := c.load(preview.CaseID)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	if state.ResolutionApprovalHash != nil && exactApprovalReceipt(*replay, preview) {
		return state, nil
	}
	if c.now().UTC().After(preview.ExpiresAt) {
		return contractsv1.ChangeCaseState{}, errors.New("approval preview has expired")
	}
	expected, err := c.PreviewApproval(preview.CaseID, preview.Actor, preview.ExpiresAt)
	if err != nil || !reflect.DeepEqual(expected, preview) {
		return contractsv1.ChangeCaseState{}, errors.New("approval preview is stale or altered")
	}
	state.UpdatedAt = occurredAt.UTC()
	if _, err = c.appendState(replay, contractsv1.ReceiptReceiptTypeResolutionApproved, state, occurredAt, []contractsv1.SHA256{preview.ChangeHash, preview.PreviewHash}, []contractsv1.SHA256{preview.CommitToken}, preview.Actor); err != nil {
		if latest, latestReplay, loadErr := c.load(preview.CaseID); loadErr == nil && exactApprovalReceipt(*latestReplay, preview) {
			return latest, nil
		}
		return contractsv1.ChangeCaseState{}, err
	}
	state, _, err = c.load(preview.CaseID)
	return state, err
}

func exactApprovalReceipt(replay contractsv1.ReplayBundle, preview ResolutionApprovalPreview) bool {
	for _, receipt := range replay.Receipts {
		if receipt.ReceiptType != contractsv1.ReceiptReceiptTypeResolutionApproved || receipt.Actor == nil || *receipt.Actor != preview.Actor {
			continue
		}
		if hashesContain(receipt.InputHashes, preview.ChangeHash) && hashesContain(receipt.InputHashes, preview.PreviewHash) && hashesContain(receipt.OutputHashes, preview.CommitToken) {
			return true
		}
	}
	return false
}

func hashesContain(values []contractsv1.SHA256, target contractsv1.SHA256) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (c *ChangeCaseCore) AcquireLease(ctx context.Context, caseID string, ttl time.Duration, occurredAt time.Time) (contractsv1.ChangeCaseState, error) {
	now := c.now().UTC()
	state, replay, err := c.load(caseID)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	if state.ResolutionApprovalHash == nil || state.MergedChangeHash == nil {
		return contractsv1.ChangeCaseState{}, errors.New("exact change approval is required")
	}
	if state.Status != contractsv1.ChangeCaseStateStatusReady && state.Status != contractsv1.ChangeCaseStateStatusAwaitingResolutionApproval {
		return contractsv1.ChangeCaseState{}, errors.New("change case cannot acquire another mutation lease")
	}
	if !c.approvalAllowed(state, *replay) {
		return contractsv1.ChangeCaseState{}, errors.New("change approval actor is not authorized")
	}
	if state.Lease != nil && now.Before(state.Lease.ExpiresAt) {
		return contractsv1.ChangeCaseState{}, errors.New("an active mutation lease already exists")
	}
	if ttl <= 0 || ttl > time.Hour {
		return contractsv1.ChangeCaseState{}, errors.New("mutation lease duration is invalid")
	}
	if err := c.checkCurrent(ctx, state.Resource); err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	for _, proposal := range state.Proposals {
		if err := c.verifyProposal(proposal); err != nil {
			return contractsv1.ChangeCaseState{}, fmt.Errorf("proposal %s is not canonical: %w", proposal.Id, err)
		}
	}
	lease := contractsv1.MutationLease{Kind: "mutation_lease", SchemaVersion: 2, CaseId: caseID, Resource: state.Resource, ChangeHash: *state.MergedChangeHash, ApprovalReceiptHash: *state.ResolutionApprovalHash, AcquiredAt: now, ExpiresAt: now.Add(ttl)}
	hash, err := Digest(lease)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	lease.Id, lease.LeaseHash = contractsv1.Identifier(shortID("lease-", hash)), contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("MutationLease", lease); err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	state.Status, state.Lease, state.UpdatedAt = contractsv1.ChangeCaseStateStatusLeased, &lease, occurredAt.UTC()
	_, err = c.appendState(replay, contractsv1.ReceiptReceiptTypeMutationLeaseAcquired, state, occurredAt, []contractsv1.SHA256{lease.ApprovalReceiptHash, lease.ChangeHash}, []contractsv1.SHA256{lease.LeaseHash}, "")
	return state, err
}

func (c *ChangeCaseCore) Apply(ctx context.Context, caseID string, occurredAt time.Time) (contractsv1.ChangeCaseState, error) {
	now := c.now().UTC()
	state, replay, err := c.load(caseID)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	if state.Status == contractsv1.ChangeCaseStateStatusCompleted {
		return state, nil
	}
	if state.Lease == nil || state.MergedChangeHash == nil || (state.Status != contractsv1.ChangeCaseStateStatusLeased && state.Status != contractsv1.ChangeCaseStateStatusApplied) {
		return contractsv1.ChangeCaseState{}, errors.New("an active exact mutation lease is required")
	}
	adapter := c.mutations[state.Resource.ResourceType]
	if adapter == nil {
		return contractsv1.ChangeCaseState{}, errors.New("resource type has no mutation adapter")
	}
	if state.Status == contractsv1.ChangeCaseStateStatusLeased {
		if !now.Before(state.Lease.ExpiresAt) {
			return contractsv1.ChangeCaseState{}, errors.New("an active exact mutation lease is required")
		}
		if err := c.checkCurrent(ctx, state.Resource); err != nil {
			return contractsv1.ChangeCaseState{}, err
		}
		for _, proposal := range state.Proposals {
			if err := c.verifyProposal(proposal); err != nil {
				return contractsv1.ChangeCaseState{}, fmt.Errorf("proposal %s is not canonical: %w", proposal.Id, err)
			}
		}
		observed, err := adapter.Apply(ctx, *state.Lease, state.MergedChange)
		if err != nil {
			return contractsv1.ChangeCaseState{}, err
		}
		apply, err := mutationEvidence(contractsv1.MutationEvidenceKindMutationApplyEvidence, state, observed)
		if err != nil {
			return contractsv1.ChangeCaseState{}, err
		}
		state.Status, state.ApplyEvidence, state.UpdatedAt = contractsv1.ChangeCaseStateStatusApplied, &apply, occurredAt.UTC()
		if replay, err = c.appendState(replay, contractsv1.ReceiptReceiptTypeMutationApplied, state, occurredAt, []contractsv1.SHA256{state.Lease.LeaseHash, *state.MergedChangeHash}, []contractsv1.SHA256{apply.EvidenceHash}, ""); err != nil {
			return contractsv1.ChangeCaseState{}, err
		}
	}
	if state.ApplyEvidence == nil {
		return contractsv1.ChangeCaseState{}, errors.New("applied change has no canonical evidence")
	}
	readback, err := adapter.Readback(ctx, *state.Lease)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	if readback != state.ApplyEvidence.ObservedHash {
		return contractsv1.ChangeCaseState{}, errors.New("mutation readback does not match applied resource")
	}
	evidence, err := mutationEvidence(contractsv1.MutationEvidenceKindMutationReadbackEvidence, state, readback)
	if err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	state.Status, state.ReadbackEvidence, state.UpdatedAt = contractsv1.ChangeCaseStateStatusCompleted, &evidence, occurredAt.UTC()
	_, err = c.appendState(replay, contractsv1.ReceiptReceiptTypeMutationReadback, state, occurredAt, []contractsv1.SHA256{state.ApplyEvidence.EvidenceHash}, []contractsv1.SHA256{evidence.EvidenceHash}, "")
	return state, err
}

func (c *ChangeCaseCore) Replay(caseID string) (contractsv1.ReplayBundle, error) {
	return c.ledger.Replay(caseID)
}

func MaterializeChangeCase(replay contractsv1.ReplayBundle) (contractsv1.ChangeCaseState, error) {
	return reduceChangeCase(replay)
}

func (c *ChangeCaseCore) load(caseID string) (contractsv1.ChangeCaseState, *contractsv1.ReplayBundle, error) {
	replay, err := c.ledger.Replay(caseID)
	if err != nil {
		return contractsv1.ChangeCaseState{}, nil, err
	}
	state, err := reduceChangeCase(replay)
	return state, &replay, err
}

func (c *ChangeCaseCore) appendState(replay *contractsv1.ReplayBundle, receiptType contractsv1.ReceiptReceiptType, state contractsv1.ChangeCaseState, occurredAt time.Time, inputs, outputs []contractsv1.SHA256, actor string) (*contractsv1.ReplayBundle, error) {
	if err := contract.ValidateDefinition("ChangeCaseState", state); err != nil {
		return replay, err
	}
	version := 1
	var previous *contractsv1.SHA256
	if replay != nil {
		version = len(replay.Receipts) + 1
		value := replay.CutoffReceiptHash
		previous = &value
	}
	receipt, err := sealReceiptVersion(4, state.Id, version, receiptType, occurredAt, previous, inputs, outputs, map[string]any{"state": state})
	if err != nil {
		return replay, err
	}
	if actor != "" {
		receipt.Actor = &actor
		receipt.ReceiptHash = ""
		hash, hashErr := receiptDigest(receipt)
		if hashErr != nil {
			return replay, hashErr
		}
		receipt.ReceiptHash = contractsv1.SHA256(hash)
		if err := contract.ValidateDefinition("Receipt", receipt); err != nil {
			return replay, err
		}
	}
	receipts := []contractsv1.Receipt{receipt}
	if replay != nil {
		receipts = append(append([]contractsv1.Receipt(nil), replay.Receipts...), receipt)
	}
	if _, err := replayBundle(state.Id, receipts); err != nil {
		return replay, fmt.Errorf("change case event exceeds the replay contract: %w", err)
	}
	if err := c.ledger.Append(receipt); err != nil {
		return replay, err
	}
	next, err := c.ledger.Replay(state.Id)
	return &next, err
}

func (c *ChangeCaseCore) checkCurrent(ctx context.Context, resource contractsv1.ResourceRef) error {
	authority := c.resources[resource.ResourceType]
	if authority == nil {
		return errors.New("resource type has no canonical authority")
	}
	current, err := authority.Current(ctx, resource.ResourceId)
	if err != nil {
		return err
	}
	if err := contract.ValidateDefinition("ResourceRef", current); err != nil {
		return fmt.Errorf("canonical resource authority returned invalid identity: %w", err)
	}
	if !sameDigest(current, resource) {
		return ErrResourceGenerationAdvanced
	}
	return nil
}

func (c *ChangeCaseCore) approvalAllowed(state contractsv1.ChangeCaseState, replay contractsv1.ReplayBundle) bool {
	if state.ResolutionApprovalHash == nil {
		return false
	}
	for index := len(replay.Receipts) - 1; index >= 0; index-- {
		receipt := replay.Receipts[index]
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeResolutionApproved && receipt.ReceiptHash == *state.ResolutionApprovalHash {
			return receipt.Actor != nil && c.approvalActors[state.Resource.ResourceType][*receipt.Actor]
		}
	}
	return false
}

func (c *ChangeCaseCore) materializeProposal(resource contractsv1.ResourceRef, caseID string, source ChangeProposalSource) (contractsv1.ChangeProposal, error) {
	campaign, err := c.sources.Replay(source.CampaignAggregateID)
	if err != nil {
		return contractsv1.ChangeProposal{}, errors.New("proposal Campaign Replay is not canonical")
	}
	result, err := c.sources.Replay(source.ResultAggregateID)
	if err != nil || !nodeCompletedReplay(result) {
		return contractsv1.ChangeProposal{}, errors.New("proposal result Replay is not complete")
	}
	material, err := MaterializeReplay(result, c.outputs)
	if err != nil {
		return contractsv1.ChangeProposal{}, err
	}
	prefix, node, err := proposalCampaignPrefix(campaign, result.BundleHash)
	if err != nil {
		return contractsv1.ChangeProposal{}, err
	}
	if material.Invocation.JobID != campaignStateIdentity(campaign).JobId || material.Invocation.CampaignID != campaignStateIdentity(campaign).CampaignId || material.Invocation.WorkflowRef != node.WorkflowRef || material.Invocation.Node.Id != node.NodeId {
		return contractsv1.ChangeProposal{}, errors.New("proposal result identity does not match Campaign completion")
	}
	var artifact *contractsv1.ActionArtifact
	for index := range material.Artifacts {
		if material.Artifacts[index].Id == source.ArtifactID {
			artifact = &material.Artifacts[index]
			break
		}
	}
	if artifact == nil || artifact.JobId != material.Invocation.JobID || artifact.CampaignId != material.Invocation.CampaignID || artifact.WorkflowRef != material.Invocation.WorkflowRef || artifact.NodeId != material.Invocation.Node.Id {
		return contractsv1.ChangeProposal{}, errors.New("proposal artifact is not bound to the completed Node")
	}
	evidence := append([]contractsv1.SHA256{prefix.BundleHash, result.BundleHash, artifact.ContentSha256}, artifact.InputHashes...)
	evidence = uniqueHashes(evidence)
	proposal := contractsv1.ChangeProposal{Kind: "change_proposal", SchemaVersion: 2, CaseId: caseID, Resource: resource, JobId: artifact.JobId, CampaignId: artifact.CampaignId, WorkflowRef: artifact.WorkflowRef, NodeId: artifact.NodeId, SourceCampaignAggregateId: source.CampaignAggregateID, SourceCampaignReplayHash: prefix.BundleHash, SourceResultAggregateId: source.ResultAggregateID, SourceResultReplayHash: result.BundleHash, ArtifactId: artifact.Id, Change: artifact.Content, ChangeHash: artifact.ContentSha256, CapabilityHash: material.Invocation.Capabilities.ManifestHash, EvidenceHashes: evidence, Replacement: source.Replacement}
	hash, err := Digest(proposal)
	if err != nil {
		return contractsv1.ChangeProposal{}, err
	}
	proposal.Id, proposal.ProposalHash = contractsv1.Identifier(shortID("proposal-", hash)), contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("ChangeProposal", proposal); err != nil {
		return contractsv1.ChangeProposal{}, err
	}
	return proposal, nil
}

func (c *ChangeCaseCore) verifyProposal(proposal contractsv1.ChangeProposal) error {
	materialized, err := c.materializeProposal(proposal.Resource, proposal.CaseId, ChangeProposalSource{CampaignAggregateID: proposal.SourceCampaignAggregateId, ResultAggregateID: proposal.SourceResultAggregateId, ArtifactID: proposal.ArtifactId, Replacement: proposal.Replacement})
	if err != nil {
		return err
	}
	if !sameDigest(materialized, proposal) {
		return errors.New("proposal differs from its canonical source")
	}
	return nil
}

func reduceChangeCase(replay contractsv1.ReplayBundle) (contractsv1.ChangeCaseState, error) {
	if err := VerifyReplay(replay); err != nil {
		return contractsv1.ChangeCaseState{}, err
	}
	var state contractsv1.ChangeCaseState
	for _, receipt := range replay.Receipts {
		if receipt.SchemaVersion != 4 || !changeReceiptType(receipt.ReceiptType) {
			return contractsv1.ChangeCaseState{}, errors.New("change case contains an unsupported receipt")
		}
		var next contractsv1.ChangeCaseState
		if err := decodePayload(receipt.Payload["state"], &next); err != nil || contract.ValidateDefinition("ChangeCaseState", next) != nil {
			return contractsv1.ChangeCaseState{}, errors.New("change case state is invalid")
		}
		if next.Id != replay.AggregateId || (state.Id != "" && (!sameDigest(next.Resource, state.Resource) || len(next.Proposals) < len(state.Proposals))) {
			return contractsv1.ChangeCaseState{}, errors.New("change case identity or proposal lineage changed")
		}
		for index := range state.Proposals {
			if !sameDigest(state.Proposals[index], next.Proposals[index]) {
				return contractsv1.ChangeCaseState{}, errors.New("canonical proposal history was rewritten")
			}
		}
		if err := validateChangeCaseState(next); err != nil {
			return contractsv1.ChangeCaseState{}, err
		}
		if err := validateChangeCaseTransition(state, next, receipt); err != nil {
			return contractsv1.ChangeCaseState{}, err
		}
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeResolutionApproved {
			next.ResolutionApprovalHash = &receipt.ReceiptHash
		}
		state = next
	}
	return state, nil
}

func validateChangeCaseState(state contractsv1.ChangeCaseState) error {
	expectedCaseID, err := changeCaseID(state.Resource)
	if err != nil || expectedCaseID != state.Id {
		return errors.New("change case ID does not match its exact Resource baseline")
	}
	seen := map[contractsv1.Identifier]bool{}
	replaced := map[contractsv1.Identifier]bool{}
	for _, proposal := range state.Proposals {
		if proposal.CaseId != state.Id || !sameDigest(proposal.Resource, state.Resource) {
			return errors.New("proposal is outside its Change Case")
		}
		changeHash, err := Digest(proposal.Change)
		if err != nil || contractsv1.SHA256(changeHash) != proposal.ChangeHash {
			return errors.New("proposal change hash is invalid")
		}
		unsigned, id, expectedHash := proposal, proposal.Id, proposal.ProposalHash
		unsigned.Id, unsigned.ProposalHash = "", ""
		hash, err := Digest(unsigned)
		if err != nil || contractsv1.SHA256(hash) != expectedHash || contractsv1.Identifier(shortID("proposal-", hash)) != id {
			return errors.New("proposal identity or hash is invalid")
		}
		if proposal.Replacement != nil {
			if !seen[proposal.Replacement.ProposalId] || replaced[proposal.Replacement.ProposalId] {
				return errors.New("proposal replacement lineage is invalid")
			}
			replaced[proposal.Replacement.ProposalId] = true
		}
		seen[proposal.Id] = true
	}
	if state.MergedChangeHash != nil {
		hash, err := Digest(state.MergedChange)
		if err != nil || contractsv1.SHA256(hash) != *state.MergedChangeHash {
			return errors.New("merged change hash is invalid")
		}
	}
	if state.Conflicts != nil {
		expected, err := buildConflictSet(state, state.Conflicts.Items)
		if err != nil || !sameDigest(expected, *state.Conflicts) {
			return errors.New("Conflict Set hash or proposal binding is invalid")
		}
	}
	if state.Resolution != nil {
		unsigned, id, expectedHash := *state.Resolution, state.Resolution.Id, state.Resolution.ResolutionHash
		unsigned.Id, unsigned.ResolutionHash = "", ""
		hash, err := Digest(unsigned)
		if err != nil || contractsv1.SHA256(hash) != expectedHash || contractsv1.Identifier(shortID("resolution-", hash)) != id || state.Conflicts == nil || state.Resolution.ConflictHash != state.Conflicts.ConflictHash || !reflect.DeepEqual(state.Resolution.SourceProposalIds, proposalIDs(state.Proposals)) || !resolutionProposalValid(state) {
			return errors.New("Resolution Artifact identity or conflict binding is invalid")
		}
	}
	if state.Lease != nil {
		unsigned, id, expectedHash := *state.Lease, state.Lease.Id, state.Lease.LeaseHash
		unsigned.Id, unsigned.LeaseHash = "", ""
		hash, err := Digest(unsigned)
		if err != nil || contractsv1.SHA256(hash) != expectedHash || contractsv1.Identifier(shortID("lease-", hash)) != id || !state.Lease.ExpiresAt.After(state.Lease.AcquiredAt) || !sameDigest(state.Lease.Resource, state.Resource) || state.MergedChangeHash == nil || state.Lease.ChangeHash != *state.MergedChangeHash {
			return errors.New("Mutation Lease identity or Change Case binding is invalid")
		}
	}
	for _, evidence := range []*contractsv1.MutationEvidence{state.ApplyEvidence, state.ReadbackEvidence} {
		if evidence == nil {
			continue
		}
		unsigned, expectedHash := *evidence, evidence.EvidenceHash
		unsigned.EvidenceHash = ""
		hash, err := Digest(unsigned)
		if err != nil || contractsv1.SHA256(hash) != expectedHash || state.Lease == nil || evidence.LeaseHash != state.Lease.LeaseHash || !sameDigest(evidence.Resource, state.Resource) || state.MergedChangeHash == nil || evidence.ChangeHash != *state.MergedChangeHash {
			return errors.New("mutation evidence is not bound to the exact lease and change")
		}
	}
	return nil
}

func validateChangeCaseTransition(previous, next contractsv1.ChangeCaseState, receipt contractsv1.Receipt) error {
	sameProposalCount := len(previous.Proposals) == len(next.Proposals)
	valid := false
	switch receipt.ReceiptType {
	case contractsv1.ReceiptReceiptTypeChangeProposed:
		valid = len(next.Proposals) == len(previous.Proposals)+1 && next.Status == contractsv1.ChangeCaseStateStatusProposed && next.MergedChangeHash == nil && next.Conflicts == nil && next.Lease == nil
	case contractsv1.ReceiptReceiptTypeChangeMerged:
		valid = sameProposalCount && previous.Status == contractsv1.ChangeCaseStateStatusProposed && next.Status == contractsv1.ChangeCaseStateStatusReady && next.MergedChangeHash != nil
	case contractsv1.ReceiptReceiptTypeConflictDetected:
		valid = sameProposalCount && previous.Status == contractsv1.ChangeCaseStateStatusProposed && next.Status == contractsv1.ChangeCaseStateStatusConflicted && next.Conflicts != nil
	case contractsv1.ReceiptReceiptTypeResolutionProposed:
		valid = len(next.Proposals) == len(previous.Proposals)+1 && previous.Status == contractsv1.ChangeCaseStateStatusConflicted && next.Status == contractsv1.ChangeCaseStateStatusAwaitingResolutionApproval && next.Resolution != nil && next.MergedChangeHash != nil
	case contractsv1.ReceiptReceiptTypeResolutionApproved:
		valid = sameProposalCount && (previous.Status == contractsv1.ChangeCaseStateStatusReady || previous.Status == contractsv1.ChangeCaseStateStatusAwaitingResolutionApproval) && next.Status == previous.Status && receipt.Actor != nil
	case contractsv1.ReceiptReceiptTypeMutationLeaseAcquired:
		valid = sameProposalCount && previous.ResolutionApprovalHash != nil && (previous.Status == contractsv1.ChangeCaseStateStatusReady || previous.Status == contractsv1.ChangeCaseStateStatusAwaitingResolutionApproval) && next.Status == contractsv1.ChangeCaseStateStatusLeased && next.Lease != nil && next.Lease.ApprovalReceiptHash == *previous.ResolutionApprovalHash
	case contractsv1.ReceiptReceiptTypeMutationApplied:
		valid = sameProposalCount && previous.Status == contractsv1.ChangeCaseStateStatusLeased && next.Status == contractsv1.ChangeCaseStateStatusApplied && next.ApplyEvidence != nil
	case contractsv1.ReceiptReceiptTypeMutationReadback:
		valid = sameProposalCount && previous.Status == contractsv1.ChangeCaseStateStatusApplied && next.Status == contractsv1.ChangeCaseStateStatusCompleted && next.ReadbackEvidence != nil && next.ApplyEvidence != nil && next.ReadbackEvidence.ObservedHash == next.ApplyEvidence.ObservedHash
	case contractsv1.ReceiptReceiptTypeResourceGenerationAdvanced:
		valid = sameProposalCount && next.Status == contractsv1.ChangeCaseStateStatusBlocked && next.BlockerCode != nil && *next.BlockerCode == contractsv1.ChangeCaseStateBlockerCodeResourceGenerationAdvanced
	}
	if !valid {
		return fmt.Errorf("invalid %s Change Case transition", receipt.ReceiptType)
	}
	return nil
}

func resolutionProposalValid(state contractsv1.ChangeCaseState) bool {
	for _, proposal := range state.Proposals {
		if proposal.Id != state.Resolution.ResolutionProposalId {
			continue
		}
		return proposal.Replacement != nil && (proposal.Replacement.Reason == contractsv1.ProposalReplacementReasonResolver || proposal.Replacement.Reason == contractsv1.ProposalReplacementReasonHumanImplementation) && proposal.ChangeHash == state.Resolution.ResolvedChangeHash && reflect.DeepEqual(proposal.Change, state.Resolution.ResolvedChange)
	}
	return false
}

func proposalCampaignPrefix(campaign contractsv1.ReplayBundle, resultHash contractsv1.SHA256) (contractsv1.ReplayBundle, contractsv1.NodeCompletedEventPayload, error) {
	if err := VerifyReplay(campaign); err != nil {
		return contractsv1.ReplayBundle{}, contractsv1.NodeCompletedEventPayload{}, err
	}
	for index, receipt := range campaign.Receipts {
		if receipt.ReceiptType != contractsv1.ReceiptReceiptTypeNodeCompleted {
			continue
		}
		var payload contractsv1.NodeCompletedEventPayload
		if decodePayload(receipt.Payload, &payload) == nil && payload.ResultReplayHash == resultHash {
			prefix, err := ReplayPrefix(campaign, index+1)
			return prefix, payload, err
		}
	}
	return contractsv1.ReplayBundle{}, contractsv1.NodeCompletedEventPayload{}, errors.New("Campaign Replay does not bind the completed result")
}

func campaignStateIdentity(replay contractsv1.ReplayBundle) contractsv1.CampaignExecutionState {
	var state contractsv1.CampaignExecutionState
	if len(replay.Receipts) > 0 {
		_ = decodePayload(replay.Receipts[0].Payload["state"], &state)
	}
	return state
}

func buildConflictSet(state contractsv1.ChangeCaseState, items []contractsv1.ConflictItem) (contractsv1.ConflictSet, error) {
	known := map[contractsv1.Identifier]bool{}
	for _, proposal := range state.Proposals {
		known[proposal.Id] = true
	}
	for _, item := range items {
		if len(item.ProposalIds) < 2 || strings.TrimSpace(item.Path) == "" || strings.TrimSpace(item.Reason) == "" {
			return contractsv1.ConflictSet{}, errors.New("merge adapter returned an invalid conflict")
		}
		for _, id := range item.ProposalIds {
			if !known[id] {
				return contractsv1.ConflictSet{}, errors.New("conflict references an unknown proposal")
			}
		}
	}
	conflicts := contractsv1.ConflictSet{Kind: "conflict_set", SchemaVersion: 2, CaseId: state.Id, Resource: state.Resource, Items: items}
	hash, err := Digest(conflicts)
	if err != nil {
		return conflicts, err
	}
	conflicts.ConflictHash = contractsv1.SHA256(hash)
	return conflicts, contract.ValidateDefinition("ConflictSet", conflicts)
}

func mutationEvidence(kind contractsv1.MutationEvidenceKind, state contractsv1.ChangeCaseState, observed contractsv1.SHA256) (contractsv1.MutationEvidence, error) {
	evidence := contractsv1.MutationEvidence{Kind: kind, SchemaVersion: 2, CaseId: state.Id, LeaseHash: state.Lease.LeaseHash, Resource: state.Resource, ChangeHash: *state.MergedChangeHash, ObservedHash: observed}
	hash, err := Digest(evidence)
	if err != nil {
		return contractsv1.MutationEvidence{}, err
	}
	evidence.EvidenceHash = contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("MutationEvidence", evidence); err != nil {
		return contractsv1.MutationEvidence{}, err
	}
	return evidence, nil
}

func newChangeCaseState(id string, resource contractsv1.ResourceRef, at time.Time) contractsv1.ChangeCaseState {
	return contractsv1.ChangeCaseState{Kind: "change_case_state", SchemaVersion: 2, Id: id, Resource: resource, Status: contractsv1.ChangeCaseStateStatusProposed, Proposals: []contractsv1.ChangeProposal{}, UpdatedAt: at.UTC()}
}

func clearChangeDecision(state *contractsv1.ChangeCaseState) {
	state.MergedChange, state.MergedChangeHash, state.Conflicts, state.Resolution, state.ResolutionApprovalHash, state.Lease, state.ApplyEvidence, state.ReadbackEvidence, state.BlockerCode = nil, nil, nil, nil, nil, nil, nil, nil, nil
}

func changeCaseID(resource contractsv1.ResourceRef) (string, error) {
	hash, err := Digest(resource)
	return shortID("change-case-", hash), err
}
func proposalHashes(values []contractsv1.ChangeProposal) []contractsv1.SHA256 {
	out := make([]contractsv1.SHA256, len(values))
	for i := range values {
		out[i] = values[i].ProposalHash
	}
	return out
}
func proposalIDs(values []contractsv1.ChangeProposal) []contractsv1.Identifier {
	out := make([]contractsv1.Identifier, len(values))
	for i := range values {
		out[i] = values[i].Id
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func proposalIDPresent(values []contractsv1.ChangeProposal, id contractsv1.Identifier) bool {
	for _, value := range values {
		if value.Id == id {
			return true
		}
	}
	return false
}

func conflictContainsProposal(conflicts *contractsv1.ConflictSet, id contractsv1.Identifier) bool {
	if conflicts == nil {
		return false
	}
	for _, item := range conflicts.Items {
		for _, proposalID := range item.ProposalIds {
			if proposalID == id {
				return true
			}
		}
	}
	return false
}

func proposalResultPresent(proposals []contractsv1.ChangeProposal, resultAggregateID string) bool {
	for _, proposal := range proposals {
		if proposal.SourceResultAggregateId == resultAggregateID {
			return true
		}
	}
	return false
}
func cloneProposals(values []contractsv1.ChangeProposal) ([]contractsv1.ChangeProposal, error) {
	var clone []contractsv1.ChangeProposal
	if err := decodePayload(values, &clone); err != nil {
		return nil, err
	}
	return clone, nil
}
func uniqueHashes(values []contractsv1.SHA256) []contractsv1.SHA256 {
	seen := map[contractsv1.SHA256]bool{}
	out := []contractsv1.SHA256{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func changeReceiptType(value contractsv1.ReceiptReceiptType) bool {
	switch value {
	case contractsv1.ReceiptReceiptTypeChangeProposed, contractsv1.ReceiptReceiptTypeChangeMerged, contractsv1.ReceiptReceiptTypeConflictDetected, contractsv1.ReceiptReceiptTypeResolutionProposed, contractsv1.ReceiptReceiptTypeResolutionApproved, contractsv1.ReceiptReceiptTypeMutationLeaseAcquired, contractsv1.ReceiptReceiptTypeMutationApplied, contractsv1.ReceiptReceiptTypeMutationReadback, contractsv1.ReceiptReceiptTypeResourceGenerationAdvanced:
		return true
	}
	return false
}
