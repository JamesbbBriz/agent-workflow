package workflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

var ErrContextUnavailable = errors.New("context unavailable")

type NeedsContextError struct {
	Requirements []string
	Reasons      map[string]string
}

func (e *NeedsContextError) Error() string {
	return "needs_context: " + fmt.Sprint(e.Requirements)
}

func needsContext(requirement contractsv1.ContextRequirement, err error) *NeedsContextError {
	reason := "unavailable"
	if err != nil && !errors.Is(err, ErrContextUnavailable) {
		reason = "unusable"
	}
	return &NeedsContextError{Requirements: []string{string(requirement.Id)}, Reasons: map[string]string{string(requirement.Id): reason}}
}

type Producer interface {
	Selector() string
	Supports(packType string, schemaVersion int) bool
	Resolve(context.Context, ProducerRequest) (contractsv1.ContextPackEdition, error)
}

type ProducerRequest struct {
	Requirement    contractsv1.ContextRequirement
	Job            contractsv1.JobDefinition
	Campaign       contractsv1.CampaignDefinition
	Workflow       contractsv1.WorkflowDefinition
	WorkflowRef    contractsv1.WorkflowRef
	CompileReceipt contractsv1.Receipt
	EvidenceCutoff time.Time
}

type Registry struct {
	producers map[string]Producer
}

type authoringProducer interface {
	authoringContract() (contractsv1.CatalogProducer, []contractsv1.ExpandedNodeContractContextAuthoritiesElem)
}

func NewRegistry(producers ...Producer) (*Registry, error) {
	registry := &Registry{producers: make(map[string]Producer, len(producers))}
	for _, producer := range producers {
		if producer == nil || producer.Selector() == "" {
			return nil, errors.New("context producer selector is required")
		}
		if _, exists := registry.producers[producer.Selector()]; exists {
			return nil, fmt.Errorf("context producer %q is duplicated", producer.Selector())
		}
		registry.producers[producer.Selector()] = producer
	}
	return registry, nil
}

func (r *Registry) lookup(selector string) (Producer, bool) {
	if r == nil {
		return nil, false
	}
	producer, ok := r.producers[selector]
	return producer, ok
}

type CatalogProducer struct {
	selector      string
	packType      string
	schemaVersion int
	editions      []contractsv1.ContextPackEdition
}

func NewCatalogProducer(selector, packType string, schemaVersion int, editions ...contractsv1.ContextPackEdition) *CatalogProducer {
	return &CatalogProducer{selector: selector, packType: packType, schemaVersion: schemaVersion, editions: append([]contractsv1.ContextPackEdition(nil), editions...)}
}

func (p *CatalogProducer) Selector() string { return p.selector }

func (p *CatalogProducer) Supports(packType string, schemaVersion int) bool {
	return packType == p.packType && schemaVersion == p.schemaVersion
}

func (p *CatalogProducer) authoringContract() (contractsv1.CatalogProducer, []contractsv1.ExpandedNodeContractContextAuthoritiesElem) {
	authorities := make([]contractsv1.ExpandedNodeContractContextAuthoritiesElem, 0, 3)
	seen := map[contractsv1.ExpandedNodeContractContextAuthoritiesElem]bool{}
	for _, edition := range p.editions {
		authority := contractsv1.ExpandedNodeContractContextAuthoritiesElem(edition.Authority)
		if !seen[authority] {
			seen[authority] = true
			authorities = append(authorities, authority)
		}
	}
	sort.Slice(authorities, func(i, j int) bool { return authorities[i] < authorities[j] })
	return contractsv1.CatalogProducer{Selector: contractsv1.Identifier(p.selector), PackType: contractsv1.Identifier(p.packType), SchemaVersion: p.schemaVersion}, authorities
}

func (p *CatalogProducer) Resolve(_ context.Context, request ProducerRequest) (contractsv1.ContextPackEdition, error) {
	candidates := make([]contractsv1.ContextPackEdition, 0)
	for _, edition := range p.editions {
		if string(edition.PackType) != string(request.Requirement.PackType) || edition.PackSchemaVersion != request.Requirement.SchemaVersion {
			continue
		}
		if request.Requirement.EditionId != nil && edition.Id != *request.Requirement.EditionId {
			continue
		}
		if request.Requirement.EditionId == nil && !reflect.DeepEqual(edition.Scope, request.Campaign.Scope) {
			continue
		}
		if request.Requirement.EditionId == nil && (edition.CapturedAt.After(request.EvidenceCutoff) || !edition.ExpiresAt.After(request.EvidenceCutoff)) {
			continue
		}
		candidates = append(candidates, edition)
	}
	if len(candidates) == 0 {
		return contractsv1.ContextPackEdition{}, ErrContextUnavailable
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CapturedAt.Equal(candidates[j].CapturedAt) {
			return candidates[i].Id < candidates[j].Id
		}
		return candidates[i].CapturedAt.After(candidates[j].CapturedAt)
	})
	return candidates[0], nil
}

