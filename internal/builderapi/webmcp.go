package builderapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

const (
	webMCPToolHeader      = "X-Agent-Workflow-Tool"
	webMCPRequestIDHeader = "X-Agent-Workflow-Request-ID"
	webMCPSubjectHeader   = "X-Agent-Workflow-Subject"
	webMCPOriginHeader    = "X-Agent-Workflow-Page-Origin"
	webMCPInputHashHeader = "X-Agent-Workflow-Input-SHA256"
	webMCPInputHeader     = "X-Agent-Workflow-Input"
	maxWebMCPReadInput    = 4096
	webMCPLocalSubject    = "local-operator"
)

type WebMCPConfig struct {
	PageOrigin string
	Audit      io.Writer
	RateLimit  int
}

type webMCPGate struct {
	origin  string
	token   string
	subject string
	audit   io.Writer
	limit   int
	mu      sync.Mutex
	usage   map[string]webMCPUsage
}

type webMCPUsage struct {
	window        time.Time
	count         int
	denialAudited bool
}

type webMCPAuditContextKey struct{}

type webMCPAuditRecord struct {
	SchemaVersion        int    `json:"schema_version"`
	Phase                string `json:"phase"`
	OccurredAt           string `json:"occurred_at"`
	RequestID            string `json:"request_id"`
	Subject              string `json:"subject,omitempty"`
	PageOrigin           string `json:"page_origin,omitempty"`
	Tool                 string `json:"tool,omitempty"`
	InputsSHA256         string `json:"inputs_sha256,omitempty"`
	PreviewIdentity      string `json:"preview_identity,omitempty"`
	ConfirmationIdentity string `json:"confirmation_identity,omitempty"`
	Outcome              string `json:"outcome,omitempty"`
	HTTPStatus           int    `json:"http_status,omitempty"`
	CanonicalReceiptRef  string `json:"canonical_receipt_ref,omitempty"`
}

func NewWithWebMCP(core *workflow.AuthoringCore, now func() time.Time, snapshot *contractsv1.CanvasSnapshot, config WebMCPConfig) (http.Handler, error) {
	return newWithWebMCP(NewWithCanvas(core, now, snapshot).(*Handler), config)
}

func NewWithWebMCPPortfolio(core *workflow.AuthoringCore, now func() time.Time, portfolio *contractsv1.CanvasPortfolioSnapshot, config WebMCPConfig) (http.Handler, error) {
	return newWithWebMCP(NewWithPortfolio(core, now, portfolio).(*Handler), config)
}

func NewWithWebMCPControlPlane(core *workflow.AuthoringCore, now func() time.Time, portfolio *contractsv1.CanvasPortfolioSnapshot, history DefinitionHistory, changeCases []contractsv1.ChangeCaseCanvas, config WebMCPConfig) (http.Handler, error) {
	return newWithWebMCP(NewWithControlPlane(core, now, portfolio, history, changeCases).(*Handler), config)
}

func NewWithWebMCPControlPlanePortfolios(core *workflow.AuthoringCore, now func() time.Time, portfolios []contractsv1.CanvasPortfolioSnapshot, selectedJobID contractsv1.Identifier, history DefinitionHistory, changeCases []contractsv1.ChangeCaseCanvas, config WebMCPConfig) (http.Handler, error) {
	return newWithWebMCP(NewWithControlPlanePortfolios(core, now, portfolios, selectedJobID, history, changeCases).(*Handler), config)
}

func NewWithWebMCPControlPlanePortfoliosReader(core *workflow.AuthoringCore, now func() time.Time, portfolios []contractsv1.CanvasPortfolioSnapshot, selectedJobID contractsv1.Identifier, history DefinitionHistory, readChanges func(time.Time) ([]contractsv1.ChangeCaseCanvas, error), config WebMCPConfig) (http.Handler, error) {
	return newWithWebMCP(NewWithControlPlanePortfoliosReader(core, now, portfolios, selectedJobID, history, readChanges).(*Handler), config)
}

