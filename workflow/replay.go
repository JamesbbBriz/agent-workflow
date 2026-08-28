package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

type CampaignRef struct {
	JobID      contractsv1.Identifier
	CampaignID contractsv1.Identifier
}

type ReceiptID string
type ReplayView string

const (
	ReplayViewRaw              ReplayView = "raw"
	ReplayViewPublicMetadataV1 ReplayView = "public_metadata@1"
)

type CampaignReplay struct {
	Raw      *contractsv1.ReplayBundle
	Redacted *contractsv1.RedactedReplay
}

func (e *Engine) ReplayAt(_ context.Context, ref CampaignRef, cutoff ReceiptID, view ReplayView) (CampaignReplay, error) {
	if e == nil || e.ledger == nil || ref.JobID == "" || ref.CampaignID == "" || cutoff == "" {
		return CampaignReplay{}, errors.New("Campaign ref, cutoff receipt, and ledger are required")
	}
	aggregateID, err := campaignExecutionID(ref.JobID, ref.CampaignID)
	if err != nil {
		return CampaignReplay{}, err
	}
	head, err := e.ledger.Replay(aggregateID)
	if err != nil {
		return CampaignReplay{}, err
	}
	index := -1
	for i := range head.Receipts {
		if head.Receipts[i].Id == string(cutoff) {
			index = i
			break
		}
	}
	if index < 0 {
		return CampaignReplay{}, errors.New("cutoff receipt is not in the canonical Campaign aggregate")
	}
	prefix, err := ReplayPrefix(head, index+1)
	if err != nil {
		return CampaignReplay{}, err
	}
	switch view {
	case ReplayViewRaw:
		clone, err := cloneReplay(prefix)
		if err != nil {
			return CampaignReplay{}, err
		}
		return CampaignReplay{Raw: &clone}, nil
	case ReplayViewPublicMetadataV1:
		redacted, err := redactReplay(prefix)
		if err != nil {
			return CampaignReplay{}, err
		}
		return CampaignReplay{Redacted: &redacted}, nil
	default:
		return CampaignReplay{}, errors.New("unknown Replay view or redaction policy")
	}
}

func cloneReplay(source contractsv1.ReplayBundle) (contractsv1.ReplayBundle, error) {
	body, err := json.Marshal(source)
	if err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	var clone contractsv1.ReplayBundle
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&clone); err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	return clone, VerifyReplay(clone)
}

func redactReplay(source contractsv1.ReplayBundle) (contractsv1.RedactedReplay, error) {
	if err := VerifyReplay(source); err != nil {
		return contractsv1.RedactedReplay{}, err
	}
	receipts := redactReplayReceipts(source)
	excluded := []contractsv1.ReplayRedactionProofExcludedClassesElem{
		contractsv1.ReplayRedactionProofExcludedClassesElemActor,
		contractsv1.ReplayRedactionProofExcludedClassesElemPayload,
	}
	proofHash, err := redactionDigest(source.BundleHash, source.CutoffReceiptHash, receipts, excluded)
	if err != nil {
		return contractsv1.RedactedReplay{}, err
	}
	redacted := contractsv1.RedactedReplay{
		Kind: contractsv1.RedactedReplayKindRedactedReplay, SchemaVersion: 1,
		AggregateId: source.AggregateId, CutoffReceiptId: source.Receipts[len(source.Receipts)-1].Id,
		CutoffReceiptHash: source.CutoffReceiptHash, Receipts: receipts,
		Proof: contractsv1.ReplayRedactionProof{
			Kind: contractsv1.ReplayRedactionProofKindReplayRedactionProof, SchemaVersion: 1,
			Policy:           contractsv1.ReplayRedactionProofPolicyPublicMetadata1,
			SourceBundleHash: source.BundleHash, CutoffReceiptHash: source.CutoffReceiptHash,
			ExcludedClasses: excluded, ProofHash: contractsv1.SHA256(proofHash),
		},
	}
	if err := contract.ValidateDefinition("RedactedReplay", redacted); err != nil {
		return contractsv1.RedactedReplay{}, err
	}
	if err := VerifyRedactedReplay(redacted, source); err != nil {
		return contractsv1.RedactedReplay{}, err
	}
	return redacted, nil
}

func VerifyRedactedReplay(redacted contractsv1.RedactedReplay, source contractsv1.ReplayBundle) error {
	if err := contract.ValidateDefinition("RedactedReplay", redacted); err != nil {
		return err
	}
	if err := VerifyReplay(source); err != nil {
		return err
	}
	if redacted.AggregateId != source.AggregateId || redacted.CutoffReceiptId != source.Receipts[len(source.Receipts)-1].Id || redacted.CutoffReceiptHash != source.CutoffReceiptHash || redacted.Proof.SourceBundleHash != source.BundleHash || redacted.Proof.CutoffReceiptHash != source.CutoffReceiptHash || len(redacted.Receipts) != len(source.Receipts) {
		return errors.New("redacted Replay is not bound to its canonical source")
	}
	want := redactReplayReceipts(source)
	if !reflect.DeepEqual(redacted.Receipts, want) {
		return errors.New("redacted Replay receipt links do not match the canonical source")
	}
	hash, err := redactionDigest(source.BundleHash, source.CutoffReceiptHash, redacted.Receipts, redacted.Proof.ExcludedClasses)
	if err != nil {
		return err
	}
	if redacted.Proof.ProofHash != contractsv1.SHA256(hash) {
		return errors.New("redacted Replay proof hash is invalid")
	}
	return nil
}

func redactReplayReceipts(source contractsv1.ReplayBundle) []contractsv1.RedactedReceipt {
	receipts := make([]contractsv1.RedactedReceipt, 0, len(source.Receipts))
	for _, receipt := range source.Receipts {
		receipts = append(receipts, contractsv1.RedactedReceipt{
			Id: receipt.Id, ReceiptType: contractsv1.RedactedReceiptReceiptType(receipt.ReceiptType), AggregateVersion: receipt.AggregateVersion,
			OccurredAt: receipt.OccurredAt, InputHashes: append([]contractsv1.SHA256{}, receipt.InputHashes...),
			OutputHashes: append([]contractsv1.SHA256{}, receipt.OutputHashes...), PreviousReceiptHash: receipt.PreviousReceiptHash, ReceiptHash: receipt.ReceiptHash,
		})
	}
	return receipts
}

func redactionDigest(source, cutoff contractsv1.SHA256, receipts []contractsv1.RedactedReceipt, excluded []contractsv1.ReplayRedactionProofExcludedClassesElem) (string, error) {
	return Digest(struct {
		Policy          string
		Source          contractsv1.SHA256
		Cutoff          contractsv1.SHA256
		Receipts        []contractsv1.RedactedReceipt
		ExcludedClasses []contractsv1.ReplayRedactionProofExcludedClassesElem
	}{string(ReplayViewPublicMetadataV1), source, cutoff, receipts, excluded})
}
