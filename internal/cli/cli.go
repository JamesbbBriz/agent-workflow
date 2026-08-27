package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/JamesbbBriz/agent-workflow/canvas"
	"github.com/JamesbbBriz/agent-workflow/internal/builderapi"
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
		fmt.Fprintln(stderr, "usage: agent-workflow <validate|demo|canvas|builder> [options]")
		return 2
	}
	switch args[0] {
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "canvas":
		return runCanvas(args[1:], stdout, stderr)
	case "builder":
		return runBuilder(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: agent-workflow <validate|demo|canvas|builder> [options]")
		return 2
	}
}

func runBuilder(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("builder", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenAddress := flags.String("listen", "127.0.0.1:4321", "loopback listen address")
	ledgerPath := flags.String("ledger", ".agent-workflow/builder.jsonl", "canonical admission ledger")
	canvasPath := flags.String("canvas", "", "verified Canvas snapshot supplying approval source Replays")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "builder accepts only --listen, --ledger, and --canvas")
		return 2
	}
	host, _, err := net.SplitHostPort(*listenAddress)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return writeError(stdout, stderr, true, "invalid_listen_address", errors.New("builder must listen on an explicit loopback IP"))
	}
	ledger, err := workflow.OpenFileLedger(*ledgerPath)
	if err != nil {
		return writeError(stdout, stderr, true, "ledger_unavailable", err)
	}
	sources := workflow.NewMemoryLedger()
	var sourceCanvas *contractsv1.CanvasSnapshot
	if *canvasPath != "" {
		sources, sourceCanvas, err = loadCanvasSources(*canvasPath)
		if err != nil {
			return writeError(stdout, stderr, true, "canvas_unavailable", err)
		}
	}
	core, err := demoAuthoringCore(ledger, sources, sourceCanvas)
	if err != nil {
		return writeError(stdout, stderr, true, "builder_unavailable", err)
	}
	if sourceCanvas != nil {
		sourceCanvas, err = restoreCanvasAdmissions(sourceCanvas, ledger)
		if err != nil {
			return writeError(stdout, stderr, true, "canvas_unavailable", err)
		}
		sourceCanvas, err = restoreCanvasApprovals(sourceCanvas, ledger)
		if err != nil {
			return writeError(stdout, stderr, true, "canvas_unavailable", err)
		}
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return writeError(stdout, stderr, true, "listen_failed", errors.New("builder listener is unavailable"))
	}
	fmt.Fprintf(stdout, "builder listening on http://%s\n", listener.Addr())
	server := &http.Server{Handler: builderapi.NewWithCanvas(core, time.Now, sourceCanvas), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return writeError(stdout, stderr, true, "serve_failed", errors.New("builder server stopped"))
	}
	return 0
}

func restoreCanvasAdmissions(snapshot *contractsv1.CanvasSnapshot, ledger *workflow.FileLedger) (*contractsv1.CanvasSnapshot, error) {
	replays, err := ledger.ReplaysByReceiptType(contractsv1.ReceiptReceiptTypeAdmission)
	if err != nil || len(replays) == 0 {
		return snapshot, err
	}
	admissions := map[contractsv1.WorkflowRef]contractsv1.WorkflowAdmission{}
	var latest contractsv1.WorkflowAdmission
	var latestAt time.Time
	for _, replay := range replays {
		for version, receipt := range replay.Receipts {
			admission, err := workflow.MaterializeAdmission(replay, version+1)
			if err != nil {
				return nil, err
			}
			ref := contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", admission.Workflow.Id, admission.Workflow.Version))
			admissions[ref] = admission
			if receipt.OccurredAt.After(latestAt) || receipt.OccurredAt.Equal(latestAt) && string(receipt.ReceiptHash) > string(latest.Receipt.ReceiptHash) {
				latest, latestAt = admission, receipt.OccurredAt
			}
		}
	}
	definitions := make([]contractsv1.WorkflowDefinition, 0, len(latest.Campaign.WorkflowPlan))
	for _, ref := range latest.Campaign.WorkflowPlan {
		if admission, ok := admissions[ref]; ok {
			definitions = append(definitions, admission.Workflow)
			continue
		}
		for _, definition := range snapshot.Definition.Workflows {
			if contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version)) == ref {
				definitions = append(definitions, definition)
				break
			}
		}
	}
	next, err := canvas.ProjectWithAdmissions(latest.Job, latest.Campaign, definitions, replays)
	if err != nil {
		return nil, err
	}
	return &next, nil
}

