package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	publiccontracts "github.com/JamesbbBriz/agent-workflow/contracts"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaID = "https://agent-workflow.dev/contracts/agent-workflow.v1.schema.json"

var (
	workflowSchemaOnce sync.Once
	workflowSchema     *jsonschema.Schema
	workflowSchemaErr  error
	definitionMu       sync.Mutex
	definitionSchemas  = make(map[string]*jsonschema.Schema)
)

type WorkflowIdentity struct {
	Ref  string
	Hash string
}

func ValidateWorkflow(raw []byte) (WorkflowIdentity, error) {
	value, err := decodeOne(raw)
	if err != nil {
		return WorkflowIdentity{}, err
	}
	schema, err := compiledWorkflowSchema()
	if err != nil {
		return WorkflowIdentity{}, err
	}
	if err := schema.Validate(value); err != nil {
		return WorkflowIdentity{}, fmt.Errorf("workflow schema validation failed: %w", err)
	}

	var workflow contractsv1.WorkflowDefinition
	if err := json.Unmarshal(raw, &workflow); err != nil {
		return WorkflowIdentity{}, fmt.Errorf("decode workflow: %w", err)
	}
	if err := validateWorkflowSemantics(workflow); err != nil {
		return WorkflowIdentity{}, err
	}
	hash, err := canonicalHash(value)
	if err != nil {
		return WorkflowIdentity{}, err
	}
	if workflow.DefinitionHash != nil && string(*workflow.DefinitionHash) != hash {
		return WorkflowIdentity{}, errors.New("workflow definition_hash does not match canonical content")
	}
	intentHash, err := canonicalIntentHash(value)
	if err != nil {
		return WorkflowIdentity{}, err
	}
	if workflow.Intent.DescriptorHash != nil && string(*workflow.Intent.DescriptorHash) != intentHash {
		return WorkflowIdentity{}, errors.New("intent descriptor_hash does not match canonical content")
	}
	return WorkflowIdentity{Ref: fmt.Sprintf("%s@%d", workflow.Id, workflow.Version), Hash: hash}, nil
}

func ValidateDefinition(name string, value any) error {
	switch name {
	case "JobDefinition", "CampaignDefinition", "ContextPackEdition", "ContextBundle", "CapabilityManifest", "ActionArtifact", "Receipt", "ReplayBundle", "CanvasSnapshot", "CampaignExecutionState", "CampaignDrivePreview", "CampaignDriveReceipt",
		"AuthoringCatalog", "WorkflowLintReport", "WorkflowAdmissionPreview", "WorkflowAdmission", "ApprovalBrief", "ApprovalPreview":
	default:
		return fmt.Errorf("public definition %q is unknown", name)
	}
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	document, err := decodeOne(body)
	if err != nil {
		return err
	}
	schema, err := compiledDefinitionSchema(name)
	if err != nil {
		return err
	}
	if err := schema.Validate(document); err != nil {
		return fmt.Errorf("%s schema validation failed: %w", name, err)
	}
	return nil
}

func DefinitionHashes(value any) (string, string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", "", fmt.Errorf("encode definition: %w", err)
	}
	document, err := decodeOne(body)
	if err != nil {
		return "", "", err
	}
	definitionHash, err := canonicalHash(document)
	if err != nil {
		return "", "", err
	}
	intentHash, err := canonicalIntentHash(document)
	if err != nil {
		return "", "", err
	}
	return definitionHash, intentHash, nil
}

