package workflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func DefaultAgentRoleCatalog() contractsv1.AgentRoleCatalog {
	return contractsv1.AgentRoleCatalog{
		Kind: contractsv1.AgentRoleCatalogKindAgentRoleCatalog, SchemaVersion: 1,
		Roles: []contractsv1.AgentRole{
			{Id: "bounded-execution-agent", Title: "Bounded execution agent", Purpose: "Executes one admitted Node with exact Context and capabilities.", Responsibilities: []string{"Run admitted work", "Return typed artifacts"}, EvidenceReceiptTypes: []contractsv1.AgentRoleReceiptType{"invocation", "provider_execution", "result", "node_completed"}},
			{Id: "change-verification-agent", Title: "Change verification agent", Purpose: "Verifies proposed effects against canonical readback evidence.", Responsibilities: []string{"Resolve change evidence", "Verify applied state"}, EvidenceReceiptTypes: []contractsv1.AgentRoleReceiptType{"change_proposed", "change_merged", "mutation_applied", "mutation_readback"}},
			{Id: "context-preparation-agent", Title: "Context preparation agent", Purpose: "Refreshes and binds exact Context Packs before execution.", Responsibilities: []string{"Refresh evidence", "Bind Context Packs"}, EvidenceReceiptTypes: []contractsv1.AgentRoleReceiptType{"pack_edition", "context_available", "context_bound", "needs_context"}},
		},
	}
}

func BuildEvidenceWindowReport(catalog contractsv1.AgentRoleCatalog, replays []contractsv1.ReplayBundle, startedAt, endedAt time.Time) (contractsv1.EvidenceWindowReport, error) {
	startedAt, endedAt = startedAt.UTC(), endedAt.UTC()
	if endedAt.Before(startedAt) {
		return contractsv1.EvidenceWindowReport{}, errors.New("evidence window ends before it starts")
	}
	if err := contract.ValidateDefinition("AgentRoleCatalog", catalog); err != nil {
		return contractsv1.EvidenceWindowReport{}, err
	}
	roles := append([]contractsv1.AgentRole(nil), catalog.Roles...)
	sort.Slice(roles, func(i, j int) bool { return roles[i].Id < roles[j].Id })
	available := make([]contractsv1.Identifier, 0, len(roles))
	roleReceipts := make(map[contractsv1.ReceiptReceiptType][]contractsv1.Identifier)
	seenRoles := make(map[contractsv1.Identifier]bool, len(roles))
	for _, role := range roles {
		if seenRoles[role.Id] {
			return contractsv1.EvidenceWindowReport{}, fmt.Errorf("Agent role %q is duplicated", role.Id)
		}
		seenRoles[role.Id] = true
		available = append(available, role.Id)
		for _, receiptType := range role.EvidenceReceiptTypes {
			roleReceipts[contractsv1.ReceiptReceiptType(receiptType)] = append(roleReceipts[contractsv1.ReceiptReceiptType(receiptType)], role.Id)
		}
	}
	invoked := map[contractsv1.Identifier]bool{}
	counts := contractsv1.EvidenceCounts{}
	evidence := []contractsv1.EvidenceReference{}
	for _, replay := range replays {
		if err := VerifyReplay(replay); err != nil {
			return contractsv1.EvidenceWindowReport{}, fmt.Errorf("verify Replay %s: %w", replay.AggregateId, err)
		}
		matched := false
		for _, receipt := range replay.Receipts {
			if receipt.OccurredAt.Before(startedAt) || receipt.OccurredAt.After(endedAt) {
				continue
			}
			matched = true
			counts.Receipts++
			for _, roleID := range roleReceipts[receipt.ReceiptType] {
				invoked[roleID] = true
			}
			switch receipt.ReceiptType {
			case contractsv1.ReceiptReceiptTypeContextAvailable, contractsv1.ReceiptReceiptTypePackEdition:
				counts.ContextRefreshes++
			case contractsv1.ReceiptReceiptTypeInvocation:
				counts.AgentInvocations++
			case contractsv1.ReceiptReceiptTypeApproval:
				counts.Approvals++
			case contractsv1.ReceiptReceiptTypeMutationApplied:
				counts.Effects++
			case contractsv1.ReceiptReceiptTypeMutationReadback:
				counts.Readbacks++
			case contractsv1.ReceiptReceiptTypeTerminal:
				counts.Outcomes++
			}
			occurredAt := receipt.OccurredAt
			evidence = append(evidence, contractsv1.EvidenceReference{Kind: contractsv1.EvidenceReferenceKindReceipt, Id: fmt.Sprintf("%s:%d", receipt.AggregateId, receipt.AggregateVersion), Sha256: receipt.ReceiptHash, OccurredAt: &occurredAt})
		}
		if matched {
			counts.Replays++
			evidence = append(evidence, contractsv1.EvidenceReference{Kind: contractsv1.EvidenceReferenceKindReplay, Id: replay.AggregateId, Sha256: replay.BundleHash})
		}
	}
	invokedIDs := make([]contractsv1.Identifier, 0, len(invoked))
	for _, roleID := range available {
		if invoked[roleID] {
			invokedIDs = append(invokedIDs, roleID)
		}
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].Kind == evidence[j].Kind {
			return evidence[i].Id < evidence[j].Id
		}
		return evidence[i].Kind < evidence[j].Kind
	})
	report := contractsv1.EvidenceWindowReport{
		Kind: contractsv1.EvidenceWindowReportKindEvidenceWindowReport, SchemaVersion: 1,
		Window:           contractsv1.EvidenceWindow{StartedAt: startedAt, EndedAt: endedAt, DurationSeconds: int(endedAt.Sub(startedAt).Seconds())},
		AvailableRoleIds: available, InvokedRoleIds: invokedIDs, Counts: counts, Evidence: evidence,
	}
	if err := contract.ValidateDefinition("EvidenceWindowReport", report); err != nil {
		return contractsv1.EvidenceWindowReport{}, err
	}
	return report, nil
}

