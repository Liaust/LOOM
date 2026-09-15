package portal

import (
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/search"
)

func TestBuildDoctorDataEmptyStateIsHealthy(t *testing.T) {
	data := BuildDoctorData(Snapshot{})
	if data.Status != DoctorStatusOK {
		t.Fatalf("status = %q, want %q", data.Status, DoctorStatusOK)
	}
	if data.Totals.Total != 0 {
		t.Fatalf("total findings = %d, want 0", data.Totals.Total)
	}
	if len(data.Areas) == 0 {
		t.Fatal("doctor should define stable areas")
	}
	if data.Summary == "" || !strings.Contains(data.Summary, "No active issues") {
		t.Fatalf("unexpected summary: %q", data.Summary)
	}
}

func TestDoctorScreenIsInDailyGroupAfterHome(t *testing.T) {
	groups := ScreenGroups()
	if len(groups) == 0 || groups[0].ID != "daily" {
		t.Fatalf("first group = %#v, want daily", groups)
	}
	if len(groups[0].Screens) < 2 {
		t.Fatalf("daily group too short: %#v", groups[0].Screens)
	}
	if groups[0].Screens[0].ID != ScreenHome || groups[0].Screens[1].ID != ScreenDoctor {
		t.Fatalf("daily group starts with %#v, want home then doctor", groups[0].Screens[:2])
	}
}

func TestDoctorNormalizeAndSearchAliases(t *testing.T) {
	for _, alias := range []string{"doctor", "repair", "attention", "health", "problems", "fix"} {
		if got := NormalizeScreen(alias); got != ScreenDoctor {
			t.Fatalf("NormalizeScreen(%q) = %q, want %q", alias, got, ScreenDoctor)
		}
		results := SearchPortalNavigation(actions.DefaultRegistry(), alias, 20)
		if !searchResultsTargetScreen(results, ScreenDoctor) {
			t.Fatalf("navigation search for %q did not find Doctor: %#v", alias, results)
		}
	}
}

func TestDoctorScopedSearchIsDiscoverable(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenHome, Snapshot{})
	results := SearchPortal(actions.DefaultRegistry(), state, "#doctor", 20)
	if !searchResultsContainTitle(results, "#doctor") {
		t.Fatalf("#doctor scope missing: %#v", results)
	}
	parsed := ParsePortalSearchInput("#repair")
	if parsed.Scope != "doctor" {
		t.Fatalf("#repair scope = %q, want doctor", parsed.Scope)
	}
}

func TestSupportBundleActionVisibleOnDoctorWithoutNewScreen(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenDoctor, Snapshot{})
	action, ok := findPortalAction(ScreenActions(state), "support.bundle.create_hint")
	if !ok {
		t.Fatalf("missing support bundle action: %#v", ScreenActions(state))
	}
	if action.Risk != ActionRiskInspect || action.Executor.Kind != PortalExecutorRecordInspect {
		t.Fatalf("unexpected support bundle action classification: %#v", action)
	}
	raw := strings.Join(action.RawCommand, " ")
	if !strings.Contains(raw, "loom support bundle create") {
		t.Fatalf("support bundle action command = %q", raw)
	}
	if strings.Contains(raw, "--include-logs") || strings.Contains(raw, "--include-live") {
		t.Fatalf("support bundle hint bypassed default privacy gates: %q", raw)
	}
	for _, screen := range Screens() {
		combined := strings.ToLower(screen.ID + " " + screen.Title)
		if strings.Contains(combined, "support") || strings.Contains(combined, "bundle") {
			t.Fatalf("support bundle should not have a top-level screen: %#v", screen)
		}
	}
}

func TestSupportBundleHomeHintOnlyWhenAttentionExists(t *testing.T) {
	healthy := ScreenStateFromSnapshot(ScreenHome, Snapshot{})
	if _, ok := findPortalAction(ScreenActions(healthy), "support.bundle.create_hint"); ok {
		t.Fatal("healthy Home should not expose support bundle as a top action")
	}

	withAttention := ScreenStateFromSnapshot(ScreenHome, Snapshot{
		JobStatus: jobs.QueueSummary{FailedCount: 1},
	})
	if _, ok := findPortalAction(ScreenActions(withAttention), "support.bundle.create_hint"); !ok {
		t.Fatalf("Home with attention should expose support bundle handoff hint: %#v", ScreenActions(withAttention))
	}
}

func TestSupportBundleSearchAliases(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenHome, Snapshot{})
	for _, query := range []string{"support", "bundle", "diagnostic bundle", "handoff"} {
		results := SearchPortal(actions.DefaultRegistry(), state, query, 20)
		if !searchResultsContainAction(results, "support.bundle.create_hint") {
			t.Fatalf("search for %q did not find support bundle hint: %#v", query, results)
		}
	}
}

