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

func TestFixtureRuntimeFindsApprovalAfterCampaignAdvanced(t *testing.T) {
	fixture := conformanceassets.GenericFixture()
	var definition contractsv1.ConformanceFixture
	if err := json.Unmarshal(fixture, &definition); err != nil {
		t.Fatal(err)
	}
	ledger := NewMemoryLedger()
	runtime, err := NewFixtureRuntime(definition, nil, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Admit(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Drive(t.Context(), definition.Campaigns[0].Id, 100); err != nil {
		t.Fatal(err)
	}
	preview, err := runtime.PreviewApproval(t.Context(), definition.Campaigns[0].Id)
	if err != nil {
		t.Fatal(err)
	}
	runtime.now = func() time.Time { return preview.ExpiresAt.Add(-time.Second) }
	if _, err := runtime.ConfirmApproval(preview, "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Drive(t.Context(), definition.Campaigns[0].Id, 100); err != nil {
		t.Fatal(err)
	}
	option, ok, err := runtime.ExistingApprovalOption(t.Context(), definition.Campaigns[0].Id)
	if err != nil || !ok || option != "approve" {
		t.Fatalf("completed approval not recovered: option=%q ok=%v err=%v", option, ok, err)
	}
}

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
