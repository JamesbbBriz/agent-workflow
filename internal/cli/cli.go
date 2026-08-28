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
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
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
		fmt.Fprintln(stderr, "usage: agent-workflow <validate|demo|canvas|builder|provider|conformance> [options]")
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
	case "provider":
		return runProvider(args[1:], stdout, stderr)
	case "conformance":
		return runConformance(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: agent-workflow <validate|demo|canvas|builder|provider|conformance> [options]")
		return 2
	}
}

func runConformance(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("conformance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "conformance fixture JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *file == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "conformance requires exactly one --file")
		return 2
	}
	body, err := os.ReadFile(*file)
	if err != nil {
		return writeError(stdout, stderr, true, "input_unavailable", errors.New("conformance fixture is unavailable"))
	}
	var fixture contractsv1.ConformanceFixture
	if err := contract.DecodeDefinition("ConformanceFixture", body, &fixture); err != nil {
		return writeError(stdout, stderr, true, "invalid_fixture", err)
	}
	report, err := workflow.RunConformance(context.Background(), fixture, toolVersion())
	if err != nil {
		return writeError(stdout, stderr, true, "conformance_failed", err)
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return writeError(stdout, stderr, true, "output_failed", errors.New("conformance report could not be written"))
	}
	if !report.Passed {
		return 1
	}
	return 0
}

func toolVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "development"
}

func runBuilder(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("builder", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenAddress := flags.String("listen", "127.0.0.1:4321", "loopback listen address")
	ledgerPath := flags.String("ledger", ".agent-workflow/builder.jsonl", "canonical admission ledger")
	canvasPath := flags.String("canvas", "", "verified Canvas snapshot supplying approval source Replays")
	webOrigin := flags.String("web-origin", "", "exact page origin allowed to use experimental WebMCP")
	webMCPAuditPath := flags.String("webmcp-audit", ".agent-workflow/webmcp-audit.jsonl", "append-only WebMCP audit log")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "builder accepts only --listen, --ledger, --canvas, --web-origin, and --webmcp-audit")
		return 2
	}
	host, _, err := net.SplitHostPort(*listenAddress)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return writeError(stdout, stderr, true, "invalid_listen_address", errors.New("builder must listen on an explicit loopback IP"))
	}
	if err := os.MkdirAll(filepath.Dir(*ledgerPath), 0o700); err != nil {
		return writeError(stdout, stderr, true, "ledger_unavailable", errors.New("builder ledger directory is unavailable"))
	}
	if *webOrigin != "" {
		if err := os.MkdirAll(filepath.Dir(*webMCPAuditPath), 0o700); err != nil {
			return writeError(stdout, stderr, true, "audit_unavailable", errors.New("WebMCP audit directory is unavailable"))
		}
	}
	paths := []string{*ledgerPath}
	if *canvasPath != "" {
		paths = append(paths, *canvasPath)
	}
	if *webOrigin != "" {
		paths = append(paths, *webMCPAuditPath)
	}
	for left := 0; left < len(paths); left++ {
		for right := left + 1; right < len(paths); right++ {
			alias, err := pathsAlias(paths[left], paths[right])
			if err != nil || alias {
				return writeError(stdout, stderr, true, "path_collision", errors.New("builder ledger, Canvas, and WebMCP audit paths must be distinct"))
			}
		}
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
	var portfolio *contractsv1.CanvasPortfolioSnapshot
	var history builderapi.DefinitionHistory
	if sourceCanvas != nil {
		portfolio, history, err = restoreCanvasPortfolio(sourceCanvas, ledger)
		if err != nil {
			return writeError(stdout, stderr, true, "canvas_unavailable", err)
		}
		portfolio, err = restorePortfolioApprovals(portfolio, ledger)
		if err != nil {
			return writeError(stdout, stderr, true, "canvas_unavailable", err)
		}
	}
	var handler http.Handler = builderapi.NewWithPortfolioHistory(core, time.Now, portfolio, history)
	var auditFile *os.File
	if *webOrigin != "" {
		auditFile, err = os.OpenFile(*webMCPAuditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return writeError(stdout, stderr, true, "audit_unavailable", errors.New("WebMCP audit log is unavailable"))
		}
		auditInfo, statErr := auditFile.Stat()
		for _, protected := range []string{*ledgerPath, *canvasPath} {
			if protected == "" {
				continue
			}
			protectedInfo, protectedErr := os.Stat(protected)
			if statErr != nil || protectedErr != nil || os.SameFile(auditInfo, protectedInfo) {
				_ = auditFile.Close()
				return writeError(stdout, stderr, true, "path_collision", errors.New("builder ledger, Canvas, and WebMCP audit paths must be distinct"))
			}
		}
		if auditFile.Chmod(0o600) != nil {
			_ = auditFile.Close()
			return writeError(stdout, stderr, true, "audit_unavailable", errors.New("WebMCP audit log is unavailable"))
		}
		defer auditFile.Close()
		handler, err = builderapi.NewWithWebMCPPortfolio(core, time.Now, portfolio, builderapi.WebMCPConfig{PageOrigin: *webOrigin, Audit: auditFile})
		if err != nil {
			return writeError(stdout, stderr, true, "webmcp_unavailable", err)
		}
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return writeError(stdout, stderr, true, "listen_failed", errors.New("builder listener is unavailable"))
	}
	fmt.Fprintf(stdout, "builder listening on http://%s\n", listener.Addr())
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return writeError(stdout, stderr, true, "serve_failed", errors.New("builder server stopped"))
	}
	return 0
}