func RenderEvidenceWindowMarkdown(report contractsv1.EvidenceWindowReport) (string, error) {
	if err := contract.ValidateDefinition("EvidenceWindowReport", report); err != nil {
		return "", err
	}
	join := func(ids []contractsv1.Identifier) string {
		values := make([]string, len(ids))
		for index, id := range ids {
			values[index] = string(id)
		}
		if len(values) == 0 {
			return "None"
		}
		return strings.Join(values, ", ")
	}
	var output strings.Builder
	fmt.Fprintf(&output, "# Evidence window report\n\n- Window: %s to %s (%d seconds)\n- Available roles: %s\n- Invoked roles: %s\n\n## Activity\n\n| Metric | Count |\n|---|---:|\n", report.Window.StartedAt.Format(time.RFC3339), report.Window.EndedAt.Format(time.RFC3339), report.Window.DurationSeconds, join(report.AvailableRoleIds), join(report.InvokedRoleIds))
	for _, row := range []struct {
		name  string
		count int
	}{{"Context refreshes", report.Counts.ContextRefreshes}, {"Agent invocations", report.Counts.AgentInvocations}, {"Approvals", report.Counts.Approvals}, {"Effects", report.Counts.Effects}, {"Readbacks", report.Counts.Readbacks}, {"Outcomes", report.Counts.Outcomes}, {"Receipts", report.Counts.Receipts}, {"Replays", report.Counts.Replays}} {
		fmt.Fprintf(&output, "| %s | %d |\n", row.name, row.count)
	}
	output.WriteString("\n## Canonical evidence\n\n")
	for _, ref := range report.Evidence {
		fmt.Fprintf(&output, "- %s `%s` — `%s`\n", ref.Kind, ref.Id, ref.Sha256)
	}
	return output.String(), nil
}
