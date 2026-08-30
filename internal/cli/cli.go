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
	conformanceassets "github.com/JamesbbBriz/agent-workflow/conformance"
	"github.com/JamesbbBriz/agent-workflow/internal/builderapi"
	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

type response struct {
	Kind          string `json:"kind,omitempty"`
	SchemaVersion int    `json:"schema_version,omitempty"`
	OK            bool   `json:"ok"`
	WorkflowRef   string `json:"workflow_ref,omitempty"`
	Hash          string `json:"hash,omitempty"`
	Error         string `json:"error,omitempty"`
	Code          string `json:"code,omitempty"`
}

const maxConcurrentDeliveryAttempts = 100

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return 2
	}
	switch args[0] {
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "run":
		return runLocal(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "approval":
		return runApproval(args[1:], stdout, stderr)
	case "replay":
		return runReplay(args[1:], stdout, stderr)
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
		writeUsage(stderr)
		return 2
	}
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: agent-workflow <init|doctor|run|status|approval|replay|validate|demo|canvas|builder|provider|conformance> [options]")
}

func runInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", ".", "project directory")
	jsonOutput := flags.Bool("json", true, "write a JSON response")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !*jsonOutput {
		return 2
	}
	root := filepath.Clean(*dir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return writeError(stdout, stderr, true, "project_unavailable", errors.New("project directory is unavailable"))
	}
	if err := trustedProjectRoot(root); err != nil {
		return writeError(stdout, stderr, true, "project_unsafe", err)
	}
	want := conformanceassets.GenericFixture()
	definitionPath := filepath.Join(root, "agent-workflow.json")
	if info, err := os.Lstat(definitionPath); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return writeError(stdout, stderr, true, "project_unsafe", errors.New("agent-workflow.json must be a regular file, not a symlink"))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return writeError(stdout, stderr, true, "project_unavailable", errors.New("project definition is unavailable"))
	}
	if existing, err := os.ReadFile(definitionPath); err == nil {
		if !reflect.DeepEqual(existing, want) {
			return writeError(stdout, stderr, true, "project_exists", errors.New("agent-workflow.json already exists with different content"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return writeError(stdout, stderr, true, "project_unavailable", errors.New("project definition is unavailable"))
	} else if err := os.WriteFile(definitionPath, want, 0o600); err != nil {
		return writeError(stdout, stderr, true, "project_unavailable", errors.New("project definition could not be written"))
	}
	ledgerPath, err := localLedgerPath(root, true)
	if err != nil {
		return writeError(stdout, stderr, true, "ledger_unavailable", err)
	}
	ledger, err := workflow.OpenFileLedger(ledgerPath)
	if err != nil {
		return writeError(stdout, stderr, true, "ledger_unavailable", err)
	}
	_ = ledger
	result := contractsv1.LocalProjectInit{Kind: contractsv1.LocalProjectInitKindLocalProjectInit, SchemaVersion: 1, Project: definitionPath, Ledger: ledgerPath}
	if err := contract.ValidateDefinition("LocalProjectInit", result); err != nil {
		return writeError(stdout, stderr, true, "output_failed", err)
	}
	return writeProviderData(stdout, result)
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	runtime, campaign, root, code := openLocalRuntime(args, stdout, stderr, "doctor")
	if code != 0 {
		return code
	}
	if err := runtime.Preflight(); err != nil {
		return writeError(stdout, stderr, true, "core_not_ready", err)
	}
	ledgerPath, err := localLedgerPath(root, false)
	if err != nil {
		return writeError(stdout, stderr, true, "ledger_unavailable", err)
	}
	info, err := os.Stat(ledgerPath)
	if err != nil {
		return writeError(stdout, stderr, true, "ledger_unavailable", errors.New("ledger is unavailable"))
	}
	if info.Mode().Perm() != 0o600 {
		return writeError(stdout, stderr, true, "ledger_unsafe", errors.New("ledger mode must be 0600"))
	}
	result := contractsv1.LocalProjectDoctor{
		Kind: contractsv1.LocalProjectDoctorKindLocalProjectDoctor, SchemaVersion: 1, Ready: true,
		CampaignId: campaign, Status: contractsv1.LocalProjectDoctorStatusReady, Core: contractsv1.LocalProjectDoctorCoreReady,
		Provider: contractsv1.LocalProjectDoctorProvider{Id: "demo", Ready: true, Production: false},
		Storage:  contractsv1.LocalProjectDoctorStorage{Path: ledgerPath, Mode: contractsv1.LocalProjectDoctorStorageModeRw},
	}
	if err := contract.ValidateDefinition("LocalProjectDoctor", result); err != nil {
		return writeError(stdout, stderr, true, "output_failed", err)
	}
	return writeProviderData(stdout, result)
}

func runLocal(args []string, stdout, stderr io.Writer) int {
	runtime, campaign, root, code := openLocalRuntime(args, stdout, stderr, "run")
	if code != 0 {
		return code
	}
	for attempt := 0; ; attempt++ {
		if err := runtime.Admit(); err != nil {
			return writeError(stdout, stderr, true, "admission_failed", err)
		}
		if _, err := runtime.Drive(context.Background(), campaign, 100); err == nil {
			break
		} else if !errors.Is(err, workflow.ErrCanonicalConflict) || attempt == maxConcurrentDeliveryAttempts-1 {
			return writeError(stdout, stderr, true, "run_failed", err)
		}
		var reloadCode int
		runtime, campaign, _, reloadCode = loadLocalRuntime(root, campaign, stdout, stderr)
		if reloadCode != 0 {
			return reloadCode
		}
	}
	preview, err := runtime.Preview(context.Background(), campaign)
	if err != nil {
		return writeError(stdout, stderr, true, "run_failed", err)
	}
	return writeProviderData(stdout, preview)
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	runtime, campaign, _, code := openLocalRuntime(args, stdout, stderr, "status")
	if code != 0 {
		return code
	}
	preview, err := runtime.Preview(context.Background(), campaign)
	if err != nil {
		return writeError(stdout, stderr, true, "status_failed", err)
	}
	return writeProviderData(stdout, preview)
}

func runApproval(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "confirm" {
		fmt.Fprintln(stderr, "usage: agent-workflow approval confirm [--dir .] [--campaign id] [--option approve]")
		return 2
	}
	flags := flag.NewFlagSet("approval confirm", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", ".", "project directory")
	campaignFlag := flags.String("campaign", "", "Campaign id; optional for a single-Campaign project")
	option := flags.String("option", "approve", "approval option")
	jsonOutput := flags.Bool("json", true, "write a JSON response")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !*jsonOutput {
		return 2
	}
	runtime, campaign, root, code := loadLocalRuntime(*dir, contractsv1.Identifier(*campaignFlag), stdout, stderr)
	if code != 0 {
		return code
	}
	for attempt := 0; ; attempt++ {
		preview, err := runtime.PreviewApproval(context.Background(), campaign)
		if err != nil && !errors.Is(err, workflow.ErrApprovalAlreadyDecided) {
			if selected, ok, lookupErr := runtime.ExistingApprovalOption(context.Background(), campaign); lookupErr != nil {
				return writeError(stdout, stderr, true, "approval_unavailable", lookupErr)
			} else if !ok || selected != *option {
				return writeError(stdout, stderr, true, "approval_unavailable", err)
			}
		} else if err == nil {
			if _, err := runtime.ConfirmApproval(preview, *option); err != nil {
				return writeError(stdout, stderr, true, "approval_failed", err)
			}
		} else if selected, ok, lookupErr := runtime.ExistingApprovalOption(context.Background(), campaign); lookupErr != nil {
			return writeError(stdout, stderr, true, "approval_unavailable", lookupErr)
		} else if !ok || selected != *option {
			return writeError(stdout, stderr, true, "approval_conflict", errors.New("existing approval selected a different option"))
		}
		if _, err := runtime.Drive(context.Background(), campaign, 100); err == nil {
			break
		} else if !errors.Is(err, workflow.ErrCanonicalConflict) || attempt == maxConcurrentDeliveryAttempts-1 {
			return writeError(stdout, stderr, true, "resume_failed", err)
		}
		var reloadCode int
		runtime, campaign, _, reloadCode = loadLocalRuntime(root, campaign, stdout, stderr)
		if reloadCode != 0 {
			return reloadCode
		}
	}
	completed, err := runtime.Preview(context.Background(), campaign)
	if err != nil {
		return writeError(stdout, stderr, true, "resume_failed", err)
	}
	return writeProviderData(stdout, completed)
}

func runReplay(args []string, stdout, stderr io.Writer) int {
	runtime, campaign, _, code := openLocalRuntime(args, stdout, stderr, "replay")
	if code != 0 {
		return code
	}
	replay, err := runtime.Replay(campaign)
	if err != nil {
		return writeError(stdout, stderr, true, "replay_unavailable", err)
	}
	return writeProviderData(stdout, replay)
}

func openLocalRuntime(args []string, stdout, stderr io.Writer, name string) (*workflow.FixtureRuntime, contractsv1.Identifier, string, int) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", ".", "project directory")
	campaign := flags.String("campaign", "", "Campaign id; optional for a single-Campaign project")
	jsonOutput := flags.Bool("json", true, "write a JSON response")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !*jsonOutput {
		return nil, "", "", 2
	}
	return loadLocalRuntime(*dir, contractsv1.Identifier(*campaign), stdout, stderr)
}

func loadLocalRuntime(dir string, campaign contractsv1.Identifier, stdout, stderr io.Writer) (*workflow.FixtureRuntime, contractsv1.Identifier, string, int) {
	root := filepath.Clean(dir)
	if err := trustedProjectRoot(root); err != nil {
		return nil, "", root, writeError(stdout, stderr, true, "project_unsafe", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "agent-workflow.json"))
	if err != nil {
		return nil, "", root, writeError(stdout, stderr, true, "project_unavailable", errors.New("run agent-workflow init first"))
	}
	var fixture contractsv1.ConformanceFixture
	if err := contract.DecodeDefinition("ConformanceFixture", body, &fixture); err != nil {
		return nil, "", root, writeError(stdout, stderr, true, "project_invalid", err)
	}
	ledgerPath, err := localLedgerPath(root, false)
	if err != nil {
		return nil, "", root, writeError(stdout, stderr, true, "ledger_unavailable", err)
	}
	ledger, err := workflow.OpenFileLedger(ledgerPath)
	if err != nil {
		return nil, "", root, writeError(stdout, stderr, true, "ledger_unavailable", err)
	}
	runtime, err := workflow.NewFixtureRuntime(fixture, &demoProvider{results: make(map[string]workflow.ProviderResult)}, ledger)
	if err != nil {
		return nil, "", root, writeError(stdout, stderr, true, "project_invalid", err)
	}
	selected := campaign
	if selected == "" && len(fixture.Campaigns) == 1 {
		selected = fixture.Campaigns[0].Id
	}
	return runtime, selected, root, 0
}

func localLedgerPath(root string, allowMissing bool) (string, error) {
	dir := filepath.Join(root, ".agent-workflow")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return filepath.Join(dir, "ledger.jsonl"), nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New(".agent-workflow must be a real project directory")
	}
	if info.Mode().Perm() != 0o700 {
		return "", errors.New(".agent-workflow must use mode 0700")
	}
	path := filepath.Join(dir, "ledger.jsonl")
	if !allowMissing {
		info, err = os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("ledger must be an initialized regular file")
		}
	}
	return path, nil
}

func trustedProjectRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("project root must be a real directory")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("project root must not be group- or world-writable")
	}
	return nil
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
	var portfolios []contractsv1.CanvasPortfolioSnapshot
	var selectedJobID contractsv1.Identifier
	var history builderapi.DefinitionHistory
	if sourceCanvas != nil {
		portfolios, selectedJobID, history, err = restoreCanvasPortfolios(sourceCanvas, ledger)
		if err != nil {
			return writeError(stdout, stderr, true, "canvas_unavailable", err)
		}
		for index := range portfolios {
			portfolio, approvalErr := restorePortfolioApprovals(&portfolios[index], ledger)
			if approvalErr != nil {
				return writeError(stdout, stderr, true, "canvas_unavailable", approvalErr)
			}
			portfolios[index] = *portfolio
		}
	}
	readChangeCases := func(generatedAt time.Time) ([]contractsv1.ChangeCaseCanvas, error) {
		return restoreChangeCases(ledger, generatedAt)
	}
	if _, err := readChangeCases(time.Now().UTC()); err != nil {
		return writeError(stdout, stderr, true, "change_cases_unavailable", err)
	}
	var handler http.Handler = builderapi.NewWithControlPlanePortfoliosReader(core, time.Now, portfolios, selectedJobID, history, readChangeCases)
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
		handler, err = builderapi.NewWithWebMCPControlPlanePortfoliosReader(core, time.Now, portfolios, selectedJobID, history, readChangeCases, builderapi.WebMCPConfig{PageOrigin: *webOrigin, Audit: auditFile})
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
	portfolios, selectedJobID, history, err := restoreCanvasPortfolios(snapshot, ledger)
	if err != nil {
		return nil, builderapi.DefinitionHistory{}, err
	}
	for index := range portfolios {
		if portfolios[index].Job.Id == selectedJobID {
			return &portfolios[index], history, nil
		}
	}
	return nil, builderapi.DefinitionHistory{}, errors.New("selected Job is unavailable")
}