func pathsAlias(left, right string) (bool, error) {
	leftPath, err := canonicalPathCandidate(left)
	if err != nil {
		return false, err
	}
	rightPath, err := canonicalPathCandidate(right)
	if err != nil {
		return false, err
	}
	if leftPath == rightPath {
		return true, nil
	}
	leftInfo, leftErr := os.Stat(leftPath)
	rightInfo, rightErr := os.Stat(rightPath)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo), nil
	}
	if leftErr != nil && !errors.Is(leftErr, os.ErrNotExist) {
		return false, leftErr
	}
	if rightErr != nil && !errors.Is(rightErr, os.ErrNotExist) {
		return false, rightErr
	}
	return false, nil
}

func canonicalPathCandidate(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(parent, filepath.Base(abs))
	info, err := os.Lstat(candidate)
	if errors.Is(err, os.ErrNotExist) {
		return candidate, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return candidate, nil
	}
	return filepath.EvalSymlinks(candidate)
}

func restoreCanvasAdmissions(snapshot *contractsv1.CanvasSnapshot, ledger *workflow.FileLedger) (*contractsv1.CanvasSnapshot, error) {
	portfolio, _, err := restoreCanvasPortfolio(snapshot, ledger)
	if err != nil {
		return nil, err
	}
	for index := range portfolio.Campaigns {
		if portfolio.Campaigns[index].CampaignId == portfolio.SelectedCampaignId {
			return &portfolio.Campaigns[index].Canvas, nil
		}
	}
	return nil, errors.New("selected Campaign is unavailable")
}

