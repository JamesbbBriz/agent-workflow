package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestEngineCompilesResolvesExecutesAndReplaysWithoutDuplicateProviderWork(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	seed := packFixture(t, scope, cutoff)
	registry, err := workflow.NewRegistry(
		workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed),
		workflow.NewIntentProducer(),
	)
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{
		"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead,
	}, outputCatalog(), provider, workflow.NewMemoryLedger())
	request := workflow.RunRequest{
		Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition,
		NodeID: "research",
	}

	first, err := engine.RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if provider.work != 1 {
		t.Fatalf("provider performed %d units of work, want 1", provider.work)
	}
	if first.Compiled.Nodes[0].Definition.Id != "research" || len(first.Compiled.Nodes[0].Definition.Context) != 2 {
		t.Fatalf("defaults were not compiled into the node: %+v", first.Compiled.Nodes[0])
	}
	if first.Bundle.BundleHash != second.Bundle.BundleHash || first.Replay.BundleHash != second.Replay.BundleHash {
		t.Fatal("redelivery did not converge on the same bundle and replay")
	}
	if len(first.Artifacts) != 1 || len(first.Replay.Receipts) != 7 {
		t.Fatalf("unexpected result: artifacts=%d receipts=%d", len(first.Artifacts), len(first.Replay.Receipts))
	}
	tampered := first.Replay
	tampered.Receipts[3].Payload["accepted"] = false
	if err := workflow.VerifyReplay(tampered); err == nil {
		t.Fatal("tampered replay was accepted")
	}
}

func TestProviderResultRecoveryConvergesAfterLedgerFailure(t *testing.T) {
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	ledger := &failOnceLedger{Ledger: workflow.NewMemoryLedger(), failAt: 6}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger)
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"}
	if _, err := engine.RunNode(context.Background(), request); err == nil {
		t.Fatal("injected ledger failure was ignored")
	}
	result, err := engine.RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if provider.work != 1 || len(result.Replay.Receipts) != 7 {
		t.Fatalf("redelivery duplicated provider work or failed to converge: work=%d receipts=%d", provider.work, len(result.Replay.Receipts))
	}
}

type failOnceLedger struct {
	workflow.Ledger
	appendCount int
	failAt      int
}

func (l *failOnceLedger) Append(receipt contractsv1.Receipt) error {
	l.appendCount++
	if l.appendCount == l.failAt {
		return errors.New("injected append failure")
	}
	return l.Ledger.Append(receipt)
}

func TestFileLedgerSurvivesCoreRestartAndRedelivery(t *testing.T) {
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	ledger, err := workflow.OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"}
	first, err := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := workflow.OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	restartedProvider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	second, err := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), restartedProvider, reopened).RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if provider.work != 1 || restartedProvider.work != 0 || first.Replay.BundleHash != second.Replay.BundleHash {
		t.Fatalf("restart redelivery diverged: first_work=%d restarted_work=%d first=%s second=%s", provider.work, restartedProvider.work, first.Replay.BundleHash, second.Replay.BundleHash)
	}
	material, err := workflow.MaterializeReplay(second.Replay, outputCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if material.Invocation.Playbook.Objective == "" || material.Invocation.IntentChain.PackType != "intent-chain" || len(material.Artifacts) != 1 {
		t.Fatalf("replay did not reconstruct the provider boundary: %+v", material)
	}
}

func TestCompilerInjectsRequiredIntentChainAndPlaybook(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	definition.DefaultContext = definition.DefaultContext[1:]
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(provider.last.Playbook, definition.Intent) || provider.last.IntentChain.PackType != "intent-chain" {
		t.Fatalf("provider did not receive the immutable playbook and intent chain: %+v", provider.last)
	}
}

