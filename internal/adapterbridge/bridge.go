package adapterbridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

const maxUpstreamOutput = contract.MaxDocumentBytes

type Config struct {
	Provider      contractsv1.ProviderID
	Upstream      string
	WorkspaceRoot string
}

type bridge struct {
	config Config
	run    *run
}

type run struct {
	mu         sync.Mutex
	ref        contractsv1.ProviderRunRef
	invocation workflow.Invocation
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	done       chan struct{}
	stdout     limitedBuffer
	stderr     limitedBuffer
	err        error
	resultHash *contractsv1.SHA256
	events     []contractsv1.ProviderEvent
	cancelled  bool
}

type upstreamOutput struct {
	Outputs map[string]any `json:"outputs"`
}

func Run(config Config, stdin io.Reader, stdout io.Writer) error {
	if config.WorkspaceRoot == "" {
		config.WorkspaceRoot = "/workspace"
	}
	b := &bridge{config: config}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64<<10), contract.MaxDocumentBytes+1)
	encoder := json.NewEncoder(stdout)
	for scanner.Scan() {
		var request contractsv1.ProviderProtocolRequest
		if err := contract.DecodeDefinition("ProviderProtocolRequest", append([]byte{}, scanner.Bytes()...), &request); err != nil {
			return err
		}
		response := b.handle(request)
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (b *bridge) handle(request contractsv1.ProviderProtocolRequest) contractsv1.ProviderProtocolResponse {
	response := contractsv1.ProviderProtocolResponse{ProtocolVersion: 1, RequestId: request.RequestId}
	var err error
	switch request.Operation {
	case contractsv1.ProviderProtocolRequestOperationDescribe:
		var descriptor contractsv1.ProviderDescriptor
		descriptor, err = workflow.ProviderDescriptor(b.config.Provider)
		response.ResponseType, response.Descriptor = contractsv1.ProviderProtocolResponseResponseTypeDescriptor, &descriptor
	case contractsv1.ProviderProtocolRequestOperationStart:
		var ref contractsv1.ProviderRunRef
		ref, err = b.start(request)
		response.ResponseType, response.Run = contractsv1.ProviderProtocolResponseResponseTypeRun, &ref
	case contractsv1.ProviderProtocolRequestOperationEvents:
		var page contractsv1.ProviderEventPage
		page, err = b.eventPage(request)
		response.ResponseType, response.Events = contractsv1.ProviderProtocolResponseResponseTypeEvents, &page
	case contractsv1.ProviderProtocolRequestOperationInspect:
		var observation contractsv1.ProviderObservation
		observation, err = b.inspect(request)
		response.ResponseType, response.Observation = contractsv1.ProviderProtocolResponseResponseTypeObservation, &observation
	case contractsv1.ProviderProtocolRequestOperationCancel:
		var cancellation contractsv1.ProviderCancellation
		cancellation, err = b.cancel(request)
		response.ResponseType, response.Cancellation = contractsv1.ProviderProtocolResponseResponseTypeCancellation, &cancellation
	default:
		err = errors.New("unsupported operation")
	}
	if err != nil {
		code := contractsv1.Identifier("adapter_failure")
		return contractsv1.ProviderProtocolResponse{ProtocolVersion: 1, RequestId: request.RequestId, ResponseType: contractsv1.ProviderProtocolResponseResponseTypeError, ErrorCode: &code}
	}
	return response
}

func (b *bridge) start(request contractsv1.ProviderProtocolRequest) (contractsv1.ProviderRunRef, error) {
	if b.run != nil || request.InvocationId == nil || request.IdempotencyKey == nil || request.Deadline == nil || request.ExecutorProfile == nil {
		return contractsv1.ProviderRunRef{}, errors.New("invalid start")
	}
	body, err := json.Marshal(request.Invocation)
	if err != nil {
		return contractsv1.ProviderRunRef{}, err
	}
	var invocation workflow.Invocation
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&invocation); err != nil {
		return contractsv1.ProviderRunRef{}, err
	}
	if err := verifyStart(b.config.Provider, request, invocation); err != nil {
		return contractsv1.ProviderRunRef{}, err
	}
	prompt, err := buildPrompt(invocation)
	if err != nil {
		return contractsv1.ProviderRunRef{}, err
	}
	if err := workflow.ValidateBundledProviderProfile(*request.ExecutorProfile, b.config.WorkspaceRoot); err != nil {
		return contractsv1.ProviderRunRef{}, err
	}
	args, err := upstreamArgs(b.config.Provider, *request.ExecutorProfile, *request.IdempotencyKey, prompt)
	if err != nil {
		return contractsv1.ProviderRunRef{}, err
	}
	upstream, err := resolveUpstream(b.config.Provider, b.config.Upstream)
	if err != nil {
		return contractsv1.ProviderRunRef{}, err
	}
	ctx, cancel := context.WithDeadline(context.Background(), request.Deadline.UTC())
	cmd := exec.CommandContext(ctx, upstream, args...)
	cmd.Dir = b.config.WorkspaceRoot
	cmd.Env = append(os.Environ(), "HOME="+b.config.WorkspaceRoot, "PATH=/usr/local/bin:/usr/bin:/bin", "TMPDIR=/tmp")
	if b.config.Provider == contractsv1.ProviderIDOpenclaw {
		cmd.Env = append(cmd.Env, "OPENCLAW_CONFIG_PATH="+filepath.Join(b.config.WorkspaceRoot, "input", "providers", request.ExecutorProfile.ConfigRef+".json"))
	}
	if token := os.Getenv("AGENT_WORKFLOW_PROVIDER_TOKEN"); token != "" {
		name, err := tokenEnvironment(request.ExecutorProfile.ModelRef)
		if err != nil {
			cancel()
			return contractsv1.ProviderRunRef{}, err
		}
		cmd.Env = append(cmd.Env, name+"="+token)
	}
	r := &run{invocation: invocation, cmd: cmd, cancel: cancel, done: make(chan struct{}), stdout: limitedBuffer{max: maxUpstreamOutput}, stderr: limitedBuffer{max: 64 << 10}}
	cmd.Stdout, cmd.Stderr = &r.stdout, &r.stderr
	startedAt := time.Now().UTC()
	refHash, _ := workflow.Digest(map[string]any{"provider": b.config.Provider, "idempotency_key": *request.IdempotencyKey, "config_hash": request.ExecutorProfile.ConfigHash})
	r.ref = contractsv1.ProviderRunRef{Kind: contractsv1.ProviderRunRefKindProviderRunRef, SchemaVersion: 1, ProviderId: b.config.Provider, InvocationId: *request.InvocationId, RunRef: "run-" + strings.TrimPrefix(refHash, "sha256:")[:24], ExecutorConfigHash: request.ExecutorProfile.ConfigHash, StartedAt: startedAt}
	payloadHash, _ := workflow.Digest(map[string]any{"state": "started"})
	r.events = []contractsv1.ProviderEvent{{Kind: contractsv1.ProviderEventKindProviderEvent, SchemaVersion: 1, RunRef: r.ref.RunRef, Cursor: 1, EventType: contractsv1.ProviderEventEventTypeStarted, ObservedAt: startedAt, PayloadHash: contractsv1.SHA256(payloadHash)}}
	if err := cmd.Start(); err != nil {
		cancel()
		return contractsv1.ProviderRunRef{}, err
	}
	b.run = r
	go r.wait(b.config.WorkspaceRoot)
	return r.ref, nil
}

