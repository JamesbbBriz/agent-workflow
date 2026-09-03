package workflow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestRetireBlockedCampaignRequiresQuiescenceAndExactReceipt(t *testing.T) {
	for _, expiredChild := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired_child_%t", expiredChild), func(t *testing.T) {
			ctx := context.Background()
			cutoff := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
			now := cutoff.Add(time.Hour)
			scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
			registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
			if err != nil {
				t.Fatal(err)
			}
			definition := loadExample(t)
			definition.Nodes = definition.Nodes[:1]
			definition.Nodes[0].OutcomeRoutes = contractsv1.NodeOutcomeRoutes{string(contractsv1.CampaignNodeExecutionStatusBlocked): contractsv1.NodeOutcomeRouteStop}
			definition.Nodes[0].OutputSlots[0].Consumers = []string{"workflow-output"}
			definition.Outputs = append([]contractsv1.Slot(nil), definition.Nodes[0].OutputSlots...)
			definition.Outputs[0].Consumers = append([]string(nil), definition.Intent.Consumers...)
			one := 1
			definition.Nodes[0].DeadlineSeconds = &one
			definition.Nodes[0].Budget.MaxAttempts = 1
			job, campaign := jobFixture(scope), campaignFixture(scope, cutoff)
			campaign.Budget.MaxAttempts = 1
			campaign.Budget.MaxDurationSeconds = &one
			ledger := workflow.NewMemoryLedger()
			admit(t, ledger, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
			provider := &retirementTestProvider{now: &now}
			engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, dagOutputCatalog(), provider, ledger).WithClock(func() time.Time { return now })
			request := workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}
			active, err := engine.Drive(ctx, workflow.CampaignDriveCommand{CampaignRunRequest: request})
			if err != nil {
				t.Fatal(err)
			}
			retirement := contractsv1.CampaignRetirementRequest{SchemaVersion: 1, JobId: job.Id, CampaignId: campaign.Id, ExpectedReceiptHash: active.CampaignReplay.Receipts[len(active.CampaignReplay.Receipts)-1].ReceiptHash, Actor: "operator:test", Reason: "Retire failed legacy run; retain its evidence."}
			if _, err := engine.RetireBlockedCampaign(ctx, request, retirement); err == nil {
				t.Fatal("active invocation retired")
			}
			if expiredChild {
				now = now.Add(2 * time.Second)
				blocked, err := engine.Drive(ctx, workflow.CampaignDriveCommand{CampaignRunRequest: request})
				if err != nil || blocked.State.Status != contractsv1.CampaignExecutionStateStatusBlocked {
					t.Fatalf("expected duration blocker: %+v %v", blocked.State, err)
				}
				retirement.ExpectedReceiptHash = blocked.CampaignReplay.Receipts[len(blocked.CampaignReplay.Receipts)-1].ReceiptHash
				if _, err := engine.RetireBlockedCampaign(ctx, request, retirement); err == nil || !strings.Contains(err.Error(), "unfinished child") {
					t.Fatalf("duration-exhausted parent hid active child: %v", err)
				}
				return
			}
			provider.ready = true
			blocked, err := engine.Drive(ctx, workflow.CampaignDriveCommand{CampaignRunRequest: request})
			if err != nil || blocked.State.Status != contractsv1.CampaignExecutionStateStatusBlocked {
				t.Fatalf("blocked=%+v err=%v", blocked.State, err)
			}
			retirement.ExpectedReceiptHash = blocked.CampaignReplay.Receipts[len(blocked.CampaignReplay.Receipts)-1].ReceiptHash
			stale := retirement
			stale.ExpectedReceiptHash = contractsv1.SHA256("sha256:" + strings.Repeat("0", 64))
			if _, err := engine.RetireBlockedCampaign(ctx, request, stale); err == nil {
				t.Fatal("stale head retired")
			}
			var calls atomic.Int64
			barrier := make(chan struct{})
			engine.WithClock(func() time.Time {
				n := calls.Add(1)
				if n == 2 {
					close(barrier)
				}
				<-barrier
				return now.Add(time.Duration(n) * time.Nanosecond)
			})
			type answer struct {
				receipt contractsv1.CampaignDriveReceipt
				err     error
			}
			answers := make(chan answer, 2)
			for i := 0; i < 2; i++ {
				go func() {
					r, err := engine.RetireBlockedCampaign(ctx, request, retirement)
					answers <- answer{r, err}
				}()
			}
			first, second := <-answers, <-answers
			if first.err != nil || second.err != nil || first.receipt.CampaignReplay.BundleHash != second.receipt.CampaignReplay.BundleHash {
				t.Fatalf("concurrent exact retirement failed: %v %v", first.err, second.err)
			}
			retired := first.receipt
			if retired.State.Status != contractsv1.CampaignExecutionStateStatusTerminal || retired.State.BlockerCode == nil || *retired.State.BlockerCode != *blocked.State.BlockerCode {
				t.Fatalf("lost terminal or original blocker: %+v", retired.State)
			}
			if err := workflow.VerifyReplay(*retired.CampaignReplay); err != nil {
				t.Fatal(err)
			}
			terminal := retired.CampaignReplay.Receipts[len(retired.CampaignReplay.Receipts)-1]
			if terminal.SchemaVersion != 5 {
				t.Fatal("retirement changed historical terminal version semantics")
			}
			wrongVersion := cloneReplayForTest(t, *retired.CampaignReplay)
			wrongVersion.Receipts[len(wrongVersion.Receipts)-1].SchemaVersion = 2
			wrongVersion = rehashReplayForTest(t, wrongVersion, len(wrongVersion.Receipts)-1)
			versionLedger := workflow.NewMemoryLedger()
			for _, receipt := range wrongVersion.Receipts[:len(wrongVersion.Receipts)-1] {
				if err := versionLedger.Append(receipt); err != nil {
					t.Fatal(err)
				}
			}
			if err := versionLedger.Append(wrongVersion.Receipts[len(wrongVersion.Receipts)-1]); err == nil {
				t.Fatal("retirement accepted under completed-only v2 schema")
			}
			var childBindings map[string]string
			body, _ := json.Marshal(terminal.Payload["child_replays"])
			if err := json.Unmarshal(body, &childBindings); err != nil || len(childBindings) != 1 {
				t.Fatalf("missing canonical child cutoffs: %s %v", body, err)
			}
			for childID, cutoffHash := range childBindings {
				child, err := ledger.Replay(childID)
				if err != nil || string(child.BundleHash) != cutoffHash {
					t.Fatalf("wrong child cutoff: %v", err)
				}
				// A later well-formed ledger suffix must not change an already
				// accepted retirement's exact child prefix.
				next := child.Receipts[0]
				next.Id += "-later"
				next.AggregateVersion = len(child.Receipts) + 1
				next.OccurredAt = now.Add(time.Second)
				next.PreviousReceiptHash = child.CutoffReceiptHash
				next.ReceiptHash = ""
				next.ReceiptHash = contractsv1.SHA256(canonicalDigestForTest(t, next))
				if err := ledger.Append(next); err != nil {
					t.Fatal(err)
				}
			}
			again, err := engine.RetireBlockedCampaign(ctx, request, retirement)
			if err != nil || again.Transitions != 0 || again.CampaignReplay.BundleHash != retired.CampaignReplay.BundleHash {
				t.Fatalf("retry was not idempotent: %+v %v", again, err)
			}
			conflict := retirement
			conflict.Reason = "different authority"
			if _, err := engine.RetireBlockedCampaign(ctx, request, conflict); err == nil {
				t.Fatal("conflicting retry accepted")
			}
			driven, err := engine.Drive(ctx, workflow.CampaignDriveCommand{CampaignRunRequest: request, MaxTransitions: 10})
			if err != nil || driven.State.Status != contractsv1.CampaignExecutionStateStatusTerminal || driven.Transitions != 0 || provider.starts != 1 {
				t.Fatalf("retired Campaign resumed: %+v %v starts=%d", driven, err, provider.starts)
			}
			extra := terminal
			extra.Id += "-illegal-suffix"
			extra.AggregateVersion++
			extra.PreviousReceiptHash = terminal.ReceiptHash
			extra.ReceiptHash = ""
			extra.ReceiptHash = contractsv1.SHA256(canonicalDigestForTest(t, extra))
			if err := ledger.Append(extra); err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Preview(ctx, request); err == nil {
				t.Fatal("post-terminal Campaign receipt accepted")
			}
		})
	}
}

type retirementTestProvider struct {
	now        *time.Time
	ready      bool
	starts     int
	invocation workflow.Invocation
}

func (p *retirementTestProvider) Start(_ context.Context, invocation workflow.Invocation) error {
	if p.invocation.IdempotencyKey == "" {
		p.starts++
		p.invocation = invocation
	}
	return nil
}
func (p *retirementTestProvider) Poll(_ context.Context, key string) (workflow.ProviderResult, bool, error) {
	blocker := contractsv1.Identifier("provider-timeout")
	return workflow.ProviderResult{IdempotencyKey: key, CompletedAt: *p.now, Artifacts: []contractsv1.ActionArtifact{}, Outcome: contractsv1.CampaignNodeExecutionStatusBlocked, BlockerCode: &blocker}, p.ready, nil
}
func (*retirementTestProvider) Cancel(context.Context, string) error { return nil }
