package workflow

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestBundledProviderRegistryAndProfilesAreClosed(t *testing.T) {
	descriptors := BundledProviderDescriptors()
	if len(descriptors) != 5 {
		t.Fatalf("bundled providers=%d want=5", len(descriptors))
	}
	want := []contractsv1.ProviderID{contractsv1.ProviderIDCodex, contractsv1.ProviderIDClaudeCode, contractsv1.ProviderIDPi, contractsv1.ProviderIDOpenclaw, contractsv1.ProviderIDHermesAgent}
	for i, descriptor := range descriptors {
		if descriptor.Id != want[i] {
			t.Fatalf("provider[%d]=%s want=%s", i, descriptor.Id, want[i])
		}
		if err := contract.ValidateDefinition("ProviderDescriptor", descriptor); err != nil {
			t.Fatal(err)
		}
	}
	descriptors[0].Capabilities[0] = "unknown"
	fresh, _ := ProviderDescriptor(contractsv1.ProviderIDCodex)
	if fresh.Capabilities[0] == "unknown" {
		t.Fatal("provider registry was mutable through a returned slice")
	}

	profile := testExecutorProfile(t, "config-a")
	changed := testExecutorProfile(t, "config-b")
	if profile.ConfigHash == changed.ConfigHash {
		t.Fatal("provider config change did not create a new attempt identity")
	}
	forged := profile
	forged.ConfigHash = repeatedSHA('0')
	if VerifyExecutorProfile(forged) == nil {
		t.Fatal("forged executor profile hash was accepted")
	}
}

func TestBundledUpstreamResolutionIgnoresUnexposedUserPath(t *testing.T) {
	dir := t.TempDir()
	name := "agent-workflow-path-only-upstream"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := resolveSystemExecutable(name); err == nil {
		t.Fatal("readiness accepted an upstream path that the provider sandbox cannot expose")
	}
}

func TestProviderAttemptReservationBlocksUncertainRestart(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "output"), 0700); err != nil {
		t.Fatal(err)
	}
	provider := &AgentRunnerProvider{sandbox: &SubprocessProvider{config: SubprocessProviderConfig{StagedRoot: root}}}
	invocation := Invocation{IdempotencyKey: "attempt-1"}
	created, recovered, err := provider.reserveAttempt(invocation)
	if err != nil || !created || recovered != nil {
		t.Fatalf("first reservation created=%v recovered=%#v err=%v", created, recovered, err)
	}
	if _, _, err := provider.reserveAttempt(invocation); err == nil {
		t.Fatal("uncertain provider attempt was started again after restart")
	}
	created, recovered, err = provider.reserveAttempt(Invocation{IdempotencyKey: "attempt-2"})
	if err != nil || !created || recovered != nil {
		t.Fatalf("second identity reservation created=%v recovered=%#v err=%v", created, recovered, err)
	}
}

func TestProviderProtocolFailsClosed(t *testing.T) {
	request := contractsv1.ProviderProtocolRequest{ProtocolVersion: 1, RequestId: "r1", Operation: contractsv1.ProviderProtocolRequestOperationStart}
	if validateProtocolRequest(request) == nil {
		t.Fatal("incomplete start request was accepted")
	}
	response := contractsv1.ProviderProtocolResponse{
		ProtocolVersion: 1, RequestId: "r1", ResponseType: contractsv1.ProviderProtocolResponseResponseTypeDescriptor,
		Descriptor: &bundledProviders[0], Observation: &contractsv1.ProviderObservation{},
	}
	if validateProtocolResponse(response) == nil {
		t.Fatal("ambiguous protocol response was accepted")
	}
	page := contractsv1.ProviderEventPage{Kind: contractsv1.ProviderEventPageKindProviderEventPage, SchemaVersion: 1, RunRef: "run-a", AfterCursor: 0, NextCursor: 2, Events: []contractsv1.ProviderEvent{{Kind: contractsv1.ProviderEventKindProviderEvent, SchemaVersion: 1, RunRef: "run-a", Cursor: 2, EventType: contractsv1.ProviderEventEventTypeStarted, ObservedAt: time.Now().UTC(), PayloadHash: repeatedSHA('1')}}}
	if validateEventPage(page, "run-a", 0) == nil {
		t.Fatal("non-contiguous provider cursor was accepted")
	}
}

