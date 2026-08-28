package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func main() {
	if err := os.MkdirAll("conformance/fixtures", 0o755); err != nil {
		panic(err)
	}
	for name, fixture := range map[string]contractsv1.ConformanceFixture{"generic": fixture(false), "seo-shaped": fixture(true)} {
		body, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			panic(err)
		}
		body = append(body, '\n')
		if err := os.WriteFile(filepath.Join("conformance/fixtures", name+".json"), body, 0o644); err != nil {
			panic(err)
		}
	}
}

func fixture(seo bool) contractsv1.ConformanceFixture {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	profile, subject := contractsv1.ConformanceFixtureProfileGeneric, "generic-project"
	if seo {
		profile, subject = contractsv1.ConformanceFixtureProfileSeoShaped, "seo-example-project"
	}
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{subject}, Labels: map[string]string{"tenant": "fixture"}}
	job := contractsv1.JobDefinition{Kind: contractsv1.JobDefinitionKindJobDefinition, SchemaVersion: 1, Id: contractsv1.Identifier(subject + "-job"), Intent: intent(contractsv1.IntentCardKindJob, "Fixture growth job"), Scope: scope, Budget: contractsv1.Budget{MaxAttempts: 8, MaxActions: 8, MaxCandidates: 12}, CampaignArchetypes: []string{"research", "comparison"}}
	research := researchWorkflow("discover-review", true)
	workflows := []contractsv1.WorkflowDefinition{research}
	campaigns := []contractsv1.CampaignDefinition{campaign(job, "research-campaign", "research", cutoff, []contractsv1.WorkflowRef{"discover-review@1"}, 3)}
	if seo {
		resolver := researchWorkflow("resolver", false)
		comparison := researchWorkflow("comparison", false)
		workflows = append(workflows, resolver, comparison)
		campaigns[0].WorkflowPlan = []contractsv1.WorkflowRef{"discover-review@1", "resolver@1"}
		campaigns[0].Budget = contractsv1.Budget{MaxAttempts: 4, MaxActions: 4, MaxCandidates: 6}
		campaigns = append(campaigns, campaign(job, "comparison-campaign", "comparison", cutoff, []contractsv1.WorkflowRef{"comparison@1"}, 2))
	}
	return contractsv1.ConformanceFixture{
		Kind: contractsv1.ConformanceFixtureKindConformanceFixture, SchemaVersion: 1, Profile: profile,
		Job: job, Campaigns: campaigns, Workflows: workflows, ContextPacks: []contractsv1.ContextPackEdition{contextPack(scope, cutoff)},
		CapabilityManifests: []contractsv1.CapabilityManifest{capabilityManifest()},
		BlockerCodes:        []string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, ApprovalPolicies: []string{"human-confirm"},
	}
}

func campaign(job contractsv1.JobDefinition, id, archetype string, cutoff time.Time, plan []contractsv1.WorkflowRef, attempts int) contractsv1.CampaignDefinition {
	return contractsv1.CampaignDefinition{
		Kind: contractsv1.CampaignDefinitionKindCampaignDefinition, SchemaVersion: 1, Id: contractsv1.Identifier(id), JobId: job.Id, Archetype: contractsv1.Identifier(archetype),
		Intent: intent(contractsv1.IntentCardKindCampaign, fmt.Sprintf("Fixture %s Campaign", archetype)), Scope: job.Scope,
		EvidenceFrontier: contractsv1.EvidenceFrontier{Cutoff: cutoff, SourceHashes: []contractsv1.SHA256{}}, WorkflowPlan: plan,
		Budget: contractsv1.Budget{MaxAttempts: attempts, MaxActions: attempts, MaxCandidates: attempts * 2},
	}
}

