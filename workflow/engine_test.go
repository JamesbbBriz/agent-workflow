package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestEngineCompilesResolvesExecutesAndReplaysWithoutDuplicateProviderWork(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	seed := packFixture(t, scope, cutoff)
	registry, err := workflow.NewRegistry(
		workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed),
		workflow.NewIntentProducer(),
	)
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{
		Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition,
		NodeID: "research",
	}
	admit(t, ledger, registry, request)
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{
		"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead,
	}, outputCatalog(), provider, ledger)

	first, err := engine.RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if provider.work != 1 {
		t.Fatalf("provider performed %d units of work, want 1", provider.work)
	}
	if first.Compiled.Nodes[0].Definition.Id != "research" || len(first.Compiled.Nodes[0].Definition.Context) != 2 {
		t.Fatalf("defaults were not compiled into the node: %+v", first.Compiled.Nodes[0])
	}
	if first.Bundle.BundleHash != second.Bundle.BundleHash || first.Replay.BundleHash != second.Replay.BundleHash {
		t.Fatal("redelivery did not converge on the same bundle and replay")
	}
	if len(first.Artifacts) != 1 || len(first.Replay.Receipts) != 7 {
		t.Fatalf("unexpected result: artifacts=%d receipts=%d", len(first.Artifacts), len(first.Replay.Receipts))
	}
	semanticTamper := cloneReplayForTest(t, first.Replay)
	providerIndex := -1
	for index := range semanticTamper.Receipts {
		if semanticTamper.Receipts[index].ReceiptType == contractsv1.ReceiptReceiptTypeProviderExecution {
			providerIndex = index
			semanticTamper.Receipts[index].Payload["outcome"] = contractsv1.CampaignNodeExecutionStatusCompletedNoAction
			break
		}
	}
	semanticTamper = rehashReplayForTest(t, semanticTamper, providerIndex)
	if err := workflow.VerifyReplay(semanticTamper); err != nil {
		t.Fatalf("semantic tamper fixture is not hash-chain valid: %v", err)
	}
	if _, err := workflow.MaterializeReplay(semanticTamper, outputCatalog()); err == nil || !strings.Contains(err.Error(), "outcome does not match") {
		t.Fatalf("finished Replay accepted divergent provider outcome: %v", err)
	}
	tampered := first.Replay
	tampered.Receipts[3].Payload["accepted"] = false
	if err := workflow.VerifyReplay(tampered); err == nil {
		t.Fatal("tampered replay was accepted")
	}
}

