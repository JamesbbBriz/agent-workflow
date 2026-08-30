package builderapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/builderapi"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestEvidenceReportEndpointProjectsCanonicalReplays(t *testing.T) {
	startedAt := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	replay := validEvidenceReplay(t, startedAt)
	handler := builderapi.WithEvidenceReport(http.NotFoundHandler(), func() ([]contractsv1.ReplayBundle, error) {
		return []contractsv1.ReplayBundle{replay}, nil
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/evidence-report", nil))
	var body struct {
		OK   bool                             `json:"ok"`
		Data contractsv1.EvidenceWindowReport `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !body.OK || body.Data.Counts.Receipts != 1 || !body.Data.Window.StartedAt.Equal(startedAt) {
		t.Fatalf("unexpected evidence response: code=%d body=%s", response.Code, response.Body.String())
	}
}

func TestEvidenceReportEndpointHasTypedEmptyState(t *testing.T) {
	handler := builderapi.WithEvidenceReport(http.NotFoundHandler(), func() ([]contractsv1.ReplayBundle, error) { return nil, nil })
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/evidence-report", nil))
	if response.Code != http.StatusNotFound || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("empty report code=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestEvidenceReportEndpointFailsClosedWithoutLeakingLedgerErrors(t *testing.T) {
	handler := builderapi.WithEvidenceReport(http.NotFoundHandler(), func() ([]contractsv1.ReplayBundle, error) {
		return nil, errors.New("private ledger path and corruption detail")
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/evidence-report", nil))
	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("failed report code=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if body := response.Body.String(); body == "" || !json.Valid(response.Body.Bytes()) || strings.Contains(body, "private ledger path") {
		t.Fatalf("failed report leaked or omitted its typed error: %s", body)
	}
}

func validEvidenceReplay(t *testing.T, occurredAt time.Time) contractsv1.ReplayBundle {
	t.Helper()
	definition := loadDefinition(t)
	job, campaign := definitions(definition)
	core := testCore(t)
	preview, _, err := core.Preview(job, campaign, definition, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Confirm(preview, "operator", occurredAt); err != nil {
		t.Fatal(err)
	}
	replay, err := core.AdmissionReplay(string(definition.Id))
	if err != nil {
		t.Fatal(err)
	}
	return replay
}