func TestCompilerNormalizesV1SlotsWithoutExecutionMetadata(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	for nodeIndex := range definition.Nodes {
		for slotIndex := range definition.Nodes[nodeIndex].InputSlots {
			definition.Nodes[nodeIndex].InputSlots[slotIndex].ArtifactKind = nil
			definition.Nodes[nodeIndex].InputSlots[slotIndex].ContentSchema = nil
		}
		for slotIndex := range definition.Nodes[nodeIndex].OutputSlots {
			definition.Nodes[nodeIndex].OutputSlots[slotIndex].ArtifactKind = nil
			definition.Nodes[nodeIndex].OutputSlots[slotIndex].ContentSchema = nil
			definition.Nodes[nodeIndex].OutputSlots[slotIndex].Consumers = nil
		}
	}
	for slotIndex := range definition.Outputs {
		definition.Outputs[slotIndex].ArtifactKind = nil
		definition.Outputs[slotIndex].ContentSchema = nil
		definition.Outputs[slotIndex].Consumers = nil
	}
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	result, err := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Compiled.Nodes[0].Definition.OutputSlots[0].ContentSchema == nil || len(result.Compiled.Nodes[0].Definition.OutputSlots[0].Consumers) != 1 {
		t.Fatalf("legacy v1 slots were not made explicit: %+v", result.Compiled.Nodes[0].Definition.OutputSlots[0])
	}
}

func TestCoreIssuesProviderDeadline(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(provider.last.Deadline); remaining <= 0 || remaining > 11*time.Minute {
		t.Fatalf("provider deadline escaped the Node runtime bound: %s", remaining)
	}
}

func TestEngineRejectsFutureEvidenceCutoff(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"},
	)
	if err == nil || provider.work != 0 {
		t.Fatalf("future evidence cutoff crossed the provider boundary: err=%v work=%d", err, provider.work)
	}
}

func TestRequiredContextFailsClosed(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	registry, err := workflow.NewRegistry(
		workflow.NewCatalogProducer("project-brief", "project-brief", 1),
		workflow.NewIntentProducer(),
	)
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger())
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	_, err = engine.RunNode(context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"})
	var missing *workflow.NeedsContextError
	if !errors.As(err, &missing) || len(missing.Requirements) != 1 || missing.Requirements[0] != "project-brief" {
		t.Fatalf("expected typed project-brief blocker, got %v", err)
	}
	if provider.work != 0 {
		t.Fatal("provider ran without required context")
	}
}

func TestContextPackAuthorityFailsClosedBeforeProviderExecution(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	tests := []struct {
		name   string
		mutate func(*contractsv1.ContextPackEdition)
	}{
		{name: "tampered", mutate: func(pack *contractsv1.ContextPackEdition) { pack.Content = map[string]any{"brief": "tampered"} }},
		{name: "stale", mutate: func(pack *contractsv1.ContextPackEdition) { pack.CapturedAt = cutoff.Add(-8 * 24 * time.Hour) }},
		{name: "partial disallowed", mutate: func(pack *contractsv1.ContextPackEdition) {
			pack.Coverage = contractsv1.ContextPackEditionCoveragePartial
		}},
		{name: "wrong scope", mutate: func(pack *contractsv1.ContextPackEdition) { pack.Scope.SubjectIds = []string{"project-b"} }},
		{name: "unknown pack version", mutate: func(pack *contractsv1.ContextPackEdition) { pack.PackSchemaVersion = 2 }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pack := packFixture(t, scope, cutoff)
			test.mutate(&pack)
			registry, err := workflow.NewRegistry(
				workflow.NewCatalogProducer("project-brief", "project-brief", 1, pack),
				workflow.NewIntentProducer(),
			)
			if err != nil {
				t.Fatal(err)
			}
			provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
			engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger())
			_, err = engine.RunNode(context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"})
			var blocker *workflow.NeedsContextError
			if !errors.As(err, &blocker) || blocker.Reasons["project-brief"] == "" {
				t.Fatalf("invalid required context did not become a typed blocker: %v", err)
			}
			if provider.work != 0 {
				t.Fatal("provider ran with invalid context")
			}
		})
	}
}

