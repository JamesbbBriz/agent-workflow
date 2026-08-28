package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestAuthoringPreviewConfirmKeepsImmutableVersions(t *testing.T) {
	core := authoringCore(t)
	definition := authoringDefinition(t)
	job, campaign := testDefinitions(definition)
	preview, lint, err := core.Preview(job, campaign, definition, "operator@example.com")
	if err != nil || !lint.Valid || preview.BaseRevision != 0 || len(preview.ExpandedNodes[0].Definition.Context) != 2 {
		t.Fatalf("preview did not expand a valid draft: preview=%+v lint=%+v err=%v", preview, lint, err)
	}
	if _, err := core.ReadWorkflow("research-review", 1); !errors.Is(err, workflow.ErrReplayEmpty) {
		t.Fatalf("preview mutated canonical history: %v", err)
	}
	at := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	first, err := core.Confirm(preview, "operator@example.com", at)
	if err != nil || first.Revision != 1 || first.Receipt.Actor == nil || *first.Receipt.Actor != "operator@example.com" {
		t.Fatalf("confirm failed: admission=%+v err=%v", first, err)
	}
	redelivery, err := core.Confirm(preview, "operator@example.com", at.Add(time.Hour))
	if err != nil || redelivery.Receipt.ReceiptHash != first.Receipt.ReceiptHash {
		t.Fatalf("exact confirmation did not converge: admission=%+v err=%v", redelivery, err)
	}

	definition.Version = 2
	definition.Intent.Summary = "A revised immutable version."
	campaign.WorkflowPlan = []contractsv1.WorkflowRef{"research-review@2"}
	secondPreview, _, err := core.Preview(job, campaign, definition, "operator@example.com")
	if err != nil || secondPreview.BaseRevision != 1 {
		t.Fatalf("version 2 preview failed: %v", err)
	}
	if _, err := core.Confirm(secondPreview, "operator@example.com", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	old, err := core.ReadWorkflow("research-review", 1)
	if err != nil || old.Workflow.Version != 1 || old.Workflow.Intent.Summary == definition.Intent.Summary {
		t.Fatalf("old Workflow version changed: %+v err=%v", old, err)
	}
}

func TestAuthoringPreviewRejectsUnavailableRequiredContext(t *testing.T) {
	definition := authoringDefinition(t)
	job, campaign := testDefinitions(definition)
	registry, err := workflow.NewRegistry(workflow.NewIntentProducer(), workflow.NewCatalogProducer("project-brief", "project-brief", 1))
	if err != nil {
		t.Fatal(err)
	}
	core := workflow.NewAuthoringCore(registry,
		workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent, "human-approval@1": contractsv1.NodeDefinitionKindApproval},
		workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead},
		workflow.OutputCatalog{"recommendation@1": func(any) error { return nil }, "review-decision@1": func(any) error { return nil }},
		[]string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, workflow.NewMemoryLedger())
	_, report, err := core.Preview(job, campaign, definition, "operator@example.com")
	if err == nil || report.Valid || report.Issues[0].Code != "context-unavailable" {
		t.Fatalf("unavailable required Context received an admission token: report=%+v err=%v", report, err)
	}
}

