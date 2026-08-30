package builderapi

import (
	"encoding/json"
	"net/http"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func WithEvidenceReport(next http.Handler, readReplays func() ([]contractsv1.ReplayBundle, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/evidence-report" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		replays, err := readReplays()
		startedAt, endedAt := evidenceBounds(replays)
		if err == nil && startedAt.IsZero() {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "ledger has no evidence receipts", "code": "evidence_unavailable"})
			return
		}
		var report contractsv1.EvidenceWindowReport
		if err == nil {
			report, err = workflow.BuildEvidenceWindowReport(workflow.DefaultAgentRoleCatalog(), replays, startedAt, endedAt)
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "evidence report is unavailable", "code": "evidence_unavailable"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": report})
	})
}

func evidenceBounds(replays []contractsv1.ReplayBundle) (time.Time, time.Time) {
	var startedAt, endedAt time.Time
	for _, replay := range replays {
		for _, receipt := range replay.Receipts {
			if startedAt.IsZero() || receipt.OccurredAt.Before(startedAt) {
				startedAt = receipt.OccurredAt
			}
			if endedAt.IsZero() || receipt.OccurredAt.After(endedAt) {
				endedAt = receipt.OccurredAt
			}
		}
	}
	return startedAt, endedAt
}
