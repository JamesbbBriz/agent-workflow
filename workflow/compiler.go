package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

type CompiledWorkflow struct {
	WorkflowRef    contractsv1.WorkflowRef `json:"workflow_ref"`
	DefinitionHash contractsv1.SHA256      `json:"definition_hash"`
	Nodes          []CompiledNode          `json:"nodes"`
	CompileHash    contractsv1.SHA256      `json:"compile_hash"`
}

type CompiledNode struct {
	Definition contractsv1.NodeDefinition `json:"definition"`
}

func compileWorkflow(definition contractsv1.WorkflowDefinition, registry *Registry, aggregateID string, occurredAt time.Time) (CompiledWorkflow, contractsv1.Receipt, error) {
	body, err := json.Marshal(definition)
	if err != nil {
		return CompiledWorkflow{}, contractsv1.Receipt{}, fmt.Errorf("encode workflow definition: %w", err)
	}
	identity, err := contract.ValidateWorkflow(body)
	if err != nil {
		return CompiledWorkflow{}, contractsv1.Receipt{}, err
	}
	if registry == nil {
		return CompiledWorkflow{}, contractsv1.Receipt{}, errors.New("producer registry is required")
	}
	var executable contractsv1.WorkflowDefinition
	if err := json.Unmarshal(body, &executable); err != nil {
		return CompiledWorkflow{}, contractsv1.Receipt{}, err
	}
	definition = executable
	normalizeLegacySlots(&definition)
	if err := validateSlotFlow(definition); err != nil {
		return CompiledWorkflow{}, contractsv1.Receipt{}, err
	}
	compiled := CompiledWorkflow{WorkflowRef: contractsv1.WorkflowRef(identity.Ref), DefinitionHash: contractsv1.SHA256(identity.Hash)}
	for _, node := range definition.Nodes {
		if node.DeadlineSeconds == nil && node.Budget.MaxDurationSeconds == nil {
			return CompiledWorkflow{}, contractsv1.Receipt{}, fmt.Errorf("compile node %q: deadline is required", node.Id)
		}
		contextRequirements, err := compileNodeContext(definition.DefaultContext, node.Context, registry)
		if err != nil {
			return CompiledWorkflow{}, contractsv1.Receipt{}, fmt.Errorf("compile node %q: %w", node.Id, err)
		}
		node.Context = contextRequirements
		if len(node.Context) > 8 {
			return CompiledWorkflow{}, contractsv1.Receipt{}, fmt.Errorf("compile node %q: context fanout exceeds 8", node.Id)
		}
		totalOutputs := 0
		for _, slot := range node.OutputSlots {
			totalOutputs += slot.MaxItems
		}
		if totalOutputs > 8 {
			return CompiledWorkflow{}, contractsv1.Receipt{}, fmt.Errorf("compile node %q: output fanout exceeds 8", node.Id)
		}
		compiled.Nodes = append(compiled.Nodes, CompiledNode{Definition: node})
	}
	hash, err := Digest(struct {
		WorkflowRef    contractsv1.WorkflowRef
		DefinitionHash contractsv1.SHA256
		Nodes          []CompiledNode
	}{compiled.WorkflowRef, compiled.DefinitionHash, compiled.Nodes})
	if err != nil {
		return CompiledWorkflow{}, contractsv1.Receipt{}, err
	}
	compiled.CompileHash = contractsv1.SHA256(hash)
	receipt, err := sealReceipt(aggregateID, 1, contractsv1.ReceiptReceiptTypeCompile, occurredAt, nil,
		[]contractsv1.SHA256{compiled.DefinitionHash}, []contractsv1.SHA256{compiled.CompileHash},
		map[string]any{"workflow_ref": compiled.WorkflowRef})
	return compiled, receipt, err
}

func normalizeLegacySlots(definition *contractsv1.WorkflowDefinition) {
	for nodeIndex := range definition.Nodes {
		node := &definition.Nodes[nodeIndex]
		for slotIndex := range node.OutputSlots {
			slot := &node.OutputSlots[slotIndex]
			if slot.ArtifactKind == nil {
				kind := contractsv1.SlotArtifactKindActionArtifact
				slot.ArtifactKind = &kind
			}
			if slot.ContentSchema == nil {
				schema := contractsv1.WorkflowRef(fmt.Sprintf("%s@1", slot.ArtifactType))
				slot.ContentSchema = &schema
			}
			if len(slot.Consumers) == 0 {
				for _, candidate := range definition.Nodes {
					if containsString(candidate.DependsOn, string(node.Id)) && hasMatchingSlot(candidate.InputSlots, *slot) {
						slot.Consumers = append(slot.Consumers, string(candidate.Id))
					}
				}
				if hasMatchingSlot(definition.Outputs, *slot) {
					slot.Consumers = append(slot.Consumers, "workflow-output")
				}
			}
		}
	}
	for nodeIndex := range definition.Nodes {
		node := &definition.Nodes[nodeIndex]
		for slotIndex := range node.InputSlots {
			slot := &node.InputSlots[slotIndex]
			for _, dependency := range node.DependsOn {
				dependencyNode, ok := nodeByID(definition.Nodes, dependency)
				if !ok {
					continue
				}
				if source, ok := matchingSlot(dependencyNode.OutputSlots, *slot); ok {
					if slot.ArtifactKind == nil {
						slot.ArtifactKind = source.ArtifactKind
					}
					if slot.ContentSchema == nil {
						slot.ContentSchema = source.ContentSchema
					}
				}
			}
		}
	}
	for slotIndex := range definition.Outputs {
		slot := &definition.Outputs[slotIndex]
		for _, node := range definition.Nodes {
			if source, ok := matchingSlot(node.OutputSlots, *slot); ok {
				if slot.ArtifactKind == nil {
					slot.ArtifactKind = source.ArtifactKind
				}
				if slot.ContentSchema == nil {
					slot.ContentSchema = source.ContentSchema
				}
			}
		}
		if len(slot.Consumers) == 0 {
			slot.Consumers = append([]string(nil), definition.Intent.Consumers...)
		}
	}
}

