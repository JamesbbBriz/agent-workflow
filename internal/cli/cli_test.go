package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JamesbbBriz/agent-workflow/internal/cli"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestValidateReportsStableWorkflowIdentity(t *testing.T) {
	t.Parallel()
	_, current, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	var stdout, stderr bytes.Buffer

	exit := cli.Run([]string{"validate", "--file", filepath.Join(root, "examples", "research-review.workflow.json"), "--json"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("validate exited %d: %s", exit, stderr.String())
	}
	var response struct {
		OK          bool   `json:"ok"`
		WorkflowRef string `json:"workflow_ref"`
		Hash        string `json:"hash"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout.String())
	}
	if !response.OK || response.WorkflowRef != "research-review@1" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Hash != "sha256:f9a457a90150f68644d041aa158c9ad8589bf70d7ad75ac8ffabecffd103bf3a" {
		t.Fatalf("unexpected workflow hash: %s", response.Hash)
	}
}

func TestValidateRejectsUnsafeDefinitions(t *testing.T) {
	t.Parallel()
	_, current, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	original, err := os.ReadFile(filepath.Join(root, "examples", "research-review.workflow.json"))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown field", mutate: func(document map[string]any) { document["surprise"] = true }},
		{name: "unknown version", mutate: func(document map[string]any) { document["schema_version"] = float64(2) }},
		{name: "wrong definition hash", mutate: func(document map[string]any) { document["definition_hash"] = "sha256:" + strings.Repeat("0", 64) }},
		{name: "unknown dependency", mutate: func(document map[string]any) {
			document["nodes"].([]any)[0].(map[string]any)["depends_on"] = []any{"missing"}
		}},
		{name: "dependency cycle", mutate: func(document map[string]any) {
			document["nodes"].([]any)[0].(map[string]any)["depends_on"] = []any{"review"}
		}},
		{name: "incomplete identity", mutate: func(document map[string]any) { delete(document, "id") }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var document map[string]any
			if err := json.Unmarshal(original, &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document)
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "workflow.json")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if exit := cli.Run([]string{"validate", "--file", path, "--json"}, &stdout, &stderr); exit != 1 {
				t.Fatalf("expected invalid workflow exit, got %d: %s", exit, stdout.String())
			}
			var response struct {
				OK   bool   `json:"ok"`
				Code string `json:"code"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("decode error response: %v: %s", err, stdout.String())
			}
			if response.OK || response.Code != "invalid_workflow" {
				t.Fatalf("unexpected error response: %+v", response)
			}
		})
	}
}

