package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func TestProviderCancellationRequiresQuiescence(t *testing.T) {
	done := make(chan struct{})
	cancelled := make(chan struct{}, 2)
	p := &SubprocessProvider{runs: map[string]*subprocessRun{"key": {done: done}}, cancels: map[string]context.CancelFunc{"key": func() { cancelled <- struct{}{} }}}
	ctx, cancel := context.WithCancel(context.Background())
	answer := make(chan error, 1)
	go func() { answer <- p.Cancel(ctx, "key") }()
	<-cancelled
	select {
	case err := <-answer:
		t.Fatalf("cancel returned before process exit: %v", err)
	default:
	}
	cancel()
	if err := <-answer; !errors.Is(err, context.Canceled) {
		t.Fatalf("unsettled cancellation: %v", err)
	}
	close(done)
	if err := p.Cancel(context.Background(), "key"); err != nil {
		t.Fatal(err)
	}
	if err := p.Cancel(context.Background(), "unknown"); err == nil {
		t.Fatal("unknown run treated as settled")
	}
	adapter := &AgentRunnerProvider{runs: map[string]*agentRunnerRun{"key": {cancellation: &contractsv1.ProviderCancellation{Status: contractsv1.ProviderCancellationStatusUnsupported}}}}
	if err := adapter.Cancel(context.Background(), "key"); err == nil {
		t.Fatal("unsupported cancellation treated as settled")
	}
}