func restoreCanvasPortfolios(snapshot *contractsv1.CanvasSnapshot, ledger *workflow.FileLedger) ([]contractsv1.CanvasPortfolioSnapshot, contractsv1.Identifier, builderapi.DefinitionHistory, error) {
	replays, err := ledger.ReplaysByReceiptType(contractsv1.ReceiptReceiptTypeAdmission)
	if err != nil {
		return nil, "", builderapi.DefinitionHistory{}, err
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
				return nil, "", builderapi.DefinitionHistory{}, err
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
	type portfolioState struct {
		job         contractsv1.JobDefinition
		campaigns   []contractsv1.CanvasSnapshot
		selected    contractsv1.Identifier
		definitions map[contractsv1.Identifier]map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition
	}
	jobs := map[contractsv1.Identifier]contractsv1.JobDefinition{snapshot.Definition.Job.Id: snapshot.Definition.Job}
	campaignDefinitions := map[contractsv1.Identifier]contractsv1.CampaignDefinition{
		snapshot.Definition.Campaign.Id: snapshot.Definition.Campaign,
	}
	states := map[contractsv1.Identifier]*portfolioState{}
	definitionsByRef := map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition{}
	for _, definition := range snapshot.Definition.Workflows {
		definitionsByRef[contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version))] = definition
	}
	states[snapshot.Definition.Job.Id] = &portfolioState{job: snapshot.Definition.Job, campaigns: []contractsv1.CanvasSnapshot{*snapshot}, selected: snapshot.Definition.Campaign.Id, definitions: map[contractsv1.Identifier]map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition{snapshot.Definition.Campaign.Id: definitionsByRef}}
	selectedJobID := snapshot.Definition.Job.Id
	for _, item := range events {
		if existing, ok := jobs[item.admission.Job.Id]; ok && !reflect.DeepEqual(existing, item.admission.Job) {
			return nil, "", builderapi.DefinitionHistory{}, errors.New("canonical admissions contain conflicting definitions for one Job")
		}
		jobs[item.admission.Job.Id] = item.admission.Job
		if existing, ok := campaignDefinitions[item.admission.Campaign.Id]; ok && !reflect.DeepEqual(existing, item.admission.Campaign) {
			return nil, "", builderapi.DefinitionHistory{}, errors.New("canonical admissions contain conflicting definitions for one Campaign")
		}
		campaignDefinitions[item.admission.Campaign.Id] = item.admission.Campaign
		state := states[item.admission.Job.Id]
		if state == nil {
			state = &portfolioState{job: item.admission.Job, definitions: map[contractsv1.Identifier]map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition{}}
			states[item.admission.Job.Id] = state
		}
		definitionsByRef = state.definitions[item.admission.Campaign.Id]
		if definitionsByRef == nil {
			definitionsByRef = map[contractsv1.WorkflowRef]contractsv1.WorkflowDefinition{}
			state.definitions[item.admission.Campaign.Id] = definitionsByRef
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
			return nil, "", builderapi.DefinitionHistory{}, err
		}
		replaced := false
		for index := range state.campaigns {
			if state.campaigns[index].Definition.Campaign.Id == next.Definition.Campaign.Id {
				state.campaigns[index] = canvas.MergeAdmissionReadback(state.campaigns[index], next)
				replaced = true
				break
			}
		}
		if !replaced {
			state.campaigns = append(state.campaigns, next)
		}
		state.selected = next.Definition.Campaign.Id
		selectedJobID = item.admission.Job.Id
	}
	jobIDs := make([]contractsv1.Identifier, 0, len(states))
	for jobID := range states {
		jobIDs = append(jobIDs, jobID)
	}
	sort.Slice(jobIDs, func(i, j int) bool { return jobIDs[i] < jobIDs[j] })
	portfolios := make([]contractsv1.CanvasPortfolioSnapshot, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		state := states[jobID]
		portfolio, err := canvas.ProjectPortfolio(state.job, state.campaigns, state.selected)
		if err != nil {
			return nil, "", builderapi.DefinitionHistory{}, err
		}
		portfolios = append(portfolios, portfolio)
	}
	history := builderapi.DefinitionHistory{}
	for _, item := range jobs {
		history.Jobs = append(history.Jobs, item)
	}
	for _, item := range campaignDefinitions {
		history.Campaigns = append(history.Campaigns, item)
	}
	sort.Slice(history.Jobs, func(i, j int) bool { return history.Jobs[i].Id < history.Jobs[j].Id })
	sort.Slice(history.Campaigns, func(i, j int) bool { return history.Campaigns[i].Id < history.Campaigns[j].Id })
	return portfolios, selectedJobID, history, nil
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