func restoreCanvasPortfolio(snapshot *contractsv1.CanvasSnapshot, ledger *workflow.FileLedger) (*contractsv1.CanvasPortfolioSnapshot, builderapi.DefinitionHistory, error) {
	replays, err := ledger.ReplaysByReceiptType(contractsv1.ReceiptReceiptTypeAdmission)
	if err != nil {
		return nil, builderapi.DefinitionHistory{}, err
	}
	type event struct {
		admission contractsv1.WorkflowAdmission
		replay    contractsv1.ReplayBundle
		at        time.Time
		hash      contractsv1.SHA256
	}
	events := make([]event, 0)
	for _, replay := range replays {
		for version, receipt := range replay.Receipts {
			admission, err := workflow.MaterializeAdmission(replay, version+1)
			if err != nil {
				return nil, builderapi.DefinitionHistory{}, err
			}
			events = append(events, event{admission: admission, replay: replay, at: receipt.OccurredAt, hash: receipt.ReceiptHash})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].at.Equal(events[j].at) {
			return events[i].hash < events[j].hash
		}
		return events[i].at.Before(events[j].at)
	})
	job := snapshot.Definition.Job
	jobs := map[contractsv1.Identifier]contractsv1.JobDefinition{job.Id: job}
	campaignDefinitions := map[contractsv1.Identifier]contractsv1.CampaignDefinition{
		snapshot.Definition.Campaign.Id: snapshot.Definition.Campaign,
	}
	campaigns := []contractsv1.CanvasSnapshot{*snapshot}
	selected := snapshot.Definition.Campaign.Id
	definitionsByCampaign := map[contractsv1.Identifier]map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition{}
	definitionsByRef := map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition{}
	for _, definition := range snapshot.Definition.Workflows {
		definitionsByRef[contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version))] = definition
	}
	definitionsByCampaign[selected] = definitionsByRef
	for _, item := range events {
		if existing, ok := jobs[item.admission.Job.Id]; ok && !reflect.DeepEqual(existing, item.admission.Job) {
			return nil, builderapi.DefinitionHistory{}, errors.New("canonical admissions contain conflicting definitions for one Job")
		}
		jobs[item.admission.Job.Id] = item.admission.Job
		if existing, ok := campaignDefinitions[item.admission.Campaign.Id]; ok && !reflect.DeepEqual(existing, item.admission.Campaign) {
			return nil, builderapi.DefinitionHistory{}, errors.New("canonical admissions contain conflicting definitions for one Campaign")
		}
		campaignDefinitions[item.admission.Campaign.Id] = item.admission.Campaign
		if item.admission.Job.Id != job.Id {
			job = item.admission.Job
			campaigns = nil
			definitionsByCampaign = map[contractsv1.Identifier]map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition{}
		}
		definitionsByRef = definitionsByCampaign[item.admission.Campaign.Id]
		if definitionsByRef == nil {
			definitionsByRef = map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition{}
			definitionsByCampaign[item.admission.Campaign.Id] = definitionsByRef
		}
		ref := contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", item.admission.Workflow.Id, item.admission.Workflow.Version))
		definitionsByRef[ref] = item.admission.Workflow
		definitions := make([]contractsv1.WorkflowDefinition, 0, len(item.admission.Campaign.WorkflowPlan))
		for _, planned := range item.admission.Campaign.WorkflowPlan {
			if definition, ok := definitionsByRef[planned]; ok {
				definitions = append(definitions, definition)
			}
		}
		next, err := canvas.ProjectWithAdmissions(item.admission.Job, item.admission.Campaign, definitions, []contractsv1.ReplayBundle{item.replay})
		if err != nil {
			return nil, builderapi.DefinitionHistory{}, err
		}
		replaced := false
		for index := range campaigns {
			if campaigns[index].Definition.Campaign.Id == next.Definition.Campaign.Id {
				campaigns[index] = canvas.MergeAdmissionReadback(campaigns[index], next)
				replaced = true
				break
			}
		}
		if !replaced {
			campaigns = append(campaigns, next)
		}
		selected = next.Definition.Campaign.Id
	}
	portfolio, err := canvas.ProjectPortfolio(job, campaigns, selected)
	history := builderapi.DefinitionHistory{}
	for _, item := range jobs {
		history.Jobs = append(history.Jobs, item)
	}
	for _, item := range campaignDefinitions {
		history.Campaigns = append(history.Campaigns, item)
	}
	sort.Slice(history.Jobs, func(i, j int) bool { return history.Jobs[i].Id < history.Jobs[j].Id })
	sort.Slice(history.Campaigns, func(i, j int) bool { return history.Campaigns[i].Id < history.Campaigns[j].Id })
	return &portfolio, history, err
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