func verifyStart(provider contractsv1.ProviderID, request contractsv1.ProviderProtocolRequest, invocation workflow.Invocation) error {
	if request.ExecutorProfile.ProviderId != provider || workflow.VerifyExecutorProfile(*request.ExecutorProfile) != nil {
		return errors.New("executor profile is invalid")
	}
	if invocation.ExecutorProfile == nil || !reflect.DeepEqual(*invocation.ExecutorProfile, *request.ExecutorProfile) || invocation.IdempotencyKey != *request.IdempotencyKey || invocation.IdempotencyKey != *request.InvocationId || !invocation.Deadline.Equal(request.Deadline.UTC()) {
		return errors.New("invocation identity is invalid")
	}
	if err := workflow.VerifyCapabilityManifest(invocation.Capabilities); err != nil {
		return errors.New("invocation capability manifest is invalid")
	}
	tools := make([]string, 0, len(invocation.Capabilities.Capabilities))
	for _, capability := range invocation.Capabilities.Capabilities {
		if capability.Authority != contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead {
			return errors.New("bundled agent runners accept only read capabilities")
		}
		tools = append(tools, string(capability.Name))
	}
	sort.Strings(tools)
	if !reflect.DeepEqual(tools, []string(request.ExecutorProfile.ToolAllowlist)) {
		return errors.New("invocation capabilities do not match the executor tool allowlist")
	}
	if request.StagedWorkspace == nil || *request.StagedWorkspace != contractsv1.ProviderProtocolRequestStagedWorkspaceWorkspace || !request.Deadline.After(time.Now()) {
		return errors.New("invocation workspace or deadline is invalid")
	}
	inputHash, err := workflow.Digest(invocation.InputHashes)
	if err != nil || request.InputManifestHash == nil || *request.InputManifestHash != contractsv1.SHA256(inputHash) {
		return errors.New("input manifest hash is invalid")
	}
	outputHash, err := workflow.Digest(invocation.Node.OutputSlots)
	if err != nil || request.OutputContractHash == nil || *request.OutputContractHash != contractsv1.SHA256(outputHash) {
		return errors.New("output contract hash is invalid")
	}
	return nil
}

