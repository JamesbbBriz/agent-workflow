package workflow

import (
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestReceiptV1RejectsChangeCaseEvents(t *testing.T) {
	if _, err := sealReceiptVersion(1, "change-case-a", 1, contractsv1.ReceiptReceiptTypeChangeProposed, time.Now().UTC(), nil, nil, nil, map[string]any{"state": map[string]any{}}); err == nil {
		t.Fatal("receipt schema v1 accepted a Change Case event")
	}
}
