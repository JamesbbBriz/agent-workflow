package builderapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/builderapi"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

const testPageOrigin = "http://127.0.0.1:5173"

func TestWebMCPExactPreviewConfirmAndAudit(t *testing.T) {
	snapshot := loadCanvas(t)
	var audit bytes.Buffer
	handler, err := builderapi.NewWithWebMCP(testCore(t), time.Now, &snapshot, builderapi.WebMCPConfig{PageOrigin: testPageOrigin, Audit: &audit, RateLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	token := webMCPSession(t, handler, testPageOrigin)
	actor := "local-operator"
	definition := loadDefinition(t)
	previewResponse := webMCPRequest(t, handler, token, actor, "preview_workflow_admission", "/v1/workflows/preview", map[string]any{
		"actor": actor, "job": snapshot.Definition.Job, "campaign": snapshot.Definition.Campaign, "workflow": definition,
	})
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview: %s", previewResponse.Body.String())
	}
	var previewEnvelope struct {
		Data struct {
			Preview contractsv1.WorkflowAdmissionPreview `json:"preview"`
		} `json:"data"`
	}
	decodeBody(t, previewResponse, &previewEnvelope)
	confirmResponse := webMCPRequest(t, handler, token, actor, "confirm_authorized_action", "/v1/workflows/confirm", map[string]any{"actor": actor, "preview": previewEnvelope.Data.Preview})
	if confirmResponse.Code != http.StatusOK {
		t.Fatalf("confirm: %s", confirmResponse.Body.String())
	}
	var confirmEnvelope struct {
		Data struct {
			Admission contractsv1.WorkflowAdmission `json:"admission"`
		} `json:"data"`
	}
	decodeBody(t, confirmResponse, &confirmEnvelope)
	if confirmEnvelope.Data.Admission.Receipt.ReceiptHash == "" {
		t.Fatal("confirmation did not return a canonical receipt")
	}
	records := auditRecords(t, audit.Bytes())
	if len(records) != 6 {
		t.Fatalf("audit records = %d, want 6", len(records))
	}
	authorized := records[len(records)-2]
	if authorized["phase"] != "authorized" || authorized["preview_identity"] == "" || authorized["confirmation_identity"] == "" {
		t.Fatalf("confirmation was not durably authorized before mutation: %+v", authorized)
	}
	completed := records[len(records)-1]
	if completed["phase"] != "completed" || completed["outcome"] != "succeeded" || completed["tool"] != "confirm_authorized_action" || completed["request_id"] == "" || completed["subject"] != actor || completed["page_origin"] != testPageOrigin || completed["preview_identity"] == "" || completed["confirmation_identity"] == "" || completed["canonical_receipt_ref"] != string(confirmEnvelope.Data.Admission.Receipt.ReceiptHash) {
		t.Fatalf("incomplete confirmation audit: %+v", completed)
	}
}

func TestWebMCPDeniesOriginAuthenticationRouteHashAndStaleConfirm(t *testing.T) {
	snapshot := loadCanvas(t)
	var audit bytes.Buffer
	handler, err := builderapi.NewWithWebMCP(testCore(t), time.Now, &snapshot, builderapi.WebMCPConfig{PageOrigin: testPageOrigin, Audit: &audit, RateLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if response := webMCPSessionResponse(handler, "https://evil.example"); response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin session status = %d", response.Code)
	}
	token := webMCPSession(t, handler, testPageOrigin)
	body := map[string]any{"actor": "local-operator", "job": snapshot.Definition.Job, "campaign": snapshot.Definition.Campaign, "workflow": loadDefinition(t)}
	if response := webMCPRequestWith(t, handler, "", "local-operator", "preview_workflow_admission", "/v1/workflows/preview", body, testPageOrigin, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d", response.Code)
	}
	if response := webMCPRequestWith(t, handler, token, "impersonated-operator", "preview_workflow_admission", "/v1/workflows/preview", body, testPageOrigin, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("subject impersonation status = %d", response.Code)
	}
	if audit.Len() != 0 {
		t.Fatal("unauthenticated headers entered the audit log")
	}
	if response := webMCPRequestWith(t, handler, token, "local-operator", "inspect_current_canvas", "/v1/workflows/preview", body, testPageOrigin, ""); response.Code != http.StatusForbidden {
		t.Fatalf("route widening status = %d", response.Code)
	}
	if response := webMCPRequestWith(t, handler, token, "local-operator", "preview_workflow_admission", "/v1/workflows/preview", body, testPageOrigin, "sha256:"+string(bytes.Repeat([]byte{'0'}, 64))); response.Code != http.StatusBadRequest {
		t.Fatalf("hash mismatch status = %d", response.Code)
	}
	read := httptest.NewRequest(http.MethodGet, "/v1/canvas", nil)
	setWebMCPHeaders(read, token, "local-operator", "inspect_current_canvas", testPageOrigin, testDigest([]byte(`{"different":true}`)))
	read.Header.Set("X-Agent-Workflow-Input", `{}`)
	readResponse := httptest.NewRecorder()
	handler.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusBadRequest {
		t.Fatalf("read hash mismatch status = %d", readResponse.Code)
	}
	previewResponse := webMCPRequest(t, handler, token, "local-operator", "preview_workflow_admission", "/v1/workflows/preview", body)
	var previewEnvelope struct {
		Data struct {
			Preview contractsv1.WorkflowAdmissionPreview `json:"preview"`
		} `json:"data"`
	}
	decodeBody(t, previewResponse, &previewEnvelope)
	previewEnvelope.Data.Preview.CommitToken = contractsv1.SHA256("sha256:" + string(bytes.Repeat([]byte{'a'}, 64)))
	stale := webMCPRequest(t, handler, token, "local-operator", "confirm_authorized_action", "/v1/workflows/confirm", map[string]any{"actor": "local-operator", "preview": previewEnvelope.Data.Preview})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale confirm status = %d: %s", stale.Code, stale.Body.String())
	}
}

func TestWebMCPRateLimitIsPerSubject(t *testing.T) {
	snapshot := loadCanvas(t)
	var audit bytes.Buffer
	handler, err := builderapi.NewWithWebMCP(testCore(t), func() time.Time { return time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC) }, &snapshot, builderapi.WebMCPConfig{PageOrigin: testPageOrigin, Audit: &audit, RateLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	token := webMCPSession(t, handler, testPageOrigin)
	if response := webMCPGet(t, handler, token, "local-operator", "inspect_current_canvas", map[string]any{}); response.Code != http.StatusOK {
		t.Fatalf("first read status = %d", response.Code)
	}
	response := webMCPGet(t, handler, token, "local-operator", "explain_context_blockers", map[string]any{"node_id": "research"})
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "60" {
		t.Fatalf("rate limit status = %d retry=%q", response.Code, response.Header().Get("Retry-After"))
	}
}

func TestWebMCPRateLimitPrecedesBodyReadAndCoalescesDenialAudit(t *testing.T) {
	snapshot := loadCanvas(t)
	var audit bytes.Buffer
	handler, err := builderapi.NewWithWebMCP(testCore(t), func() time.Time { return time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC) }, &snapshot, builderapi.WebMCPConfig{PageOrigin: testPageOrigin, Audit: &audit, RateLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	token := webMCPSession(t, handler, testPageOrigin)
	body := map[string]any{"actor": "local-operator", "job": snapshot.Definition.Job, "campaign": snapshot.Definition.Campaign, "workflow": loadDefinition(t)}
	badHash := "sha256:" + string(bytes.Repeat([]byte{'0'}, 64))
	first := webMCPRequestWith(t, handler, token, "local-operator", "preview_workflow_admission", "/v1/workflows/preview", body, testPageOrigin, badHash)
	if first.Code != http.StatusBadRequest {
		t.Fatalf("first invalid request status = %d", first.Code)
	}
	before := audit.Len()
	second := webMCPRequestWith(t, handler, token, "local-operator", "preview_workflow_admission", "/v1/workflows/preview", body, testPageOrigin, badHash)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "60" {
		t.Fatalf("second invalid request status = %d retry=%q", second.Code, second.Header().Get("Retry-After"))
	}
	if audit.Len() <= before {
		t.Fatal("first rate-limited request was not audited")
	}
	records := auditRecords(t, audit.Bytes())
	denial := records[len(records)-1]
	if denial["phase"] != "completed" || denial["outcome"] != "rate_limited" || denial["http_status"] != float64(http.StatusTooManyRequests) || denial["request_id"] != "123e4567-e89b-42d3-a456-426614174000" || denial["subject"] != "local-operator" || denial["page_origin"] != testPageOrigin || denial["tool"] != "preview_workflow_admission" || denial["inputs_sha256"] != badHash {
		t.Fatalf("incomplete rate-limit audit: %+v", denial)
	}
	before = audit.Len()
	third := webMCPRequestWith(t, handler, token, "local-operator", "preview_workflow_admission", "/v1/workflows/preview", body, testPageOrigin, badHash)
	if third.Code != http.StatusTooManyRequests {
		t.Fatalf("third invalid request status = %d", third.Code)
	}
	if audit.Len() != before {
		t.Fatal("repeated rate-limited request grew the audit log")
	}
}

func TestWebMCPRateLimitAuditUsesTheAdmissionWindowAtMinuteRollover(t *testing.T) {
	snapshot := loadCanvas(t)
	base := time.Date(2026, 8, 28, 0, 0, 59, 0, time.UTC)
	calls := 0
	now := func() time.Time {
		calls++
		if calls <= 4 {
			return base
		}
		if calls <= 6 {
			return base.Add(500 * time.Millisecond)
		}
		return base.Add(time.Second)
	}
	var audit bytes.Buffer
	handler, err := builderapi.NewWithWebMCP(testCore(t), now, &snapshot, builderapi.WebMCPConfig{PageOrigin: testPageOrigin, Audit: &audit, RateLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	token := webMCPSession(t, handler, testPageOrigin)
	if response := webMCPGet(t, handler, token, "local-operator", "inspect_current_canvas", map[string]any{}); response.Code != http.StatusOK {
		t.Fatalf("first request status = %d", response.Code)
	}
	if response := webMCPGet(t, handler, token, "local-operator", "inspect_current_canvas", map[string]any{}); response.Code != http.StatusTooManyRequests {
		t.Fatalf("rollover request status = %d", response.Code)
	}
	records := auditRecords(t, audit.Bytes())
	if records[len(records)-1]["outcome"] != "rate_limited" {
		t.Fatalf("minute-boundary denial was not audited: %+v", records)
	}
}

func TestWebMCPConfigurationFailsClosed(t *testing.T) {
	if _, err := builderapi.NewWithWebMCP(testCore(t), time.Now, nil, builderapi.WebMCPConfig{PageOrigin: testPageOrigin}); err == nil {
		t.Fatal("missing audit writer was accepted")
	}
	if _, err := builderapi.NewWithWebMCP(testCore(t), time.Now, nil, builderapi.WebMCPConfig{PageOrigin: "https://example.com/path", Audit: &bytes.Buffer{}}); err == nil {
		t.Fatal("non-origin page URL was accepted")
	}
}

func TestWebMCPConfirmationSurvivesExternalCompletionAuditFailureThroughCanonicalReceipt(t *testing.T) {
	snapshot := loadCanvas(t)
	audit := &failWrite{failAt: 6}
	handler, err := builderapi.NewWithWebMCP(testCore(t), time.Now, &snapshot, builderapi.WebMCPConfig{PageOrigin: testPageOrigin, Audit: audit, RateLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	token := webMCPSession(t, handler, testPageOrigin)
	body := map[string]any{"actor": "local-operator", "job": snapshot.Definition.Job, "campaign": snapshot.Definition.Campaign, "workflow": loadDefinition(t)}
	previewResponse := webMCPRequest(t, handler, token, "local-operator", "preview_workflow_admission", "/v1/workflows/preview", body)
	var previewEnvelope struct {
		Data struct {
			Preview contractsv1.WorkflowAdmissionPreview `json:"preview"`
		} `json:"data"`
	}
	decodeBody(t, previewResponse, &previewEnvelope)
	confirmation := webMCPRequest(t, handler, token, "local-operator", "confirm_authorized_action", "/v1/workflows/confirm", map[string]any{"actor": "local-operator", "preview": previewEnvelope.Data.Preview})
	if confirmation.Code != http.StatusOK {
		t.Fatalf("canonical audit fallback status = %d: %s", confirmation.Code, confirmation.Body.String())
	}
	var confirmed struct {
		Data struct {
			Admission contractsv1.WorkflowAdmission `json:"admission"`
		} `json:"data"`
	}
	decodeBody(t, confirmation, &confirmed)
	binding, ok := confirmed.Data.Admission.Receipt.Payload["webmcp_audit"].(map[string]any)
	if !ok || binding["request_id"] != "123e4567-e89b-42d3-a456-426614174000" {
		t.Fatalf("canonical receipt is missing WebMCP audit binding: %+v", confirmed.Data.Admission.Receipt.Payload)
	}
}

func TestWebMCPReadFailsClosedWhenCompletionAuditFails(t *testing.T) {
	snapshot := loadCanvas(t)
	audit := &failWrite{failAt: 3}
	handler, err := builderapi.NewWithWebMCP(testCore(t), time.Now, &snapshot, builderapi.WebMCPConfig{PageOrigin: testPageOrigin, Audit: audit, RateLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	response := webMCPGet(t, handler, webMCPSession(t, handler, testPageOrigin), "local-operator", "inspect_current_canvas", map[string]any{})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("read completion audit failure status = %d", response.Code)
	}
}

func TestWebMCPCanonicalAuditFallbackRequiresExactRequestBinding(t *testing.T) {
	snapshot := loadCanvas(t)
	audit := &failWrite{failAt: 9}
	handler, err := builderapi.NewWithWebMCP(testCore(t), time.Now, &snapshot, builderapi.WebMCPConfig{PageOrigin: testPageOrigin, Audit: audit, RateLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	token := webMCPSession(t, handler, testPageOrigin)
	previewResponse := webMCPRequest(t, handler, token, "local-operator", "preview_workflow_admission", "/v1/workflows/preview", map[string]any{
		"actor": "local-operator", "job": snapshot.Definition.Job, "campaign": snapshot.Definition.Campaign, "workflow": loadDefinition(t),
	})
	var previewEnvelope struct {
		Data struct {
			Preview contractsv1.WorkflowAdmissionPreview `json:"preview"`
		} `json:"data"`
	}
	decodeBody(t, previewResponse, &previewEnvelope)
	first := webMCPRequest(t, handler, token, "local-operator", "confirm_authorized_action", "/v1/workflows/confirm", map[string]any{"actor": "local-operator", "preview": previewEnvelope.Data.Preview})
	if first.Code != http.StatusOK {
		t.Fatalf("first confirmation status = %d: %s", first.Code, first.Body.String())
	}
	previewJSON, err := json.Marshal(previewEnvelope.Data.Preview)
	if err != nil {
		t.Fatal(err)
	}
	reordered := append([]byte(`{"preview":`), previewJSON...)
	reordered = append(reordered, []byte(`,"actor":"local-operator"}`)...)
	retry := webMCPRawRequest(t, handler, token, "local-operator", "confirm_authorized_action", "/v1/workflows/confirm", reordered)
	if retry.Code != http.StatusServiceUnavailable {
		t.Fatalf("mismatched canonical audit fallback status = %d: %s", retry.Code, retry.Body.String())
	}
}

type failWrite struct {
	bytes.Buffer
	writes int
	failAt int
}

func (w *failWrite) Write(body []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("audit write failed")
	}
	return w.Buffer.Write(body)
}

func loadCanvas(t *testing.T) contractsv1.CanvasSnapshot {
	t.Helper()
	body, err := os.ReadFile("../../web/public/canvas.response.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		t.Fatal("decode canvas fixture")
	}
	return envelope.Data
}

func webMCPSession(t *testing.T, handler http.Handler, origin string) string {
	t.Helper()
	response := webMCPSessionResponse(handler, origin)
	if response.Code != http.StatusOK {
		t.Fatalf("session: %s", response.Body.String())
	}
	var envelope struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	decodeBody(t, response, &envelope)
	return envelope.Data.Token
}

func webMCPSessionResponse(handler http.Handler, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/webmcp/session", nil)
	request.Header.Set("X-Agent-Workflow-Page-Origin", origin)
	request.Header.Set("Origin", origin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func webMCPRequest(t *testing.T, handler http.Handler, token, subject, tool, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	return webMCPRequestWith(t, handler, token, subject, tool, path, value, testPageOrigin, "")
}

func webMCPRequestWith(t *testing.T, handler http.Handler, token, subject, tool, path string, value any, origin, hashOverride string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	hash := testDigest(body)
	if hashOverride != "" {
		hash = hashOverride
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	setWebMCPHeaders(request, token, subject, tool, origin, hash)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func webMCPRawRequest(t *testing.T, handler http.Handler, token, subject, tool, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	setWebMCPHeaders(request, token, subject, tool, testPageOrigin, testDigest(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func webMCPGet(t *testing.T, handler http.Handler, token, subject, tool string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/canvas", nil)
	setWebMCPHeaders(request, token, subject, tool, testPageOrigin, testDigest(body))
	request.Header.Set("X-Agent-Workflow-Input", string(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func setWebMCPHeaders(request *http.Request, token, subject, tool, origin, hash string) {
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Origin", origin)
	request.Header.Set("X-Agent-Workflow-Page-Origin", origin)
	request.Header.Set("X-Agent-Workflow-Request-ID", "123e4567-e89b-42d3-a456-426614174000")
	request.Header.Set("X-Agent-Workflow-Subject", subject)
	request.Header.Set("X-Agent-Workflow-Tool", tool)
	request.Header.Set("X-Agent-Workflow-Input-SHA256", hash)
}

func testDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func auditRecords(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	var records []map[string]any
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("decode WebMCP audit")
		}
		records = append(records, record)
	}
	return records
}