func TestAuthoringPreviewValidatesResolvedRequiredContext(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*contractsv1.WorkflowDefinition, *contractsv1.ContextPackEdition)
	}{
		{name: "tampered hash", mutate: func(_ *contractsv1.WorkflowDefinition, pack *contractsv1.ContextPackEdition) {
			pack.Content = map[string]any{"brief": "tampered"}
		}},
		{name: "pinned wrong scope", mutate: func(definition *contractsv1.WorkflowDefinition, pack *contractsv1.ContextPackEdition) {
			pack.Scope.SubjectIds = []string{"other-project"}
			editionID := pack.Id
			definition.DefaultContext[1].EditionId = &editionID
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := authoringDefinition(t)
			job, campaign := testDefinitions(definition)
			content := map[string]any{"brief": "bounded"}
			hash, err := workflow.Digest(content)
			if err != nil {
				t.Fatal(err)
			}
			zero := contractsv1.SHA256("sha256:0000000000000000000000000000000000000000000000000000000000000000")
			pack := contractsv1.ContextPackEdition{Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1, Id: "project-brief-edition", PackType: "project-brief", PackSchemaVersion: 1, Authority: contractsv1.ContextPackEditionAuthorityCanonical, Scope: campaign.Scope, CapturedAt: campaign.EvidenceFrontier.Cutoff.Add(-time.Hour), ExpiresAt: campaign.EvidenceFrontier.Cutoff.Add(time.Hour), Coverage: contractsv1.ContextPackEditionCoverageComplete, Content: content, ContentSha256: contractsv1.SHA256(hash), Provenance: []contractsv1.ArtifactRef{{Id: "seed", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "seed", SchemaVersion: 1, Sha256: zero, MediaType: "application/json"}}}
			test.mutate(&definition, &pack)
			registry, _ := workflow.NewRegistry(workflow.NewIntentProducer(), workflow.NewCatalogProducer("project-brief", "project-brief", 1, pack))
			core := workflow.NewAuthoringCore(registry, workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent, "human-approval@1": contractsv1.NodeDefinitionKindApproval}, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, workflow.OutputCatalog{"recommendation@1": func(any) error { return nil }, "review-decision@1": func(any) error { return nil }}, []string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, workflow.NewMemoryLedger())
			if _, report, err := core.Preview(job, campaign, definition, "operator@example.com"); err == nil || report.Valid {
				t.Fatalf("invalid required Context received an admission token: report=%+v err=%v", report, err)
			}
		})
	}
}

func TestAuthoringFailsClosedOnCatalogAndPreviewDrift(t *testing.T) {
	core := authoringCore(t)
	definition := authoringDefinition(t)
	definition.Nodes[0].Executor = "shell@1"
	report := core.Lint(definition)
	if report.Valid || report.Issues[0].Code != "executor-unregistered" {
		t.Fatalf("unregistered executor passed lint: %+v", report)
	}

	definition = authoringDefinition(t)
	job, campaign := testDefinitions(definition)
	preview, _, err := core.Preview(job, campaign, definition, "operator@example.com")
	if err != nil {
		t.Fatal(err)
	}
	preview.Workflow.Intent.Objective = "altered after preview"
	if _, err := core.Confirm(preview, "operator@example.com", time.Now()); err == nil {
		t.Fatal("altered preview was accepted")
	}
}

