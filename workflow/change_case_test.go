package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

func TestChangeCaseCoordinatesTwoCampaignsThroughConflictApprovalAndReadback(t *testing.T) {
	ctx := context.Background()
	sources := workflow.NewMemoryLedger()
	first := completedChangeSource(t, sources, "campaign-a", map[string]any{"recommendation": "A"})
	second := completedChangeSource(t, sources, "campaign-b", map[string]any{"recommendation": "B"})
	resolutionSource := completedChangeSource(t, sources, "campaign-resolver", map[string]any{"recommendation": "resolved"})
	resource := resourceFixture(t, 1, map[string]any{"title": "baseline"})
	authority := &testResourceAuthority{current: resource}
	mutation := &testMutationAdapter{}
	core := workflow.NewChangeCaseCore(workflow.NewMemoryLedger(), sources, outputCatalog(), workflow.ChangeCaseCatalog{
		Mergers:           map[contractsv1.Identifier]workflow.ChangeMergeAdapter{"document": conflictMerge{}},
		Resources:         map[contractsv1.Identifier]workflow.ResourceAuthority{"document": authority},
		Mutations:         map[contractsv1.Identifier]workflow.MutationAdapter{"document": mutation},
		ApprovalActors:    map[contractsv1.Identifier][]string{"document": {"reviewer@example.com"}},
		ResolutionSources: map[contractsv1.Identifier][]workflow.ResolutionSourceAuthority{"document": {{WorkflowRef: "workflow-campaign-resolver@1", NodeID: "research", Reason: contractsv1.ProposalReplacementReasonResolver}}},
	})
	at := time.Now().UTC()
	ready, err := core.SubmitProposal(ctx, resource, first, at)
	if err != nil || ready.Status != contractsv1.ChangeCaseStateStatusReady {
		t.Fatalf("first proposal was not ready: state=%+v err=%v", ready, err)
	}
	malformed := second
	malformed.Replacement = &contractsv1.ProposalReplacement{ProposalId: "missing-proposal", Reason: contractsv1.ProposalReplacementReasonResolver}
	if _, err := core.SubmitProposal(ctx, resource, malformed, at.Add(time.Second)); err == nil {
		t.Fatal("replacement inherited authority from an unknown proposal")
	}
	conflicted, err := core.SubmitProposal(ctx, resource, second, at.Add(time.Second))
	if err != nil || conflicted.Status != contractsv1.ChangeCaseStateStatusConflicted || len(conflicted.Proposals) != 2 || conflicted.Conflicts == nil {
		t.Fatalf("two Campaign proposals did not share one conflicted case: state=%+v err=%v", conflicted, err)
	}
	if _, err := core.AcquireLease(ctx, conflicted.Id, time.Minute, at.Add(2*time.Second)); err == nil {
		t.Fatal("conflicted case acquired a lease without an exact resolution approval")
	}
	resolutionSource.Replacement = &contractsv1.ProposalReplacement{ProposalId: conflicted.Proposals[0].Id, Reason: contractsv1.ProposalReplacementReasonResolver}
	selfAttested := first
	selfAttested.Replacement = resolutionSource.Replacement
	if _, err := core.ProposeResolution(conflicted.Id, selfAttested, at.Add(3*time.Second)); err == nil {
		t.Fatal("a conflicting source self-attested as the resolver")
	}
	resolved, err := core.ProposeResolution(conflicted.Id, resolutionSource, at.Add(3*time.Second))
	if err != nil || resolved.Status != contractsv1.ChangeCaseStateStatusAwaitingResolutionApproval {
		t.Fatalf("resolution was not reviewable: state=%+v err=%v", resolved, err)
	}
	redelivered, err := core.ProposeResolution(conflicted.Id, resolutionSource, at.Add(4*time.Second))
	if err != nil || redelivered.Resolution == nil || redelivered.Resolution.ResolutionHash != resolved.Resolution.ResolutionHash || len(redelivered.Proposals) != len(resolved.Proposals) {
		t.Fatalf("resolution source redelivery did not converge: state=%+v err=%v", redelivered, err)
	}
	preview, err := core.PreviewApproval(resolved.Id, "reviewer@example.com", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approved, err := core.ConfirmApproval(preview, at.Add(4*time.Second))
	if err != nil || approved.ResolutionApprovalHash == nil {
		t.Fatalf("resolution approval failed: %v", err)
	}
	leased, err := core.AcquireLease(ctx, approved.Id, time.Minute, at.Add(5*time.Second))
	if err != nil || leased.Status != contractsv1.ChangeCaseStateStatusLeased {
		t.Fatalf("lease failed: %v", err)
	}
	if _, err := core.AcquireLease(ctx, approved.Id, time.Minute, at.Add(6*time.Second)); err == nil {
		t.Fatal("second active lease was accepted")
	}
	completed, err := core.Apply(ctx, approved.Id, at.Add(7*time.Second))
	if err != nil || completed.Status != contractsv1.ChangeCaseStateStatusCompleted || completed.ApplyEvidence == nil || completed.ReadbackEvidence == nil {
		t.Fatalf("apply/readback did not complete: state=%+v err=%v", completed, err)
	}
	if mutation.applies != 1 || mutation.readbacks != 1 || !reflect.DeepEqual(mutation.change, map[string]any{"recommendation": "resolved"}) {
		t.Fatalf("mutation adapter did not receive the exact approved change: %+v", mutation)
	}
}

func TestChangeCaseReconcilesAfterProposalAppendFailureWindow(t *testing.T) {
	sources := workflow.NewMemoryLedger()
	source := completedChangeSource(t, sources, "campaign-a", map[string]any{"recommendation": "A"})
	resource := resourceFixture(t, 1, map[string]any{"title": "baseline"})
	ledger := &failNthAppendLedger{Ledger: workflow.NewMemoryLedger(), failAt: 2}
	core := workflow.NewChangeCaseCore(ledger, sources, outputCatalog(), workflow.ChangeCaseCatalog{
		Mergers:   map[contractsv1.Identifier]workflow.ChangeMergeAdapter{"document": conflictMerge{}},
		Resources: map[contractsv1.Identifier]workflow.ResourceAuthority{"document": &testResourceAuthority{current: resource}},
	})
	at := time.Now().UTC()
	if _, err := core.SubmitProposal(context.Background(), resource, source, at); err == nil {
		t.Fatal("injected merge append failure was not observed")
	}
	state, err := core.SubmitProposal(context.Background(), resource, source, at.Add(time.Second))
	if err != nil || state.Status != contractsv1.ChangeCaseStateStatusReady || len(state.Proposals) != 1 {
		t.Fatalf("redelivery did not converge from the canonical proposal: state=%+v err=%v", state, err)
	}
}

func TestChangeCaseRecordsStaleResourceGenerationWithoutMutation(t *testing.T) {
	sources := workflow.NewMemoryLedger()
	source := completedChangeSource(t, sources, "campaign-a", map[string]any{"recommendation": "A"})
	requested := resourceFixture(t, 1, map[string]any{"title": "baseline"})
	current := requested
	current.Generation = 2
	current.BaselineHash = hashOf(t, map[string]any{"title": "new baseline"})
	mutation := &testMutationAdapter{}
	core := workflow.NewChangeCaseCore(workflow.NewMemoryLedger(), sources, outputCatalog(), workflow.ChangeCaseCatalog{
		Mergers:   map[contractsv1.Identifier]workflow.ChangeMergeAdapter{"document": conflictMerge{}},
		Resources: map[contractsv1.Identifier]workflow.ResourceAuthority{"document": &testResourceAuthority{current: current}},
		Mutations: map[contractsv1.Identifier]workflow.MutationAdapter{"document": mutation},
	})
	state, err := core.SubmitProposal(context.Background(), requested, source, time.Now().UTC())
	if err != nil || state.Status != contractsv1.ChangeCaseStateStatusBlocked || state.BlockerCode == nil || *state.BlockerCode != contractsv1.ChangeCaseStateBlockerCodeResourceGenerationAdvanced {
		t.Fatalf("stale resource was not a typed blocker: state=%+v err=%v", state, err)
	}
	if mutation.applies != 0 {
		t.Fatal("stale resource reached mutation")
	}
}

func TestChangeCaseResumesReadbackWithoutRepeatingAppliedEffect(t *testing.T) {
	sources := workflow.NewMemoryLedger()
	source := completedChangeSource(t, sources, "campaign-a", map[string]any{"recommendation": "A"})
	resource := resourceFixture(t, 1, map[string]any{"title": "baseline"})
	ledger := &failNthAppendLedger{Ledger: workflow.NewMemoryLedger(), failAt: 6}
	mutation := &testMutationAdapter{}
	at := time.Now().UTC()
	now := at
	core := workflow.NewChangeCaseCore(ledger, sources, outputCatalog(), workflow.ChangeCaseCatalog{
		Mergers:        map[contractsv1.Identifier]workflow.ChangeMergeAdapter{"document": conflictMerge{}},
		Resources:      map[contractsv1.Identifier]workflow.ResourceAuthority{"document": &testResourceAuthority{current: resource}},
		Mutations:      map[contractsv1.Identifier]workflow.MutationAdapter{"document": mutation},
		ApprovalActors: map[contractsv1.Identifier][]string{"document": {"reviewer@example.com"}},
		Clock:          func() time.Time { return now },
	})
	ready, err := core.SubmitProposal(context.Background(), resource, source, at)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := core.PreviewApproval(ready.Id, "reviewer@example.com", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approved, err := core.ConfirmApproval(preview, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	leased, err := core.AcquireLease(context.Background(), approved.Id, time.Minute, at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = core.Apply(context.Background(), approved.Id, at.Add(3*time.Second)); err == nil {
		t.Fatal("injected readback receipt failure was not observed")
	}
	now = leased.Lease.ExpiresAt
	if _, err = core.AcquireLease(context.Background(), approved.Id, time.Minute, now); err == nil {
		t.Fatal("an applied change case acquired another mutation lease")
	}
	completed, err := core.Apply(context.Background(), approved.Id, at.Add(4*time.Second))
	if err != nil || completed.Status != contractsv1.ChangeCaseStateStatusCompleted || mutation.applies != 1 || mutation.readbacks != 2 {
		t.Fatalf("readback recovery repeated apply or failed: state=%+v adapter=%+v err=%v", completed, mutation, err)
	}
}

func TestChangeCaseDoesNotReplaceLeaseAfterUncertainApply(t *testing.T) {
	sources := workflow.NewMemoryLedger()
	source := completedChangeSource(t, sources, "campaign-a", map[string]any{"recommendation": "A"})
	resource := resourceFixture(t, 1, map[string]any{"title": "baseline"})
	ledger := &failNthAppendLedger{Ledger: workflow.NewMemoryLedger(), failAt: 5}
	mutation := &testMutationAdapter{}
	now := time.Now().UTC()
	core := workflow.NewChangeCaseCore(ledger, sources, outputCatalog(), workflow.ChangeCaseCatalog{
		Mergers:        map[contractsv1.Identifier]workflow.ChangeMergeAdapter{"document": conflictMerge{}},
		Resources:      map[contractsv1.Identifier]workflow.ResourceAuthority{"document": &testResourceAuthority{current: resource}},
		Mutations:      map[contractsv1.Identifier]workflow.MutationAdapter{"document": mutation},
		ApprovalActors: map[contractsv1.Identifier][]string{"document": {"reviewer@example.com"}},
		Clock:          func() time.Time { return now },
	})
	ready, err := core.SubmitProposal(context.Background(), resource, source, now)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := core.PreviewApproval(ready.Id, "reviewer@example.com", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approved, err := core.ConfirmApproval(preview, now)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := core.AcquireLease(context.Background(), approved.Id, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Apply(context.Background(), approved.Id, now); err == nil || mutation.applies != 1 {
		t.Fatalf("fixture did not stop after the uncertain external apply: applies=%d err=%v", mutation.applies, err)
	}
	now = leased.Lease.ExpiresAt
	if _, err := core.AcquireLease(context.Background(), approved.Id, time.Minute, now); err == nil {
		t.Fatal("uncertain external apply obtained replacement mutation authority")
	}
	if _, err := core.Apply(context.Background(), approved.Id, now); err == nil || mutation.applies != 1 {
		t.Fatalf("expired uncertain apply repeated the external effect: applies=%d err=%v", mutation.applies, err)
	}
}

func TestChangeCaseRejectsOversizedReplayBeforeCorruptingLedger(t *testing.T) {
	sources := workflow.NewMemoryLedger()
	resource := resourceFixture(t, 1, map[string]any{"title": "baseline"})
	ledger, err := workflow.OpenFileLedger(t.TempDir() + "/changes.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	core := workflow.NewChangeCaseCore(ledger, sources, outputCatalog(), workflow.ChangeCaseCatalog{
		Mergers:   map[contractsv1.Identifier]workflow.ChangeMergeAdapter{"document": equalMerge{}},
		Resources: map[contractsv1.Identifier]workflow.ResourceAuthority{"document": &testResourceAuthority{current: resource}},
	})
	at := time.Now().UTC()
	var caseID string
	rejected := false
	for index := 0; index < 8; index++ {
		source := completedChangeSource(t, sources, fmt.Sprintf("campaign-large-%d", index), map[string]any{"recommendation": strings.Repeat(string(rune('a'+index)), 100<<10)})
		state, appendErr := core.SubmitProposal(context.Background(), resource, source, at.Add(time.Duration(index)*time.Second))
		if state.Id != "" {
			caseID = state.Id
		}
		if appendErr != nil {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Fatal("oversized Change Case replay was not rejected")
	}
	replay, err := core.Replay(caseID)
	if err != nil {
		t.Fatalf("bounded rejection corrupted the canonical ledger: %v", err)
	}
	if _, err := workflow.MaterializeChangeCase(replay); err != nil {
		t.Fatalf("bounded rejection left an unreplayable Change Case: %v", err)
	}
}

func TestChangeCaseApprovalBindsExactProposalFrontier(t *testing.T) {
	sources := workflow.NewMemoryLedger()
	first := completedChangeSource(t, sources, "campaign-a", map[string]any{"recommendation": "same"})
	second := completedChangeSource(t, sources, "campaign-b", map[string]any{"recommendation": "same"})
	resource := resourceFixture(t, 1, map[string]any{"title": "baseline"})
	core := workflow.NewChangeCaseCore(workflow.NewMemoryLedger(), sources, outputCatalog(), workflow.ChangeCaseCatalog{
		Mergers:        map[contractsv1.Identifier]workflow.ChangeMergeAdapter{"document": equalMerge{}},
		Resources:      map[contractsv1.Identifier]workflow.ResourceAuthority{"document": &testResourceAuthority{current: resource}},
		ApprovalActors: map[contractsv1.Identifier][]string{"document": {"reviewer@example.com"}},
	})
	at := time.Now().UTC()
	ready, err := core.SubmitProposal(context.Background(), resource, first, at)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := core.PreviewApproval(ready.Id, "reviewer@example.com", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ready, err = core.SubmitProposal(context.Background(), resource, second, at.Add(time.Second))
	if err != nil || ready.Status != contractsv1.ChangeCaseStateStatusReady || ready.MergedChangeHash == nil || *ready.MergedChangeHash != preview.ChangeHash {
		t.Fatalf("fixture did not preserve the same merged change: state=%+v err=%v", ready, err)
	}
	if _, err := core.ConfirmApproval(preview, at.Add(2*time.Second)); err == nil {
		t.Fatal("approval preview survived an unseen canonical proposal frontier")
	}
}

func TestChangeCaseReplacementLineageCannotSuppressAnotherCampaign(t *testing.T) {
	sources := workflow.NewMemoryLedger()
	first := completedChangeSource(t, sources, "campaign-a", map[string]any{"recommendation": "A"})
	second := completedChangeSource(t, sources, "campaign-b", map[string]any{"recommendation": "B"})
	third := completedChangeSource(t, sources, "campaign-c", map[string]any{"recommendation": "B"})
	resource := resourceFixture(t, 1, map[string]any{"title": "baseline"})
	core := workflow.NewChangeCaseCore(workflow.NewMemoryLedger(), sources, outputCatalog(), workflow.ChangeCaseCatalog{
		Mergers:   map[contractsv1.Identifier]workflow.ChangeMergeAdapter{"document": equalMerge{}},
		Resources: map[contractsv1.Identifier]workflow.ResourceAuthority{"document": &testResourceAuthority{current: resource}},
	})
	at := time.Now().UTC()
	state, err := core.SubmitProposal(context.Background(), resource, first, at)
	if err != nil {
		t.Fatal(err)
	}
	state, err = core.SubmitProposal(context.Background(), resource, second, at.Add(time.Second))
	if err != nil || state.Status != contractsv1.ChangeCaseStateStatusConflicted {
		t.Fatalf("fixture did not conflict: %+v %v", state, err)
	}
	third.Replacement = &contractsv1.ProposalReplacement{ProposalId: state.Proposals[0].Id, Reason: contractsv1.ProposalReplacementReasonResolver}
	state, err = core.SubmitProposal(context.Background(), resource, third, at.Add(2*time.Second))
	if err != nil || state.Status != contractsv1.ChangeCaseStateStatusConflicted {
		t.Fatalf("caller-controlled replacement bypassed conflict resolution: state=%+v err=%v", state, err)
	}
}

func TestChangeCaseUsesTrustedClockForApprovalAndLeaseExpiry(t *testing.T) {
	sources := workflow.NewMemoryLedger()
	source := completedChangeSource(t, sources, "campaign-a", map[string]any{"recommendation": "A"})
	resource := resourceFixture(t, 1, map[string]any{"title": "baseline"})
	now := time.Now().UTC()
	mutation := &testMutationAdapter{}
	core := workflow.NewChangeCaseCore(workflow.NewMemoryLedger(), sources, outputCatalog(), workflow.ChangeCaseCatalog{
		Mergers:        map[contractsv1.Identifier]workflow.ChangeMergeAdapter{"document": conflictMerge{}},
		Resources:      map[contractsv1.Identifier]workflow.ResourceAuthority{"document": &testResourceAuthority{current: resource}},
		Mutations:      map[contractsv1.Identifier]workflow.MutationAdapter{"document": mutation},
		ApprovalActors: map[contractsv1.Identifier][]string{"document": {"reviewer@example.com"}},
		Clock:          func() time.Time { return now },
	})
	ready, err := core.SubmitProposal(context.Background(), resource, source, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := core.PreviewApproval(ready.Id, "reviewer@example.com", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := core.ConfirmApproval(preview, preview.ExpiresAt.Add(-time.Second)); err == nil {
		t.Fatal("caller-supplied timestamp revived an expired approval")
	}

	now = preview.ExpiresAt.Add(-time.Second)
	approved, err := core.ConfirmApproval(preview, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	leased, err := core.AcquireLease(context.Background(), approved.Id, time.Minute, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	now = leased.Lease.ExpiresAt
	if _, err := core.Apply(context.Background(), approved.Id, leased.Lease.AcquiredAt); err == nil {
		t.Fatal("caller-supplied timestamp revived an expired mutation lease")
	}
	if mutation.applies != 0 {
		t.Fatal("expired mutation lease reached the mutation adapter")
	}
}

type completedSource struct{ campaign, result, artifact string }

func completedChangeSource(t *testing.T, ledger workflow.Ledger, campaignID string, content any) workflow.ChangeProposalSource {
	t.Helper()
	cutoff := time.Now().UTC().Add(-time.Hour)
	scope := contractsv1.Scope{SubjectType: "project", SubjectIds: []string{"project-a"}}
	registry, err := workflow.NewRegistry(workflow.NewCatalogProducer("project-brief", "project-brief", 1, packFixture(t, scope, cutoff)), workflow.NewIntentProducer())
	if err != nil {
		t.Fatal(err)
	}
	definition := loadExample(t)
	definition.Id = contractsv1.Identifier("workflow-" + campaignID)
	definition.Nodes = definition.Nodes[:1]
	definition.Nodes[0].OutputSlots[0].Consumers = nil
	definition.Outputs = definition.Nodes[0].OutputSlots
	definition.Completion = []string{"research completed"}
	job := jobFixture(scope)
	campaign := campaignFixture(scope, cutoff)
	campaign.Id = contractsv1.Identifier(campaignID)
	campaign.WorkflowPlan = []contractsv1.WorkflowRef{contractsv1.WorkflowRef(string(definition.Id) + "@1")}
	provider := &memoProvider{results: map[string]workflow.ProviderResult{}, content: content}
	admit(t, ledger, registry, workflow.RunRequest{Job: job, Campaign: campaign, Workflow: definition, NodeID: "research"})
	engine := workflow.NewEngine(registry, workflow.CapabilityCatalog{"read-evidence": contractsv1.CapabilityManifestCapabilitiesElemAuthorityRead}, outputCatalog(), provider, ledger)
	driven, err := engine.Drive(context.Background(), workflow.CampaignDriveCommand{CampaignRunRequest: workflow.CampaignRunRequest{Job: job, Campaign: campaign, Workflow: definition}, MaxTransitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if driven.CampaignReplay == nil || driven.NodeReplay == nil {
		t.Fatal("fixture did not produce canonical replays")
	}
	return workflow.ChangeProposalSource{CampaignAggregateID: driven.CampaignReplay.AggregateId, ResultAggregateID: driven.NodeReplay.AggregateId, ArtifactID: "artifact-recommendation-1"}
}

type conflictMerge struct{}

func (conflictMerge) Merge(_ context.Context, _ contractsv1.ResourceRef, proposals []contractsv1.ChangeProposal) (workflow.MergeDecision, error) {
	if len(proposals) == 1 {
		return workflow.MergeDecision{Change: proposals[0].Change}, nil
	}
	return workflow.MergeDecision{Conflicts: []contractsv1.ConflictItem{{Path: "/recommendation", ProposalIds: []contractsv1.Identifier{proposals[0].Id, proposals[1].Id}, Reason: "proposals differ"}}}, nil
}

type equalMerge struct{}

func (equalMerge) Merge(_ context.Context, _ contractsv1.ResourceRef, proposals []contractsv1.ChangeProposal) (workflow.MergeDecision, error) {
	for index := 1; index < len(proposals); index++ {
		if !reflect.DeepEqual(proposals[0].Change, proposals[index].Change) {
			return workflow.MergeDecision{Conflicts: []contractsv1.ConflictItem{{Path: "/recommendation", ProposalIds: []contractsv1.Identifier{proposals[0].Id, proposals[index].Id}, Reason: "proposals differ"}}}, nil
		}
	}
	return workflow.MergeDecision{Change: proposals[0].Change}, nil
}

type testResourceAuthority struct{ current contractsv1.ResourceRef }

func (a *testResourceAuthority) Current(context.Context, string) (contractsv1.ResourceRef, error) {
	return a.current, nil
}

type testMutationAdapter struct {
	applies, readbacks int
	change             any
	observed           contractsv1.SHA256
}

func (a *testMutationAdapter) Apply(_ context.Context, _ contractsv1.MutationLease, change any) (contractsv1.SHA256, error) {
	a.applies++
	a.change = change
	a.observed = hashOfNoTest(change)
	return a.observed, nil
}
func (a *testMutationAdapter) Readback(context.Context, contractsv1.MutationLease) (contractsv1.SHA256, error) {
	a.readbacks++
	return a.observed, nil
}

type failNthAppendLedger struct {
	workflow.Ledger
	calls, failAt int
}

func (l *failNthAppendLedger) Append(receipt contractsv1.Receipt) error {
	l.calls++
	if l.calls == l.failAt {
		return errors.New("injected append failure")
	}
	return l.Ledger.Append(receipt)
}

func resourceFixture(t *testing.T, generation int, baseline any) contractsv1.ResourceRef {
	t.Helper()
	return contractsv1.ResourceRef{Kind: "resource_ref", SchemaVersion: 2, ResourceType: "document", ResourceId: "document-1", Generation: generation, BaselineRevision: "revision-1", BaselineHash: hashOf(t, baseline)}
}
func hashOf(t *testing.T, value any) contractsv1.SHA256 {
	t.Helper()
	hash, err := workflow.Digest(value)
	if err != nil {
		t.Fatal(err)
	}
	return contractsv1.SHA256(hash)
}
func hashOfNoTest(value any) contractsv1.SHA256 {
	hash, _ := workflow.Digest(value)
	return contractsv1.SHA256(hash)
}