func restorePortfolioApprovals(portfolio *contractsv1.CanvasPortfolioSnapshot, ledger *workflow.FileLedger) (*contractsv1.CanvasPortfolioSnapshot, error) {
	if portfolio == nil {
		return nil, nil
	}
	campaigns := make([]contractsv1.CanvasSnapshot, 0, len(portfolio.Campaigns))
	for index := range portfolio.Campaigns {
		next, err := restoreCanvasApprovals(&portfolio.Campaigns[index].Canvas, ledger)
		if err != nil {
			return nil, err
		}
		campaigns = append(campaigns, *next)
	}
	next, err := canvas.ProjectPortfolio(portfolio.Job, campaigns, portfolio.SelectedCampaignId)
	return &next, err
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
	if brief.Action.CampaignId != snapshot.Definition.Campaign.Id {
		return false, nil
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
		[]string{"context-missing", "provider-timeout", "approval-required", "approval-stale"}, []string{"human-confirm"}, ledger, sources).
		WithApprovalAuthorities(workflow.ApprovalAuthorityCatalog{"human-confirm": []string{"local-operator"}}), nil
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
	for _, replays := range [][]contractsv1.ReplayBundle{envelope.Data.Replays, envelope.Data.AdmissionReplays, envelope.Data.ApprovalReplays} {
		for _, replay := range replays {
			if err := workflow.VerifyReplay(replay); err != nil {
				return nil, nil, errors.New("Canvas source Replay is invalid")
			}
		}
	}
	ledger := workflow.NewMemoryLedger()
	for _, replay := range envelope.Data.Replays {
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

func runProvider(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: agent-workflow provider <list|doctor|conformance> [options]")
		return 2
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return 2
		}
		return writeProviderData(stdout, workflow.BundledProviderDescriptors())
	case "doctor":
		flags := flag.NewFlagSet("provider doctor", flag.ContinueOnError)
		flags.SetOutput(stderr)
		id := flags.String("id", "", "bundled provider id")
		stagedRoot := flags.String("staged-root", "", "staged input/output workspace")
		configRef := flags.String("config-ref", "default", "non-secret provider configuration reference")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return 2
		}
		if *id != "" {
			result, err := workflow.InspectProviderReadinessAt(contractsv1.ProviderID(*id), *stagedRoot, *configRef)
			if err != nil {
				return writeError(stdout, stderr, true, "unknown_provider", err)
			}
			return writeProviderData(stdout, result)
		}
		results := make([]workflow.ProviderReadiness, 0, 5)
		for _, descriptor := range workflow.BundledProviderDescriptors() {
			result, _ := workflow.InspectProviderReadinessAt(descriptor.Id, *stagedRoot, *configRef)
			results = append(results, result)
		}
		return writeProviderData(stdout, results)
	case "conformance":
		return runProviderConformance(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: agent-workflow provider <list|doctor|conformance> [options]")
		return 2
	}
}