func TestRenderDoctorEmptyState(t *testing.T) {
	output := RenderScreenWithState(RenderInput{
		Mode:   testMode(),
		Screen: ScreenDoctor,
		State:  ScreenStateFromSnapshot(ScreenDoctor, Snapshot{}),
		Width:  100,
		Height: 40,
	})
	for _, want := range []string{"Doctor", "status=ok", "Current Issues", "No active issues", "System Areas"} {
		if !strings.Contains(output, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderDoctorGroupsFindingsAndSelectedDetail(t *testing.T) {
	data := BuildDoctorDataFromFindings(time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC), []DoctorFinding{
		{
			ID:             "doctor.jobs.critical",
			Area:           DoctorAreaJobs,
			Severity:       DoctorSeverityCritical,
			Title:          "Indexing failures",
			Impact:         "Search is incomplete.",
			SafeNextAction: "Retry failed index work",
			TargetKind:     DoctorTargetKindScreen,
			TargetRef:      ScreenJobs,
		},
		{
			ID:             "doctor.storage.warning",
			Area:           DoctorAreaStorage,
			Severity:       DoctorSeverityWarning,
			Title:          "Storage export stale",
			SafeNextAction: "Refresh storage export",
			TargetKind:     DoctorTargetKindScreen,
			TargetRef:      ScreenStorage,
		},
	}, nil)
	state := ScreenStateFromSnapshot(ScreenDoctor, Snapshot{})
	state.Data.Doctor = data
	state.SelectedIndex = 0

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenDoctor, State: state, Width: 120, Height: 50})
	for _, want := range []string{"Critical", "Warnings", "Selected Finding", "issue: Indexing failures", "impact: Search is incomplete.", "next: Retry failed index work"} {
		if !strings.Contains(output, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderDoctorShowsPartialLoadErrors(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenDoctor, Snapshot{
		PartialErrors: []SnapshotError{{Source: "notes", Message: "notes endpoint unavailable"}},
	})

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenDoctor, State: state, Width: 120, Height: 50})
	for _, want := range []string{"partial loads=warning 1", "Partial Loads", "notes: notes endpoint unavailable"} {
		if !strings.Contains(output, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderDoctorShowsSafeActionAvailability(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenDoctor, Snapshot{
		IndexFailures: []search.IndexStatus{{
			IndexStatusID:    "index_status_test",
			Status:           "failed",
			LastErrorMessage: "extractor failed",
		}},
	})

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenDoctor, State: state, Width: 120, Height: 50})
	for _, want := range []string{"actions:", "Jobs", "Retry Failed Index Work", "safe_run", "confirmation"} {
		if !strings.Contains(output, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderDoctorHidesDangerousActionsByDefault(t *testing.T) {
	data := BuildDoctorDataFromFindings(time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC), []DoctorFinding{{
		ID:                      "doctor.storage.danger",
		Area:                    DoctorAreaStorage,
		Severity:                DoctorSeverityWarning,
		Title:                   "Dangerous repair exists",
		SafeNextAction:          "Inspect storage before repair",
		DangerousRepairActionID: "storage.archive",
	}}, nil)
	state := ScreenStateFromSnapshot(ScreenDoctor, Snapshot{})
	state.Data.Doctor = data

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenDoctor, State: state, Width: 120, Height: 50})
	if strings.Contains(output, "storage.archive") || strings.Contains(output, "dangerous_repair") {
		t.Fatalf("doctor default output exposed dangerous action detail:\n%s", output)
	}

	rawState := state.ToggleRawDetails()
	rawOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenDoctor, State: rawState, Width: 120, Height: 50})
	if !strings.Contains(rawOutput, "dangerous_repair=storage.archive") {
		t.Fatalf("doctor raw output should expose source repair metadata:\n%s", rawOutput)
	}
}

func TestDoctorBuildsFindingsFromHomeAttention(t *testing.T) {
	captured := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	snapshot := Snapshot{
		CapturedAt: captured,
		JobStatus:  jobs.QueueSummary{FailedCount: 2},
		PartialErrors: []SnapshotError{
			{Source: "workers", Message: "worker endpoint unavailable"},
		},
	}

	data := BuildDoctorData(snapshot)
	if data.Status != DoctorStatusCritical {
		t.Fatalf("status = %q, want critical; findings=%#v", data.Status, data.Findings)
	}
	if data.Totals.Critical == 0 || data.Totals.Warning == 0 {
		t.Fatalf("totals should include critical job and warning partial findings: %#v", data.Totals)
	}
	if !doctorFindingsContainArea(data.Findings, DoctorAreaJobs) {
		t.Fatalf("missing jobs/indexing finding: %#v", data.Findings)
	}
	if !doctorFindingsContainArea(data.Findings, DoctorAreaPortal) {
		t.Fatalf("missing portal partial-load finding: %#v", data.Findings)
	}
	if len(data.PartialErrors) != 1 || data.PartialErrors[0].Source != "workers" {
		t.Fatalf("partial errors not preserved: %#v", data.PartialErrors)
	}
}

