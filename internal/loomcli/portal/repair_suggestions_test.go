package portal

import (
	"context"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/loomcli/actions"
)

func TestAttentionRepairSuggestionClassifiesIndexFailure(t *testing.T) {
	item := applyAttentionRepairSuggestion(AttentionItem{
		ID:         "indexes.failed",
		Domain:     "Search",
		Severity:   "error",
		Title:      "Indexing failures",
		Reason:     "3 failed indexes",
		Suggestion: "open Jobs",
		TargetKind: "screen",
		TargetRef:  ScreenJobs,
	})

	if item.FailureClass != FailureClassIndexFailure {
		t.Fatalf("FailureClass = %q, want %q", item.FailureClass, FailureClassIndexFailure)
	}
	if item.RepairActionID != "index.retry_failed" {
		t.Fatalf("RepairActionID = %q, want index.retry_failed", item.RepairActionID)
	}
	if !strings.Contains(item.Suggestion, "retry failed index") {
		t.Fatalf("suggestion should explain the next step, got %q", item.Suggestion)
	}
}

func TestAttentionRepairSuggestionUsesBoxWatchPolicyAction(t *testing.T) {
	item := applyAttentionRepairSuggestion(AttentionItem{
		ID:       "box.contract.invalid",
		Domain:   "Box",
		Severity: "warning",
		Title:    "Box contract invalid",
		Reason:   "Box contract diagnostic policy is invalid",
	})

	if item.FailureClass != FailureClassBoxContractInvalid {
		t.Fatalf("FailureClass = %q, want %q", item.FailureClass, FailureClassBoxContractInvalid)
	}
	if item.RepairActionID != "box.watch_policy.apply" {
		t.Fatalf("RepairActionID = %q, want box.watch_policy.apply", item.RepairActionID)
	}
}

func TestLegacyStorageExportAttentionRoutesToTypedInspectionOnly(t *testing.T) {
	item := applyAttentionRepairSuggestion(AttentionItem{
		ID:       "storage.export.stale",
		Domain:   "Storage",
		Severity: "warning",
		Title:    "Storage export stale",
		Reason:   "legacy storage export refresh failed",
	})

	if item.FailureClass != FailureClassStorageExport {
		t.Fatalf("FailureClass = %q, want %q", item.FailureClass, FailureClassStorageExport)
	}
	if item.TargetRef != ScreenStorage || item.InspectActionID != "storage.open" {
		t.Fatalf("typed inspection route = target %q inspect %q", item.TargetRef, item.InspectActionID)
	}
	if item.RepairActionID != "" {
		t.Fatalf("legacy export attention exposed repair action %q", item.RepairActionID)
	}
	guidance := strings.ToLower(item.Suggestion)
	for _, want := range []string{"filesystem", "catalog", "inspect"} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("typed inspection guidance missing %q: %q", want, item.Suggestion)
		}
	}
	for _, obsolete := range []string{"request storage refresh", "rebuild"} {
		if strings.Contains(guidance, obsolete) {
			t.Fatalf("typed inspection guidance retained obsolete operation %q: %q", obsolete, item.Suggestion)
		}
	}

	finding := doctorFindingFromAttention(item, time.Now().UTC())
	if finding.InspectActionID != "storage.open" || finding.SafeRepairActionID != "" || finding.DangerousRepairActionID != "" {
		t.Fatalf("Doctor action routing = inspect %q safe %q dangerous %q", finding.InspectActionID, finding.SafeRepairActionID, finding.DangerousRepairActionID)
	}
	for _, action := range doctorRelatedActionsForFinding(finding, true) {
		if action.ID != "storage.open" {
			t.Fatalf("Doctor exposed non-inspection action %#v", action)
		}
	}
}

func TestHomeAttentionRendersTerseRepairSuggestion(t *testing.T) {
	output := RenderScreen(testMode(), fakeSnapshot(), actions.DefaultRegistry(), ScreenHome)
	for _, want := range []string{"Attention", "Open Doctor for full triage", "Indexing failures"} {
		if !strings.Contains(output, want) {
			t.Fatalf("home attention output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "fix:") || strings.Contains(output, "failure_class=") || strings.Contains(output, "repair_action_id=") {
		t.Fatalf("home attention should not expose inline repair or raw suggestion metadata by default:\n%s", output)
	}
}

func TestAttentionInspectActionShowsSuggestedRepairDetail(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenHome, fakeSnapshot())
	items := ScreenRecordItems(state)
	var selected SelectableItem
	for _, item := range items {
		if item.RecordRef == "indexes.failed" {
			selected = item
			break
		}
	}
	if selected.PrimaryAction == nil {
		t.Fatalf("expected indexes.failed attention item with inspect action, got %#v", items)
	}

	result, err := ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", *selected.PrimaryAction, true)
	if err != nil {
		t.Fatalf("execute attention inspect returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded {
		t.Fatalf("attention inspect did not succeed: %#v", result)
	}
	if got := resultFieldValue(result, "Failure Class"); got != FailureClassIndexFailure {
		t.Fatalf("Failure Class = %q, want %q; fields=%#v", got, FailureClassIndexFailure, result.Fields)
	}
	if got := resultFieldValue(result, "Repair Action Id"); got != "index.retry_failed" {
		t.Fatalf("Repair Action Id = %q, want index.retry_failed; fields=%#v", got, result.Fields)
	}
	if got := resultFieldValue(result, "Dangerous Repairs"); got != "not run automatically" {
		t.Fatalf("Dangerous Repairs = %q, want not run automatically; fields=%#v", got, result.Fields)
	}
}

func TestHealthyHomeDoesNotRenderRepairSuggestions(t *testing.T) {
	output := RenderScreen(testMode(), Snapshot{}, actions.DefaultRegistry(), ScreenHome)
	if strings.Contains(output, "fix:") {
		t.Fatalf("healthy empty Home should not show repair suggestions:\n%s", output)
	}
}