func TestProviderProtocolWriteHonorsContext(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	configureProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, cancelRun := context.WithCancel(context.Background())
	run := &agentRunnerRun{cmd: cmd, stdin: stdin, scanner: bufio.NewScanner(stdout), cancel: cancelRun}
	profile := testExecutorProfile(t, "blocked-write")
	invocationID := "blocked-write"
	deadline := time.Now().Add(time.Minute)
	workspace := contractsv1.ProviderProtocolRequestStagedWorkspaceWorkspace
	request := contractsv1.ProviderProtocolRequest{
		ProtocolVersion: 1, RequestId: "blocked-write:start", Operation: contractsv1.ProviderProtocolRequestOperationStart,
		InvocationId: &invocationID, IdempotencyKey: &invocationID, Deadline: &deadline, StagedWorkspace: &workspace,
		InputManifestHash: ptrSHA(repeatedSHA('1')), OutputContractHash: ptrSHA(repeatedSHA('2')), ExecutorProfile: &profile,
		Invocation: contractsv1.ProviderProtocolRequestInvocation{"padding": string(make([]byte, 128<<10))},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := run.requestContext(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked protocol write error=%v want deadline exceeded", err)
	}
	_ = cmd.Wait()
}

func TestAgentRunnerProviderUsesBoundedProtocolAndExactResult(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS intentionally has no production subprocess sandbox")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, child := range []string{"input", "output"} {
		if err := os.Mkdir(filepath.Join(root, child), 0700); err != nil {
			t.Fatal(err)
		}
	}
	profile := testExecutorProfile(t, "test")
	provider, err := NewAgentRunnerProvider(SubprocessProviderConfig{
		Executable: executable, Args: []string{"-test.run=TestAgentRunnerProtocolHelper"}, StagedRoot: root,
		Environment: map[string]string{"OPENAI_API_KEY": "agent-workflow-helper"},
	}, profile)
	if err != nil {
		t.Skipf("production sandbox unavailable: %v", err)
	}
	isolation := provider.IsolationEvidence()
	invocation := Invocation{IdempotencyKey: "runner-attempt", Deadline: time.Now().Add(10 * time.Second), Node: contractsv1.NodeDefinition{Id: "research", OutputSlots: []contractsv1.Slot{}}, InputHashes: []contractsv1.SHA256{repeatedSHA('1')}, ExecutorProfile: &profile, Isolation: &isolation}
	if err := provider.Start(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	result, ready, err := provider.Poll(context.Background(), invocation.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if !ready || result.IdempotencyKey != invocation.IdempotencyKey || result.Run == nil || result.Observation == nil || len(result.Events) != 1 {
		t.Fatalf("normalized result=%#v ready=%v", result, ready)
	}
	if result.Run.ProviderId != contractsv1.ProviderIDCodex || result.Observation.Status != contractsv1.ProviderObservationStatusSucceeded {
		t.Fatalf("provider identity/status was not preserved: %#v", result)
	}
	restarted, err := NewAgentRunnerProvider(SubprocessProviderConfig{
		Executable: executable, Args: []string{"-test.run=TestAgentRunnerProtocolHelper"}, StagedRoot: root,
		Environment: map[string]string{"OPENAI_API_KEY": "agent-workflow-helper"},
	}, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Start(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	recovered, ready, err := restarted.Poll(context.Background(), invocation.IdempotencyKey)
	if err != nil || !ready || !recovered.CompletedAt.Equal(result.CompletedAt) || recovered.Run == nil || recovered.Observation == nil {
		t.Fatalf("restart recovery=%#v ready=%v err=%v want same completed_at=%s", recovered, ready, err, result.CompletedAt)
	}
	second := invocation
	second.IdempotencyKey = "runner-attempt-2"
	if err := provider.Start(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := provider.Poll(context.Background(), second.IdempotencyKey); err != nil || !ready {
		t.Fatalf("second sequential attempt ready=%v err=%v", ready, err)
	}
	cancellation, err := provider.CancelRun(context.Background(), invocation.IdempotencyKey)
	if err != nil || cancellation.Status != contractsv1.ProviderCancellationStatusAlreadyTerminal {
		t.Fatalf("terminal cancellation=%#v err=%v", cancellation, err)
	}
}

func TestAgentRunnerProtocolHelper(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") != "agent-workflow-helper" {
		return
	}
	descriptor, _ := ProviderDescriptor(contractsv1.ProviderIDCodex)
	runRef := "opaque-provider-run"
	invocationID := ""
	cursor := 0
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request contractsv1.ProviderProtocolRequest
		if err := contract.DecodeDefinition("ProviderProtocolRequest", append([]byte{}, scanner.Bytes()...), &request); err != nil {
			os.Exit(20)
		}
		response := contractsv1.ProviderProtocolResponse{ProtocolVersion: 1, RequestId: request.RequestId}
		switch request.Operation {
		case contractsv1.ProviderProtocolRequestOperationDescribe:
			response.ResponseType, response.Descriptor = contractsv1.ProviderProtocolResponseResponseTypeDescriptor, &descriptor
		case contractsv1.ProviderProtocolRequestOperationStart:
			invocationID = *request.InvocationId
			response.ResponseType = contractsv1.ProviderProtocolResponseResponseTypeRun
			response.Run = &contractsv1.ProviderRunRef{Kind: contractsv1.ProviderRunRefKindProviderRunRef, SchemaVersion: 1, ProviderId: descriptor.Id, InvocationId: *request.InvocationId, RunRef: runRef, ExecutorConfigHash: request.ExecutorProfile.ConfigHash, StartedAt: time.Now().UTC()}
		case contractsv1.ProviderProtocolRequestOperationEvents:
			response.ResponseType = contractsv1.ProviderProtocolResponseResponseTypeEvents
			page := contractsv1.ProviderEventPage{Kind: contractsv1.ProviderEventPageKindProviderEventPage, SchemaVersion: 1, RunRef: runRef, AfterCursor: *request.AfterCursor, NextCursor: cursor, Events: []contractsv1.ProviderEvent{}}
			if cursor == 0 {
				payloadHash, _ := Digest(map[string]any{"state": "started"})
				cursor = 1
				page.Events = []contractsv1.ProviderEvent{{Kind: contractsv1.ProviderEventKindProviderEvent, SchemaVersion: 1, RunRef: runRef, Cursor: 1, EventType: contractsv1.ProviderEventEventTypeStarted, ObservedAt: time.Now().UTC(), PayloadHash: contractsv1.SHA256(payloadHash)}}
				page.NextCursor = cursor
			}
			response.Events = &page
		case contractsv1.ProviderProtocolRequestOperationInspect:
			result := ProviderResult{IdempotencyKey: invocationID, CompletedAt: time.Now().UTC(), Artifacts: []contractsv1.ActionArtifact{}}
			body, _ := json.Marshal(result)
			if err := os.WriteFile("/workspace/output/result", body, 0600); err != nil {
				os.Exit(21)
			}
			hash, _ := hashFile("/workspace/output/result")
			response.ResponseType = contractsv1.ProviderProtocolResponseResponseTypeObservation
			response.Observation = &contractsv1.ProviderObservation{Kind: contractsv1.ProviderObservationKindProviderObservation, SchemaVersion: 1, RunRef: runRef, Status: contractsv1.ProviderObservationStatusSucceeded, NextCursor: cursor, Usage: contractsv1.ProviderUsage{}, OutputHash: &hash}
		case contractsv1.ProviderProtocolRequestOperationCancel:
			response.ResponseType = contractsv1.ProviderProtocolResponseResponseTypeCancellation
			response.Cancellation = &contractsv1.ProviderCancellation{Kind: contractsv1.ProviderCancellationKindProviderCancellation, SchemaVersion: 1, RunRef: runRef, Status: contractsv1.ProviderCancellationStatusAccepted}
		}
		if err := encoder.Encode(response); err != nil {
			os.Exit(22)
		}
	}
	os.Exit(0)
}

func testExecutorProfile(t *testing.T, configRef string) contractsv1.ExecutorProfile {
	t.Helper()
	descriptor, _ := ProviderDescriptor(contractsv1.ProviderIDCodex)
	profile, err := SealExecutorProfile(contractsv1.ExecutorProfile{
		Kind: contractsv1.ExecutorProfileKindExecutorProfile, SchemaVersion: 1,
		ProviderId: descriptor.Id, ProviderVersion: "test-1", AdapterVersion: descriptor.AdapterVersion,
		ModelRef: "test-model", ConfigRef: configRef, Capabilities: descriptor.Capabilities,
		IsolationProfile: contractsv1.ProviderIsolationProfileStagedSubprocess, ToolAllowlist: []string{"read-evidence"}, SecretRefs: []string{"env:OPENAI_API_KEY"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}