func nodeByID(nodes []contractsv1.NodeDefinition, id string) (contractsv1.NodeDefinition, bool) {
	for _, node := range nodes {
		if string(node.Id) == id {
			return node, true
		}
	}
	return contractsv1.NodeDefinition{}, false
}

func hasMatchingSlot(slots []contractsv1.Slot, wanted contractsv1.Slot) bool {
	_, ok := matchingSlot(slots, wanted)
	return ok
}

func matchingSlot(slots []contractsv1.Slot, wanted contractsv1.Slot) (contractsv1.Slot, bool) {
	for _, slot := range slots {
		if slot.Id == wanted.Id && slot.ArtifactType == wanted.ArtifactType {
			return slot, true
		}
	}
	return contractsv1.Slot{}, false
}

func compileNodeContext(defaults, explicit []contractsv1.ContextRequirement, registry *Registry) ([]contractsv1.ContextRequirement, error) {
	intentRequirement := contractsv1.ContextRequirement{
		Id: "intent-chain", Selector: "intent-chain", PackType: "intent-chain",
		SchemaVersion: 1, Required: true, AllowPartial: false,
	}
	defaults = append([]contractsv1.ContextRequirement{intentRequirement}, defaults...)
	byID := make(map[string]contractsv1.ContextRequirement, len(defaults)+len(explicit))
	order := make([]string, 0, len(defaults)+len(explicit))
	for _, requirement := range append(append([]contractsv1.ContextRequirement(nil), defaults...), explicit...) {
		id := string(requirement.Id)
		if existing, ok := byID[id]; ok {
			if !reflect.DeepEqual(existing, requirement) {
				return nil, fmt.Errorf("context requirement %q conflicts with the workflow default", id)
			}
			continue
		}
		producer, ok := registry.lookup(string(requirement.Selector))
		if !ok {
			return nil, fmt.Errorf("context producer %q is not registered", requirement.Selector)
		}
		if !producer.Supports(string(requirement.PackType), requirement.SchemaVersion) {
			return nil, fmt.Errorf("context producer %q does not support %s@%d", requirement.Selector, requirement.PackType, requirement.SchemaVersion)
		}
		byID[id] = requirement
		order = append(order, id)
	}
	result := make([]contractsv1.ContextRequirement, 0, len(order))
	for _, id := range order {
		result = append(result, byID[id])
	}
	return result, nil
}