func cloneReplayForTest(t *testing.T, replay contractsv1.ReplayBundle) contractsv1.ReplayBundle {
	t.Helper()
	body, err := json.Marshal(replay)
	if err != nil {
		t.Fatal(err)
	}
	var clone contractsv1.ReplayBundle
	if err := json.Unmarshal(body, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func rehashReplayForTest(t *testing.T, replay contractsv1.ReplayBundle, from int) contractsv1.ReplayBundle {
	t.Helper()
	if from < 0 {
		t.Fatal("provider execution receipt is missing")
	}
	previous := replay.Receipts[from-1].ReceiptHash
	for index := from; index < len(replay.Receipts); index++ {
		receipt := replay.Receipts[index]
		receipt.PreviousReceiptHash = previous
		receipt.ReceiptHash = ""
		receipt.ReceiptHash = contractsv1.SHA256(canonicalDigestForTest(t, receipt))
		replay.Receipts[index] = receipt
		previous = receipt.ReceiptHash
	}
	replay.CutoffReceiptHash = previous
	replay.BundleHash = ""
	replay.BundleHash = contractsv1.SHA256(canonicalDigestForTest(t, replay))
	return replay
}

func canonicalDigestForTest(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var wire any
	if err := decoder.Decode(&wire); err != nil {
		t.Fatal(err)
	}
	hash, err := workflow.Digest(wire)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestEngineRejectsUnadmittedWorkflowBeforeProviderExecution(t *testing.T) {
	definition := loadExample(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"})
	if err == nil || !strings.Contains(err.Error(), "not canonically admitted") || provider.work != 0 {
		t.Fatalf("unadmitted Workflow crossed the provider boundary: err=%v work=%d", err, provider.work)
	}
}

func TestEngineReusesCanonicalWorkflowAdmissionAcrossCampaigns(t *testing.T) {
	definition := loadExample(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	ledger := workflow.NewMemoryLedger()
	first := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
	admit(t, ledger, registry, first)
	second := first
	second.Job.Id = "job-b"
	second.Campaign.Id = "campaign-b"
	second.Campaign.JobId = second.Job.Id
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger)
	if _, err := engine.RunNode(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNode(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if provider.work != 2 {
		t.Fatalf("provider performed %d units of work, want one per Campaign", provider.work)
	}
}

func TestEngineRejectsNonAtomicLedgerBeforeProviderExecution(t *testing.T) {
	definition := loadExample(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	canonical := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
	admit(t, canonical, registry, request)
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledgerWithoutBatch{canonical}).RunNode(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "AtomicLedger") || provider.work != 0 {
		t.Fatalf("non-atomic Ledger crossed the provider boundary: err=%v work=%d", err, provider.work)
	}
}

func TestCampaignDriveRejectsNonAtomicLedgerBeforeReservation(t *testing.T) {
	definition := loadExample(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	canonical := workflow.NewMemoryLedger()
	request := workflow.CampaignRunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition}
	admit(t, canonical, registry, workflow.RunRequest{Job: request.Job, Campaign: request.Campaign, Workflow: definition, NodeID: "research"})
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledgerWithoutBatch{canonical})
	if _, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: request}); err == nil || !strings.Contains(err.Error(), "AtomicLedger") {
		t.Fatalf("Campaign Drive accepted a non-atomic Ledger: %v", err)
	}
	preview, err := engine.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.State.Usage.Attempts != 0 || preview.State.Nodes[0].Status != contractsv1.CampaignNodeExecutionStatusPending || preview.State.Nodes[0].ContextBundleHash != nil || provider.work != 0 {
		t.Fatalf("non-atomic Drive mutated provider state: state=%+v work=%d", preview.State, provider.work)
	}
}

type ledgerWithoutBatch struct{ workflow.Ledger }

func TestProviderResultRecoveryConvergesAfterLedgerFailure(t *testing.T) {
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	canonical := workflow.NewMemoryLedger()
	definition := loadExample(t)
	deadline := 1
	definition.Nodes[0].DeadlineSeconds = &deadline
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
	admit(t, canonical, registry, request)
	ledger := &failOnceLedger{Ledger: canonical, failType: contractsv1.ReceiptReceiptTypeResult}
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger)
	if _, err := engine.RunNode(context.Background(), request); err == nil {
		t.Fatal("injected ledger failure was ignored")
	}
	partial, err := canonical.Replay(executionIDForTest(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if !hasReceiptType(partial, contractsv1.ReceiptReceiptTypeProviderExecution) || hasReceiptType(partial, contractsv1.ReceiptReceiptTypeResult) || hasReceiptType(partial, contractsv1.ReceiptReceiptTypeTerminal) {
		t.Fatalf("failed atomic result batch left a partial accepted result: %+v", partial.Receipts)
	}
	key := provider.last.IdempotencyKey
	original := provider.results[key]
	tampered := original
	tampered.Outcome = contractsv1.CampaignNodeExecutionStatusCompletedNoAction
	provider.results[key] = tampered
	if _, err := engine.RunNode(context.Background(), request); err == nil {
		t.Fatal("redelivery accepted an outcome that differs from the recorded provider execution")
	}
	blocker := contractsv1.Identifier("context-missing")
	tampered.Outcome, tampered.BlockerCode = contractsv1.CampaignNodeExecutionStatusBlocked, &blocker
	provider.results[key] = tampered
	if _, err := engine.RunNode(context.Background(), request); err == nil {
		t.Fatal("redelivery accepted a blocker that differs from the recorded provider execution")
	}
	provider.results[key] = original
	time.Sleep(1100 * time.Millisecond)
	result, err := engine.RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if provider.work != 1 || len(result.Replay.Receipts) != 7 {
		t.Fatalf("redelivery duplicated provider work or failed to converge: work=%d receipts=%d", provider.work, len(result.Replay.Receipts))
	}
	preview, err := engine.Preview(context.Background(), workflow.CampaignRunRequest{Job: request.Job, Campaign: request.Campaign, Workflow: request.Workflow})
	if err != nil {
		t.Fatal(err)
	}
	if preview.State.NextNodeId == nil || *preview.State.NextNodeId != "review" || preview.State.Nodes[0].Status != contractsv1.CampaignNodeExecutionStatusCompleted {
		t.Fatalf("redelivery completed only the child Replay, not its Campaign transition: %+v", preview.State)
	}
}

func hasReceiptType(replay contractsv1.ReplayBundle, receiptType contractsv1.ReceiptReceiptType) bool {
	for _, receipt := range replay.Receipts {
		if receipt.ReceiptType == receiptType {
			return true
		}
	}
	return false
}

func TestExpiredProviderPollBecomesOneTerminalReceipt(t *testing.T) {
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	definition := loadExample(t)
	deadline := 1
	definition.Nodes[0].DeadlineSeconds = &deadline
	provider := &pendingProvider{}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
	admit(t, ledger, registry, request)
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger)
	if _, err := engine.RunNode(context.Background(), request); err == nil {
		t.Fatal("pending provider unexpectedly completed")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := engine.RunNode(context.Background(), request); !errors.Is(err, workflow.ErrProviderDeadline) {
		t.Fatalf("expired provider did not become typed terminal: %v", err)
	}
	replay, err := ledger.Replay(executionIDForTest(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.MaterializeInvocation(replay); err != nil {
		t.Fatalf("terminal replay lost its canonical invocation: %v", err)
	}
	if provider.cancelled != 1 || replay.Receipts[len(replay.Receipts)-1].Payload["state"] != "deadline_expired" {
		t.Fatalf("deadline terminal did not converge: cancelled=%d replay=%+v", provider.cancelled, replay)
	}
	count := len(replay.Receipts)
	if _, err := engine.RunNode(context.Background(), request); !errors.Is(err, workflow.ErrProviderDeadline) {
		t.Fatalf("terminal redelivery changed outcome: %v", err)
	}
	replay, _ = ledger.Replay(executionIDForTest(t, request))
	if len(replay.Receipts) != count {
		t.Fatal("terminal redelivery appended another receipt")
	}
}

func TestProviderResultCompletedAfterDeadlineIsRejected(t *testing.T) {
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	definition := loadExample(t)
	deadline := 1
	definition.Nodes[0].DeadlineSeconds = &deadline
	provider := &lateProvider{}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
	admit(t, ledger, registry, request)
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger)
	if _, err := engine.RunNode(context.Background(), request); err == nil {
		t.Fatal("late provider unexpectedly completed on the first poll")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := engine.RunNode(context.Background(), request); !errors.Is(err, workflow.ErrProviderDeadline) {
		t.Fatalf("late provider result crossed the deadline: %v", err)
	}
}

type failOnceLedger struct {
	workflow.Ledger
	appendCount int
	failType    contractsv1.ReceiptReceiptType
	failed      bool
}

type pendingProvider struct{ cancelled int }

func (*pendingProvider) Start(context.Context, workflow.Invocation) error { return nil }
func (*pendingProvider) Poll(context.Context, string) (workflow.ProviderResult, bool, error) {
	return workflow.ProviderResult{}, false, nil
}

type lateProvider struct {
	invocation workflow.Invocation
	polls      int
}

func (p *lateProvider) Start(_ context.Context, invocation workflow.Invocation) error {
	p.invocation = invocation
	return nil
}
func (p *lateProvider) Poll(context.Context, string) (workflow.ProviderResult, bool, error) {
	p.polls++
	if p.polls == 1 {
		return workflow.ProviderResult{}, false, nil
	}
	content := map[string]any{"recommendation": "too late"}
	hash, _ := workflow.Digest(content)
	artifact := contractsv1.ActionArtifact{
		Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "artifact-late",
		ArtifactType: "recommendation", JobId: p.invocation.JobID, CampaignId: p.invocation.CampaignID,
		WorkflowRef: p.invocation.WorkflowRef, NodeId: p.invocation.Node.Id, InputHashes: p.invocation.InputHashes,
		Content: content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending,
	}
	return workflow.ProviderResult{IdempotencyKey: p.invocation.IdempotencyKey, CompletedAt: p.invocation.Deadline.Add(-time.Nanosecond), Artifacts: []contractsv1.ActionArtifact{artifact}}, true, nil
}
func (*lateProvider) Cancel(context.Context, string) error { return nil }
func (p *pendingProvider) Cancel(context.Context, string) error {
	p.cancelled++
	return nil
}

func executionIDForTest(t *testing.T, request workflow.RunRequest) string {
	t.Helper()
	hash, err := workflow.Digest(struct {
		JobID      contractsv1.Identifier
		CampaignID contractsv1.Identifier
		Workflow   string
		NodeID     string
	}{request.Job.Id, request.Campaign.Id, string(request.Workflow.Id) + "@1", request.NodeID})
	if err != nil {
		t.Fatal(err)
	}
	return "run-" + hash[len("sha256:"):len("sha256:")+20]
}

func (l *failOnceLedger) Append(receipt contractsv1.Receipt) error {
	l.appendCount++
	if !l.failed && receipt.ReceiptType == l.failType {
		l.failed = true
		return errors.New("injected append failure")
	}
	return l.Ledger.Append(receipt)
}

func (l *failOnceLedger) AppendBatch(receipts []contractsv1.Receipt) error {
	for _, receipt := range receipts {
		l.appendCount++
		if !l.failed && receipt.ReceiptType == l.failType {
			l.failed = true
			return errors.New("injected append failure")
		}
	}
	batch, ok := l.Ledger.(interface {
		AppendBatch([]contractsv1.Receipt) error
	})
	if !ok {
		return errors.New("test ledger does not support atomic batches")
	}
	return batch.AppendBatch(receipts)
}

func TestFileLedgerSurvivesCoreRestartAndRedelivery(t *testing.T) {
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	ledger, err := workflow.OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"}
	admit(t, ledger, registry, request)
	first, err := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := workflow.OpenFileLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	restartedProvider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	second, err := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), restartedProvider, reopened).RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if provider.work != 1 || restartedProvider.work != 0 || first.Replay.BundleHash != second.Replay.BundleHash {
		t.Fatalf("restart redelivery diverged: first_work=%d restarted_work=%d first=%s second=%s", provider.work, restartedProvider.work, first.Replay.BundleHash, second.Replay.BundleHash)
	}
	material, err := workflow.MaterializeReplay(second.Replay, outputCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if material.Invocation.Playbook.Objective == "" || material.Invocation.IntentChain.PackType != "intent-chain" || len(material.Artifacts) != 1 {
		t.Fatalf("replay did not reconstruct the provider boundary: %+v", material)
	}
}

func TestCompilerInjectsRequiredIntentChainAndPlaybook(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	definition.DefaultContext = definition.DefaultContext[1:]
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
	admit(t, ledger, registry, request)
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(provider.last.Playbook, definition.Intent) || provider.last.IntentChain.PackType != "intent-chain" {
		t.Fatalf("provider did not receive the immutable playbook and intent chain: %+v", provider.last)
	}
}

func TestCompilerNormalizesV1SlotsWithoutExecutionMetadata(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	for nodeIndex := range definition.Nodes {
		for slotIndex := range definition.Nodes[nodeIndex].OutputSlots {
			definition.Nodes[nodeIndex].OutputSlots[slotIndex].Consumers = nil
		}
	}
	for slotIndex := range definition.Outputs {
		definition.Outputs[slotIndex].Consumers = nil
	}
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
	admit(t, ledger, registry, request)
	result, err := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Compiled.Nodes[0].Definition.OutputSlots[0].ContentSchema == nil || len(result.Compiled.Nodes[0].Definition.OutputSlots[0].Consumers) != 1 {
		t.Fatalf("legacy v1 slots were not made explicit: %+v", result.Compiled.Nodes[0].Definition.OutputSlots[0])
	}
}

func TestCoreIssuesProviderDeadline(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"}
	admit(t, ledger, registry, request)
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(provider.last.Deadline); remaining <= 0 || remaining > 11*time.Minute {
		t.Fatalf("provider deadline escaped the Node runtime bound: %s", remaining)
	}
}

func TestEngineRejectsFutureEvidenceCutoff(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"},
	)
	if err == nil || provider.work != 0 {
		t.Fatalf("future evidence cutoff crossed the provider boundary: err=%v work=%d", err, provider.work)
	}
}

func TestRequiredContextFailsClosed(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	registry, err := workflow.NewRegistry(
		workflow.NewCatalogProducer("project-brief", "project-brief", 1),
		workflow.NewIntentProducer(),
	)
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
	admissionRegistry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	admit(t, ledger, admissionRegistry, request)
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger)
	_, err = engine.RunNode(context.Background(), request)
	var missing *workflow.NeedsContextError
	if !errors.As(err, &missing) || len(missing.Requirements) != 1 || missing.Requirements[0] != "project-brief" {
		t.Fatalf("expected typed project-brief blocker, got %v", err)
	}
	if provider.work != 0 {
		t.Fatal("provider ran without required context")
	}
	resumed, err := workflow.NewEngine(admissionRegistry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).RunNode(context.Background(), request)
	if err != nil || provider.work != 1 || len(resumed.Artifacts) != 1 {
		t.Fatalf("new verified Context did not resume the same Node: result=%+v err=%v work=%d", resumed, err, provider.work)
	}
	preview, err := workflow.NewEngine(admissionRegistry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).Preview(context.Background(), workflow.CampaignRunRequest{Job: request.Job, Campaign: request.Campaign, Workflow: request.Workflow})
	if err != nil || preview.State.Nodes[0].ContextBundleHash == nil || preview.State.Nodes[0].Usage.Attempts != 1 {
		t.Fatalf("Context recovery was not retained in canonical Campaign state: preview=%+v err=%v", preview, err)
	}
}

func TestContextPackAuthorityFailsClosedBeforeProviderExecution(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	tests := []struct {
		name   string
		mutate func(*contractsv1.ContextPackEdition)
	}{
		{name: "tampered", mutate: func(pack *contractsv1.ContextPackEdition) { pack.Content = map[string]any{"brief": "tampered"} }},
		{name: "stale", mutate: func(pack *contractsv1.ContextPackEdition) { pack.CapturedAt = cutoff.Add(-8 * 24 * time.Hour) }},
		{name: "partial disallowed", mutate: func(pack *contractsv1.ContextPackEdition) {
			pack.Coverage = contractsv1.ContextPackEditionCoveragePartial
		}},
		{name: "wrong scope", mutate: func(pack *contractsv1.ContextPackEdition) { pack.Scope.SubjectIds = []string{"project-b"} }},
		{name: "unknown pack version", mutate: func(pack *contractsv1.ContextPackEdition) { pack.PackSchemaVersion = 2 }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pack := packFixture(t, scope, cutoff)
			test.mutate(&pack)
			registry, err := workflow.NewRegistry(
				workflow.NewCatalogProducer("project-brief", "project-brief", 1, pack),
				workflow.NewIntentProducer(),
			)
			if err != nil {
				t.Fatal(err)
			}
			provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
			ledger := workflow.NewMemoryLedger()
			request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
			admissionRegistry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
			admit(t, ledger, admissionRegistry, request)
			engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger)
			_, err = engine.RunNode(context.Background(), request)
			var blocker *workflow.NeedsContextError
			if !errors.As(err, &blocker) || blocker.Reasons["project-brief"] == "" {
				t.Fatalf("invalid required context did not become a typed blocker: %v", err)
			}
			if provider.work != 0 {
				t.Fatal("provider ran with invalid context")
			}
		})
	}
}

func TestOptionalContextProducesExplicitDegradedBundle(t *testing.T) {
	t.Parallel()
	definition := loadExample(t)
	for index := range definition.DefaultContext {
		if definition.DefaultContext[index].Id == "project-brief" {
			definition.DefaultContext[index].Required = false
		}
	}
	registry, err := workflow.NewRegistry(
		workflow.NewCatalogProducer("project-brief", "project-brief", 1),
		workflow.NewIntentProducer(),
	)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"}
	admit(t, ledger, registry, request)
	result, err := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).RunNode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Bundle.Degraded || len(result.Bundle.MissingOptional) != 1 || result.Bundle.MissingOptional[0] != "project-brief" {
		t.Fatalf("optional context was not made explicit: %+v", result.Bundle)
	}
}