func TestValidateRejectsDuplicateJSONFields(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.json")
	if err := os.WriteFile(path, []byte(`{"kind":"workflow_definition","kind":"workflow_definition"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exit := cli.Run([]string{"validate", "--file", path, "--json"}, &stdout, &stderr); exit != 1 {
		t.Fatalf("expected invalid workflow exit, got %d: %s", exit, stdout.String())
	}
	if !strings.Contains(stdout.String(), "duplicate object field") {
		t.Fatalf("expected duplicate-field error, got %s", stdout.String())
	}
}

func TestDemoRunsOneContextBoundNodeAndReturnsReplay(t *testing.T) {
	t.Parallel()
	_, current, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{
		"demo", "--file", filepath.Join(root, "examples", "research-review.workflow.json"),
		"--at", "2026-08-27T00:00:00Z", "--json",
	}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("demo exited %d: %s", exit, stderr.String())
	}
	var repeatedOut, repeatedErr bytes.Buffer
	if exit := cli.Run([]string{
		"demo", "--file", filepath.Join(root, "examples", "research-review.workflow.json"),
		"--at", "2026-08-27T00:00:00Z", "--json",
	}, &repeatedOut, &repeatedErr); exit != 0 || !bytes.Equal(stdout.Bytes(), repeatedOut.Bytes()) {
		t.Fatalf("fixed --at demo changed: exit=%d first=%s second=%s stderr=%s", exit, stdout.String(), repeatedOut.String(), repeatedErr.String())
	}
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			Artifacts []any `json:"artifacts"`
			Replay    struct {
				Receipts []any `json:"receipts"`
			} `json:"replay"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode demo response: %v\n%s", err, stdout.String())
	}
	if !response.OK || len(response.Data.Artifacts) != 1 || len(response.Data.Replay.Receipts) != 7 {
		t.Fatalf("unexpected demo response: %s", stdout.String())
	}
}

func TestLocalProjectGoldenPathPersistsApprovalAndReplay(t *testing.T) {
	dir := t.TempDir()
	call := func(args ...string) map[string]any {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := cli.Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v exited %d: stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
		var response map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
			t.Fatalf("decode %v: %v\n%s", args, err, stdout.String())
		}
		if response["kind"] != "cli_response" || response["schema_version"] != float64(1) {
			t.Fatalf("unversioned response for %v: %#v", args, response)
		}
		return response
	}

	initialized := call("init", "--dir", dir)
	if initialized["ok"] != true || initialized["data"].(map[string]any)["kind"] != "local_project_init" {
		t.Fatalf("init response: %#v", initialized)
	}
	if info, err := os.Stat(filepath.Join(dir, ".agent-workflow", "ledger.jsonl")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secure ledger was not created: info=%v err=%v", info, err)
	}
	var disabledOut, disabledErr bytes.Buffer
	if code := cli.Run([]string{"doctor", "--dir", dir, "--json=false"}, &disabledOut, &disabledErr); code != 2 || disabledOut.Len() != 0 {
		t.Fatalf("dead --json=false control was accepted: code=%d stdout=%s", code, disabledOut.String())
	}
	doctor := call("doctor", "--dir", dir, "--json")
	doctorData := doctor["data"].(map[string]any)
	if doctorData["kind"] != "local_project_doctor" || doctorData["ready"] != true || doctorData["provider"].(map[string]any)["production"] != false || doctorData["storage"].(map[string]any)["mode"] != "-rw-------" {
		t.Fatalf("doctor omitted readiness or storage security: %#v", doctor)
	}
	if body, err := os.ReadFile(filepath.Join(dir, ".agent-workflow", "ledger.jsonl")); err != nil || len(body) != 0 {
		t.Fatalf("read-only doctor changed the ledger: bytes=%d err=%v", len(body), err)
	}

	waiting := call("run", "--dir", dir, "--json")
	waitingData := waiting["data"].(map[string]any)
	state := waitingData["state"].(map[string]any)
	if state["status"] != "running" || waitingData["next_action"] != "wait" || state["nodes"].([]any)[1].(map[string]any)["status"] != "awaiting_approval" {
		t.Fatalf("run did not stop at approval: %#v", waiting)
	}

	status := call("status", "--dir", dir)
	if status["data"].(map[string]any)["next_action"] != "wait" {
		t.Fatalf("restart did not recover approval state: %#v", status)
	}

	completed := call("approval", "confirm", "--dir", dir)
	if completed["data"].(map[string]any)["state"].(map[string]any)["status"] != "completed" {
		t.Fatalf("approval did not resume Campaign: %#v", completed)
	}

	replay := call("replay", "--dir", dir)
	if replay["data"].(map[string]any)["bundle_hash"] == "" || len(replay["data"].(map[string]any)["receipts"].([]any)) == 0 {
		t.Fatalf("replay was not exported: %#v", replay)
	}
	report := call("report", "--dir", dir)
	reportData := report["data"].(map[string]any)
	if reportData["kind"] != "evidence_window_report" || len(reportData["available_role_ids"].([]any)) != 3 || len(reportData["invoked_role_ids"].([]any)) == 0 || reportData["counts"].(map[string]any)["receipts"].(float64) == 0 {
		t.Fatalf("report omitted workforce or evidence: %#v", report)
	}
	ledgerBeforeMarkdown, err := os.ReadFile(filepath.Join(dir, ".agent-workflow", "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var markdownOut, markdownErr bytes.Buffer
	if code := cli.Run([]string{"report", "--dir", dir, "--format", "markdown"}, &markdownOut, &markdownErr); code != 0 || !strings.Contains(markdownOut.String(), "# Evidence window report") {
		t.Fatalf("Markdown report failed: code=%d stdout=%s stderr=%s", code, markdownOut.String(), markdownErr.String())
	}
	ledgerAfterMarkdown, err := os.ReadFile(filepath.Join(dir, ".agent-workflow", "ledger.jsonl"))
	if err != nil || !bytes.Equal(ledgerBeforeMarkdown, ledgerAfterMarkdown) {
		t.Fatalf("report mutated ledger: err=%v", err)
	}

	redelivered := call("init", "--dir", dir)
	if redelivered["ok"] != true {
		t.Fatalf("idempotent init failed: %#v", redelivered)
	}
}

func TestLocalApprovalConfirmResumesAnAlreadyPersistedDecision(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--dir", dir}, {"run", "--dir", dir}} {
		var stdout, stderr bytes.Buffer
		if code := cli.Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v: code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, "agent-workflow.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture contractsv1.ConformanceFixture
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	ledger, err := workflow.OpenFileLedger(filepath.Join(dir, ".agent-workflow", "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workflow.NewFixtureRuntime(fixture, nil, ledger)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := runtime.PreviewApproval(t.Context(), "research-campaign")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ConfirmApproval(preview, "approve"); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"approval", "confirm", "--dir", dir, "--option=reject"}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "approval_conflict") {
		t.Fatalf("conflicting retry was accepted: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run([]string{"approval", "confirm", "--dir", dir, "--option=approve"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"status":"completed"`) {
		t.Fatalf("retry did not resume: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestDoctorRejectsInvalidCampaignPlanWithoutWritingLedger(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"init", "--dir", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("init: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	definitionPath := filepath.Join(dir, "agent-workflow.json")
	body, err := os.ReadFile(definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	var definition map[string]any
	if err := json.Unmarshal(body, &definition); err != nil {
		t.Fatal(err)
	}
	definition["campaigns"].([]any)[0].(map[string]any)["workflow_plan"] = []any{"missing-workflow@1"}
	body, err = json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definitionPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(dir, ".agent-workflow", "ledger.jsonl")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run([]string{"doctor", "--dir", dir}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "core_not_ready") {
		t.Fatalf("doctor accepted invalid plan: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("doctor mutated ledger: err=%v before=%q after=%q", err, before, after)
	}
}

func TestReadOnlyLocalCommandsPreserveTornLedger(t *testing.T) {
	for _, command := range []string{"doctor", "status", "replay"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			var stdout, stderr bytes.Buffer
			if code := cli.Run([]string{"init", "--dir", dir}, &stdout, &stderr); code != 0 {
				t.Fatalf("init: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			ledgerPath := filepath.Join(dir, ".agent-workflow", "ledger.jsonl")
			torn := []byte(`{"kind":`)
			if err := os.WriteFile(ledgerPath, torn, 0o600); err != nil {
				t.Fatal(err)
			}
			stdout.Reset()
			stderr.Reset()
			if code := cli.Run([]string{command, "--dir", dir}, &stdout, &stderr); code != 1 {
				t.Fatalf("%s accepted torn ledger: code=%d stdout=%s stderr=%s", command, code, stdout.String(), stderr.String())
			}
			after, err := os.ReadFile(ledgerPath)
			if err != nil || !bytes.Equal(after, torn) {
				t.Fatalf("%s changed torn ledger: after=%q err=%v", command, after, err)
			}
		})
	}
}

func TestLocalCommandsConvergeConcurrentDeliveries(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"init", "--dir", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("init: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	concurrent := func(args ...string) {
		t.Helper()
		start := make(chan struct{})
		results := make(chan string, 2)
		for range 2 {
			go func() {
				<-start
				var out, errOut bytes.Buffer
				code := cli.Run(args, &out, &errOut)
				results <- fmt.Sprintf("code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
			}()
		}
		close(start)
		for range 2 {
			result := <-results
			if !strings.HasPrefix(result, "code=0 ") {
				t.Fatal(result)
			}
		}
	}
	concurrent("run", "--dir", dir)
	concurrent("approval", "confirm", "--dir", dir)
}

func TestLocalProjectRejectsWritableSharedRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"init", "--dir", dir}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "project_unsafe") {
		t.Fatalf("shared root was accepted: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestLocalProjectInitRefusesToOverwriteDifferentDefinition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-workflow.json")
	if err := os.WriteFile(path, []byte(`{"owned_by":"user"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"init", "--dir", dir}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "project_exists") {
		t.Fatalf("init overwrote or misreported existing project: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != `{"owned_by":"user"}` {
		t.Fatalf("existing project changed: body=%s err=%v", body, err)
	}
}

func TestLocalProjectInitRejectsSymlinkedDefinition(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "user-owned.json")
	if err := os.WriteFile(target, []byte(`{"owned_by":"user"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "agent-workflow.json")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"init", "--dir", dir}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "project_unsafe") {
		t.Fatalf("symlinked definition was accepted: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != `{"owned_by":"user"}` {
		t.Fatalf("symlink target changed: body=%s err=%v", body, err)
	}
}

func TestLocalProjectInitRejectsSymlinkedStateDirectory(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, ".agent-workflow")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"init", "--dir", dir}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "ledger_unavailable") {
		t.Fatalf("symlinked state directory was accepted: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(target, "ledger.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ledger escaped the project root: %v", err)
	}
}

func TestConformanceCommandEmitsMachineReadableReport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"conformance", "--file", "../../conformance/fixtures/generic.json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("conformance failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var report contractsv1.ConformanceReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || !report.Passed || report.ToolVersion != "development" {
		t.Fatalf("invalid conformance report: %+v err=%v", report, err)
	}
}

func TestProviderListAndDoctorExposeReadinessWithoutSecrets(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"provider", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("provider list code=%d stderr=%s", code, stderr.String())
	}
	var listed struct {
		OK   bool `json:"ok"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil || !listed.OK || len(listed.Data) != 5 {
		t.Fatalf("provider list=%s err=%v", stdout.String(), err)
	}

	t.Setenv("OPENAI_API_KEY", "never-print-this-secret")
	stdout.Reset()
	if code := cli.Run([]string{"provider", "doctor", "--id", "codex"}, &stdout, &stderr); code != 0 {
		t.Fatalf("provider doctor code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "never-print-this-secret") || !strings.Contains(stdout.String(), `"descriptor"`) {
		t.Fatalf("provider doctor leaked or omitted readiness: %s", stdout.String())
	}
}

func TestBuilderRejectsRemoteListenAddress(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"builder", "--listen", "0.0.0.0:4321"}, &stdout, &stderr); code == 0 {
		t.Fatalf("remote Builder listener was accepted: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "invalid_listen_address") {
		t.Fatalf("unexpected error: %s %s", stdout.String(), stderr.String())
	}
}

func TestBuilderRejectsCanonicalAndAuditPathCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.jsonl")
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"builder", "--listen", "127.0.0.1:0", "--ledger", path, "--web-origin", "http://127.0.0.1:5173", "--webmcp-audit", path}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stdout.String(), `"code":"path_collision"`) {
		t.Fatalf("collision code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("collision created or modified the canonical path")
	}
}

func TestBuilderRejectsHardLinkedAuditPath(t *testing.T) {
	directory := t.TempDir()
	ledger := filepath.Join(directory, "ledger.jsonl")
	audit := filepath.Join(directory, "audit.jsonl")
	if err := os.WriteFile(ledger, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(ledger, audit); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"builder", "--listen", "127.0.0.1:0", "--ledger", ledger, "--web-origin", "http://127.0.0.1:5173", "--webmcp-audit", audit}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stdout.String(), `"code":"path_collision"`) {
		t.Fatalf("hard-link collision code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestBuilderRejectsDanglingAuditSymlinkToLedger(t *testing.T) {
	directory := t.TempDir()
	ledger := filepath.Join(directory, "ledger.jsonl")
	audit := filepath.Join(directory, "audit.jsonl")
	if err := os.Symlink(filepath.Base(ledger), audit); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"builder", "--listen", "127.0.0.1:0", "--ledger", ledger, "--web-origin", "http://127.0.0.1:5173", "--webmcp-audit", audit}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stdout.String(), `"code":"path_collision"`) {
		t.Fatalf("dangling-symlink collision code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Fatal("dangling-symlink collision created the ledger")
	}
}
