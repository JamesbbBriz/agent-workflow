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
	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestBuilderHTTPDraftPreviewConfirmReadback(t *testing.T) {
	definition := loadDefinition(t)
	core := testCore(t)
	handler := builderapi.New(core, func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) })

	job, campaign := definitions(definition)
	previewResponse := post(t, handler, "/v1/workflows/preview", map[string]any{"actor": "operator@example.com", "job": job, "campaign": campaign, "workflow": definition})
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

	confirmResponse := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator@example.com", "preview": previewBody.Data.Preview})
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

func TestBuilderHTTPReturnsTypedControlPlaneReadModel(t *testing.T) {
	definition := loadDefinition(t)
	job, campaign := definitions(definition)
	handler := builderapi.New(testCore(t), func() time.Time { return time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC) })
	admissionPreview := preview(t, handler, job, campaign, definition)
	if response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": admissionPreview}); response.Code != http.StatusOK {
		t.Fatalf("confirm: %s", response.Body.String())
	}

	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/v1/control-plane", nil))
	var response struct {
		OK   bool                             `json:"ok"`
		Data contractsv1.ControlPlaneSnapshot `json:"data"`
	}
	decodeBody(t, read, &response)
	if read.Code != http.StatusOK || !response.OK || response.Data.Portfolio.Job.Id != job.Id || len(response.Data.Providers) != 5 {
		t.Fatalf("control-plane readback failed: %s", read.Body.String())
	}
	if err := contract.ValidateDefinition("ControlPlaneSnapshot", response.Data); err != nil {
		t.Fatalf("control-plane response is not canonical: %v", err)
	}
}

func TestBuilderHTTPKeepsTwoCampaignsInTheCanonicalPortfolio(t *testing.T) {
	definition := loadDefinition(t)
	job, firstCampaign := definitions(definition)
	job.Scope.SubjectIds = []string{"example-project", "second-project"}
	handler := builderapi.New(testCore(t), time.Now)

	firstPreview := preview(t, handler, job, firstCampaign, definition)
	if response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": firstPreview}); response.Code != http.StatusOK {
		t.Fatalf("confirm first Campaign: %s", response.Body.String())
	}
	reboundJob := job
	reboundJob.Budget.MaxAttempts++
	reboundDefinition := definition
	reboundDefinition.Id = "rebound-review"
	reboundDefinition.DefaultContext = reboundDefinition.DefaultContext[:1]
	reboundCampaign := firstCampaign
	reboundCampaign.Id = "rebound-campaign"
	reboundCampaign.WorkflowPlan = []contractsv1.WorkflowRef{"rebound-review@1"}
	reboundPreview := preview(t, handler, reboundJob, reboundCampaign, reboundDefinition)
	if response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": reboundPreview}); response.Code != http.StatusConflict {
		t.Fatalf("same Job ID was rebound before commit: %s", response.Body.String())
	}
	readRejected := httptest.NewRecorder()
	handler.ServeHTTP(readRejected, httptest.NewRequest(http.MethodGet, "/v1/workflows/rebound-review/versions/1", nil))
	if readRejected.Code != http.StatusConflict {
		t.Fatalf("rejected Job rebind still persisted: %s", readRejected.Body.String())
	}

	secondDefinition := definition
	secondDefinition.Id = "second-review"
	secondDefinition.DefaultContext = secondDefinition.DefaultContext[:1]
	secondCampaign := firstCampaign
	secondCampaign.Id = "second-campaign"
	secondCampaign.Scope.SubjectIds = []string{"second-project"}
	secondCampaign.Budget.MaxAttempts = 7
	secondCampaign.WorkflowPlan = []contractsv1.WorkflowRef{"second-review@1"}
	secondPreview := preview(t, handler, job, secondCampaign, secondDefinition)
	if response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": secondPreview}); response.Code != http.StatusOK {
		t.Fatalf("confirm second Campaign: %s", response.Body.String())
	}
	secondDefinition.Version = 2
	secondCampaign.WorkflowPlan = []contractsv1.WorkflowRef{"second-review@2"}
	secondPreview = preview(t, handler, job, secondCampaign, secondDefinition)
	if response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": secondPreview}); response.Code != http.StatusConflict {
		t.Fatalf("admitted Campaign changed its pinned Workflow: %s", response.Body.String())
	}

	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/v2/canvas", nil))
	var response struct {
		Data contractsv1.CanvasPortfolioSnapshot `json:"data"`
	}
	decodeBody(t, read, &response)
	if read.Code != http.StatusOK || response.Data.SelectedCampaignId != secondCampaign.Id || len(response.Data.Campaigns) != 2 {
		t.Fatalf("multi-Campaign readback failed: %s", read.Body.String())
	}
	if response.Data.Campaigns[0].Canvas.Definition.Campaign.Id != firstCampaign.Id || response.Data.Campaigns[1].Canvas.Definition.Campaign.WorkflowPlan[0] != "second-review@1" {
		t.Fatalf("Campaign definitions were merged or invented: %+v", response.Data.Campaigns)
	}
	for _, item := range response.Data.Campaigns {
		if item.State != contractsv1.CanvasEntityStatusAdmitted || item.Canvas.Definition.CampaignState != contractsv1.CanvasEntityStatusConfigured {
			t.Fatalf("v2 state=%q rewrote v1 state=%q", item.State, item.Canvas.Definition.CampaignState)
		}
	}
}

