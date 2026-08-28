package adapterbridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestBundledBridgesTranslateTheSameInvocation(t *testing.T) {
	tests := []struct {
		id       contractsv1.ProviderID
		first    string
		contains []string
		excludes []string
	}{
		{contractsv1.ProviderIDCodex, "exec", []string{"--sandbox\nread-only\n", "--ephemeral\n", "--ignore-user-config\n", "--ignore-rules\n"}, nil},
		{contractsv1.ProviderIDClaudeCode, "-p", []string{"--bare\n", "--tools\nRead,Glob,Grep\n", "--allowedTools\nRead,Glob,Grep\n"}, nil},
		{contractsv1.ProviderIDPi, "-p", []string{"--no-extensions\n", "--no-skills\n", "--tools\nread,grep,find,ls\n"}, nil},
		{contractsv1.ProviderIDOpenclaw, "agent", []string{"--local\n", "--agent\ntest\n"}, nil},
		{contractsv1.ProviderIDHermesAgent, "-z", []string{"--toolsets\nvision\n", "--ignore-user-config\n", "--ignore-rules\n"}, []string{"--safe-mode\n", "--toolsets\nfile\n"}},
	}
	for _, test := range tests {
		t.Run(string(test.id), func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "input", "providers"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "output"), 0700); err != nil {
				t.Fatal(err)
			}
			if test.id == contractsv1.ProviderIDOpenclaw {
				config := `{"agents":{"entries":{"test":{"workspace":"/workspace","tools":{"allow":["read"]}}}},"gateway":{"mode":"remote"}}`
				if err := os.WriteFile(filepath.Join(root, "input", "providers", "test.json"), []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			argsPath := filepath.Join(root, "args")
			script := filepath.Join(root, "upstream")
			body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argsPath) + "\nprintf '%s\\n' '{\"outputs\":{\"recommendation\":{\"recommendation\":\"bounded\"}}}'\n"
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			inputR, inputW := ioPipe(t)
			outputR, outputW := ioPipe(t)
			done := make(chan error, 1)
			go func() { done <- Run(Config{Provider: test.id, Upstream: script, WorkspaceRoot: root}, inputR, outputW) }()
			encoder := json.NewEncoder(inputW)
			decoder := json.NewDecoder(bufio.NewReader(outputR))
			request := providerStartRequest(t, test.id)
			if err := encoder.Encode(request); err != nil {
				t.Fatal(err)
			}
			var response contractsv1.ProviderProtocolResponse
			if err := decoder.Decode(&response); err != nil || response.Run == nil {
				t.Fatalf("start response=%#v err=%v", response, err)
			}
			runRef := response.Run.RunRef
			for i := 0; i < 1000; i++ {
				if err := encoder.Encode(contractsv1.ProviderProtocolRequest{ProtocolVersion: 1, RequestId: "inspect", Operation: contractsv1.ProviderProtocolRequestOperationInspect, RunRef: &runRef}); err != nil {
					t.Fatal(err)
				}
				response = contractsv1.ProviderProtocolResponse{}
				if err := decoder.Decode(&response); err != nil {
					t.Fatal(err)
				}
				if response.Observation != nil && response.Observation.Status == contractsv1.ProviderObservationStatusSucceeded {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if response.Observation == nil || response.Observation.Status != contractsv1.ProviderObservationStatusSucceeded || response.Observation.OutputHash == nil {
				t.Fatalf("terminal observation=%#v", response.Observation)
			}
			args, err := os.ReadFile(argsPath)
			if err != nil || !strings.HasPrefix(string(args), test.first+"\n") {
				t.Fatalf("upstream args=%q err=%v want first %q", args, err, test.first)
			}
			for _, value := range test.contains {
				if !strings.Contains(string(args), value) {
					t.Fatalf("upstream args=%q missing %q", args, value)
				}
			}
			for _, value := range test.excludes {
				if strings.Contains(string(args), value) {
					t.Fatalf("upstream args=%q unexpectedly contains %q", args, value)
				}
			}
			inputW.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenClawBridgeRejectsProfileWithoutExactToolAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "input", "providers"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "output"), 0700); err != nil {
		t.Fatal(err)
	}
	config := `{"agents":{"entries":{"test":{"workspace":"/workspace","tools":{"allow":["read","exec"]}}}}}`
	if err := os.WriteFile(filepath.Join(root, "input", "providers", "test.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	inputR, inputW := ioPipe(t)
	outputR, outputW := ioPipe(t)
	done := make(chan error, 1)
	go func() {
		done <- Run(Config{Provider: contractsv1.ProviderIDOpenclaw, Upstream: filepath.Join(root, "missing"), WorkspaceRoot: root}, inputR, outputW)
	}()
	if err := json.NewEncoder(inputW).Encode(providerStartRequest(t, contractsv1.ProviderIDOpenclaw)); err != nil {
		t.Fatal(err)
	}
	var response contractsv1.ProviderProtocolResponse
	if err := json.NewDecoder(bufio.NewReader(outputR)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ResponseType != contractsv1.ProviderProtocolResponseResponseTypeError {
		t.Fatalf("response=%#v want tool authority rejection", response)
	}
	inputW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBridgeAcceptsCoreSizedProtocolRequest(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "input"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "output"), 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "upstream")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"outputs\":{\"recommendation\":{\"recommendation\":\"bounded\"}}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	inputR, inputW := ioPipe(t)
	outputR, outputW := ioPipe(t)
	done := make(chan error, 1)
	go func() {
		done <- Run(Config{Provider: contractsv1.ProviderIDCodex, Upstream: script, WorkspaceRoot: root}, inputR, outputW)
	}()
	request := providerStartRequest(t, contractsv1.ProviderIDCodex)
	request.Invocation["padding"] = ""
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Invocation["padding"] = strings.Repeat("x", contract.MaxDocumentBytes-len(body))
	body, err = json.Marshal(request)
	if err != nil || len(body) != contract.MaxDocumentBytes {
		t.Fatalf("protocol request bytes=%d err=%v", len(body), err)
	}
	if _, err := inputW.Write(append(body, '\n')); err != nil {
		t.Fatal(err)
	}
	var response contractsv1.ProviderProtocolResponse
	if err := json.NewDecoder(bufio.NewReader(outputR)).Decode(&response); err != nil || response.Run == nil {
		t.Fatalf("core-sized start response=%#v err=%v", response, err)
	}
	inputW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	request.Invocation["padding"] = request.Invocation["padding"].(string) + "x"
	body, err = json.Marshal(request)
	if err != nil || len(body) != contract.MaxDocumentBytes+1 {
		t.Fatalf("oversized protocol request bytes=%d err=%v", len(body), err)
	}
	if err := Run(Config{Provider: contractsv1.ProviderIDCodex, Upstream: script, WorkspaceRoot: root}, bytes.NewReader(append(body, '\n')), io.Discard); err == nil {
		t.Fatal("oversized protocol request was accepted")
	}
}

func providerStartRequest(t *testing.T, id contractsv1.ProviderID) contractsv1.ProviderProtocolRequest {
	t.Helper()
	descriptor, err := workflow.ProviderDescriptor(id)
	if err != nil {
		t.Fatal(err)
	}
	secretRefs := make([]string, len(descriptor.AuthEnvironment))
	for i, name := range descriptor.AuthEnvironment {
		secretRefs[i] = "env:" + name
	}
	profile, err := workflow.SealExecutorProfile(contractsv1.ExecutorProfile{Kind: contractsv1.ExecutorProfileKindExecutorProfile, SchemaVersion: 1, ProviderId: id, ProviderVersion: "test", AdapterVersion: descriptor.AdapterVersion, ModelRef: "test-model", ConfigRef: "test", Capabilities: descriptor.Capabilities, IsolationProfile: contractsv1.ProviderIsolationProfileStagedSubprocess, ToolAllowlist: []string{"read-evidence"}, SecretRefs: secretRefs})
	if err != nil {
		t.Fatal(err)
	}
	invocationID := "same-admitted-node"
	deadline := time.Now().Add(time.Minute).UTC()
	workspace := contractsv1.ProviderProtocolRequestStagedWorkspaceWorkspace
	capabilities := contractsv1.CapabilityManifest{Kind: contractsv1.CapabilityManifestKindCapabilityManifest, SchemaVersion: 1, Id: "capability-read-evidence", Capabilities: []contractsv1.CapabilityManifestCapabilitiesElem{{Name: "read-evidence", Authority: contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}}}
	capabilityHash, err := workflow.Digest(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	capabilities.ManifestHash = contractsv1.SHA256(capabilityHash)
	invocation := workflow.Invocation{IdempotencyKey: invocationID, JobID: "job", CampaignID: "campaign", WorkflowRef: "workflow@1", Node: contractsv1.NodeDefinition{Id: "research", OutputSlots: []contractsv1.Slot{{Id: "recommendation", ArtifactType: "recommendation", MinItems: 1, MaxItems: 1}}}, InputHashes: []contractsv1.SHA256{"sha256:1111111111111111111111111111111111111111111111111111111111111111"}, Capabilities: capabilities, Deadline: deadline, ExecutorProfile: &profile}
	body, err := json.Marshal(invocation)
	if err != nil {
		t.Fatal(err)
	}
	var material contractsv1.ProviderProtocolRequestInvocation
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&material); err != nil {
		t.Fatal(err)
	}
	manifestHash, err := workflow.Digest(invocation.InputHashes)
	if err != nil {
		t.Fatal(err)
	}
	outputHash, err := workflow.Digest(invocation.Node.OutputSlots)
	if err != nil {
		t.Fatal(err)
	}
	manifest, output := contractsv1.SHA256(manifestHash), contractsv1.SHA256(outputHash)
	return contractsv1.ProviderProtocolRequest{ProtocolVersion: 1, RequestId: "start", Operation: contractsv1.ProviderProtocolRequestOperationStart, InvocationId: &invocationID, IdempotencyKey: &invocationID, Deadline: &deadline, StagedWorkspace: &workspace, InputManifestHash: &manifest, OutputContractHash: &output, ExecutorProfile: &profile, Invocation: material}
}

func ioPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