func TestCompilerRejectsUnavailableProducerConflictingDefaultsAndBrokenSlots(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	seed := packFixture(t, scope, cutoff)
	tests := []struct {
		name     string
		registry func() *workflow.Registry
		mutate   func(*contractsv1.WorkflowDefinition)
	}{
		{
			name: "producer unavailable",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewIntentProducer())
				return registry
			},
			mutate: func(*contractsv1.WorkflowDefinition) {},
		},
		{
			name: "default conflict",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				conflict := definition.DefaultContext[0]
				conflict.AllowPartial = true
				definition.Nodes[0].Context = append(definition.Nodes[0].Context, conflict)
			},
		},
		{
			name: "broken slot flow",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				definition.Nodes[1].InputSlots[0].ArtifactType = "other-artifact"
			},
		},
		{
			name: "unknown output consumer",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				definition.Nodes[0].OutputSlots[0].Consumers = []string{"missing-node"}
			},
		},
		{
			name: "reserved context pack output",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				kind := contractsv1.SlotArtifactKindContextPack
				definition.Nodes[0].OutputSlots[0].ArtifactKind = &kind
			},
		},
		{
			name: "excessive output fanout",
			registry: func() *workflow.Registry {
				registry, _ := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, seed), workflow.NewIntentProducer())
				return registry
			},
			mutate: func(definition *contractsv1.WorkflowDefinition) {
				definition.Nodes[0].OutputSlots[0].MaxItems = 9
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definition := loadExample(t)
			test.mutate(&definition)
			provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
			engine := workflow.NewEngine(test.registry(), workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger())
			_, err := engine.RunNode(context.Background(), workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: definition, NodeID: "research"})
			if err == nil {
				t.Fatal("invalid workflow compiled")
			}
			if provider.work != 0 {
				t.Fatal("provider ran after compile failure")
			}
		})
	}
}