func NewWithWebMCPControlPlaneReaders(core *workflow.AuthoringCore, now func() time.Time, portfolios []contractsv1.CanvasPortfolioSnapshot, selectedJobID contractsv1.Identifier, history DefinitionHistory, readChanges func(time.Time) ([]contractsv1.ChangeCaseCanvas, error), readPortfolios func(time.Time) ([]contractsv1.CanvasPortfolioSnapshot, contractsv1.Identifier, error), config WebMCPConfig) (http.Handler, error) {
	return newWithWebMCP(NewWithControlPlaneReaders(core, now, portfolios, selectedJobID, history, readChanges, readPortfolios).(*Handler), config)
}

func newWithWebMCP(handler *Handler, config WebMCPConfig) (http.Handler, error) {
	if config.Audit == nil {
		return nil, errors.New("WebMCP audit writer is required")
	}
	origin, err := url.Parse(config.PageOrigin)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.User != nil {
		return nil, errors.New("WebMCP page origin must be an exact HTTP origin")
	}
	limit := config.RateLimit
	if limit == 0 {
		limit = 30
	}
	if limit < 1 {
		return nil, errors.New("WebMCP rate limit must be positive")
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, errors.New("WebMCP session token is unavailable")
	}
	handler.webMCP = &webMCPGate{origin: origin.String(), token: hex.EncodeToString(tokenBytes), subject: webMCPLocalSubject, audit: config.Audit, limit: limit, usage: map[string]webMCPUsage{}}
	return handler, nil
}

func (h *Handler) serveWebMCPSession(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(webMCPOriginHeader) != h.webMCP.origin || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != h.webMCP.origin) {
		h.writeStatus(w, http.StatusForbidden, "origin_denied", "WebMCP page origin is not allowed")
		return
	}
	h.write(w, map[string]any{"token": h.webMCP.token, "subject": h.webMCP.subject, "page_origin": h.webMCP.origin}, nil)
}

