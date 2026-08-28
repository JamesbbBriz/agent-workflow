package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JamesbbBriz/agent-workflow/internal/cli"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
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
