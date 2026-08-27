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
	if response.Hash != "sha256:8b301a63ee614ed93360118f32deb55355d38047342037336b95efc5b5d5a08e" {
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