func TestOptionalContextProducesExplicitDegradedBundle(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	for index := range definition.DefaultContext {
		if definition.DefaultContext[index].Id == "project-brief" {
			definition.DefaultContext[index].Required = false
		}
	}
	registry, err := workflow.NewRegistry(
		workflow.NewCatalogProducer("project-brief", "project-brief", 1),
		workflow.NewIntentProducer(),
	)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	result, err := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Bundle.Degraded || len(result.Bundle.MissingOptional) != 1 || result.Bundle.MissingOptional[0] != "project-brief" {
		t.Fatalf("optional context was not made explicit: %+v", result.Bundle)
	}
}

func TestCompilerRejectsUnavailableProducerConflictingDefaultsAndBrokenSlots(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	seed := packFixture(t, scope, cutoff)
	tests := []struct {
		name     string
		registry func() *workflow.Registry
		mutate   func(*contractsv1.WorkflowDefinition)
	}{
		{
			name: "producer unavailable",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewIntentProducer())
				return registry
			},
			mutate: func(*contractsv1.WorkflowDefinition) {},
		},
		{
			name: "default conflict",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				conflict := definition.DefaultContext[0]
				conflict.AllowPartial = true
				definition.Nodes[0].Context = append(definition.Nodes[0].Context, conflict)
			},
		},
		{
			name: "broken slot flow",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				definition.Nodes[1].InputSlots[0].ArtifactType = "other-artifact"
			},
		},
		{
			name: "unknown output consumer",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				definition.Nodes[0].OutputSlots[0].Consumers = []string{"missing-node"}
			},
		},
		{
			name: "reserved context pack output",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				kind := contractsv1.SlotArtifactKindContextPack
				definition.Nodes[0].OutputSlots[0].ArtifactKind = &kind
			},
		},
		{
			name: "excessive output fanout",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				definition.Nodes[0].OutputSlots[0].MaxItems = 9
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definition := loadExample(t)
			test.mutate(&definition)
			provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
			engine := workflow.NewEngine(test.registry(), workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger())
			_, err := engine.RunNode(context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"})
			if err == nil {
				t.Fatal("invalid workflow compiled")
			}
			if provider.work != 0 {
				t.Fatal("provider ran after compile failure")
			}
		})
	}
}

func TestEngineRejectsUnknownAggregateContractBeforeProviderExecution(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	job := jobFixture(scope)
	job.SchemaVersion = 2
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: job, Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"},
	)
	if err == nil || provider.work != 0 {
		t.Fatalf("unknown Job contract crossed the provider boundary: err=%v work=%d", err, provider.work)
	}
}

func TestEngineRejectsStaleDeclaredAggregateHash(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	job := jobFixture(scope)
	wrong := contractsv1.SHA256(zeroHash)
	job.DefinitionHash = &wrong
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: job, Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"},
	)
	if err == nil || provider.work != 0 {
		t.Fatalf("stale Job hash crossed the provider boundary: err=%v work=%d", err, provider.work)
	}
}

func TestEngineRejectsArtifactContentOutsideTheDeclaredSchema(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact), content: map[string]any{"recommendation": 42}}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"},
	)
	if err == nil {
		t.Fatal("artifact content outside the declared schema was accepted")
	}
}

func TestEngineRejectsOversizedArtifactContent(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string][]contractsv1.ActionArtifact), content: map[string]any{"recommendation": strings.Repeat("x", 1<<20)}}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"},
	)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized artifact was not rejected: %v", err)
	}
}

type memoProvider struct {
	work    int
	results map[string][]contractsv1.ActionArtifact
	content any
	last    workflow.Invocation
}

