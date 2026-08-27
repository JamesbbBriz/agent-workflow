package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

type response struct {
	OK          bool   `json:"ok"`
	WorkflowRef string `json:"workflow_ref,omitempty"`
	Hash        string `json:"hash,omitempty"`
	Error       string `json:"error,omitempty"`
	Code        string `json:"code,omitempty"`
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: agent-workflow <validate|demo> [options]")
		return 2
	}
	switch args[0] {
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: agent-workflow <validate|demo> [options]")
		return 2
	}
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "workflow definition JSON")
	jsonOutput := flags.Bool("json", false, "write a JSON response")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *file == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "validate requires exactly one --file")
		return 2
	}
	body, err := os.ReadFile(*file)
	if err != nil {
		return writeError(stdout, stderr, *jsonOutput, "input_unavailable", errors.New("workflow file is unavailable"))
	}
	identity, err := contract.ValidateWorkflow(body)
	if err != nil {
		return writeError(stdout, stderr, *jsonOutput, "invalid_workflow", err)
	}
	result := response{OK: true, WorkflowRef: identity.Ref, Hash: identity.Hash}
	if *jsonOutput {
		_ = json.NewEncoder(stdout).Encode(result)
	} else {
		fmt.Fprintf(stdout, "%s %s\n", identity.Ref, identity.Hash)
	}
	return 0
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "workflow definition JSON")
	at := flags.String("at", "", "pinned evidence cutoff in RFC3339")
	jsonOutput := flags.Bool("json", false, "write a JSON response")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *file == "" || *at == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "demo requires exactly one --file and --at")
		return 2
	}
	cutoff, err := time.Parse(time.RFC3339, *at)
	if err != nil {
		return writeError(stdout, stderr, *jsonOutput, "invalid_time", errors.New("demo time must be RFC3339"))
	}
	body, err := os.ReadFile(*file)
	if err != nil {
		return writeError(stdout, stderr, *jsonOutput, "input_unavailable", errors.New("workflow file is unavailable"))
	}
	if _, err := contract.ValidateWorkflow(body); err != nil {
		return writeError(stdout, stderr, *jsonOutput, "invalid_workflow", err)
	}
	var definition contractsv1.WorkflowDefinition
	if err := json.Unmarshal(body, &definition); err != nil {
		return writeError(stdout, stderr, *jsonOutput, "invalid_workflow", errors.New("workflow file is invalid"))
	}
	result, err := executeDemo(definition, cutoff.UTC())
	if err != nil {
		return writeError(stdout, stderr, *jsonOutput, "demo_failed", err)
	}
	if *jsonOutput {
		_ = json.NewEncoder(stdout).Encode(struct {
			OK   bool               `json:"ok"`
			Data workflow.RunResult `json:"data"`
		}{OK: true, Data: result})
	} else {
		fmt.Fprintf(stdout, "%s %s\n", result.Compiled.WorkflowRef, result.Replay.BundleHash)
	}
	return 0
}

func executeDemo(definition contractsv1.WorkflowDefinition, cutoff time.Time) (workflow.RunResult, error) {
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"example-project"}}
	brief := map[string]any{"brief": "Prefer evidence-bound recommendations."}
	briefHash, err := workflow.Digest(brief)
	if err != nil {
		return workflow.RunResult{}, err
	}
	seedHash := contractsv1.SHA256("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	pack := contractsv1.ContextPackEdition{
		Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1,
		Id: "pack-project-brief-example", PackType: "project-brief", PackSchemaVersion: 1,
		Authority: contractsv1.ContextPackEditionAuthorityCanonical, Scope: scope,
		CapturedAt: cutoff.Add(-time.Hour), ExpiresAt: cutoff.Add(24 * time.Hour),
		Coverage: contractsv1.ContextPackEditionCoverageComplete, Content: brief, ContentSha256: contractsv1.SHA256(briefHash),
		Provenance: []contractsv1.ArtifactRef{{Id: "example-seed", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "seed", SchemaVersion: 1, Sha256: seedHash, MediaType: "application/json"}},
	}
	registry, err := workflow.NewRegistry(
		workflow.NewCatalogProducer("project-brief", "project-brief", 1, pack),
		workflow.NewIntentProducer(),
	)
	if err != nil {
		return workflow.RunResult{}, err
	}
	job := contractsv1.JobDefinition{
		Kind: contractsv1.JobDefinitionKindJobDefinition, SchemaVersion: 1, Id: "example-job",
		Intent: demoIntent(contractsv1.IntentCardKindJob, "Example research job"), Scope: scope,
		Budget: contractsv1.Budget{MaxAttempts: 2, MaxActions: 1, MaxCandidates: 3}, CampaignArchetypes: []string{"research"},
	}
	campaign := contractsv1.CampaignDefinition{
		Kind: contractsv1.CampaignDefinitionKindCampaignDefinition, SchemaVersion: 1, Id: "example-campaign", JobId: job.Id,
		Archetype: "research", Intent: demoIntent(contractsv1.IntentCardKindCampaign, "Example evidence campaign"), Scope: scope,
		EvidenceFrontier: contractsv1.EvidenceFrontier{Cutoff: cutoff, SourceHashes: []contractsv1.SHA256{}}, WorkflowPlan: []contractsv1.WorkflowRef{contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version))},
		Budget: job.Budget,
	}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{
		"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead,
	}, workflow.OutputCatalog{
		"recommendation@1": validateRecommendation,
	}, &demoProvider{results: make(map[string][]contractsv1.ActionArtifact)}, workflow.NewMemoryLedger())
	return engine.RunNode(context.Background(), workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
}

func demoIntent(kind contractsv1.IntentCardKind, title string) contractsv1.IntentCard {
	return contractsv1.IntentCard{
		SchemaVersion: 1, Kind: kind, Title: title, Summary: title, Objective: title,
		SuccessSignals: []string{"done"}, NonGoals: []string{"none"}, Completion: []string{"done"}, NoActionWhen: []string{"not needed"},
	}
}

type demoProvider struct {
	results map[string][]contractsv1.ActionArtifact
}

func validateRecommendation(value any) error {
	object, ok := value.(map[string]any)
	if !ok {
		return errors.New("recommendation must be an object")
	}
	text, ok := object["recommendation"].(string)
	if !ok || text == "" {
		return errors.New("recommendation is required")
	}
	return nil
}

func (p *demoProvider) Start(_ context.Context, invocation workflow.Invocation) error {
	if _, ok := p.results[invocation.IdempotencyKey]; ok {
		return nil
	}
	content := map[string]any{"recommendation": "Keep the bounded workflow."}
	hash, err := workflow.Digest(content)
	if err != nil {
		return err
	}
	p.results[invocation.IdempotencyKey] = []contractsv1.ActionArtifact{{
		Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "example-recommendation",
		ArtifactType: "recommendation", JobId: invocation.JobID, CampaignId: invocation.CampaignID,
		WorkflowRef: invocation.WorkflowRef, NodeId: invocation.Node.Id,
		InputHashes: invocation.InputHashes,
		Content:     content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending,
	}}
	return nil
}

func (p *demoProvider) Poll(_ context.Context, key string) ([]contractsv1.ActionArtifact, bool, error) {
	result, ok := p.results[key]
	return result, ok, nil
}

func (*demoProvider) Cancel(context.Context, string) error { return nil }

func writeError(stdout, stderr io.Writer, jsonOutput bool, code string, err error) int {
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(response{OK: false, Error: err.Error(), Code: code})
	} else {
		fmt.Fprintln(stderr, err)
	}
	return 1
}
