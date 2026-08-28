package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func sealReceipt(aggregateID string, version int, receiptType contractsv1.ReceiptReceiptType, occurredAt time.Time, previous *contractsv1.SHA256, inputs, outputs []contractsv1.SHA256, payload map[string]any) (contractsv1.Receipt, error) {
	return sealReceiptVersion(1, aggregateID, version, receiptType, occurredAt, previous, inputs, outputs, payload)
}

func sealReceiptVersion(schemaVersion int, aggregateID string, version int, receiptType contractsv1.ReceiptReceiptType, occurredAt time.Time, previous *contractsv1.SHA256, inputs, outputs []contractsv1.SHA256, payload map[string]any) (contractsv1.Receipt, error) {
	if inputs == nil {
		inputs = []contractsv1.SHA256{}
	}
	if outputs == nil {
		outputs = []contractsv1.SHA256{}
	}
	identityHash, err := Digest(struct {
		AggregateID string
		Version     int
		Type        contractsv1.ReceiptReceiptType
	}{aggregateID, version, receiptType})
	if err != nil {
		return contractsv1.Receipt{}, err
	}
	receipt := contractsv1.Receipt{
		Kind: contractsv1.ReceiptKindReceipt, SchemaVersion: contractsv1.ReceiptSchemaVersion(schemaVersion),
		Id: shortID("receipt-", identityHash), AggregateId: aggregateID, AggregateVersion: version,
		OccurredAt: occurredAt.UTC(), ReceiptType: receiptType, InputHashes: inputs, OutputHashes: outputs,
		Payload: payload,
	}
	if previous == nil {
		receipt.PreviousReceiptHash = nil
	} else {
		receipt.PreviousReceiptHash = *previous
	}
	hash, err := receiptDigest(receipt)
	if err != nil {
		return contractsv1.Receipt{}, fmt.Errorf("seal receipt: %w", err)
	}
	receipt.ReceiptHash = contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("Receipt", receipt); err != nil {
		return contractsv1.Receipt{}, err
	}
	return receipt, nil
}

func sealActorReceipt(aggregateID string, version int, receiptType contractsv1.ReceiptReceiptType, actor string, occurredAt time.Time, previous *contractsv1.SHA256, inputs, outputs []contractsv1.SHA256, payload map[string]any) (contractsv1.Receipt, error) {
	receipt, err := sealReceipt(aggregateID, version, receiptType, occurredAt, previous, inputs, outputs, payload)
	if err != nil {
		return contractsv1.Receipt{}, err
	}
	receipt.Actor = &actor
	receipt.ReceiptHash = ""
	hash, err := receiptDigest(receipt)
	if err != nil {
		return contractsv1.Receipt{}, err
	}
	receipt.ReceiptHash = contractsv1.SHA256(hash)
	if err := contract.ValidateDefinition("Receipt", receipt); err != nil {
		return contractsv1.Receipt{}, err
	}
	return receipt, nil
}

func receiptDigest(receipt contractsv1.Receipt) (string, error) {
	body, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var wire any
	if err := decoder.Decode(&wire); err != nil {
		return "", err
	}
	return Digest(wire)
}