func (p *memoProvider) Execute(_ context.Context, invocation workflow.Invocation) ([]contractsv1.ActionArtifact, error) {
	if result, ok := p.results[invocation.IdempotencyKey]; ok {
		return result, nil
	}
	p.work++
	p.last = invocation
	content := p.content
	if content == nil {
		content = map[string]any{"recommendation": "Keep the bounded workflow."}
	}
	hash, err := workflow.Digest(content)
	if err != nil {
		return nil, err
	}
	result := []contractsv1.ActionArtifact{{
		Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "artifact-recommendation-1",
		ArtifactType: "recommendation", JobId: invocation.JobID, CampaignId: invocation.CampaignID,
		WorkflowRef: invocation.WorkflowRef, NodeId: invocation.Node.Id,
		InputHashes: invocation.InputHashes,
		Content:     content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending,
	}}
	p.results[invocation.IdempotencyKey] = result
	return result, nil
}

func loadExample(t *testing.T) contractsv1.WorkflowDefinition {
	t.Helper()
	_, current, _, _ := runtime.Caller(0)
	body, err := os.ReadFile(filepath.Join(filepath.Dir(current), "..", "examples", "research-review.workflow.json"))
	if err != nil {
		t.Fatal(err)
	}
	var definition contractsv1.WorkflowDefinition
	if err := json.Unmarshal(body, &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

func intent(kind contractsv1.IntentCardKind, title string) contractsv1.IntentCard {
	return contractsv1.IntentCard{SchemaVersion: 1, Kind: kind, Title: title, Summary: title, Objective: title, SuccessSignals: []string{"done"}, NonGoals: []string{"none"}, Completion: []string{"done"}, NoActionWhen: []string{"not needed"}}
}

func jobFixture(scope contractsv1.Scope) contractsv1.JobDefinition {
	return contractsv1.JobDefinition{Kind: contractsv1.JobDefinitionKindJobDefinition, SchemaVersion: 1, Id: "job-a", Intent: intent(contractsv1.IntentCardKindJob, "Job"), Scope: scope, Budget: budget(), CampaignArchetypes: []string{"research"}}
}

func campaignFixture(scope contractsv1.Scope, cutoff time.Time) contractsv1.CampaignDefinition {
	return contractsv1.CampaignDefinition{Kind: contractsv1.CampaignDefinitionKindCampaignDefinition, SchemaVersion: 1, Id: "campaign-a", JobId: "job-a", Archetype: "research", Intent: intent(contractsv1.IntentCardKindCampaign, "Campaign"), Scope: scope, EvidenceFrontier: contractsv1.EvidenceFrontier{Cutoff: cutoff, SourceHashes: []contractsv1.SHA256{}}, WorkflowPlan: []contractsv1.WorkflowRef{"research-review@1"}, Budget: budget()}
}

func budget() contractsv1.Budget {
	return contractsv1.Budget{MaxAttempts: 2, MaxActions: 1, MaxCandidates: 3}
}

func packFixture(t *testing.T, scope contractsv1.Scope, cutoff time.Time) contractsv1.ContextPackEdition {
	t.Helper()
	content := map[string]any{"brief": "Prefer evidence-bound recommendations."}
	hash, err := workflow.Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	return contractsv1.ContextPackEdition{
		Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1,
		Id: "pack-project-brief-1", PackType: "project-brief", PackSchemaVersion: 1,
		Authority: contractsv1.ContextPackEditionAuthorityCanonical, Scope: scope,
		CapturedAt: cutoff.Add(-time.Hour), ExpiresAt: cutoff.Add(24 * time.Hour),
		Coverage: contractsv1.ContextPackEditionCoverageComplete, Content: content, ContentSha256: contractsv1.SHA256(hash),
		Provenance: []contractsv1.ArtifactRef{{Id: "seed-receipt", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "seed", SchemaVersion: 1, Sha256: contractsv1.SHA256(zeroHash), MediaType: "application/json"}},
	}
}

func outputCatalog() workflow.OutputCatalog {
	return workflow.OutputCatalog{
		"recommendation@1": func(value any) error {
			object, ok := value.(map[string]any)
			if !ok {
				return errors.New("recommendation must be an object")
			}
			text, ok := object["recommendation"].(string)
			if !ok || text == "" {
				return errors.New("recommendation is required")
			}
			return nil
		},
	}
}

const zeroHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
