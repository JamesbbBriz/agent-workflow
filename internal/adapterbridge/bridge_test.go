package adapterbridge

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestBundledBridgesTranslateTheSameInvocation(t *testing.T) {
	tests := []struct {
		id    contractsv1.ProviderID
		first string
	}{
		{contractsv1.ProviderIDCodex, "exec"},
		{contractsv1.ProviderIDClaudeCode, "-p"},
		{contractsv1.ProviderIDPi, "-p"},
		{contractsv1.ProviderIDOpenclaw, "agent"},
		{contractsv1.ProviderIDHermesAgent, "-z"},
	}
	for _, test := range tests {
		t.Run(string(test.id), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "output"), 0700); err != nil {
				t.Fatal(err)
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
			inputW.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
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
	invocation := workflow.Invocation{IdempotencyKey: invocationID, JobID: "job", CampaignID: "campaign", WorkflowRef: "workflow@1", Node: contractsv1.NodeDefinition{Id: "research", OutputSlots: []contractsv1.Slot{{Id: "recommendation", ArtifactType: "recommendation", MinItems: 1, MaxItems: 1}}}, InputHashes: []contractsv1.SHA256{"sha256:1111111111111111111111111111111111111111111111111111111111111111"}, Deadline: deadline, ExecutorProfile: &profile}
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