func (h *Handler) serveWebMCP(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.Header.Get(webMCPRequestIDHeader))
	inputHash := strings.TrimSpace(r.Header.Get(webMCPInputHashHeader))
	tool := strings.TrimSpace(r.Header.Get(webMCPToolHeader))
	if r.Header.Get(webMCPOriginHeader) != h.webMCP.origin || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != h.webMCP.origin) {
		h.writeStatus(w, http.StatusForbidden, "origin_denied", "WebMCP page origin is not allowed")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+h.webMCP.token || strings.TrimSpace(r.Header.Get(webMCPSubjectHeader)) != h.webMCP.subject {
		h.writeStatus(w, http.StatusUnauthorized, "authentication_required", "WebMCP authentication is required")
		return
	}
	if !validRequestID(requestID) || !validSHA256(inputHash) {
		h.writeStatus(w, http.StatusBadRequest, "invalid_request", "WebMCP request correlation is invalid")
		return
	}
	if !webMCPRouteAllowed(tool, r.Method, r.URL.Path) {
		h.writeStatus(w, http.StatusForbidden, "tool_denied", "WebMCP tool is not allowed on this route")
		return
	}
	requestTime := h.now().UTC()
	record := webMCPAuditRecord{
		SchemaVersion: 1,
		Phase:         "started",
		OccurredAt:    requestTime.Format(time.RFC3339Nano),
		RequestID:     requestID,
		Subject:       h.webMCP.subject,
		PageOrigin:    h.webMCP.origin,
		Tool:          tool,
		InputsSHA256:  inputHash,
	}
	allowed, err := h.webMCP.admit(record, requestTime)
	if err != nil {
		h.writeStatus(w, http.StatusServiceUnavailable, "audit_unavailable", "WebMCP audit is unavailable")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", "60")
		h.writeStatus(w, http.StatusTooManyRequests, "rate_limited", "WebMCP rate limit exceeded")
		return
	}
	if err := h.webMCP.writeAudit(record); err != nil {
		h.writeStatus(w, http.StatusServiceUnavailable, "audit_unavailable", "WebMCP audit is unavailable")
		return
	}
	deny := func(status int, code, message string) {
		record.Phase, record.Outcome, record.HTTPStatus = "completed", code, status
		record.OccurredAt = h.now().UTC().Format(time.RFC3339Nano)
		if err := h.webMCP.writeAudit(record); err != nil {
			h.writeStatus(w, http.StatusServiceUnavailable, "audit_unavailable", "WebMCP audit is unavailable")
			return
		}
		h.writeStatus(w, status, code, message)
	}
	var body []byte
	if r.Method == http.MethodPost {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
		if err != nil || len(body) > maxRequestBytes {
			deny(http.StatusBadRequest, "invalid_request", "request body exceeds 2 MiB")
			return
		}
		if digestBytes(body) != record.InputsSHA256 {
			deny(http.StatusBadRequest, "input_hash_mismatch", "WebMCP input hash does not match the request")
			return
		}
		var actorRequest struct {
			Actor string `json:"actor"`
		}
		if json.Unmarshal(body, &actorRequest) != nil || actorRequest.Actor != record.Subject {
			deny(http.StatusUnauthorized, "subject_mismatch", "WebMCP subject does not match the Core actor")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	} else {
		body = []byte(r.Header.Get(webMCPInputHeader))
		if len(body) == 0 || len(body) > maxWebMCPReadInput || !json.Valid(body) || digestBytes(body) != record.InputsSHA256 {
			deny(http.StatusBadRequest, "input_hash_mismatch", "WebMCP input hash does not match the request")
			return
		}
	}
	record.Phase = "authorized"
	record.OccurredAt = h.now().UTC().Format(time.RFC3339Nano)
	record.PreviewIdentity, record.ConfirmationIdentity, _ = webMCPResponseIdentity(body, nil)
	if err := h.webMCP.writeAudit(record); err != nil {
		h.writeStatus(w, http.StatusServiceUnavailable, "audit_unavailable", "WebMCP audit is unavailable")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), webMCPAuditContextKey{}, workflow.AdmissionAudit{
		SchemaVersion: 1, RequestID: record.RequestID, Subject: record.Subject, PageOrigin: record.PageOrigin, Tool: record.Tool, InputsSHA256: record.InputsSHA256,
	}))
	capture := &webMCPResponseWriter{header: make(http.Header), status: http.StatusOK}
	h.serve(capture, r)
	record.Phase = "completed"
	record.OccurredAt = h.now().UTC().Format(time.RFC3339Nano)
	record.HTTPStatus = capture.status
	record.Outcome = "succeeded"
	if capture.status < 200 || capture.status >= 300 {
		record.Outcome = "rejected"
	}
	responsePreview, responseConfirmation, responseReceipt := webMCPResponseIdentity(body, capture.body.Bytes())
	record.CanonicalReceiptRef = responseReceipt
	if responsePreview != "" {
		record.PreviewIdentity = responsePreview
	}
	if responseConfirmation != "" {
		record.ConfirmationIdentity = responseConfirmation
	}
	if err := h.webMCP.writeAudit(record); err != nil && !webMCPResponseHasAudit(capture.body.Bytes(), workflow.AdmissionAudit{
		SchemaVersion: 1,
		RequestID:     record.RequestID,
		Subject:       record.Subject,
		PageOrigin:    record.PageOrigin,
		Tool:          record.Tool,
		InputsSHA256:  record.InputsSHA256,
	}, record.PreviewIdentity) {
		h.writeStatus(w, http.StatusServiceUnavailable, "audit_unavailable", "WebMCP audit is unavailable")
		return
	}
	for key, values := range capture.header {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.WriteHeader(capture.status)
	_, _ = w.Write(capture.body.Bytes())
}