func restoreCanvasApprovals(snapshot *contractsv1.CanvasSnapshot, ledger *workflow.FileLedger) (*contractsv1.CanvasSnapshot, error) {
	approvals, err := ledger.ReplaysByReceiptType(contractsv1.ReceiptReceiptTypeApproval)
	if err != nil {
		return nil, err
	}
	for _, approval := range approvals {
		next, err := canvas.ApplyApproval(*snapshot, approval)
		if err != nil {
			visible, visibilityErr := approvalActionVisible(*snapshot, approval)
			if visibilityErr != nil {
				return nil, visibilityErr
			}
			if !visible {
				continue
			}
			return nil, err
		}
		snapshot = &next
	}
	return snapshot, nil
}

func approvalActionVisible(snapshot contractsv1.CanvasSnapshot, approval contractsv1.ReplayBundle) (bool, error) {
	if err := workflow.VerifyReplay(approval); err != nil || len(approval.Receipts) != 1 {
		return false, errors.New("approval replay is invalid")
	}
	body, err := json.Marshal(approval.Receipts[0].Payload["brief"])
	if err != nil {
		return false, errors.New("approval brief is invalid")
	}
	var brief contractsv1.ApprovalBrief
	if json.Unmarshal(body, &brief) != nil || contract.ValidateDefinition("ApprovalBrief", brief) != nil {
		return false, errors.New("approval brief is invalid")
	}
	for _, execution := range snapshot.Executions {
		for _, output := range execution.Outputs {
			if output.Id == brief.Action.Id {
				return true, nil
			}
		}
	}
	return false, nil
}

func demoAuthoringCore(ledger, sources workflow.Ledger, snapshot *contractsv1.CanvasSnapshot) (*workflow.AuthoringCore, error) {
	var projectBriefs []contractsv1.ContextPackEdition
	if snapshot != nil {
		for _, execution := range snapshot.Executions {
			for _, port := range execution.ContextPorts {
				if port.Selector == "project-brief" && port.Edition != nil {
					projectBriefs = append(projectBriefs, *port.Edition)
				}
			}
		}
	}
	registry, err := workflow.NewRegistry(workflow.NewIntentProducer(), workflow.NewCatalogProducer("project-brief", "project-brief", 1, projectBriefs...))
	if err != nil {
		return nil, err
	}
	return workflow.NewAuthoringCoreWithSources(registry,
		workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent, "human-approval@1": contractsv1.NodeDefinitionKindApproval},
		workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead},
		workflow.OutputCatalog{"recommendation@1": validateRecommendation, "review-decision@1": func(any) error { return nil }},
		[]string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, ledger, sources), nil
}