type IntentProducer struct{}

func NewIntentProducer() IntentProducer { return IntentProducer{} }

func (IntentProducer) Selector() string { return "intent-chain" }

func (IntentProducer) Supports(packType string, schemaVersion int) bool {
	return packType == "intent-chain" && schemaVersion == 1
}

func (IntentProducer) authoringContract() (contractsv1.CatalogProducer, []contractsv1.ExpandedNodeContractContextAuthoritiesElem) {
	return contractsv1.CatalogProducer{Selector: "intent-chain", PackType: "intent-chain", SchemaVersion: 1}, []contractsv1.ExpandedNodeContractContextAuthoritiesElem{contractsv1.ExpandedNodeContractContextAuthoritiesElemDerived}
}

func (IntentProducer) Resolve(_ context.Context, request ProducerRequest) (contractsv1.ContextPackEdition, error) {
	content := map[string]any{
		"job": request.Job.Intent, "campaign": request.Campaign.Intent, "workflow": request.Workflow.Intent,
		"workflow_ref": request.WorkflowRef,
	}
	contentHash, err := Digest(content)
	if err != nil {
		return contractsv1.ContextPackEdition{}, err
	}
	return contractsv1.ContextPackEdition{
		Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1,
		Id: shortID("pack-intent-", contentHash), PackType: "intent-chain", PackSchemaVersion: 1,
		Authority: contractsv1.ContextPackEditionAuthorityDerived, Scope: request.Campaign.Scope,
		CapturedAt: request.EvidenceCutoff.UTC(), ExpiresAt: request.EvidenceCutoff.Add(24 * time.Hour).UTC(),
		Coverage: contractsv1.ContextPackEditionCoverageComplete, Content: content, ContentSha256: contractsv1.SHA256(contentHash),
		Provenance: []contractsv1.ArtifactRef{{
			Id: request.CompileReceipt.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "compile",
			SchemaVersion: 1, Sha256: request.CompileReceipt.ReceiptHash, MediaType: "application/json",
		}},
	}, nil
}

func verifyProducerContext(producer Producer, request ProducerRequest, pack contractsv1.ContextPackEdition) error {
	expected, err := producer.Resolve(context.Background(), request)
	if err != nil {
		return errors.New("context pack cannot be resolved from its registered producer")
	}
	expected.Content = pack.Content // ContentSha256 is verified separately.
	if producer.Selector() == "intent-chain" {
		expected.Provenance = pack.Provenance // Built-in intent content is the derived authority; compile identity is historical metadata.
	}
	if !sameDigest(expected, pack) {
		return errors.New("context pack does not match its registered producer")
	}
	return nil
}