func TestEngineRejectsUnknownAggregateContractBeforeProviderExecution(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	job := jobFixture(scope)
	job.SchemaVersion = 2
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: job, Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"},
	)
	if err == nil || provider.work != 0 {
		t.Fatalf("unknown Job contract crossed the provider boundary: err=%v work=%d", err, provider.work)
	}
}

func TestEngineRejectsStaleDeclaredAggregateHash(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	job := jobFixture(scope)
	wrong := contractsv1.SHA256(zeroHash)
	job.DefinitionHash = &wrong
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult)}
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, workflow.NewMemoryLedger()).RunNode(
		context.Background(), workflow.RunRequest{Job: job, Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"},
	)
	if err == nil || provider.work != 0 {
		t.Fatalf("stale Job hash crossed the provider boundary: err=%v work=%d", err, provider.work)
	}
}

func TestEngineRejectsArtifactContentOutsideTheDeclaredSchema(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult), content: map[string]any{"recommendation": 42}}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"}
	admit(t, ledger, registry, request)
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).RunNode(context.Background(), request)
	if err == nil {
		t.Fatal("artifact content outside the declared schema was accepted")
	}
}

func TestEngineRejectsOversizedArtifactContent(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	provider := &memoProvider{results: make(map[string]workflow.ProviderResult), content: map[string]any{"recommendation": strings.Repeat("x", 1<<20)}}
	ledger := workflow.NewMemoryLedger()
	request := workflow.RunRequest{Job: jobFixture(scope), Campaign: campaignFixture(scope, cutoff), Workflow: loadExample(t), NodeID: "research"}
	admit(t, ledger, registry, request)
	_, err = workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger).RunNode(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized artifact was not rejected: %v", err)
	}
}