func TestApprovalBindsBriefActorOptionAndStaleToken(t *testing.T) {
	ledger := workflow.NewMemoryLedger()
	core := authoringCoreWithLedger(t, ledger)
	source := approvalSource(t, ledger)
	action := source.Artifacts[0]
	var resultReceipt contractsv1.Receipt
	for _, receipt := range source.Replay.Receipts {
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeResult {
			resultReceipt = receipt
		}
	}
	brief := contractsv1.ApprovalBrief{
		Kind: contractsv1.ApprovalBriefKindApprovalBrief, SchemaVersion: 1, Title: "Publish the reviewed change?",
		Evidence:            []contractsv1.ArtifactRef{{Id: resultReceipt.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "result", SchemaVersion: 1, Sha256: resultReceipt.ReceiptHash, MediaType: "application/json"}},
		Options:             []contractsv1.ApprovalOption{{Id: "approve", Label: "Approve", Decision: contractsv1.ApprovalOptionDecisionApprove, Tradeoffs: []string{"Publishes the exact reviewed artifact"}}, {Id: "reject", Label: "Reject", Decision: contractsv1.ApprovalOptionDecisionReject, Tradeoffs: []string{"Leaves production unchanged"}}},
		RecommendedOptionId: "approve", Recommendation: "Approve because the independent review passed.", Risks: []string{"The public page changes immediately"},
		Action: action, ApprovalPolicy: identifierPointer("human-confirm"),
	}
	partialLedger := workflow.NewMemoryLedger()
	for _, receipt := range source.Replay.Receipts[:len(source.Replay.Receipts)-1] {
		if err := partialLedger.Append(receipt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := authoringCoreWithLedger(t, partialLedger).PreviewApproval(brief, "human@example.com", source.Replay.AggregateId); err == nil || !strings.Contains(err.Error(), "node_completed") {
		t.Fatalf("result-only partial Replay became approvable: %v", err)
	}
	withoutPolicy := brief
	withoutPolicy.ApprovalPolicy = nil
	if _, err := core.PreviewApproval(withoutPolicy, "human@example.com", source.Replay.AggregateId); err == nil {
		t.Fatal("approval without a registered policy was accepted")
	}
	if _, err := core.PreviewApproval(brief, "other@example.com", source.Replay.AggregateId); err == nil {
		t.Fatal("unauthorized actor received an approval token")
	}
	preview, err := core.PreviewApproval(brief, "human@example.com", source.Replay.AggregateId)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ExpiresAt == nil {
		t.Fatal("approval preview has no canonical expiry")
	}
	if _, err := core.ConfirmApproval(preview, "human@example.com", "approve", preview.ExpiresAt.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired approval token was accepted: %v", err)
	}
	receipt, err := core.ConfirmApproval(preview, "human@example.com", "approve", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil || receipt.ReceiptType != contractsv1.ReceiptReceiptTypeApproval {
		t.Fatalf("approval failed: %+v %v", receipt, err)
	}
	redelivered, err := core.ConfirmApproval(preview, "human@example.com", "approve", preview.ExpiresAt.Add(time.Second))
	if err != nil || redelivered.ReceiptHash != receipt.ReceiptHash {
		t.Fatalf("exact approval redelivery did not converge after expiry: %+v %v", redelivered, err)
	}
	if _, err := core.ConfirmApproval(preview, "other@example.com", "approve", time.Now()); err == nil {
		t.Fatal("different actor reused approval token")
	}
	if _, err := core.PreviewApproval(brief, "human@example.com", "run-forged"); err == nil {
		t.Fatal("untrusted approval source was accepted")
	}
}

func identifierPointer(value contractsv1.Identifier) *contractsv1.Identifier { return &value }

type approvalProvider struct {
	invocation workflow.Invocation
	at         time.Time
}

func (p *approvalProvider) Start(_ context.Context, invocation workflow.Invocation) error {
	p.invocation = invocation
	return nil
}
func (p *approvalProvider) Poll(_ context.Context, key string) (workflow.ProviderResult, bool, error) {
	content := map[string]any{"recommendation": "Publish the reviewed change."}
	hash, err := workflow.Digest(content)
	if err != nil {
		return workflow.ProviderResult{}, false, err
	}
	artifact := contractsv1.ActionArtifact{Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "publish-action", ArtifactType: "recommendation", JobId: p.invocation.JobID, CampaignId: p.invocation.CampaignID, WorkflowRef: p.invocation.WorkflowRef, NodeId: p.invocation.Node.Id, InputHashes: p.invocation.InputHashes, Content: content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending}
	return workflow.ProviderResult{IdempotencyKey: key, CompletedAt: p.at, Artifacts: []contractsv1.ActionArtifact{artifact}}, true, nil
}
func (*approvalProvider) Cancel(context.Context, string) error { return nil }

func approvalSource(t *testing.T, ledger workflow.Ledger) workflow.RunResult {
	t.Helper()
	definition := authoringDefinition(t)
	job, campaign := testDefinitions(definition)
	cutoff := campaign.EvidenceFrontier.Cutoff
	content := map[string]any{"brief": "bounded"}
	hash, err := workflow.Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	zero := contractsv1.SHA256("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	pack := contractsv1.ContextPackEdition{Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1, Id: "project-brief", PackType: "project-brief", PackSchemaVersion: 1, Authority: contractsv1.ContextPackEditionAuthorityCanonical, Scope: campaign.Scope, CapturedAt: cutoff.Add(-time.Hour), ExpiresAt: cutoff.Add(time.Hour), Coverage: contractsv1.ContextPackEditionCoverageComplete, Content: content, ContentSha256: contractsv1.SHA256(hash), Provenance: []contractsv1.ArtifactRef{{Id: "seed", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "seed", SchemaVersion: 1, Sha256: zero, MediaType: "application/json"}}}
	registry, err := workflow.NewRegistry(workflow.NewIntentProducer(), workflow.NewCatalogProducer("project-brief", "project-brief", 1, pack))
	if err != nil {
		t.Fatal(err)
	}
	provider := &approvalProvider{at: cutoff.Add(time.Minute)}
	request := workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"}
	admit(t, ledger, registry, request)
	result, err := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, workflow.OutputCatalog{"recommendation@1": func(any) error { return nil }}, provider, ledger).RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func testDefinitions(definition contractsv1.WorkflowDefinition) (contractsv1.JobDefinition, contractsv1.CampaignDefinition) {
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"example-project"}}
	intent := func(kind contractsv1.IntentCardKind, title string) contractsv1.IntentCard {
		return contractsv1.IntentCard{SchemaVersion: 1, Kind: kind, Title: title, Summary: title, Objective: title, SuccessSignals: []string{"done"}, NonGoals: []string{"none"}, Completion: []string{"done"}, NoActionWhen: []string{"no change"}}
	}
	job := contractsv1.JobDefinition{Kind: contractsv1.JobDefinitionKindJobDefinition, SchemaVersion: 1, Id: "example-job", Intent: intent(contractsv1.IntentCardKindJob, "Example Job"), Scope: scope, Budget: contractsv1.Budget{MaxAttempts: 2, MaxActions: 1, MaxCandidates: 3}, CampaignArchetypes: []string{"research"}}
	campaign := contractsv1.CampaignDefinition{Kind: contractsv1.CampaignDefinitionKindCampaignDefinition, SchemaVersion: 1, Id: "example-campaign", JobId: job.Id, Archetype: "research", Intent: intent(contractsv1.IntentCardKindCampaign, "Example Campaign"), Scope: scope, EvidenceFrontier: contractsv1.EvidenceFrontier{Cutoff: time.Now().UTC().Add(-time.Hour), SourceHashes: []contractsv1.SHA256{}}, WorkflowPlan: []contractsv1.WorkflowRef{"research-review@1"}, Budget: job.Budget}
	return job, campaign
}

func authoringCore(t *testing.T) *workflow.AuthoringCore {
	return authoringCoreWithLedger(t, workflow.NewMemoryLedger())
}

func authoringCoreWithLedger(t *testing.T, ledger workflow.Ledger) *workflow.AuthoringCore {
	t.Helper()
	zero := contractsv1.SHA256("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"example-project"}}
	content := map[string]any{"brief": "bounded"}
	hash, err := workflow.Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	pack := contractsv1.ContextPackEdition{Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1, Id: "project-brief-edition", PackType: "project-brief", PackSchemaVersion: 1, Authority: contractsv1.ContextPackEditionAuthorityCanonical, Scope: scope, CapturedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour), Coverage: contractsv1.ContextPackEditionCoverageComplete, Content: content, ContentSha256: contractsv1.SHA256(hash), Provenance: []contractsv1.ArtifactRef{{Id: "seed", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "seed", SchemaVersion: 1, Sha256: zero, MediaType: "application/json"}}}
	registry, err := workflow.NewRegistry(workflow.NewIntentProducer(), workflow.NewCatalogProducer("project-brief", "project-brief", 1, pack))
	if err != nil {
		t.Fatal(err)
	}
	return workflow.NewAuthoringCore(registry,
		workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent, "human-approval@1": contractsv1.NodeDefinitionKindApproval},
		workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead},
		workflow.OutputCatalog{"recommendation@1": func(any) error { return nil }, "review-decision@1": func(any) error { return nil }},
		[]string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, ledger).
		WithApprovalAuthorities(workflow.ApprovalAuthorityCatalog{"human-confirm": []string{"human@example.com"}})
}

func authoringDefinition(t *testing.T) contractsv1.WorkflowDefinition {
	t.Helper()
	body, err := os.ReadFile("../examples/research-review.workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition contractsv1.WorkflowDefinition
	if err := json.Unmarshal(body, &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

func admit(t *testing.T, ledger workflow.Ledger, registry *workflow.Registry, request workflow.RunRequest) contractsv1.WorkflowAdmission {
	t.Helper()
	outputs := outputCatalog()
	outputs["review-decision@1"] = func(any) error { return nil }
	core := workflow.NewAuthoringCore(registry,
		workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent, "human-approval@1": contractsv1.NodeDefinitionKindApproval},
		workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputs,
		[]string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, ledger)
	preview, _, err := core.Preview(request.Job, request.Campaign, request.Workflow, "test-operator")
	if err != nil {
		t.Fatal(err)
	}
	admission, err := core.Confirm(preview, "test-operator", request.Campaign.EvidenceFrontier.Cutoff)
	if err != nil {
		t.Fatal(err)
	}
	return admission
}