func TestBuilderHTTPAdmissionReadbackKeepsTrustedRuntime(t *testing.T) {
	body, err := os.ReadFile("../../web/public/canvas.response.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	definition := loadDefinition(t)
	job, campaign := envelope.Data.Definition.Job, envelope.Data.Definition.Campaign
	handler := builderapi.NewWithCanvas(testCore(t), time.Now, &envelope.Data)
	admissionPreview := preview(t, handler, job, campaign, definition)
	if response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": admissionPreview}); response.Code != http.StatusOK {
		t.Fatalf("confirm: %s", response.Body.String())
	}
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/v1/canvas", nil))
	var response struct {
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	decodeBody(t, read, &response)
	if len(response.Data.Executions) != len(envelope.Data.Executions) || len(response.Data.Replays) != len(envelope.Data.Replays) {
		t.Fatalf("admission replaced trusted runtime readback: %s", read.Body.String())
	}
}

func TestBuilderHTTPRejectsJobRebindingAcrossDefinitions(t *testing.T) {
	body, err := os.ReadFile("../../web/public/canvas.response.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	definition := loadDefinition(t)
	definition.Id = "rebound-review"
	job, campaign := envelope.Data.Definition.Job, envelope.Data.Definition.Campaign
	job.Intent.Title = "Rebound job title"
	campaign.WorkflowPlan = []contractsv1.WorkflowRef{"research-review@1", "rebound-review@1"}
	handler := builderapi.NewWithCanvas(testCore(t), time.Now, &envelope.Data)
	admissionPreview := preview(t, handler, job, campaign, definition)
	if response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": admissionPreview}); response.Code != http.StatusConflict {
		t.Fatalf("Job rebind was accepted: %s", response.Body.String())
	}
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/v1/canvas", nil))
	var response struct {
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	decodeBody(t, read, &response)
	if len(response.Data.Executions) != len(envelope.Data.Executions) || response.Data.Definition.Job.Intent.Title == "Rebound job title" {
		t.Fatalf("rejected Job rebind changed historical runtime: %s", read.Body.String())
	}
	if _, ok := response.Data.Definition.WorkflowStates["rebound-review@1"]; ok || len(response.Data.AdmissionReplays) != 1 {
		t.Fatalf("historical admission was rebound to changed definitions: %s", read.Body.String())
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

func TestBuilderHTTPBindsTargetBeforeAdmission(t *testing.T) {
	definition := loadDefinition(t)
	job, campaign := definitions(definition)
	core := testCore(t)
	handler := builderapi.New(core, time.Now)
	definition.Version = 2
	campaign.WorkflowPlan = []contractsv1.WorkflowRef{"research-review@1"}
	response := post(t, handler, "/v1/workflows/preview", map[string]any{"actor": "operator", "job": job, "campaign": campaign, "workflow": definition})
	if response.Code != http.StatusConflict {
		t.Fatalf("unpinned version received an admission preview: %s", response.Body.String())
	}
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/v1/workflows/research-review/versions/1", nil))
	if read.Code == http.StatusOK {
		t.Fatalf("failed preview mutated admission history: %s", read.Body.String())
	}
}

func TestBuilderHTTPRejectsWorkflowVersionChangeInsideAdmittedCampaign(t *testing.T) {
	definition := loadDefinition(t)
	job, campaign := definitions(definition)
	handler := builderapi.New(testCore(t), time.Now)

	previewV1 := preview(t, handler, job, campaign, definition)
	if response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": previewV1}); response.Code != http.StatusOK {
		t.Fatalf("confirm v1: %s", response.Body.String())
	}

	definition.Version = 2
	campaign.WorkflowPlan = []contractsv1.WorkflowRef{"research-review@2"}
	previewV2 := preview(t, handler, job, campaign, definition)
	response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": previewV2})
	if response.Code != http.StatusConflict {
		t.Fatalf("admitted Campaign changed its pinned Workflow version: %s", response.Body.String())
	}
}

func TestBuilderHTTPRejectsIncompleteMultiWorkflowCanvasBeforeCommit(t *testing.T) {
	definition := loadDefinition(t)
	job, campaign := definitions(definition)
	campaign.WorkflowPlan = append(campaign.WorkflowPlan, "unrelated-workflow@1")
	core := testCore(t)
	handler := builderapi.New(core, time.Now)
	preview := preview(t, handler, job, campaign, definition)
	response := post(t, handler, "/v1/workflows/confirm", map[string]any{"actor": "operator", "preview": preview})
	if response.Code != http.StatusConflict {
		t.Fatalf("incomplete Canvas plan passed: %s", response.Body.String())
	}
	if _, err := core.ReadWorkflow(string(definition.Id), 1); err == nil {
		t.Fatal("admission was committed before Canvas preflight")
	}
}

func TestBuilderHTTPRejectsOversizedBodies(t *testing.T) {
	core := testCore(t)
	handler := builderapi.New(core, time.Now)
	body := append([]byte(`{}`), bytes.Repeat([]byte(" "), 3<<20)...)
	response := postRaw(handler, "/v1/workflows/lint", body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized request passed: %s", response.Body.String())
	}
}

func TestBuilderHTTPValidatesApprovalCanvasBeforeCommit(t *testing.T) {
	var envelope struct {
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	body, err := os.ReadFile("../../web/public/canvas.response.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	snapshot := envelope.Data
	source := snapshot.Replays[0]
	sources := workflow.NewMemoryLedger()
	for _, receipt := range source.Receipts {
		if err := sources.Append(receipt); err != nil {
			t.Fatal(err)
		}
	}
	var result contractsv1.Receipt
	for _, receipt := range source.Receipts {
		if receipt.ReceiptType == contractsv1.ReceiptReceiptTypeResult {
			result = receipt
		}
	}
	policy := contractsv1.Identifier("human-confirm")
	brief := contractsv1.ApprovalBrief{Kind: contractsv1.ApprovalBriefKindApprovalBrief, SchemaVersion: 1, Title: "Approve exact action?", Action: snapshot.Executions[0].Outputs[0], Evidence: []contractsv1.ArtifactRef{{Id: result.Id, Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "result", SchemaVersion: 1, Sha256: result.ReceiptHash, MediaType: "application/json"}}, Options: []contractsv1.ApprovalOption{{Id: "approve", Label: "Approve", Decision: contractsv1.ApprovalOptionDecisionApprove, Tradeoffs: []string{"Changes the target"}}, {Id: "reject", Label: "Reject", Decision: contractsv1.ApprovalOptionDecisionReject, Tradeoffs: []string{"No change"}}}, RecommendedOptionId: "approve", Recommendation: "Approve the reviewed action.", Risks: []string{"Public impact"}, ApprovalPolicy: &policy}
	core := testCoreWithSources(t, sources)
	invalid := snapshot
	invalid.Executions = []contractsv1.CanvasExecution{}
	handler := builderapi.NewWithCanvas(core, time.Now, &invalid)
	if response := post(t, handler, "/v1/approvals/preview", map[string]any{"actor": "forged@example.com", "brief": brief, "source_aggregate_id": source.AggregateId}); response.Code != http.StatusBadRequest {
		t.Fatalf("caller supplied approval actor was accepted: %s", response.Body.String())
	}
	previewResponse := post(t, handler, "/v1/approvals/preview", map[string]any{"brief": brief, "source_aggregate_id": source.AggregateId})
	var previewBody struct {
		Data contractsv1.ApprovalPreview `json:"data"`
	}
	decodeBody(t, previewResponse, &previewBody)
	response := post(t, handler, "/v1/approvals/confirm", map[string]any{"option_id": "approve", "preview": previewBody.Data})
	if response.Code != http.StatusConflict {
		t.Fatalf("invalid Canvas approval passed: %s", response.Body.String())
	}
	if _, err := core.ApprovalReplay(string(previewBody.Data.Brief.Id)); err == nil {
		t.Fatal("approval was committed before Canvas validation")
	}
	second := snapshot
	second.Definition.Campaign.Id = "second-campaign"
	second.Definition.Campaign.WorkflowPlan = []contractsv1.WorkflowRef{"research-review@1"}
	second.Executions = []contractsv1.CanvasExecution{}
	second.Replays = []contractsv1.ReplayBundle{}
	second.ApprovalReplays = []contractsv1.ReplayBundle{}
	portfolio := contractsv1.CanvasPortfolioSnapshot{
		Kind: contractsv1.CanvasPortfolioSnapshotKindCanvasPortfolioSnapshot, SchemaVersion: 2,
		Job: snapshot.Definition.Job, SelectedCampaignId: second.Definition.Campaign.Id,
		Campaigns: []contractsv1.CanvasPortfolioCampaign{
			{CampaignId: snapshot.Definition.Campaign.Id, State: contractsv1.CanvasEntityStatusAwaitingHuman, Canvas: snapshot},
			{CampaignId: second.Definition.Campaign.Id, State: contractsv1.CanvasEntityStatusConfigured, Canvas: second},
		},
	}
	handler = builderapi.NewWithPortfolio(core, time.Now, &portfolio)
	request := map[string]any{"option_id": "approve", "preview": previewBody.Data}
	if response := post(t, handler, "/v1/approvals/confirm", request); response.Code != http.StatusOK {
		t.Fatalf("valid approval failed: %s", response.Body.String())
	}
	if response := post(t, handler, "/v1/approvals/confirm", request); response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"approval_state":"approved"`)) {
		t.Fatalf("exact approval retry did not converge: %s", response.Body.String())
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

func preview(t *testing.T, handler http.Handler, job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition, definition contractsv1.WorkflowDefinition) contractsv1.WorkflowAdmissionPreview {
	t.Helper()
	response := post(t, handler, "/v1/workflows/preview", map[string]any{"actor": "operator", "job": job, "campaign": campaign, "workflow": definition})
	var body struct {
		Data struct {
			Preview contractsv1.WorkflowAdmissionPreview `json:"preview"`
		} `json:"data"`
	}
	decodeBody(t, response, &body)
	if response.Code != http.StatusOK {
		t.Fatalf("preview: %s", response.Body.String())
	}
	return body.Data.Preview
}

func testCore(t *testing.T) *workflow.AuthoringCore {
	return testCoreWithSources(t, workflow.NewMemoryLedger())
}

func testCoreWithSources(t *testing.T, sources workflow.Ledger) *workflow.AuthoringCore {
	t.Helper()
	cutoff := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"example-project"}}
	content := map[string]any{"brief": "bounded"}
	hash, err := workflow.Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	pack := contractsv1.ContextPackEdition{Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1, Id: "project-brief-edition", PackType: "project-brief", PackSchemaVersion: 1, Authority: contractsv1.ContextPackEditionAuthorityCanonical, Scope: scope, CapturedAt: cutoff.Add(-48 * time.Hour), ExpiresAt: cutoff.Add(24 * time.Hour), Coverage: contractsv1.ContextPackEditionCoverageComplete, Content: content, ContentSha256: contractsv1.SHA256(hash), Provenance: []contractsv1.ArtifactRef{{Id: "seed", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "seed", SchemaVersion: 1, Sha256: "sha256:0000000000000000000000000000000000000000000000000000000000000000", MediaType: "application/json"}}}
	registry, err := workflow.NewRegistry(workflow.NewIntentProducer(), workflow.NewCatalogProducer("project-brief", "project-brief", 1, pack))
	if err != nil {
		t.Fatal(err)
	}
	return workflow.NewAuthoringCoreWithSources(registry,
		workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent, "human-approval@1": contractsv1.NodeDefinitionKindApproval},
		workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead},
		workflow.OutputCatalog{"recommendation@1": func(any) error { return nil }, "review-decision@1": func(any) error { return nil }},
		[]string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, workflow.NewMemoryLedger(), sources).
		WithApprovalAuthorities(workflow.ApprovalAuthorityCatalog{"human-confirm": []string{"local-operator"}})
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