func loadCanvasSources(path string) (workflow.Ledger, *contractsv1.CanvasSnapshot, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, errors.New("Canvas source file is unavailable")
	}
	var envelope struct {
		Data contractsv1.CanvasSnapshot `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || contract.ValidateDefinition("CanvasSnapshot", envelope.Data) != nil {
		return nil, nil, errors.New("Canvas source file is invalid")
	}
	ledger := workflow.NewMemoryLedger()
	for _, replay := range envelope.Data.Replays {
		if err := workflow.VerifyReplay(replay); err != nil {
			return nil, nil, errors.New("Canvas source Replay is invalid")
		}
		for _, receipt := range replay.Receipts {
			if err := ledger.Append(receipt); err != nil {
				return nil, nil, err
			}
		}
	}
	return ledger, &envelope.Data, nil
}

func runCanvas(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("canvas", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "workflow definition JSON")
	at := flags.String("at", "", "pinned evidence cutoff in RFC3339")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *file == "" || *at == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "canvas requires exactly one --file and --at")
		return 2
	}
	cutoff, err := time.Parse(time.RFC3339, *at)
	if err != nil {
		return writeError(stdout, stderr, true, "invalid_time", errors.New("canvas time must be RFC3339"))
	}
	body, err := os.ReadFile(*file)
	if err != nil {
		return writeError(stdout, stderr, true, "input_unavailable", errors.New("workflow file is unavailable"))
	}
	if _, err := contract.ValidateWorkflow(body); err != nil {
		return writeError(stdout, stderr, true, "invalid_workflow", err)
	}
	var definition contractsv1.WorkflowDefinition
	if err := json.Unmarshal(body, &definition); err != nil {
		return writeError(stdout, stderr, true, "invalid_workflow", errors.New("workflow file is invalid"))
	}
	result, err := executeDemo(definition, cutoff.UTC())
	if err != nil {
		return writeError(stdout, stderr, true, "canvas_failed", err)
	}
	job, campaign := demoDefinitions(definition, cutoff.UTC())
	snapshot, err := canvas.ProjectWithAdmissions(job, campaign, []contractsv1.WorkflowDefinition{definition}, []contractsv1.ReplayBundle{result.AdmissionReplay}, canvas.ExecutionInput{
		Replay:  result.Replay,
		Outputs: workflow.OutputCatalog{"recommendation@1": validateRecommendation},
	})
	if err != nil {
		return writeError(stdout, stderr, true, "canvas_failed", err)
	}
	_ = json.NewEncoder(stdout).Encode(struct {
		OK   bool                       `json:"ok"`
		Data contractsv1.CanvasSnapshot `json:"data"`
	}{OK: true, Data: snapshot})
	return 0
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
	job, campaign := demoDefinitions(definition, cutoff)
	ledger := workflow.NewMemoryLedger()
	authoring := workflow.NewAuthoringCore(registry,
		workflow.ExecutorCatalog{"bounded-agent@1": contractsv1.NodeDefinitionKindAgent, "human-approval@1": contractsv1.NodeDefinitionKindApproval},
		workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead},
		workflow.OutputCatalog{"recommendation@1": validateRecommendation, "review-decision@1": func(any) error { return nil }},
		[]string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, ledger)
	preview, _, err := authoring.Preview(job, campaign, definition, "demo-operator")
	if err != nil {
		return workflow.RunResult{}, err
	}
	if _, err := authoring.Confirm(preview, "demo-operator", cutoff); err != nil {
		return workflow.RunResult{}, err
	}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{
		"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead,
	}, workflow.OutputCatalog{
		"recommendation@1": validateRecommendation,
	}, &demoProvider{results: make(map[string]workflow.ProviderResult)}, ledger)
	return engine.RunNode(context.Background(), workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
}

func demoDefinitions(definition contractsv1.WorkflowDefinition, cutoff time.Time) (contractsv1.JobDefinition, contractsv1.CampaignDefinition) {
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"example-project"}}
	job := contractsv1.JobDefinition{
		Kind: contractsv1.JobDefinitionKindJobDefinition, SchemaVersion: 1, Id: "example-job",
		Intent: demoIntent(contractsv1.IntentCardKindJob, "Example research job"), Scope: scope,
		Budget: contractsv1.Budget{MaxAttempts: 2, MaxActions: 1, MaxCandidates: 3}, CampaignArchetypes: []string{"research"},
	}
	return job, contractsv1.CampaignDefinition{
		Kind: contractsv1.CampaignDefinitionKindCampaignDefinition, SchemaVersion: 1, Id: "example-campaign", JobId: job.Id,
		Archetype: "research", Intent: demoIntent(contractsv1.IntentCardKindCampaign, "Example evidence campaign"), Scope: scope,
		EvidenceFrontier: contractsv1.EvidenceFrontier{Cutoff: cutoff, SourceHashes: []contractsv1.SHA256{}}, WorkflowPlan: []contractsv1.WorkflowRef{contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version))},
		Budget: job.Budget,
	}
}

func demoIntent(kind contractsv1.IntentCardKind, title string) contractsv1.IntentCard {
	return contractsv1.IntentCard{
		SchemaVersion: 1, Kind: kind, Title: title, Summary: title, Objective: title,
		SuccessSignals: []string{"done"}, NonGoals: []string{"none"}, Completion: []string{"done"}, NoActionWhen: []string{"not needed"},
	}
}

type demoProvider struct {
	results map[string]workflow.ProviderResult
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
	p.results[invocation.IdempotencyKey] = workflow.ProviderResult{IdempotencyKey: invocation.IdempotencyKey, CompletedAt: time.Now().UTC(), Artifacts: []contractsv1.ActionArtifact{{
		Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "example-recommendation",
		ArtifactType: "recommendation", JobId: invocation.JobID, CampaignId: invocation.CampaignID,
		WorkflowRef: invocation.WorkflowRef, NodeId: invocation.Node.Id,
		InputHashes: invocation.InputHashes,
		Content:     content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending,
	}}}
	return nil
}

func (p *demoProvider) Poll(_ context.Context, key string) (workflow.ProviderResult, bool, error) {
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