func TestDoctorFindingIDsAreStable(t *testing.T) {
	snapshot := Snapshot{CapturedAt: time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC), JobStatus: jobs.QueueSummary{FailedCount: 1}}
	first := BuildDoctorData(snapshot)
	second := BuildDoctorData(snapshot)
	if len(first.Findings) == 0 || len(second.Findings) == 0 {
		t.Fatalf("expected findings: first=%#v second=%#v", first.Findings, second.Findings)
	}
	if first.Findings[0].ID != second.Findings[0].ID {
		t.Fatalf("finding ID changed: %q vs %q", first.Findings[0].ID, second.Findings[0].ID)
	}
}

func TestDoctorFindingsAttachSafeRepairActions(t *testing.T) {
	snapshot := Snapshot{
		CapturedAt: time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC),
		IndexFailures: []search.IndexStatus{{
			IndexStatusID:    "index_status_test",
			Status:           "failed",
			LastErrorMessage: "extractor failed",
		}},
	}

	data := BuildDoctorData(snapshot)
	finding := doctorFindingBySource(data.Findings, "indexes.failed")
	if finding == nil {
		t.Fatalf("missing indexes.failed doctor finding: %#v", data.Findings)
	}
	if finding.InspectActionID != "jobs_search.open" {
		t.Fatalf("InspectActionID = %q, want jobs_search.open", finding.InspectActionID)
	}
	if finding.SafeRepairActionID != "index.retry_failed" {
		t.Fatalf("SafeRepairActionID = %q, want index.retry_failed", finding.SafeRepairActionID)
	}
	if finding.DangerousRepairActionID != "" {
		t.Fatalf("DangerousRepairActionID = %q, want empty", finding.DangerousRepairActionID)
	}

	items := ScreenRecordItems(ScreenStateFromSnapshot(ScreenDoctor, snapshot))
	row := doctorSelectableBySource(items, "indexes.failed")
	if row == nil {
		t.Fatalf("missing selectable indexes.failed doctor row: %#v", items)
	}
	if row.PrimaryAction == nil || row.PrimaryAction.ID == "" {
		t.Fatalf("doctor finding should expose an inspect primary action: %#v", row.PrimaryAction)
	}
	action, ok := findPortalAction(row.RelatedActions, "index.retry_failed")
	if !ok {
		t.Fatalf("missing safe repair action in related actions: %#v", row.RelatedActions)
	}
	if action.Risk != ActionRiskSafeRun {
		t.Fatalf("repair risk = %s, want %s", action.Risk, ActionRiskSafeRun)
	}
	if !action.RequiresConfirmation() {
		t.Fatalf("safe repair action should preserve confirmation policy: %#v", action)
	}
}

func TestDoctorDoesNotExposeDangerousActionsByDefault(t *testing.T) {
	finding := DoctorFinding{
		ID:                      "doctor.storage.dangerous",
		Area:                    DoctorAreaStorage,
		Title:                   "Dangerous repair exists",
		DangerousRepairActionID: "storage.archive",
	}

	defaultActions := doctorRelatedActionsForFinding(finding, false)
	if _, ok := findPortalAction(defaultActions, "storage.archive"); ok {
		t.Fatalf("dangerous action appeared in default Doctor actions: %#v", defaultActions)
	}

	if doctorActionAllowedInDefault(PortalAction{ID: "danger", Risk: ActionRiskDangerous, State: ActionAvailable}) {
		t.Fatal("dangerous actions should not be allowed in default Doctor actions")
	}
	if doctorActionAllowedInDefault(PortalAction{ID: "blocked", Risk: ActionRiskBlocked, State: ActionAvailable}) {
		t.Fatal("blocked actions should not be allowed in default Doctor actions")
	}
	if doctorActionAllowedInDefault(PortalAction{ID: "disabled", Risk: ActionRiskInspect, State: ActionDisabled}) {
		t.Fatal("disabled actions should not be allowed in default Doctor actions")
	}
}

func doctorFindingsContainArea(findings []DoctorFinding, area string) bool {
	for _, finding := range findings {
		if finding.Area == area {
			return true
		}
	}
	return false
}

func doctorFindingBySource(findings []DoctorFinding, sourceID string) *DoctorFinding {
	for idx := range findings {
		if findings[idx].SourceID == sourceID {
			return &findings[idx]
		}
	}
	return nil
}

func doctorSelectableBySource(items []SelectableItem, sourceID string) *SelectableItem {
	for idx := range items {
		if itemSourceID(items[idx]) == sourceID {
			return &items[idx]
		}
	}
	return nil
}

func itemSourceID(item SelectableItem) string {
	if item.PrimaryAction == nil {
		return ""
	}
	return item.PrimaryAction.RawDetails["source_id"]
}

func searchResultsTargetScreen(results []PortalSearchResult, screen string) bool {
	for _, result := range results {
		if result.Action.TargetRef == screen || result.Action.Executor.Target == screen {
			return true
		}
	}
	return false
}

func searchResultsContainAction(results []PortalSearchResult, actionID string) bool {
	for _, result := range results {
		if result.Action.ID == actionID {
			return true
		}
	}
	return false
}