func runProviderConformance(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("provider conformance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	id := flags.String("id", "", "bundled provider id")
	adapter := flags.String("adapter", "", "adapter executable; defaults to the bundled command name")
	stagedRoot := flags.String("staged-root", "", "staged input/output workspace")
	model := flags.String("model", "", "provider model reference")
	providerVersion := flags.String("provider-version", "", "provider version")
	configRef := flags.String("config-ref", "default", "non-secret provider configuration reference")
	allowNetwork := flags.Bool("allow-network", false, "allow the isolated adapter to reach its provider API")
	file := flags.String("file", "conformance/fixtures/generic.json", "admitted conformance fixture or legacy Workflow definition")
	at := flags.String("at", "", "pinned evidence cutoff in RFC3339")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if *id == "" || *stagedRoot == "" || *model == "" || *providerVersion == "" || *at == "" {
		fmt.Fprintln(stderr, "provider conformance requires --id, --staged-root, --model, --provider-version, and --at")
		return 2
	}
	cutoff, err := time.Parse(time.RFC3339, *at)
	if err != nil {
		return writeError(stdout, stderr, true, "invalid_time", errors.New("conformance time must be RFC3339"))
	}
	descriptor, err := workflow.ProviderDescriptor(contractsv1.ProviderID(*id))
	if err != nil {
		return writeError(stdout, stderr, true, "unknown_provider", err)
	}
	secretRefs := make([]string, len(descriptor.AuthEnvironment))
	environment := make(map[string]string, len(descriptor.AuthEnvironment))
	for i, name := range descriptor.AuthEnvironment {
		secretRefs[i] = "env:" + name
		if value := os.Getenv(name); value != "" {
			environment[name] = value
		} else {
			return writeError(stdout, stderr, true, "provider_unavailable", fmt.Errorf("missing env:%s", name))
		}
	}
	profile, err := workflow.SealExecutorProfile(contractsv1.ExecutorProfile{
		Kind: contractsv1.ExecutorProfileKindExecutorProfile, SchemaVersion: 1,
		ProviderId: descriptor.Id, ProviderVersion: *providerVersion, AdapterVersion: descriptor.AdapterVersion,
		ModelRef: *model, ConfigRef: *configRef, Capabilities: descriptor.Capabilities,
		IsolationProfile: contractsv1.ProviderIsolationProfileStagedSubprocess, NetworkAccess: *allowNetwork, ToolAllowlist: []string{"read-evidence"}, SecretRefs: secretRefs,
	})
	if err != nil {
		return writeError(stdout, stderr, true, "invalid_profile", err)
	}
	provider, err := workflow.NewAgentRunnerProvider(workflow.SubprocessProviderConfig{Executable: *adapter, StagedRoot: *stagedRoot, Environment: environment, AllowNetwork: *allowNetwork}, profile)
	if err != nil {
		return writeError(stdout, stderr, true, "provider_unavailable", err)
	}
	body, err := os.ReadFile(*file)
	if err != nil {
		return writeError(stdout, stderr, true, "input_unavailable", errors.New("workflow fixture is unavailable"))
	}
	var fixture contractsv1.ConformanceFixture
	if err := contract.DecodeDefinition("ConformanceFixture", body, &fixture); err == nil {
		if fixture.Profile != contractsv1.ConformanceFixtureProfileGeneric {
			return writeError(stdout, stderr, true, "invalid_fixture", errors.New("provider conformance requires the generic fixture"))
		}
		report, err := workflow.RunConformanceWithProvider(context.Background(), fixture, toolVersion(), descriptor.Id, provider)
		stopAt := time.Now().Add(5 * time.Minute)
		for errors.Is(err, workflow.ErrProviderNotReady) && time.Now().Before(stopAt) {
			time.Sleep(50 * time.Millisecond)
			report, err = workflow.RunConformanceWithProvider(context.Background(), fixture, toolVersion(), descriptor.Id, provider)
		}
		if err != nil {
			return writeError(stdout, stderr, true, "conformance_failed", err)
		}
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return writeError(stdout, stderr, true, "output_failed", errors.New("conformance report could not be written"))
		}
		return 0
	}
	var definition contractsv1.WorkflowDefinition
	if _, err := contract.ValidateWorkflow(body); err != nil {
		return writeError(stdout, stderr, true, "invalid_workflow", err)
	}
	if err := json.Unmarshal(body, &definition); err != nil {
		return writeError(stdout, stderr, true, "invalid_workflow", err)
	}
	result, err := executeDemoWithProvider(definition, cutoff.UTC(), provider)
	stopAt := time.Now().Add(5 * time.Minute)
	for errors.Is(err, workflow.ErrProviderNotReady) && time.Now().Before(stopAt) {
		time.Sleep(50 * time.Millisecond)
		result, err = executeDemoWithProvider(definition, cutoff.UTC(), provider)
	}
	if err != nil {
		return writeError(stdout, stderr, true, "conformance_failed", err)
	}
	return writeProviderData(stdout, result)
}

func writeProviderData(stdout io.Writer, data any) int {
	_ = json.NewEncoder(stdout).Encode(struct {
		OK   bool `json:"ok"`
		Data any  `json:"data"`
	}{OK: true, Data: data})
	return 0
}

func executeDemo(definition contractsv1.WorkflowDefinition, cutoff time.Time) (workflow.RunResult, error) {
	return executeDemoWithProvider(definition, cutoff, &demoProvider{results: make(map[string]workflow.ProviderResult)})
}

func executeDemoWithProvider(definition contractsv1.WorkflowDefinition, cutoff time.Time, provider workflow.Provider) (workflow.RunResult, error) {
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
	}, provider, ledger)
	if _, ok := provider.(*workflow.AgentRunnerProvider); ok {
		engine.RequireProviderIsolation(contractsv1.ProviderIsolationProfileStagedSubprocess)
	}
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
