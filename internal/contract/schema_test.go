package contract

import (
	"encoding/json"
	"testing"

	publiccontracts "github.com/JamesbbBriz/agent-workflow/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestPublicContractsFreezeWorkflowRefsAndContextOnlyBundles(t *testing.T) {
	t.Parallel()
	compiler := jsonschema.NewCompiler()
	var document any
	if err := json.Unmarshal(publiccontracts.AgentWorkflowV1, &document); err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(schemaID, document); err != nil {
		t.Fatal(err)
	}

	campaign, err := compiler.Compile(schemaID + "#/$defs/CampaignDefinition")
	if err != nil {
		t.Fatal(err)
	}
	campaignDocument := map[string]any{
		"kind": "campaign_definition", "schema_version": 1, "id": "campaign-a", "job_id": "job-a",
		"archetype": "research", "intent": intentFixture("campaign"), "scope": scopeFixture(),
		"evidence_frontier": map[string]any{"cutoff": "2026-08-28T00:00:00Z", "source_hashes": []any{}},
		"workflow_plan":     []any{"do research later"}, "budget": budgetFixture(),
	}
	if err := campaign.Validate(campaignDocument); err == nil {
		t.Fatal("campaign accepted a workflow plan without an immutable id@version reference")
	}
	campaignDocument["workflow_plan"] = []any{"research-review@1"}
	if err := campaign.Validate(campaignDocument); err != nil {
		t.Fatalf("campaign rejected a versioned workflow plan: %v", err)
	}

	bundle, err := compiler.Compile(schemaID + "#/$defs/ContextBundle")
	if err != nil {
		t.Fatal(err)
	}
	bundleDocument := map[string]any{
		"kind": "context_bundle", "schema_version": 1, "id": "bundle-a", "job_id": "job-a",
		"campaign_id": "campaign-a", "workflow_ref": "research-review@1", "node_id": "research",
		"evidence_cutoff": "2026-08-28T00:00:00Z", "degraded": false,
		"bundle_hash": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"entries": []any{map[string]any{
			"id": "action-a", "kind": "action_artifact", "artifact_type": "recommendation",
			"schema_version": 1, "sha256": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"media_type": "application/json",
		}},
	}
	if err := bundle.Validate(bundleDocument); err == nil {
		t.Fatal("context bundle accepted a decision artifact as context evidence")
	}
	bundleDocument["entries"].([]any)[0].(map[string]any)["kind"] = "context_pack"
	if err := bundle.Validate(bundleDocument); err != nil {
		t.Fatalf("context bundle rejected a context pack reference: %v", err)
	}
}

func TestV2CampaignReceiptPayloadsAreClosedAndTyped(t *testing.T) {
	t.Parallel()
	compiler := jsonschema.NewCompiler()
	var document any
	if err := json.Unmarshal(publiccontracts.AgentWorkflowV1, &document); err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(schemaID, document); err != nil {
		t.Fatal(err)
	}
	receipt, err := compiler.Compile(schemaID + "#/$defs/Receipt")
	if err != nil {
		t.Fatal(err)
	}
	sha := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	documentReceipt := map[string]any{
		"kind": "receipt", "schema_version": 2, "id": "receipt-a", "receipt_type": "attempt_reserved",
		"aggregate_id": "campaign-a", "aggregate_version": 2, "occurred_at": "2026-08-28T00:00:00Z",
		"input_hashes": []any{sha}, "output_hashes": []any{}, "previous_receipt_hash": sha, "receipt_hash": sha,
		"payload": map[string]any{},
	}
	if err := receipt.Validate(documentReceipt); err == nil {
		t.Fatal("v2 Campaign receipt accepted an untyped payload")
	}
	documentReceipt["payload"] = map[string]any{"workflow_ref": "research-review@1", "node_id": "research", "started_at": "2026-08-28T00:00:00Z"}
	if err := receipt.Validate(documentReceipt); err != nil {
		t.Fatalf("v2 Campaign receipt rejected its exact payload: %v", err)
	}
}

func intentFixture(kind string) map[string]any {
	return map[string]any{
		"schema_version": 1, "kind": kind, "title": "Title", "summary": "Summary", "objective": "Objective",
		"success_signals": []any{"done"}, "non_goals": []any{"none"}, "completion": []any{"done"}, "no_action_when": []any{"not needed"},
	}
}

func scopeFixture() map[string]any {
	return map[string]any{"subject_type": "project", "subject_ids": []any{"project-a"}}
}

func budgetFixture() map[string]any {
	return map[string]any{"max_attempts": 1, "max_actions": 0, "max_candidates": 1}
}