func (r *run) wait(workspace string) {
	err := r.cmd.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	defer close(r.done)
	r.err = err
	if err == nil {
		r.err = r.writeResult(workspace)
	}
	eventType := contractsv1.ProviderEventEventTypeResult
	state := "succeeded"
	if r.err != nil {
		eventType, state = contractsv1.ProviderEventEventTypeFailed, "failed"
	}
	payloadHash, _ := workflow.Digest(map[string]any{"state": state})
	r.events = append(r.events, contractsv1.ProviderEvent{Kind: contractsv1.ProviderEventKindProviderEvent, SchemaVersion: 1, RunRef: r.ref.RunRef, Cursor: len(r.events) + 1, EventType: eventType, ObservedAt: time.Now().UTC(), PayloadHash: contractsv1.SHA256(payloadHash)})
}

func (r *run) writeResult(workspace string) error {
	var output upstreamOutput
	if err := decodeUpstreamOutput(r.stdout.Bytes(), &output); err != nil {
		return err
	}
	artifacts := make([]contractsv1.ActionArtifact, 0, len(r.invocation.Node.OutputSlots))
	for _, slot := range r.invocation.Node.OutputSlots {
		content, ok := output.Outputs[string(slot.Id)]
		if !ok {
			return fmt.Errorf("upstream output is missing slot %q", slot.Id)
		}
		hash, err := workflow.Digest(content)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, contractsv1.ActionArtifact{Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: string(r.invocation.Node.Id) + "-" + string(slot.Id), ArtifactType: slot.ArtifactType, JobId: r.invocation.JobID, CampaignId: r.invocation.CampaignID, WorkflowRef: r.invocation.WorkflowRef, NodeId: r.invocation.Node.Id, InputHashes: r.invocation.InputHashes, Content: content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending})
	}
	result := workflow.ProviderResult{IdempotencyKey: r.invocation.IdempotencyKey, CompletedAt: time.Now().UTC(), Artifacts: artifacts}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	path := filepath.Join(workspace, "output", "result")
	if err := os.WriteFile(path, body, 0600); err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	value := contractsv1.SHA256(fmt.Sprintf("sha256:%x", sum))
	r.resultHash = &value
	return nil
}