func researchWorkflow(id string, approval bool) contractsv1.WorkflowDefinition {
	contextRequirement := contractsv1.ContextRequirement{Id: "project-brief", Selector: "project-brief", PackType: "project-brief", SchemaVersion: 1, Required: true}
	recommendation := slot("recommendation", "recommendation", "recommendation@1")
	deadline := 600
	agent := contractsv1.NodeDefinition{Id: "research", Kind: contractsv1.NodeDefinitionKindAgent, Executor: "bounded-agent@1", DependsOn: []string{}, Context: []contractsv1.ContextRequirement{}, Capabilities: []string{"read-evidence"}, InputSlots: []contractsv1.Slot{}, OutputSlots: []contractsv1.Slot{recommendation}, Budget: contractsv1.Budget{MaxAttempts: 2, MaxActions: 1, MaxCandidates: 2}, DeadlineSeconds: &deadline, BlockerCodes: []string{"context-missing", "provider-timeout"}}
	definition := contractsv1.WorkflowDefinition{Kind: contractsv1.WorkflowDefinitionKindWorkflowDefinition, SchemaVersion: 1, Id: contractsv1.Identifier(id), Version: 1, Intent: intent(contractsv1.IntentCardKindWorkflow, "Fixture "+id), DefaultContext: []contractsv1.ContextRequirement{contextRequirement}, Nodes: []contractsv1.NodeDefinition{agent}, Outputs: []contractsv1.Slot{recommendation}, Completion: []string{"research completed"}, Blockers: []string{"context-missing", "provider-timeout"}}
	if !approval {
		definition.Outputs[0].Consumers = []string{"fixture-consumer"}
		return definition
	}
	policy := contractsv1.Identifier("human-confirm")
	reviewDecision := slot("review-decision", "review-decision", "review-decision@1")
	definition.Nodes[0].OutputSlots[0].Consumers = []string{"review"}
	definition.Nodes = append(definition.Nodes, contractsv1.NodeDefinition{Id: "review", Kind: contractsv1.NodeDefinitionKindApproval, Executor: "human-approval@1", DependsOn: []string{"research"}, Context: []contractsv1.ContextRequirement{}, Capabilities: []string{}, InputSlots: []contractsv1.Slot{recommendation}, OutputSlots: []contractsv1.Slot{reviewDecision}, Budget: contractsv1.Budget{MaxAttempts: 1, MaxActions: 1, MaxCandidates: 1}, DeadlineSeconds: &deadline, ApprovalPolicy: &policy, BlockerCodes: []string{"approval-required", "approval-stale"}})
	definition.Outputs = []contractsv1.Slot{reviewDecision}
	definition.Outputs[0].Consumers = []string{"fixture-consumer"}
	definition.Completion = []string{"review approved"}
	definition.Blockers = append(definition.Blockers, "approval-required", "approval-stale")
	return definition
}

func slot(id, artifactType, schema string) contractsv1.Slot {
	kind := contractsv1.SlotArtifactKindActionArtifact
	contentSchema := contractsv1.WorkflowRef(schema)
	return contractsv1.Slot{Id: contractsv1.Identifier(id), ArtifactType: contractsv1.Identifier(artifactType), ArtifactKind: &kind, ContentSchema: &contentSchema, MinItems: 1, MaxItems: 1}
}

func intent(kind contractsv1.IntentCardKind, title string) contractsv1.IntentCard {
	return contractsv1.IntentCard{SchemaVersion: 1, Kind: kind, Title: title, Summary: title, Objective: title, SuccessSignals: []string{"fixture completes"}, NonGoals: []string{"production mutation"}, Completion: []string{"terminal receipt exists"}, NoActionWhen: []string{"fixture has no work"}, Consumers: []string{"fixture-consumer"}}
}

func contextPack(scope contractsv1.Scope, cutoff time.Time) contractsv1.ContextPackEdition {
	content := map[string]any{"brief": "Use only deterministic fixture evidence."}
	hash, err := workflow.Digest(content)
	if err != nil {
		panic(err)
	}
	zero := contractsv1.SHA256("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	return contractsv1.ContextPackEdition{
		Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1, Id: "fixture-project-brief", PackType: "project-brief", PackSchemaVersion: 1,
		Authority: contractsv1.ContextPackEditionAuthorityCanonical, Scope: scope, CapturedAt: cutoff.Add(-time.Hour), ExpiresAt: cutoff.Add(24 * time.Hour), Coverage: contractsv1.ContextPackEditionCoverageComplete,
		Content: content, ContentSha256: contractsv1.SHA256(hash), Provenance: []contractsv1.ArtifactRef{{Id: "fixture-source", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "fixture", SchemaVersion: 1, Sha256: zero, MediaType: "application/json"}},
	}
}

func capabilityManifest() contractsv1.CapabilityManifest {
	capabilities := []contractsv1.CapabilityManifestCapabilitiesElem{{Name: "read-evidence", Authority: contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}}
	identity, err := workflow.Digest(capabilities)
	if err != nil {
		panic(err)
	}
	manifest := contractsv1.CapabilityManifest{Kind: contractsv1.CapabilityManifestKindCapabilityManifest, SchemaVersion: 1, Id: "capability-" + identity[len("sha256:"):len("sha256:")+20], Capabilities: capabilities}
	hash, err := workflow.Digest(manifest)
	if err != nil {
		panic(err)
	}
	manifest.ManifestHash = contractsv1.SHA256(hash)
	return manifest
}
