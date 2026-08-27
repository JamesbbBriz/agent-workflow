package builderapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/builderapi"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestBuilderHTTPDraftPreviewConfirmReadback(t *testing.T) {
	definition := loadDefinition(t)
	core := testCore(t)
	handler := builderapi.New(core, func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) })

	previewResponse := post(t, handler, "/v1/workflows/preview", map[string]any{"actor": "operator@example.com", "workflow": definition})
	var previewBody struct {
		OK   bool `json:"ok"`
		Data struct {
			Preview contractsv1.WorkflowAdmissionPreview `json:"preview"`
		} `json:"data"`
	}
	decodeBody(t, previewResponse, &previewBody)
	if !previewBody.OK || previewBody.Data.Preview.CommitToken == "" {
		t.Fatalf("preview failed: %s", previewResponse.Body.String())
	}

	job, campaign := definitions(definition)
	confirmResponse := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator@example.com", "preview": previewBody.Data.Preview, "job": job, "campaign": campaign})
	var confirmBody struct {
		OK   bool `json:"ok"`
		Data struct {
			Admission contractsv1.WorkflowAdmission `json:"admission"`
			Canvas    contractsv1.CanvasSnapshot    `json:"canvas"`
		} `json:"data"`
	}
	decodeBody(t, confirmResponse, &confirmBody)
	ref := string(definition.Id) + "@1"
	if !confirmBody.OK || confirmBody.Data.Admission.Revision != 1 || confirmBody.Data.Canvas.Definition.WorkflowStates[ref] != contractsv1.CanvasEntityStatusAdmitted || len(confirmBody.Data.Canvas.AdmissionReplays) != 1 {
		t.Fatalf("confirm did not return canonical Canvas readback: %s", confirmResponse.Body.String())
	}

	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/v1/workflows/research-review/versions/1", nil))
	if read.Code != http.StatusOK || !bytes.Contains(read.Body.Bytes(), []byte(`"revision":1`)) {
		t.Fatalf("readback failed: %s", read.Body.String())
	}
}

func TestBuilderHTTPRejectsUnknownInputAndStalePreview(t *testing.T) {
	core := testCore(t)
	handler := builderapi.New(core, time.Now)
	bad := postRaw(handler, "/v1/workflows/preview", []byte(`{"actor":"operator","workflow":{},"authority":"forged"}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown request field passed: %s", bad.Body.String())
	}
}

func post(t *testing.T, handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return postRaw(handler, path, body)
}

func postRaw(handler http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeBody(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v: %s", err, response.Body.String())
	}
}

func testCore(t *testing.T) *workflow.AuthoringCore {
	t.Helper()
	registry, err := workflow.NewRegistry(workflow.NewIntentProducer(), workflow.NewCatalogProducer("project-brief", "project-brief", 1))
	if err != nil {
		t.Fatal(err)
	}
	return workflow.NewAuthoringCore(registry,
		workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent, "human-approval@1": contractsv1.NodeDefinitionKindApproval},
		workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead},
		workflow.OutputCatalog{"recommendation@1": func(any) error { return nil }, "review-decision@1": func(any) error { return nil }},
		[]string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, workflow.NewMemoryLedger())
}

func loadDefinition(t *testing.T) contractsv1.WorkflowDefinition {
	t.Helper()
	body, err := os.ReadFile("../../examples/research-review.workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition contractsv1.WorkflowDefinition
	if err := json.Unmarshal(body, &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

func definitions(workflowDefinition contractsv1.WorkflowDefinition) (contractsv1.JobDefinition, contractsv1.CampaignDefinition) {
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"example-project"}}
	intent := func(kind contractsv1.IntentCardKind, title string) contractsv1.IntentCard {
		return contractsv1.IntentCard{SchemaVersion: 1, Kind: kind, Title: title, Summary: title, Objective: title, SuccessSignals: []string{"done"}, NonGoals: []string{"none"}, Completion: []string{"done"}, NoActionWhen: []string{"no change"}}
	}
	job := contractsv1.JobDefinition{Kind: contractsv1.JobDefinitionKindJobDefinition, SchemaVersion: 1, Id: "example-job", Intent: intent(contractsv1.IntentCardKindJob, "Example Job"), Scope: scope, Budget: contractsv1.Budget{MaxAttempts: 2, MaxActions: 1, MaxCandidates: 3}, CampaignArchetypes: []string{"research"}}
	campaign := contractsv1.CampaignDefinition{Kind: contractsv1.CampaignDefinitionKindCampaignDefinition, SchemaVersion: 1, Id: "example-campaign", JobId: job.Id, Archetype: "research", Intent: intent(contractsv1.IntentCardKindCampaign, "Example Campaign"), Scope: scope, EvidenceFrontier: contractsv1.EvidenceFrontier{Cutoff: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC), SourceHashes: []contractsv1.SHA256{}}, WorkflowPlan: []contractsv1.WorkflowRef{"research-review@1"}, Budget: job.Budget}
	return job, campaign
}