func validateSlotFlow(definition contractsv1.WorkflowDefinition) error {
	nodes := make(map[string]contractsv1.NodeDefinition, len(definition.Nodes))
	dependents := make(map[string]map[string]struct{}, len(definition.Nodes))
	globalOutputs := make(map[string]contractsv1.Slot)
	for _, node := range definition.Nodes {
		nodes[string(node.Id)] = node
		for _, output := range node.OutputSlots {
			if output.ArtifactKind != nil && *output.ArtifactKind == contractsv1.SlotArtifactKindContextPack {
				return fmt.Errorf("node %q output slot %q uses reserved context_pack output support", node.Id, output.Id)
			}
			if existing, exists := globalOutputs[string(output.Id)]; exists && !slotProducerCompatible(existing, output) {
				return fmt.Errorf("output slot %q has incompatible producer definitions: %+v and %+v", output.Id, existing, output)
			}
			globalOutputs[string(output.Id)] = output
		}
		for _, dependency := range node.DependsOn {
			if dependents[dependency] == nil {
				dependents[dependency] = make(map[string]struct{})
			}
			dependents[dependency][string(node.Id)] = struct{}{}
		}
	}
	workflowInputs := make(map[string]contractsv1.Slot, len(definition.Inputs))
	for _, input := range definition.Inputs {
		if input.ArtifactKind == nil || *input.ArtifactKind != contractsv1.SlotArtifactKindActionArtifact || input.ContentSchema == nil || len(input.Consumers) == 0 {
			return fmt.Errorf("workflow input slot %q must declare action_artifact kind, content_schema, and consumers", input.Id)
		}
		if _, duplicate := workflowInputs[string(input.Id)]; duplicate {
			return fmt.Errorf("workflow input slot %q is duplicated", input.Id)
		}
		for _, consumer := range input.Consumers {
			node, ok := nodes[consumer]
			if !ok || !hasMatchingSlot(node.InputSlots, input) {
				return fmt.Errorf("workflow input slot %q has unknown consumer %q", input.Id, consumer)
			}
		}
		workflowInputs[string(input.Id)] = input
	}
	for _, node := range definition.Nodes {
		outputTypes := make(map[contractsv1.Identifier]struct{}, len(node.OutputSlots))
		for _, output := range node.OutputSlots {
			if output.ArtifactKind == nil || output.ContentSchema == nil || len(output.Consumers) == 0 {
				return fmt.Errorf("node %q output slot %q must declare artifact_kind, content_schema, and consumers", node.Id, output.Id)
			}
			if _, duplicate := outputTypes[output.ArtifactType]; duplicate {
				return fmt.Errorf("node %q output artifact type %q is duplicated", node.Id, output.ArtifactType)
			}
			outputTypes[output.ArtifactType] = struct{}{}
			for _, consumer := range output.Consumers {
				if consumer == "workflow-output" {
					continue
				}
				if _, ok := dependents[string(node.Id)][consumer]; !ok {
					return fmt.Errorf("node %q output slot %q has unknown consumer %q", node.Id, output.Id, consumer)
				}
			}
		}
	}
	for _, node := range definition.Nodes {
		available := make(map[string]contractsv1.Slot)
		owners := make(map[string]string)
		for _, dependency := range node.DependsOn {
			for _, slot := range nodes[dependency].OutputSlots {
				if owner, duplicate := owners[string(slot.Id)]; duplicate && !slotProducerCompatible(available[string(slot.Id)], slot) {
					return fmt.Errorf("node %q input slot %q has incompatible dependency producers %q and %q", node.Id, slot.Id, owner, dependency)
				}
				available[string(slot.Id)] = slot
				owners[string(slot.Id)] = dependency
			}
		}
		for _, input := range node.InputSlots {
			output, ok := available[string(input.Id)]
			direct := ok && slotSupplies(output, input)
			if !direct {
				for _, candidate := range available {
					if artifactContractSupplies(candidate, input) {
						direct = true
						break
					}
				}
			}
			external := workflowInputSupplies(workflowInputs[string(input.Id)], input, string(node.Id))
			if direct && external {
				return fmt.Errorf("node %q input slot %q has both dependency and Workflow producers", node.Id, input.Id)
			}
			if !direct && !external {
				return fmt.Errorf("node %q input slot %q is not supplied by a direct dependency", node.Id, input.Id)
			}
		}
	}
	availableOutputs := make(map[string]contractsv1.Slot)
	for _, node := range definition.Nodes {
		for _, slot := range node.OutputSlots {
			availableOutputs[string(slot.Id)] = slot
		}
	}
	for _, output := range definition.Outputs {
		if output.ArtifactKind == nil || output.ContentSchema == nil || len(output.Consumers) == 0 {
			return fmt.Errorf("workflow output slot %q must declare artifact_kind, content_schema, and consumers", output.Id)
		}
		for _, consumer := range output.Consumers {
			if !containsString(definition.Intent.Consumers, consumer) {
				return fmt.Errorf("workflow output slot %q has undeclared consumer %q", output.Id, consumer)
			}
		}
		candidate, ok := availableOutputs[string(output.Id)]
		if !ok || candidate.ArtifactType != output.ArtifactType || candidate.MaxItems < output.MinItems || candidate.ArtifactKind == nil || *candidate.ArtifactKind != *output.ArtifactKind || candidate.ContentSchema == nil || *candidate.ContentSchema != *output.ContentSchema {
			return fmt.Errorf("workflow output slot %q is not supplied by a node", output.Id)
		}
	}
	return nil
}

func slotSupplies(output, input contractsv1.Slot) bool {
	return output.Id == input.Id && artifactContractSupplies(output, input)
}

func artifactContractSupplies(output, input contractsv1.Slot) bool {
	return output.ArtifactType == input.ArtifactType && output.MaxItems >= input.MinItems && output.ArtifactKind != nil && input.ArtifactKind != nil && *output.ArtifactKind == *input.ArtifactKind && output.ContentSchema != nil && input.ContentSchema != nil && *output.ContentSchema == *input.ContentSchema
}

func slotProducerCompatible(left, right contractsv1.Slot) bool {
	return left.Id == right.Id && left.ArtifactType == right.ArtifactType && left.MaxItems == right.MaxItems && left.CountsAsCandidates == right.CountsAsCandidates && reflect.DeepEqual(left.ArtifactKind, right.ArtifactKind) && reflect.DeepEqual(left.ContentSchema, right.ContentSchema) && reflect.DeepEqual(left.Consumers, right.Consumers)
}

func workflowInputSupplies(input, wanted contractsv1.Slot, consumer string) bool {
	return slotSupplies(input, wanted) && containsString(input.Consumers, consumer)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func compiledNodeByID(compiled CompiledWorkflow, nodeID string) (CompiledNode, bool) {
	for _, node := range compiled.Nodes {
		if string(node.Definition.Id) == nodeID {
			return node, true
		}
	}
	return CompiledNode{}, false
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