func restoreChangeCases(ledger *workflow.FileLedger, generatedAt time.Time) ([]contractsv1.ChangeCaseCanvas, error) {
	replays, err := ledger.ReplaysByReceiptTypes(
		contractsv1.ReceiptReceiptTypeChangeProposed,
		contractsv1.ReceiptReceiptTypeChangeMerged,
		contractsv1.ReceiptReceiptTypeConflictDetected,
		contractsv1.ReceiptReceiptTypeResolutionProposed,
		contractsv1.ReceiptReceiptTypeResolutionApproved,
		contractsv1.ReceiptReceiptTypeMutationLeaseAcquired,
		contractsv1.ReceiptReceiptTypeMutationApplied,
		contractsv1.ReceiptReceiptTypeMutationReadback,
		contractsv1.ReceiptReceiptTypeResourceGenerationAdvanced,
	)
	if err != nil {
		return nil, err
	}
	result := make([]contractsv1.ChangeCaseCanvas, 0, len(replays))
	for _, replay := range replays {
		projected, err := canvas.ProjectChangeCase(replay, generatedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, projected)
	}
	return result, nil
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
	result := response{Kind: "cli_response", SchemaVersion: 1, OK: true, WorkflowRef: identity.Ref, Hash: identity.Hash}
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
		Kind          string `json:"kind"`
		SchemaVersion int    `json:"schema_version"`
		OK            bool   `json:"ok"`
		Data          any    `json:"data"`
	}{Kind: "cli_response", SchemaVersion: 1, OK: true, Data: data})
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
		_ = json.NewEncoder(stdout).Encode(response{Kind: "cli_response", SchemaVersion: 1, OK: false, Error: err.Error(), Code: code})
	} else {
		fmt.Fprintln(stderr, err)
	}
	return 1
}
