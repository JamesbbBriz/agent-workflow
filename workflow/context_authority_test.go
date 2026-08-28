package workflow

import (
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestCatalogProducerRejectsSelfHashedUnregisteredContext(t *testing.T) {
	cutoff := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	content := map[string]any{"brief": "canonical"}
	contentHash, err := Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	zero := contractsv1.SHA256("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	canonical := contractsv1.ContextPackEdition{
		Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1,
		Id: "brief-1", PackType: "project-brief", PackSchemaVersion: 1,
		Authority: contractsv1.ContextPackEditionAuthorityCanonical, Scope: scope,
		CapturedAt: cutoff.Add(-time.Hour), ExpiresAt: cutoff.Add(time.Hour), Coverage: contractsv1.ContextPackEditionCoverageComplete,
		Content: content, ContentSha256: contractsv1.SHA256(contentHash),
		Provenance: []contractsv1.ArtifactRef{{Id: "canonical-receipt", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "source", SchemaVersion: 1, Sha256: zero, MediaType: "application/json"}},
	}
	producer := NewCatalogProducer("project-brief", "project-brief", 1, canonical)
	request := ProducerRequest{Requirement: contractsv1.ContextRequirement{Id: "brief", Selector: "project-brief", PackType: "project-brief", SchemaVersion: 1, Required: true}, Campaign: contractsv1.CampaignDefinition{Scope: scope}, EvidenceCutoff: cutoff}
	forged := canonical
	forged.Provenance = []contractsv1.ArtifactRef{{Id: "forged-receipt", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "source", SchemaVersion: 1, Sha256: zero, MediaType: "application/json"}}
	if err := verifyProducerContext(producer, request, forged); err == nil {
		t.Fatal("self-hashed unregistered Context pack was accepted")
	}
}
