package workflow

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

const providerProtocolMaxLine = contract.MaxDocumentBytes

type providerAttemptReservation struct {
	IdempotencyKey string             `json:"idempotency_key"`
	InvocationHash contractsv1.SHA256 `json:"invocation_hash"`
}

type AgentRunnerProvider struct {
	descriptor contractsv1.ProviderDescriptor
	profile    contractsv1.ExecutorProfile
	sandbox    *SubprocessProvider
	mu         sync.Mutex
	runs       map[string]*agentRunnerRun
	starting   map[string]bool
	results    map[string]ProviderResult
}

type agentRunnerRun struct {
	mu           sync.Mutex
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	scanner      *bufio.Scanner
	cancel       context.CancelFunc
	ref          contractsv1.ProviderRunRef
	cursor       int
	events       []contractsv1.ProviderEvent
	observation  contractsv1.ProviderObservation
	cancellation *contractsv1.ProviderCancellation
	terminal     bool
	waited       bool
	err          error
}

func NewAgentRunnerProvider(config SubprocessProviderConfig, profile contractsv1.ExecutorProfile) (*AgentRunnerProvider, error) {
	normalized, err := SealExecutorProfile(profile)
	if err != nil || normalized.ConfigHash != profile.ConfigHash {
		if err == nil {
			err = errors.New("executor profile configuration hash is invalid")
		}
		return nil, err
	}
	profile = normalized
	descriptor, err := ProviderDescriptor(profile.ProviderId)
	if err != nil {
		return nil, err
	}
	if config.Executable == "" {
		path, err := exec.LookPath(descriptor.Executable)
		if err != nil {
			return nil, fmt.Errorf("provider adapter %q is unavailable: %w", descriptor.Executable, err)
		}
		config.Executable = path
	}
	allowedEnvironment := make(map[string]bool, len(descriptor.AuthEnvironment))
	for _, name := range descriptor.AuthEnvironment {
		allowedEnvironment[name] = true
	}
	for name := range config.Environment {
		if !allowedEnvironment[name] {
			return nil, errors.New("provider environment contains an undeclared secret or configuration value")
		}
	}
	if profile.NetworkAccess != config.AllowNetwork {
		return nil, errors.New("executor profile network authority does not match the sandbox")
	}
	sandbox, err := NewSubprocessProvider(config)
	if err != nil {
		return nil, err
	}
	return &AgentRunnerProvider{descriptor: descriptor, profile: profile, sandbox: sandbox, runs: map[string]*agentRunnerRun{}, starting: map[string]bool{}, results: map[string]ProviderResult{}}, nil
}

func (p *AgentRunnerProvider) IsolationEvidence() contractsv1.ProviderIsolationEvidence {
	return p.sandbox.IsolationEvidence()
}

func (p *AgentRunnerProvider) ExecutorProfile() contractsv1.ExecutorProfile {
	profile := p.profile
	profile.Capabilities = append([]contractsv1.ProviderCapability{}, profile.Capabilities...)
	profile.ToolAllowlist = append([]string{}, profile.ToolAllowlist...)
	profile.SecretRefs = append([]string{}, profile.SecretRefs...)
	return profile
}