func TestProductionIsolationRejectsTrustedAndUnknownProfiles(t *testing.T) {
	engine := NewEngine(nil, nil, nil, inertProvider{}, NewMemoryLedger()).RequireProviderIsolation(contractsv1.ProviderIsolationProfileStagedSubprocess)
	if _, err := engine.providerIsolation(); err == nil {
		t.Fatal("production execution accepted an in-process provider")
	}
	engine.RequireProviderIsolation(contractsv1.ProviderIsolationProfile("future-profile"))
	if _, err := engine.providerIsolation(); err == nil {
		t.Fatal("unknown isolation profile was accepted")
	}
	forged, err := sealProviderIsolation(contractsv1.ProviderIsolationEvidence{
		Kind: contractsv1.ProviderIsolationEvidenceKindProviderIsolationEvidence, SchemaVersion: 1,
		Profile: contractsv1.ProviderIsolationProfileStagedSubprocess, Driver: contractsv1.ProviderIsolationEvidenceDriverSandboxExec,
		ExecutableSha256: testSHA("a"), StagedRootSha256: testSHA("b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine = NewEngine(nil, nil, nil, forgedIsolationProvider{evidence: forged}, NewMemoryLedger()).RequireProviderIsolation(contractsv1.ProviderIsolationProfileStagedSubprocess)
	if _, err := engine.providerIsolation(); err == nil {
		t.Fatal("production isolation accepted an in-process provider's self-attestation")
	}
}

func TestProviderExecutionReceiptBindsIsolationEvidence(t *testing.T) {
	evidence, err := sealProviderIsolation(contractsv1.ProviderIsolationEvidence{
		Kind: contractsv1.ProviderIsolationEvidenceKindProviderIsolationEvidence, SchemaVersion: 1,
		Profile: contractsv1.ProviderIsolationProfileTrustedInProcess, Driver: contractsv1.ProviderIsolationEvidenceDriverInProcess,
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	previous, err := sealReceipt("execution-a", 1, contractsv1.ReceiptReceiptTypeInvocation, at, nil, nil, nil, map[string]any{"invocation": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	key := contractsv1.SHA256("sha256:" + strings.Repeat("1", 64))
	invocation := Invocation{IdempotencyKey: string(key), Node: contractsv1.NodeDefinition{Id: "node-a"}, Isolation: &evidence}
	result := ProviderResult{IdempotencyKey: string(key), CompletedAt: at}
	receipts, err := postExecutionReceiptsWithState("execution-a", at, previous, invocation, result, []contractsv1.SHA256{}, true, "node_completed")
	if err != nil {
		t.Fatal(err)
	}
	if receipts[0].SchemaVersion != 2 {
		t.Fatalf("provider receipt schema=%d want=2", receipts[0].SchemaVersion)
	}
	var recorded contractsv1.ProviderIsolationEvidence
	if err := decodePayload(receipts[0].Payload["isolation"], &recorded); err != nil || recorded.EvidenceHash != evidence.EvidenceHash {
		t.Fatalf("provider receipt lost isolation evidence: %#v err=%v", recorded, err)
	}
}

func TestSubprocessProviderSandboxHidesAmbientEnvironmentAndOutsideFiles(t *testing.T) {
	root := t.TempDir()
	for _, child := range []string{"input", "output"} {
		if err := os.Mkdir(filepath.Join(root, child), 0700); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside-canary")
	if err := os.WriteFile(outside, []byte("outside-file-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNDECLARED_PROVIDER_CANARY", "ambient-env-canary")
	completedAt := time.Now().UTC()
	script := "#!/bin/sh\n" +
		"[ -z \"$UNDECLARED_PROVIDER_CANARY\" ] || exit 21\n" +
		"if /usr/bin/cat \"$1\" >/dev/null 2>&1; then exit 24; fi\n" +
		"if /usr/bin/tee input/tamper </dev/null >/dev/null 2>&1; then exit 25; fi\n" +
		"if /usr/bin/tee output/other </dev/null >/dev/null 2>&1; then exit 27; fi\n" +
		"printf output > output/result || exit 26\n" +
		"printf '%s\\n' '{\"idempotency_key\":\"sandbox-attempt\",\"completed_at\":\"" + completedAt.Format(time.RFC3339Nano) + "\",\"artifacts\":[]}'\n"
	if err := os.WriteFile(filepath.Join(root, "input", "provider.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	provider, err := NewSubprocessProvider(SubprocessProviderConfig{
		Executable:  "/bin/sh",
		Args:        []string{"input/provider.sh", outside},
		StagedRoot:  root,
		Environment: map[string]string{"LC_ALL": "C"},
	})
	if err != nil {
		if runtime.GOOS == "darwin" {
			t.Skip("macOS has no supported production process-containment sandbox")
		}
		t.Fatal(err)
	}
	invocation := Invocation{IdempotencyKey: "sandbox-attempt", Deadline: time.Now().Add(10 * time.Second)}
	if err := provider.Start(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	result, ready, err := waitForProvider(provider, invocation.IdempotencyKey, invocation.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	if !ready || result.IdempotencyKey != invocation.IdempotencyKey {
		t.Fatalf("isolated provider result=%#v ready=%v", result, ready)
	}
	if provider.IsolationEvidence().Profile != contractsv1.ProviderIsolationProfileStagedSubprocess {
		t.Fatal("subprocess provider did not expose staged isolation evidence")
	}
	if body, err := os.ReadFile(filepath.Join(root, "output", "result")); err != nil || string(body) != "output" {
		t.Fatalf("provider output root was not writable: %q err=%v", body, err)
	}
}

func TestSubprocessProviderEnforcesCancellationAndOutputLimit(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS has no supported production process-containment sandbox")
	}
	newProvider := func(script string, limit int) *SubprocessProvider {
		root := t.TempDir()
		for _, child := range []string{"input", "output"} {
			if err := os.Mkdir(filepath.Join(root, child), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, "input", "provider.sh"), []byte("#!/bin/sh\n"+script), 0700); err != nil {
			t.Fatal(err)
		}
		provider, err := NewSubprocessProvider(SubprocessProviderConfig{Executable: "/bin/sh", Args: []string{"input/provider.sh"}, StagedRoot: root, MaxOutputBytes: limit})
		if err != nil {
			t.Fatal(err)
		}
		return provider
	}

	t.Run("cancel", func(t *testing.T) {
		provider := newProvider("/usr/bin/sleep 30 & echo $! > output/result\nwhile :; do :; done\n", 1024)
		invocation := Invocation{IdempotencyKey: "cancel-attempt", Deadline: time.Now().Add(5 * time.Second)}
		if err := provider.Start(context.Background(), invocation); err != nil {
			t.Fatal(err)
		}
		var childPID int
		for childPID == 0 && time.Now().Before(invocation.Deadline) {
			body, _ := os.ReadFile(filepath.Join(provider.config.StagedRoot, "output", "result"))
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(body)))
			if childPID == 0 {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if childPID == 0 {
			t.Fatal("provider child process did not start")
		}
		if err := provider.Cancel(context.Background(), invocation.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		select {
		case <-provider.runs[invocation.IdempotencyKey].done:
		case <-time.After(2 * time.Second):
			t.Fatal("canceled provider process did not stop")
		}
		if childPIDObservable() {
			stopBy := time.Now().Add(time.Second)
			for processAlive(childPID) && time.Now().Before(stopBy) {
				time.Sleep(10 * time.Millisecond)
			}
			if processAlive(childPID) {
				terminateProcess(childPID)
				t.Fatal("canceled provider child process survived")
			}
		}
	})

	t.Run("output", func(t *testing.T) {
		provider := newProvider("i=0; while [ \"$i\" -lt 100; do printf 12345678; i=$((i+1)); done\n", 512)
		invocation := Invocation{IdempotencyKey: "output-attempt", Deadline: time.Now().Add(5 * time.Second)}
		if err := provider.Start(context.Background(), invocation); err != nil {
			t.Fatal(err)
		}
		_, ready, err := waitForProvider(provider, invocation.IdempotencyKey, invocation.Deadline)
		if err == nil || ready {
			t.Fatalf("oversized provider output was accepted: ready=%v err=%v", ready, err)
		}
	})

	t.Run("staged output", func(t *testing.T) {
		script := "i=0; while [ \"$i\" -lt 100; do printf 12345678; i=$((i+1)); done > output/result\n" +
			"printf '%s\\n' '{\"idempotency_key\":\"file-attempt\",\"completed_at\":\"2026-08-28T00:00:00Z\",\"artifacts\":[]}'\n"
		provider := newProvider(script, 512)
		invocation := Invocation{IdempotencyKey: "file-attempt", Deadline: time.Now().Add(5 * time.Second)}
		if err := provider.Start(context.Background(), invocation); err != nil {
			t.Fatal(err)
		}
		_, _, _ = waitForProvider(provider, invocation.IdempotencyKey, invocation.Deadline)
		info, statErr := os.Stat(filepath.Join(provider.config.StagedRoot, "output", "result"))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Size() > 512 {
			t.Fatalf("staged output escaped file limit: size=%d", info.Size())
		}
	})
}

func waitForProvider(provider *SubprocessProvider, key string, deadline time.Time) (ProviderResult, bool, error) {
	for time.Now().Before(deadline) {
		result, ready, err := provider.Poll(context.Background(), key)
		if ready || err != nil {
			return result, ready, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ProviderResult{}, false, context.DeadlineExceeded
}

type inertProvider struct{}

func (inertProvider) Start(context.Context, Invocation) error { return nil }
func (inertProvider) Poll(context.Context, string) (ProviderResult, bool, error) {
	return ProviderResult{}, false, nil
}
func (inertProvider) Cancel(context.Context, string) error { return nil }

type forgedIsolationProvider struct {
	inertProvider
	evidence contractsv1.ProviderIsolationEvidence
}

func (p forgedIsolationProvider) IsolationEvidence() contractsv1.ProviderIsolationEvidence {
	return p.evidence
}

func testSHA(digit string) *contractsv1.SHA256 {
	value := contractsv1.SHA256("sha256:" + strings.Repeat(digit, 64))
	return &value
}