func sameDigest(left, right any) bool {
	leftHash, leftErr := Digest(left)
	rightHash, rightErr := Digest(right)
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

type resolvedContext struct {
	Bundle contractsv1.ContextBundle
	Packs  []contractsv1.ContextPackEdition
}

func resolveContext(ctx context.Context, registry *Registry, request RunRequest, compiled CompiledWorkflow, node CompiledNode, compileReceipt contractsv1.Receipt) (resolvedContext, error) {
	result := resolvedContext{Bundle: contractsv1.ContextBundle{Entries: []contractsv1.ContextPackRef{}}, Packs: []contractsv1.ContextPackEdition{}}
	missing := make([]string, 0)
	for _, requirement := range node.Definition.Context {
		producer, ok := registry.lookup(string(requirement.Selector))
		if !ok {
			return result, fmt.Errorf("context producer %q is not registered", requirement.Selector)
		}
		pack, err := producer.Resolve(ctx, ProducerRequest{
			Requirement: requirement, Job: request.Job, Campaign: request.Campaign, Workflow: request.Workflow,
			WorkflowRef: compiled.WorkflowRef, CompileReceipt: compileReceipt, EvidenceCutoff: request.Campaign.EvidenceFrontier.Cutoff,
		})
		if errors.Is(err, ErrContextUnavailable) {
			if requirement.Required {
				missing = append(missing, string(requirement.Id))
			} else {
				result.Bundle.Degraded = true
				result.Bundle.MissingOptional = append(result.Bundle.MissingOptional, string(requirement.Id))
			}
			continue
		}
		if err != nil {
			if requirement.Required {
				return result, needsContext(requirement, err)
			}
			return result, fmt.Errorf("resolve context %q: %w", requirement.Id, err)
		}
		producerRequest := ProducerRequest{Requirement: requirement, Job: request.Job, Campaign: request.Campaign, Workflow: request.Workflow, WorkflowRef: compiled.WorkflowRef, CompileReceipt: compileReceipt, EvidenceCutoff: request.Campaign.EvidenceFrontier.Cutoff}
		if err := verifyProducerContext(producer, producerRequest, pack); err != nil {
			if requirement.Required {
				return result, needsContext(requirement, err)
			}
			return result, fmt.Errorf("context %q: %w", requirement.Id, err)
		}
		if err := validatePack(pack, requirement, request.Campaign.Scope, request.Campaign.EvidenceFrontier.Cutoff); err != nil {
			if requirement.Required {
				return result, needsContext(requirement, err)
			}
			return result, fmt.Errorf("context %q: %w", requirement.Id, err)
		}
		packHash, err := Digest(pack)
		if err != nil {
			return result, err
		}
		result.Packs = append(result.Packs, pack)
		requirementID := requirement.Id
		result.Bundle.Entries = append(result.Bundle.Entries, contractsv1.ContextPackRef{
			Id: pack.Id, Kind: contractsv1.ContextPackRefKindContextPack, RequirementId: &requirementID, ArtifactType: pack.PackType,
			SchemaVersion: pack.PackSchemaVersion, Sha256: contractsv1.SHA256(packHash), MediaType: "application/json",
		})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		reasons := make(map[string]string, len(missing))
		for _, requirement := range missing {
			reasons[requirement] = "unavailable"
		}
		return result, &NeedsContextError{Requirements: missing, Reasons: reasons}
	}
	result.Bundle.Kind = contractsv1.ContextBundleKindContextBundle
	result.Bundle.SchemaVersion = 1
	result.Bundle.JobId = request.Job.Id
	result.Bundle.CampaignId = request.Campaign.Id
	result.Bundle.WorkflowRef = compiled.WorkflowRef
	result.Bundle.NodeId = node.Definition.Id
	result.Bundle.EvidenceCutoff = request.Campaign.EvidenceFrontier.Cutoff.UTC()
	identityHash, err := Digest(result.Bundle)
	if err != nil {
		return result, err
	}
	result.Bundle.Id = shortID("bundle-", identityHash)
	bundleHash, err := Digest(result.Bundle)
	if err != nil {
		return result, err
	}
	result.Bundle.BundleHash = contractsv1.SHA256(bundleHash)
	if err := contract.ValidateDefinition("ContextBundle", result.Bundle); err != nil {
		return result, err
	}
	if err := validateResolvedContextForNode(result, registry, request, compiled, node, compileReceipt); err != nil {
		return result, err
	}
	return result, nil
}

func validateResolvedContextForNode(resolved resolvedContext, registry *Registry, request RunRequest, compiled CompiledWorkflow, node CompiledNode, compileReceipt contractsv1.Receipt) error {
	if err := validateRecordedContextForNode(resolved, node, request.Campaign.Scope, request.Campaign.EvidenceFrontier.Cutoff); err != nil {
		return err
	}
	for index, entry := range resolved.Bundle.Entries {
		requirement := contextRequirement(node, *entry.RequirementId)
		producer, _ := registry.lookup(string(requirement.Selector))
		producerRequest := ProducerRequest{Requirement: requirement, Job: request.Job, Campaign: request.Campaign, Workflow: request.Workflow, WorkflowRef: compiled.WorkflowRef, CompileReceipt: compileReceipt, EvidenceCutoff: request.Campaign.EvidenceFrontier.Cutoff}
		if err := verifyProducerContext(producer, producerRequest, resolved.Packs[index]); err != nil {
			return fmt.Errorf("context %q has no producer authority: %w", requirement.Id, err)
		}
	}
	return nil
}

func validateRecordedContextForNode(resolved resolvedContext, node CompiledNode, scope contractsv1.Scope, cutoff time.Time) error {
	if err := VerifyContextBundle(resolved.Bundle, resolved.Packs); err != nil {
		return err
	}
	requirements := make(map[contractsv1.Identifier]contractsv1.ContextRequirement, len(node.Definition.Context))
	for _, requirement := range node.Definition.Context {
		requirements[requirement.Id] = requirement
	}
	present := make(map[contractsv1.Identifier]bool, len(resolved.Bundle.Entries))
	for index, entry := range resolved.Bundle.Entries {
		if entry.RequirementId == nil {
			return errors.New("context bundle entry has no requirement binding")
		}
		requirement, ok := requirements[*entry.RequirementId]
		if !ok || present[*entry.RequirementId] {
			return errors.New("context bundle contains an unknown or duplicate requirement")
		}
		if err := validatePack(resolved.Packs[index], requirement, scope, cutoff); err != nil {
			return err
		}
		present[*entry.RequirementId] = true
	}
	missingOptional := make([]string, 0)
	for _, requirement := range node.Definition.Context {
		if present[requirement.Id] {
			continue
		}
		if requirement.Required {
			return errors.New("context bundle is missing a required requirement")
		}
		missingOptional = append(missingOptional, string(requirement.Id))
	}
	sort.Strings(missingOptional)
	actualMissing := append([]string(nil), resolved.Bundle.MissingOptional...)
	sort.Strings(actualMissing)
	if len(actualMissing) != len(missingOptional) || len(actualMissing) > 0 && !reflect.DeepEqual(actualMissing, missingOptional) || resolved.Bundle.Degraded != (len(missingOptional) > 0) {
		return errors.New("context bundle optional coverage does not match")
	}
	return nil
}

func contextRequirement(node CompiledNode, id contractsv1.Identifier) contractsv1.ContextRequirement {
	for _, requirement := range node.Definition.Context {
		if requirement.Id == id {
			return requirement
		}
	}
	return contractsv1.ContextRequirement{}
}

func VerifyContextBundle(bundle contractsv1.ContextBundle, packs []contractsv1.ContextPackEdition) error {
	if err := contract.ValidateDefinition("ContextBundle", bundle); err != nil {
		return err
	}
	if len(bundle.Entries) != len(packs) {
		return errors.New("context bundle entries do not match the exact pack set")
	}
	seenRequirements := make(map[contractsv1.Identifier]bool, len(bundle.Entries))
	for index, pack := range packs {
		hash, err := Digest(pack)
		if err != nil {
			return err
		}
		entry := bundle.Entries[index]
		if entry.RequirementId != nil {
			if seenRequirements[*entry.RequirementId] {
				return errors.New("context bundle requirement binding is duplicated")
			}
			seenRequirements[*entry.RequirementId] = true
		}
		if entry.Id != pack.Id || entry.ArtifactType != pack.PackType || entry.SchemaVersion != pack.PackSchemaVersion || entry.Sha256 != contractsv1.SHA256(hash) {
			return errors.New("context bundle pack reference does not match")
		}
	}
	expected := bundle.BundleHash
	bundle.BundleHash = ""
	hash, err := Digest(bundle)
	if err != nil || contractsv1.SHA256(hash) != expected {
		return errors.New("context bundle hash does not match")
	}
	return nil
}

func validatePack(pack contractsv1.ContextPackEdition, requirement contractsv1.ContextRequirement, scope contractsv1.Scope, cutoff time.Time) error {
	if err := validateBoundedJSON("context pack content", pack.Content); err != nil {
		return err
	}
	if err := contract.ValidateDefinition("ContextPackEdition", pack); err != nil {
		return err
	}
	if pack.Kind != contractsv1.ContextPackEditionKindContextPackEdition || pack.SchemaVersion != 1 {
		return errors.New("unknown context pack contract version")
	}
	if pack.PackType != requirement.PackType || pack.PackSchemaVersion != requirement.SchemaVersion {
		return errors.New("context pack type or schema version does not match")
	}
	if requirement.EditionId != nil && pack.Id != *requirement.EditionId {
		return errors.New("context pack edition does not match")
	}
	if !reflect.DeepEqual(pack.Scope, scope) {
		return errors.New("context pack scope does not match")
	}
	if pack.CapturedAt.After(cutoff) || !pack.ExpiresAt.After(cutoff) {
		return errors.New("context pack is outside the evidence cutoff")
	}
	if requirement.MaxAgeSeconds != nil && pack.CapturedAt.Add(time.Duration(*requirement.MaxAgeSeconds)*time.Second).Before(cutoff) {
		return errors.New("context pack is stale")
	}
	if pack.Coverage == contractsv1.ContextPackEditionCoveragePartial && !requirement.AllowPartial {
		return errors.New("partial context pack is not allowed")
	}
	if len(pack.Provenance) == 0 {
		return errors.New("context pack provenance is required")
	}
	contentHash, err := Digest(pack.Content)
	if err != nil {
		return err
	}
	if contractsv1.SHA256(contentHash) != pack.ContentSha256 {
		return errors.New("context pack content hash does not match")
	}
	return nil
}