type memoProvider struct {
	work    int
	results map[string]workflow.ProviderResult
	content any
	last    workflow.Invocation
}

func (p *memoProvider) Start(_ context.Context, invocation workflow.Invocation) error {
	if _, ok := p.results[invocation.IdempotencyKey]; ok {
		return nil
	}
	p.work++
	p.last = invocation
	content := p.content
	if content == nil {
		content = map[string]any{"recommendation": "Keep the bounded workflow."}
	}
	hash, err := workflow.Digest(content)
	if err != nil {
		return err
	}
	result := []contractsv1.ActionArtifact{{
		Kind: contractsv1.ActionArtifactKindActionArtifact, SchemaVersion: 1, Id: "artifact-recommendation-1",
		ArtifactType: "recommendation", JobId: invocation.JobID, CampaignId: invocation.CampaignID,
		WorkflowRef: invocation.WorkflowRef, NodeId: invocation.Node.Id,
		InputHashes: invocation.InputHashes,
		Content:     content, ContentSha256: contractsv1.SHA256(hash), ApprovalState: contractsv1.ActionArtifactApprovalStatePending,
	}}
	p.results[invocation.IdempotencyKey] = workflow.ProviderResult{IdempotencyKey: invocation.IdempotencyKey, CompletedAt: time.Now().UTC(), Artifacts: result}
	return nil
}

