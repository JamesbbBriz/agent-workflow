package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	conformanceassets "github.com/JamesbbBriz/agent-workflow/conformance"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestFixtureRuntimeRejectsApprovalAtTheActualExpiredTime(t *testing.T) {
	var fixture contractsv1.ConformanceFixture
	if err := json.Unmarshal(conformanceassets.GenericFixture(), &fixture); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewFixtureRuntime(fixture, nil, NewMemoryLedger())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Admit(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Drive(context.Background(), "research-campaign", 100); err != nil {
		t.Fatal(err)
	}
	preview, err := runtime.PreviewApproval(context.Background(), "research-campaign")
	if err != nil || preview.ExpiresAt == nil {
		t.Fatalf("preview: %+v err=%v", preview, err)
	}
	runtime.now = func() time.Time { return preview.ExpiresAt.Add(time.Second) }
	if _, err := runtime.ConfirmApproval(preview, "approve"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired approval was accepted: %v", err)
	}
}