func DecodeDefinition(name string, raw []byte, target any) error {
	value, err := decodeOne(raw)
	if err != nil {
		return err
	}
	schema, err := compiledDefinitionSchema(name)
	if err != nil {
		return err
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("%s schema validation failed: %w", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	return nil
}

func compiledDefinitionSchema(name string) (*jsonschema.Schema, error) {
	definitionMu.Lock()
	defer definitionMu.Unlock()
	if schema := definitionSchemas[name]; schema != nil {
		return schema, nil
	}
	var document any
	if err := json.Unmarshal(publiccontracts.AgentWorkflowV1, &document); err != nil {
		return nil, fmt.Errorf("decode embedded workflow schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource(schemaID, document); err != nil {
		return nil, fmt.Errorf("register workflow schema: %w", err)
	}
	schema, err := compiler.Compile(schemaID + "#/$defs/" + name)
	if err != nil {
		return nil, fmt.Errorf("compile %s schema: %w", name, err)
	}
	definitionSchemas[name] = schema
	return schema, nil
}

func compiledWorkflowSchema() (*jsonschema.Schema, error) {
	workflowSchemaOnce.Do(func() {
		var document any
		if err := json.Unmarshal(publiccontracts.AgentWorkflowV1, &document); err != nil {
			workflowSchemaErr = fmt.Errorf("decode embedded workflow schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()
		if err := compiler.AddResource(schemaID, document); err != nil {
			workflowSchemaErr = fmt.Errorf("register workflow schema: %w", err)
			return
		}
		workflowSchema, workflowSchemaErr = compiler.Compile(schemaID)
	})
	return workflowSchema, workflowSchemaErr
}

func decodeOne(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > 2*1024*1024 {
		return nil, errors.New("workflow document size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder, 0)
	if err != nil {
		return nil, fmt.Errorf("decode workflow document: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("workflow document has trailing data")
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("workflow document nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("object field name is invalid")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate object field %q", key)
			}
			object[key], err = decodeJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

func canonicalHash(value any) (string, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("workflow document must be an object")
	}
	delete(object, "definition_hash")
	if intent, ok := object["intent"].(map[string]any); ok {
		delete(intent, "descriptor_hash")
	}
	return canonicalDigest(object)
}

func canonicalIntentHash(value any) (string, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("workflow document must be an object")
	}
	intent, ok := object["intent"].(map[string]any)
	if !ok {
		return "", errors.New("workflow intent must be an object")
	}
	delete(intent, "descriptor_hash")
	return canonicalDigest(intent)
}

func canonicalDigest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize workflow document: %w", err)
	}
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validateWorkflowSemantics(workflow contractsv1.WorkflowDefinition) error {
	if workflow.Intent.Kind != contractsv1.IntentCardKindWorkflow {
		return errors.New("workflow identity or intent kind is invalid")
	}
	if err := uniqueRequirementIDs("default context", workflow.DefaultContext); err != nil {
		return err
	}
	nodes := make(map[string]contractsv1.NodeDefinition, len(workflow.Nodes))
	for _, node := range workflow.Nodes {
		id := string(node.Id)
		if _, exists := nodes[id]; exists {
			return fmt.Errorf("node id %q is duplicated", id)
		}
		nodes[id] = node
		if err := uniqueRequirementIDs("node "+id+" context", node.Context); err != nil {
			return err
		}
		if err := validSlots("node "+id+" input", node.InputSlots); err != nil {
			return err
		}
		if err := validSlots("node "+id+" output", node.OutputSlots); err != nil {
			return err
		}
	}
	if err := validSlots("workflow output", workflow.Outputs); err != nil {
		return err
	}
	for _, node := range workflow.Nodes {
		for _, dependency := range node.DependsOn {
			id := string(node.Id)
			if dependency == id {
				return fmt.Errorf("node %q depends on itself", id)
			}
			if _, exists := nodes[dependency]; !exists {
				return fmt.Errorf("node %q depends on unknown node %q", id, dependency)
			}
		}
	}
	return validateAcyclic(nodes)
}

func uniqueRequirementIDs(label string, requirements []contractsv1.ContextRequirement) error {
	seen := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		id := string(requirement.Id)
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%s requirement id %q is duplicated", label, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validSlots(label string, slots []contractsv1.Slot) error {
	seen := make(map[string]struct{}, len(slots))
	for _, slot := range slots {
		id := string(slot.Id)
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%s slot id %q is duplicated", label, id)
		}
		seen[id] = struct{}{}
		if slot.MinItems > slot.MaxItems {
			return fmt.Errorf("%s slot %q has min_items greater than max_items", label, id)
		}
	}
	return nil
}

func validateAcyclic(nodes map[string]contractsv1.NodeDefinition) error {
	const (
		visiting = iota + 1
		done
	)
	state := make(map[string]int, len(nodes))
	stack := make([]string, 0, len(nodes))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("workflow dependency cycle: %s -> %s", strings.Join(stack, " -> "), id)
		case done:
			return nil
		}
		state[id] = visiting
		stack = append(stack, id)
		dependencies := append([]string(nil), nodes[id].DependsOn...)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = done
		return nil
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
