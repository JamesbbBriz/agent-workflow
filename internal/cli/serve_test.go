package cli_test

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/cli"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestServeReadsTheCanonicalCLIProjectLedger(t *testing.T) {
	project := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"init", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("init code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run([]string{"run", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("run code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "agent-workflow")
	build := exec.Command("go", "build", "-o", binary, "./cmd/agent-workflow")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build serve binary: %v\n%s", err, output)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "serve", "--listen", address, "--dir", project, "--web-origin", "http://"+address)
	serverCWD := t.TempDir()
	command.Dir = serverCWD
	var serverOutput bytes.Buffer
	command.Stdout, command.Stderr = &serverOutput, &serverOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, requestErr := client.Get("http://" + address + "/v1/evidence-report")
		if requestErr == nil {
			var body struct {
				OK   bool                             `json:"ok"`
				Data contractsv1.EvidenceWindowReport `json:"data"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && body.OK && body.Data.Counts.Receipts > 0 {
				controlResponse, controlErr := client.Get("http://" + address + "/v1/control-plane")
				if controlErr != nil {
					continue
				}
				var control struct {
					OK   bool                             `json:"ok"`
					Data contractsv1.ControlPlaneSnapshot `json:"data"`
				}
				controlDecodeErr := json.NewDecoder(controlResponse.Body).Decode(&control)
				_ = controlResponse.Body.Close()
				if controlResponse.StatusCode == http.StatusOK && controlDecodeErr == nil && control.OK && len(control.Data.Portfolios) == 1 && control.Data.Portfolios[0].Job.Id == "generic-project-job" {
					if _, statErr := os.Stat(filepath.Join(project, ".agent-workflow", "webmcp-audit.jsonl")); statErr != nil {
						t.Fatalf("project-bound WebMCP audit was not created: %v", statErr)
					}
					if _, statErr := os.Stat(filepath.Join(serverCWD, ".agent-workflow", "webmcp-audit.jsonl")); !os.IsNotExist(statErr) {
						t.Fatalf("WebMCP audit escaped into invocation CWD: %v", statErr)
					}
					stdout.Reset()
					stderr.Reset()
					if code := cli.Run([]string{"approval", "confirm", "--dir", project}, &stdout, &stderr); code != 0 {
						t.Fatalf("approval confirm code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
					}
					refreshDeadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(refreshDeadline) {
						refreshedResponse, refreshErr := client.Get("http://" + address + "/v1/canvas")
						if refreshErr == nil {
							var refreshed struct {
								OK   bool                       `json:"ok"`
								Data contractsv1.CanvasSnapshot `json:"data"`
							}
							decodeRefreshErr := json.NewDecoder(refreshedResponse.Body).Decode(&refreshed)
							_ = refreshedResponse.Body.Close()
							if decodeRefreshErr == nil && refreshed.OK {
								canvas := refreshed.Data
								if len(canvas.Executions) == 1 && canvas.Executions[0].ApprovalState == contractsv1.CanvasExecutionApprovalStateApproved && len(canvas.Executions[0].Outputs) == 1 && canvas.Executions[0].Outputs[0].ApprovalState == contractsv1.ActionArtifactApprovalStateApproved && len(canvas.ApprovalReplays) == 1 {
									return
								}
							}
						}
						time.Sleep(50 * time.Millisecond)
					}
					t.Fatal("Canvas did not reproject the post-start CLI approval")
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("serve did not read CLI evidence from %s: %s", project, serverOutput.String())
}