func (b *bridge) eventPage(request contractsv1.ProviderProtocolRequest) (contractsv1.ProviderEventPage, error) {
	r, err := b.current(request.RunRef)
	if err != nil || request.AfterCursor == nil {
		return contractsv1.ProviderEventPage{}, errors.New("invalid event request")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if *request.AfterCursor < 0 || *request.AfterCursor > len(r.events) {
		return contractsv1.ProviderEventPage{}, errors.New("invalid event cursor")
	}
	events := append([]contractsv1.ProviderEvent{}, r.events[*request.AfterCursor:]...)
	return contractsv1.ProviderEventPage{Kind: contractsv1.ProviderEventPageKindProviderEventPage, SchemaVersion: 1, RunRef: r.ref.RunRef, AfterCursor: *request.AfterCursor, NextCursor: len(r.events), Events: events}, nil
}

func (b *bridge) inspect(request contractsv1.ProviderProtocolRequest) (contractsv1.ProviderObservation, error) {
	r, err := b.current(request.RunRef)
	if err != nil {
		return contractsv1.ProviderObservation{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	status := contractsv1.ProviderObservationStatusRunning
	if r.cancelled {
		status = contractsv1.ProviderObservationStatusCancelled
	} else {
		select {
		case <-r.done:
			if r.err != nil {
				status = contractsv1.ProviderObservationStatusFailed
			} else {
				status = contractsv1.ProviderObservationStatusSucceeded
			}
		default:
		}
	}
	return contractsv1.ProviderObservation{Kind: contractsv1.ProviderObservationKindProviderObservation, SchemaVersion: 1, RunRef: r.ref.RunRef, Status: status, NextCursor: len(r.events), Usage: contractsv1.ProviderUsage{}, OutputHash: r.resultHash}, nil
}

func (b *bridge) cancel(request contractsv1.ProviderProtocolRequest) (contractsv1.ProviderCancellation, error) {
	r, err := b.current(request.RunRef)
	if err != nil {
		return contractsv1.ProviderCancellation{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	status := contractsv1.ProviderCancellationStatusAccepted
	select {
	case <-r.done:
		status = contractsv1.ProviderCancellationStatusAlreadyTerminal
	default:
		r.cancelled = true
		r.cancel()
		_ = r.cmd.Process.Kill()
	}
	return contractsv1.ProviderCancellation{Kind: contractsv1.ProviderCancellationKindProviderCancellation, SchemaVersion: 1, RunRef: r.ref.RunRef, Status: status}, nil
}

func (b *bridge) current(ref *string) (*run, error) {
	if b.run == nil || ref == nil || *ref != b.run.ref.RunRef {
		return nil, errors.New("unknown provider run")
	}
	return b.run, nil
}

func buildPrompt(invocation workflow.Invocation) (string, error) {
	body, err := json.Marshal(invocation)
	if err != nil {
		return "", err
	}
	return "Execute this already-authorized node using only /workspace/input as evidence. Return exactly one JSON object with an outputs object keyed by every output slot id; values are the content objects. No markdown fences. Invocation: " + string(body), nil
}

func upstreamArgs(provider contractsv1.ProviderID, profile contractsv1.ExecutorProfile, runRef, prompt string) ([]string, error) {
	model := profile.ModelRef
	switch provider {
	case contractsv1.ProviderIDCodex:
		return []string{"exec", "--model", model, "--sandbox", "read-only", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", prompt}, nil
	case contractsv1.ProviderIDClaudeCode:
		return []string{"-p", "--bare", "--disable-slash-commands", "--output-format", "text", "--model", model, "--no-session-persistence", "--tools", "Read,Glob,Grep", "--allowedTools", "Read,Glob,Grep", prompt}, nil
	case contractsv1.ProviderIDPi:
		return []string{"-p", "--mode", "text", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--tools", "read,grep,find,ls", "--model", model, prompt}, nil
	case contractsv1.ProviderIDOpenclaw:
		return []string{"agent", "--local", "--agent", profile.ConfigRef, "--json", "--model", model, "--session-key", runRef, "--message", prompt}, nil
	case contractsv1.ProviderIDHermesAgent:
		return []string{"-z", prompt, "--ignore-user-config", "--ignore-rules", "--toolsets", "vision", "--model", model}, nil
	default:
		return nil, errors.New("unknown provider")
	}
}

func tokenEnvironment(model string) (string, error) {
	prefix := strings.ToLower(strings.SplitN(model, "/", 2)[0])
	switch prefix {
	case "anthropic", "claude":
		return "ANTHROPIC_API_KEY", nil
	case "openrouter":
		return "OPENROUTER_API_KEY", nil
	case "google", "gemini":
		return "GEMINI_API_KEY", nil
	case "deepseek":
		return "DEEPSEEK_API_KEY", nil
	case "nous":
		return "NOUS_API_KEY", nil
	case "openai", "codex", "gpt":
		return "OPENAI_API_KEY", nil
	default:
		return "", errors.New("generic provider token requires a provider/model reference")
	}
}

func resolveUpstream(provider contractsv1.ProviderID, name string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return name, nil
	}
	upstream, err := workflow.ResolveBundledProviderUpstream(provider)
	if err == nil {
		return upstream, nil
	}
	return "", fmt.Errorf("upstream executable %q is unavailable in the provider sandbox", name)
}

func decodeUpstreamOutput(body []byte, target *upstreamOutput) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(body)))
	decoder.UseNumber()
	if err := decoder.Decode(target); err == nil && len(target.Outputs) > 0 {
		return nil
	}
	var openclaw struct {
		Payloads []struct {
			Text string `json:"text"`
		} `json:"payloads"`
	}
	if json.Unmarshal(body, &openclaw) == nil && len(openclaw.Payloads) > 0 {
		decoder = json.NewDecoder(strings.NewReader(openclaw.Payloads[0].Text))
		decoder.UseNumber()
		if err := decoder.Decode(target); err == nil && len(target.Outputs) > 0 {
			return nil
		}
	}
	return errors.New("upstream returned invalid structured output")
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("upstream output exceeded its limit")
	}
	return b.Buffer.Write(p)
}