func (p *AgentRunnerProvider) Start(ctx context.Context, invocation Invocation) error {
	if invocation.IdempotencyKey == "" || invocation.Deadline.IsZero() {
		return errors.New("provider invocation identity and deadline are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if invocation.ExecutorProfile == nil || !reflect.DeepEqual(*invocation.ExecutorProfile, p.profile) {
		return errors.New("provider invocation does not match the admitted executor profile")
	}
	isolation := p.IsolationEvidence()
	if invocation.Isolation == nil || invocation.Isolation.EvidenceHash != isolation.EvidenceHash {
		return errors.New("provider invocation does not match the admitted isolation evidence")
	}
	p.mu.Lock()
	if _, ok := p.results[invocation.IdempotencyKey]; ok || p.runs[invocation.IdempotencyKey] != nil || p.starting[invocation.IdempotencyKey] {
		p.mu.Unlock()
		return nil
	}
	p.starting[invocation.IdempotencyKey] = true
	p.mu.Unlock()
	started := false
	defer func() {
		if started {
			return
		}
		p.mu.Lock()
		delete(p.starting, invocation.IdempotencyKey)
		p.mu.Unlock()
	}()
	reserved, recovered, err := p.reserveAttempt(invocation)
	if err != nil {
		return err
	}
	if recovered != nil {
		if err := p.releaseActiveLease(invocation.IdempotencyKey); err != nil {
			return err
		}
		p.mu.Lock()
		delete(p.starting, invocation.IdempotencyKey)
		p.results[invocation.IdempotencyKey] = *recovered
		started = true
		p.mu.Unlock()
		return nil
	}
	removeReservation := reserved
	defer func() {
		if removeReservation {
			_ = os.Remove(p.attemptReservationPath(invocation.IdempotencyKey))
		}
	}()
	leaseHeld := false
	if err := p.acquireActiveLease(invocation.IdempotencyKey); err != nil {
		return err
	}
	leaseHeld = true
	defer func() {
		if leaseHeld {
			_ = p.releaseActiveLease(invocation.IdempotencyKey)
		}
	}()
	if err := p.sandbox.verifyInputs(); err != nil {
		return err
	}
	name, args, err := sandboxCommand(p.sandbox.config.Executable, p.sandbox.config.Args, p.sandbox.config.StagedRoot, p.sandbox.config.MaxOutputBytes, p.sandbox.config.AllowNetwork)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithDeadline(context.Background(), invocation.Deadline)
	cmd := exec.CommandContext(runCtx, name, args...)
	configureProcessGroup(cmd)
	cmd.Dir = p.sandbox.config.StagedRoot
	for key, value := range p.sandbox.config.Environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	sort.Strings(cmd.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	stderr := &limitedBuffer{limit: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	removeReservation = false
	run := &agentRunnerRun{cmd: cmd, stdin: stdin, scanner: bufio.NewScanner(stdout), cancel: cancel}
	run.scanner.Buffer(make([]byte, 64<<10), providerProtocolMaxLine)
	if err := p.handshake(ctx, run, invocation); err != nil {
		cancel()
		killProcessGroup(cmd)
		_ = cmd.Wait()
		return err
	}
	leaseHeld = false
	p.mu.Lock()
	delete(p.starting, invocation.IdempotencyKey)
	p.runs[invocation.IdempotencyKey] = run
	started = true
	p.mu.Unlock()
	return nil
}

func (p *AgentRunnerProvider) attemptPath(kind, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(p.sandbox.config.StagedRoot, "output", fmt.Sprintf(".agent-workflow-%s-%x.json", kind, sum))
}

func (p *AgentRunnerProvider) attemptReservationPath(key string) string {
	return p.attemptPath("attempt", key)
}

func (p *AgentRunnerProvider) activeLeasePath() string {
	return filepath.Join(p.sandbox.config.StagedRoot, "output", ".agent-workflow-active.json")
}

func (p *AgentRunnerProvider) acquireActiveLease(key string) error {
	lock, err := lockLedger(p.activeLeasePath(), true)
	if err != nil {
		return err
	}
	defer unlockLedger(lock)
	body, err := json.Marshal(struct {
		IdempotencyKey string `json:"idempotency_key"`
	}{IdempotencyKey: key})
	if err != nil {
		return err
	}
	created, err := linkDurableFile(p.activeLeasePath(), body)
	if err != nil {
		return err
	}
	if !created {
		return errors.New("provider staged root already has an active attempt")
	}
	return nil
}

func (p *AgentRunnerProvider) releaseActiveLease(key string) error {
	path := p.activeLeasePath()
	lock, err := lockLedger(path, true)
	if err != nil {
		return err
	}
	defer unlockLedger(lock)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var lease struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	if json.Unmarshal(body, &lease) != nil || lease.IdempotencyKey != key {
		return errors.New("provider active attempt belongs to another invocation")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return err
	}
	return directory.Close()
}

func (p *AgentRunnerProvider) reserveAttempt(invocation Invocation) (bool, *ProviderResult, error) {
	hash, err := Digest(invocation)
	if err != nil {
		return false, nil, err
	}
	want := providerAttemptReservation{IdempotencyKey: invocation.IdempotencyKey, InvocationHash: contractsv1.SHA256(hash)}
	path := p.attemptReservationPath(invocation.IdempotencyKey)
	body, err := json.Marshal(want)
	if err != nil {
		return false, nil, err
	}
	created, err := linkDurableFile(path, body)
	if err != nil {
		return false, nil, err
	}
	if created {
		return true, nil, nil
	}
	body, err = os.ReadFile(path)
	if err != nil {
		return false, nil, err
	}
	var existing providerAttemptReservation
	if json.Unmarshal(body, &existing) != nil || existing != want {
		return false, nil, errors.New("provider attempt reservation does not match the invocation")
	}
	resultBody, err := os.ReadFile(p.attemptPath("result", invocation.IdempotencyKey))
	if err == nil && len(resultBody) > 0 {
		var result ProviderResult
		decoder := json.NewDecoder(bytes.NewReader(resultBody))
		decoder.UseNumber()
		decoder.DisallowUnknownFields()
		if decoder.Decode(&result) == nil && ensureJSONEOF(decoder) == nil && result.IdempotencyKey == invocation.IdempotencyKey && result.Observation != nil && result.Observation.OutputHash != nil {
			_, rawHash, readErr := readBoundedResult(p.attemptPath("output", invocation.IdempotencyKey), int64(p.sandbox.config.MaxOutputBytes))
			if readErr == nil && rawHash == *result.Observation.OutputHash {
				return false, &result, nil
			}
		}
	}
	return false, nil, errors.New("provider attempt is already reserved without a recoverable result")
}

func linkDurableFile(path string, body []byte) (bool, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".agent-workflow-link-")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err == nil {
		_, err = temp.Write(body)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if err := os.Link(tempPath, path); errors.Is(err, os.ErrExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		_ = os.Remove(path)
		return false, err
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		_ = os.Remove(path)
		return false, err
	}
	if err := directory.Close(); err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return true, nil
}

func (p *AgentRunnerProvider) handshake(ctx context.Context, run *agentRunnerRun, invocation Invocation) error {
	describe := contractsv1.ProviderProtocolRequest{ProtocolVersion: 1, RequestId: invocation.IdempotencyKey + ":describe", Operation: contractsv1.ProviderProtocolRequestOperationDescribe}
	response, err := run.requestContext(ctx, describe)
	if err != nil {
		return err
	}
	if response.ResponseType != contractsv1.ProviderProtocolResponseResponseTypeDescriptor || response.Descriptor == nil || !reflect.DeepEqual(*response.Descriptor, p.descriptor) {
		return errors.New("provider descriptor does not match the bundled registry")
	}
	inputHash, err := Digest(invocation.InputHashes)
	if err != nil {
		return err
	}
	outputHash, err := Digest(invocation.Node.OutputSlots)
	if err != nil {
		return err
	}
	body, err := json.Marshal(invocation)
	if err != nil {
		return err
	}
	var material map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&material); err != nil {
		return err
	}
	workspace := contractsv1.ProviderProtocolRequestStagedWorkspace("/workspace")
	start := contractsv1.ProviderProtocolRequest{
		ProtocolVersion: 1, RequestId: invocation.IdempotencyKey + ":start", Operation: contractsv1.ProviderProtocolRequestOperationStart,
		InvocationId: &invocation.IdempotencyKey, IdempotencyKey: &invocation.IdempotencyKey, Deadline: &invocation.Deadline,
		StagedWorkspace: &workspace, InputManifestHash: ptrSHA(contractsv1.SHA256(inputHash)), OutputContractHash: ptrSHA(contractsv1.SHA256(outputHash)),
		ExecutorProfile: &p.profile, Invocation: material,
	}
	response, err = run.requestContext(ctx, start)
	if err != nil {
		return err
	}
	if response.ResponseType != contractsv1.ProviderProtocolResponseResponseTypeRun || response.Run == nil {
		return errors.New("provider start response is incomplete")
	}
	if err := contract.ValidateDefinition("ProviderRunRef", *response.Run); err != nil {
		return err
	}
	if response.Run.ProviderId != p.descriptor.Id || response.Run.InvocationId != invocation.IdempotencyKey || response.Run.ExecutorConfigHash != p.profile.ConfigHash {
		return errors.New("provider run identity does not match the invocation")
	}
	run.ref = *response.Run
	return nil
}

func (p *AgentRunnerProvider) Poll(ctx context.Context, key string) (ProviderResult, bool, error) {
	p.mu.Lock()
	if result, ok := p.results[key]; ok {
		p.mu.Unlock()
		return result, true, nil
	}
	run := p.runs[key]
	p.mu.Unlock()
	if run == nil {
		return ProviderResult{}, false, nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.err != nil {
		return ProviderResult{}, false, run.err
	}
	if run.terminal {
		return ProviderResult{}, false, nil
	}
	runRef := run.ref.RunRef
	after := run.cursor
	eventsResponse, err := run.requestUnlockedContext(ctx, contractsv1.ProviderProtocolRequest{ProtocolVersion: 1, RequestId: key + ":events:" + fmt.Sprint(after), Operation: contractsv1.ProviderProtocolRequestOperationEvents, RunRef: &runRef, AfterCursor: &after})
	if err != nil {
		return ProviderResult{}, false, p.failRun(key, run, err)
	}
	if eventsResponse.ResponseType != contractsv1.ProviderProtocolResponseResponseTypeEvents || eventsResponse.Events == nil {
		return ProviderResult{}, false, p.failRun(key, run, errors.New("provider events response is incomplete"))
	}
	if err := validateEventPage(*eventsResponse.Events, run.ref.RunRef, run.cursor); err != nil {
		return ProviderResult{}, false, p.failRun(key, run, err)
	}
	run.events = append(run.events, eventsResponse.Events.Events...)
	if len(run.events) > 1000 {
		return ProviderResult{}, false, p.failRun(key, run, errors.New("provider event history exceeded its limit"))
	}
	run.cursor = eventsResponse.Events.NextCursor
	observationResponse, err := run.requestUnlockedContext(ctx, contractsv1.ProviderProtocolRequest{ProtocolVersion: 1, RequestId: key + ":inspect:" + fmt.Sprint(run.cursor), Operation: contractsv1.ProviderProtocolRequestOperationInspect, RunRef: &runRef})
	if err != nil {
		return ProviderResult{}, false, p.failRun(key, run, err)
	}
	if observationResponse.ResponseType != contractsv1.ProviderProtocolResponseResponseTypeObservation || observationResponse.Observation == nil {
		return ProviderResult{}, false, p.failRun(key, run, errors.New("provider observation response is incomplete"))
	}
	observation := *observationResponse.Observation
	if err := contract.ValidateDefinition("ProviderObservation", observation); err != nil {
		return ProviderResult{}, false, p.failRun(key, run, err)
	}
	if observation.RunRef != run.ref.RunRef || observation.NextCursor != run.cursor {
		return ProviderResult{}, false, p.failRun(key, run, errors.New("provider observation does not match the event frontier"))
	}
	run.observation = observation
	switch observation.Status {
	case contractsv1.ProviderObservationStatusQueued, contractsv1.ProviderObservationStatusRunning:
		return ProviderResult{}, false, nil
	case contractsv1.ProviderObservationStatusFailed:
		return ProviderResult{}, false, p.failRun(key, run, errors.New("provider run failed"))
	case contractsv1.ProviderObservationStatusCancelled:
		run.terminal = true
		run.stop()
		if err := p.releaseActiveLease(key); err != nil {
			return ProviderResult{}, false, err
		}
		return ProviderResult{}, false, nil
	case contractsv1.ProviderObservationStatusSucceeded:
		result, err := p.acceptResult(key, run)
		if err != nil {
			return ProviderResult{}, false, p.failRun(key, run, err)
		}
		run.terminal = true
		if err := p.releaseActiveLease(key); err != nil {
			return ProviderResult{}, false, p.failRun(key, run, err)
		}
		p.mu.Lock()
		p.results[key] = result
		p.mu.Unlock()
		return result, true, nil
	default:
		return ProviderResult{}, false, p.failRun(key, run, errors.New("unknown provider terminal state"))
	}
}

func (p *AgentRunnerProvider) failRun(key string, run *agentRunnerRun, err error) error {
	run.fail(err)
	if releaseErr := p.releaseActiveLease(key); releaseErr != nil {
		return releaseErr
	}
	return err
}

func (p *AgentRunnerProvider) acceptResult(key string, run *agentRunnerRun) (ProviderResult, error) {
	path := p.sandbox.config.StagedRoot + "/output/result"
	body, hash, err := readBoundedResult(path, int64(p.sandbox.config.MaxOutputBytes))
	if err != nil {
		return ProviderResult{}, err
	}
	if run.observation.OutputHash == nil || *run.observation.OutputHash != hash {
		return ProviderResult{}, errors.New("provider output does not match the terminal observation")
	}
	var result ProviderResult
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ProviderResult{}, fmt.Errorf("decode provider result: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ProviderResult{}, err
	}
	if result.IdempotencyKey != key {
		return ProviderResult{}, errors.New("provider result idempotency key does not match invocation")
	}
	result.Run = &run.ref
	result.Events = append([]contractsv1.ProviderEvent{}, run.events...)
	result.Observation = &run.observation
	if err := p.persistAcceptedResult(result); err != nil {
		return ProviderResult{}, err
	}
	run.stop()
	return result, nil
}

func (p *AgentRunnerProvider) persistAcceptedResult(result ProviderResult) error {
	raw, _, err := readBoundedResult(filepath.Join(p.sandbox.config.StagedRoot, "output", "result"), int64(p.sandbox.config.MaxOutputBytes))
	if err != nil {
		return err
	}
	if err := p.writeDurableAttemptFile("output", result.IdempotencyKey, raw); err != nil {
		return err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return p.writeDurableAttemptFile("result", result.IdempotencyKey, body)
}

func (p *AgentRunnerProvider) writeDurableAttemptFile(kind, key string, body []byte) error {
	directoryPath := filepath.Join(p.sandbox.config.StagedRoot, "output")
	temp, err := os.CreateTemp(directoryPath, ".agent-workflow-"+kind+"-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err == nil {
		_, err = temp.Write(body)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	path := p.attemptPath(kind, key)
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return err
	}
	return directory.Close()
}

func (p *AgentRunnerProvider) Cancel(ctx context.Context, key string) error {
	_, err := p.CancelRun(ctx, key)
	return err
}

func (p *AgentRunnerProvider) CancelRun(ctx context.Context, key string) (contractsv1.ProviderCancellation, error) {
	p.mu.Lock()
	run := p.runs[key]
	p.mu.Unlock()
	if run == nil {
		return contractsv1.ProviderCancellation{}, errors.New("provider run is unknown")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.cancellation != nil {
		return *run.cancellation, nil
	}
	if run.terminal {
		cancellation := contractsv1.ProviderCancellation{Kind: contractsv1.ProviderCancellationKindProviderCancellation, SchemaVersion: 1, RunRef: run.ref.RunRef, Status: contractsv1.ProviderCancellationStatusAlreadyTerminal}
		run.cancellation = &cancellation
		return cancellation, nil
	}
	runRef := run.ref.RunRef
	response, err := run.requestUnlockedContext(ctx, contractsv1.ProviderProtocolRequest{ProtocolVersion: 1, RequestId: key + ":cancel", Operation: contractsv1.ProviderProtocolRequestOperationCancel, RunRef: &runRef})
	if err != nil {
		return contractsv1.ProviderCancellation{}, p.failRun(key, run, err)
	}
	if response.ResponseType != contractsv1.ProviderProtocolResponseResponseTypeCancellation || response.Cancellation == nil {
		return contractsv1.ProviderCancellation{}, p.failRun(key, run, errors.New("provider cancellation response is incomplete"))
	}
	cancellation := *response.Cancellation
	if err := contract.ValidateDefinition("ProviderCancellation", cancellation); err != nil {
		return contractsv1.ProviderCancellation{}, p.failRun(key, run, err)
	}
	if cancellation.RunRef != run.ref.RunRef {
		return contractsv1.ProviderCancellation{}, p.failRun(key, run, errors.New("provider cancellation run identity is invalid"))
	}
	run.cancellation = &cancellation
	if cancellation.Status == contractsv1.ProviderCancellationStatusAccepted || cancellation.Status == contractsv1.ProviderCancellationStatusAlreadyTerminal {
		run.terminal = true
		run.stop()
	}
	if cancellation.Status == contractsv1.ProviderCancellationStatusAccepted || cancellation.Status == contractsv1.ProviderCancellationStatusAlreadyTerminal {
		if err := p.releaseActiveLease(key); err != nil {
			return contractsv1.ProviderCancellation{}, err
		}
	}
	return cancellation, nil
}

func (r *agentRunnerRun) request(request contractsv1.ProviderProtocolRequest) (contractsv1.ProviderProtocolResponse, error) {
	return r.requestContext(context.Background(), request)
}

func (r *agentRunnerRun) requestContext(ctx context.Context, request contractsv1.ProviderProtocolRequest) (contractsv1.ProviderProtocolResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requestUnlockedContext(ctx, request)
}

func (r *agentRunnerRun) requestUnlocked(request contractsv1.ProviderProtocolRequest) (contractsv1.ProviderProtocolResponse, error) {
	return r.requestUnlockedContext(context.Background(), request)
}

func (r *agentRunnerRun) requestUnlockedContext(ctx context.Context, request contractsv1.ProviderProtocolRequest) (contractsv1.ProviderProtocolResponse, error) {
	if err := validateProtocolRequest(request); err != nil {
		return contractsv1.ProviderProtocolResponse{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return contractsv1.ProviderProtocolResponse{}, err
	}
	written := make(chan error, 1)
	go func() {
		_, err := r.stdin.Write(append(body, '\n'))
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			return contractsv1.ProviderProtocolResponse{}, err
		}
	case <-ctx.Done():
		r.cancel()
		killProcessGroup(r.cmd)
		<-written
		return contractsv1.ProviderProtocolResponse{}, ctx.Err()
	}
	scanned := make(chan bool, 1)
	go func() { scanned <- r.scanner.Scan() }()
	select {
	case ok := <-scanned:
		if !ok {
			if err := r.scanner.Err(); err != nil {
				return contractsv1.ProviderProtocolResponse{}, err
			}
			return contractsv1.ProviderProtocolResponse{}, errors.New("provider protocol ended before a response")
		}
	case <-ctx.Done():
		r.cancel()
		killProcessGroup(r.cmd)
		<-scanned
		return contractsv1.ProviderProtocolResponse{}, ctx.Err()
	}
	var response contractsv1.ProviderProtocolResponse
	if err := contract.DecodeDefinition("ProviderProtocolResponse", append([]byte{}, r.scanner.Bytes()...), &response); err != nil {
		return contractsv1.ProviderProtocolResponse{}, err
	}
	if response.RequestId != request.RequestId || response.ProtocolVersion != 1 {
		return contractsv1.ProviderProtocolResponse{}, errors.New("provider response identity is invalid")
	}
	if response.ResponseType == contractsv1.ProviderProtocolResponseResponseTypeError {
		if response.ErrorCode == nil {
			return contractsv1.ProviderProtocolResponse{}, errors.New("provider error response has no code")
		}
		return contractsv1.ProviderProtocolResponse{}, fmt.Errorf("provider protocol error: %s", *response.ErrorCode)
	}
	if err := validateProtocolResponse(response); err != nil {
		return contractsv1.ProviderProtocolResponse{}, err
	}
	return response, nil
}

func (r *agentRunnerRun) fail(err error) error {
	r.err = err
	r.stop()
	return err
}

func (r *agentRunnerRun) stop() {
	if r.waited {
		return
	}
	r.cancel()
	killProcessGroup(r.cmd)
	_ = r.cmd.Wait()
	r.waited = true
}

func validateProtocolRequest(request contractsv1.ProviderProtocolRequest) error {
	if err := contract.ValidateDefinition("ProviderProtocolRequest", request); err != nil {
		return err
	}
	switch request.Operation {
	case contractsv1.ProviderProtocolRequestOperationDescribe:
		if request.InvocationId != nil || request.RunRef != nil || request.AfterCursor != nil || request.Invocation != nil {
			return errors.New("provider describe request contains unrelated fields")
		}
	case contractsv1.ProviderProtocolRequestOperationStart:
		if request.InvocationId == nil || request.IdempotencyKey == nil || request.Deadline == nil || request.StagedWorkspace == nil || request.InputManifestHash == nil || request.OutputContractHash == nil || request.ExecutorProfile == nil || request.Invocation == nil {
			return errors.New("provider start request is incomplete")
		}
		if request.RunRef != nil || request.AfterCursor != nil {
			return errors.New("provider start request contains unrelated fields")
		}
	case contractsv1.ProviderProtocolRequestOperationEvents:
		if request.RunRef == nil || request.AfterCursor == nil {
			return errors.New("provider events request is incomplete")
		}
		if request.InvocationId != nil || request.IdempotencyKey != nil || request.Deadline != nil || request.ExecutorProfile != nil || request.Invocation != nil {
			return errors.New("provider events request contains unrelated fields")
		}
	case contractsv1.ProviderProtocolRequestOperationInspect, contractsv1.ProviderProtocolRequestOperationCancel:
		if request.RunRef == nil {
			return errors.New("provider run request is incomplete")
		}
		if request.AfterCursor != nil || request.InvocationId != nil || request.IdempotencyKey != nil || request.Deadline != nil || request.ExecutorProfile != nil || request.Invocation != nil {
			return errors.New("provider run request contains unrelated fields")
		}
	default:
		return errors.New("unknown provider protocol operation")
	}
	return nil
}

func validateProtocolResponse(response contractsv1.ProviderProtocolResponse) error {
	count := 0
	for _, present := range []bool{response.Descriptor != nil, response.Run != nil, response.Events != nil, response.Observation != nil, response.Cancellation != nil, response.ErrorCode != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return errors.New("provider response must contain exactly one typed payload")
	}
	switch response.ResponseType {
	case contractsv1.ProviderProtocolResponseResponseTypeDescriptor:
		if response.Descriptor == nil {
			return errors.New("provider descriptor response is incomplete")
		}
	case contractsv1.ProviderProtocolResponseResponseTypeRun:
		if response.Run == nil {
			return errors.New("provider run response is incomplete")
		}
	case contractsv1.ProviderProtocolResponseResponseTypeEvents:
		if response.Events == nil {
			return errors.New("provider events response is incomplete")
		}
	case contractsv1.ProviderProtocolResponseResponseTypeObservation:
		if response.Observation == nil {
			return errors.New("provider observation response is incomplete")
		}
	case contractsv1.ProviderProtocolResponseResponseTypeCancellation:
		if response.Cancellation == nil {
			return errors.New("provider cancellation response is incomplete")
		}
	default:
		return errors.New("unknown provider response type")
	}
	return nil
}

func validateEventPage(page contractsv1.ProviderEventPage, runRef string, after int) error {
	if err := contract.ValidateDefinition("ProviderEventPage", page); err != nil {
		return err
	}
	if page.RunRef != runRef || page.AfterCursor != after || page.NextCursor < after {
		return errors.New("provider event page frontier is invalid")
	}
	expected := after + 1
	for _, event := range page.Events {
		if event.RunRef != runRef || event.Cursor != expected {
			return errors.New("provider event cursor is invalid")
		}
		expected++
	}
	if page.NextCursor != expected-1 {
		return errors.New("provider event page next cursor is invalid")
	}
	return nil
}

func ptrSHA(value contractsv1.SHA256) *contractsv1.SHA256 { return &value }

func readBoundedResult(path string, limit int64) ([]byte, contractsv1.SHA256, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", errors.New("provider result file is invalid")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, "", errors.New("provider result file is invalid")
	}
	sum := sha256.Sum256(body)
	return body, contractsv1.SHA256("sha256:" + hex.EncodeToString(sum[:])), nil
}

var _ Provider = (*AgentRunnerProvider)(nil)