func webMCPAdmissionAudit(ctx context.Context) *workflow.AdmissionAudit {
	audit, ok := ctx.Value(webMCPAuditContextKey{}).(workflow.AdmissionAudit)
	if !ok {
		return nil
	}
	return &audit
}

func (h *Handler) writeStatus(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": message, "code": code})
}

func (g *webMCPGate) admit(record webMCPAuditRecord, now time.Time) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	window := now.Truncate(time.Minute)
	usage := g.usage[record.Subject]
	if !usage.window.Equal(window) {
		usage = webMCPUsage{window: window}
	}
	if usage.count >= g.limit {
		if !usage.denialAudited {
			record.Phase, record.Outcome, record.HTTPStatus = "completed", "rate_limited", http.StatusTooManyRequests
			if err := g.writeAuditLocked(record); err != nil {
				return false, err
			}
			usage.denialAudited = true
			g.usage[record.Subject] = usage
		}
		return false, nil
	}
	usage.count++
	g.usage[record.Subject] = usage
	return true, nil
}

func (g *webMCPGate) writeAudit(record webMCPAuditRecord) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writeAuditLocked(record)
}

func (g *webMCPGate) writeAuditLocked(record webMCPAuditRecord) error {
	if err := json.NewEncoder(g.audit).Encode(record); err != nil {
		return err
	}
	if syncer, ok := g.audit.(interface{ Sync() error }); ok {
		return syncer.Sync()
	}
	return nil
}

type webMCPResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *webMCPResponseWriter) Header() http.Header { return w.header }

func (w *webMCPResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *webMCPResponseWriter) Write(body []byte) (int, error) {
	_, _ = w.body.Write(body)
	return len(body), nil
}

func webMCPRouteAllowed(tool, method, path string) bool {
	switch tool {
	case "inspect_current_canvas", "explain_context_blockers", "navigate_pending_approval":
		return method == http.MethodGet && path == "/v1/canvas"
	case "preview_workflow_admission":
		return method == http.MethodPost && path == "/v1/workflows/preview"
	case "confirm_authorized_action":
		return method == http.MethodPost && path == "/v1/workflows/confirm"
	default:
		return false
	}
}

func digestBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validRequestID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b'
}

func webMCPResponseIdentity(requestBody, responseBody []byte) (preview, confirmation, receipt string) {
	var request map[string]any
	_ = json.Unmarshal(requestBody, &request)
	if value, ok := request["preview"].(map[string]any); ok {
		preview, _ = value["preview_hash"].(string)
		confirmation, _ = value["commit_token"].(string)
	}
	var response struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(responseBody, &response) != nil {
		return
	}
	if value, ok := response.Data["preview"].(map[string]any); ok {
		preview, _ = value["preview_hash"].(string)
	}
	if value, ok := response.Data["admission"].(map[string]any); ok {
		preview, _ = value["preview_hash"].(string)
		if sealed, ok := value["receipt"].(map[string]any); ok {
			receipt, _ = sealed["receipt_hash"].(string)
		}
	}
	if value, ok := response.Data["receipt"].(map[string]any); ok {
		receipt, _ = value["receipt_hash"].(string)
	}
	return
}

func webMCPResponseHasAudit(responseBody []byte, expected workflow.AdmissionAudit, previewIdentity string) bool {
	var response struct {
		Data struct {
			Admission struct {
				PreviewHash string `json:"preview_hash"`
				Receipt     struct {
					Payload struct {
						PreviewHash string                  `json:"preview_hash"`
						WebMCPAudit workflow.AdmissionAudit `json:"webmcp_audit"`
					} `json:"payload"`
				} `json:"receipt"`
			} `json:"admission"`
		} `json:"data"`
	}
	if json.Unmarshal(responseBody, &response) != nil {
		return false
	}
	admission := response.Data.Admission
	return previewIdentity != "" &&
		admission.PreviewHash == previewIdentity &&
		admission.Receipt.Payload.PreviewHash == previewIdentity &&
		admission.Receipt.Payload.WebMCPAudit == expected
}
