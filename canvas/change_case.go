package canvas

import (
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func ProjectChangeCase(replay contractsv1.ReplayBundle, generatedAt time.Time) (contractsv1.ChangeCaseCanvas, error) {
	state, err := workflow.MaterializeChangeCase(replay)
	if err != nil {
		return contractsv1.ChangeCaseCanvas{}, err
	}
	receipts := make([]contractsv1.ChangeCaseReceiptLink, len(replay.Receipts))
	for index, receipt := range replay.Receipts {
		receipts[index] = contractsv1.ChangeCaseReceiptLink{Id: receipt.Id, ReceiptType: contractsv1.ChangeCaseReceiptLinkReceiptType(receipt.ReceiptType), ReceiptHash: receipt.ReceiptHash, OccurredAt: receipt.OccurredAt}
	}
	result := contractsv1.ChangeCaseCanvas{Kind: "change_case_canvas", SchemaVersion: 2, GeneratedAt: generatedAt.UTC(), State: state, Receipts: receipts}
	if err := contract.ValidateDefinition("ChangeCaseCanvas", result); err != nil {
		return contractsv1.ChangeCaseCanvas{}, err
	}
	return result, nil
}