func (p *memoProvider) Poll(_ context.Context, key string) (workflow.ProviderResult, bool, error) {
	result, ok := p.results[key]
	return result, ok, nil
}

func (*memoProvider) Cancel(context.Context, string) error { return nil }

func loadExample(t *testing.T) contractsv1.WorkflowDefinition {
	t.Helper()
	_, current, _, _ := runtime.Caller(0)
	body, err := os.ReadFile(filepath.Join(filepath.Dir(current), "..", "examples", "research-review.workflow.json"))
	if err != nil {
		t.Fatal(err)
	}
	var definition contractsv1.WorkflowDefinition
	if err := json.Unmarshal(body, &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

func intent(kind contractsv1.IntentCardKind, title string) contractsv1.IntentCard {
	return contractsv1.IntentCard{SchemaVersion: 1, Kind: kind, Title: title, Summary: title, Objective: title, SuccessSignals: []string{"done"}, NonGoals: []string{"none"}, Completion: []string{"done"}, NoActionWhen: []string{"not needed"}}
}

func jobFixture(scope contractsv1.Scope) contractsv1.JobDefinition {
	return contractsv1.JobDefinition{Kind: contractsv1.JobDefinitionKindJobDefinition, SchemaVersion: 1, Id: "job-a", Intent: intent(contractsv1.IntentCardKindJob, "Job"), Scope: scope, Budget: budget(), CampaignArchetypes: []string{"research"}}
}

func campaignFixture(scope contractsv1.Scope, cutoff time.Time) contractsv1.CampaignDefinition {
	return contractsv1.CampaignDefinition{Kind: contractsv1.CampaignDefinitionKindCampaignDefinition, SchemaVersion: 1, Id: "campaign-a", JobId: "job-a", Archetype: "research", Intent: intent(contractsv1.IntentCardKindCampaign, "Campaign"), Scope: scope, EvidenceFrontier: contractsv1.EvidenceFrontier{Cutoff: cutoff, SourceHashes: []contractsv1.SHA256{}}, WorkflowPlan: []contractsv1.WorkflowRef{"research-review@1"}, Budget: budget()}
}

func budget() contractsv1.Budget {
	return contractsv1.Budget{MaxAttempts: 2, MaxActions: 1, MaxCandidates: 3}
}

func packFixture(t *testing.T, scope contractsv1.Scope, cutoff time.Time) contractsv1.ContextPackEdition {
	t.Helper()
	content := map[string]any{"brief": "Prefer evidence-bound recommendations."}
	hash, err := workflow.Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	return contractsv1.ContextPackEdition{
		Kind: contractsv1.ContextPackEditionKindContextPackEdition, SchemaVersion: 1,
		Id: "pack-project-brief-1", PackType: "project-brief", PackSchemaVersion: 1,
		Authority: contractsv1.ContextPackEditionAuthorityCanonical, Scope: scope,
		CapturedAt: cutoff.Add(-time.Hour), ExpiresAt: cutoff.Add(24 * time.Hour),
		Coverage: contractsv1.ContextPackEditionCoverageComplete, Content: content, ContentSha256: contractsv1.SHA256(hash),
		Provenance: []contractsv1.ArtifactRef{{Id: "seed-receipt", Kind: contractsv1.ArtifactRefKindReceipt, ArtifactType: "seed", SchemaVersion: 1, Sha256: contractsv1.SHA256(zeroHash), MediaType: "application/json"}},
	}
}

func outputCatalog() workflow.OutputCatalog {
	return workflow.OutputCatalog{
		"recommendation@1": func(value any) error {
			object, ok := value.(map[string]any)
			if !ok {
				return errors.New("recommendation must be an object")
			}
			text, ok := object["recommendation"].(string)
			if !ok || text == "" {
				return errors.New("recommendation is required")
			}
			return nil
		},
	}
}

const zeroHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
