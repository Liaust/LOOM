package portal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/serviceregistry"
	loomstatus "loom.local/loom/internal/status"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/storageview"
	loomsync "loom.local/loom/internal/sync"
	"loom.local/loom/internal/update"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
)

func TestCollectSnapshotUsesRequiredAndOptionalData(t *testing.T) {
	client := newFakePortalClient()
	snapshot, err := CollectSnapshot(context.Background(), client, "corr_test")
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	if snapshot.Health.Status != "ok" || snapshot.Status.Status != "ok" {
		t.Fatalf("missing required snapshot data: %#v", snapshot)
	}
	if got := len(snapshot.Workers); got != 1 {
		t.Fatalf("workers = %d, want 1", got)
	}
	if got := snapshot.AttentionCount(); got == 0 {
		t.Fatal("expected attention count from fake data")
	}
}

func TestCollectSnapshotKeepsOptionalFailuresPartial(t *testing.T) {
	client := newFakePortalClient()
	client.optionalErr = errors.New("optional unavailable")
	snapshot, err := CollectSnapshot(context.Background(), client, "corr_test")
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	if len(snapshot.PartialErrors) == 0 {
		t.Fatal("expected partial errors")
	}
	if snapshot.Health.Status != "ok" || snapshot.Status.Status != "ok" {
		t.Fatal("required health and status should still be present")
	}
}

func TestCollectSnapshotTreatsUnreachableHealthAsOffline(t *testing.T) {
	client := newFakePortalClient()
	client.healthErr = errors.New("daemon unavailable")
	snapshot, err := CollectSnapshot(context.Background(), client, "corr_test")
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	if snapshot.MainAvailability.State != MainAvailabilityOffline {
		t.Fatalf("availability = %q, want offline", snapshot.MainAvailability.State)
	}
}

func TestRenderScreenIncludesOperationalSections(t *testing.T) {
	mode := testMode()
	snapshot := fakeSnapshot()
	registry := actions.DefaultRegistry()

	for _, tc := range []struct {
		screen string
		want   string
	}{
		{ScreenHome, "Attention"},
		{ScreenTimeline, "Timeline"},
		{ScreenProjects, "Project List"},
		{ScreenDatabase, "Recent Object Changes"},
		{ScreenBackground, "Worker Attention"},
		{ScreenAutomations, "Direct Events"},
		{ScreenJobs, "Indexing"},
		{ScreenNodes, "Active Watched Roots"},
		{ScreenCapabilities, "Tools By Task"},
	} {
		t.Run(tc.screen, func(t *testing.T) {
			output := RenderScreen(mode, snapshot, registry, tc.screen)
			if !strings.Contains(output, tc.want) {
				t.Fatalf("screen %s missing %q:\n%s", tc.screen, tc.want, output)
			}
			if strings.Contains(output, "\x1b[") {
				t.Fatalf("test mode should render without ANSI:\n%q", output)
			}
		})
	}
}

func TestHomeDashboardUsesCleanAttentionInbox(t *testing.T) {
	output := RenderScreen(testMode(), fakeSnapshot(), actions.DefaultRegistry(), ScreenHome)
	for _, want := range []string{"Attention", "Overview", "Recent Activity", "Navigation"} {
		if !strings.Contains(output, want) {
			t.Fatalf("home dashboard missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "Attention") > strings.Index(output, "Overview") {
		t.Fatalf("home dashboard should put attention before overview:\n%s", output)
	}
	for _, oldSection := range []string{"\nSystem\n", "\nNetwork\n", "\nStorage\n", "\nRuntime\n"} {
		if strings.Contains(output, oldSection) {
			t.Fatalf("home dashboard should use compact overview, not old section %q:\n%s", oldSection, output)
		}
	}
	for _, rawCounter := range []string{"failed jobs:", "maintenance findings:", "failed indexes:", "Surfaces"} {
		if strings.Contains(output, rawCounter) {
			t.Fatalf("home dashboard still exposes raw counter or old surface section %q:\n%s", rawCounter, output)
		}
	}
	if !strings.Contains(output, "Failed jobs") || !strings.Contains(output, "Indexing failures") {
		t.Fatalf("home dashboard should surface failures as attention items:\n%s", output)
	}
}

func TestHomeDashboardHealthySnapshotStaysQuiet(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	snapshot := Snapshot{
		CapturedAt: now,
		Health: health.Report{
			Service: "loomd",
			Status:  "ok",
			Version: "test",
			Node:    health.NodeInfo{ID: "main", Role: "main"},
			Checks: health.Checks{
				Database:   db.Result{Status: "ok", Database: "loom"},
				Storage:    health.StorageCheck{Status: "ok"},
				Migrations: health.CheckResult{Status: "ok"},
				Bootstrap:  health.CheckResult{Status: "ok"},
			},
		},
		Status:    loomstatus.Report{Status: "ok"},
		BoxStatus: box.Status{State: "ready", RootPath: "/tmp/loom-box", Profile: "main"},
	}

	output := RenderScreen(testMode(), snapshot, actions.DefaultRegistry(), ScreenHome)
	if !strings.Contains(output, "No failures.") {
		t.Fatalf("healthy home should render a quiet attention line:\n%s", output)
	}
	for _, absent := range []string{"Running\n", "Recent\n"} {
		if strings.Contains(output, absent) {
			t.Fatalf("healthy home should not render empty activity section %q:\n%s", absent, output)
		}
	}
}

func TestHomeAndTimelineDoNotTreatCancelledJobsAsActiveAttention(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	snapshot := Snapshot{
		CapturedAt: now,
		Health: health.Report{
			Service: "loomd",
			Status:  "ok",
			Version: "test",
			Node:    health.NodeInfo{ID: "main", Role: "main"},
			Checks: health.Checks{
				Database:   db.Result{Status: "ok", Database: "loom"},
				Storage:    health.StorageCheck{Status: "ok"},
				Migrations: health.CheckResult{Status: "ok"},
				Bootstrap:  health.CheckResult{Status: "ok"},
			},
		},
		Status:    loomstatus.Report{Status: "ok"},
		BoxStatus: box.Status{State: "ready", RootPath: "/tmp/loom-box", Profile: "main"},
		JobStatus: jobs.QueueSummary{
			CancelledCount: 3,
		},
	}

	output := RenderScreen(testMode(), snapshot, actions.DefaultRegistry(), ScreenHome)
	if !strings.Contains(output, "No failures.") {
		t.Fatalf("cancelled-only jobs should not create Home active attention:\n%s", output)
	}
	if strings.Contains(output, "Failed jobs") || strings.Contains(output, "jobs failed") {
		t.Fatalf("cancelled-only jobs should not be rendered as failed attention:\n%s", output)
	}
	if got := snapshot.AttentionCount(); got != 0 {
		t.Fatalf("cancelled-only jobs attention count = %d, want 0", got)
	}

	timeline := BuildTimelineData(TimelineBuildInput{Snapshot: snapshot})
	for _, event := range timeline.Events {
		if event.ID == "jobs.summary.failed" {
			t.Fatalf("cancelled-only jobs should not create failed timeline summary: %#v", event)
		}
	}
}

func TestHomeDashboardSurfacesStalePendingScheduleInvocation(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	oldest := now.Add(-2 * time.Minute)
	snapshot := Snapshot{
		CapturedAt: now,
		Health: health.Report{
			Service: "loomd",
			Status:  "ok",
			Node:    health.NodeInfo{ID: "main", Role: "main"},
			Checks: health.Checks{
				Database:   db.Result{Status: "ok", Database: "loom"},
				Storage:    health.StorageCheck{Status: "ok"},
				Migrations: health.CheckResult{Status: "ok"},
				Bootstrap:  health.CheckResult{Status: "ok"},
			},
		},
		Status:    loomstatus.Report{Status: "ok"},
		BoxStatus: box.Status{State: "ready", RootPath: "/tmp/loom-box", Profile: "main"},
		ScheduleStatus: automation.ScheduleStatus{
			PendingInvocationCount:    1,
			OldestPendingInvocationAt: &oldest,
		},
	}

	output := RenderScreen(testMode(), snapshot, actions.DefaultRegistry(), ScreenHome)
	for _, want := range []string{"Schedule dispatch delayed", "pending invocation(s)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("home dashboard missing stale pending schedule warning %q:\n%s", want, output)
		}
	}
}

func TestHomeDashboardUsesTimelinePreviewInsteadOfRunningBlock(t *testing.T) {
	snapshot := fakeSnapshot()
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	snapshot.CapturedAt = now
	snapshot.JobStatus.RunningCount = 1

	output := RenderScreen(testMode(), snapshot, actions.DefaultRegistry(), ScreenHome)
	if strings.Contains(output, "\nRunning\n") {
		t.Fatalf("home dashboard should not render a dedicated Running section:\n%s", output)
	}
	if !strings.Contains(output, "Recent Activity") || !strings.Contains(output, "Jobs running") {
		t.Fatalf("home dashboard should render fresh running work in the timeline preview:\n%s", output)
	}
}

func TestTimelineScreenAggregatesAndInspectsEvents(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenTimeline, Snapshot{})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned error: %v", result.Err)
	}
	data := result.State.Data.Timeline
	if len(data.Events) == 0 {
		t.Fatalf("expected timeline events")
	}
	if len(data.Running) == 0 || len(data.Recent) == 0 {
		t.Fatalf("timeline should classify active and chronological events: running=%d recent=%d events=%#v", len(data.Running), len(data.Recent), data.Events)
	}

	output := RenderScreenWithState(RenderInput{Mode: testMode(), HomeSnapshot: result.Snapshot, Registry: actions.DefaultRegistry(), Screen: ScreenTimeline, State: result.State, Width: 100, Height: 40})
	for _, want := range []string{"Timeline", "Active Now", "Recent Failures Or Attention", "Full History", "Today,", "Script", "Index full_text", "[jobs]"} {
		if !strings.Contains(output, want) {
			t.Fatalf("timeline output missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"\nRunning\n", "\nRecent\n", "\nFailed\n"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("timeline output should not render legacy section %q:\n%s", notWant, output)
		}
	}
	if !timelineContainsEvent(data.Events, "job.job_recent") || !timelineContainsEvent(data.Events, "index.index_status_test") {
		t.Fatalf("timeline should retain inspectable raw refs in data: %#v", data.Events)
	}

	items := ScreenSelectableItems(result.State)
	if len(items) == 0 {
		t.Fatalf("timeline should expose selectable events")
	}
	action := items[0].PrimaryAction
	if action == nil || action.Executor.Kind != PortalExecutorRecordInspect || action.TargetKind != "timeline_event" {
		t.Fatalf("timeline first item inspect action invalid: %#v", items[0])
	}
	actionResult, err := ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", *action, true)
	if err != nil {
		t.Fatalf("timeline inspect action returned error: %v", err)
	}
	if actionResult.Status != ActionLifecycleSucceeded || resultFieldValue(actionResult, "Event Id") == "" {
		t.Fatalf("timeline inspect result missing event details: %#v", actionResult)
	}

	searchResults := SearchPortal(actions.DefaultRegistry(), result.State, "timeline", 5)
	if len(searchResults) == 0 || searchResults[0].Action.TargetRef != ScreenTimeline {
		t.Fatalf("timeline should be discoverable by navigation search: %#v", searchResults)
	}
}

func timelineContainsEvent(events []TimelineEvent, id string) bool {
	for _, event := range events {
		if event.ID == id {
			return true
		}
	}
	return false
}

func TestSearchActionsFindsCorePortalActions(t *testing.T) {
	registry := actions.DefaultRegistry()
	results := SearchActions(registry, "direct event", 3)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if got := results[0].Action.ID; got != "automations.open" {
		t.Fatalf("top result = %s, want automations.open", got)
	}
}

func TestRunExitAfterRenderWritesSingleView(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Out:             &out,
		StartScreen:     ScreenBackground,
		ExitAfterRender: true,
		Registry:        actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Background Operations") {
		t.Fatalf("expected background screen, got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "\x1b[?1049") {
		t.Fatalf("exit-after-render output should not include alt-screen control sequences: %q", out.String())
	}
}

func TestRunExitAfterRenderRendersEveryScreen(t *testing.T) {
	for _, tc := range []struct {
		screen string
		want   string
	}{
		{ScreenHome, "Attention"},
		{ScreenTimeline, "Timeline"},
		{ScreenBox, "LOOM Box"},
		{ScreenStorage, "LOOM Main Storage"},
		{ScreenProjects, "Projects"},
		{ScreenDatabase, "Object Store Diagnostics"},
		{ScreenBackground, "Worker Attention"},
		{ScreenAutomations, "Direct Events"},
		{ScreenJobs, "Jobs"},
		{ScreenNodes, "Nodes And Watched Roots"},
		{ScreenCapabilities, "Capabilities And Providers"},
	} {
		t.Run(tc.screen, func(t *testing.T) {
			var out bytes.Buffer
			err := Run(context.Background(), Options{
				Mode:            testMode(),
				Client:          newFakePortalClient(),
				CorrelationID:   "corr_test",
				Out:             &out,
				StartScreen:     tc.screen,
				ExitAfterRender: true,
				Registry:        actions.DefaultRegistry(),
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("screen %s missing %q:\n%s", tc.screen, tc.want, out.String())
			}
		})
	}
}

func TestRunExitAfterRenderNonHomeUsesScreenLoader(t *testing.T) {
	client := newFakePortalClient()
	client.healthErr = errors.New("health should not be needed for background one-shot render")
	var out bytes.Buffer

	err := Run(context.Background(), Options{
		Mode:            testMode(),
		Client:          client,
		CorrelationID:   "corr_test",
		Out:             &out,
		StartScreen:     ScreenBackground,
		ExitAfterRender: true,
		Registry:        actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Background Operations") {
		t.Fatalf("expected background screen, got:\n%s", out.String())
	}
}

func TestRunExitAfterRenderProjectsUsesLiveData(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Out:             &out,
		StartScreen:     ScreenProjects,
		ExitAfterRender: true,
		Registry:        actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	output := out.String()
	for _, want := range []string{"Projects", "Project List", "Active", "Portal Project"} {
		if !strings.Contains(output, want) {
			t.Fatalf("projects screen missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"Status", "Facets", "Capabilities", "Automation", "Runtime", "Recent Activity", "main@system.status.read", "Data Policy"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("projects home should not render %q:\n%s", notWant, output)
		}
	}
}

func TestProjectsHomeOrdersActiveBeforeArchivedSummary(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	archived := fakeArchivedPortalProject()
	archived.Name = "Archived Portal Project"
	state.Data.Projects.Projects = append(state.Data.Projects.Projects, archived)

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: state, Width: 120, Height: 50})
	for _, want := range []string{"Project List", "Active", "Archived", "runtime actions are quarantined", "Portal Project", "Archived Portal Project"} {
		if !strings.Contains(output, want) {
			t.Fatalf("projects home output missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "\nActive\n") > strings.Index(output, "\nArchived\n") {
		t.Fatalf("active projects should render before archived group:\n%s", output)
	}
}

func TestProjectsHomeHidesSelectedProjectChildPartials(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.PartialErrors = []SnapshotError{
		{Source: "project_schedules", Message: "archived project schedule query failed"},
		{Source: "project_direct_events", Message: "archived project event query failed"},
	}
	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Screen:   ScreenProjects,
		State:    state,
		Registry: actions.DefaultRegistry(),
	})
	if strings.Contains(output, "Partial Data") || strings.Contains(output, "archived project schedule query failed") {
		t.Fatalf("projects home should hide selected-project child partials:\n%s", output)
	}

	state.Data.Projects.Explorer.Level = ProjectExplorerRuntime
	output = RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Screen:   ScreenProjects,
		State:    state,
		Registry: actions.DefaultRegistry(),
	})
	if !strings.Contains(output, "Partial Data") || !strings.Contains(output, "archived project schedule query failed") {
		t.Fatalf("project subview should keep relevant child partials:\n%s", output)
	}
}

func TestRenderProjectDetailSummarizesAreas(t *testing.T) {
	result := loadProjectsScreenWithSelection(context.Background(), newFakePortalClient(), "corr_test", Snapshot{}, "portal-project")
	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Screen:   ScreenProjects,
		State:    result.State,
		Registry: actions.DefaultRegistry(),
	})
	for _, want := range []string{"Status", "Project Areas", "Structure", "Runtime", "Storage", "Timeline"} {
		if !strings.Contains(output, want) {
			t.Fatalf("project detail missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"main@system.status.read", "Data Policy", "Recent Activity"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("project detail should summarize instead of rendering %q:\n%s", notWant, output)
		}
	}
}

func TestProjectsHomeSelectableItemsAreOnlyCoreActionsAndProjectRows(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	items := ScreenSelectableItems(result.State)
	if len(items) == 0 {
		t.Fatal("projects home should expose selectable items")
	}
	for _, item := range items {
		if selectableIsOperationalActionPresentation(item) {
			continue
		}
		switch item.Kind {
		case SelectableKindAction:
			if item.ActionID != "project.scaffold.backend" {
				t.Fatalf("projects home exposed non-core action item: %#v", item)
			}
		case SelectableKindRecord:
			if item.RecordKind != "project" {
				t.Fatalf("projects home exposed non-project record item: %#v", item)
			}
		default:
			t.Fatalf("projects home exposed unexpected selectable kind: %#v", item)
		}
	}
}

func TestProjectDetailSelectableItemsAreOnlySectionRows(t *testing.T) {
	result := loadProjectsScreenWithSelection(context.Background(), newFakePortalClient(), "corr_test", Snapshot{}, "portal-project")
	items := ScreenRecordItems(result.State)
	if len(items) == 0 {
		t.Fatal("project detail should expose section rows")
	}
	for _, item := range contentSelectableItems(items) {
		if item.RecordKind != projectSectionRecordKind(ProjectExplorerStructure) &&
			item.RecordKind != projectSectionRecordKind(ProjectExplorerRuntime) &&
			item.RecordKind != projectSectionRecordKind(ProjectExplorerStorage) &&
			item.RecordKind != projectSectionRecordKind(ProjectExplorerTimeline) &&
			item.RecordKind != projectSectionRecordKind(ProjectExplorerArchive) {
			t.Fatalf("project detail exposed non-section row: %#v", item)
		}
	}
}

func TestRenderProjectSubviewsExposeRelevantBlocks(t *testing.T) {
	result := loadProjectsScreenWithSelection(context.Background(), newFakePortalClient(), "corr_test", Snapshot{}, "portal-project")
	tests := []struct {
		level ProjectExplorerLevel
		want  []string
	}{
		{ProjectExplorerStructure, []string{"Facets", "Validation And Plan"}},
		{ProjectExplorerRuntime, []string{"Capabilities", "main@system.status.read", "Automation", "Runtime"}},
		{ProjectExplorerStorage, []string{"Data Policy", "Sync Status", "Backup Status"}},
		{ProjectExplorerTimeline, []string{"Recent Activity", "jobs:", "capability calls:"}},
	}
	for _, tc := range tests {
		t.Run(string(tc.level), func(t *testing.T) {
			state := result.State
			state.Data.Projects.Explorer.Level = tc.level
			output := RenderScreenWithState(RenderInput{
				Mode:     testMode(),
				Screen:   ScreenProjects,
				State:    state,
				Registry: actions.DefaultRegistry(),
			})
			for _, want := range tc.want {
				if !strings.Contains(output, want) {
					t.Fatalf("project %s view missing %q:\n%s", tc.level, want, output)
				}
			}
		})
	}
}

func TestProjectsScopedSearchFindsProjectRecords(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenProjects, Snapshot{})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned error: %v", result.Err)
	}
	results := SearchPortal(actions.DefaultRegistry(), result.State, "#projects portal-project", 8)
	if len(results) == 0 {
		t.Fatal("expected projects scoped search results")
	}
	if got := results[0].Category; got != "project" {
		t.Fatalf("top category = %s, want project", got)
	}
}

func TestLoadScreenProjectsHandlesEmptyList(t *testing.T) {
	client := newFakePortalClient()
	client.noProjects = true
	result := LoadScreen(context.Background(), client, "corr_test", ScreenProjects, Snapshot{})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned error: %v", result.Err)
	}
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("projects screen status = %s, want loaded", result.State.Status)
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: result.State, Registry: actions.DefaultRegistry()})
	if !strings.Contains(output, "No projects yet.") || !strings.Contains(output, "Create Project") {
		t.Fatalf("empty projects state missing helpful message:\n%s", output)
	}
}

func TestProjectsEmptyStateExposesTargetAwareCreateProjectAction(t *testing.T) {
	client := newFakePortalClient()
	client.noProjects = true
	missingBox := box.Resolved{RootPath: filepath.Join(t.TempDir(), "missing-box"), PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	result := LoadScreen(context.Background(), client, "corr_test", ScreenProjects, Snapshot{}, SnapshotOptions{BoxResolved: &missingBox})
	actions := ScreenAvailableActions(result.State)
	action, ok := findPortalAction(actions, "project.scaffold.backend")
	if !ok {
		t.Fatalf("empty project screen missing create project action: %#v", actions)
	}
	if action.Label != "Create Project" || action.TargetLabel != "select node" {
		t.Fatalf("unexpected project create action copy: %#v", action)
	}
	targetField, ok := portalActionInputField(action, "target_node")
	if !ok || targetField.Kind != ActionFieldSelect || !containsString(targetField.Options, "main") {
		t.Fatalf("unexpected target node field: %#v", targetField)
	}
	if _, ok := findPortalAction(actions, "project.create.hint"); ok {
		t.Fatalf("projects home should not expose old create hint when backend creation is available: %#v", actions)
	}
}

func TestLoadScreenProjectsKeepsPartialSubsystemFailures(t *testing.T) {
	client := newFakePortalClient()
	client.runtimeBindingsErr = errors.New("runtime binding store offline")
	result := LoadScreen(context.Background(), client, "corr_test", ScreenProjects, Snapshot{})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned error: %v", result.Err)
	}
	if result.State.Status != ScreenLoadPartial {
		t.Fatalf("projects screen status = %s, want partial", result.State.Status)
	}
	if len(result.State.Data.Projects.Projects) == 0 || len(result.State.Data.Projects.Capabilities) == 0 {
		t.Fatalf("partial runtime failure should preserve project and capability data: %#v", result.State.Data.Projects)
	}
	found := false
	for _, partial := range result.State.PartialErrors {
		if partial.Source == "project_runtime_bindings" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected project_runtime_bindings partial, got %#v", result.State.PartialErrors)
	}
}

func TestProjectsSelectableItemsAreContextualByExplorerLevel(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned error: %v", result.Err)
	}

	homeKinds := selectableRecordKinds(ScreenRecordItems(result.State))
	if !hasSelectableRecordKind(ScreenRecordItems(result.State), "project") {
		t.Fatalf("projects home missing project rows: %#v", homeKinds)
	}
	for _, notWant := range []string{"project_facet", "project_capability", "project_schedule", "project_watched_root", "project_job"} {
		if containsString(homeKinds, notWant) {
			t.Fatalf("projects home should not expose child record %s in %#v", notWant, homeKinds)
		}
	}

	detail := result.State
	detail.Data.Projects.Explorer.Level = ProjectExplorerDetail
	detailKinds := selectableRecordKinds(ScreenRecordItems(detail))
	for _, want := range []string{
		projectSectionRecordKind(ProjectExplorerStructure),
		projectSectionRecordKind(ProjectExplorerRuntime),
		projectSectionRecordKind(ProjectExplorerStorage),
		projectSectionRecordKind(ProjectExplorerTimeline),
	} {
		if !containsString(detailKinds, want) {
			t.Fatalf("project detail missing section row %s in %#v", want, detailKinds)
		}
	}
	for _, notWant := range []string{"project", "project_capability", "project_job", "project_watched_root"} {
		if containsString(detailKinds, notWant) {
			t.Fatalf("project detail should not expose %s in %#v", notWant, detailKinds)
		}
	}

	structure := result.State
	structure.Data.Projects.Explorer.Level = ProjectExplorerStructure
	structureKinds := selectableRecordKinds(ScreenRecordItems(structure))
	if !containsString(structureKinds, "project_facet") || containsString(structureKinds, "project_capability") {
		t.Fatalf("structure should expose facets only, got %#v", structureKinds)
	}

	runtime := result.State
	runtime.Data.Projects.Explorer.Level = ProjectExplorerRuntime
	runtimeKinds := selectableRecordKinds(ScreenRecordItems(runtime))
	for _, want := range []string{"project_workflow", "project_provider", "project_capability", "project_runtime_binding", "project_schedule", "project_direct_event_endpoint", "project_direct_event"} {
		if !containsString(runtimeKinds, want) {
			t.Fatalf("runtime missing %s in %#v", want, runtimeKinds)
		}
	}
	for _, notWant := range []string{"project_watched_root", "project_job", "project_capability_call"} {
		if containsString(runtimeKinds, notWant) {
			t.Fatalf("runtime should not expose %s in %#v", notWant, runtimeKinds)
		}
	}

	storage := result.State
	storage.Data.Projects.Explorer.Level = ProjectExplorerStorage
	storageKinds := selectableRecordKinds(ScreenRecordItems(storage))
	for _, want := range []string{"project_watched_root", "project_watched_root_finding", "project_sync_status", "project_backup_status"} {
		if !containsString(storageKinds, want) {
			t.Fatalf("storage missing %s in %#v", want, storageKinds)
		}
	}

	timeline := result.State
	timeline.Data.Projects.Explorer.Level = ProjectExplorerTimeline
	timelineKinds := selectableRecordKinds(ScreenRecordItems(timeline))
	for _, want := range []string{"project_invocation", "project_job", "project_capability_call"} {
		if !containsString(timelineKinds, want) {
			t.Fatalf("timeline missing %s in %#v", want, timelineKinds)
		}
	}
}

func TestProjectInlineActionsOnlyExpandForCurrentContextRows(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})

	state := result.State
	state.Data.Projects.Explorer.ExpandedRef = state.Data.Projects.SelectedProjectRef
	expandedHome := ScreenRecordItems(state)
	if got := countSelectableRecordKind(expandedHome, "project_inline_action"); got > 1 {
		t.Fatalf("home project rows should not expose old selected-project inline action bundle, got %d in %#v", got, selectableRecordKinds(expandedHome))
	}

	runtime := result.State
	runtime.Data.Projects.Explorer.Level = ProjectExplorerRuntime
	capabilityRef := "main@system.status.read"
	runtime.Data.Projects.Explorer.ExpandedRef = capabilityRef
	expandedRuntime := ScreenRecordItems(runtime)
	if countSelectableRecordKind(expandedRuntime, "project_inline_action") < 2 {
		t.Fatalf("expanded runtime capability row did not expose contextual inline actions: %#v", selectableRecordKinds(expandedRuntime))
	}
}

func TestProjectAvailableActionsAreContextualAndSmall(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	tests := []struct {
		name  string
		level ProjectExplorerLevel
		setup func(*ScreenState)
		want  []string
	}{
		{name: "home", level: ProjectExplorerHome, want: []string{"Create Project"}},
		{name: "detail", level: ProjectExplorerDetail, want: []string{"Inspect Project", "View Automation Health", "View Registration Plan", "Re-register Contract", "Add Facet", "Run Project Doctor", "Activate Project", "Review Archive"}},
		{name: "structure", level: ProjectExplorerStructure, want: []string{"Validate Contract", "View Registration Plan", "Add Facet", "Regenerate Missing Contracts"}},
		{name: "runtime", level: ProjectExplorerRuntime, want: []string{"Register Runtime", "Validate Runtime", "Disable Runtime"}},
		{name: "storage", level: ProjectExplorerStorage, want: []string{"View Watch Plan", "View Sync Status", "View Backup Status"}},
		{name: "timeline", level: ProjectExplorerTimeline, want: []string{"View Automation Health", "Run Project Doctor"}},
		{
			name:  "archive",
			level: ProjectExplorerArchive,
			setup: func(state *ScreenState) {
				state.Data.Projects.SelectedProject.Project.Status = "archived"
				state.Data.Projects.RegistrationDetail.Project.Project.Status = "archived"
			},
			want: []string{"Repository Status", "Inspect Repository", "Inspect Archive", "Plan Restore"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := result.State
			state.Data.Projects.Explorer.Level = tc.level
			if tc.setup != nil {
				tc.setup(&state)
			}
			actions := ScreenAvailableActions(state)
			if len(actions) > 8 {
				t.Fatalf("%s actions = %d, want <= 8: %#v", tc.name, len(actions), portalActionLabels(actions))
			}
			for _, label := range tc.want {
				if !hasPortalActionLabel(actions, label) {
					t.Fatalf("%s actions missing %q in %#v", tc.name, label, portalActionLabels(actions))
				}
			}
			for _, forbidden := range []string{"Back", "Refresh", "Open Folder"} {
				if hasPortalActionLabel(actions, forbidden) {
					t.Fatalf("%s actions should not include navigation action %q in %#v", tc.name, forbidden, portalActionLabels(actions))
				}
			}
		})
	}
}

func TestProjectSurfaceShowsStatusSignals(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.Data.Projects.Explorer.Level = ProjectExplorerDetail

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: state, Width: 120, Height: 40})
	for _, want := range []string{
		"Project Status",
		"lifecycle:",
		"active",
		"registration:",
		"registered",
		"activation:",
		projects.ProjectActivationStatusBaseActive,
		"backend owner: main",
		"facets: scripts",
		"contract health:",
		"contract drift:",
		string(projectdoctor.BackendDriftCurrent),
		"ok",
		"runtime:",
		"next action:",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("project status output missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "next action:") > strings.Index(output, "Project Metadata") {
		t.Fatalf("project status should show next action before metadata:\n%s", output)
	}
	if strings.Index(output, "contract health:") > strings.Index(output, "Project Metadata") ||
		strings.Index(output, "runtime:") > strings.Index(output, "Project Metadata") {
		t.Fatalf("project status should show health before metadata:\n%s", output)
	}
}

func TestProjectSurfaceShowsBackendContractDrift(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.Data.Projects.Explorer.Level = ProjectExplorerDetail
	state.Data.Projects.BackendAnalysis = &projectdoctor.BackendAnalysisResult{
		ProjectRef:  "portal-project",
		ProjectRoot: "/home/loomadmin/loom-box/Projects/portal-project",
		DriftStatus: projectdoctor.BackendDriftStale,
		Current:     false,
		NextAction:  "re-register contract from backend",
		Diff:        projectdoctor.DiffReport{ProjectRef: "portal-project", Summary: projectdoctor.DiffSummary{Changed: 1}},
		Report:      projectdoctor.Report{ProjectRef: "portal-project", Summary: projectdoctor.Summary{Blocked: 1}},
		Analysis:    projectcontracts.Analysis{Report: projectcontracts.ValidationReport{OK: true, Registerable: true}},
	}

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: state, Width: 120, Height: 40})
	for _, want := range []string{"contract drift:", string(projectdoctor.BackendDriftStale), "contract health:", "stale", "next action: re-register contract from backend"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stale backend drift output missing %q:\n%s", want, output)
		}
	}
}

func TestArchivedProjectSurfaceShowsArchiveState(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.Data.Projects.Explorer.Level = ProjectExplorerArchive
	state.Data.Projects.SelectedProject.Project.Status = "archived"
	state.Data.Projects.SelectedProject.Project.ArchiveState = fakeProjectArchiveState("portal-project")
	state.Data.Projects.RegistrationDetail.Project.Project.Status = "archived"
	state.Data.Projects.RegistrationDetail.Project.Project.ArchiveState = fakeProjectArchiveState("portal-project")

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: state, Width: 120, Height: 40})
	for _, want := range []string{
		"Archived Project",
		"Runtime is disabled",
		"Archive State",
		"archive status:",
		"runtime archive:",
		"project_runtime_archive_portal",
		"source:",
		"target:",
		"runtime migration:",
		"not_migrated",
		"next action: inspect archive or plan restore",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("archived project output missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"Add Facet", "Activate Project", "Register Runtime", "Validate Runtime", "Disable Runtime"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("archived project output exposed normal action %q:\n%s", notWant, output)
		}
	}
}

func TestArchivedProjectHomeRowShowsArchivedBeforeActivation(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	if len(state.Data.Projects.Projects) == 0 {
		t.Fatal("test fixture expected project list")
	}
	state.Data.Projects.Projects[0].Status = "archived"
	state.Data.Projects.Projects[0].ArchiveState = fakeProjectArchiveState("portal-project")
	state.Data.Projects.SelectedProject.Project.Status = "archived"
	state.Data.Projects.SelectedProject.Project.ArchiveState = fakeProjectArchiveState("portal-project")
	state.Data.Projects.RegistrationDetail.Project.Project.Status = "archived"
	state.Data.Projects.RegistrationDetail.Project.Project.ArchiveState = fakeProjectArchiveState("portal-project")
	if state.Data.Projects.RegistrationDetail.Registration == nil {
		t.Fatal("test fixture expected project registration")
	}
	state.Data.Projects.RegistrationDetail.Registration.ActivationStatus = projects.ProjectActivationStatusBaseActive

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: state, Width: 120, Height: 40})
	if !strings.Contains(output, "Portal Project  archived") {
		t.Fatalf("archived project row should render archived status:\n%s", output)
	}
	if strings.Contains(output, "Portal Project  "+projects.ProjectActivationStatusBaseActive) {
		t.Fatalf("archived project row should not render activation status as primary status:\n%s", output)
	}
}

func TestProjectSurfaceGuidesInactiveUnregisteredProject(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.Data.Projects.Explorer.Level = ProjectExplorerDetail
	state.Data.Projects.SelectedProject.Project.Status = "inactive"
	state.Data.Projects.RegistrationDetail.Project.Project.Status = "inactive"
	state.Data.Projects.RegistrationDetail.Registration = nil
	state.Data.Projects.RegistrationDetail.Facets = nil
	state.Data.Projects.RegistrationDetail.ScriptExposures = nil
	state.Data.Projects.RegistrationDetail.WatchedRootRegistrations = nil
	state.Data.Projects.Workflows = nil

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: state, Width: 120, Height: 40})
	for _, want := range []string{"inactive", "not_registered", "facets: none", "next action: register contract from backend"} {
		if !strings.Contains(output, want) {
			t.Fatalf("inactive project output missing %q:\n%s", want, output)
		}
	}
}

func TestProjectSurfaceGuidesStaleRegistration(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.Data.Projects.Explorer.Level = ProjectExplorerDetail
	if state.Data.Projects.RegistrationDetail.Registration == nil {
		t.Fatal("test fixture expected registered project")
	}
	state.Data.Projects.RegistrationDetail.Registration.RegistrationStatus = projects.ProjectRegistrationStatusStale

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: state, Width: 120, Height: 40})
	for _, want := range []string{projects.ProjectRegistrationStatusStale, "next action: re-register contract from backend"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stale project output missing %q:\n%s", want, output)
		}
	}
}

func TestProjectSurfaceShowsNewlyAddedFacet(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.Data.Projects.Explorer.Level = ProjectExplorerDetail
	state.Data.Projects.RegistrationDetail.Facets = append(state.Data.Projects.RegistrationDetail.Facets, projects.ProjectContractFacet{
		FacetKey:    "notes",
		Enabled:     true,
		Present:     true,
		FacetStatus: projects.ProjectFacetStatusActivated,
	})

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: state, Width: 120, Height: 40})
	if !strings.Contains(output, "facets: scripts, notes") {
		t.Fatalf("new facet was not reflected in project status:\n%s", output)
	}
}

func TestProjectLocalOnlyActionsHiddenWhenRootIsNotReadable(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	if state.Data.Projects.RegistrationDetail.Registration == nil {
		t.Fatal("test fixture expected registered project")
	}
	state.Data.Projects.RegistrationDetail.Registration.ProjectRoot = filepath.Join(t.TempDir(), "missing-project-root")

	state.Data.Projects.Explorer.Level = ProjectExplorerStructure
	structureLabels := portalActionLabels(ScreenAvailableActions(state))
	if containsString(structureLabels, "Validate Local Contract") {
		t.Fatalf("structure actions exposed unreadable local validation: %#v", structureLabels)
	}
	if !containsString(structureLabels, "Validate Contract") || !containsString(structureLabels, "Add Facet") {
		t.Fatalf("structure actions lost backend management actions: %#v", structureLabels)
	}

	state.Data.Projects.Explorer.Level = ProjectExplorerDetail
	detailLabels := portalActionLabels(ScreenAvailableActions(state))
	if containsString(detailLabels, "Clean Scaffold Examples") {
		t.Fatalf("detail actions exposed unreadable local scaffold cleanup: %#v", detailLabels)
	}
}

func TestProjectLocalValidationAppearsForReadableRoot(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	if state.Data.Projects.RegistrationDetail.Registration == nil {
		t.Fatal("test fixture expected registered project")
	}
	state.Data.Projects.Explorer.Level = ProjectExplorerStructure
	state.Data.Projects.RegistrationDetail.Registration.ProjectRoot = t.TempDir()

	labels := portalActionLabels(ScreenAvailableActions(state))
	if !containsString(labels, "Validate Local Contract") {
		t.Fatalf("structure actions should expose local validation for readable root: %#v", labels)
	}
}

func TestProjectPlaceholderActionsAreDisabled(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	for _, tc := range []struct {
		level ProjectExplorerLevel
		label string
	}{
		{ProjectExplorerStructure, "Regenerate Missing Contracts"},
		{ProjectExplorerRuntime, "Register Runtime"},
		{ProjectExplorerRuntime, "Validate Runtime"},
		{ProjectExplorerRuntime, "Disable Runtime"},
	} {
		t.Run(string(tc.level)+" "+tc.label, func(t *testing.T) {
			state := result.State
			state.Data.Projects.Explorer.Level = tc.level
			action, ok := findPortalActionByLabel(ScreenAvailableActions(state), tc.label)
			if !ok {
				t.Fatalf("missing action %q", tc.label)
			}
			if !action.Disabled() || action.DisabledReason == "" {
				t.Fatalf("action %q should be disabled with reason: %#v", tc.label, action)
			}
		})
	}
}

func TestProjectRuntimeRowsDoNotExposeScheduleManagementByDefault(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.Data.Projects.Explorer.Level = ProjectExplorerRuntime
	for _, item := range ScreenRecordItems(state) {
		if item.RecordKind != "project_schedule" {
			continue
		}
		for _, action := range item.RelatedActions {
			switch action.Label {
			case "Fire Schedule Now", "Pause Schedule", "Resume Schedule":
				t.Fatalf("project runtime schedule row exposed management action %q", action.Label)
			}
		}
		return
	}
	t.Fatal("expected project runtime schedule row")
}

func TestArchivedProjectAvailableActionsRemainRepositoryInspectableAndMutationQuarantined(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	for _, level := range []ProjectExplorerLevel{
		ProjectExplorerDetail,
		ProjectExplorerStructure,
		ProjectExplorerRuntime,
		ProjectExplorerStorage,
		ProjectExplorerTimeline,
		ProjectExplorerArchive,
	} {
		t.Run(string(level), func(t *testing.T) {
			state := result.State
			state.Data.Projects.Explorer.Level = level
			state.Data.Projects.SelectedProject.Project.Status = "archived"
			state.Data.Projects.RegistrationDetail.Project.Project.Status = "archived"
			labels := portalActionLabels(ScreenAvailableActions(state))
			if len(labels) != 4 || !containsString(labels, "Repository Status") || !containsString(labels, "Inspect Repository") || !containsString(labels, "Inspect Archive") || !containsString(labels, "Plan Restore") {
				t.Fatalf("archived project actions = %#v, want repository/archive inspection and restore planning only", labels)
			}
			allActions := ScreenActions(state)
			allLabels := portalActionLabels(allActions)
			if len(allLabels) != 4 || !containsString(allLabels, "Repository Status") || !containsString(allLabels, "Inspect Repository") || !containsString(allLabels, "Inspect Archive") || !containsString(allLabels, "Plan Restore") {
				t.Fatalf("archived project screen actions = %#v, want repository/archive inspection and restore planning only", allLabels)
			}
			inventory := ActionInventoryForScreen(state)
			if len(inventory) != 4 {
				t.Fatalf("archived project inventory = %#v, want bounded read-only repository/archive actions", inventory)
			}
			for _, action := range allActions {
				if action.TargetKind == "project_repository" || action.TargetKind == "project_repository_status" {
					if action.Risk != ActionRiskInspect || action.Executor.Kind != PortalExecutorRecordInspect {
						t.Fatalf("archived repository action is not inspect-only: %#v", action)
					}
				}
			}
			assertNoPortalActionExecutors(t, allActions,
				PortalExecutorProjectAddFacet,
				PortalExecutorProjectRegisterBackend,
				PortalExecutorProjectValidateBackend,
				PortalExecutorProjectActivate,
				PortalExecutorProjectDeactivate,
				PortalExecutorCapabilityCall,
				PortalExecutorJobRetry,
				PortalExecutorJobCancel,
				PortalExecutorScheduleFireNow,
			)
			for _, item := range contentSelectableItems(ScreenRecordItems(state)) {
				if item.PrimaryAction != nil {
					t.Fatalf("archived project record %s exposed primary action %#v", item.RecordKind, *item.PrimaryAction)
				}
				if len(item.RelatedActions) != 0 {
					t.Fatalf("archived project record %s exposed related actions %#v", item.RecordKind, portalActionLabels(item.RelatedActions))
				}
			}
		})
	}
}

func TestProjectExplorerEnterOnProjectMovesHomeToDetail(t *testing.T) {
	state := testProjectExplorerState()
	state.SelectedIndex = projectExplorerIndexForRef(state, "portal-project")
	model := Model{
		screen:       ScreenProjects,
		screenStates: map[string]ScreenState{ScreenProjects: state},
		keymap:       DefaultKeyMap(),
	}

	updated, cmd, ok := model.openProjectSelection()
	if !ok {
		t.Fatal("openProjectSelection returned ok=false")
	}
	if cmd != nil {
		t.Fatal("opening already loaded project should not reload")
	}
	next := updated.currentScreenState()
	if got := next.Data.Projects.Explorer.Level; got != ProjectExplorerDetail {
		t.Fatalf("explorer level = %s, want %s", got, ProjectExplorerDetail)
	}
	if got := next.Data.Projects.Explorer.SelectedProjectRef; got != "portal-project" {
		t.Fatalf("selected project ref = %q, want portal-project", got)
	}
}

func TestProjectExplorerEnterOnDetailSectionMovesToSubview(t *testing.T) {
	state := testProjectExplorerState()
	state.Data.Projects.Explorer.Level = ProjectExplorerDetail
	state.SelectedIndex = projectExplorerIndexForRef(state, "portal-project."+string(ProjectExplorerRuntime))
	model := Model{
		screen:       ScreenProjects,
		screenStates: map[string]ScreenState{ScreenProjects: state},
		keymap:       DefaultKeyMap(),
	}

	updated, cmd, ok := model.openProjectSelection()
	if !ok {
		t.Fatal("openProjectSelection returned ok=false")
	}
	if cmd != nil {
		t.Fatal("opening project section should not reload")
	}
	next := updated.currentScreenState()
	if got := next.Data.Projects.Explorer.Level; got != ProjectExplorerRuntime {
		t.Fatalf("explorer level = %s, want %s", got, ProjectExplorerRuntime)
	}
}

func TestProjectExplorerBackMovesDetailHomeAndSubviewsDetail(t *testing.T) {
	t.Run("detail to home", func(t *testing.T) {
		state := testProjectExplorerState()
		state.Data.Projects.Explorer.Level = ProjectExplorerDetail
		model := Model{
			screen:       ScreenProjects,
			screenStates: map[string]ScreenState{ScreenProjects: state},
			keymap:       DefaultKeyMap(),
		}

		updated, ok := model.projectExplorerBack()
		if !ok {
			t.Fatal("projectExplorerBack returned ok=false")
		}
		if got := updated.currentScreenState().Data.Projects.Explorer.Level; got != ProjectExplorerHome {
			t.Fatalf("explorer level = %s, want %s", got, ProjectExplorerHome)
		}
	})

	t.Run("subview to detail", func(t *testing.T) {
		state := testProjectExplorerState()
		state.Data.Projects.Explorer.Level = ProjectExplorerRuntime
		model := Model{
			screen:       ScreenProjects,
			screenStates: map[string]ScreenState{ScreenProjects: state},
			keymap:       DefaultKeyMap(),
		}

		updated, ok := model.projectExplorerBack()
		if !ok {
			t.Fatal("projectExplorerBack returned ok=false")
		}
		if got := updated.currentScreenState().Data.Projects.Explorer.Level; got != ProjectExplorerDetail {
			t.Fatalf("explorer level = %s, want %s", got, ProjectExplorerDetail)
		}
	})

	t.Run("expanded actions collapse first", func(t *testing.T) {
		state := testProjectExplorerState()
		state.Data.Projects.Explorer.Level = ProjectExplorerRuntime
		state.Data.Projects.Explorer.ExpandedRef = "portal-project"
		state.SelectedIndex = projectExplorerIndexForRef(state, "portal-project")
		model := Model{
			screen:       ScreenProjects,
			screenStates: map[string]ScreenState{ScreenProjects: state},
			keymap:       DefaultKeyMap(),
		}

		updated, ok := model.projectExplorerBack()
		if !ok {
			t.Fatal("projectExplorerBack returned ok=false")
		}
		next := updated.currentScreenState().Data.Projects.Explorer
		if next.ExpandedRef != "" {
			t.Fatalf("expanded ref = %q, want empty", next.ExpandedRef)
		}
		if got := next.Level; got != ProjectExplorerRuntime {
			t.Fatalf("explorer level = %s, want %s", got, ProjectExplorerRuntime)
		}
	})
}

func TestProjectSelectionLoaderPreservesDetailLevel(t *testing.T) {
	result := loadProjectsScreenWithSelection(context.Background(), newFakePortalClient(), "corr_test", Snapshot{}, "portal-project")
	if result.Err != nil {
		t.Fatalf("loadProjectsScreenWithSelection returned error: %v", result.Err)
	}
	if got := result.State.Data.Projects.Explorer.Level; got != ProjectExplorerDetail {
		t.Fatalf("explorer level = %s, want %s", got, ProjectExplorerDetail)
	}
}

func testProjectExplorerState() ScreenState {
	homeNode := "main"
	state := NewScreenState(ScreenProjects)
	state.Status = ScreenLoadLoaded
	state.Data.Projects = ProjectsData{
		Projects: []projects.Project{{
			ProjectID:       "project_portal",
			ProjectScopeKey: "project.portal-project",
			Slug:            "portal-project",
			Name:            "Portal Project",
			HomeNodeID:      &homeNode,
			Status:          "active",
			ProjectType:     "automation",
		}},
		SelectedProjectRef: "portal-project",
		Explorer: ProjectExplorerState{
			Level:              ProjectExplorerHome,
			SelectedProjectRef: "portal-project",
		},
	}
	return state
}

func TestProjectActionsValidateActivateAndResolve(t *testing.T) {
	client := newFakePortalClient()
	detail := fakeProjectRegistrationDetail()
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Portal Project",
		Slug:      "portal-project",
		OwnerNode: "main",
		Preset:    projectcontracts.PresetMinimal,
		Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	detail.Registration.ProjectRoot = scaffold.ProjectRoot

	validate := NewProjectValidateLocalAction(detail, ScreenProjects)
	validateResult, err := ExecutePortalAction(context.Background(), client, "corr_test", validate, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(validate) returned error: %v", err)
	}
	if validateResult.Status != ActionLifecycleSucceeded || !strings.Contains(validateResult.Summary, "valid") {
		t.Fatalf("unexpected validation result: %#v", validateResult)
	}

	missing := detail
	missing.Registration.ProjectRoot = scaffold.ProjectRoot + "-missing"
	missingValidate := NewProjectValidateLocalAction(missing, ScreenProjects)
	if !missingValidate.Disabled() || !strings.Contains(missingValidate.DisabledReason, "not readable") {
		t.Fatalf("missing-root validation should be disabled: %#v", missingValidate)
	}

	activation, err := ExecutePortalAction(context.Background(), client, "corr_test", NewProjectActivateAction(detail, "scripts", ScreenProjects), true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(activate) returned error: %v", err)
	}
	if activation.Status != ActionLifecycleSucceeded || client.activateProject.Facet != "scripts" {
		t.Fatalf("activation result/input = %#v / %#v", activation, client.activateProject)
	}

	plan, err := ExecutePortalAction(context.Background(), client, "corr_test", NewProjectRegistrationPlanAction(detail, ScreenProjects), true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(plan) returned error: %v", err)
	}
	if plan.Status != ActionLifecycleSucceeded || !hasActionField(plan.Fields, "Actions", "1") || !hasActionField(plan.Fields, "Workflows", "1") {
		t.Fatalf("registration plan did not summarize stored plan: %#v", plan)
	}

	action, err := ResolvePortalAction(context.Background(), ActionResolver{Registry: actions.DefaultRegistry(), Client: client, CorrelationID: "corr_test"}, "project.portal_project.registration_plan", ScreenProjects, Snapshot{})
	if err != nil {
		t.Fatalf("ResolvePortalAction returned error: %v", err)
	}
	if action.Executor.Kind != PortalExecutorProjectRegistrationPlan {
		t.Fatalf("resolved executor = %s, want %s", action.Executor.Kind, PortalExecutorProjectRegistrationPlan)
	}

	health, err := ExecutePortalAction(context.Background(), client, "corr_test", NewProjectAutomationHealthAction(detail, ScreenProjects), true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(automation health) returned error: %v", err)
	}
	for _, want := range []string{"Overall", "Backend Drift", "Target Capabilities", "Runtime Bindings", "Credentials", "Dispatcher"} {
		if !hasActionFieldLabel(health.Fields, want) {
			t.Fatalf("automation health missing %q field: %#v", want, health.Fields)
		}
	}
	if client.invocationFilter.ProjectRef != "portal-project" || client.invocationFailureFilter.ProjectRef != "portal-project" {
		t.Fatalf("automation health should use project-filtered invocations: %#v / %#v", client.invocationFilter, client.invocationFailureFilter)
	}
}

func TestProjectAddFacetActionExecutesBackendDryRun(t *testing.T) {
	client := newFakePortalClient()
	detail := fakeProjectRegistrationDetail()
	action := NewProjectAddFacetAction(detail, ScreenProjects)
	if action.State != ActionAvailable || action.Executor.Kind != PortalExecutorProjectAddFacet || action.Interaction() != ActionInteractionFormRun {
		t.Fatalf("add facet action should be an available backend form action: %#v", action)
	}
	if !hasPortalActionInputField(action, "register_after_apply") {
		t.Fatalf("add facet action missing register_after_apply field: %#v", action.InputFields)
	}
	action.InputValues["facets"] = "notes, scripts"

	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(add facet) returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded ||
		!strings.Contains(result.Summary, "planned") ||
		!hasActionField(result.Fields, "Project", "portal-project") ||
		!hasActionField(result.Fields, "Requested", "notes, scripts") ||
		!hasActionField(result.Fields, "Added", "notes") ||
		!hasActionField(result.Fields, "Already Enabled", "scripts") ||
		!hasActionField(result.Fields, "Dry Run", "true") ||
		!hasActionField(result.Fields, "Registration", "skipped_dry_run") {
		t.Fatalf("unexpected add facet result: %#v", result)
	}
	if client.addProjectFacetsInput.ProjectRef != "portal-project" ||
		client.addProjectFacetsInput.ProjectRoot != "/tmp/portal-project" ||
		strings.Join(client.addProjectFacetsInput.Facets, ",") != "notes,scripts" ||
		!client.addProjectFacetsInput.DryRun {
		t.Fatalf("unexpected add facet input: %#v", client.addProjectFacetsInput)
	}
	if client.registerBackendInput.ProjectRef != "" || client.registerBackendInput.ProjectRoot != "" {
		t.Fatalf("dry-run add facet should not register contract: %#v", client.registerBackendInput)
	}
}

func TestProjectAddFacetActionAppliesAndRegistersBackendContract(t *testing.T) {
	client := newFakePortalClient()
	detail := fakeProjectRegistrationDetail()
	action := NewProjectAddFacetAction(detail, ScreenProjects)
	action.InputValues["facets"] = "notes"
	action.InputValues["dry_run"] = "false"
	action.InputValues["force"] = "true"

	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(add facet apply) returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded ||
		!strings.Contains(result.Summary, "registered") ||
		!hasActionField(result.Fields, "Dry Run", "false") ||
		!hasActionField(result.Fields, "Registration", "registered") ||
		!hasActionField(result.Fields, "Revision", "1") ||
		!hasActionField(result.Fields, "Registered Root", "/tmp/portal-project") {
		t.Fatalf("unexpected add facet apply result: %#v", result)
	}
	if !client.addProjectFacetsInput.Force || client.addProjectFacetsInput.DryRun {
		t.Fatalf("unexpected add facet apply input: %#v", client.addProjectFacetsInput)
	}
	if client.registerBackendInput.ProjectRef != "portal-project" ||
		client.registerBackendInput.ProjectRoot != "/tmp/portal-project" {
		t.Fatalf("add facet apply should register generated contract, got %#v", client.registerBackendInput)
	}
}

func TestProjectLayoutMigrationActionIsLegacyOnlyAndExecutesBackend(t *testing.T) {
	client := newFakePortalClient()
	detail := fakeProjectRegistrationDetail()
	root := "/home/loomadmin/loom-box/Projects/portal-project"
	legacyAnalysis := &projectdoctor.BackendAnalysisResult{Analysis: projectcontracts.Analysis{Loaded: &projectcontracts.LoadedProject{
		RootPath:     root,
		ContractPath: filepath.Join(root, projectcontracts.LegacyRootContractPath),
		Layout:       projectcontracts.ProjectLayoutLegacy,
	}}}
	action := NewProjectMigrateLayoutAction(detail, legacyAnalysis, ScreenProjects)
	if action.State != ActionAvailable || action.Executor.Kind != PortalExecutorProjectMigrateLayout || action.Interaction() != ActionInteractionFormRun {
		t.Fatalf("legacy migration should be an available form action: %#v", action)
	}
	if action.InputValues["dry_run"] != "true" {
		t.Fatalf("migration should default to dry-run: %#v", action.InputValues)
	}

	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(migration dry-run) returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded || !strings.Contains(result.Summary, "planned") || !hasActionField(result.Fields, "Layout", "legacy") || !hasActionField(result.Fields, "Dry Run", "true") {
		t.Fatalf("unexpected migration dry-run result: %#v", result)
	}
	if client.layoutMigrationInput.ProjectRef != "portal-project" || client.layoutMigrationInput.ProjectRoot != root || client.layoutMigrationInput.Apply || client.layoutMigrationInput.Yes {
		t.Fatalf("unexpected migration dry-run input: %#v", client.layoutMigrationInput)
	}

	action.InputValues["dry_run"] = "false"
	result, err = ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(migration apply) returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded || !strings.Contains(result.Summary, "migrated") || !hasActionField(result.Fields, "Layout", "legacy -> canonical") || !hasActionField(result.Fields, "Record", ".loom/state/layout-migrations/test.json") {
		t.Fatalf("unexpected migration apply result: %#v", result)
	}
	if !client.layoutMigrationInput.Apply || !client.layoutMigrationInput.Yes {
		t.Fatalf("migration apply must send explicit apply and confirmation: %#v", client.layoutMigrationInput)
	}

	canonicalAnalysis := &projectdoctor.BackendAnalysisResult{Analysis: projectcontracts.Analysis{Loaded: &projectcontracts.LoadedProject{
		RootPath:     root,
		ContractPath: filepath.Join(root, filepath.FromSlash(projectcontracts.CanonicalRootContractPath)),
		Layout:       projectcontracts.ProjectLayoutCanonical,
	}}}
	if canonical := NewProjectMigrateLayoutAction(detail, canonicalAnalysis, ScreenProjects); !canonical.Disabled() {
		t.Fatalf("canonical project should not expose migration: %#v", canonical)
	}
	detail.Project.Project.Status = "archived"
	if archived := NewProjectMigrateLayoutAction(detail, legacyAnalysis, ScreenProjects); !archived.Disabled() || !strings.Contains(strings.ToLower(archived.DisabledReason), "archived") {
		t.Fatalf("archived project migration should be disabled: %#v", archived)
	}
}

func TestProjectStructureShowsLegacyLayoutAndMigrationAction(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.Data.Projects.Explorer.Level = ProjectExplorerStructure
	root := "/home/loomadmin/loom-box/Projects/portal-project"
	state.Data.Projects.BackendAnalysis = &projectdoctor.BackendAnalysisResult{Analysis: projectcontracts.Analysis{Loaded: &projectcontracts.LoadedProject{
		RootPath:     root,
		ContractPath: filepath.Join(root, projectcontracts.LegacyRootContractPath),
		Layout:       projectcontracts.ProjectLayoutLegacy,
	}}}

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: state, Width: 120, Height: 40})
	for _, want := range []string{"layout: legacy", "contract: " + filepath.Join(root, projectcontracts.LegacyRootContractPath), "compatibility: migration available in Structure actions", "Migrate Project Layout"} {
		if !strings.Contains(output, want) {
			t.Fatalf("legacy project structure missing %q:\n%s", want, output)
		}
	}
}

func TestProjectValidateAndRegisterBackendActionsExecute(t *testing.T) {
	client := newFakePortalClient()
	detail := fakeProjectRegistrationDetail()

	validate := NewProjectValidateBackendAction(detail, ScreenProjects)
	validateResult, err := ExecutePortalAction(context.Background(), client, "corr_test", validate, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(validate backend) returned error: %v", err)
	}
	if validateResult.Status != ActionLifecycleSucceeded ||
		!hasActionField(validateResult.Fields, "Project", "portal-project") ||
		!hasActionField(validateResult.Fields, "Drift", string(projectdoctor.BackendDriftCurrent)) ||
		!hasActionField(validateResult.Fields, "Current", "true") ||
		client.backendAnalysisInput.ProjectRef != "portal-project" ||
		client.backendAnalysisInput.ProjectRoot != "/tmp/portal-project" {
		t.Fatalf("unexpected backend validation result/input: %#v / %#v", validateResult, client.backendAnalysisInput)
	}

	register := NewProjectRegisterBackendAction(detail, ScreenProjects)
	if register.State != ActionAvailable || register.Executor.Kind != PortalExecutorProjectRegisterBackend || register.Interaction() != ActionInteractionFormRun {
		t.Fatalf("register backend action should be an available form action: %#v", register)
	}
	register.InputValues["strict"] = "true"
	registerResult, err := ExecutePortalAction(context.Background(), client, "corr_test", register, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(register backend) returned error: %v", err)
	}
	if registerResult.Status != ActionLifecycleSucceeded ||
		!strings.Contains(registerResult.Summary, "updated") ||
		!hasActionField(registerResult.Fields, "Strict", "true") ||
		client.registerBackendInput.ProjectRef != "portal-project" ||
		client.registerBackendInput.ProjectRoot != "/tmp/portal-project" ||
		!client.registerBackendInput.Strict {
		t.Fatalf("unexpected backend registration result/input: %#v / %#v", registerResult, client.registerBackendInput)
	}
}

func TestProjectDiffActionUsesBackendWhenLocalRootUnreadable(t *testing.T) {
	client := newFakePortalClient()
	detail := fakeProjectRegistrationDetail()
	if detail.Registration == nil {
		t.Fatal("test fixture expected registered project")
	}
	detail.Registration.ProjectRoot = filepath.Join(t.TempDir(), "missing-project-root")
	stale := projectdoctor.BackendAnalysisResult{
		ProjectRef:  "portal-project",
		ProjectRoot: "/home/loomadmin/loom-box/Projects/portal-project",
		Analysis: projectcontracts.Analysis{
			Report: projectcontracts.ValidationReport{OK: true, Registerable: true, Project: projectcontracts.PlanProject{Slug: "portal-project"}},
			Plan:   projectcontracts.ProjectPlan{Registerable: true, Project: projectcontracts.PlanProject{Slug: "portal-project"}},
		},
		Diff:        projectdoctor.DiffReport{ProjectRef: "portal-project", Summary: projectdoctor.DiffSummary{Changed: 1}},
		Report:      projectdoctor.Report{ProjectRef: "portal-project", Summary: projectdoctor.Summary{Blocked: 1}},
		DriftStatus: projectdoctor.BackendDriftStale,
		Current:     false,
		NextAction:  "re-register contract from backend",
	}
	client.backendAnalysisOverride = &stale

	action := NewProjectDiffAction(detail, ScreenProjects)
	if action.Disabled() || action.Executor.Payload["project_root"] != "" {
		t.Fatalf("diff action should be backend-capable for unreadable roots: %#v", action)
	}
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(diff) returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded ||
		!hasActionField(result.Fields, "Changed", "1") ||
		client.backendAnalysisInput.ProjectRef != "portal-project" {
		t.Fatalf("unexpected backend diff result/input: %#v / %#v", result, client.backendAnalysisInput)
	}
}

func TestProjectArchiveActionExecutesBackendDryRun(t *testing.T) {
	client := newFakePortalClient()
	detail := fakeProjectRegistrationDetail()
	action := newLegacyProjectArchiveAction(detail, ScreenProjects)
	if action.State != ActionAvailable || action.Executor.Kind != PortalExecutorProjectArchive || action.Interaction() != ActionInteractionFormRun {
		t.Fatalf("archive action should be an available backend form action: %#v", action)
	}
	action.InputValues["skip_storage_archive"] = "true"
	action.InputValues["target_path"] = "main/Archive/Projects/portal-project"
	action.InputValues["reason"] = "portal test"

	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(archive) returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded ||
		!strings.Contains(result.Summary, "planned") ||
		!hasActionField(result.Fields, "Dry Run", "true") ||
		!hasActionField(result.Fields, "Storage Archive Skipped", "true") ||
		client.projectArchiveRef != "portal-project" ||
		!client.projectArchiveInput.DryRun ||
		!client.projectArchiveInput.SkipStorageArchive ||
		client.projectArchiveInput.TargetPath != "main/Archive/Projects/portal-project" ||
		client.projectArchiveInput.Reason != "portal test" {
		t.Fatalf("unexpected project archive result/input: %#v / %#v", result, client.projectArchiveInput)
	}
}

func TestProjectArchiveInspectAndRestorePlanActionsExecute(t *testing.T) {
	client := newFakePortalClient()
	detail := fakeProjectRegistrationDetail()
	detail.Project.Project.Status = "archived"
	detail.Project.Project.ArchiveState = fakeProjectArchiveState("portal-project")

	inspect := NewProjectInspectArchiveAction(detail, ScreenProjects)
	if inspect.State != ActionAvailable || inspect.Executor.Kind != PortalExecutorProjectArchiveInspect || inspect.Interaction() != ActionInteractionDirectInspect {
		t.Fatalf("inspect archive should be an available backend action: %#v", inspect)
	}
	inspectResult, err := ExecutePortalAction(context.Background(), client, "corr_test", inspect, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(archive inspect) returned error: %v", err)
	}
	if inspectResult.Status != ActionLifecycleSucceeded ||
		client.projectArchiveInspectRef != "portal-project" ||
		!hasActionField(inspectResult.Fields, "Archive Status", "archived") ||
		!hasActionField(inspectResult.Fields, "Runtime Migration", "not_migrated") {
		t.Fatalf("unexpected archive inspect result/ref: %#v / %s", inspectResult, client.projectArchiveInspectRef)
	}

	restore := NewProjectRestoreReactivateAction(detail, ScreenProjects)
	if restore.State != ActionAvailable || restore.Executor.Kind != PortalExecutorProjectArchiveRestore || restore.Interaction() != ActionInteractionFormRun {
		t.Fatalf("restore plan should be an available backend form action: %#v", restore)
	}
	restore.InputValues["to_node"] = "macbook"
	restoreResult, err := ExecutePortalAction(context.Background(), client, "corr_test", restore, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction(archive restore plan) returned error: %v", err)
	}
	if restoreResult.Status != ActionLifecycleSucceeded ||
		client.projectArchiveRestoreRef != "portal-project" ||
		client.projectArchiveRestoreInput.ToNode != "macbook" ||
		!client.projectArchiveRestoreInput.DryRun ||
		!hasActionField(restoreResult.Fields, "Dry Run", "true") ||
		!hasActionField(restoreResult.Fields, "Step storage", "would_restore") {
		t.Fatalf("unexpected archive restore plan result/input: %#v / %#v", restoreResult, client.projectArchiveRestoreInput)
	}
}

func TestProjectActivationActionDisabledWithoutRegistration(t *testing.T) {
	detail := fakeProjectRegistrationDetail()
	detail.Registration = nil
	action := NewProjectActivateAction(detail, "scripts", ScreenProjects)
	if !action.Disabled() || !strings.Contains(action.DisabledReason, "no registered") {
		t.Fatalf("activation should be disabled without registration: %#v", action)
	}
}

func TestArchivedProjectMutationActionsDisabled(t *testing.T) {
	detail := fakeProjectRegistrationDetail()
	detail.Project.Project.Status = "archived"
	detail.Project.Project.ArchiveState = fakeProjectArchiveState("portal-project")

	activate := NewProjectActivateAction(detail, "scripts", ScreenProjects)
	if !activate.Disabled() || !strings.Contains(strings.ToLower(activate.DisabledReason), "archived") {
		t.Fatalf("activation should be disabled for archived project: %#v", activate)
	}
	addFacet := NewProjectAddFacetAction(detail, ScreenProjects)
	if !addFacet.Disabled() || !strings.Contains(strings.ToLower(addFacet.DisabledReason), "archived") {
		t.Fatalf("add facet should be disabled for archived project: %#v", addFacet)
	}
}

func TestArchivedProjectDoesNotExposeSupportBundleProjectAction(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenProjects, Snapshot{})
	state := result.State
	state.Data.Projects.Explorer.Level = ProjectExplorerDetail
	state.Data.Projects.SelectedProject.Project.Status = "archived"
	state.Data.Projects.RegistrationDetail.Project.Project.Status = "archived"
	if action, ok := findPortalAction(ScreenActions(state), "support.bundle.create_hint"); ok {
		t.Fatalf("archived project should not expose support bundle as a project action: %#v", action)
	}
}

func hasSelectableRecordKind(items []SelectableItem, kind string) bool {
	for _, item := range items {
		if item.RecordKind == kind {
			return true
		}
	}
	return false
}

func countSelectableRecordKind(items []SelectableItem, kind string) int {
	count := 0
	for _, item := range items {
		if item.RecordKind == kind {
			count++
		}
	}
	return count
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasActionFieldLabel(fields []ActionResultField, label string) bool {
	for _, field := range fields {
		if field.Label == label {
			return true
		}
	}
	return false
}

func selectableRecordKinds(items []SelectableItem) []string {
	kinds := []string{}
	seen := map[string]bool{}
	for _, item := range items {
		if item.RecordKind == "" || seen[item.RecordKind] {
			continue
		}
		seen[item.RecordKind] = true
		kinds = append(kinds, item.RecordKind)
	}
	return kinds
}

func selectableRecordRefs(items []SelectableItem) []string {
	refs := []string{}
	for _, item := range items {
		if item.RecordKind == "" {
			continue
		}
		refs = append(refs, item.RecordKind+":"+item.RecordRef)
	}
	return refs
}

func findRecordItem(items []SelectableItem, kind, ref string) (SelectableItem, bool) {
	for _, item := range items {
		if item.RecordKind == kind && item.RecordRef == ref {
			return item, true
		}
	}
	return SelectableItem{}, false
}

func hasActionField(fields []ActionResultField, label, value string) bool {
	for _, field := range fields {
		if field.Label == label && field.Value == value {
			return true
		}
	}
	return false
}

func TestLoadScreenNotesLoadsOverview(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenNotes, Snapshot{})
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("notes screen status = %s, partials=%v", result.State.Status, result.State.PartialErrors)
	}
	if !client.notesOverviewInput.IncludeInactive {
		t.Fatal("notes loader should request inactive roots so raw details can reveal them")
	}
	if result.State.Data.Notes.Overview.Totals.RootCount != 3 {
		t.Fatalf("root count = %d, want 3", result.State.Data.Notes.Overview.Totals.RootCount)
	}
}

func TestRenderNotesScreenShowsOverviewAndHidesInactiveByDefault(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenNotes, Snapshot{})
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenNotes, State: result.State, Width: 120, Height: 40})
	for _, want := range []string{"LOOM Notes", "Available Actions", "Policies...", "s notes search", "Search notes...", "Embeddings: OFF", "runtime=ollama", "model=mxbai-embed-large", "Search Results", "Press s to search indexed notes.", "Notes Attention", "Doctor owns", "Coverage", "roots=2", "File Types", "markdown=2", "Notes Across Network", "Box Notes - 2 files", "osint-tools / notes - 2 files", "Processing And Search Health", "extracted=2", "too_large=1", "Projection"} {
		if !strings.Contains(output, want) {
			t.Fatalf("notes output missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "Search Results") > strings.Index(output, "Coverage") ||
		strings.Index(output, "Coverage") > strings.Index(output, "Projection") ||
		strings.Index(output, "Notes Across Network") > strings.Index(output, "Projection") {
		t.Fatalf("notes output should show search, coverage, and network roots before projection internals:\n%s", output)
	}
	if strings.Contains(output, "archived-project") {
		t.Fatalf("notes default output exposed inactive root:\n%s", output)
	}

	rawOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenNotes, State: result.State.ToggleRawDetails(), Width: 120, Height: 60})
	if !strings.Contains(rawOutput, "archived-project / notes") || !strings.Contains(rawOutput, "status=disabled") {
		t.Fatalf("notes raw output missing inactive root:\n%s", rawOutput)
	}
	if !strings.Contains(rawOutput, "index queued=1 processing=0") {
		t.Fatalf("notes raw output should keep local attention counters:\n%s", rawOutput)
	}
	if !strings.Contains(rawOutput, "extraction too_large=1") {
		t.Fatalf("notes raw output should expose local extraction attention counters:\n%s", rawOutput)
	}
}

func TestNotesSelectableItemsExposeRootInspectActions(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenNotes, Snapshot{})
	items := contentSelectableItems(ScreenRecordItems(result.State))
	if got := len(items); got != 2 {
		t.Fatalf("notes selectable items = %d, want 2", got)
	}
	action, ok := PortalActionFromSelectableItem(items[0])
	if !ok || action.Executor.Kind != PortalExecutorRecordInspect || action.Domain != "notes" {
		t.Fatalf("notes selectable action = %#v ok=%t", action, ok)
	}
	rawItems := ScreenRecordItems(result.State.ToggleRawDetails())
	if got := len(rawItems); got != 3 {
		t.Fatalf("notes raw selectable items = %d, want 3", got)
	}
}

func TestNotesEmbeddingToggleIsGlobalTopAction(t *testing.T) {
	client := newFakePortalClient()
	client.notesEmbeddingStatus = fakeNotesEmbeddingStatus(true)
	result := LoadScreen(context.Background(), client, "corr_test", ScreenNotes, Snapshot{})
	action := PortalAction{}
	for _, item := range ScreenSelectableItems(result.State) {
		for _, related := range item.RelatedActions {
			if related.ID == "notes.embeddings.disable" {
				action = related
				break
			}
		}
	}
	if action.ID == "" {
		t.Fatalf("notes embedding action missing from sectioned actions: %#v", ScreenSelectableItems(result.State))
	}
	if action.Executor.Kind != PortalExecutorNotesEmbeddingsToggle || action.Executor.Payload["enabled"] != "false" || action.TargetKind != "notes_embeddings" {
		t.Fatalf("unexpected notes embedding action: %#v", action)
	}
	for _, item := range contentSelectableItems(ScreenRecordItems(result.State)) {
		for _, related := range item.RelatedActions {
			if related.Executor.Kind == PortalExecutorNotesEmbeddingsToggle {
				t.Fatalf("embedding toggle should not be attached to record %s: %#v", item.RecordKind, related)
			}
		}
	}
}

func TestNotesSearchInputParsesFilters(t *testing.T) {
	input := notesSearchInputFromQuery("project:osint-tools node:main tag:osint path:reports class:markdown root:notes after:2026-01-01 before:2026-02-01 sort:newest threat intel")
	if input.Query != "threat intel" {
		t.Fatalf("query = %q, want threat intel", input.Query)
	}
	if input.ProjectRef != "osint-tools" || input.SourceNodeKey != "main" || input.Path != "reports" || input.FileClass != "markdown" || input.RootRef != "notes" {
		t.Fatalf("unexpected filters: %#v", input)
	}
	if len(input.Tags) != 1 || input.Tags[0] != "osint" {
		t.Fatalf("tags = %#v, want osint", input.Tags)
	}
	if input.After != "2026-01-01" || input.Before != "2026-02-01" || input.Sort != knowledge.NotesSearchSortNewest {
		t.Fatalf("absolute-time filters = %#v", input)
	}
}

func TestRenderNotesSearchResultsEmptyAndFailureStates(t *testing.T) {
	state := ScreenState{Screen: ScreenNotes, Status: ScreenLoadLoaded}
	state.Data.Notes.Search = NotesSearchData{
		Query:     "missing",
		Input:     knowledge.NotesSearchInput{Query: "missing", Limit: 10},
		ResultSet: knowledge.NotesSearchResultSet{Query: "missing", ResultCount: 0},
		Status:    ScreenLoadLoaded,
	}
	emptyOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenNotes, State: state, Width: 120, Height: 40})
	if !strings.Contains(emptyOutput, "No indexed notes matched the search.") {
		t.Fatalf("empty notes search output missing empty state:\n%s", emptyOutput)
	}

	state.Data.Notes.Search = NotesSearchData{Query: "broken", Status: ScreenLoadFailed, Error: "notes unavailable"}
	failedOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenNotes, State: state, Width: 120, Height: 40})
	if !strings.Contains(failedOutput, "notes unavailable") {
		t.Fatalf("failed notes search output missing error:\n%s", failedOutput)
	}
}

func TestNotesSearchCommandLoadsResultsAndFilters(t *testing.T) {
	client := newFakePortalClient()
	msg := notesSearchCmd(client, "corr_test", "project:osint-tools node:main tag:osint path:reports threat intel")()
	loaded, ok := msg.(notesSearchLoadedMsg)
	if !ok {
		t.Fatalf("message = %T, want notesSearchLoadedMsg", msg)
	}
	if loaded.Err != nil {
		t.Fatalf("notes search returned error: %v", loaded.Err)
	}
	if client.notesSearchInput.Query != "threat intel" || client.notesSearchInput.ProjectRef != "osint-tools" || client.notesSearchInput.SourceNodeKey != "main" || client.notesSearchInput.Path != "reports" {
		t.Fatalf("unexpected notes search input: %#v", client.notesSearchInput)
	}
	if len(client.notesSearchInput.Tags) != 1 || client.notesSearchInput.Tags[0] != "osint" {
		t.Fatalf("notes search tags = %#v, want osint", client.notesSearchInput.Tags)
	}
	if got := loaded.ResultSet.ResultCount; got != 1 {
		t.Fatalf("result count = %d, want 1", got)
	}
}

func TestRenderNotesScreenShowsSearchResultsAndInspectActions(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenNotes, Snapshot{})
	result.State.Data.Notes.Search = fakeNotesSearchResultSet("threat intel")
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenNotes, State: result.State, Width: 120, Height: 60})
	for _, want := range []string{"Search notes... last=\"threat intel\" results=1", "query=\"threat intel\" results=1 mode=hybrid", "Threat Intel Runbook", "match=title,body,semantic", "date=2026-07-03", "node=main", "project=project_osint", "source=embedded_text", "extraction=extracted", "Projects/osint-tools/notes/threat-intel.md > summary", "citation=Projects/osint-tools/notes/threat-intel.md#summary"} {
		if !strings.Contains(output, want) {
			t.Fatalf("notes search output missing %q:\n%s", want, output)
		}
	}
	rawOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenNotes, State: result.State.ToggleRawDetails(), Width: 140, Height: 80})
	for _, want := range []string{"source_created=2026-07-01T08:00:00Z", "source_modified=2026-07-03T09:30:00Z", "recency=2026-07-03T09:30:00Z", "recency_basis=source_filesystem_mtime", "observed=2026-07-04T11:00:00Z", "recency:0.9400", "recency_rank=1", "indexed=2026-07-04T12:30:00Z", "lexical_rank=1", "semantic_rank=2", "semantic_distance=0.1800", "semantic_score=0.8200"} {
		if !strings.Contains(rawOutput, want) {
			t.Fatalf("raw notes search output missing %q:\n%s", want, rawOutput)
		}
	}
	items := ScreenRecordItems(result.State)
	resultItem := findSelectableRecord(items, "notes_search_result")
	action, ok := PortalActionFromSelectableItem(resultItem)
	if !ok || action.Executor.Kind != PortalExecutorRecordInspect || action.Domain != "notes" || action.TargetKind != "notes_search_result" {
		t.Fatalf("notes search result action = %#v ok=%t", action, ok)
	}
	if action.Executor.Payload["text_source"] != knowledge.TextSourceEmbeddedText || action.Executor.Payload["extraction_status"] != knowledge.ExtractionStatusExtracted || action.Executor.Payload["recency_basis"] != knowledge.AbsoluteTimeBasisSourceFilesystemMtime || action.Executor.Payload["recency_rank"] != "1" {
		t.Fatalf("notes search inspect payload missing provenance: %#v", action.Executor.Payload)
	}
}

func TestRenderNotesSearchShowsModeFallback(t *testing.T) {
	data := fakeNotesSearchResultSet("semantic fallback")
	data.ResultSet.Mode = knowledge.NotesSearchModeLexical
	data.ResultSet.RequestedMode = knowledge.NotesSearchModeHybrid
	data.ResultSet.FallbackReason = "embeddings_disabled"
	output := RenderNotesSearchWithSelection(testMode(), data.Query, data, 0, 120, 40)
	for _, want := range []string{"query=\"semantic fallback\" results=1 mode=lexical requested=hybrid fallback=embeddings_disabled", "Threat Intel Runbook"} {
		if !strings.Contains(output, want) {
			t.Fatalf("notes search fallback output missing %q:\n%s", want, output)
		}
	}
}

func TestLoadScreenDatabaseLoadsObjectsAndSyncData(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenDatabase, Snapshot{})
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("database screen status = %s, partials=%v", result.State.Status, result.State.PartialErrors)
	}
	if got := len(result.State.Data.Database.Objects); got != 2 {
		t.Fatalf("objects = %d, want 2", got)
	}
	if got := len(result.State.Data.Database.SyncBatches); got != 1 {
		t.Fatalf("sync batches = %d, want 1", got)
	}
	if got := len(result.State.Data.Database.IndexFailures); got != 1 {
		t.Fatalf("index failures = %d, want 1", got)
	}
	if client.syncStatusFilter.NodeRef != "main" {
		t.Fatalf("sync status node ref = %q, want main", client.syncStatusFilter.NodeRef)
	}
}

func TestRunExitAfterRenderDatabaseSearch(t *testing.T) {
	client := newFakePortalClient()
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Mode:                testMode(),
		Client:              client,
		CorrelationID:       "corr_test",
		Out:                 &out,
		DatabaseSearchQuery: "portal",
		ExitAfterRender:     true,
		Registry:            actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.searchInput.Query != "portal" {
		t.Fatalf("search query = %q, want portal", client.searchInput.Query)
	}
	if output := out.String(); !strings.Contains(output, "Object Diagnostics Search") || !strings.Contains(output, "Portal Test Object") {
		t.Fatalf("database search output missing live result:\n%s", output)
	}
}

func TestExecuteObjectInspectActionLoadsDetailVersionsAndIndex(t *testing.T) {
	client := newFakePortalClient()
	action := NewObjectInspectAction(objects.Object{ObjectID: "object_test", Name: "Portal Test Object"})
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded {
		t.Fatalf("status = %s, want succeeded", result.Status)
	}
	if client.objectInspectRef != "object_test" || client.objectVersionsRef != "object_test" {
		t.Fatalf("object refs inspect=%q versions=%q", client.objectInspectRef, client.objectVersionsRef)
	}
	output := RenderPortalActionResult(testMode(), ActionPanelState{Lifecycle: result.Status, Action: action, Result: result})
	for _, want := range []string{"Object detail loaded", "Version Count", "Index Statuses"} {
		if !strings.Contains(output, want) {
			t.Fatalf("object action output missing %q:\n%s", want, output)
		}
	}
}

func TestObjectInspectActionToleratesIndexExplainPartial(t *testing.T) {
	client := newFakePortalClient()
	client.indexExplainErr = errors.New("index explain unavailable")
	action := NewObjectInspectAction(objects.Object{ObjectID: "object_test", Name: "Portal Test Object"})
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded {
		t.Fatalf("status = %s, want succeeded", result.Status)
	}
	if result.RawResponse == "" {
		t.Fatal("expected partial index error in raw response")
	}
	output := RenderPortalActionResult(testMode(), ActionPanelState{Lifecycle: result.Status, Action: action, Result: result, RawDetails: true})
	if !strings.Contains(output, "Index State: unavailable") {
		t.Fatalf("expected partial index state field:\n%s", output)
	}
}

func TestDatabaseAvailableActionsIncludeIndexerAndRetryFailed(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenDatabase, Snapshot{})
	available := ScreenAvailableActions(result.State)
	ids := map[string]bool{}
	for _, action := range available {
		ids[action.ID] = true
	}
	if !ids["index.retry_failed"] {
		t.Fatalf("database actions missing index.retry_failed: %#v", available)
	}
	if !ids["worker.indexer_text.run_once"] {
		t.Fatalf("database actions missing worker.indexer_text.run_once: %#v", available)
	}
}

func TestDatabasePortalDecluttersDefaultAndKeepsRawDetails(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenDatabase, Snapshot{})
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenDatabase, State: result.State, Width: 120, Height: 40})
	for _, want := range []string{"Diagnostics Attention", "index_status_test", "Diagnostics Summary", "objects=2", "user_objects=1", "Recent Object Changes", "Portal Test Object", "Indexed Object Summary", "Diagnostics for object metadata"} {
		if !strings.Contains(output, want) {
			t.Fatalf("database output missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "Diagnostics Attention") > strings.Index(output, "Diagnostics Summary") ||
		strings.Index(output, "Diagnostics Summary") > strings.Index(output, "Recent Object Changes") ||
		strings.Index(output, "Recent Object Changes") > strings.Index(output, "Indexed Object Summary") {
		t.Fatalf("database default output is not problem-first:\n%s", output)
	}
	for _, notWant := range []string{".loom-acceptance", "sync_batch_test", "private_backup_test"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("database default output exposed raw/dev record %q:\n%s", notWant, output)
		}
	}

	rawState := result.State.ToggleRawDetails()
	rawOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenDatabase, State: rawState, Width: 120, Height: 60})
	for _, want := range []string{"Diagnostics", "Raw Objects", ".loom-acceptance", "Raw Index Failures", "index_status_test", "Sync Batches", "sync_batch_test", "Private Backups", "private_backup_test", "Deletion Requests", "deletion_request_test"} {
		if !strings.Contains(rawOutput, want) {
			t.Fatalf("database raw output missing %q:\n%s", want, rawOutput)
		}
	}
}

func TestDatabasePortalScopedSearchHidesRawRecordsByDefault(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenDatabase, Snapshot{})
	results := SearchPortal(actions.DefaultRegistry(), result.State, "#database portal", 10)
	if !searchResultsContainTitle(results, "Portal Test Object") {
		t.Fatalf("#database search missing user object: %#v", results)
	}
	for _, notWant := range []string{".loom-acceptance smoke object", "sync_batch_test", "private_backup_test", "deletion_request_test"} {
		if searchResultsContainTitle(results, notWant) {
			t.Fatalf("#database default search exposed raw/dev record %q: %#v", notWant, results)
		}
	}
	indexResults := SearchPortal(actions.DefaultRegistry(), result.State, "#indexes object", 10)
	if !searchResultsContainCategory(indexResults, "index") && !searchResultsContainTitle(indexResults, "Portal Test Object") {
		t.Fatalf("#indexes should expose grouped index health, got %#v", indexResults)
	}
}

func TestPortalSearchRanksNotesAboveDiagnosticsForNotes(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenHome, fakeSnapshot())
	results := SearchPortal(actions.DefaultRegistry(), state, "notes", 10)
	if len(results) == 0 {
		t.Fatal("expected portal search results for notes")
	}
	if results[0].Action.Executor.Target != ScreenNotes && results[0].Title != "LOOM Notes" {
		t.Fatalf("notes search should prefer LOOM Notes, got %#v", results[0])
	}
}

func TestPortalSearchKeepsDiagnosticsDiscoverable(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenHome, fakeSnapshot())
	for _, query := range []string{"database", "object", "index failure"} {
		results := SearchPortal(actions.DefaultRegistry(), state, query, 10)
		if !searchResultsContainTitle(results, "Object Store Diagnostics") {
			t.Fatalf("%q search should find diagnostics, got %#v", query, results)
		}
	}
}

func TestDatabaseSyncRecordInspectResult(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenDatabase, Snapshot{})
	result.State = result.State.ToggleRawDetails()
	var action PortalAction
	for _, candidate := range ScreenActions(result.State) {
		if strings.HasPrefix(candidate.ID, "database.sync_batch.") {
			action = candidate
			break
		}
	}
	if action.ID == "" {
		t.Fatal("missing sync batch inspect action")
	}
	actionResult, err := ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction returned error: %v", err)
	}
	if actionResult.Summary != "Database record detail loaded." {
		t.Fatalf("summary = %q", actionResult.Summary)
	}
}

func TestDatabaseSearchResultActionUsesSearchDocumentID(t *testing.T) {
	action := NewSearchResultInspectAction(search.SearchResult{
		SearchDocumentID: "search_document_test",
		ObjectID:         "object_test",
		Title:            "Portal Test Object",
	})
	if !strings.Contains(action.ID, "search_document_test") {
		t.Fatalf("action id should use search document id, got %s", action.ID)
	}
	if action.TargetRef != "object_test" {
		t.Fatalf("target ref = %q, want object_test", action.TargetRef)
	}
}

func TestRunExitAfterRenderSearchAndPreviewHooks(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "search",
			opts: Options{SearchQuery: "watched root"},
			want: "Nodes And Watched Roots",
		},
		{
			name: "preview",
			opts: Options{PreviewActionID: "raw.workers.list"},
			want: "Raw: loom workers list",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			opts := tc.opts
			opts.Mode = testMode()
			opts.Client = newFakePortalClient()
			opts.CorrelationID = "corr_test"
			opts.Out = &out
			opts.ExitAfterRender = true
			opts.Registry = actions.DefaultRegistry()
			if err := Run(context.Background(), opts); err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, out.String())
			}
		})
	}
}

func TestRunExitAfterRenderSearchAndPreviewDoNotNeedClient(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "search",
			opts: Options{SearchQuery: "direct event"},
			want: "Automation Center",
		},
		{
			name: "preview",
			opts: Options{PreviewActionID: "raw.workers.list"},
			want: "Raw: loom workers list",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			opts := tc.opts
			opts.Mode = testMode()
			opts.Out = &out
			opts.ExitAfterRender = true
			opts.Registry = actions.DefaultRegistry()
			if err := Run(context.Background(), opts); err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("output missing %q:\n%s", tc.want, out.String())
			}
		})
	}
}

func TestExecuteActionRequiresConfirmationAndRunsThroughClient(t *testing.T) {
	client := newFakePortalClient()
	_, err := ExecuteAction(context.Background(), client, "corr_test", actions.DefaultRegistry(), "worker.selfcheck.run_once", false)
	if !errors.Is(err, ErrActionConfirmationRequired) {
		t.Fatalf("error = %v, want confirmation requirement", err)
	}

	result, err := ExecuteAction(context.Background(), client, "corr_test", actions.DefaultRegistry(), "worker.selfcheck.run_once", true)
	if err != nil {
		t.Fatalf("ExecuteAction returned error: %v", err)
	}
	if client.runWorkerRef != "main.worker_selfcheck" {
		t.Fatalf("worker ref = %q, want main.worker_selfcheck", client.runWorkerRef)
	}
	if result.WorkerRunID != "worker_run_test" {
		t.Fatalf("worker run = %q, want worker_run_test", result.WorkerRunID)
	}
	if client.runInput.IdempotencyKey == "" {
		t.Fatal("expected portal action to provide idempotency key")
	}
}

func TestPortalActionFromRegistryMapsRiskAndExecutor(t *testing.T) {
	action, ok := actions.DefaultRegistry().Get("worker.indexer_text.run_once")
	if !ok {
		t.Fatal("missing action")
	}
	portalAction := PortalActionFromRegistry(action)
	if portalAction.Risk != ActionRiskSafeRun {
		t.Fatalf("risk = %s, want %s", portalAction.Risk, ActionRiskSafeRun)
	}
	if portalAction.Executor.Kind != PortalExecutorWorkerRunOnce || portalAction.Executor.Target != "main.indexer_text" {
		t.Fatalf("executor = %#v", portalAction.Executor)
	}
	if portalAction.TargetKind != "worker" || portalAction.TargetRef != "main.indexer_text" {
		t.Fatalf("target = %s %s", portalAction.TargetKind, portalAction.TargetRef)
	}
	if !portalAction.RequiresConfirmation() {
		t.Fatal("safe run action should require confirmation")
	}

	openAction, ok := actions.DefaultRegistry().Get("background.open")
	if !ok {
		t.Fatal("missing screen action")
	}
	openPortalAction := PortalActionFromRegistry(openAction)
	if openPortalAction.Risk != ActionRiskInspect {
		t.Fatalf("open risk = %s, want inspect", openPortalAction.Risk)
	}
	if openPortalAction.Executor.Kind != PortalExecutorNavigate || openPortalAction.Executor.Target != ScreenBackground {
		t.Fatalf("open executor = %#v", openPortalAction.Executor)
	}
	if openPortalAction.RequiresConfirmation() {
		t.Fatal("inspect action should not require confirmation")
	}
}

func TestPortalActionInputValidation(t *testing.T) {
	action := PortalAction{
		ID:    "test.action",
		Label: "Test Action",
		InputFields: []PortalActionField{{
			Name:     "reason",
			Label:    "Reason",
			Kind:     ActionFieldText,
			Required: true,
		}},
		InputValues: map[string]string{},
	}
	if action.InputValid() {
		t.Fatal("expected missing required input to be invalid")
	}
	fields := action.ValidateInput()
	if len(fields) != 1 || fields[0].Error == "" {
		t.Fatalf("expected field error: %#v", fields)
	}

	action.InputValues["reason"] = "manual run"
	if !action.InputValid() {
		t.Fatal("expected required input to be valid after value is provided")
	}
}

func TestActionInventoryClassifiesGuidedBoxActions(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBox, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	inventory := ActionInventoryForScreen(result.State)
	assertInventoryInteraction(t, inventory, "box.status.inspect", ActionInteractionDirectInspect)
	assertInventoryInteraction(t, inventory, "box.project.scaffold", ActionInteractionFormRun)
	assertInventoryInteraction(t, inventory, "box.watch_policy.apply", ActionInteractionFormRun)
}

func TestProjectsScreenUsesTargetAwareCreateProjectFormWhenBoxIsLoaded(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	client := newFakePortalClient()
	client.noProjects = true
	result := LoadScreen(context.Background(), client, "corr_test", ScreenProjects, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	action, ok := findPortalAction(ScreenAvailableActions(result.State), "project.scaffold.backend")
	if !ok {
		t.Fatalf("projects screen missing main create-project action")
	}
	if action.Interaction() != ActionInteractionFormRun || action.Executor.Kind != PortalExecutorProjectScaffoldBackend {
		t.Fatalf("create-project action not wired as backend form scaffold: %#v", action)
	}
	for _, field := range []string{"project_name", "target_node", "preset", "facets", "dry_run", "register_after_create"} {
		if !hasPortalActionInputField(action, field) {
			t.Fatalf("create-project action missing input field %q: %#v", field, action.InputFields)
		}
	}
	targetField, ok := portalActionInputField(action, "target_node")
	if !ok || targetField.Kind != ActionFieldSelect || !containsString(targetField.Options, "main") || !containsString(targetField.Options, "macbook") {
		t.Fatalf("create-project target node should be a select with main and local options: %#v", targetField)
	}
	if _, ok := findPortalAction(ScreenAvailableActions(result.State), "box.project.scaffold"); ok {
		t.Fatalf("projects screen should not expose local Box project scaffold action")
	}
}

func TestProjectsScreenCanCreateProjectThroughSelectedNode(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenProjects, Snapshot{}, SnapshotOptions{})
	action, ok := findPortalAction(ScreenAvailableActions(result.State), "project.scaffold.backend")
	if !ok {
		t.Fatalf("projects screen missing backend project scaffold action")
	}
	if action.Label != "Create Project" || action.TargetLabel != "select node" {
		t.Fatalf("create-project action should read as a target-aware project flow: %#v", action)
	}
	if action.Interaction() != ActionInteractionFormRun || action.Executor.Kind != PortalExecutorProjectScaffoldBackend {
		t.Fatalf("backend create-project action not wired as form scaffold: %#v", action)
	}
	action.InputValues["project_name"] = "Remote Main Project"
	action.InputValues["target_node"] = "main"
	action.InputValues["preset"] = projectcontracts.PresetAutomation
	action.InputValues["facets"] = "notes,scripts"
	outcome, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("execute backend scaffold returned error: %v", err)
	}
	if outcome.Status != ActionLifecycleSucceeded ||
		!strings.Contains(outcome.Summary, "created and registered on main under loom-box/Projects") ||
		!hasActionField(outcome.Fields, "Target Node", "main") ||
		!hasActionField(outcome.Fields, "Capability Prefix", "main@remote-main-project") ||
		!hasActionField(outcome.Fields, "Project Path", "/home/loomadmin/loom-box/Projects/remote-main-project") ||
		!hasActionField(outcome.Fields, "Preset", projectcontracts.PresetAutomation) ||
		!hasActionField(outcome.Fields, "Facets", "notes, scripts") ||
		!hasActionField(outcome.Fields, "Registration", "registered") ||
		!hasActionField(outcome.Fields, "Revision", "1") ||
		!hasActionField(outcome.Fields, "Registered Root", "/home/loomadmin/loom-box/Projects/remote-main-project") ||
		!hasActionFieldLabel(outcome.Fields, "Next Steps") {
		t.Fatalf("unexpected backend scaffold outcome: %#v", outcome)
	}
	if client.scaffoldProjectInput.Directory != "" {
		t.Fatalf("backend scaffold action should let backend resolve Box directory, got %q", client.scaffoldProjectInput.Directory)
	}
	if client.scaffoldProjectInput.Preset != projectcontracts.PresetAutomation || client.scaffoldProjectInput.OwnerNode != "main" {
		t.Fatalf("unexpected backend scaffold input: %#v", client.scaffoldProjectInput)
	}
	if got := strings.Join(client.scaffoldProjectInput.Facets, ","); got != "notes,scripts" {
		t.Fatalf("unexpected backend scaffold facets: %#v", client.scaffoldProjectInput.Facets)
	}
	if client.registerBackendInput.ProjectRef != "remote-main-project" ||
		client.registerBackendInput.ProjectRoot != "/home/loomadmin/loom-box/Projects/remote-main-project" {
		t.Fatalf("backend scaffold should register generated contract, got %#v", client.registerBackendInput)
	}
}

func TestProjectsScreenCanCreateLocalBoxProjectFromSameAction(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenProjects, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	action, ok := findPortalAction(ScreenAvailableActions(result.State), "project.scaffold.backend")
	if !ok {
		t.Fatalf("projects screen missing create-project action")
	}
	action.InputValues["project_name"] = "Local Mac Project"
	action.InputValues["target_node"] = "macbook"
	action.InputValues["preset"] = projectcontracts.PresetResearch
	action.InputValues["facets"] = "notes,backup_policy"
	outcome, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("execute local scaffold returned error: %v", err)
	}
	if outcome.Status != ActionLifecycleSucceeded ||
		!strings.Contains(outcome.Summary, "macbook under loom-box/Projects") ||
		!hasActionField(outcome.Fields, "Target Node", "macbook") ||
		!hasActionField(outcome.Fields, "Capability Prefix", "macbook@local-mac-project") ||
		!hasActionField(outcome.Fields, "Project Path", filepath.Join(root, "Projects", "local-mac-project")) {
		t.Fatalf("unexpected local scaffold outcome: %#v", outcome)
	}
	if _, err := os.Stat(filepath.Join(root, "Projects", "local-mac-project", filepath.FromSlash(projectcontracts.CanonicalRootContractPath))); err != nil {
		t.Fatalf("local project scaffold was not written: %v", err)
	}
	if client.scaffoldProjectInput.Name != "" {
		t.Fatalf("local node scaffold should not call backend scaffold: %#v", client.scaffoldProjectInput)
	}
	if client.registerBackendInput.ProjectRef != "" || client.registerBackendInput.ProjectRoot != "" {
		t.Fatalf("local node scaffold should not register through backend: %#v", client.registerBackendInput)
	}
}

func TestProjectsScreenDryRunCreateDoesNotRegisterProject(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenProjects, Snapshot{}, SnapshotOptions{})
	action, ok := findPortalAction(ScreenAvailableActions(result.State), "project.scaffold.backend")
	if !ok {
		t.Fatalf("projects screen missing backend project scaffold action")
	}
	action.InputValues["project_name"] = "Dry Run Main Project"
	action.InputValues["target_node"] = "main"
	action.InputValues["dry_run"] = "true"
	outcome, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("execute dry-run backend scaffold returned error: %v", err)
	}
	if outcome.Status != ActionLifecycleSucceeded ||
		!strings.Contains(outcome.Summary, "planned on main under loom-box/Projects") ||
		!hasActionField(outcome.Fields, "Registration", "skipped_dry_run") {
		t.Fatalf("unexpected dry-run backend scaffold outcome: %#v", outcome)
	}
	if client.registerBackendInput.ProjectRef != "" || client.registerBackendInput.ProjectRoot != "" {
		t.Fatalf("dry-run scaffold should not register through backend: %#v", client.registerBackendInput)
	}
}

func TestProjectScaffoldCleanupPortalAction(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Portal Cleanup",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"scripts"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{
			ProjectID: "proj_portal_cleanup",
			Slug:      scaffold.Slug,
			Name:      scaffold.Name,
			Status:    "active",
		}},
		Registration: &projects.ProjectContractRegistration{ProjectRoot: scaffold.ProjectRoot},
	}
	action := NewProjectScaffoldCleanupAction(detail, ScreenProjects)
	action.InputValues["dry_run"] = "true"
	outcome, err := ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", action, true)
	if err != nil {
		t.Fatalf("execute cleanup dry-run returned error: %v", err)
	}
	if outcome.Status != ActionLifecycleSucceeded || !hasActionField(outcome.Fields, "Dry Run", "true") || !hasActionField(outcome.Fields, "Planned Remove", "1") {
		t.Fatalf("unexpected cleanup dry-run outcome: %#v", outcome)
	}
	if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, "scripts", "hello_world")); err != nil {
		t.Fatalf("dry-run should leave scaffold package in place: %v", err)
	}

	action.InputValues["dry_run"] = "false"
	outcome, err = ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", action, true)
	if err != nil {
		t.Fatalf("execute cleanup returned error: %v", err)
	}
	if outcome.Status != ActionLifecycleSucceeded || !hasActionField(outcome.Fields, "Removed", "1") {
		t.Fatalf("unexpected cleanup outcome: %#v", outcome)
	}
	if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, "scripts", "hello_world")); !os.IsNotExist(err) {
		t.Fatalf("expected scaffold package to be removed, stat err=%v", err)
	}
}

func TestRenderPortalActionPanels(t *testing.T) {
	action, ok := actions.DefaultRegistry().Get("worker.selfcheck.run_once")
	if !ok {
		t.Fatal("missing action")
	}
	portalAction := PortalActionFromRegistry(action)
	preview := RenderPortalActionPreview(testMode(), ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: portalAction})
	for _, want := range []string{"Action Preview", "Run Worker Selfcheck Once", "Interaction: direct run", "Risk: safe_run", "Confirmation: required"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview missing %q:\n%s", want, preview)
		}
	}
	if strings.Contains(preview, "Raw Details") {
		t.Fatalf("interactive preview should hide raw details by default:\n%s", preview)
	}

	confirmation := RenderPortalActionConfirmation(testMode(), ActionPanelState{Lifecycle: ActionLifecycleNeedsConfirmation, Action: portalAction})
	if !strings.Contains(confirmation, "Confirm Action") || !strings.Contains(confirmation, "Enter confirms") {
		t.Fatalf("unexpected confirmation:\n%s", confirmation)
	}

	result := RenderPortalActionResult(testMode(), ActionPanelState{
		Lifecycle: ActionLifecycleFailed,
		Action:    portalAction,
		Result: PortalActionResult{
			Title:        portalAction.Label,
			Status:       ActionLifecycleFailed,
			Summary:      "Action failed.",
			ErrorCode:    "portal.action_failed",
			ErrorMessage: "backend unavailable",
			RawCommand:   portalAction.RawCommand,
		},
	})
	if !strings.Contains(result, "Action Failed") || !strings.Contains(result, "backend unavailable") {
		t.Fatalf("unexpected failed result:\n%s", result)
	}
}

func assertInventoryInteraction(t *testing.T, inventory []PortalActionInventoryItem, actionID string, want PortalActionInteraction) {
	t.Helper()
	for _, item := range inventory {
		if item.ActionID == actionID {
			if item.Interaction != want {
				t.Fatalf("inventory action %s interaction = %s, want %s; item=%#v", actionID, item.Interaction, want, item)
			}
			return
		}
	}
	t.Fatalf("inventory missing action %s: %#v", actionID, inventory)
}

func TestRenderSearchDoesNotShowRawCommandByDefault(t *testing.T) {
	output := RenderSearch(testMode(), "workers", actions.DefaultRegistry().Search("workers", 4))
	if !strings.Contains(output, "Background Operations") {
		t.Fatalf("search missing expected result:\n%s", output)
	}
	if strings.Contains(output, "Raw: loom") {
		t.Fatalf("search should not render raw commands by default:\n%s", output)
	}
}

func TestPortalStylesStatusAndLogoPrimitives(t *testing.T) {
	colorStyles := NewPortalStyles(colorMode())
	if rendered := colorStyles.Title.Render("LOOM"); !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected color title to contain ANSI, got %q", rendered)
	}
	plainStyles := NewPortalStyles(testMode())
	if rendered := plainStyles.Title.Render("LOOM"); strings.Contains(rendered, "\x1b[") {
		t.Fatalf("plain title should not contain ANSI, got %q", rendered)
	}
	for status, want := range map[string]statusClass{
		"healthy":    statusClassSuccess,
		"running":    statusClassProgress,
		"failed":     statusClassDanger,
		"conflicted": statusClassDanger,
		"disabled":   statusClassMuted,
	} {
		if got := classifyStatus(status); got != want {
			t.Fatalf("classifyStatus(%q) = %s, want %s", status, got, want)
		}
	}
	logo := SelectPortalLogo(40, rand.New(rand.NewSource(1)))
	if !logo.Fits(40) {
		t.Fatalf("selected logo does not fit width 40: %s width=%d", logo.ID, logo.MaxWidth())
	}
	if got := SelectPortalLogo(3, rand.New(rand.NewSource(1))); got.ID != "compact" {
		t.Fatalf("narrow logo = %s, want compact", got.ID)
	}
}

func TestModelBootAnimationCanAdvanceAndSkip(t *testing.T) {
	model := NewModelWithOptions(ModelOptions{
		Mode:        colorMode(),
		Snapshot:    fakeSnapshot(),
		Registry:    actions.DefaultRegistry(),
		StartScreen: ScreenHome,
	})
	if !model.boot.Active {
		t.Fatal("expected boot to be active in animated portal mode")
	}
	if cmd := model.Init(); cmd == nil {
		t.Fatal("expected boot init command")
	}
	view := model.View()
	if !strings.Contains(view, "enter skip") || strings.Contains(view, "Attention") {
		t.Fatalf("boot view should show only boot content:\n%s", view)
	}
	updated, _ := model.Update(bootTickMsg{At: model.boot.StartedAt.Add(model.boot.FrameDelay)})
	model = updated.(Model)
	if model.boot.VisibleLines == 0 {
		t.Fatal("expected boot tick to reveal logo lines")
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("boot skip should not return command")
	}
	if model.boot.Active {
		t.Fatal("expected enter to complete boot")
	}

	model = NewModelWithOptions(ModelOptions{
		Mode:            colorMode(),
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})
	if model.boot.Active {
		t.Fatal("no boot animation option should disable boot")
	}
}

func TestHomeEnterUsesGroupedNavigationOrder(t *testing.T) {
	screens := HomeNavigationScreens()
	for index, screen := range screens {
		t.Run(screen.ID, func(t *testing.T) {
			model := NewModelWithOptions(ModelOptions{
				Mode:            testMode(),
				Snapshot:        fakeSnapshot(),
				Registry:        actions.DefaultRegistry(),
				StartScreen:     ScreenHome,
				NoBootAnimation: true,
			})
			if got := model.currentSelectableCount(); got != len(screens) {
				t.Fatalf("home selectable count = %d, want %d", got, len(screens))
			}
			state := model.currentScreenState()
			state.SelectedIndex = index
			model.setScreenState(ScreenHome, state)
			updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = updated.(Model)
			if cmd != nil {
				t.Fatal("home navigation without client should not load asynchronously")
			}
			if model.screen != screen.ID {
				t.Fatalf("home navigation index %d opened %q, want %q", index, model.screen, screen.ID)
			}
		})
	}
}

func TestPortalSearchIsCategorizedAndHidesRawEntries(t *testing.T) {
	registry := actions.DefaultRegistry()
	results := SearchPortalActions(registry, "status", 20, false)
	for _, result := range results {
		if strings.HasPrefix(result.Action.ID, "raw.") || result.Category == "raw" {
			t.Fatalf("raw result should be hidden by default: %#v", result)
		}
	}
	rawResults := SearchPortalActions(registry, "status", 20, true)
	foundRaw := false
	for _, result := range rawResults {
		if result.Action.ID == "raw.status" {
			foundRaw = true
			break
		}
	}
	if !foundRaw {
		t.Fatalf("expected raw.status when raw results are included: %#v", rawResults)
	}

	output := RenderPortalSearch(testMode(), "background", SearchPortalActions(registry, "background", 8, false))
	for _, want := range []string{"Command Palette", "+", "Search", "[screen]", "Background Operations"} {
		if !strings.Contains(output, want) {
			t.Fatalf("search output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Show Raw Status") || strings.Contains(output, "Raw: loom") {
		t.Fatalf("search output should hide raw entries and commands:\n%s", output)
	}
}

func TestPortalSearchUsesScopedLiveRecords(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenCapabilities, Snapshot{})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned unexpected error: %v", result.Err)
	}

	defaultResults := SearchPortal(actions.DefaultRegistry(), result.State, "read system", 20)
	for _, result := range defaultResults {
		if result.Category == "capability" {
			t.Fatalf("default search should not return capability records: %#v", defaultResults)
		}
	}

	results := SearchPortal(actions.DefaultRegistry(), result.State, "#capabilities main@system.status", 20)
	foundCapability := false
	for _, result := range results {
		if result.Category == "capability" && result.Title == "main@system.status.read" && result.Action.Executor.Kind == PortalExecutorCapabilityInspect {
			foundCapability = true
			break
		}
	}
	if !foundCapability {
		t.Fatalf("live capability record missing from search: %#v", results)
	}

	results = SearchPortal(actions.DefaultRegistry(), result.State, "#capabilities main@", 20)
	foundProvider := false
	for _, result := range results {
		if result.Category == "provider" && result.Title == "main@system" && result.Action.Executor.Kind == PortalExecutorProviderInspect {
			foundProvider = true
			break
		}
	}
	if !foundProvider {
		t.Fatalf("live provider scoped result missing from search: %#v", results)
	}

	output := RenderPortalSearch(testMode(), "#capabilities main@", results)
	for _, want := range []string{"Capability Search", "[provider]", "main@system"} {
		if !strings.Contains(output, want) {
			t.Fatalf("dynamic search output missing %q:\n%s", want, output)
		}
	}
}

func TestPortalScopedSearchSuggestsScopesBeforeSpace(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenHome, fakeSnapshot())
	results := SearchPortal(actions.DefaultRegistry(), state, "#", 20)
	for _, want := range []string{"#doctor", "#capabilities", "#storage", "#database", "#workers", "#automation", "#jobs", "#nodes"} {
		found := false
		for _, result := range results {
			if result.Category == "scope" && result.Title == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("scope %s missing from # results: %#v", want, results)
		}
	}
	for _, legacy := range []string{"#objects", "#schedules", "#indexes", "#archives", "#transfers", "#cloud"} {
		for _, result := range results {
			if result.Category == "scope" && result.Title == legacy {
				t.Fatalf("legacy scope %s should be an alias, not a first-class result: %#v", legacy, results)
			}
		}
	}

	results = SearchPortal(actions.DefaultRegistry(), state, "#capa", 20)
	if len(results) != 1 || results[0].Title != "#capabilities" {
		t.Fatalf("#capa results = %#v, want only #capabilities", results)
	}

	results = SearchPortal(actions.DefaultRegistry(), state, "#capability", 20)
	if len(results) != 1 || results[0].Title != "#capabilities" {
		t.Fatalf("#capability results = %#v, want #capabilities scope suggestion until space", results)
	}

	results = SearchPortal(actions.DefaultRegistry(), state, "#arch", 20)
	if len(results) != 1 || results[0].Title != "#storage" || !strings.Contains(results[0].Description, "archives") {
		t.Fatalf("#arch results = %#v, want #storage alias with archive filter hint", results)
	}

	results = SearchPortal(actions.DefaultRegistry(), state, "#sched", 20)
	if len(results) != 1 || results[0].Title != "#automation" || !strings.Contains(results[0].Description, "schedules") {
		t.Fatalf("#sched results = %#v, want #automation alias with schedule filter hint", results)
	}
}

func TestCommandModeParserAliasesAndClassifier(t *testing.T) {
	parsed := ParseCommandInput(`$ loom inspect capability "main@system.status.read"`)
	if parsed.Error != "" {
		t.Fatalf("parse error = %s", parsed.Error)
	}
	if got := strings.Join(parsed.CanonicalTokens, " "); got != "capability inspect main@system.status.read" {
		t.Fatalf("canonical = %q", got)
	}
	if parsed.AliasApplied != "inspect capability" {
		t.Fatalf("alias = %q", parsed.AliasApplied)
	}

	jsonParsed := ParseCommandInput(`$ capability call main@system.status.read '{"include":true}'`)
	if jsonParsed.Error != "" {
		t.Fatalf("quoted JSON parse error = %s", jsonParsed.Error)
	}
	if got := jsonParsed.CanonicalTokens[len(jsonParsed.CanonicalTokens)-1]; got != `{"include":true}` {
		t.Fatalf("quoted JSON token = %q", got)
	}

	preview := BuildCommandPreview("$ worker run main.indexer_text --once")
	if preview.Classification != CommandClassSafeRun || !preview.RequiresConfirmation {
		t.Fatalf("preview = %#v", preview)
	}

	for _, input := range []string{"$ health | jq .", "$ health )"} {
		blocked := BuildCommandPreview(input)
		if blocked.ParseError == "" || !strings.Contains(blocked.ParseError, "Shell operators") {
			t.Fatalf("expected shell operator rejection for %q: %#v", input, blocked)
		}
	}

	dangerous := BuildCommandPreview("$ integration revoke gmail")
	if dangerous.Classification != CommandClassDangerous || dangerous.BlockedReason == "" {
		t.Fatalf("dangerous preview = %#v", dangerous)
	}
}

func TestRenderCommandPalettePreviewAndCompletions(t *testing.T) {
	state := CommandModeState{
		Active:  true,
		Input:   "$ list capabilities",
		Preview: BuildCommandPreview("$ list capabilities"),
		Suggestions: []CommandSuggestion{{
			Label:      "capabilities",
			Kind:       "command",
			InsertText: "capabilities",
		}},
		CompletionOpen: true,
	}
	output := RenderCommandPalette(testMode(), state, 100, 40)
	for _, want := range []string{"LOOM Command", "Canonical: loom capabilities list", "Classification: inspect", "Completions", "capabilities"} {
		if !strings.Contains(output, want) {
			t.Fatalf("command palette missing %q:\n%s", want, output)
		}
	}

	if got := applyCommandSuggestion("$ cap", CommandSuggestion{InsertText: "capabilities", ReplacementStart: 0, ReplacementEnd: len("$ cap")}); got != "$ capabilities" {
		t.Fatalf("alias/full replacement should preserve command prefix, got %q", got)
	}
}

func TestCommandCompletionSubcommandsAndLiveRefs(t *testing.T) {
	capabilitySuggestions := CompleteCommand(context.Background(), newFakePortalClient(), "corr_test", "$ cap", len("$ cap"))
	if !hasCommandSuggestion(capabilitySuggestions, "capabilities") {
		t.Fatalf("missing capabilities root suggestion: %#v", capabilitySuggestions)
	}

	subcommands := CompleteCommand(context.Background(), newFakePortalClient(), "corr_test", "$ capabilities ", len("$ capabilities "))
	if !hasCommandSuggestion(subcommands, "list") || !hasCommandSuggestion(subcommands, "search") {
		t.Fatalf("missing capabilities subcommands: %#v", subcommands)
	}

	flags := CompleteCommand(context.Background(), newFakePortalClient(), "corr_test", "$ worker run main.worker_selfcheck --", len("$ worker run main.worker_selfcheck --"))
	if !hasCommandSuggestion(flags, "--once") || !hasCommandSuggestion(flags, "--idempotency-key") {
		t.Fatalf("missing worker run flags: %#v", flags)
	}

	liveRefs := CompleteCommand(context.Background(), newFakePortalClient(), "corr_test", "$ capability inspect main@", len("$ capability inspect main@"))
	if !hasCommandSuggestion(liveRefs, "main@system.status.read") {
		t.Fatalf("missing live capability ref: %#v", liveRefs)
	}
}

func TestModelCommandModeOpensPreviewAndRequiresConfirmation(t *testing.T) {
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CommandRunner:   fakeCommandRunner{},
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})
	model.searchOpen = true
	model.searchText = "$ worker run main.indexer_text --once"

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("opening command preview should not execute")
	}
	model = updated.(Model)
	if model.commandPanel.Lifecycle != ActionLifecyclePreview {
		t.Fatalf("lifecycle = %s, want preview", model.commandPanel.Lifecycle)
	}
	if len(model.commandMode.History) != 1 || model.commandMode.History[0].Input != "$ worker run main.indexer_text --once" {
		t.Fatalf("preview should be recorded in session history: %#v", model.commandMode.History)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("safe command should need confirmation before command execution")
	}
	model = updated.(Model)
	if model.commandPanel.Lifecycle != ActionLifecycleNeedsConfirmation {
		t.Fatalf("lifecycle = %s, want confirmation", model.commandPanel.Lifecycle)
	}
}

func TestExecutePortalCommandRunsInspectAndBlocksUnsafePaths(t *testing.T) {
	runner := fakeCommandRunner{}
	preview := BuildCommandPreview("$ health")
	result, err := ExecutePortalCommand(context.Background(), runner, CommandRequest{
		Input:           preview.Input,
		CanonicalTokens: preview.CanonicalTokens,
		Classification:  preview.Classification,
		Confirmed:       false,
	})
	if err != nil {
		t.Fatalf("inspect command returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded || !strings.Contains(result.Stdout, "fake command output") {
		t.Fatalf("result = %#v", result)
	}

	safe := BuildCommandPreview("$ worker run main.indexer_text --once")
	_, err = ExecutePortalCommand(context.Background(), runner, CommandRequest{Input: safe.Input, CanonicalTokens: safe.CanonicalTokens, Classification: safe.Classification})
	if !errors.Is(err, ErrCommandConfirmationRequired) {
		t.Fatalf("error = %v, want command confirmation", err)
	}

	blocked := BuildCommandPreview("$ enter")
	result, err = ExecutePortalCommand(context.Background(), runner, CommandRequest{Input: blocked.Input, CanonicalTokens: blocked.CanonicalTokens, Classification: blocked.Classification})
	if err != nil {
		t.Fatalf("blocked command should render result, got err: %v", err)
	}
	if result.Status != ActionLifecycleFailed || !strings.Contains(result.ErrorMessage, "interactive portal") {
		t.Fatalf("blocked result = %#v", result)
	}
}

func TestRunExitAfterRenderCommandHooks(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Mode:                testMode(),
		Client:              newFakePortalClient(),
		CommandRunner:       fakeCommandRunner{},
		CorrelationID:       "corr_test",
		Out:                 &out,
		CommandPreviewInput: "$ list capabilities",
		ExitAfterRender:     true,
		Registry:            actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("command preview hook returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Command Preview") || !strings.Contains(out.String(), "loom capabilities list") {
		t.Fatalf("preview output missing command details:\n%s", out.String())
	}

	out.Reset()
	err = Run(context.Background(), Options{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CommandRunner:   fakeCommandRunner{},
		CorrelationID:   "corr_test",
		Out:             &out,
		CommandRunInput: "$ health",
		ExitAfterRender: true,
		Registry:        actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("command run hook returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Command Complete") || !strings.Contains(out.String(), "fake command output") {
		t.Fatalf("command run output missing result:\n%s", out.String())
	}

	out.Reset()
	err = Run(context.Background(), Options{
		Mode:                 testMode(),
		Client:               newFakePortalClient(),
		CommandRunner:        fakeCommandRunner{},
		CorrelationID:        "corr_test",
		Out:                  &out,
		CommandCompleteInput: "$ cap",
		ExitAfterRender:      true,
		Registry:             actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("command complete hook returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Completions") || !strings.Contains(out.String(), "capabilities") {
		t.Fatalf("completion output missing suggestions:\n%s", out.String())
	}

	out.Reset()
	err = Run(context.Background(), Options{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CommandRunner:   fakeCommandRunner{},
		CorrelationID:   "corr_test",
		Out:             &out,
		SearchQuery:     "$ list capabilities",
		ExitAfterRender: true,
		Registry:        actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("command search hook returned error: %v", err)
	}
	if !strings.Contains(out.String(), "LOOM Command") || !strings.Contains(out.String(), "Canonical: loom capabilities list") {
		t.Fatalf("command search output missing command palette:\n%s", out.String())
	}
}

func TestExecutePortalActionRequiresConfirmationForSafeRun(t *testing.T) {
	action, ok := actions.DefaultRegistry().Get("worker.selfcheck.run_once")
	if !ok {
		t.Fatal("missing action")
	}
	_, err := ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", PortalActionFromRegistry(action), false)
	if !errors.Is(err, ErrActionConfirmationRequired) {
		t.Fatalf("error = %v, want confirmation required", err)
	}
}

func TestExecutePortalActionRunsWorkerOnceThroughClient(t *testing.T) {
	client := newFakePortalClient()
	action, ok := actions.DefaultRegistry().Get("worker.selfcheck.run_once")
	if !ok {
		t.Fatal("missing action")
	}
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", PortalActionFromRegistry(action), true)
	if err != nil {
		t.Fatalf("ExecutePortalAction returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded {
		t.Fatalf("status = %s, want succeeded", result.Status)
	}
	if client.runWorkerRef != "main.worker_selfcheck" {
		t.Fatalf("worker ref = %q", client.runWorkerRef)
	}
	if resultFieldValue(result, "Run") != "worker_run_test" {
		t.Fatalf("run field = %q", resultFieldValue(result, "Run"))
	}
	if result.IdempotencyKey == "" {
		t.Fatal("expected idempotency key")
	}
}

func TestExecutePortalActionInspectsWorkerThroughClient(t *testing.T) {
	client := newFakePortalClient()
	action := NewWorkerInspectAction(workers.WorkerListItem{WorkerKey: "main.worker_selfcheck", WorkerKind: workers.KindSelfcheck, Enabled: true}, ScreenBackground)
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("ExecutePortalAction returned error: %v", err)
	}
	if client.inspectWorkerRef != "main.worker_selfcheck" {
		t.Fatalf("inspect ref = %q", client.inspectWorkerRef)
	}
	if result.Status != ActionLifecycleSucceeded || resultFieldValue(result, "Worker") != "main.worker_selfcheck" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecutePortalActionRunsIndexActionsThroughClient(t *testing.T) {
	client := newFakePortalClient()
	status := search.IndexStatus{IndexStatusID: "index_status_test", Status: "failed", ObjectID: "object_test", IndexType: "full_text"}

	retry, err := ExecutePortalAction(context.Background(), client, "corr_test", NewIndexRetryAction(status), true)
	if err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if client.retryIndexID != "index_status_test" || retry.Status != ActionLifecycleSucceeded {
		t.Fatalf("retry did not use selected index status: ref=%s result=%#v", client.retryIndexID, retry)
	}

	rebuild, err := ExecutePortalAction(context.Background(), client, "corr_test", NewIndexRebuildObjectAction(status), true)
	if err != nil {
		t.Fatalf("rebuild returned error: %v", err)
	}
	if client.rebuildObjectRef != "object_test" || rebuild.Status != ActionLifecycleSucceeded {
		t.Fatalf("rebuild did not use selected object: ref=%s result=%#v", client.rebuildObjectRef, rebuild)
	}

	retryFailed, err := ExecutePortalAction(context.Background(), client, "corr_test", NewIndexRetryFailedAction(search.IndexRetryFailedInput{Limit: 12}), true)
	if err != nil {
		t.Fatalf("retry failed returned error: %v", err)
	}
	if client.retryFailedInput.Limit != 12 || resultFieldValue(retryFailed, "Retried") != "1" {
		t.Fatalf("retry failed input/result mismatch: input=%#v result=%#v", client.retryFailedInput, retryFailed)
	}
}

func TestExecutePortalActionTogglesNotesEmbeddingsThroughClient(t *testing.T) {
	client := newFakePortalClient()
	action := NewNotesEmbeddingsToggleAction(fakeNotesEmbeddingStatus(false))
	if _, err := ExecutePortalAction(context.Background(), client, "corr_test", action, false); !errors.Is(err, ErrActionConfirmationRequired) {
		t.Fatalf("toggle without confirmation error = %v, want confirmation required", err)
	}
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true)
	if err != nil {
		t.Fatalf("enable action returned error: %v", err)
	}
	if !client.notesEmbeddingsEnable || result.Status != ActionLifecycleSucceeded || resultFieldValue(result, "Enabled") != "true" {
		t.Fatalf("enable action mismatch: client=%#v result=%#v", client, result)
	}

	disable := NewNotesEmbeddingsToggleAction(fakeNotesEmbeddingStatus(true))
	result, err = ExecutePortalAction(context.Background(), client, "corr_test", disable, true)
	if err != nil {
		t.Fatalf("disable action returned error: %v", err)
	}
	if !client.notesEmbeddingsDisable || result.Status != ActionLifecycleSucceeded || resultFieldValue(result, "Enabled") != "false" {
		t.Fatalf("disable action mismatch: client=%#v result=%#v", client, result)
	}
}

func TestExecutePortalActionRunsMaintenanceActionsThroughClient(t *testing.T) {
	client := newFakePortalClient()

	backupRun, err := ExecutePortalAction(context.Background(), client, "corr_test", NewMaintenanceBackupRunAction(), true)
	if err != nil {
		t.Fatalf("backup run returned error: %v", err)
	}
	if client.backupRunInput.IdempotencyKey == "" || resultFieldValue(backupRun, "Backup") != "backup_operation_test" {
		t.Fatalf("backup run mismatch: input=%#v result=%#v", client.backupRunInput, backupRun)
	}

	backup := maintenance.BackupOperation{Operation: maintenance.Operation{MaintenanceOperationID: "backup_operation_test", Status: maintenance.OperationSucceeded, StartedAt: time.Now().UTC()}}
	verify, err := ExecutePortalAction(context.Background(), client, "corr_test", NewMaintenanceBackupVerifyAction(backup), true)
	if err != nil {
		t.Fatalf("verify returned error: %v", err)
	}
	if client.backupVerifyRef != "backup_operation_test" || resultFieldValue(verify, "Verification") != maintenance.VerificationSucceeded {
		t.Fatalf("verify mismatch: ref=%s result=%#v", client.backupVerifyRef, verify)
	}

	scan, err := ExecutePortalAction(context.Background(), client, "corr_test", NewMaintenanceObjectStoreScanAction(), true)
	if err != nil {
		t.Fatalf("object scan returned error: %v", err)
	}
	if client.objectScanInput.Mode != "sample" || resultFieldValue(scan, "Scan Status") != "ok" {
		t.Fatalf("scan mismatch: input=%#v result=%#v", client.objectScanInput, scan)
	}
}

func TestSlice07DomainRowsExposePortalActions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen string
		want   []string
	}{
		{
			name:   "automations",
			screen: ScreenAutomations,
			want: []string{
				"automation.schedule.schedule_test.inspect",
				"automation.schedule.schedule_test.fire_now",
				"automation.schedule.schedule_test.pause",
				"automation.direct_endpoint.direct_event_endpoint_test.inspect",
				"automation.direct_endpoint.direct_event_endpoint_test.test",
				"automation.invocation.invocation_recent.inspect",
			},
		},
		{
			name:   "capabilities",
			screen: ScreenCapabilities,
			want: []string{
				"capability.provider.provider_test.inspect",
				"capability.provider.provider_test.health",
				"capability.endpoint.capability_endpoint_test.inspect",
				"capability.endpoint.capability_endpoint_test.usage_docs",
				"capability.endpoint.main_system_status_read.call",
			},
		},
		{
			name:   "jobs",
			screen: ScreenJobs,
			want: []string{
				"job.job_recent.inspect",
				"job.job_recent.logs",
				"job.job_recent.outputs",
				"job.job_queued.cancel",
				"job.job_failed.retry",
			},
		},
		{
			name:   "nodes",
			screen: ScreenNodes,
			want: []string{
				"node.node_main.inspect",
				"node.node_main.health",
				"nodes.watched_root.node_workspace_vault.inspect",
				"nodes.sync_conflict.sync_conflict_test.inspect",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", tc.screen, Snapshot{})
			state := result.State
			if tc.screen == ScreenNodes {
				state = state.ToggleRawDetails()
			}
			ids := map[string]bool{}
			for _, action := range ScreenActions(state) {
				ids[action.ID] = true
			}
			for _, want := range tc.want {
				if !ids[want] {
					t.Fatalf("missing action %s in %#v", want, ids)
				}
			}
		})
	}
}

func TestArchivedProjectQuarantineSuppressesAutomationActions(t *testing.T) {
	project := fakeArchivedPortalProject()
	state := NewScreenState(ScreenAutomations)
	state.Data.Automations = AutomationsData{
		ArchivedProjects: []projects.Project{project},
		Automations: []automation.Automation{{
			AutomationID: "automation_archived",
			DisplayName:  "Archived Automation",
			Status:       automation.AutomationStatusActive,
			ProjectID:    &project.ProjectID,
			ScopeID:      &project.ProjectScopeID,
		}},
		Schedules: []automation.Schedule{{
			ScheduleID:        "schedule_archived",
			AutomationID:      "automation_archived",
			ScheduleKey:       "daily_archived",
			DisplayName:       "Daily Archived",
			Status:            automation.ScheduleStatusActive,
			ProjectID:         &project.ProjectID,
			ScopeID:           &project.ProjectScopeID,
			TargetProfileJSON: jsonRaw(`{"capability_ref":"main@portal-project.curl_url","project_ref":"portal-project"}`),
		}},
		DirectEventEndpoints: []automation.DirectEventEndpoint{{
			EndpointID:   "endpoint_archived",
			EndpointSlug: "archived-hook",
			AutomationID: "automation_archived",
			Status:       automation.DirectEventEndpointStatusActive,
		}},
		Invocations: []automation.Invocation{{
			InvocationID:     "invocation_archived",
			AutomationID:     "automation_archived",
			ProjectID:        &project.ProjectID,
			ScopeID:          &project.ProjectScopeID,
			TargetCapability: "main@portal-project.curl_url",
			Status:           automation.InvocationStatusFailed,
		}},
	}

	actions := ScreenActions(state)
	assertNoPortalActionExecutors(t, actions, PortalExecutorScheduleFireNow, PortalExecutorSchedulePause, PortalExecutorScheduleResume)
	for _, forbidden := range []string{"Fire Schedule Now", "Pause Schedule", "Resume Schedule", "Test Direct Event Endpoint"} {
		if hasPortalActionLabel(actions, forbidden) {
			t.Fatalf("archived automation actions exposed %q in %#v", forbidden, portalActionLabels(actions))
		}
	}
	for _, item := range ScreenRecordItems(state) {
		for _, action := range item.RelatedActions {
			switch action.Label {
			case "Fire Schedule Now", "Pause Schedule", "Resume Schedule", "Test Direct Event Endpoint":
				t.Fatalf("archived automation record %s exposed related action %q", item.RecordKind, action.Label)
			}
		}
	}
	if !hasPortalActionLabel(actions, "Inspect Schedule") || !hasPortalActionLabel(actions, "Inspect Direct Event Endpoint") {
		t.Fatalf("archived automation records should keep inspect actions: %#v", portalActionLabels(actions))
	}
}

func TestStaleProjectRefsSuppressAutomationActions(t *testing.T) {
	activeProject := fakeArchivedPortalProject()
	activeProject.ProjectID = "project_active"
	activeProject.ProjectScopeID = "scope_active_project"
	activeProject.ProjectScopeKey = "project.active-project"
	activeProject.Slug = "active-project"
	activeProject.Name = "Active Project"
	activeProject.Status = "active"
	staleProjectID := "project_missing"

	state := NewScreenState(ScreenAutomations)
	state.Data.Automations = AutomationsData{
		Projects: []projects.Project{activeProject},
		Automations: []automation.Automation{
			{
				AutomationID:      "automation_stale",
				DisplayName:       "Stale Automation",
				Status:            automation.AutomationStatusActive,
				ProjectID:         &staleProjectID,
				TargetProfileJSON: jsonRaw(`{"capability_ref":"main@missing-project.curl_url","project_ref":"missing-project"}`),
			},
			{
				AutomationID:      "automation_active",
				DisplayName:       "Active Automation",
				Status:            automation.AutomationStatusActive,
				ProjectID:         &activeProject.ProjectID,
				TargetProfileJSON: jsonRaw(`{"capability_ref":"main@active-project.curl_url","project_ref":"active-project"}`),
			},
		},
		Schedules: []automation.Schedule{
			{
				ScheduleID:        "schedule_stale",
				AutomationID:      "automation_stale",
				ScheduleKey:       "daily_stale",
				DisplayName:       "Daily Stale",
				Status:            automation.ScheduleStatusActive,
				ProjectID:         &staleProjectID,
				TargetProfileJSON: jsonRaw(`{"capability_ref":"main@missing-project.curl_url","project_ref":"missing-project"}`),
			},
			{
				ScheduleID:        "schedule_active",
				AutomationID:      "automation_active",
				ScheduleKey:       "daily_active",
				DisplayName:       "Daily Active",
				Status:            automation.ScheduleStatusActive,
				ProjectID:         &activeProject.ProjectID,
				TargetProfileJSON: jsonRaw(`{"capability_ref":"main@active-project.curl_url","project_ref":"active-project"}`),
			},
		},
		DirectEventEndpoints: []automation.DirectEventEndpoint{
			{
				EndpointID:   "endpoint_stale",
				EndpointSlug: "stale-hook",
				AutomationID: "automation_stale",
				Status:       automation.DirectEventEndpointStatusActive,
			},
			{
				EndpointID:   "endpoint_active",
				EndpointSlug: "active-hook",
				AutomationID: "automation_active",
				Status:       automation.DirectEventEndpointStatusActive,
			},
		},
	}

	refs := staleAutomationProjectRefs(state.Data.Automations)
	if len(refs) != 2 || refs[0] != "missing-project" || refs[1] != "project_missing" {
		t.Fatalf("stale refs = %#v, want missing-project and project_missing", refs)
	}
	items := ScreenRecordItems(state)
	staleSchedule, ok := findRecordItem(items, "schedule", "schedule_stale")
	if !ok {
		t.Fatalf("missing stale schedule in %#v", selectableRecordRefs(items))
	}
	if hasPortalActionLabel(staleSchedule.RelatedActions, "Fire Schedule Now") ||
		hasPortalActionLabel(staleSchedule.RelatedActions, "Pause Schedule") ||
		hasPortalActionLabel(staleSchedule.RelatedActions, "Resume Schedule") {
		t.Fatalf("stale schedule exposed runtime actions: %#v", portalActionLabels(staleSchedule.RelatedActions))
	}
	activeSchedule, ok := findRecordItem(items, "schedule", "schedule_active")
	if !ok {
		t.Fatalf("missing active schedule in %#v", selectableRecordRefs(items))
	}
	if !hasPortalActionLabel(activeSchedule.RelatedActions, "Fire Schedule Now") {
		t.Fatalf("active schedule lost runtime actions: %#v", portalActionLabels(activeSchedule.RelatedActions))
	}
	staleEndpoint, ok := findRecordItem(items, "direct_event_endpoint", "endpoint_stale")
	if !ok {
		t.Fatalf("missing stale endpoint in %#v", selectableRecordRefs(items))
	}
	if hasPortalActionLabel(staleEndpoint.RelatedActions, "Test Direct Event Endpoint") {
		t.Fatalf("stale endpoint exposed test action: %#v", portalActionLabels(staleEndpoint.RelatedActions))
	}
	activeEndpoint, ok := findRecordItem(items, "direct_event_endpoint", "endpoint_active")
	if !ok {
		t.Fatalf("missing active endpoint in %#v", selectableRecordRefs(items))
	}
	if !hasPortalActionLabel(activeEndpoint.RelatedActions, "Test Direct Event Endpoint") {
		t.Fatalf("active endpoint lost test action: %#v", portalActionLabels(activeEndpoint.RelatedActions))
	}
}

func TestArchivedProjectQuarantineSuppressesCapabilityInvokeActions(t *testing.T) {
	project := fakeArchivedPortalProject()
	state := NewScreenState(ScreenCapabilities)
	state.Data.Capabilities = CapabilitiesData{
		ArchivedProjects: []projects.Project{project},
		Explorer: CapabilityExplorerState{
			Level:            CapabilityExplorerCapabilities,
			SelectedScope:    "main",
			SelectedProvider: "main@portal-project",
			ExpandedRef:      "main@portal-project.curl_url",
		},
		Providers: []capabilities.ProviderListItem{{
			Provider: capabilities.Provider{
				ProviderID:     "provider_archived",
				ProviderKey:    "portal-project",
				CompactAddress: "main@portal-project",
				DisplayName:    "Portal Project",
				ScopeID:        project.ProjectScopeID,
				Status:         capabilities.ProviderStatusActive,
			},
		}},
		Capabilities: []capabilities.CapabilityListItem{{
			CapabilityEndpoint: capabilities.CapabilityEndpoint{
				CapabilityEndpointID:        "capability_archived",
				ProviderID:                  "provider_archived",
				CompactAddress:              "main@portal-project.curl_url",
				EndpointName:                "curl_url",
				Form:                        capabilities.CapabilityFormCommand,
				RiskLevel:                   capabilities.RiskLevelLow,
				ExecutionAuthorizationLevel: 2,
				Status:                      capabilities.EndpointStatusActive,
			},
			ProviderAddress: "main@portal-project",
			ProviderKey:     "portal-project",
			ProviderHealth:  capabilities.HealthStatusOK,
			ClassName:       "curl_url",
			DisplayName:     "Curl URL",
		}},
	}

	actions := ScreenActions(state)
	assertNoPortalActionExecutors(t, actions, PortalExecutorCapabilityCall)
	if hasPortalActionLabel(actions, "Invoke Capability") {
		t.Fatalf("archived capability actions exposed invoke in %#v", portalActionLabels(actions))
	}
	if !hasPortalActionLabel(actions, "Inspect Capability") || !hasPortalActionLabel(actions, "View Usage Docs") {
		t.Fatalf("archived capability should keep inspect/docs actions: %#v", portalActionLabels(actions))
	}
	for _, item := range ScreenRecordItems(state) {
		for _, action := range item.RelatedActions {
			if action.Executor.Kind == PortalExecutorCapabilityCall || action.Label == "Invoke Capability" {
				t.Fatalf("archived capability record exposed invoke action: %#v", action)
			}
		}
	}
}

func TestArchivedProjectQuarantineSuppressesJobRunActions(t *testing.T) {
	project := fakeArchivedPortalProject()
	scopeID := project.ProjectScopeID
	targetKind := "project"
	state := NewScreenState(ScreenJobs)
	state.Data.Jobs = JobsData{
		ArchivedProjects: []projects.Project{project},
		Jobs: []jobs.Job{{
			JobID:      "job_archived_running",
			JobType:    "script",
			Status:     jobs.StatusRunning,
			ScopeID:    &scopeID,
			TargetKind: &targetKind,
			TargetID:   &project.ProjectID,
		}},
		QueuedJobs: []jobs.Job{{
			JobID:      "job_archived_queued",
			JobType:    "script",
			Status:     jobs.StatusQueued,
			ScopeID:    &scopeID,
			TargetKind: &targetKind,
			TargetID:   &project.ProjectID,
		}},
		FailedJobs: []jobs.Job{{
			JobID:        "job_archived_failed",
			JobType:      "script",
			Status:       jobs.StatusFailed,
			ScopeID:      &scopeID,
			TargetKind:   &targetKind,
			TargetID:     &project.ProjectID,
			AttemptCount: 1,
			MaxAttempts:  2,
		}},
	}

	actions := ScreenActions(state)
	for _, action := range actions {
		switch action.Executor.Kind {
		case PortalExecutorJobRetry, PortalExecutorJobCancel:
			if strings.Contains(action.TargetRef, "job_archived") {
				t.Fatalf("archived job action exposed %s for %s", action.Executor.Kind, action.TargetRef)
			}
		}
	}
	for _, item := range ScreenRecordItems(state) {
		for _, action := range item.RelatedActions {
			switch action.Executor.Kind {
			case PortalExecutorJobRetry, PortalExecutorJobCancel:
				t.Fatalf("archived job record %s exposed related action %#v", item.RecordRef, action)
			}
		}
	}
	if !hasPortalActionLabel(actions, "Inspect Job Logs") || !hasPortalActionLabel(actions, "Inspect Job Outputs") {
		t.Fatalf("archived jobs should keep inspect logs/outputs actions: %#v", portalActionLabels(actions))
	}
}

func TestNodesRawDetailsExposeBackendRows(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenNodes, Snapshot{})
	if client.syncStatusFilter.NodeRef != "main" {
		t.Fatalf("sync status node ref = %q, want main", client.syncStatusFilter.NodeRef)
	}
	result.State = result.State.ToggleRawDetails()
	ids := map[string]bool{}
	for _, action := range ScreenActions(result.State) {
		ids[action.ID] = true
	}
	for _, want := range []string{
		"nodes.sync_batch.sync_batch_test.inspect",
		"nodes.sync_replica.sync_replica_test.inspect",
		"nodes.private_backup.private_backup_test.inspect",
	} {
		if !ids[want] {
			t.Fatalf("raw details missing action %s in %#v", want, ids)
		}
	}
}

func TestNodesPortalDecluttersDefaultAndKeepsDiagnostics(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenNodes, Snapshot{})
	defaultOutput := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenNodes,
		State:    result.State,
	})
	for _, want := range []string{"Attention", "Node Cards", "Protected Folders", "Active Watched Roots", "Box", "macbook", "Diagnostics"} {
		if !strings.Contains(defaultOutput, want) {
			t.Fatalf("nodes default output missing %q:\n%s", want, defaultOutput)
		}
	}
	if strings.Index(defaultOutput, "Attention") > strings.Index(defaultOutput, "Node Cards") ||
		strings.Index(defaultOutput, "Node Cards") > strings.Index(defaultOutput, "Active Watched Roots") {
		t.Fatalf("nodes default output is not attention-first:\n%s", defaultOutput)
	}
	for _, hidden := range []string{"node_workspace", "sync_batch_test", "backup_batch_test", "sync_replica_test", "private_backup_test", "deletion_request_test"} {
		if strings.Contains(defaultOutput, hidden) {
			t.Fatalf("nodes default output should hide raw record %q:\n%s", hidden, defaultOutput)
		}
	}

	detailsState := result.State.ToggleRawDetails()
	detailsOutput := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenNodes,
		State:    detailsState,
	})
	for _, want := range []string{"Raw Nodes", "node_workspace", "Raw Backup Batches", "backup_batch_test", "Raw Sync Batches", "sync_batch_test", "Raw Private Backups", "private_backup_test"} {
		if !strings.Contains(detailsOutput, want) {
			t.Fatalf("nodes raw details output missing %q:\n%s", want, detailsOutput)
		}
	}
}

func TestNodesScopedSearchUsesDeclutteredRows(t *testing.T) {
	state := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenNodes, Snapshot{}).State
	results := SearchPortal(actions.DefaultRegistry(), state, "#nodes macbook", 20)
	if !searchResultsContainTitle(results, "macbook") {
		t.Fatalf("nodes scoped search should find friendly node rows: %#v", results)
	}
	for _, result := range results {
		if strings.Contains(result.Title, "sync_batch_test") || strings.Contains(result.Title, "private_backup_test") || strings.Contains(result.Title, "deletion_request_test") {
			t.Fatalf("nodes scoped search exposed raw diagnostics by default: %#v", results)
		}
	}

	rawResults := SearchPortal(actions.DefaultRegistry(), state.ToggleRawDetails(), "#nodes sync_batch_test", 20)
	if !searchResultsContainTitle(rawResults, "sync_batch_test") {
		t.Fatalf("nodes scoped search should expose raw diagnostics in details mode: %#v", rawResults)
	}
}

func TestExecutePortalActionRunsAutomationDomainActions(t *testing.T) {
	client := newFakePortalClient()
	schedule := automation.Schedule{ScheduleID: "schedule_test", ScheduleKey: "daily_backup", DisplayName: "Daily Backup", Status: automation.ScheduleStatusActive}

	inspect, err := ExecutePortalAction(context.Background(), client, "corr_test", NewScheduleInspectAction(schedule, ScreenAutomations), true)
	if err != nil {
		t.Fatalf("schedule inspect returned error: %v", err)
	}
	if client.scheduleRef != "schedule_test" || resultFieldValue(inspect, "Schedule") != "Daily Backup" {
		t.Fatalf("schedule inspect mismatch: ref=%s result=%#v", client.scheduleRef, inspect)
	}

	fire, err := ExecutePortalAction(context.Background(), client, "corr_test", NewScheduleFireNowAction(schedule, ScreenAutomations), true)
	if err != nil {
		t.Fatalf("schedule fire returned error: %v", err)
	}
	if client.fireScheduleRef != "schedule_test" || resultFieldValue(fire, "Fire") != "schedule_fire_portal" {
		t.Fatalf("schedule fire mismatch: ref=%s result=%#v", client.fireScheduleRef, fire)
	}
	if !client.fireScheduleInput.DispatchNow {
		t.Fatal("schedule fire portal action did not request immediate dispatch")
	}

	pause, err := ExecutePortalAction(context.Background(), client, "corr_test", NewSchedulePauseAction(schedule, ScreenAutomations), true)
	if err != nil {
		t.Fatalf("schedule pause returned error: %v", err)
	}
	if client.pauseScheduleRef != "schedule_test" || resultFieldValue(pause, "Status") != automation.ScheduleStatusPaused {
		t.Fatalf("schedule pause mismatch: ref=%s result=%#v", client.pauseScheduleRef, pause)
	}

	resumeAction := NewScheduleResumeAction(automation.Schedule{ScheduleID: "schedule_test", Status: automation.ScheduleStatusPaused}, ScreenAutomations)
	resume, err := ExecutePortalAction(context.Background(), client, "corr_test", resumeAction, true)
	if err != nil {
		t.Fatalf("schedule resume returned error: %v", err)
	}
	if client.resumeScheduleRef != "schedule_test" || resultFieldValue(resume, "Status") != automation.ScheduleStatusActive {
		t.Fatalf("schedule resume mismatch: ref=%s result=%#v", client.resumeScheduleRef, resume)
	}
}

func TestExecutePortalActionRunsCapabilityDomainActions(t *testing.T) {
	client := newFakePortalClient()
	capability := capabilities.CapabilityListItem{
		CapabilityEndpoint: capabilities.CapabilityEndpoint{
			CapabilityEndpointID:        "capability_endpoint_test",
			CompactAddress:              "main@system.status.read",
			Form:                        "query",
			RiskLevel:                   "low",
			ExecutionAuthorizationLevel: 1,
			Status:                      "active",
		},
		DisplayName: "Read System Status",
	}
	provider := capabilities.ProviderListItem{Provider: capabilities.Provider{ProviderID: "provider_test", ProviderKey: "system", CompactAddress: "main@system", DisplayName: "System", Status: "active"}}

	health, err := ExecutePortalAction(context.Background(), client, "corr_test", NewProviderHealthAction(provider, ScreenCapabilities), true)
	if err != nil {
		t.Fatalf("provider health returned error: %v", err)
	}
	if client.providerHealthRef != "provider_test" || resultFieldValue(health, "Health") != "healthy" {
		t.Fatalf("provider health mismatch: ref=%s result=%#v", client.providerHealthRef, health)
	}

	docs, err := ExecutePortalAction(context.Background(), client, "corr_test", NewCapabilityUsageDocsAction(capability, ScreenCapabilities), true)
	if err != nil {
		t.Fatalf("usage docs returned error: %v", err)
	}
	if client.usageDocsRef != "capability_endpoint_test" || resultFieldValue(docs, "Documents") != "1" {
		t.Fatalf("usage docs mismatch: ref=%s result=%#v", client.usageDocsRef, docs)
	}

	callAction := NewCapabilityCallAction(capability, ScreenCapabilities)
	callAction.InputValues["input_json"] = `{"format":"short"}`
	call, err := ExecutePortalAction(context.Background(), client, "corr_test", callAction, true)
	if err != nil {
		t.Fatalf("capability call returned error: %v", err)
	}
	if client.callCapability.Target != "main@system.status.read" || resultFieldValue(call, "Capability Call") != "capability_call_portal" {
		t.Fatalf("capability call mismatch: input=%#v result=%#v", client.callCapability, call)
	}

	unsafe := capability
	unsafe.CapabilityEndpoint.RiskLevel = "destructive"
	unsafeAction := NewCapabilityCallAction(unsafe, ScreenCapabilities)
	if !unsafeAction.Disabled() || !strings.Contains(unsafeAction.DisabledReason, "risk") {
		t.Fatalf("unsafe capability call should be disabled: %#v", unsafeAction)
	}
}

func TestExecutePortalActionRunsJobAndNodeDomainActions(t *testing.T) {
	client := newFakePortalClient()
	failedJob := jobs.Job{JobID: "job_failed", JobType: "script", Status: jobs.StatusFailed, AttemptCount: 1, MaxAttempts: 2}
	queuedJob := jobs.Job{JobID: "job_queued", JobType: "script", Status: jobs.StatusQueued}
	node := nodes.Node{NodeID: "node_main", NodeKey: "main", Status: "active"}

	jobDetail, err := ExecutePortalAction(context.Background(), client, "corr_test", NewJobInspectAction(failedJob, ScreenJobs), true)
	if err != nil {
		t.Fatalf("job inspect returned error: %v", err)
	}
	if client.jobInspectRef != "job_failed" || resultFieldValue(jobDetail, "Job") != "job_failed" {
		t.Fatalf("job inspect mismatch: ref=%s result=%#v", client.jobInspectRef, jobDetail)
	}

	logs, err := ExecutePortalAction(context.Background(), client, "corr_test", NewJobLogsInspectAction(failedJob, ScreenJobs), true)
	if err != nil {
		t.Fatalf("job logs returned error: %v", err)
	}
	if client.jobLogsRef != "job_failed" || resultFieldValue(logs, "Logs") != "1" {
		t.Fatalf("job logs mismatch: ref=%s result=%#v", client.jobLogsRef, logs)
	}

	retry, err := ExecutePortalAction(context.Background(), client, "corr_test", NewJobRetryAction(failedJob, ScreenJobs), true)
	if err != nil {
		t.Fatalf("job retry returned error: %v", err)
	}
	if client.jobRetryRef != "job_failed" || resultFieldValue(retry, "Status") != jobs.StatusQueued {
		t.Fatalf("job retry mismatch: ref=%s result=%#v", client.jobRetryRef, retry)
	}
	acknowledge, err := ExecutePortalAction(context.Background(), client, "corr_test", NewJobAcknowledgeAttentionAction(failedJob, ScreenJobs), true)
	if err != nil {
		t.Fatalf("job attention acknowledge returned error: %v", err)
	}
	if client.jobAcknowledgeRef != "job_failed" || resultFieldValue(acknowledge, "Attention") != jobs.FailureAttentionStatusAcknowledged {
		t.Fatalf("job attention acknowledge mismatch: ref=%s result=%#v", client.jobAcknowledgeRef, acknowledge)
	}
	archive, err := ExecutePortalAction(context.Background(), client, "corr_test", NewJobArchiveAttentionAction(failedJob, ScreenJobs), true)
	if err != nil {
		t.Fatalf("job attention archive returned error: %v", err)
	}
	if client.jobArchiveRef != "job_failed" || resultFieldValue(archive, "Attention") != jobs.FailureAttentionStatusArchived {
		t.Fatalf("job attention archive mismatch: ref=%s result=%#v", client.jobArchiveRef, archive)
	}
	exhaustedRetry := NewJobRetryAction(jobs.Job{JobID: "job_exhausted", JobType: "script", Status: jobs.StatusFailed, AttemptCount: 1, MaxAttempts: 1}, ScreenJobs)
	if !exhaustedRetry.Disabled() || !strings.Contains(exhaustedRetry.DisabledReason, "exhausted") {
		t.Fatalf("exhausted job retry should be disabled: %#v", exhaustedRetry)
	}

	cancel, err := ExecutePortalAction(context.Background(), client, "corr_test", NewJobCancelAction(queuedJob, ScreenJobs), true)
	if err != nil {
		t.Fatalf("job cancel returned error: %v", err)
	}
	if client.jobCancelRef != "job_queued" || resultFieldValue(cancel, "Status") != jobs.StatusCancelled {
		t.Fatalf("job cancel mismatch: ref=%s result=%#v", client.jobCancelRef, cancel)
	}

	health, err := ExecutePortalAction(context.Background(), client, "corr_test", NewNodeHealthAction(node, ScreenNodes), true)
	if err != nil {
		t.Fatalf("node health returned error: %v", err)
	}
	if client.nodeHealthRef != "node_main" || resultFieldValue(health, "Heartbeat Age Seconds") != "3" {
		t.Fatalf("node health mismatch: ref=%s result=%#v", client.nodeHealthRef, health)
	}
}

func TestModelPreviewEnterAllowsSensitiveConfirmation(t *testing.T) {
	model := NewModelWithOptions(ModelOptions{
		Mode:          testMode(),
		Client:        newFakePortalClient(),
		CorrelationID: "corr_test",
		Snapshot:      fakeSnapshot(),
		Registry:      actions.DefaultRegistry(),
		StartScreen:   ScreenJobs,
	})
	model.actionPanel = ActionPanelState{
		Lifecycle: ActionLifecyclePreview,
		Action:    NewJobCancelAction(jobs.Job{JobID: "job_queued", Status: jobs.StatusQueued}, ScreenJobs),
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("sensitive preview should not execute before confirmation")
	}
	if model.actionPanel.Lifecycle != ActionLifecycleNeedsConfirmation {
		t.Fatalf("lifecycle = %s, want confirmation", model.actionPanel.Lifecycle)
	}
}

func TestExecutePortalActionRunsDeletionRequestLifecycleActions(t *testing.T) {
	client := newFakePortalClient()
	request := fakeDeletionRequestWithStatus("deletion_request_test", loomsync.DeletionRequestStatusPendingReview)

	inspect, err := ExecutePortalAction(context.Background(), client, "corr_test", NewDeletionRequestInspectAction(request, ScreenNodes), true)
	if err != nil {
		t.Fatalf("deletion request inspect returned error: %v", err)
	}
	if client.deletionRequestRef != "deletion_request_test" || resultFieldValue(inspect, "Status") != loomsync.DeletionRequestStatusPendingReview {
		t.Fatalf("deletion request inspect mismatch: ref=%s result=%#v", client.deletionRequestRef, inspect)
	}

	approve, err := ExecutePortalAction(context.Background(), client, "corr_test", NewDeletionRequestApproveAction(request, ScreenNodes), true)
	if err != nil {
		t.Fatalf("deletion request approve returned error: %v", err)
	}
	if client.deletionRequestAction != "approve" || resultFieldValue(approve, "Status") != loomsync.DeletionRequestStatusApproved {
		t.Fatalf("deletion request approve mismatch: action=%s result=%#v", client.deletionRequestAction, approve)
	}

	deny, err := ExecutePortalAction(context.Background(), client, "corr_test", NewDeletionRequestDenyAction(request, ScreenNodes), true)
	if err != nil {
		t.Fatalf("deletion request deny returned error: %v", err)
	}
	if client.deletionRequestAction != "deny" || resultFieldValue(deny, "Status") != loomsync.DeletionRequestStatusDenied {
		t.Fatalf("deletion request deny mismatch: action=%s result=%#v", client.deletionRequestAction, deny)
	}

	complete, err := ExecutePortalAction(context.Background(), client, "corr_test", NewDeletionRequestCompleteAction(request, ScreenNodes), true)
	if err != nil {
		t.Fatalf("deletion request complete returned error: %v", err)
	}
	if client.deletionRequestAction != "complete" || resultFieldValue(complete, "Status") != loomsync.DeletionRequestStatusCompleted {
		t.Fatalf("deletion request complete mismatch: action=%s result=%#v", client.deletionRequestAction, complete)
	}
}

func TestExecutePortalActionReturnsFailedResultOnBackendError(t *testing.T) {
	client := newFakePortalClient()
	client.runErr = errors.New("worker unavailable")
	action, ok := actions.DefaultRegistry().Get("worker.selfcheck.run_once")
	if !ok {
		t.Fatal("missing action")
	}
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", PortalActionFromRegistry(action), true)
	if err != nil {
		t.Fatalf("ExecutePortalAction returned invocation error: %v", err)
	}
	if result.Status != ActionLifecycleFailed {
		t.Fatalf("status = %s, want failed", result.Status)
	}
	if !strings.Contains(result.ErrorMessage, "worker unavailable") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestRunExitAfterRenderRunActionRequiresConfirmation(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Out:             &out,
		ExitAfterRender: true,
		RunActionID:     "worker.selfcheck.run_once",
		Registry:        actions.DefaultRegistry(),
	})
	if !errors.Is(err, ErrActionConfirmationRequired) {
		t.Fatalf("error = %v, want confirmation required", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output on invocation error, got:\n%s", out.String())
	}
}

func TestRunExitAfterRenderConfirmedActionRendersPortalResult(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Out:             &out,
		ExitAfterRender: true,
		RunActionID:     "worker.selfcheck.run_once",
		ConfirmAction:   true,
		Registry:        actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Action Complete") || !strings.Contains(out.String(), "Run: worker_run_test") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}

func TestRenderActionPreviewAndExecution(t *testing.T) {
	action, ok := actions.DefaultRegistry().Get("worker.selfcheck.run_once")
	if !ok {
		t.Fatal("missing action")
	}
	preview := RenderActionPreview(testMode(), action)
	if !strings.Contains(preview, "Confirmation: required") || !strings.Contains(preview, "loom worker run main.worker_selfcheck --once") {
		t.Fatalf("unexpected preview:\n%s", preview)
	}
	executed := RenderActionExecution(testMode(), ActionExecution{
		Action:      action,
		WorkerRunID: "worker_run_test",
		WorkerKey:   "main.worker_selfcheck",
	})
	if !strings.Contains(executed, "Action Complete") || !strings.Contains(executed, "worker_run_test") {
		t.Fatalf("unexpected execution render:\n%s", executed)
	}
}

func TestModelSearchAndEscape(t *testing.T) {
	model := NewModel(testMode(), fakeSnapshot(), actions.DefaultRegistry(), ScreenHome)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updated.(Model)
	if !model.searchOpen {
		t.Fatal("expected search to open")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("worker")})
	model = updated.(Model)
	if !strings.Contains(model.View(), "Background Operations") {
		t.Fatalf("search view missing worker result:\n%s", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.searchOpen {
		t.Fatal("expected escape to close search")
	}
}

func TestModelSearchSelectionMoves(t *testing.T) {
	model := NewModel(testMode(), fakeSnapshot(), actions.DefaultRegistry(), ScreenHome)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("search")})
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if model.searchIndex != 1 {
		t.Fatalf("search index after down = %d, want 1", model.searchIndex)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(Model)
	if model.searchIndex != 0 {
		t.Fatalf("search index after up = %d, want 0", model.searchIndex)
	}
}

func TestModelSearchAllowsVimLettersInQueries(t *testing.T) {
	model := NewModel(testMode(), fakeSnapshot(), actions.DefaultRegistry(), ScreenHome)
	model = updateModelWithRunes(t, model, "/")
	model = updateModelWithRunes(t, model, "#workers selfcheck")
	if model.searchText != "#workers selfcheck" {
		t.Fatalf("search text = %q, want full query with k letters", model.searchText)
	}
	if len(model.searchResults()) == 0 {
		t.Fatal("expected scoped worker results after typing k in the query")
	}

	model = NewModel(testMode(), fakeSnapshot(), actions.DefaultRegistry(), ScreenHome)
	model = updateModelWithRunes(t, model, "/")
	model = updateModelWithRunes(t, model, "$ worker run main.worker_selfcheck --once")
	if model.searchText != "$ worker run main.worker_selfcheck --once" {
		t.Fatalf("command text = %q, want full command with k letters", model.searchText)
	}
	if !model.commandMode.Active {
		t.Fatal("expected command mode to remain active")
	}
}

func TestModelScopedSearchAllowsSpaceAndScopeSelection(t *testing.T) {
	model := NewModel(testMode(), fakeSnapshot(), actions.DefaultRegistry(), ScreenHome)
	model = updateModelWithRunes(t, model, "/")
	model = updateModelWithRunes(t, model, "#capability")
	results := model.searchResults()
	if len(results) == 0 || results[0].Title != "#capabilities" {
		t.Fatalf("scope suggestion missing: %#v", results)
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("selecting a search scope should not execute or navigate")
	}
	if model.searchText != "#capabilities " {
		t.Fatalf("search text after scope enter = %q", model.searchText)
	}

	model = NewModel(testMode(), fakeSnapshot(), actions.DefaultRegistry(), ScreenHome)
	model = updateModelWithRunes(t, model, "/")
	model = updateModelWithRunes(t, model, "#capability")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(Model)
	if model.searchText != "#capability " {
		t.Fatalf("search text after space = %q, want scope plus space", model.searchText)
	}
	model = updateModelWithRunes(t, model, "main@system")
	if input := ParsePortalSearchInput(model.searchText); !input.ScopeComplete || input.Scope != "capabilities" || input.Query != "main@system" {
		t.Fatalf("parsed scoped search = %#v", input)
	}
}

func TestModelEnterOnScopedWorkerSearchResultOpensRecordPreview(t *testing.T) {
	client := newFakePortalClient()
	model := NewModelWithOptions(ModelOptions{
		Mode:          testMode(),
		Client:        client,
		CorrelationID: "corr_test",
		Snapshot:      fakeSnapshot(),
		Registry:      actions.DefaultRegistry(),
		StartScreen:   ScreenHome,
	})

	model = updateModelWithRunes(t, model, "/")
	model = updateModelWithRunes(t, model, "#workers run worker selfcheck")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("opening a safe-run preview must not execute immediately")
	}
	if model.searchOpen {
		t.Fatal("expected search to close after opening action preview")
	}
	if model.actionPanel.Lifecycle != ActionLifecyclePreview {
		t.Fatalf("lifecycle = %s, want preview", model.actionPanel.Lifecycle)
	}
	if model.actionPanel.Action.ID != "worker.main_worker_selfcheck.inspect" {
		t.Fatalf("action = %s", model.actionPanel.Action.ID)
	}
	if client.runWorkerRef != "" {
		t.Fatalf("worker executed too early: %s", client.runWorkerRef)
	}
}

func TestScopedWorkerSearchReturnsRecordsBeforeActions(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenBackground, fakeSnapshot())
	results := SearchPortal(actions.DefaultRegistry(), state, "#workers run worker selfcheck", 8)
	if len(results) == 0 {
		t.Fatal("expected scoped worker results")
	}
	if results[0].Action.ID != "worker.main_worker_selfcheck.inspect" {
		t.Fatalf("first result = %s, want worker inspect record; all results: %#v", results[0].Action.ID, results)
	}
	for _, result := range results {
		if strings.Contains(result.Action.ID, ".run_once") {
			t.Fatalf("scoped worker search should return records before actions: %#v", results)
		}
	}

	navigationResults := SearchPortal(actions.DefaultRegistry(), state, "selfcheck", 8)
	for _, result := range navigationResults {
		if strings.Contains(result.Action.ID, "worker.") {
			t.Fatalf("default navigation search exposed worker action: %#v", navigationResults)
		}
	}
}

func TestModelSearchNavigationLoadsLiveScreenData(t *testing.T) {
	client := newFakePortalClient()
	model := NewModelWithOptions(ModelOptions{
		Mode:          testMode(),
		Client:        client,
		CorrelationID: "corr_test",
		Snapshot:      fakeSnapshot(),
		Registry:      actions.DefaultRegistry(),
		StartScreen:   ScreenHome,
	})

	model = updateModelWithRunes(t, model, "/")
	model = updateModelWithRunes(t, model, "capabilities")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("opening a screen from search should load live screen data")
	}
	if model.screen != ScreenCapabilities {
		t.Fatalf("screen = %s, want %s", model.screen, ScreenCapabilities)
	}
	if got := model.currentScreenState().Status; got != ScreenLoadLoading {
		t.Fatalf("screen status = %s, want loading", got)
	}

	msg := cmd()
	loaded, ok := msg.(screenLoadedMsg)
	if !ok {
		t.Fatalf("load command returned %T", msg)
	}
	updated, _ = model.Update(loaded)
	model = updated.(Model)
	state := model.currentScreenState()
	if got := len(state.Data.Capabilities.Capabilities); got != 1 {
		t.Fatalf("loaded capabilities = %d, want 1", got)
	}
	if got := len(state.Data.Capabilities.Providers); got != 1 {
		t.Fatalf("loaded providers = %d, want 1", got)
	}
	if strings.Contains(model.View(), "No capabilities returned by the backend.") {
		t.Fatalf("loaded capabilities screen rendered empty state:\n%s", model.View())
	}
}

func TestModelInitialNonHomeScreenLoadsLiveData(t *testing.T) {
	client := newFakePortalClient()
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          client,
		CorrelationID:   "corr_test",
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenCapabilities,
		NoBootAnimation: true,
	})
	if got := model.currentScreenState().Status; got != ScreenLoadLoading {
		t.Fatalf("initial screen status = %s, want loading", got)
	}
	cmd := model.Init()
	if cmd == nil {
		t.Fatal("non-home initial screen should load live data")
	}
	msg := cmd()
	loaded, ok := msg.(screenLoadedMsg)
	if !ok {
		t.Fatalf("load command returned %T", msg)
	}
	updated, _ := model.Update(loaded)
	model = updated.(Model)
	if got := len(model.currentScreenState().Data.Capabilities.Capabilities); got != 1 {
		t.Fatalf("loaded capabilities = %d, want 1", got)
	}
}

func TestModelPreviewEnterRequiresConfirmationForSafeRun(t *testing.T) {
	model := modelWithSelfcheckPreview(t, newFakePortalClient())

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("preview enter should not execute safe action before confirmation")
	}
	if model.actionPanel.Lifecycle != ActionLifecycleNeedsConfirmation {
		t.Fatalf("lifecycle = %s, want confirmation", model.actionPanel.Lifecycle)
	}
	if !strings.Contains(model.View(), "Confirm Action") {
		t.Fatalf("confirmation not rendered:\n%s", model.View())
	}
}

func TestModelActionFormEditsRequiredInputBeforeConfirmation(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	action := NewBoxProjectScaffoldAction(box.Inspect(resolved))
	model := NewModelWithOptions(ModelOptions{Mode: testMode(), Snapshot: fakeSnapshot(), Registry: actions.DefaultRegistry(), StartScreen: ScreenBox})
	model.actionPanel = ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("invalid form should not execute")
	}
	if model.actionPanel.Lifecycle != ActionLifecycleNeedsInput {
		t.Fatalf("lifecycle = %s, want needs_input", model.actionPanel.Lifecycle)
	}
	if !strings.Contains(model.View(), "Action Form") || !strings.Contains(model.View(), "Required") {
		t.Fatalf("form validation not rendered:\n%s", model.View())
	}

	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("Research")},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune("Notes")},
	} {
		updated, cmd = model.Update(msg)
		model = updated.(Model)
		if cmd != nil {
			t.Fatal("typing into form should not execute")
		}
	}
	if got := model.actionPanel.Action.InputValues["project_name"]; got != "Research Notes" {
		t.Fatalf("project_name input = %q", got)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("valid form should advance to confirmation before execution")
	}
	if model.actionPanel.Lifecycle != ActionLifecycleNeedsConfirmation {
		t.Fatalf("lifecycle = %s, want confirmation", model.actionPanel.Lifecycle)
	}
	if !strings.Contains(model.View(), "Confirm Action") || !strings.Contains(model.View(), "Research Notes") {
		t.Fatalf("confirmation missing form value:\n%s", model.View())
	}
}

func TestModelConfirmationEnterRunsActionAndRefreshesScreen(t *testing.T) {
	client := newFakePortalClient()
	model := modelWithSelfcheckPreview(t, client)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("confirmation enter should return execution command")
	}
	if model.actionPanel.Lifecycle != ActionLifecycleRunning {
		t.Fatalf("lifecycle = %s, want running", model.actionPanel.Lifecycle)
	}

	msg := cmd()
	updated, refreshCmd := model.Update(msg)
	model = updated.(Model)
	if model.actionPanel.Lifecycle != ActionLifecycleSucceeded {
		t.Fatalf("lifecycle after execution = %s, want succeeded", model.actionPanel.Lifecycle)
	}
	if model.screen != ScreenBackground {
		t.Fatalf("screen = %s, want background", model.screen)
	}
	if model.currentScreenState().Status != ScreenLoadLoading {
		t.Fatalf("screen status = %s, want loading", model.currentScreenState().Status)
	}
	if refreshCmd == nil {
		t.Fatal("successful action should refresh affected screen")
	}
	if !strings.Contains(model.View(), "Action Complete") || !strings.Contains(model.View(), "worker_run_test") {
		t.Fatalf("result not rendered:\n%s", model.View())
	}

	updated, _ = model.Update(refreshCmd())
	model = updated.(Model)
	if model.currentScreenState().Status != ScreenLoadLoaded {
		t.Fatalf("status after refresh = %s, want loaded", model.currentScreenState().Status)
	}
	if !strings.Contains(model.View(), "Action Complete") {
		t.Fatalf("result panel should stay visible after refresh:\n%s", model.View())
	}
}

func TestModelActionFailureRendersFailurePanel(t *testing.T) {
	client := newFakePortalClient()
	client.runErr = errors.New("worker unavailable")
	model := modelWithSelfcheckPreview(t, client)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected execution command")
	}
	updated, refreshCmd := model.Update(cmd())
	model = updated.(Model)
	if refreshCmd != nil {
		t.Fatal("failed action should not refresh screen")
	}
	if model.actionPanel.Lifecycle != ActionLifecycleFailed {
		t.Fatalf("lifecycle = %s, want failed", model.actionPanel.Lifecycle)
	}
	if !strings.Contains(model.View(), "Action Failed") || !strings.Contains(model.View(), "worker unavailable") {
		t.Fatalf("failure not rendered:\n%s", model.View())
	}
}

func TestModelEscapeCancelsActionPreview(t *testing.T) {
	model := modelWithSelfcheckPreview(t, newFakePortalClient())

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("escape from preview should not run a command")
	}
	if model.actionPanel.Lifecycle != ActionLifecycleCancelled {
		t.Fatalf("lifecycle = %s, want cancelled", model.actionPanel.Lifecycle)
	}
	if !strings.Contains(model.View(), "Action Cancelled") {
		t.Fatalf("cancelled panel not rendered:\n%s", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.actionPanelActive() {
		t.Fatalf("action panel should close after second escape: %#v", model.actionPanel)
	}
}

func TestRenderScreenActionsUseFriendlyLabels(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBackground, Snapshot{})
	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenBackground,
		State:    result.State,
	})
	if !strings.Contains(output, "Available Actions") || !strings.Contains(output, "Run...") || !strings.Contains(output, "Check...") {
		t.Fatalf("grouped action labels missing:\n%s", output)
	}
	if strings.Index(output, "Available Actions") > strings.Index(output, "Worker Attention") {
		t.Fatalf("available actions should render before worker attention:\n%s", output)
	}
	if strings.Contains(output, "Run Worker Selfcheck Once") {
		t.Fatalf("worker run action should be behind the Run group by default:\n%s", output)
	}
	if strings.Contains(output, "loom worker run") {
		t.Fatalf("surface should not show raw worker commands by default:\n%s", output)
	}
}

func TestOperationalActionGroupsDeclutterDefaultSurfaces(t *testing.T) {
	for _, tc := range []struct {
		name       string
		screen     string
		wantGroups []string
		hidden     []string
	}{
		{
			name:       "background",
			screen:     ScreenBackground,
			wantGroups: []string{"Run...", "Check...", "Logs...", "Inspect..."},
			hidden:     []string{"Run Worker Selfcheck Once", "Verify Backup"},
		},
		{
			name:       "jobs",
			screen:     ScreenJobs,
			wantGroups: []string{"Run...", "Indexing...", "Repair...", "Check...", "Logs..."},
			hidden:     []string{"Retry Failed Index Work", "Explain Object Index", "Inspect Job Logs"},
		},
		{
			name:       "automations",
			screen:     ScreenAutomations,
			wantGroups: []string{"Run...", "Controls...", "Check...", "Logs..."},
			hidden:     []string{"Fire Schedule Now", "Inspect Direct Event Raw Payload"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", tc.screen, Snapshot{})
			output := RenderScreenWithState(RenderInput{
				Mode:     testMode(),
				Registry: actions.DefaultRegistry(),
				Screen:   tc.screen,
				State:    result.State,
			})
			if !strings.Contains(output, "Available Actions") {
				t.Fatalf("%s output missing grouped actions:\n%s", tc.name, output)
			}
			for _, want := range tc.wantGroups {
				if !strings.Contains(output, want) {
					t.Fatalf("%s output missing group %q:\n%s", tc.name, want, output)
				}
			}
			for _, hidden := range tc.hidden {
				if strings.Contains(output, hidden) {
					t.Fatalf("%s output should hide flat action %q by default:\n%s", tc.name, hidden, output)
				}
			}
		})
	}
}

func TestOperationalActionGroupsExpandToOriginalActions(t *testing.T) {
	background := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBackground, Snapshot{}).State
	background.ExpandedActionGroup = operationalActionGroupRef(ScreenBackground, "check")
	if action := findGroupedSelectableAction(background, "maintenance.backup.backup_operation_test.verify"); action.Executor.Kind != PortalExecutorMaintenanceBackupVerify {
		t.Fatalf("background check group verify action = %#v", action)
	}
	backupRow := findSelectableRecord(background, "maintenance_backup_operation")
	if action := findRelatedAction(backupRow, PortalExecutorMaintenanceBackupVerify); action.ID == "" {
		t.Fatalf("backup row lost verify action: %#v", backupRow.RelatedActions)
	}

	jobsState := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenJobs, Snapshot{}).State
	jobsState.ExpandedActionGroup = operationalActionGroupRef(ScreenJobs, "retry")
	if action := findGroupedSelectableAction(jobsState, "index.retry_failed"); action.Executor.Kind != PortalExecutorIndexRetryFailed {
		t.Fatalf("jobs retry group action = %#v", action)
	}

	automationState := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenAutomations, Snapshot{}).State
	automationState.ExpandedActionGroup = operationalActionGroupRef(ScreenAutomations, "controls")
	if action := findGroupedSelectableAction(automationState, "automation.schedule.schedule_test.fire_now"); action.Executor.Kind != PortalExecutorScheduleFireNow {
		t.Fatalf("automation controls group action = %#v", action)
	}
}

func TestPortalActionSectionsRenderOnActionHeavySurfaces(t *testing.T) {
	tests := []struct {
		screen string
		want   []string
	}{
		{ScreenStorage, []string{"Protection", "Cloud", "Mac Mount", "Archives And Recovery"}},
		{ScreenBox, []string{"Box Setup", "Watch Policy", "Projects"}},
		{ScreenDatabase, []string{"Search And Indexing", "Objects", "Deletion Requests", "Sync Diagnostics", "Workers", "Raw Diagnostics"}},
		{ScreenNodes, []string{"Node Health", "Watched Roots", "Sync", "Private Backups", "Deletion Requests", "Diagnostics"}},
		{ScreenNotes, []string{"Inspect", "Policies"}},
		{ScreenProjects, []string{"Lifecycle"}},
		{ScreenJobs, []string{"Run Workers", "Search Indexing", "Repair", "Inspect", "Logs And Outputs"}},
		{ScreenAutomations, []string{"Run Workers", "Controls", "Inspect", "Logs And Payloads"}},
	}
	for _, tc := range tests {
		t.Run(tc.screen, func(t *testing.T) {
			result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", tc.screen, Snapshot{})
			output := RenderScreenWithState(RenderInput{
				Mode:     testMode(),
				Registry: actions.DefaultRegistry(),
				Screen:   tc.screen,
				State:    result.State,
				Height:   80,
			})
			if !strings.Contains(output, "Available Actions") {
				t.Fatalf("%s output missing Available Actions:\n%s", tc.screen, output)
			}
			for _, want := range tc.want {
				if !strings.Contains(output, "\n  "+want+"\n") {
					t.Fatalf("%s output missing action subsection %q:\n%s", tc.screen, want, output)
				}
			}
		})
	}
}

func TestDeletionRequestActionsAreCompactedOnDatabaseAndNodes(t *testing.T) {
	tests := []struct {
		screen string
	}{
		{ScreenDatabase},
		{ScreenNodes},
	}
	for _, tc := range tests {
		t.Run(tc.screen, func(t *testing.T) {
			state := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", tc.screen, Snapshot{}).State
			output := RenderScreenWithState(RenderInput{
				Mode:     testMode(),
				Registry: actions.DefaultRegistry(),
				Screen:   tc.screen,
				State:    state,
				Height:   80,
			})
			if !strings.Contains(output, "Deletion Requests...") {
				t.Fatalf("%s output missing compact deletion request group:\n%s", tc.screen, output)
			}
			if strings.Contains(output, "Approve Deletion Request") || strings.Contains(output, "Deny Deletion Request") || strings.Contains(output, "Complete Deletion Request") {
				t.Fatalf("%s output rendered deletion lifecycle actions flat:\n%s", tc.screen, output)
			}

			state.ExpandedActionGroup = operationalActionGroupRef(tc.screen, "deletion_requests")
			expanded := RenderScreenWithState(RenderInput{
				Mode:     testMode(),
				Registry: actions.DefaultRegistry(),
				Screen:   tc.screen,
				State:    state,
				Height:   80,
			})
			if !strings.Contains(expanded, "Approve Deletion Request") || !strings.Contains(expanded, "Complete Deletion Request") {
				t.Fatalf("%s expanded deletion group did not expose lifecycle actions:\n%s", tc.screen, expanded)
			}
			if action := findGroupedSelectableAction(state, "deletion_request.deletion_request_test.approve"); action.Executor.Kind != PortalExecutorDeletionRequestApprove {
				t.Fatalf("%s grouped approve action = %#v", tc.screen, action)
			}
		})
	}
}

func TestScreenActionItemsMapStaticScreenActions(t *testing.T) {
	items := ScreenActionItems(ScreenBackground)
	if len(items) != 2 {
		t.Fatalf("background action items = %d, want 2", len(items))
	}
	if items[0].Kind != SelectableKindAction || items[0].ActionID != "worker.selfcheck.run_once" || items[0].RecordRef != "main.worker_selfcheck" {
		t.Fatalf("unexpected first action item: %#v", items[0])
	}
	action, ok := PortalActionFromSelectableItem(items[0])
	if !ok {
		t.Fatal("expected selectable item to resolve to a portal action")
	}
	if action.ID != "worker.selfcheck.run_once" || action.Risk != ActionRiskSafeRun {
		t.Fatalf("unexpected portal action: %#v", action)
	}
}

func TestScreenAvailableActionsUseLiveWorkerRecords(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBackground, Snapshot{})
	available := ScreenAvailableActions(result.State)
	action, ok := findPortalAction(available, "worker.main_worker_selfcheck.run_once")
	if !ok {
		t.Fatalf("dynamic worker run action missing: %#v", available)
	}
	if action.Executor.Target != "main.worker_selfcheck" {
		t.Fatalf("executor target = %q, want live worker key", action.Executor.Target)
	}
	if action.Label != "Run Worker Selfcheck Once" {
		t.Fatalf("label = %q", action.Label)
	}
}

func TestOperatorStaticActionsHideFromDefaultTopButRemainReachable(t *testing.T) {
	state := NewScreenState(ScreenAutomations)

	if _, ok := findPortalAction(ScreenAvailableActions(state), "worker.automation_scheduler.run_once"); !ok {
		t.Fatal("static automation worker shortcut should remain in available actions")
	}
	if _, ok := findPortalAction(ScreenActions(state), "worker.automation_scheduler.run_once"); !ok {
		t.Fatal("static automation worker shortcut should remain in screen action registry")
	}
	if _, ok := findPortalAction(screenTopAvailableActionsForRecords(state, ScreenRecordItems(state)), "worker.automation_scheduler.run_once"); ok {
		t.Fatal("static automation worker shortcut should not render in the default top action block")
	}
	for _, item := range ScreenSelectableItems(state) {
		if item.Kind == SelectableKindAction && item.ActionID == "worker.automation_scheduler.run_once" {
			t.Fatalf("static automation worker shortcut should not be a default selectable top action: %#v", item)
		}
	}

	state.RawDetails = true
	if _, ok := findPortalAction(screenTopAvailableActionsForRecords(state, ScreenRecordItems(state)), "worker.automation_scheduler.run_once"); !ok {
		t.Fatal("raw/details mode should expose static automation worker shortcut in top actions")
	}
	foundRawSelectable := false
	for _, item := range ScreenSelectableItems(state) {
		if item.Kind == SelectableKindAction && item.ActionID == "worker.automation_scheduler.run_once" {
			foundRawSelectable = true
			break
		}
	}
	if !foundRawSelectable {
		t.Fatal("raw/details mode should make static automation worker shortcut selectable")
	}
}

func TestPortalActionPresentationClassifiesStaticAndLiveWorkerRuns(t *testing.T) {
	staticItems := StaticScreenActionItems(ScreenAutomations)
	if len(staticItems) == 0 {
		t.Fatal("expected static automation worker shortcuts")
	}
	staticAction, ok := PortalActionFromSelectableItem(staticItems[0])
	if !ok {
		t.Fatal("expected static selectable item to resolve to portal action")
	}
	if got := portalActionPresentationFor(staticAction); got != portalActionPresentationOperator {
		t.Fatalf("static worker shortcut presentation = %s, want %s", got, portalActionPresentationOperator)
	}

	liveAction := NewWorkerRunOnceAction(workers.WorkerListItem{
		WorkerKey:       "main.worker_selfcheck",
		WorkerKind:      workers.KindSelfcheck,
		DisplayName:     "Worker Selfcheck",
		LifecycleStatus: workers.LifecycleActive,
		Enabled:         true,
	}, ScreenBackground)
	if got := portalActionPresentationFor(liveAction); got != portalActionPresentationContextual {
		t.Fatalf("live worker run presentation = %s, want %s", got, portalActionPresentationContextual)
	}
}

func TestArchivedProjectQuarantineSuppressesWorkerRunActions(t *testing.T) {
	project := fakeArchivedPortalProject()
	state := NewScreenState(ScreenBackground)
	state.Data.Background = BackgroundData{
		ArchivedProjects: []projects.Project{project},
		Workers: []workers.WorkerListItem{{
			WorkerInstanceID: "main@portal-project.worker-instance",
			WorkerKey:        "main@portal-project.worker",
			WorkerKind:       workers.KindSelfcheck,
			DisplayName:      "Archived Project Worker",
			LifecycleStatus:  workers.LifecycleActive,
			Enabled:          true,
		}},
	}

	for _, action := range ScreenAvailableActions(state) {
		if action.Executor.Kind == PortalExecutorWorkerRunOnce {
			t.Fatalf("archived project worker exposed run-once action in available actions: %#v", action)
		}
	}
	for _, action := range ScreenActions(state) {
		if action.Executor.Kind == PortalExecutorWorkerRunOnce {
			t.Fatalf("archived project worker exposed run-once action in screen actions: %#v", action)
		}
	}
	for _, item := range ScreenRecordItems(state) {
		if item.RecordKind != "worker" {
			continue
		}
		if item.PrimaryAction == nil || item.PrimaryAction.Executor.Kind != PortalExecutorWorkerInspect {
			t.Fatalf("archived worker should remain inspectable: %#v", item)
		}
		if action := findRelatedAction(item, PortalExecutorWorkerRunsInspect); action.ID == "" {
			t.Fatalf("archived worker should keep runs inspection: %#v", item.RelatedActions)
		}
		if action := findRelatedAction(item, PortalExecutorWorkerRunOnce); action.ID != "" {
			t.Fatalf("archived worker exposed related run-once action: %#v", item.RelatedActions)
		}
		return
	}
	t.Fatal("expected archived project worker row")
}

func TestDynamicWorkerRunActionExplainsDisabledWorkers(t *testing.T) {
	action := NewWorkerRunOnceAction(workers.WorkerListItem{
		WorkerKey:       "main.disabled_worker",
		WorkerKind:      workers.KindSelfcheck,
		LifecycleStatus: workers.LifecycleDisabled,
		Enabled:         false,
	}, ScreenBackground)
	if !action.Disabled() {
		t.Fatalf("expected disabled action: %#v", action)
	}
	if !strings.Contains(action.DisabledReason, "not active") {
		t.Fatalf("disabled reason = %q", action.DisabledReason)
	}
}

func TestModelEnterOnWorkerRowOpensInspectPreview(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBackground, Snapshot{})
	model := NewModelWithOptions(ModelOptions{
		Mode:          testMode(),
		Client:        newFakePortalClient(),
		CorrelationID: "corr_test",
		Snapshot:      fakeSnapshot(),
		Registry:      actions.DefaultRegistry(),
		StartScreen:   ScreenBackground,
	})
	model.setScreenState(ScreenBackground, result.State)
	state := model.currentScreenState()
	workerIndex := -1
	for _, item := range ScreenSelectableItems(state) {
		if item.RecordKind == "worker" {
			workerIndex = item.RowIndex
			break
		}
	}
	if workerIndex < 0 {
		t.Fatalf("worker row missing: %#v", ScreenSelectableItems(state))
	}
	state.SelectedIndex = workerIndex
	model.setScreenState(ScreenBackground, state)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("opening worker inspect preview should not execute immediately")
	}
	if model.actionPanel.Lifecycle != ActionLifecyclePreview {
		t.Fatalf("lifecycle = %s, want preview", model.actionPanel.Lifecycle)
	}
	if model.actionPanel.Action.Executor.Kind != PortalExecutorWorkerInspect {
		t.Fatalf("executor = %s", model.actionPanel.Action.Executor.Kind)
	}
}

func TestModelEnterOnAvailableActionRowOpensPreview(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBackground, Snapshot{})
	groupIndex := -1
	for _, item := range ScreenSelectableItems(result.State) {
		if selectableIsOperationalActionGroup(item) && item.RecordRef == operationalActionGroupRef(ScreenBackground, "run") {
			groupIndex = item.RowIndex
			break
		}
	}
	if groupIndex < 0 {
		t.Fatalf("background run group missing: %#v", ScreenSelectableItems(result.State))
	}
	result.State.SelectedIndex = groupIndex

	model := NewModelWithOptions(ModelOptions{
		Mode:          testMode(),
		Client:        newFakePortalClient(),
		CorrelationID: "corr_test",
		Snapshot:      fakeSnapshot(),
		Registry:      actions.DefaultRegistry(),
		StartScreen:   ScreenBackground,
	})
	model.setScreenState(ScreenBackground, result.State)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("expanding an action group should not execute immediately")
	}
	state := model.currentScreenState()
	if state.ExpandedActionGroup != operationalActionGroupRef(ScreenBackground, "run") {
		t.Fatalf("expanded group = %q", state.ExpandedActionGroup)
	}
	actionIndex := -1
	for _, item := range ScreenSelectableItems(state) {
		if selectableIsOperationalActionGroupAction(item) && item.ActionID == "maintenance.backup.run" {
			actionIndex = item.RowIndex
			break
		}
	}
	if actionIndex < 0 {
		t.Fatalf("maintenance backup action missing from expanded group: %#v", ScreenSelectableItems(state))
	}
	state.SelectedIndex = actionIndex
	model.setScreenState(ScreenBackground, state)

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("opening grouped action preview should not execute immediately")
	}
	if model.actionPanel.Lifecycle != ActionLifecyclePreview {
		t.Fatalf("lifecycle = %s, want preview", model.actionPanel.Lifecycle)
	}
	if model.actionPanel.Action.ID != "maintenance.backup.run" {
		t.Fatalf("action = %s, want maintenance.backup.run", model.actionPanel.Action.ID)
	}
}

func TestResolvePortalActionFindsDynamicScreenActions(t *testing.T) {
	action, err := ResolvePortalAction(context.Background(), ActionResolver{
		Registry:      actions.DefaultRegistry(),
		Client:        newFakePortalClient(),
		CorrelationID: "corr_test",
	}, "worker.main_worker_selfcheck.run_once", ScreenBackground, Snapshot{})
	if err != nil {
		t.Fatalf("ResolvePortalAction returned error: %v", err)
	}
	if action.Executor.Kind != PortalExecutorWorkerRunOnce || action.Executor.Target != "main.worker_selfcheck" {
		t.Fatalf("unexpected action: %#v", action)
	}
}

func TestModelTracksScreenStateRefreshSelectionDetailsAndWindow(t *testing.T) {
	model := NewModelWithOptions(ModelOptions{
		Mode:          testMode(),
		Client:        newFakePortalClient(),
		CorrelationID: "corr_test",
		Snapshot:      fakeSnapshot(),
		Registry:      actions.DefaultRegistry(),
		StartScreen:   ScreenBackground,
	})

	if model.currentScreenState().Status != ScreenLoadLoading {
		t.Fatalf("initial status = %s, want loading", model.currentScreenState().Status)
	}
	cmd := model.Init()
	if cmd == nil {
		t.Fatal("expected non-home initial screen to return a load command")
	}
	msg := cmd()
	updated, _ := model.Update(msg)
	model = updated.(Model)
	if model.currentScreenState().Status != ScreenLoadLoaded {
		t.Fatalf("status after initial load = %s, want loaded", model.currentScreenState().Status)
	}

	updated, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	if model.width != 100 || model.height != 30 {
		t.Fatalf("window size = %dx%d, want 100x30", model.width, model.height)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if got := model.currentScreenState().SelectedIndex; got != 1 {
		t.Fatalf("selected index after down = %d, want 1", got)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if !model.currentScreenState().RawDetails {
		t.Fatal("expected tab to toggle raw details")
	}
	if !strings.Contains(model.View(), "Raw Details") {
		t.Fatalf("raw details toggle not reflected in view:\n%s", model.View())
	}

	updatedModel, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updatedModel.(Model)
	if cmd == nil {
		t.Fatal("expected refresh to return a load command")
	}
	if model.currentScreenState().Status != ScreenLoadLoading {
		t.Fatalf("status after refresh = %s, want loading", model.currentScreenState().Status)
	}

	msg = cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	if model.currentScreenState().Status != ScreenLoadLoaded {
		t.Fatalf("status after load = %s, want loaded", model.currentScreenState().Status)
	}
}

func updateModelWithRunes(t *testing.T, model Model, value string) Model {
	t.Helper()
	for _, r := range value {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(Model)
	}
	return model
}

func modelWithSelfcheckPreview(t *testing.T, client *fakePortalClient) Model {
	t.Helper()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenBackground, Snapshot{})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned unexpected error: %v", result.Err)
	}
	action, ok := findPortalAction(ScreenAvailableActions(result.State), "worker.main_worker_selfcheck.run_once")
	if !ok {
		t.Fatalf("dynamic worker run action missing")
	}
	model := NewModelWithOptions(ModelOptions{
		Mode:          testMode(),
		Client:        client,
		CorrelationID: "corr_test",
		Snapshot:      fakeSnapshot(),
		Registry:      actions.DefaultRegistry(),
		StartScreen:   ScreenHome,
	})
	model.screen = ScreenBackground
	model.setScreenState(ScreenBackground, result.State)
	model.actionPanel = ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action}
	if model.actionPanel.Lifecycle != ActionLifecyclePreview {
		t.Fatalf("lifecycle = %s, want preview", model.actionPanel.Lifecycle)
	}
	return model
}

func TestLoadScreenFromSnapshotScopesPartialErrors(t *testing.T) {
	snapshot := fakeSnapshot()
	snapshot.PartialErrors = []SnapshotError{
		{Source: "workers", Message: "workers unavailable"},
		{Source: "nodes", Message: "nodes unavailable"},
	}

	background := LoadScreenFromSnapshot(ScreenBackground, snapshot)
	if background.State.Status != ScreenLoadPartial || len(background.State.PartialErrors) != 1 || background.State.PartialErrors[0].Source != "workers" {
		t.Fatalf("background partials unexpected: %#v", background.State)
	}

	nodes := LoadScreenFromSnapshot(ScreenNodes, snapshot)
	if nodes.State.Status != ScreenLoadPartial || len(nodes.State.PartialErrors) != 1 || nodes.State.PartialErrors[0].Source != "nodes" {
		t.Fatalf("nodes partials unexpected: %#v", nodes.State)
	}
}

func TestLoadScreenUsesScreenSpecificBackendCalls(t *testing.T) {
	client := newFakePortalClient()
	client.healthErr = errors.New("health should not be needed for background refresh")
	client.statusErr = errors.New("status should not be needed for background refresh")

	result := LoadScreen(context.Background(), client, "corr_test", ScreenBackground, Snapshot{})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned unexpected error: %v", result.Err)
	}
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("background status = %s, want loaded; partials=%#v", result.State.Status, result.State.PartialErrors)
	}
	if got := len(result.State.Data.Background.Workers); got != 1 {
		t.Fatalf("background workers = %d, want 1", got)
	}
}

func TestLoadScreenPartialErrorsAreScreenSpecific(t *testing.T) {
	client := newFakePortalClient()
	client.workerRunsErr = errors.New("worker runs unavailable")

	result := LoadScreen(context.Background(), client, "corr_test", ScreenBackground, Snapshot{})
	if result.State.Status != ScreenLoadPartial {
		t.Fatalf("background status = %s, want partial", result.State.Status)
	}
	if !hasPartialSource(result.State.PartialErrors, "worker_runs") {
		t.Fatalf("missing worker_runs partial: %#v", result.State.PartialErrors)
	}
	if got := len(result.State.Data.Background.Workers); got != 1 {
		t.Fatalf("workers should still render on partial load, got %d", got)
	}
}

func TestLoadScreenHomeStillUsesBootSnapshot(t *testing.T) {
	client := newFakePortalClient()
	client.healthErr = errors.New("health unavailable")

	result := LoadScreen(context.Background(), client, "corr_test", ScreenHome, Snapshot{})
	if result.Err != nil {
		t.Fatalf("home load returned error: %v", result.Err)
	}
	if result.Snapshot.MainAvailability.State != MainAvailabilityOffline {
		t.Fatalf("availability = %q, want offline", result.Snapshot.MainAvailability.State)
	}
}

func TestLoadScreenCapabilitiesLoadsBackendData(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenCapabilities, Snapshot{})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned unexpected error: %v", result.Err)
	}
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("capabilities status = %s, want loaded; partials=%#v", result.State.Status, result.State.PartialErrors)
	}
	if got := len(result.State.Data.Capabilities.Providers); got != 1 {
		t.Fatalf("providers = %d, want 1", got)
	}
	if got := len(result.State.Data.Capabilities.Capabilities); got != 1 {
		t.Fatalf("capabilities = %d, want 1", got)
	}
}

func TestRenderCapabilitiesUsesLiveBackendData(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenCapabilities, Snapshot{})
	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenCapabilities,
		State:    result.State,
	})
	for _, want := range []string{"Tools By Task", "Diagnostics", "risk=low", "auth=1", "Capability Explorer", "Capabilities", "main", "1 providers, 1 capabilities", "Provider Advertisements"} {
		if !strings.Contains(output, want) {
			t.Fatalf("capability output missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "Tools By Task") > strings.Index(output, "Capability Explorer") {
		t.Fatalf("capability output should show task summary before provider explorer:\n%s", output)
	}
	if strings.Contains(output, "Capability forms are preview-only") {
		t.Fatalf("capabilities screen still renders placeholder:\n%s", output)
	}
}

func TestCapabilitiesHideNonActionableProviderAdvertisementsByDefault(t *testing.T) {
	state := NewScreenState(ScreenCapabilities)
	state.Status = ScreenLoadLoaded
	state.Data.Capabilities = CapabilitiesData{
		Advertisements: []capabilities.ProviderAdvertisement{{
			ProviderAdvertisementID: "provider_advertisement_accepted",
			OriginNodeID:            "node_workspace",
			Status:                  "accepted",
			ReceivedAt:              time.Now().UTC(),
		}},
		Explorer: defaultCapabilityExplorerState(),
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenCapabilities, State: state, Registry: actions.DefaultRegistry()})
	if strings.Contains(output, "Provider Advertisements") || strings.Contains(output, "provider_advertisement_accepted") {
		t.Fatalf("capabilities default render should hide non-actionable provider advertisements:\n%s", output)
	}
	details := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenCapabilities, State: state.ToggleRawDetails(), Registry: actions.DefaultRegistry()})
	if !strings.Contains(details, "Provider Advertisements") || !strings.Contains(details, "provider_advertisement_accepted") {
		t.Fatalf("capabilities raw details should expose provider advertisements:\n%s", details)
	}
}

func TestRenderCapabilitiesDoesNotUseStaticRegistryAsData(t *testing.T) {
	state := NewScreenState(ScreenCapabilities)
	state.Status = ScreenLoadLoaded
	state.LoadedAt = time.Now().UTC()
	state.Data.Capabilities = CapabilitiesData{}

	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenCapabilities,
		State:    state,
	})
	if strings.Contains(output, "Capabilities And Providers -> loom capabilities list") {
		t.Fatalf("capabilities screen rendered static registry action as data:\n%s", output)
	}
	for _, want := range []string{"No providers returned by the backend.", "No capabilities returned by the backend."} {
		if !strings.Contains(output, want) {
			t.Fatalf("capability empty state missing %q:\n%s", want, output)
		}
	}
}

func TestCapabilitiesPartialErrorStillRendersProviders(t *testing.T) {
	client := newFakePortalClient()
	client.capabilitiesErr = errors.New("capability list unavailable")

	result := LoadScreen(context.Background(), client, "corr_test", ScreenCapabilities, Snapshot{})
	if result.State.Status != ScreenLoadPartial {
		t.Fatalf("capabilities status = %s, want partial", result.State.Status)
	}
	if !hasPartialSource(result.State.PartialErrors, "capabilities") {
		t.Fatalf("missing capabilities partial: %#v", result.State.PartialErrors)
	}
	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenCapabilities,
		State:    result.State,
	})
	if !strings.Contains(output, "main") || !strings.Contains(output, "capability list unavailable") {
		t.Fatalf("partial capability output did not keep provider scope and error:\n%s", output)
	}
}

func TestBackgroundScreenShowsWorkerRuns(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBackground, Snapshot{})
	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenBackground,
		State:    result.State,
	})
	if !strings.Contains(output, "last_run=succeeded") || !strings.Contains(output, "trigger=manual") {
		t.Fatalf("background output missing worker run context:\n%s", output)
	}
}

func TestBackgroundScreenLoadsAndRendersMaintenanceSurfaces(t *testing.T) {
	updateStateDir := t.TempDir()
	if err := update.WriteUpdateManifest(update.ActiveManifestPath(updateStateDir), update.UpdateManifest{
		SchemaVersion: update.UpdateManifestSchemaVersion,
		UpdateID:      "update_portal_test",
		Status:        update.UpdateStatusSucceeded,
		StartedAt:     time.Now().UTC().Add(-time.Hour),
		Active:        update.ReleaseState{Path: "/srv/loom/releases/current"},
		Target:        update.ReleaseState{Path: "/srv/loom/releases/target"},
		Rollback: update.RollbackPlan{
			Class:               update.RollbackClassServiceOnly,
			ServiceOnlyPossible: true,
		},
		Maintenance: &update.MaintenanceWindow{
			SchemaVersion: update.MaintenanceWindowSchemaVersion,
			WindowID:      "window_portal_test",
			UpdateID:      "update_portal_test",
			Status:        update.MaintenanceWindowStatusResumed,
			StartedAt:     time.Now().UTC().Add(-time.Hour),
			PausePolicy:   update.DefaultMaintenancePausePolicy(),
		},
	}); err != nil {
		t.Fatalf("WriteUpdateManifest: %v", err)
	}

	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBackground, Snapshot{}, SnapshotOptions{UpdateStateDir: updateStateDir})
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("background status = %s, want loaded; partials=%#v", result.State.Status, result.State.PartialErrors)
	}
	data := result.State.Data.Background
	if data.DBStatus.Status == "" || data.BackupStatus.Status == "" || len(data.BackupOperations) != 1 || data.ObjectStoreStatus.Status == "" || data.UpdateStatus.Active == nil {
		t.Fatalf("maintenance data missing: %#v", data)
	}
	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenBackground,
		State:    result.State,
	})
	for _, want := range []string{"Run...", "Check...", "Database Maintenance", "loom maintenance retention dry-run --json", "Main Backups", "backup_operation_test", "Cloud Snapshots", "provider=hetzner_storage_box", "Production Updates", "update_portal_test", "service_only_possible", "Object Store"} {
		if !strings.Contains(output, want) {
			t.Fatalf("background maintenance output missing %q:\n%s", want, output)
		}
	}
}

func TestLoadAutomationsScreenLoadsLiveSummaries(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenAutomations, Snapshot{})
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("automations status = %s, want loaded; partials=%#v", result.State.Status, result.State.PartialErrors)
	}
	data := result.State.Data.Automations
	if len(data.Automations) != 1 || len(data.Integrations) != 1 || len(data.ScheduleFires) != 1 || len(data.Invocations) != 1 || len(data.DirectEvents) != 1 {
		t.Fatalf("automation live summaries missing: %#v", data)
	}
}

func TestLoadJobsScreenLoadsLiveSummaries(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenJobs, Snapshot{})
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("jobs status = %s, want loaded; partials=%#v", result.State.Status, result.State.PartialErrors)
	}
	data := result.State.Data.Jobs
	if len(data.Jobs) != 1 || len(data.QueuedJobs) != 1 || len(data.FailedJobs) != 1 || len(data.Runners) != 1 || len(data.IndexStatuses) != 1 || len(data.IndexQueue) != 1 {
		t.Fatalf("job live summaries missing: %#v", data)
	}
}

func TestLoadNodesScreenLoadsLiveSummaries(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenNodes, Snapshot{})
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("nodes status = %s, want loaded; partials=%#v", result.State.Status, result.State.PartialErrors)
	}
	data := result.State.Data.Nodes
	if len(data.BackupBatches) != 1 || len(data.SyncBatches) != 1 || len(data.SyncConflicts) != 1 || len(data.SyncReplicas) != 1 || len(data.PrivateBackups) != 1 || len(data.DeletionRequests) != 1 {
		t.Fatalf("node live summaries missing: %#v", data)
	}
}

func TestRenderAutomationJobsNodesUseLiveFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		screen  string
		want    []string
		notWant []string
		rawWant []string
	}{
		{
			name:    "automations",
			screen:  ScreenAutomations,
			want:    []string{"Automations", "Run...", "Controls...", "Automation Failures", "Doctor owns 2 automation issue", "Daily Backup", "Recent Schedules", "Direct Events"},
			notWant: []string{"Integrations", "schedule_fire_test", "direct_event_recent", "invocation_recent"},
			rawWant: []string{"Integrations", "Gmail", "Recent Schedule Fires", "schedule_fire_test", "Recent Direct Events", "direct_event_recent", "Recent Invocations", "invocation_recent"},
		},
		{
			name:    "jobs",
			screen:  ScreenJobs,
			want:    []string{"Run...", "Repair...", "Check...", "Logs...", "Job Queue", "Jobs Attention", "Doctor owns 2 jobs/indexing issue", "Running Jobs", "job_recent", "Queued Jobs", "job_queued", "Runner Health", jobs.DefaultRunnerKey, "Indexing"},
			notWant: []string{"Failed Jobs", "job_failed", "Index Failures", "index_status_test", "Index Queue", "index_queue_test", "Recent Index Statuses", "index_status_recent"},
			rawWant: []string{"Failed Jobs", "job_failed", "Index Failures", "index_status_test", "Index Queue", "index_queue_test", "Recent Index Statuses", "index_status_recent"},
		},
		{
			name:    "nodes",
			screen:  ScreenNodes,
			want:    []string{"Attention", "Doctor owns 2 node issue", "Node Cards", "Protected Folders", "Active Watched Roots", "macbook", "Diagnostics"},
			notWant: []string{"deletion object"},
			rawWant: []string{"conflict", "deletion object", "Raw Sync Conflicts", "Raw Deletion Requests"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", tc.screen, Snapshot{})
			output := RenderScreenWithState(RenderInput{
				Mode:     testMode(),
				Registry: actions.DefaultRegistry(),
				Screen:   tc.screen,
				State:    result.State,
			})
			for _, want := range tc.want {
				if !strings.Contains(output, want) {
					t.Fatalf("%s output missing %q:\n%s", tc.name, want, output)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(output, notWant) {
					t.Fatalf("%s default output should route %q to Doctor/raw details:\n%s", tc.name, notWant, output)
				}
			}
			rawOutput := RenderScreenWithState(RenderInput{
				Mode:     testMode(),
				Registry: actions.DefaultRegistry(),
				Screen:   tc.screen,
				State:    result.State.ToggleRawDetails(),
				Height:   80,
			})
			for _, want := range tc.rawWant {
				if !strings.Contains(rawOutput, want) {
					t.Fatalf("%s raw output missing %q:\n%s", tc.name, want, rawOutput)
				}
			}
		})
	}
}

func TestOperationalSurfacesPrioritizeAttention(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen string
		order  [][2]string
	}{
		{
			name:   "background",
			screen: ScreenBackground,
			order: [][2]string{
				{"Backup And Cloud Protection", "Worker Attention"},
				{"Worker Attention", "Database Maintenance"},
			},
		},
		{
			name:   "automations",
			screen: ScreenAutomations,
			order: [][2]string{
				{"Automation Failures", "Active Automations"},
				{"Active Automations", "Recent Schedules"},
				{"Recent Schedules", "Direct Events"},
			},
		},
		{
			name:   "jobs",
			screen: ScreenJobs,
			order: [][2]string{
				{"Job Queue", "Jobs Attention"},
				{"Jobs Attention", "Running Jobs"},
				{"Running Jobs", "Queued Jobs"},
				{"Runner Health", "\nIndexing\n"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", tc.screen, Snapshot{})
			output := RenderScreenWithState(RenderInput{
				Mode:     testMode(),
				Registry: actions.DefaultRegistry(),
				Screen:   tc.screen,
				State:    result.State,
			})
			for _, pair := range tc.order {
				first := strings.Index(output, pair[0])
				second := strings.Index(output, pair[1])
				if first < 0 || second < 0 || first > second {
					t.Fatalf("expected %q before %q:\n%s", pair[0], pair[1], output)
				}
			}
		})
	}
}

func TestDeclutteredOperationalSelectablesAreRendered(t *testing.T) {
	for _, screen := range []string{ScreenBackground, ScreenAutomations, ScreenJobs, ScreenNodes} {
		t.Run(screen, func(t *testing.T) {
			result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", screen, Snapshot{})
			output := RenderScreenWithState(RenderInput{
				Mode:     testMode(),
				Registry: actions.DefaultRegistry(),
				Screen:   screen,
				State:    result.State,
				Height:   80,
			})
			for _, item := range ScreenSelectableItems(result.State) {
				if selectableIsOperationalActionPresentation(item) {
					continue
				}
				label := firstNonEmpty(item.Label, item.RecordLabel, item.RecordRef, item.ActionID)
				if strings.TrimSpace(label) == "" {
					continue
				}
				if !strings.Contains(output, label) {
					t.Fatalf("%s selectable %q was not rendered:\nitems=%#v\n%s", screen, label, ScreenSelectableItems(result.State), output)
				}
			}
		})
	}
}

func TestDeclutteredActionInventoryKeepsGroupedAndRawActions(t *testing.T) {
	jobsState := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenJobs, Snapshot{}).State
	jobsInventory := ActionInventoryForScreen(jobsState)
	for _, want := range []string{"job.job_failed.retry", "index.retry_failed"} {
		if !inventoryHasAction(jobsInventory, want) {
			t.Fatalf("jobs default inventory missing grouped action %s: %#v", want, jobsInventory)
		}
	}
	jobsRawInventory := ActionInventoryForScreen(jobsState.ToggleRawDetails())
	if !inventoryHasAction(jobsRawInventory, "index.index_status_test.inspect") {
		t.Fatalf("jobs raw inventory missing index inspect action: %#v", jobsRawInventory)
	}

	automationState := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenAutomations, Snapshot{}).State
	automationInventory := ActionInventoryForScreen(automationState)
	for _, want := range []string{"automation.schedule.schedule_test.fire_now", "automation.direct_endpoint.direct_event_endpoint_test.test"} {
		if !inventoryHasAction(automationInventory, want) {
			t.Fatalf("automation default inventory missing grouped action %s: %#v", want, automationInventory)
		}
	}
	automationRawInventory := ActionInventoryForScreen(automationState.ToggleRawDetails())
	if !inventoryHasAction(automationRawInventory, "automation.direct_event.direct_event_recent.raw_payload") {
		t.Fatalf("automation raw inventory missing direct-event payload action: %#v", automationRawInventory)
	}
}

func TestScreenSpecificPartialSources(t *testing.T) {
	for _, tc := range []struct {
		screen string
		source string
	}{
		{ScreenBackground, "worker_runs"},
		{ScreenBackground, "maintenance_db"},
		{ScreenBackground, "maintenance_backup_status"},
		{ScreenBackground, "maintenance_backup_list"},
		{ScreenBackground, "maintenance_object_store"},
		{ScreenCapabilities, "providers"},
		{ScreenCapabilities, "capabilities"},
		{ScreenCapabilities, "provider_advertisements"},
		{ScreenAutomations, "automations"},
		{ScreenAutomations, "integrations"},
		{ScreenAutomations, "schedule_fires"},
		{ScreenAutomations, "invocations"},
		{ScreenAutomations, "direct_event_list"},
		{ScreenJobs, "job_list"},
		{ScreenJobs, "queued_jobs"},
		{ScreenJobs, "failed_jobs"},
		{ScreenJobs, "runners"},
		{ScreenJobs, "runner_status"},
		{ScreenJobs, "job_workers"},
		{ScreenJobs, "index_status"},
		{ScreenJobs, "index_queue"},
		{ScreenNodes, "watched_root_backup_status"},
		{ScreenNodes, "watched_root_backup_batches"},
		{ScreenNodes, "sync_status"},
		{ScreenNodes, "sync_batches"},
		{ScreenNodes, "sync_conflicts"},
		{ScreenNodes, "sync_replicas"},
		{ScreenNodes, "private_backups"},
		{ScreenNodes, "deletion_requests"},
		{ScreenStorage, "storage_entries"},
		{ScreenStorage, "storage_filesystem_status"},
		{ScreenStorage, "storage_retention_status"},
		{ScreenStorage, "storage_main_documents_status"},
	} {
		t.Run(tc.screen+"/"+tc.source, func(t *testing.T) {
			if !partialAppliesToScreen(tc.screen, tc.source) {
				t.Fatalf("source %s should apply to screen %s", tc.source, tc.screen)
			}
		})
	}
}

func TestModelCapabilitiesSelectionUsesLiveRows(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenCapabilities, Snapshot{})
	model := NewModelWithOptions(ModelOptions{
		Mode:        testMode(),
		Snapshot:    fakeSnapshot(),
		Registry:    actions.DefaultRegistry(),
		StartScreen: ScreenCapabilities,
	})
	model.setScreenState(ScreenCapabilities, result.State)

	if got := model.currentSelectableCount(); got != 1 {
		t.Fatalf("capability selectable count = %d, want 1", got)
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if got := model.currentScreenState().Data.Capabilities.Explorer.Level; got != CapabilityExplorerProviders {
		t.Fatalf("explorer level = %s, want providers", got)
	}
	if got := model.currentSelectableCount(); got != 1 {
		t.Fatalf("provider-level selectable count = %d, want 1", got)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(Model)
	if got := model.currentSelectableCount(); got != 3 {
		t.Fatalf("expanded provider-level selectable count = %d, want 3", got)
	}
	if output := model.View(); !strings.Contains(output, "Inspect Provider") || !strings.Contains(output, "Provider Health") || !strings.Contains(output, "main@system") || strings.Contains(output, "Available Actions") {
		t.Fatalf("expanded provider actions not rendered inline:\n%s", output)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("provider inline action preview should not execute immediately")
	}
	if model.actionPanel.Action.Executor.Kind != PortalExecutorProviderInspect {
		t.Fatalf("provider inline action executor = %s", model.actionPanel.Action.Executor.Kind)
	}
	model.actionPanel = ActionPanelState{}
	state := model.currentScreenState()
	state.SelectedIndex = 0
	model.setScreenState(ScreenCapabilities, state)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if got := model.currentScreenState().Data.Capabilities.Explorer.Level; got != CapabilityExplorerCapabilities {
		t.Fatalf("explorer level = %s, want capabilities", got)
	}
	if got := model.currentSelectableCount(); got != 1 {
		t.Fatalf("capability-level selectable count = %d, want 1", got)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(Model)
	if got := model.currentSelectableCount(); got != 4 {
		t.Fatalf("expanded capability-level selectable count = %d, want 4", got)
	}
	if output := model.View(); !strings.Contains(output, "Inspect Capability") || !strings.Contains(output, "View Usage Docs") || !strings.Contains(output, "Invoke Capability") || !strings.Contains(output, "main@system.status.read") || strings.Contains(output, "Available Actions") {
		t.Fatalf("expanded capability actions not rendered inline:\n%s", output)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if got := model.currentScreenState().SelectedIndex; got != 1 {
		t.Fatalf("selected index = %d, want 1", got)
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("capability inline action preview should not execute immediately")
	}
	if model.actionPanel.Action.Executor.Kind != PortalExecutorCapabilityInspect {
		t.Fatalf("capability inline action executor = %s", model.actionPanel.Action.Executor.Kind)
	}
	model.actionPanel = ActionPanelState{}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	model = updated.(Model)
	if got := model.currentScreenState().Data.Capabilities.Explorer.Level; got != CapabilityExplorerCapabilities {
		t.Fatalf("explorer level after closing actions = %s, want capabilities", got)
	}
	if got := model.currentSelectableCount(); got != 1 {
		t.Fatalf("capability selectable count after closing actions = %d, want 1", got)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	model = updated.(Model)
	if got := model.currentScreenState().Data.Capabilities.Explorer.Level; got != CapabilityExplorerProviders {
		t.Fatalf("explorer level after back = %s, want providers", got)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if got := model.currentScreenState().Data.Capabilities.Explorer.Level; got != CapabilityExplorerNodes {
		t.Fatalf("explorer level after escape = %s, want nodes", got)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.screen != ScreenHome {
		t.Fatalf("screen after escape from capability root = %s, want home", model.screen)
	}
}

func TestModelEscapeReturnsNonHomeScreensToHome(t *testing.T) {
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenBackground,
		NoBootAnimation: true,
	})
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.screen != ScreenHome {
		t.Fatalf("screen after escape = %s, want home", model.screen)
	}
}

func TestCapabilitiesExplorerKeepsSelectionVisibleInShortTerminal(t *testing.T) {
	data := CapabilitiesData{Explorer: defaultCapabilityExplorerState()}
	for idx := 0; idx < 30; idx++ {
		scope := fmt.Sprintf("node%02d", idx)
		providerAddress := scope + "@system"
		data.Providers = append(data.Providers, capabilities.ProviderListItem{
			Provider: capabilities.Provider{
				ProviderID:     fmt.Sprintf("provider_%02d", idx),
				ProviderKey:    "system",
				CompactAddress: providerAddress,
				DisplayName:    providerAddress,
				ProviderType:   "system",
				Status:         "active",
			},
			HealthStatus: "healthy",
		})
		data.Capabilities = append(data.Capabilities, capabilities.CapabilityListItem{
			CapabilityEndpoint: capabilities.CapabilityEndpoint{
				CapabilityEndpointID: fmt.Sprintf("endpoint_%02d", idx),
				CompactAddress:       providerAddress + ".status.read",
				EndpointName:         "status.read",
				Status:               "active",
			},
			ProviderAddress: providerAddress,
			DisplayName:     "Status Read",
			Description:     "Read status.",
		})
	}
	state := NewScreenState(ScreenCapabilities)
	state.Status = ScreenLoadLoaded
	state.Data.Capabilities = data
	state.SelectedIndex = 20

	mode := testMode()
	mode.Interactive = true
	mode.PortalAllowed = true
	mode.TTY.Width = 100
	mode.TTY.Height = 24
	model := NewModelWithOptions(ModelOptions{
		Mode:            mode,
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenCapabilities,
		NoBootAnimation: true,
	})
	model.setScreenState(ScreenCapabilities, state)
	output := model.View()
	if got := len(strings.Split(output, "\n")); got > 24 {
		t.Fatalf("view lines = %d, want <= 24:\n%s", got, output)
	}
	if !strings.Contains(output, "Capabilities And Providers") || !strings.Contains(output, "> node20") {
		t.Fatalf("short capability view did not preserve title and selected row:\n%s", output)
	}
}

func TestRenderScreenWithStateUsesLoadedScreenData(t *testing.T) {
	snapshot := fakeSnapshot()
	snapshot.Workers[0].WorkerKey = "snapshot_worker"
	state := ScreenStateFromSnapshot(ScreenBackground, snapshot)
	state.Data.Background.Workers = []workers.WorkerListItem{{
		WorkerKey:       "screen_state_worker",
		WorkerKind:      workers.KindSelfcheck,
		HealthStatus:    workers.HealthFailed,
		LifecycleStatus: workers.LifecycleFailed,
	}}

	output := RenderScreenWithState(RenderInput{
		Mode:         testMode(),
		HomeSnapshot: snapshot,
		Registry:     actions.DefaultRegistry(),
		Screen:       ScreenBackground,
		State:        state,
	})
	if !strings.Contains(output, "screen_state_worker") {
		t.Fatalf("screen data worker missing:\n%s", output)
	}
	if strings.Contains(output, "snapshot_worker") {
		t.Fatalf("renderer fell back to snapshot worker instead of screen state:\n%s", output)
	}
	if !strings.Contains(output, "1 needing attention") {
		t.Fatalf("degraded count should come from screen data:\n%s", output)
	}
}

func TestRenderScreenWithStateShowsLoadFailureAndPartialErrors(t *testing.T) {
	loading := RenderScreenWithState(RenderInput{
		Mode:   testMode(),
		Screen: ScreenJobs,
		State:  ScreenState{Screen: ScreenJobs, Status: ScreenLoadLoading},
	})
	if !strings.Contains(loading, "Loading Jobs") {
		t.Fatalf("loading state not rendered:\n%s", loading)
	}

	failed := RenderScreenWithState(RenderInput{
		Mode:   testMode(),
		Screen: ScreenJobs,
		State:  ScreenState{Screen: ScreenJobs, Status: ScreenLoadFailed, Error: "jobs unavailable"},
	})
	if !strings.Contains(failed, "Could not load Jobs") || !strings.Contains(failed, "jobs unavailable") {
		t.Fatalf("failed state not rendered:\n%s", failed)
	}

	partial := ScreenStateFromSnapshot(ScreenJobs, fakeSnapshot())
	partial.Status = ScreenLoadPartial
	partial.PartialErrors = []SnapshotError{{Source: "index_failures", Message: "index unavailable"}}
	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenJobs,
		State:    partial,
	})
	if !strings.Contains(output, "Partial Data") || !strings.Contains(output, "index unavailable") {
		t.Fatalf("partial errors not rendered:\n%s", output)
	}
}

func TestRenderScreenWithStateShowsSelectionAndRawDetails(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenJobs, fakeSnapshot())
	state.RawDetails = true
	state.SelectedIndex = screenActionOffset(state)

	output := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenJobs,
		State:    state,
		Width:    120,
		Height:   40,
	})
	if !strings.Contains(output, "> index_status_test") {
		t.Fatalf("selected row marker missing:\n%s", output)
	}
	if !strings.Contains(output, "Raw Details") || !strings.Contains(output, "width=120 height=40") {
		t.Fatalf("raw details missing:\n%s", output)
	}
}

func hasPartialSource(partials []SnapshotError, source string) bool {
	for _, partial := range partials {
		if partial.Source == source {
			return true
		}
	}
	return false
}

func findPortalAction(actions []PortalAction, id string) (PortalAction, bool) {
	for _, action := range actions {
		if action.ID == id {
			return action, true
		}
	}
	return PortalAction{}, false
}

func inventoryHasAction(items []PortalActionInventoryItem, id string) bool {
	for _, item := range items {
		if item.ActionID == id {
			return true
		}
	}
	return false
}

func findPortalActionByLabel(actions []PortalAction, label string) (PortalAction, bool) {
	for _, action := range actions {
		if action.Label == label {
			return action, true
		}
	}
	return PortalAction{}, false
}

func hasPortalActionLabel(actions []PortalAction, label string) bool {
	_, ok := findPortalActionByLabel(actions, label)
	return ok
}

func hasPortalActionInputField(action PortalAction, name string) bool {
	_, ok := portalActionInputField(action, name)
	return ok
}

func portalActionInputField(action PortalAction, name string) (PortalActionField, bool) {
	for _, field := range action.InputFields {
		if field.Name == name {
			return field, true
		}
	}
	return PortalActionField{}, false
}

func portalActionLabels(actions []PortalAction) []string {
	labels := make([]string, 0, len(actions))
	for _, action := range actions {
		labels = append(labels, action.Label)
	}
	return labels
}

func assertNoPortalActionExecutors(t *testing.T, actions []PortalAction, forbidden ...string) {
	t.Helper()
	blocked := map[string]bool{}
	for _, executor := range forbidden {
		blocked[executor] = true
	}
	for _, action := range actions {
		if blocked[action.Executor.Kind] {
			t.Fatalf("unexpected action executor %s in action %#v", action.Executor.Kind, action)
		}
	}
}

func hasCommandSuggestion(suggestions []CommandSuggestion, value string) bool {
	for _, suggestion := range suggestions {
		if suggestion.Label == value || suggestion.InsertText == value {
			return true
		}
	}
	return false
}

type fakeCommandRunner struct{}

func (fakeCommandRunner) Preview(_ context.Context, request CommandRequest) (CommandPreview, error) {
	return BuildCommandPreview(request.Input), nil
}

func (fakeCommandRunner) Complete(_ context.Context, request CommandCompletionRequest) ([]CommandSuggestion, error) {
	return []CommandSuggestion{
		{Label: "capabilities", Kind: "command", Description: "List capabilities", InsertText: "capabilities", ReplacementStart: 2, ReplacementEnd: len(request.Input), CanonicalPreview: "loom capabilities list"},
		{Label: "main.indexer_text", Kind: "worker", Description: "Text indexer", InsertText: "main.indexer_text", ReplacementStart: 2, ReplacementEnd: len(request.Input), CanonicalPreview: "loom worker run main.indexer_text --once"},
	}, nil
}

func (fakeCommandRunner) Execute(_ context.Context, request CommandRequest) (CommandResult, error) {
	return CommandResult{
		Input:            request.Input,
		CanonicalTokens:  append([]string{}, request.CanonicalTokens...),
		CanonicalCommand: commandString(request.CanonicalTokens),
		Classification:   request.Classification,
		Status:           ActionLifecycleSucceeded,
		Summary:          "Command completed.",
		Stdout:           "fake command output\n",
		StartedAt:        time.Now().UTC(),
		CompletedAt:      time.Now().UTC(),
	}, nil
}

func TestRunBootChecksReportsPartialFailures(t *testing.T) {
	client := newFakePortalClient()
	client.optionalErr = errors.New("optional unavailable")
	results, _, err := RunBootChecks(context.Background(), client, "corr_test")
	if err != nil {
		t.Fatalf("RunBootChecks returned error: %v", err)
	}
	foundPartial := false
	for _, result := range results {
		if result.Status == "partial" {
			foundPartial = true
			break
		}
	}
	if !foundPartial {
		t.Fatalf("expected partial boot result, got %#v", results)
	}
}

func TestBoxPortalScreenRendersMissingAndInitializedState(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}

	missing := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBox, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	if missing.Err != nil {
		t.Fatalf("LoadScreen missing Box returned error: %v", missing.Err)
	}
	if missing.State.Data.Box.Status.State != "missing" {
		t.Fatalf("missing Box state = %q", missing.State.Data.Box.Status.State)
	}
	missingOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: missing.State, Width: 120, Height: 40})
	if !strings.Contains(missingOutput, "LOOM Box") || !strings.Contains(missingOutput, "Initialize Box") {
		t.Fatalf("missing Box render lacks surface/action:\n%s", missingOutput)
	}

	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	loaded := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBox, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	if loaded.Err != nil {
		t.Fatalf("LoadScreen initialized Box returned error: %v", loaded.Err)
	}
	if loaded.State.Data.Box.Status.State != "ok" || !loaded.State.Data.Box.WatchStatusAvailable {
		t.Fatalf("initialized Box data not live: state=%q watch=%t", loaded.State.Data.Box.Status.State, loaded.State.Data.Box.WatchStatusAvailable)
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: loaded.State, Width: 120, Height: 40})
	for _, want := range []string{"Box Status", "LOOM Lane", "Folders And Policies", "Watch Policy", "Notes", "Documents", "Lane", "Create Project In Box"} {
		if !strings.Contains(output, want) {
			t.Fatalf("initialized Box render missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "\nLOOM Lane\n") > strings.Index(output, "\nWatch Policy\n") ||
		strings.Index(output, "\nFolders And Policies\n") > strings.Index(output, "\nWatch Policy\n") {
		t.Fatalf("initialized Box should put movement and folders before watch internals:\n%s", output)
	}
	for _, hidden := range []string{"transfer runtime arrives in v0.4.2", "Metadata", ".loom/state", "Dropzone State"} {
		if strings.Contains(output, hidden) {
			t.Fatalf("initialized Box default render should hide %q:\n%s", hidden, output)
		}
	}
	detailsOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: loaded.State.ToggleRawDetails(), Width: 120, Height: 80})
	for _, want := range []string{"Metadata", ".loom-box-state"} {
		if !strings.Contains(detailsOutput, want) {
			t.Fatalf("initialized Box raw details render missing %q:\n%s", want, detailsOutput)
		}
	}
	if strings.Contains(detailsOutput, "Dropzone") {
		t.Fatalf("initialized workspace Box raw details exposed retired Dropzone:\n%s", detailsOutput)
	}
}

func TestCollectSnapshotIncludesBoxState(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}

	snapshot, err := CollectSnapshot(context.Background(), newFakePortalClient(), "corr_test", SnapshotOptions{BoxResolved: &resolved})
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	if snapshot.BoxStatus.State != "ok" || !snapshot.BoxWatchStatusAvailable {
		t.Fatalf("snapshot did not include live Box state: status=%q watch=%t", snapshot.BoxStatus.State, snapshot.BoxWatchStatusAvailable)
	}
	if snapshot.BoxWatchPlan.RootPath != root {
		t.Fatalf("snapshot Box root = %q, want %q", snapshot.BoxWatchPlan.RootPath, root)
	}
}

func TestBoxPortalMainProfileShowsImportsWithoutRetiredIntake(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileMain, ProfileSource: "test", OwnerNode: "main", NodeRole: "main"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}

	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBox, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned error: %v", result.Err)
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: result.State, Width: 120, Height: 40})
	if !strings.Contains(output, "intake=Storage Imports") {
		t.Fatalf("main Box render should identify Storage Imports intake:\n%s", output)
	}
	details := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: result.State.ToggleRawDetails(), Width: 120, Height: 80})
	for _, retired := range []string{"Dropzone", "dropzone", "LOOM Lane", "loom-lane", "Send Lane", "Inspect Transfer Plan"} {
		if strings.Contains(output, retired) || strings.Contains(details, retired) {
			t.Fatalf("main Box surface exposed retired intake %q:\n%s", retired, details)
		}
	}
}

func TestBoxPortalSearchAndActions(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBox, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	results := SearchPortal(actions.DefaultRegistry(), result.State, "#box documents", 8)
	if len(results) == 0 {
		t.Fatalf("expected #box scoped results")
	}
	actions := ScreenActions(result.State)
	initAction, ok := findPortalAction(actions, "box.init")
	if !ok {
		t.Fatalf("box.init action missing")
	}
	if got := strings.Join(initAction.RawCommand, " "); !strings.Contains(got, "loom box init") || !strings.Contains(got, root) {
		t.Fatalf("unexpected box init raw command: %s", got)
	}
	projectAction, ok := findPortalAction(actions, "box.project.scaffold")
	if !ok {
		t.Fatalf("box.project.scaffold action missing")
	}
	if projectAction.Executor.Kind != PortalExecutorBoxProjectScaffold || projectAction.TargetRef != filepath.Join(root, "Projects") {
		t.Fatalf("unexpected project scaffold action: %#v", projectAction)
	}
	watchAction, ok := findPortalAction(actions, "box.watch_policy.apply")
	if !ok {
		t.Fatalf("box.watch_policy.apply action missing")
	}
	if watchAction.Executor.Kind != PortalExecutorBoxWatchApply || watchAction.Interaction() != ActionInteractionFormRun {
		t.Fatalf("unexpected watch apply action: %#v", watchAction)
	}
	watchAction.InputValues["dry_run"] = "true"
	watchResult, err := ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", watchAction, true)
	if err != nil {
		t.Fatalf("execute watch apply returned error: %v", err)
	}
	if watchResult.Status != ActionLifecycleSucceeded || resultFieldValue(watchResult, "Dry Run") != "true" {
		t.Fatalf("watch apply did not dry-run successfully: %#v", watchResult)
	}
}

func TestBoxPortalRendersLanePendingItems(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	laneFile := filepath.Join(root, box.DefaultLaneDirName, "lane-pending.txt")
	if err := os.WriteFile(laneFile, []byte("pending lane payload"), 0o644); err != nil {
		t.Fatalf("write lane file: %v", err)
	}

	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBox, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned error: %v", result.Err)
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: result.State, Width: 120, Height: 60})
	for _, want := range []string{"LOOM Lane", "pending=1", "active=1", "lane-pending.txt"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Lane render missing %q:\n%s", want, output)
		}
	}
	results := SearchPortal(actions.DefaultRegistry(), result.State, "#box lane-pending", 8)
	if len(results) == 0 || results[0].Action.TargetRef != "lane-pending.txt" {
		t.Fatalf("expected #box search to find Lane pending item, got %#v", results)
	}
	ackAction, ok := findPortalActionByLabel(ScreenActions(result.State), "Acknowledge Lane Item")
	if !ok {
		t.Fatalf("expected Lane acknowledgement action in %#v", portalActionLabels(ScreenActions(result.State)))
	}
	ackResult, err := ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", ackAction, true)
	if err != nil {
		t.Fatalf("execute Lane acknowledgement returned error: %v", err)
	}
	if ackResult.Status != ActionLifecycleSucceeded || resultFieldValue(ackResult, "Attention") != lane.AttentionStatusAcknowledged {
		t.Fatalf("unexpected Lane acknowledgement result: %#v", ackResult)
	}
	if _, err := os.Stat(laneFile); err != nil {
		t.Fatalf("Lane acknowledgement should not remove pending file: %v", err)
	}

	after := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBox, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	if after.Err != nil {
		t.Fatalf("LoadScreen after acknowledgement returned error: %v", after.Err)
	}
	afterOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: after.State, Width: 120, Height: 60})
	for _, want := range []string{"pending=1", "active=0", "ack=1", lane.AttentionStatusAcknowledged} {
		if !strings.Contains(afterOutput, want) {
			t.Fatalf("Lane acknowledgement render missing %q:\n%s", want, afterOutput)
		}
	}
}

func TestBoxPortalRecoveryDiagnosticsAreReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	batchID := "lane_portal_protected"
	safetyPath := filepath.Join(root, lane.DefaultStateRelPath, "sent", batchID)
	if err := os.MkdirAll(safetyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(safetyPath, "payload.txt"), []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	completed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	record := lane.BatchRecord{
		SchemaVersion: lane.BatchSchemaVersion, BatchID: batchID, Status: lane.BatchStatusFailed,
		SelectedTransport: lane.TransportModeFileTree, LocalSafetyPath: safetyPath, LocalSafetyCleanupState: lane.SafetyArtifactRetainedForRetry,
		CompletedAt: &completed,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	batchDir := filepath.Join(root, lane.DefaultStateRelPath, "batches")
	if err := os.MkdirAll(batchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchDir, batchID+".json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBox, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	result.State.RawDetails = true
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: result.State, Width: 160, Height: 80})
	for _, want := range []string{"recovery=9 B", "recovery_protected=9 B", "recovery retained=9 B", "protected=9 B"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Portal recovery render missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(safetyPath, "payload.txt")); err != nil {
		t.Fatalf("Portal rendering mutated protected recovery evidence: %v", err)
	}
}

func TestBoxPortalExecutesFailedLaneTransferAttentionActions(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	statePath := filepath.Join(root, ".loom", "state", "lane", "batches")
	if err := os.MkdirAll(statePath, 0o755); err != nil {
		t.Fatalf("mkdir lane batches: %v", err)
	}
	started := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	record := `{
  "schema_version": "loom.lane.batch.v0.6.3",
  "batch_id": "lane_failed_portal",
  "status": "failed",
  "file_count": 1,
  "total_bytes": 9,
  "started_at": "` + started.Format(time.RFC3339) + `",
  "error_message": "rsync failed"
}`
	if err := os.WriteFile(filepath.Join(statePath, "lane_failed_portal.json"), []byte(record), 0o644); err != nil {
		t.Fatalf("write lane batch: %v", err)
	}

	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBox, Snapshot{}, SnapshotOptions{BoxResolved: &resolved})
	if result.Err != nil {
		t.Fatalf("LoadScreen returned error: %v", result.Err)
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: result.State.ToggleRawDetails(), Width: 120, Height: 80})
	for _, want := range []string{"lane_failed_portal", "failed", "attention=active", "rsync failed"} {
		if !strings.Contains(output, want) {
			t.Fatalf("failed Lane transfer render missing %q:\n%s", want, output)
		}
	}
	ackAction, ok := findPortalActionByLabel(ScreenActions(result.State), "Acknowledge Lane Transfer")
	if !ok {
		t.Fatalf("expected failed Lane transfer acknowledgement action in %#v", portalActionLabels(ScreenActions(result.State)))
	}
	ackResult, err := ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", ackAction, true)
	if err != nil {
		t.Fatalf("execute Lane transfer acknowledgement returned error: %v", err)
	}
	if ackResult.Status != ActionLifecycleSucceeded || resultFieldValue(ackResult, "Attention") != lane.AttentionStatusAcknowledged {
		t.Fatalf("unexpected Lane transfer acknowledgement result: %#v", ackResult)
	}
	payload, err := os.ReadFile(filepath.Join(statePath, "lane_failed_portal.json"))
	if err != nil {
		t.Fatalf("read lane batch after acknowledgement: %v", err)
	}
	if !strings.Contains(string(payload), `"attention_status": "acknowledged"`) || !strings.Contains(string(payload), `"status": "failed"`) {
		t.Fatalf("batch record should preserve failed status and add attention metadata:\n%s", string(payload))
	}
}

func TestBoxPortalDropzoneMutationsFailClosed(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "drop_historical_transfer.json")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "dropzone", "testdata", "historical", "transfer-v0.4.2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{
		PortalExecutorBoxDropzoneRetry,
		PortalExecutorBoxDropzonePause,
		PortalExecutorBoxDropzoneResume,
		PortalExecutorBoxDropzoneClean,
	} {
		action := PortalAction{
			ID:            "box.dropzone.retired",
			Label:         "Retired Dropzone mutation",
			State:         ActionAvailable,
			TargetRef:     "drop_historical_transfer",
			Executor:      PortalActionExecutor{Kind: kind, Target: "drop_historical_transfer"},
			RefreshScreen: ScreenBox,
		}
		result, err := ExecutePortalAction(context.Background(), newFakePortalClient(), "corr_test", action, true)
		if err != nil {
			t.Fatalf("execute retired Dropzone action %s: %v", kind, err)
		}
		if result.Status != ActionLifecycleFailed || result.ErrorCode != "portal.dropzone_retired" {
			t.Fatalf("retired Dropzone action %s did not fail closed: %#v", kind, result)
		}
	}
	after, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(fixture) {
		t.Fatal("Portal mutation attempt changed historical Dropzone evidence")
	}
}
func TestRunPreviewBoxActionUsesResolvedBoxOptions(t *testing.T) {
	root := t.TempDir() + "/LOOM Box"
	resolved := box.Resolved{RootPath: root, PathSource: "test", Profile: box.ProfileWorkspace, ProfileSource: "test", OwnerNode: "macbook", NodeRole: "workspace"}
	var out bytes.Buffer

	err := Run(context.Background(), Options{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		BoxResolved:     &resolved,
		Out:             &out,
		StartScreen:     ScreenBox,
		PreviewActionID: "box.init",
		ExitAfterRender: true,
		Registry:        actions.DefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "loom box init") || !strings.Contains(output, root) {
		t.Fatalf("preview did not use resolved Box path %q:\n%s", root, output)
	}
}

func TestStoragePortalScreenRendersTreeStatusAndHidesInternalPaths(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenStorage, Snapshot{})
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("status = %s partials=%v", result.State.Status, result.State.PartialErrors)
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenStorage, State: result.State, Width: 120, Height: 40})
	for _, want := range []string{"LOOM Main Storage", "Attention", "Storage reports 2 advisory or attention item", "Storage Safety", "User Storage", "main/Documents", "main/Archive", "backups/macbook", "macbook/Dropzone", "Backup And Cloud Protection", "transfers: 2", "SMB by default"} {
		if !strings.Contains(output, want) {
			t.Fatalf("storage output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "file_transfer_failed") {
		t.Fatalf("storage default output should route failure IDs to Doctor/raw details:\n%s", output)
	}
	if strings.Index(output, "Attention") > strings.Index(output, "User Storage") ||
		strings.Index(output, "Storage Safety") > strings.Index(output, "User Storage") ||
		strings.Index(output, "User Storage") > strings.Index(output, "Backup And Cloud Protection") {
		t.Fatalf("storage output should put attention/safety first, then user storage, then protection:\n%s", output)
	}
	for _, notWant := range []string{"/srv/loom", "/tmp/loom-export", "file:///", "AppleDouble sidecar", "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md", "_System/Transfers"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("storage output exposed internal path %q:\n%s", notWant, output)
		}
	}
	rawOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenStorage, State: result.State.ToggleRawDetails(), Width: 120, Height: 60})
	if !strings.Contains(rawOutput, "file_transfer_failed") {
		t.Fatalf("storage raw output should keep local transfer diagnostics:\n%s", rawOutput)
	}
}

func TestPortalCloudStatusLoadsFromMainBackedClient(t *testing.T) {
	client := newFakePortalClient()
	report := cloudstorage.StatusReport{
		Status:    "reachable",
		Mode:      string(cloudstorage.StatusModeLive),
		CheckedAt: time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC),
		Config: cloudstorage.ConfigSummary{
			Path:            cloudstorage.DefaultConfigPath,
			Exists:          true,
			Enabled:         true,
			Provider:        "hetzner_storage_box",
			RemoteName:      "loom-cloud-storage",
			RemoteRoot:      "loom",
			SnapshotBackend: "borg",
		},
	}
	client.cloudStatusLiveReport = &report

	storage := LoadScreen(context.Background(), client, "corr_test", ScreenStorage, Snapshot{})
	if storage.State.Status != ScreenLoadLoaded {
		t.Fatalf("storage status = %s partials=%v", storage.State.Status, storage.State.PartialErrors)
	}
	if !storage.State.Data.Storage.CloudStatusAvailable {
		t.Fatalf("storage cloud status should be available")
	}
	if got := storage.State.Data.Storage.CloudStatus.Status; got != "reachable" {
		t.Fatalf("storage cloud status = %q, want reachable", got)
	}
	if client.cloudStatusLiveInput.ConfigPath != cloudstorage.DefaultConfigPath {
		t.Fatalf("cloud status config path = %q, want %q", client.cloudStatusLiveInput.ConfigPath, cloudstorage.DefaultConfigPath)
	}

	background := LoadScreen(context.Background(), client, "corr_test", ScreenBackground, Snapshot{})
	if background.State.Status != ScreenLoadLoaded {
		t.Fatalf("background status = %s partials=%v", background.State.Status, background.State.PartialErrors)
	}
	if !background.State.Data.Background.CloudStatusAvailable {
		t.Fatalf("background cloud status should be available")
	}
	if got := background.State.Data.Background.CloudStatus.Config.SnapshotBackend; got != "borg" {
		t.Fatalf("background cloud backend = %q, want borg", got)
	}
	if client.cloudStatusLiveCalls < 2 {
		t.Fatalf("cloud status live calls = %d, want at least 2", client.cloudStatusLiveCalls)
	}
}

func TestStoragePortalCloudStatusRendersCachedCooldownAndExplicitActions(t *testing.T) {
	lastFailure := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	nextProbe := lastFailure.Add(30 * time.Minute)
	state := NewScreenState(ScreenStorage)
	state.Status = ScreenLoadLoaded
	state.Data.Storage.CloudStatusAvailable = true
	state.Data.Storage.CloudStatus = cloudstorage.StatusReport{
		Status:    cloudstorage.RemoteStateCoolingDown,
		Mode:      string(cloudstorage.StatusModeCached),
		CheckedAt: lastFailure,
		Config: cloudstorage.ConfigSummary{
			Path:                         "/var/lib/loom/cloud/config.json",
			Exists:                       true,
			Enabled:                      true,
			Provider:                     "hetzner_storage_box",
			Driver:                       "ssh",
			RemoteName:                   "loom-cloud-storage",
			RemoteRoot:                   "loom",
			SnapshotBackend:              "borg",
			BorgCompression:              "zstd",
			BorgCheckIntervalHours:       24,
			BorgInventoryCacheTTLSeconds: 3600,
		},
		RemoteState: &cloudstorage.RemoteState{
			State:               cloudstorage.RemoteStateCoolingDown,
			LastFailureAt:       &lastFailure,
			NextLiveCheckAfter:  &nextProbe,
			LastErrorClass:      cloudstorage.RemoteErrorConnectionRefused,
			LiveProbeSkipReason: cloudstorage.RemoteStateCoolingDown,
		},
	}

	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenStorage, State: state, Width: 120, Height: 40})
	for _, want := range []string{
		"Storage reports 1 advisory or attention item",
		"cloud: cooling_down",
		"mode: cached",
		"remote state: cooling_down",
		"next live check: 2026-06-20T10:30:00Z",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("storage cloud output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "cloud backup live checks are cooling down") {
		t.Fatalf("storage cloud default output should route cooldown detail to Doctor/raw details:\n%s", output)
	}
	rawOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenStorage, State: state.ToggleRawDetails(), Width: 120, Height: 60})
	if !strings.Contains(rawOutput, "cloud backup live checks are cooling down") {
		t.Fatalf("storage cloud raw output should keep cooldown diagnostic:\n%s", rawOutput)
	}

	actions := ScreenAvailableActions(state)
	cooldown, ok := findPortalAction(actions, "cloud.cooldown.inspect")
	if !ok {
		t.Fatalf("cloud cooldown inspect action missing: %#v", actions)
	}
	if cooldown.Executor.Kind != PortalExecutorCloudCooldownInspect {
		t.Fatalf("cooldown executor = %s, want %s", cooldown.Executor.Kind, PortalExecutorCloudCooldownInspect)
	}
	if cooldown.Executor.Payload["next_live_check"] != "2026-06-20T10:30:00Z" {
		t.Fatalf("cooldown next live payload = %q", cooldown.Executor.Payload["next_live_check"])
	}
	live, ok := findPortalAction(actions, "cloud.status.live")
	if !ok {
		t.Fatalf("cloud live status action missing: %#v", actions)
	}
	if live.Executor.Kind != PortalExecutorCloudStatusLive {
		t.Fatalf("live executor = %s, want %s", live.Executor.Kind, PortalExecutorCloudStatusLive)
	}
	if got, want := strings.Join(live.RawCommand, " "), "loom cloud status --live"; got != want {
		t.Fatalf("live raw command = %q, want %q", got, want)
	}
	client := newFakePortalClient()
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", live, true)
	if err != nil {
		t.Fatalf("execute cloud live action returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded {
		t.Fatalf("cloud live action status = %s, result = %#v", result.Status, result)
	}
	if client.cloudStatusLiveInput.ConfigPath != cloudstorage.DefaultConfigPath {
		t.Fatalf("cloud live config path = %q, want %q", client.cloudStatusLiveInput.ConfigPath, cloudstorage.DefaultConfigPath)
	}
}

func TestWorkerUnknownWithoutAttentionDoesNotInflateAttention(t *testing.T) {
	attentionRunID := "worker_run_failed"
	snapshot := Snapshot{Workers: []workers.WorkerListItem{
		{
			WorkerInstanceID: "worker_unknown",
			WorkerKey:        "main.worker_selfcheck",
			WorkerKind:       workers.KindSelfcheck,
			LifecycleStatus:  workers.LifecycleActive,
			HealthStatus:     workers.HealthUnknown,
			Enabled:          true,
		},
		{
			WorkerInstanceID:  "worker_degraded",
			WorkerKey:         "main.object_store_integrity",
			WorkerKind:        workers.KindObjectStore,
			LifecycleStatus:   workers.LifecycleActive,
			HealthStatus:      workers.HealthDegraded,
			AttentionRequired: true,
			CurrentRunID:      &attentionRunID,
			Enabled:           true,
		},
	}}

	if got := snapshot.DegradedWorkerCount(); got != 1 {
		t.Fatalf("degraded worker count = %d, want 1", got)
	}
	if got := degradedWorkerCount(snapshot.Workers); got != 1 {
		t.Fatalf("render degraded worker count = %d, want 1", got)
	}
	attention := buildHomeAttention(snapshot)
	if len(attention) != 1 {
		t.Fatalf("home attention count = %d, want 1: %#v", len(attention), attention)
	}
	if attention[0].TargetRef != "main.object_store_integrity" {
		t.Fatalf("attention target = %q, want degraded worker", attention[0].TargetRef)
	}
}

func TestPortalCloudSnapshotInventoryIsExplicitOnly(t *testing.T) {
	background := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenBackground, Snapshot{})
	if background.State.Data.Background.CloudSnapshotsAvailable {
		t.Fatalf("background screen should not populate live cloud snapshot inventory: %#v", background.State.Data.Background.CloudSnapshots)
	}
	if hasPartialSource(background.State.PartialErrors, "cloud_snapshots") {
		t.Fatalf("background screen should not attempt cloud snapshot listing: %#v", background.State.PartialErrors)
	}

	storage := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenStorage, Snapshot{})
	if storage.State.Data.Storage.CloudSnapshotsAvailable {
		t.Fatalf("storage screen should not populate live cloud snapshot inventory: %#v", storage.State.Data.Storage.CloudSnapshots)
	}
	if hasPartialSource(storage.State.PartialErrors, "storage_cloud_snapshots") {
		t.Fatalf("storage screen should not attempt cloud snapshot listing: %#v", storage.State.PartialErrors)
	}
}

func TestStoragePortalScreenEmptyState(t *testing.T) {
	state := NewScreenState(ScreenStorage)
	state.Status = ScreenLoadLoaded
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenStorage, State: state, Width: 120, Height: 30})
	if !strings.Contains(output, "No user-facing storage paths returned by the backend.") {
		t.Fatalf("empty storage screen missing guidance:\n%s", output)
	}
}

func TestStoragePortalSelectionAndActions(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenStorage, Snapshot{})
	records := ScreenRecordItems(result.State)
	if len(records) < 3 {
		t.Fatalf("records = %d, want at least 3", len(records))
	}
	selected := result.State
	archiveRow := findSelectableRecord(records, "storage_archive")
	if archiveRow.RecordKind == "" {
		t.Fatalf("archive root row missing: %#v", records)
	}
	selected.SelectedIndex = archiveRow.RowIndex
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenStorage, State: selected, Width: 120, Height: 40})
	if !strings.Contains(output, "> main/Archive") {
		t.Fatalf("selection marker did not move to collapsed archive row:\n%s", output)
	}
	if item := findSelectableRecord(records, "storage_entry"); item.RecordKind != "" {
		t.Fatalf("default storage screen should expose collapsed paths, not file records: %#v", records)
	}
	report := findSelectableRecord(storageDetailedSelectableItems(result.State), "storage_entry")
	if report.PrimaryAction == nil || report.PrimaryAction.Executor.Kind != PortalExecutorStorageInspect {
		t.Fatalf("report primary action = %#v", report.PrimaryAction)
	}
	relatedKinds := map[string]bool{}
	for _, action := range report.RelatedActions {
		relatedKinds[action.Executor.Kind] = true
	}
	for _, want := range []string{PortalExecutorStorageSafeToDelete, PortalExecutorStorageFetch, PortalExecutorStorageArchive} {
		if !relatedKinds[want] {
			t.Fatalf("missing related storage action %s in %#v", want, report.RelatedActions)
		}
	}
	screenActions := ScreenAvailableActions(result.State)
	foundMountHelper := false
	for _, action := range screenActions {
		if strings.Contains(action.ID, "storage.export") || strings.Contains(action.Executor.Kind, "storage.export") {
			t.Fatalf("retired export action remains available: %#v", action)
		}
		if action.ID == "storage.mount.helper.inspect" {
			foundMountHelper = true
			if !strings.Contains(action.Description, "SMB is default") || !strings.Contains(action.Description, "rclone is optional") {
				t.Fatalf("mount helper description is not SMB-first: %q", action.Description)
			}
			if action.Executor.Payload["default_protocol"] != "smb" {
				t.Fatalf("mount helper protocol payload = %q, want smb", action.Executor.Payload["default_protocol"])
			}
			if got, want := strings.Join(action.RawCommand, " "), "scripts/loom-macbook main-storage-status"; got != want {
				t.Fatalf("mount helper raw command = %q, want %q", got, want)
			}
		}
	}
	if !foundMountHelper {
		t.Fatalf("screen actions missing storage mount helper: %#v", screenActions)
	}
}

func TestStoragePortalScopedSearch(t *testing.T) {
	result := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenStorage, Snapshot{})
	registry := actions.DefaultRegistry()
	storageResults := SearchPortal(registry, result.State, "#storage report", 10)
	if !searchResultsContainTitle(storageResults, "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md") {
		t.Fatalf("#storage search missing report result: %#v", storageResults)
	}
	for _, searchResult := range storageResults {
		if searchResult.Title == "Archive Storage Entry" {
			t.Fatalf("#storage search should return paths, not repeated archive actions: %#v", storageResults)
		}
	}
	rootResults := SearchPortal(registry, result.State, "#storage ", 10)
	if !searchResultsContainTitle(rootResults, "main/Documents") || !searchResultsContainTitle(rootResults, "backups/macbook") {
		t.Fatalf("#storage root search should expose top-level storage paths: %#v", rootResults)
	}
	archiveResults := SearchPortal(registry, result.State, "#archives old", 10)
	if !searchResultsContainCategory(archiveResults, "archive") || searchResultsContainCategory(archiveResults, "transfer") {
		t.Fatalf("#archives search not narrowed to archive records: %#v", archiveResults)
	}
	transferResults := SearchPortal(registry, result.State, "#transfers video", 10)
	if !searchResultsContainCategory(transferResults, "transfer") || searchResultsContainCategory(transferResults, "archive") {
		t.Fatalf("#transfers search not narrowed to transfer records: %#v", transferResults)
	}
	globalResults := SearchPortal(registry, result.State, "report.md", 10)
	if searchResultsContainTitle(globalResults, "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md") {
		t.Fatalf("global navigation search exposed storage file records: %#v", globalResults)
	}
}

func TestStoragePortalActionsExecuteThroughClient(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenStorage, Snapshot{})
	report := findSelectableRecord(storageDetailedSelectableItems(result.State), "storage_entry")
	if report.PrimaryAction == nil {
		t.Fatal("storage report record has no primary action")
	}
	inspect, err := ExecutePortalAction(context.Background(), client, "corr_test", *report.PrimaryAction, false)
	if err != nil {
		t.Fatalf("inspect returned error: %v", err)
	}
	if inspect.Status != ActionLifecycleSucceeded || client.storageInspectRef == "" {
		t.Fatalf("inspect did not call storage backend: result=%#v ref=%q", inspect, client.storageInspectRef)
	}
	safeAction := findRelatedAction(report, PortalExecutorStorageSafeToDelete)
	safe, err := ExecutePortalAction(context.Background(), client, "corr_test", safeAction, false)
	if err != nil {
		t.Fatalf("safe-to-delete returned error: %v", err)
	}
	if safe.Status != ActionLifecycleSucceeded || client.storageSafeInput.Ref == "" {
		t.Fatalf("safe-to-delete did not call backend: result=%#v input=%#v", safe, client.storageSafeInput)
	}
	fetchAction := findRelatedAction(report, PortalExecutorStorageFetch)
	fetchAction.InputValues["destination_path"] = filepath.Join(t.TempDir(), "report.md")
	fetch, err := ExecutePortalAction(context.Background(), client, "corr_test", fetchAction, true)
	if err != nil {
		t.Fatalf("fetch returned error: %v", err)
	}
	if fetch.Status != ActionLifecycleSucceeded || client.storageFetchInput.DestinationPath == "" {
		t.Fatalf("fetch did not call backend: result=%#v input=%#v", fetch, client.storageFetchInput)
	}
	archiveAction := findRelatedAction(report, PortalExecutorStorageArchive)
	archiveAction.InputValues["target_path"] = "main/archive/report.md"
	archive, err := ExecutePortalAction(context.Background(), client, "corr_test", archiveAction, true)
	if err != nil {
		t.Fatalf("archive returned error: %v", err)
	}
	if archive.Status != ActionLifecycleSucceeded || client.storageArchiveInput.TargetPath != "main/archive/report.md" || !client.storageArchiveInput.DryRun {
		t.Fatalf("archive did not call backend with dry-run target: result=%#v input=%#v", archive, client.storageArchiveInput)
	}
}

func findSelectableRecord(stateOrRecords interface{}, kind string) SelectableItem {
	var records []SelectableItem
	switch value := stateOrRecords.(type) {
	case ScreenState:
		records = ScreenRecordItems(value)
	case []SelectableItem:
		records = value
	}
	for _, item := range records {
		if item.RecordKind == kind {
			return item
		}
	}
	return SelectableItem{}
}

func findGroupedSelectableAction(state ScreenState, actionID string) PortalAction {
	for _, item := range ScreenSelectableItems(state) {
		if !selectableIsOperationalActionGroupAction(item) || item.ActionID != actionID || item.PrimaryAction == nil {
			continue
		}
		return *item.PrimaryAction
	}
	return PortalAction{}
}

func findRelatedAction(item SelectableItem, executor string) PortalAction {
	for _, action := range item.RelatedActions {
		if action.Executor.Kind == executor {
			return action
		}
	}
	return PortalAction{}
}

func searchResultsContainTitle(results []PortalSearchResult, title string) bool {
	for _, result := range results {
		if result.Title == title {
			return true
		}
	}
	return false
}

func searchResultsContainCategory(results []PortalSearchResult, category string) bool {
	for _, result := range results {
		if result.Category == category {
			return true
		}
	}
	return false
}

func testMode() ui.Mode {
	return ui.Mode{
		Output:        ui.OutputTable,
		Interactive:   false,
		PortalAllowed: false,
		Color:         false,
		Animation:     false,
		ThemeName:     ui.ThemeCoffee,
	}
}

func colorMode() ui.Mode {
	return ui.Mode{
		Output:        ui.OutputTable,
		Interactive:   true,
		PortalAllowed: true,
		Color:         true,
		Animation:     true,
		ThemeName:     ui.ThemeCoffee,
		TTY: ui.TerminalInfo{
			StdinTTY:     true,
			StdoutTTY:    true,
			StderrTTY:    true,
			Width:        96,
			Height:       30,
			Term:         "xterm-256color",
			ColorProfile: ui.ColorTrueColor,
		},
	}
}

func fakeSnapshot() Snapshot {
	snapshot, err := CollectSnapshot(context.Background(), newFakePortalClient(), "corr_test")
	if err != nil {
		panic(err)
	}
	return snapshot
}

type fakePortalClient struct {
	snapshotCalls              []string
	healthErr                  error
	statusErr                  error
	optionalErr                error
	workerRunsErr              error
	capabilitiesErr            error
	runErr                     error
	indexExplainErr            error
	runWorkerRef               string
	runInput                   workers.RunOnceInput
	inspectWorkerRef           string
	retryIndexID               string
	retryFailedInput           search.IndexRetryFailedInput
	rebuildObjectRef           string
	backupRunInput             maintenance.BackupRunInput
	backupVerifyRef            string
	objectScanInput            maintenance.ObjectStoreScanInput
	objectInspectRef           string
	objectVersionsRef          string
	searchInput                search.SearchInput
	scheduleRef                string
	scheduleFireRef            string
	fireScheduleRef            string
	fireScheduleInput          automation.FireScheduleInput
	pauseScheduleRef           string
	resumeScheduleRef          string
	directEndpointRef          string
	directEventRef             string
	directRawRef               string
	invocationRef              string
	invocationFilter           automation.InvocationFilter
	invocationFailureFilter    automation.InvocationFilter
	providerRef                string
	serviceRef                 string
	providerHealthRef          string
	providerAdRef              string
	capabilityRef              string
	usageDocsRef               string
	callCapability             routing.CapabilityCallInput
	capabilityCallRef          string
	jobInspectRef              string
	jobLogsRef                 string
	jobOutputsRef              string
	jobCancelRef               string
	jobRetryRef                string
	jobAcknowledgeRef          string
	jobArchiveRef              string
	notesOverviewInput         knowledge.NotesOverviewInput
	notesSearchInput           knowledge.NotesSearchInput
	notesEmbeddingStatus       knowledge.EmbeddingStatus
	notesEmbeddingsEnable      bool
	notesEmbeddingsDisable     bool
	syncStatusFilter           loomsync.ListFilter
	deletionRequestRef         string
	deletionRequestAction      string
	nodeInspectRef             string
	nodeHealthRef              string
	projectRef                 string
	scaffoldProjectInput       projectcontracts.ScaffoldOptions
	addProjectFacetsInput      projectcontracts.AddProjectFacetsOptions
	layoutMigrationInput       projectcontracts.LayoutMigrationOptions
	backendAnalysisInput       projectcontracts.BackendAnalysisInput
	backendAnalysisOverride    *projectdoctor.BackendAnalysisResult
	registerBackendInput       projects.RegisterProjectContractFromBackendInput
	activateProject            projects.ActivateProjectInput
	deactivateProject          projects.DeactivateProjectInput
	projectArchiveRef          string
	projectArchiveInput        storagearchive.ProjectArchiveInput
	projectArchiveInspectRef   string
	projectArchiveRestoreRef   string
	projectArchiveRestoreInput storagearchive.ProjectArchiveRestoreInput
	storageInspectRef          string
	storageResolvePath         string
	storageSafeInput           storageretention.SafeToDeleteInput
	storageFetchInput          storageretention.FetchInput
	storageRestoreInput        storageretention.RestoreInput
	storageArchiveInput        storagearchive.ArchiveInput
	cloudStatusLiveInput       cloudstorage.CloudStatusLiveInput
	cloudStatusLiveCalls       int
	cloudStatusLiveReport      *cloudstorage.StatusReport
	workersOverride            []workers.WorkerListItem
	noProjects                 bool
	runtimeBindingsErr         error
	runtimeBindingRef          string
	protectedFolders           backupcontracts.ProtectedFolderListResult
	protectedFolderErr         error
	protectedPreflight         backupcontracts.PreflightRecord
	protectedCreateInput       backupcontracts.CreateRequest
	protectedPreflightInput    backupcontracts.PreflightCreateRequest
	protectedAction            string
	protectedActionKey         string
}

func newFakePortalClient() *fakePortalClient {
	return &fakePortalClient{}
}

func (c *fakePortalClient) recordSnapshotCall(name string) {
	c.snapshotCalls = append(c.snapshotCalls, name)
}

func (c *fakePortalClient) ApplyBoxWatchPolicy(_ context.Context, correlationID string, input box.WatchApplyInput) (response.Envelope[box.WatchApplyResult], error) {
	if c.optionalErr != nil {
		return response.Envelope[box.WatchApplyResult]{}, c.optionalErr
	}
	plan := box.WatchPlan{}
	if input.Plan != nil {
		plan = *input.Plan
	}
	registrations := make([]box.WatchRootRegistration, 0, len(plan.WatchedRoots))
	for _, root := range plan.WatchedRoots {
		registrations = append(registrations, box.WatchRootRegistration{
			BoxID:            plan.BoxID,
			BoxRootPath:      plan.RootPath,
			NodeID:           input.Resolved.OwnerNode,
			OwnerNodeKey:     input.Resolved.OwnerNode,
			AreaKey:          root.Key,
			BackendRootKey:   root.BackendRootKey,
			WorkerKey:        root.WorkerKey,
			ActivationStatus: box.BoxWatchStatusPendingAgentApply,
			SyncMode:         root.SyncMode,
			BackupMode:       root.BackupMode,
			IndexMode:        root.IndexMode,
		})
	}
	return response.Envelope[box.WatchApplyResult]{
		OK:   true,
		Data: box.WatchApplyResult{DryRun: input.DryRun, Plan: plan, Registrations: registrations},
		Meta: response.Meta{CorrelationID: correlationID},
	}, nil
}

func (c *fakePortalClient) GetBoxWatchStatus(_ context.Context, correlationID string, input box.WatchStatusInput) (response.Envelope[box.WatchStatusResult], error) {
	c.recordSnapshotCall("box_watch_status")
	if c.optionalErr != nil {
		return response.Envelope[box.WatchStatusResult]{}, c.optionalErr
	}
	plan := box.WatchPlan{}
	if input.Plan != nil {
		plan = *input.Plan
	}
	registrations := make([]box.WatchRootRegistration, 0, len(plan.WatchedRoots))
	for _, root := range plan.WatchedRoots {
		registrations = append(registrations, box.WatchRootRegistration{
			BoxID:            plan.BoxID,
			BoxRootPath:      plan.RootPath,
			NodeID:           input.Resolved.OwnerNode,
			OwnerNodeKey:     input.Resolved.OwnerNode,
			AreaKey:          root.Key,
			BackendRootKey:   root.BackendRootKey,
			WorkerKey:        root.WorkerKey,
			ActivationStatus: box.BoxWatchStatusPendingAgentApply,
			SyncMode:         root.SyncMode,
			BackupMode:       root.BackupMode,
			IndexMode:        root.IndexMode,
		})
	}
	return response.Envelope[box.WatchStatusResult]{
		OK:   true,
		Data: box.WatchStatusResult{Plan: plan, Registrations: registrations},
		Meta: response.Meta{CorrelationID: correlationID},
	}, nil
}

func (c *fakePortalClient) GetStorageTree(context.Context, string, storagecatalog.ListFilter) (response.Envelope[storageview.Tree], error) {
	if c.optionalErr != nil {
		return response.Envelope[storageview.Tree]{}, c.optionalErr
	}
	return response.Success("corr_test", fakeStorageTree()), nil
}

func (c *fakePortalClient) ListStorageEntries(context.Context, string, storagecatalog.ListFilter) (response.Envelope[[]storagecatalog.Entry], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]storagecatalog.Entry]{}, c.optionalErr
	}
	return response.Success("corr_test", []storagecatalog.Entry{
		fakeStorageCatalogEntry("storage_entry_report", "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md", storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments),
		fakeStorageCatalogEntry("storage_entry_archive", "main/archive/old-project/report.md", storagecatalog.StorageClassArchiveEntry, storagecatalog.SourceAreaMainArchive),
		fakeStorageCatalogEntry("storage_entry_transfer", "macbook/dropzone/video.mov", storagecatalog.StorageClassDropzoneCustody, storagecatalog.SourceAreaDropzone),
	}), nil
}

func (c *fakePortalClient) GetStorageFilesystemStatus(context.Context, string) (response.Envelope[storagedoctor.FilesystemStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[storagedoctor.FilesystemStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", storagedoctor.FilesystemStatus{
		SchemaVersion: "v0.7",
		Status:        storagedoctor.StatusOK,
		Roots:         []storagedoctor.PhysicalRootStatus{{Key: "storage", Path: "/srv/loom/storage", Role: "physical storage root", Status: storagedoctor.StatusOK, Exists: true, Directory: true}},
		Catalog:       storagedoctor.CatalogStatus{QueryLimit: 500, Returned: 3, Available: 3},
		Export:        storagedoctor.ExportCompatibilityInfo{Deprecated: true, Active: false, Message: "retired"},
		GeneratedAt:   time.Now().UTC(),
	}), nil
}

func (c *fakePortalClient) ListFileTransfers(context.Context, string, filetransfer.ListFilter) (response.Envelope[[]filetransfer.Status], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]filetransfer.Status]{}, c.optionalErr
	}
	return response.Success("corr_test", []filetransfer.Status{{
		Manifest: filetransfer.Manifest{
			TransferID:         "file_transfer_failed",
			TransferKind:       filetransfer.KindWatchedRootBackup,
			SourceNodeKey:      "macbook",
			SourceRelativePath: "Documents/video.mov",
			Status:             filetransfer.StatusFailed,
			LastErrorMessage:   "network timeout",
			ChunkCount:         4,
		},
		AcceptedChunks: 1,
	}, {
		Manifest: filetransfer.Manifest{
			TransferID:         "file_transfer_done",
			TransferKind:       filetransfer.KindDropzoneCustody,
			SourceNodeKey:      "macbook",
			SourceRelativePath: "Dropzone/archive.zip",
			Status:             filetransfer.StatusAccepted,
			ChunkCount:         2,
		},
		AcceptedChunks: 2,
	}}), nil
}

func (c *fakePortalClient) InspectStorageEntry(_ context.Context, _ string, ref string) (response.Envelope[storagecatalog.EntryDetail], error) {
	c.storageInspectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[storagecatalog.EntryDetail]{}, c.optionalErr
	}
	entry := fakeStorageCatalogEntry(firstNonEmpty(ref, "storage_entry_report"), "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md", storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments)
	refRecord := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: "storage_physical_ref_report",
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindBackupArtifact,
		URI:                  "file:///srv/loom/storage/report.md",
		NodeKey:              "main",
		Status:               storagecatalog.PhysicalRefStatusAvailable,
		CreatedAt:            time.Now().UTC(),
		UpdatedAt:            time.Now().UTC(),
	}
	return response.Success("corr_test", storagecatalog.EntryDetail{Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{refRecord}}), nil
}

func (c *fakePortalClient) ResolveStoragePath(_ context.Context, _ string, pathValue string) (response.Envelope[storageview.ResolveResult], error) {
	c.storageResolvePath = pathValue
	if c.optionalErr != nil {
		return response.Envelope[storageview.ResolveResult]{}, c.optionalErr
	}
	entry := fakeStorageViewEntry("storage_entry_report", firstNonEmpty(pathValue, "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md"), storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments)
	detail := storagecatalog.EntryDetail{Entry: fakeStorageCatalogEntry(entry.StorageEntryID, entry.ViewPath, entry.StorageClass, entry.SourceArea)}
	return response.Success("corr_test", storageview.ResolveResult{Path: entry.ViewPath, ViewEntry: entry, EntryDetail: &detail}), nil
}

func (c *fakePortalClient) GetMainDocumentsStatus(context.Context, string) (response.Envelope[mainstorage.Status], error) {
	if c.optionalErr != nil {
		return response.Envelope[mainstorage.Status]{}, c.optionalErr
	}
	now := time.Now().UTC()
	return response.Success("corr_test", mainstorage.Status{
		Exists:              true,
		StableWindowSeconds: 15,
		MaxFilesPerRun:      25,
		FilesDiscovered:     4,
		FilesAccepted:       3,
		FilesDelayed:        1,
		BytesHashed:         8192,
		LatestAcceptedAt:    &now,
		GeneratedAt:         now,
		Imports: []mainstorage.FileStatus{{
			RelativePath:   "Documents/report.md",
			State:          mainstorage.StateAccepted,
			SizeBytes:      4096,
			StorageEntryID: "storage_entry_report",
		}, {
			RelativePath:  "Documents/._preview",
			State:         mainstorage.StateIgnored,
			IgnoredReason: "AppleDouble sidecar",
		}},
	}), nil
}

func (c *fakePortalClient) GetStorageRetentionStatus(context.Context, string) (response.Envelope[storagecatalog.RetentionStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[storagecatalog.RetentionStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", storagecatalog.RetentionStatus{
		Entries:          3,
		Retained:         2,
		SafeCandidates:   1,
		UnsafeCandidates: 1,
		GeneratedAt:      time.Now().UTC(),
	}), nil
}

func (c *fakePortalClient) ArchiveStorage(_ context.Context, _ string, input storagearchive.ArchiveInput) (response.Envelope[storagearchive.ArchiveResult], error) {
	c.storageArchiveInput = input
	if c.optionalErr != nil {
		return response.Envelope[storagearchive.ArchiveResult]{}, c.optionalErr
	}
	return response.Success("corr_test", storagearchive.ArchiveResult{
		ArchiveManifest: storagecatalog.ArchiveManifest{
			ArchiveManifestID: "storage_archive_manifest_test",
			ArchiveKey:        "manual-test",
			ArchiveKind:       firstNonEmpty(input.ArchiveKind, "manual_archive"),
			Status:            "planned",
			CreatedAt:         time.Now().UTC(),
		},
		DryRun:    input.DryRun,
		CreatedAt: time.Now().UTC(),
		Entries: []storagearchive.ArchivedEntry{{
			SourceStorageEntryID: input.SourceRef,
			SourceViewPath:       "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md",
			ArchiveViewPath:      firstNonEmpty(input.TargetPath, "main/archive/report.md"),
			ArchiveObjectPath:    "report.md",
			SourceRef: storagecatalog.PhysicalRef{
				RefKind: storagecatalog.PhysicalRefKindBackupArtifact,
				Status:  storagecatalog.PhysicalRefStatusAvailable,
			},
		}},
	}), nil
}

func (c *fakePortalClient) CheckStorageSafeToDelete(_ context.Context, _ string, input storageretention.SafeToDeleteInput) (response.Envelope[storageretention.SafeToDeleteResult], error) {
	c.storageSafeInput = input
	if c.optionalErr != nil {
		return response.Envelope[storageretention.SafeToDeleteResult]{}, c.optionalErr
	}
	return response.Success("corr_test", storageretention.SafeToDeleteResult{
		Ref:       input.Ref,
		Decision:  storageretention.DecisionSafe,
		Safe:      true,
		Reasons:   []string{"main custody has an available physical ref"},
		CheckedAt: time.Now().UTC(),
	}), nil
}

func (c *fakePortalClient) FetchStorage(_ context.Context, _ string, input storageretention.FetchInput) (response.Envelope[storageretention.FetchResult], error) {
	c.storageFetchInput = input
	if c.optionalErr != nil {
		return response.Envelope[storageretention.FetchResult]{}, c.optionalErr
	}
	return response.Success("corr_test", storageretention.FetchResult{
		Ref:             input.Ref,
		DestinationPath: input.DestinationPath,
		BytesWritten:    4096,
		Entry:           fakeStorageCatalogEntry(input.Ref, "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md", storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments),
		CreatedAt:       time.Now().UTC(),
	}), nil
}

func (c *fakePortalClient) RestoreStorage(_ context.Context, _ string, input storageretention.RestoreInput) (response.Envelope[storageretention.RestoreResult], error) {
	c.storageRestoreInput = input
	if c.optionalErr != nil {
		return response.Envelope[storageretention.RestoreResult]{}, c.optionalErr
	}
	return response.Success("corr_test", storageretention.RestoreResult{
		FetchResult: storageretention.FetchResult{
			Ref:             input.Ref,
			DestinationPath: input.DestinationPath,
			BytesWritten:    4096,
			Entry:           fakeStorageCatalogEntry(input.Ref, "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md", storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments),
			CreatedAt:       time.Now().UTC(),
		},
		RestoredAt: time.Now().UTC(),
	}), nil
}

func fakeStorageTree() storageview.Tree {
	size := int64(4096)
	entries := []storageview.ViewEntry{
		fakeStorageViewEntry("storage_entry_report", "backups/macbook/loom_box__documents/local_backup_batch_test/payload/report.md", storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments),
		fakeStorageViewEntry("storage_entry_archive", "main/archive/old-project/report.md", storagecatalog.StorageClassArchiveEntry, storagecatalog.SourceAreaMainArchive),
		fakeStorageViewEntry("storage_entry_transfer", "macbook/dropzone/video.mov", storagecatalog.StorageClassDropzoneCustody, storagecatalog.SourceAreaDropzone),
	}
	entries[0].SizeBytes = &size
	return storageview.Tree{
		ViewKey: storageview.DefaultViewKey,
		Root: storageview.Node{
			Name:        "loom-main",
			Path:        "",
			EntryKind:   storageview.EntryKindDirectory,
			Permissions: storageview.PermissionReadOnly,
			Children: []storageview.Node{{
				Name:        "macbook",
				Path:        "macbook",
				EntryKind:   storageview.EntryKindDirectory,
				Permissions: storageview.PermissionReadOnly,
			}},
		},
		Entries: entries,
		Counts:  storageview.Counts{Entries: len(entries), Files: len(entries), Directories: 1, ReadOnly: 2, Writable: 1},
	}
}

func fakeStorageViewEntry(id, viewPath, class, area string) storageview.ViewEntry {
	return storageview.ViewEntry{
		ViewPath:          viewPath,
		EntryKind:         storageview.EntryKindFile,
		StorageEntryID:    id,
		StorageClass:      class,
		SourceArea:        area,
		OriginNodeKey:     "macbook",
		LogicalPath:       viewPath,
		DisplayName:       filepath.Base(viewPath),
		Permissions:       storageview.PermissionReadOnly,
		FileClass:         storagecatalog.FileClassMarkdown,
		ProcessingState:   storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateRetained,
	}
}

func fakeStorageCatalogEntry(id, viewPath, class, area string) storagecatalog.Entry {
	now := time.Now().UTC()
	size := int64(4096)
	return storagecatalog.Entry{
		StorageEntryID:    firstNonEmpty(id, "storage_entry_report"),
		StorageClass:      class,
		SourceArea:        area,
		OriginNodeKey:     "macbook",
		LogicalPath:       viewPath,
		CurrentViewPath:   viewPath,
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassMarkdown,
		ProcessingState:   storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateRetained,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func jsonRaw(value string) json.RawMessage {
	return json.RawMessage(value)
}

func fakeProjectArchiveState(ref string) json.RawMessage {
	payload, _ := json.Marshal(storagearchive.ProjectRuntimeArchiveState{
		SchemaVersion:           "project.archive_state.v0.6",
		Status:                  "archived",
		ProjectRuntimeArchiveID: "project_runtime_archive_portal",
		RuntimeManifestPath:     "/home/loomadmin/loom-box/Archive/Projects/" + ref + "/runtime.json",
		SourceRef:               "/home/loomadmin/loom-box/Projects/" + ref,
		TargetPath:              "main/Archive/Projects/" + ref,
		SuccessorPolicy:         storagearchive.ProjectRuntimeSuccessorPolicy{Status: "not_migrated"},
		SafeToDelete:            true,
	})
	return payload
}

func fakeArchivedPortalProject() projects.Project {
	homeNode := "main"
	return projects.Project{
		ProjectID:       "project_portal",
		ProjectScopeID:  "scope_portal_project",
		ProjectScopeKey: "project.portal-project",
		Slug:            "portal-project",
		Name:            "Portal Project",
		HomeNodeID:      &homeNode,
		Status:          "archived",
		ProjectType:     "automation",
		ArchiveState:    fakeProjectArchiveState("portal-project"),
	}
}

func ptrProjectRegistrationDetail(value projects.ProjectRegistrationDetail) *projects.ProjectRegistrationDetail {
	return &value
}

func fakeProjectRegistrationDetail() projects.ProjectRegistrationDetail {
	homeNode := "main"
	report, _ := json.Marshal(projectcontracts.ValidationReport{
		SchemaVersion: projectcontracts.ReportSchemaV03,
		OK:            true,
		Registerable:  true,
		Project:       projectcontracts.PlanProject{Slug: "portal-project", Name: "Portal Project", OwnerNode: "main", Status: "active"},
		Summary:       projectcontracts.DiagnosticSummary{},
	})
	plan, _ := json.Marshal(projectcontracts.ProjectPlan{
		SchemaVersion: projectcontracts.PlanSchemaV03,
		Registerable:  true,
		Project:       projectcontracts.PlanProject{Slug: "portal-project", Name: "Portal Project", OwnerNode: "main", Status: "active"},
		Facets:        []projectcontracts.PlanFacet{{Key: "scripts", Enabled: true, Present: true}},
		Workflows:     []projectcontracts.WorkflowFacetItem{{WorkflowID: "draft_workflow", Name: "Draft Workflow", ContractStatus: "draft", ImplementationKind: "placeholder"}},
		Actions:       []projectcontracts.PlanAction{{Action: "would_register_project", Status: "planned", TargetKind: "project", TargetRef: "portal-project"}},
		Summary:       projectcontracts.DiagnosticSummary{},
	})
	return projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{
			ProjectID:       "project_portal",
			ProjectScopeKey: "project.portal-project",
			Slug:            "portal-project",
			Name:            "Portal Project",
			HomeNodeID:      &homeNode,
			Status:          "active",
			ProjectType:     "automation",
		}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_portal",
			ProjectID:                     "project_portal",
			ProjectRoot:                   "/tmp/portal-project",
			ContractPath:                  "/tmp/portal-project/" + projectcontracts.CanonicalRootContractPath,
			RegistrationStatus:            "registered",
			ActivationStatus:              projects.ProjectActivationStatusBaseActive,
			RegistrationRevision:          1,
			ValidationReport:              report,
			RegistrationPlan:              plan,
		},
		Facets: []projects.ProjectContractFacet{{
			FacetKey:    "scripts",
			Enabled:     true,
			Present:     true,
			FacetStatus: "active",
		}},
		ScriptExposures: []projects.ProjectScriptExposure{{
			ScriptKey:         "curl_url",
			CapabilityAddress: "main@portal-project.curl_url",
			ActivationStatus:  "active",
		}},
		WatchedRootRegistrations: []projects.ProjectWatchedRootRegistration{{
			LocalRootKey:     "notes",
			DisplayName:      "Notes",
			SyncMode:         "sync",
			BackupMode:       "backup",
			IndexMode:        "text",
			ActivationStatus: "registered",
		}},
	}
}

func (c *fakePortalClient) Health(context.Context, string) (response.Envelope[health.Report], error) {
	c.recordSnapshotCall("health")
	if c.healthErr != nil {
		return response.Envelope[health.Report]{}, c.healthErr
	}
	return response.Success("corr_test", health.Report{
		Service: "loomd",
		Status:  "ok",
		Node:    health.NodeInfo{ID: "main", Role: "main"},
		Checks: health.Checks{
			Migrations: health.CheckResult{Status: "ok"},
			Bootstrap:  health.CheckResult{Status: "ok"},
		},
	}), nil
}

func (c *fakePortalClient) Status(context.Context, string) (response.Envelope[loomstatus.Report], error) {
	c.recordSnapshotCall("status")
	if c.statusErr != nil {
		return response.Envelope[loomstatus.Report]{}, c.statusErr
	}
	return response.Success("corr_test", loomstatus.Report{Status: "ok"}), nil
}

func (c *fakePortalClient) CloudStatusLive(_ context.Context, correlationID string, input cloudstorage.CloudStatusLiveInput) (response.Envelope[cloudstorage.StatusReport], error) {
	if c.optionalErr != nil {
		return response.Envelope[cloudstorage.StatusReport]{}, c.optionalErr
	}
	c.cloudStatusLiveCalls++
	c.cloudStatusLiveInput = input
	if c.cloudStatusLiveReport != nil {
		return response.Success(correlationID, *c.cloudStatusLiveReport), nil
	}
	return response.Success(correlationID, cloudstorage.StatusReport{
		Status:    "reachable",
		Mode:      string(cloudstorage.StatusModeLive),
		CheckedAt: time.Date(2026, 6, 20, 11, 0, 0, 0, time.UTC),
		Config: cloudstorage.ConfigSummary{
			Path:            firstNonEmpty(input.ConfigPath, cloudstorage.DefaultConfigPath),
			Exists:          true,
			Enabled:         true,
			Provider:        "hetzner_storage_box",
			RemoteName:      "loom-cloud-storage",
			RemoteRoot:      "loom",
			SnapshotBackend: "borg",
		},
	}), nil
}

func (c *fakePortalClient) ListWorkers(context.Context, string, workers.WorkerFilter) (response.Envelope[[]workers.WorkerListItem], error) {
	c.recordSnapshotCall("workers")
	if c.optionalErr != nil {
		return response.Envelope[[]workers.WorkerListItem]{}, c.optionalErr
	}
	if c.workersOverride != nil {
		return response.Success("corr_test", c.workersOverride), nil
	}
	return response.Success("corr_test", []workers.WorkerListItem{{
		WorkerKey:       "main.worker_selfcheck",
		WorkerKind:      workers.KindSelfcheck,
		HealthStatus:    workers.HealthHealthy,
		LifecycleStatus: workers.LifecycleActive,
		Enabled:         true,
	}}), nil
}

func (c *fakePortalClient) InspectWorker(_ context.Context, _ string, ref string) (response.Envelope[workers.WorkerDetail], error) {
	c.inspectWorkerRef = ref
	if c.optionalErr != nil {
		return response.Envelope[workers.WorkerDetail]{}, c.optionalErr
	}
	return response.Success("corr_test", workers.WorkerDetail{
		Instance: workers.WorkerInstance{WorkerKey: firstNonEmpty(ref, "main.worker_selfcheck"), WorkerKind: workers.KindSelfcheck, Enabled: true, LifecycleStatus: workers.LifecycleActive},
	}), nil
}

func (c *fakePortalClient) ListWorkerRuns(context.Context, string, string, workers.RunFilter) (response.Envelope[[]workers.WorkerRun], error) {
	if c.workerRunsErr != nil {
		return response.Envelope[[]workers.WorkerRun]{}, c.workerRunsErr
	}
	if c.optionalErr != nil {
		return response.Envelope[[]workers.WorkerRun]{}, c.optionalErr
	}
	startedAt := time.Now().UTC().Add(-time.Minute)
	return response.Success("corr_test", []workers.WorkerRun{{
		WorkerRunID:   "worker_run_recent",
		WorkerKind:    workers.KindSelfcheck,
		RunStatus:     workers.RunStatusSucceeded,
		TriggerKind:   workers.TriggerManual,
		StartedAt:     startedAt,
		CorrelationID: "corr_test",
	}}), nil
}

func (c *fakePortalClient) MaintenanceStatus(context.Context, string) (response.Envelope[maintenance.Status], error) {
	c.recordSnapshotCall("maintenance")
	if c.optionalErr != nil {
		return response.Envelope[maintenance.Status]{}, c.optionalErr
	}
	return response.Success("corr_test", maintenance.Status{
		OverallStatus: maintenance.OverallWarning,
		Findings:      maintenance.FindingSummary{Open: 1, Warning: 1},
	}), nil
}

func (c *fakePortalClient) ListMaintenanceFindings(context.Context, string, maintenance.FindingFilter) (response.Envelope[[]maintenance.Finding], error) {
	c.recordSnapshotCall("maintenance_findings")
	if c.optionalErr != nil {
		return response.Envelope[[]maintenance.Finding]{}, c.optionalErr
	}
	return response.Success("corr_test", []maintenance.Finding{{
		MaintenanceFindingID: "maintenance_finding_test",
		FindingKey:           "backup_stale",
		WorkerKey:            "main.main_backup",
		Severity:             maintenance.SeverityWarning,
		Status:               maintenance.FindingOpen,
		Summary:              "backup stale",
	}}), nil
}

func (c *fakePortalClient) MaintenanceDBStatus(context.Context, string) (response.Envelope[maintenance.DBStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[maintenance.DBStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", maintenance.DBStatus{
		Status:          maintenance.OverallOK,
		MigrationStatus: "ok",
		CurrentVersion:  25,
		LatestVersion:   25,
		Warnings:        []string{"events.events is over 1 GiB; run `loom maintenance retention dry-run --json` before any cleanup decision"},
		Retention: maintenance.DatabaseRetention{
			Candidates: map[string]int64{"events.events": 100},
			Plans: []maintenance.DatabaseRetentionPlan{{
				Table:          "events.events",
				CandidateClass: "routine_success_events",
				CandidateRows:  100,
				KeepRule:       "keep audit/security/failure events",
				FutureAction:   "future rollup plus reviewed deletion of routine success events only",
			}},
		},
	}), nil
}

func (c *fakePortalClient) MaintenanceBackupStatus(context.Context, string) (response.Envelope[maintenance.BackupStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[maintenance.BackupStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", maintenance.BackupStatus{
		Status:       maintenance.OverallWarning,
		OpenFindings: maintenance.FindingSummary{Open: 1, Warning: 1},
		LatestSuccessful: &maintenance.BackupOperation{
			Operation: maintenance.Operation{MaintenanceOperationID: "backup_success", OperationKind: maintenance.OperationKindMainBackup, Status: maintenance.OperationSucceeded, StartedAt: time.Now().UTC().Add(-time.Hour)},
		},
	}), nil
}

func (c *fakePortalClient) ListMaintenanceBackups(context.Context, string, maintenance.OperationFilter) (response.Envelope[[]maintenance.BackupOperation], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]maintenance.BackupOperation]{}, c.optionalErr
	}
	return response.Success("corr_test", []maintenance.BackupOperation{{
		Operation: maintenance.Operation{
			MaintenanceOperationID: "backup_operation_test",
			OperationKey:           "backup-operation-test",
			OperationKind:          maintenance.OperationKindMainBackup,
			Status:                 maintenance.OperationSucceeded,
			StartedAt:              time.Now().UTC().Add(-time.Hour),
		},
		Artifacts: []maintenance.Artifact{{MaintenanceArtifactID: "artifact_test", ArtifactKind: maintenance.ArtifactKindBackupManifest}},
	}}), nil
}

func (c *fakePortalClient) RunMaintenanceBackup(ctx context.Context, correlationID string, input maintenance.BackupRunInput) (response.Envelope[workers.RunOnceResult], error) {
	c.backupRunInput = input
	if c.runErr != nil {
		return response.Envelope[workers.RunOnceResult]{}, c.runErr
	}
	raw := jsonRaw(`{"backup_operation_id":"backup_operation_test","backup_dir":"/tmp/loom-backup","artifact_count":2,"total_bytes":128}`)
	return response.SuccessWithIdempotency(correlationID, input.IdempotencyKey, workers.RunOnceResult{
		Worker: workers.WorkerDetail{Instance: workers.WorkerInstance{WorkerKey: "main.main_backup", WorkerKind: workers.KindMainBackup}},
		Run: workers.WorkerRun{
			WorkerRunID:       "worker_run_backup",
			WorkerKind:        workers.KindMainBackup,
			RunStatus:         workers.RunStatusSucceeded,
			CorrelationID:     correlationID,
			IdempotencyKey:    input.IdempotencyKey,
			ResultSummaryJSON: raw,
		},
	}), nil
}

func (c *fakePortalClient) VerifyMaintenanceBackup(_ context.Context, _ string, input maintenance.BackupVerifyInput) (response.Envelope[maintenance.BackupVerification], error) {
	c.backupVerifyRef = input.BackupRef
	if c.optionalErr != nil {
		return response.Envelope[maintenance.BackupVerification]{}, c.optionalErr
	}
	return response.Success("corr_test", maintenance.BackupVerification{
		Status:              maintenance.VerificationSucceeded,
		BackupOperationID:   input.BackupRef,
		BackupDir:           "/tmp/loom-backup",
		ManifestPath:        "/tmp/loom-backup/manifest.json",
		CheckedAt:           time.Now().UTC(),
		Checks:              map[string]string{"manifest": "ok"},
		VerifiedArtifactNum: 1,
	}), nil
}

func (c *fakePortalClient) MaintenanceObjectStoreStatus(context.Context, string) (response.Envelope[maintenance.ObjectStoreStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[maintenance.ObjectStoreStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", maintenance.ObjectStoreStatus{
		Status:     maintenance.OverallOK,
		BlobCounts: maintenance.ObjectStoreBlobCounts{Total: 3, Verified: 2, Pending: 1},
		Findings:   maintenance.FindingSummary{},
	}), nil
}

func (c *fakePortalClient) RunMaintenanceObjectStoreScan(ctx context.Context, correlationID string, input maintenance.ObjectStoreScanInput) (response.Envelope[workers.RunOnceResult], error) {
	c.objectScanInput = input
	if c.runErr != nil {
		return response.Envelope[workers.RunOnceResult]{}, c.runErr
	}
	raw := jsonRaw(`{"status":"ok","mode":"sample","checked":3,"verified":3,"missing":0,"corrupt":0,"findings_opened":0,"findings_resolved":1}`)
	return response.SuccessWithIdempotency(correlationID, input.IdempotencyKey, workers.RunOnceResult{
		Worker: workers.WorkerDetail{Instance: workers.WorkerInstance{WorkerKey: "main.object_store_integrity", WorkerKind: workers.KindObjectStore}},
		Run: workers.WorkerRun{
			WorkerRunID:       "worker_run_object_scan",
			WorkerKind:        workers.KindObjectStore,
			RunStatus:         workers.RunStatusSucceeded,
			CorrelationID:     correlationID,
			IdempotencyKey:    input.IdempotencyKey,
			ResultSummaryJSON: raw,
		},
	}), nil
}

func (c *fakePortalClient) ListAutomations(context.Context, string, automation.AutomationFilter) (response.Envelope[[]automation.Automation], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]automation.Automation]{}, c.optionalErr
	}
	return response.Success("corr_test", []automation.Automation{{
		AutomationID:  "automation_test",
		AutomationKey: "daily_backup",
		DisplayName:   "Daily Backup",
		Status:        "active",
		SourceKind:    "schedule",
	}}), nil
}

func (c *fakePortalClient) ListSchedules(context.Context, string, automation.ScheduleFilter) (response.Envelope[[]automation.Schedule], error) {
	c.recordSnapshotCall("schedule_list")
	if c.optionalErr != nil {
		return response.Envelope[[]automation.Schedule]{}, c.optionalErr
	}
	nextFire := time.Now().UTC().Add(time.Hour)
	return response.Success("corr_test", []automation.Schedule{{
		ScheduleID:  "schedule_test",
		ScheduleKey: "daily_backup",
		Status:      automation.ScheduleStatusActive,
		NextFireAt:  &nextFire,
	}}), nil
}

func (c *fakePortalClient) GetSchedule(_ context.Context, _ string, ref string) (response.Envelope[automation.ScheduleDetail], error) {
	c.scheduleRef = ref
	if c.optionalErr != nil {
		return response.Envelope[automation.ScheduleDetail]{}, c.optionalErr
	}
	nextFire := time.Now().UTC().Add(time.Hour)
	return response.Success("corr_test", automation.ScheduleDetail{
		Schedule: automation.Schedule{
			ScheduleID:        firstNonEmpty(ref, "schedule_test"),
			AutomationID:      "automation_test",
			ScheduleKey:       "daily_backup",
			DisplayName:       "Daily Backup",
			Status:            automation.ScheduleStatusActive,
			ScheduleKind:      automation.ScheduleKindInterval,
			ScheduleExpr:      "0 9 * * *",
			Timezone:          "UTC",
			NextFireAt:        &nextFire,
			RunAsActorID:      "actor_scheduler",
			TargetProfileJSON: jsonRaw(`{"capability_ref":"main@system.status.read"}`),
		},
		Automation: automation.Automation{AutomationID: "automation_test", AutomationKey: "daily_backup", DisplayName: "Daily Backup", Status: automation.AutomationStatusActive},
	}), nil
}

func (c *fakePortalClient) FireScheduleNow(_ context.Context, _ string, ref string, input automation.FireScheduleInput) (response.Envelope[automation.FireScheduleResult], error) {
	c.fireScheduleRef = ref
	c.fireScheduleInput = input
	if c.optionalErr != nil {
		return response.Envelope[automation.FireScheduleResult]{}, c.optionalErr
	}
	fire := automation.ScheduleFire{ScheduleFireID: "schedule_fire_portal", ScheduleID: firstNonEmpty(ref, "schedule_test"), AutomationID: "automation_test", ScheduledFor: time.Now().UTC(), Status: automation.FireStatusInvocationCreated}
	invocation := automation.Invocation{InvocationID: "invocation_portal", AutomationID: "automation_test", Status: automation.InvocationStatusPending, TargetCapability: "main@system.status.read"}
	return response.Success("corr_test", automation.FireScheduleResult{
		Schedule:   automation.Schedule{ScheduleID: firstNonEmpty(ref, "schedule_test"), ScheduleKey: "daily_backup", DisplayName: "Daily Backup", Status: automation.ScheduleStatusActive},
		Fire:       fire,
		Invocation: invocation,
	}), nil
}

func (c *fakePortalClient) PauseSchedule(_ context.Context, _ string, ref string, input automation.UpdateScheduleStatusInput) (response.Envelope[automation.ScheduleDetail], error) {
	c.pauseScheduleRef = ref
	if c.optionalErr != nil {
		return response.Envelope[automation.ScheduleDetail]{}, c.optionalErr
	}
	return response.Success("corr_test", automation.ScheduleDetail{
		Schedule:   automation.Schedule{ScheduleID: firstNonEmpty(ref, "schedule_test"), ScheduleKey: "daily_backup", DisplayName: "Daily Backup", Status: automation.ScheduleStatusPaused},
		Automation: automation.Automation{AutomationID: "automation_test", AutomationKey: "daily_backup", DisplayName: "Daily Backup"},
	}), nil
}

func (c *fakePortalClient) ResumeSchedule(_ context.Context, _ string, ref string, input automation.UpdateScheduleStatusInput) (response.Envelope[automation.ScheduleDetail], error) {
	c.resumeScheduleRef = ref
	if c.optionalErr != nil {
		return response.Envelope[automation.ScheduleDetail]{}, c.optionalErr
	}
	return response.Success("corr_test", automation.ScheduleDetail{
		Schedule:   automation.Schedule{ScheduleID: firstNonEmpty(ref, "schedule_test"), ScheduleKey: "daily_backup", DisplayName: "Daily Backup", Status: automation.ScheduleStatusActive},
		Automation: automation.Automation{AutomationID: "automation_test", AutomationKey: "daily_backup", DisplayName: "Daily Backup"},
	}), nil
}

func (c *fakePortalClient) ScheduleStatus(context.Context, string) (response.Envelope[automation.ScheduleStatus], error) {
	c.recordSnapshotCall("schedules")
	if c.optionalErr != nil {
		return response.Envelope[automation.ScheduleStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", automation.ScheduleStatus{
		ActiveScheduleCount:    2,
		PausedScheduleCount:    1,
		FailedInvocationCount:  1,
		SchedulerWorkerKey:     "main.automation_scheduler",
		DispatcherWorkerKey:    "main.automation_dispatcher",
		PendingInvocationCount: 1,
	}), nil
}

func (c *fakePortalClient) ListScheduleFires(context.Context, string, automation.ScheduleFireFilter) (response.Envelope[[]automation.ScheduleFire], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]automation.ScheduleFire]{}, c.optionalErr
	}
	return response.Success("corr_test", []automation.ScheduleFire{{
		ScheduleFireID: "schedule_fire_test",
		ScheduleID:     "schedule_test",
		AutomationID:   "automation_test",
		ScheduledFor:   time.Now().UTC(),
		Status:         "succeeded",
	}}), nil
}

func (c *fakePortalClient) GetScheduleFire(_ context.Context, _ string, ref string) (response.Envelope[automation.ScheduleFire], error) {
	c.scheduleFireRef = ref
	if c.optionalErr != nil {
		return response.Envelope[automation.ScheduleFire]{}, c.optionalErr
	}
	return response.Success("corr_test", automation.ScheduleFire{
		ScheduleFireID: firstNonEmpty(ref, "schedule_fire_test"),
		ScheduleID:     "schedule_test",
		AutomationID:   "automation_test",
		ScheduledFor:   time.Now().UTC(),
		Status:         automation.FireStatusCompleted,
	}), nil
}

func (c *fakePortalClient) ListIntegrations(context.Context, string, automation.IntegrationFilter) (response.Envelope[[]automation.Integration], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]automation.Integration]{}, c.optionalErr
	}
	return response.Success("corr_test", []automation.Integration{{
		IntegrationID:  "integration_test",
		IntegrationKey: "gmail",
		DisplayName:    "Gmail",
		Status:         "active",
	}}), nil
}

func (c *fakePortalClient) ListDirectEventEndpoints(context.Context, string, automation.DirectEventEndpointFilter) (response.Envelope[[]automation.DirectEventEndpoint], error) {
	c.recordSnapshotCall("direct_event_endpoints")
	if c.optionalErr != nil {
		return response.Envelope[[]automation.DirectEventEndpoint]{}, c.optionalErr
	}
	return response.Success("corr_test", []automation.DirectEventEndpoint{{
		EndpointID:   "direct_event_endpoint_test",
		EndpointSlug: "gmail-url",
		Status:       automation.DirectEventEndpointStatusActive,
		EventType:    "gmail.message",
		ResponseMode: automation.DirectEventResponseAccepted,
	}}), nil
}

func (c *fakePortalClient) GetDirectEventEndpoint(_ context.Context, _ string, ref string) (response.Envelope[automation.DirectEventEndpointDetail], error) {
	c.directEndpointRef = ref
	if c.optionalErr != nil {
		return response.Envelope[automation.DirectEventEndpointDetail]{}, c.optionalErr
	}
	return response.Success("corr_test", automation.DirectEventEndpointDetail{
		Endpoint:    automation.DirectEventEndpoint{EndpointID: firstNonEmpty(ref, "direct_event_endpoint_test"), EndpointSlug: "gmail-url", DisplayName: "Gmail URL", Status: automation.DirectEventEndpointStatusActive, EventType: "gmail.message", EndpointPath: "/v1/direct-events/ingest/gmail-url", ResponseMode: automation.DirectEventResponseAccepted, IntegrationID: "integration_test", AutomationID: "automation_test"},
		Integration: automation.Integration{IntegrationID: "integration_test", IntegrationKey: "gmail", DisplayName: "Gmail"},
		Automation:  automation.Automation{AutomationID: "automation_test", AutomationKey: "gmail_url", DisplayName: "Gmail URL"},
	}), nil
}

func (c *fakePortalClient) ListDirectEvents(context.Context, string, automation.DirectEventFilter) (response.Envelope[[]automation.DirectEvent], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]automation.DirectEvent]{}, c.optionalErr
	}
	return response.Success("corr_test", []automation.DirectEvent{{
		DirectEventID: "direct_event_recent",
		Status:        automation.DirectEventStatusAccepted,
		RequestPath:   "/v1/direct-events/ingest/gmail-url",
		ReceivedAt:    time.Now().UTC(),
	}}), nil
}

func (c *fakePortalClient) GetDirectEvent(_ context.Context, _ string, ref string) (response.Envelope[automation.DirectEventDetail], error) {
	c.directEventRef = ref
	if c.optionalErr != nil {
		return response.Envelope[automation.DirectEventDetail]{}, c.optionalErr
	}
	invocation := automation.Invocation{InvocationID: "invocation_recent", Status: automation.InvocationStatusSucceeded, TargetCapability: "main@system.status.read"}
	return response.Success("corr_test", automation.DirectEventDetail{
		DirectEvent: automation.DirectEvent{DirectEventID: firstNonEmpty(ref, "direct_event_recent"), EndpointID: "direct_event_endpoint_test", IntegrationID: "integration_test", AutomationID: "automation_test", Status: automation.DirectEventStatusCompleted, ExternalEventID: "gmail_message_test", RequestPath: "/v1/direct-events/ingest/gmail-url", PayloadHash: "sha256:event", AttemptCount: 1, InvocationID: &invocation.InvocationID, ReceivedAt: time.Now().UTC()},
		Endpoint:    automation.DirectEventEndpoint{EndpointID: "direct_event_endpoint_test", EndpointSlug: "gmail-url", DisplayName: "Gmail URL"},
		Integration: automation.Integration{IntegrationID: "integration_test", IntegrationKey: "gmail", DisplayName: "Gmail"},
		Automation:  automation.Automation{AutomationID: "automation_test", AutomationKey: "gmail_url", DisplayName: "Gmail URL"},
		Invocation:  &invocation,
	}), nil
}

func (c *fakePortalClient) GetDirectEventRawPayload(_ context.Context, _ string, ref string) (response.Envelope[automation.DirectEventRawPayload], error) {
	c.directRawRef = ref
	if c.optionalErr != nil {
		return response.Envelope[automation.DirectEventRawPayload]{}, c.optionalErr
	}
	return response.Success("corr_test", automation.DirectEventRawPayload{
		DirectEventID: firstNonEmpty(ref, "direct_event_recent"),
		HeadersJSON:   jsonRaw(`{"x-test":"yes"}`),
		QueryJSON:     jsonRaw(`{"source":"smoke"}`),
		BodyJSON:      jsonRaw(`{"url":"https://example.com"}`),
		PayloadHash:   "sha256:event",
		ReceivedAt:    time.Now().UTC(),
	}), nil
}

func (c *fakePortalClient) DirectEventStatus(context.Context, string) (response.Envelope[automation.DirectEventStatus], error) {
	c.recordSnapshotCall("direct_events")
	if c.optionalErr != nil {
		return response.Envelope[automation.DirectEventStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", automation.DirectEventStatus{
		ActiveEndpointCount:        1,
		FailedCount:                1,
		MappingPendingCount:        1,
		InvocationCreatedCount:     3,
		DirectEventIngestWorkerKey: "main.direct_event_ingest",
		DispatcherWorkerKey:        "main.automation_dispatcher",
	}), nil
}

func (c *fakePortalClient) ListInvocations(_ context.Context, _ string, filter automation.InvocationFilter) (response.Envelope[[]automation.Invocation], error) {
	c.invocationFilter = filter
	if c.optionalErr != nil {
		return response.Envelope[[]automation.Invocation]{}, c.optionalErr
	}
	return response.Success("corr_test", []automation.Invocation{{
		InvocationID:     "invocation_recent",
		Status:           automation.InvocationStatusSucceeded,
		TargetCapability: "main@system.status.read",
	}}), nil
}

func (c *fakePortalClient) GetInvocation(_ context.Context, _ string, ref string) (response.Envelope[automation.Invocation], error) {
	c.invocationRef = ref
	if c.optionalErr != nil {
		return response.Envelope[automation.Invocation]{}, c.optionalErr
	}
	jobID := "job_recent"
	callID := "capability_call_recent"
	return response.Success("corr_test", automation.Invocation{
		InvocationID:        firstNonEmpty(ref, "invocation_recent"),
		AutomationID:        "automation_test",
		SourceKind:          automation.SourceKindSchedule,
		SourceOccurrenceRef: "schedule_fire_test",
		Status:              automation.InvocationStatusSucceeded,
		TargetCapability:    "main@system.status.read",
		AttemptCount:        1,
		MaxAttempts:         1,
		CapabilityCallID:    &callID,
		JobID:               &jobID,
	}), nil
}

func (c *fakePortalClient) ListInvocationFailures(_ context.Context, _ string, filter automation.InvocationFilter) (response.Envelope[[]automation.Invocation], error) {
	c.recordSnapshotCall("invocation_failures")
	c.invocationFailureFilter = filter
	if c.optionalErr != nil {
		return response.Envelope[[]automation.Invocation]{}, c.optionalErr
	}
	return response.Success("corr_test", []automation.Invocation{{InvocationID: "invocation_test", Status: automation.InvocationStatusFailed}}), nil
}

func (c *fakePortalClient) ListDirectEventFailures(context.Context, string, automation.DirectEventFilter) (response.Envelope[[]automation.DirectEvent], error) {
	c.recordSnapshotCall("direct_event_failures")
	if c.optionalErr != nil {
		return response.Envelope[[]automation.DirectEvent]{}, c.optionalErr
	}
	return response.Success("corr_test", []automation.DirectEvent{{
		DirectEventID: "direct_event_test",
		Status:        automation.DirectEventStatusFailed,
		RequestPath:   "/v1/direct-events/ingest/gmail-url",
	}}), nil
}

func (c *fakePortalClient) JobStatus(context.Context, string) (response.Envelope[jobs.QueueSummary], error) {
	c.recordSnapshotCall("jobs")
	if c.optionalErr != nil {
		return response.Envelope[jobs.QueueSummary]{}, c.optionalErr
	}
	return response.Success("corr_test", jobs.QueueSummary{QueuedCount: 1, RunningCount: 1, FailedCount: 1}), nil
}

func (c *fakePortalClient) ListJobs(context.Context, string, jobs.ListFilter) (response.Envelope[[]jobs.Job], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]jobs.Job]{}, c.optionalErr
	}
	return response.Success("corr_test", []jobs.Job{{JobID: "job_recent", JobType: "script", Status: "running"}}), nil
}

func (c *fakePortalClient) ListQueuedJobs(context.Context, string, jobs.ListFilter) (response.Envelope[[]jobs.Job], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]jobs.Job]{}, c.optionalErr
	}
	return response.Success("corr_test", []jobs.Job{{JobID: "job_queued", JobType: "script", Status: "queued"}}), nil
}

func (c *fakePortalClient) ListFailedJobs(context.Context, string, jobs.ListFilter) (response.Envelope[[]jobs.Job], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]jobs.Job]{}, c.optionalErr
	}
	return response.Success("corr_test", []jobs.Job{{JobID: "job_failed", JobType: "script", Status: "failed"}}), nil
}

func (c *fakePortalClient) GetJob(_ context.Context, _ string, ref string) (response.Envelope[jobs.JobDetail], error) {
	c.jobInspectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[jobs.JobDetail]{}, c.optionalErr
	}
	job := jobs.Job{JobID: firstNonEmpty(ref, "job_recent"), JobType: "script", Status: jobs.StatusFailed, OriginNodeID: "node_main", ExecutionNodeID: "node_main", AttemptCount: 1, MaxAttempts: 2, CreatedAt: time.Now().UTC().Add(-time.Hour)}
	return response.Success("corr_test", jobs.JobDetail{
		Job:      job,
		Attempts: []jobs.Attempt{{JobAttemptID: "job_attempt_test", JobID: job.JobID, AttemptNumber: 1, Status: jobs.StatusFailed, WorkdirPath: "/tmp/loom-job"}},
		Logs:     []jobs.JobLog{{JobLogID: "job_log_test", JobID: job.JobID, Stream: "stdout", StorageKind: "inline", TailText: "hello"}},
		Outputs:  []jobs.JobOutput{{JobOutputID: "job_output_test", JobID: job.JobID, OutputKey: "result", OutputType: "json", Status: "ready", ValueJSON: jsonRaw(`{"ok":true}`)}},
	}), nil
}

func (c *fakePortalClient) GetJobLogs(_ context.Context, _ string, ref string) (response.Envelope[[]jobs.JobLog], error) {
	c.jobLogsRef = ref
	if c.optionalErr != nil {
		return response.Envelope[[]jobs.JobLog]{}, c.optionalErr
	}
	return response.Success("corr_test", []jobs.JobLog{{JobLogID: "job_log_test", JobID: firstNonEmpty(ref, "job_recent"), Stream: "stdout", StorageKind: "inline", TailText: "portal log tail", ByteCount: 15}}), nil
}

func (c *fakePortalClient) GetJobOutputs(_ context.Context, _ string, ref string) (response.Envelope[[]jobs.JobOutput], error) {
	c.jobOutputsRef = ref
	if c.optionalErr != nil {
		return response.Envelope[[]jobs.JobOutput]{}, c.optionalErr
	}
	return response.Success("corr_test", []jobs.JobOutput{{JobOutputID: "job_output_test", JobID: firstNonEmpty(ref, "job_recent"), OutputKey: "result", OutputType: "json", Status: "ready", ValueJSON: jsonRaw(`{"ok":true}`)}}), nil
}

func (c *fakePortalClient) CancelJob(_ context.Context, _ string, input jobs.CancelJobInput) (response.Envelope[jobs.Job], error) {
	c.jobCancelRef = input.JobRef
	if c.optionalErr != nil {
		return response.Envelope[jobs.Job]{}, c.optionalErr
	}
	return response.Success("corr_test", jobs.Job{JobID: input.JobRef, JobType: "script", Status: jobs.StatusCancelled, AttemptCount: 1, MaxAttempts: 2}), nil
}

func (c *fakePortalClient) RetryJob(_ context.Context, _ string, input jobs.RetryJobInput) (response.Envelope[jobs.Job], error) {
	c.jobRetryRef = input.JobRef
	if c.optionalErr != nil {
		return response.Envelope[jobs.Job]{}, c.optionalErr
	}
	return response.Success("corr_test", jobs.Job{JobID: input.JobRef, JobType: "script", Status: jobs.StatusQueued, AttemptCount: 1, MaxAttempts: 2}), nil
}

func (c *fakePortalClient) AcknowledgeJobAttention(_ context.Context, _ string, input jobs.JobAttentionInput) (response.Envelope[jobs.Job], error) {
	c.jobAcknowledgeRef = input.JobRef
	if c.optionalErr != nil {
		return response.Envelope[jobs.Job]{}, c.optionalErr
	}
	return response.Success("corr_test", jobs.Job{JobID: input.JobRef, JobType: "script", Status: jobs.StatusFailed, FailureAttentionStatus: jobs.FailureAttentionStatusAcknowledged, AttemptCount: 1, MaxAttempts: 2}), nil
}

func (c *fakePortalClient) ArchiveJobAttention(_ context.Context, _ string, input jobs.JobAttentionInput) (response.Envelope[jobs.Job], error) {
	c.jobArchiveRef = input.JobRef
	if c.optionalErr != nil {
		return response.Envelope[jobs.Job]{}, c.optionalErr
	}
	return response.Success("corr_test", jobs.Job{JobID: input.JobRef, JobType: "script", Status: jobs.StatusFailed, FailureAttentionStatus: jobs.FailureAttentionStatusArchived, AttemptCount: 1, MaxAttempts: 2}), nil
}

func (c *fakePortalClient) RunnerStatus(context.Context, string) (response.Envelope[jobs.QueueSummary], error) {
	if c.optionalErr != nil {
		return response.Envelope[jobs.QueueSummary]{}, c.optionalErr
	}
	return response.Success("corr_test", jobs.QueueSummary{RunnerCount: 1, IdleRunnerCount: 1}), nil
}

func (c *fakePortalClient) ListRunners(context.Context, string, jobs.RunnerFilter) (response.Envelope[[]jobs.Runner], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]jobs.Runner]{}, c.optionalErr
	}
	return response.Success("corr_test", []jobs.Runner{{RunnerID: "runner_test", RunnerKey: jobs.DefaultRunnerKey, Status: "idle"}}), nil
}

func (c *fakePortalClient) GetKnowledgeNotesOverview(_ context.Context, _ string, input knowledge.NotesOverviewInput) (response.Envelope[knowledge.NotesOverview], error) {
	c.notesOverviewInput = input
	if c.optionalErr != nil {
		return response.Envelope[knowledge.NotesOverview]{}, c.optionalErr
	}
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	indexedAt := now.Add(-15 * time.Minute)
	rebuildAt := now.Add(-1 * time.Hour)
	boxRoot := knowledge.NotesRootOverview{
		NotesSourceRootID: "notes_root_box",
		RootKind:          knowledge.RootKindBoxNotes,
		NodeKey:           "main",
		DisplayName:       "Box Notes",
		BackendRootKey:    "loom_box__notes",
		RootRelativePath:  "Notes",
		Status:            knowledge.SourceRootStatusActive,
		Totals:            knowledge.NotesOverviewTotals{RootCount: 1, ActiveRootCount: 1, ObjectCount: 3, FileCount: 2, DirectoryCount: 1, SizeBytes: 4096, SearchDocumentCount: 4},
		FileClasses: []knowledge.NotesFileClassCount{
			{FileClass: knowledge.NotesFileClassBucketMarkdown, Count: 2, SizeBytes: 3072},
			{FileClass: knowledge.NotesFileClassBucketDirectory, Count: 1},
		},
		ProcessingStates: []knowledge.NotesProcessingStateCount{{ProcessingState: knowledge.ProcessingStateChunked, Count: 2}},
		ExtractionStates: []knowledge.NotesExtractionStateCount{
			{ExtractionStatus: knowledge.ExtractionStatusExtracted, Count: 2},
			{ExtractionStatus: knowledge.ExtractionStatusMetadataOnly, Count: 1},
		},
		IndexHealth: knowledge.NotesIndexHealth{SearchDocumentCount: 4, Complete: 2, LastIndexedAt: &indexedAt, LastPipelineUpdateAt: &indexedAt},
	}
	projectRoot := knowledge.NotesRootOverview{
		NotesSourceRootID: "notes_root_project",
		RootKind:          knowledge.RootKindProjectNotes,
		NodeKey:           "main",
		ProjectID:         "project_osint",
		ProjectSlug:       "osint-tools",
		DisplayName:       "Project Notes",
		BackendRootKey:    "osint_tools__notes",
		RootRelativePath:  "Projects/osint-tools/notes",
		Status:            knowledge.SourceRootStatusActive,
		Totals:            knowledge.NotesOverviewTotals{RootCount: 1, ActiveRootCount: 1, ObjectCount: 2, FileCount: 2, SizeBytes: 2048, SearchDocumentCount: 2},
		FileClasses:       []knowledge.NotesFileClassCount{{FileClass: knowledge.NotesFileClassBucketPDF, Count: 1, SizeBytes: 2048}},
		ProcessingStates:  []knowledge.NotesProcessingStateCount{{ProcessingState: knowledge.ProcessingStateMetadataOnly, Count: 1}},
		ExtractionStates: []knowledge.NotesExtractionStateCount{
			{ExtractionStatus: knowledge.ExtractionStatusTooLarge, Count: 1},
		},
		IndexHealth: knowledge.NotesIndexHealth{SearchDocumentCount: 2, Complete: 1, Queued: 1, LastIndexedAt: &indexedAt, LastPipelineUpdateAt: &indexedAt},
	}
	archivedRoot := knowledge.NotesRootOverview{
		NotesSourceRootID: "notes_root_archived",
		RootKind:          knowledge.RootKindProjectNotes,
		NodeKey:           "main",
		ProjectSlug:       "archived-project",
		DisplayName:       "Archived Notes",
		Status:            knowledge.SourceRootStatusDisabled,
		Totals:            knowledge.NotesOverviewTotals{RootCount: 1, ObjectCount: 1, FileCount: 1, SizeBytes: 512},
		FileClasses:       []knowledge.NotesFileClassCount{{FileClass: knowledge.NotesFileClassBucketMarkdown, Count: 1, SizeBytes: 512}},
	}
	overview := knowledge.NotesOverview{
		GeneratedAt: now,
		Totals:      knowledge.NotesOverviewTotals{RootCount: 3, ActiveRootCount: 2, ObjectCount: 6, FileCount: 5, DirectoryCount: 1, SizeBytes: 6656, SearchDocumentCount: 6},
		Nodes: []knowledge.NotesNodeOverview{{
			NodeKey: "main",
			Totals:  knowledge.NotesOverviewTotals{RootCount: 3, ActiveRootCount: 2, ObjectCount: 6, FileCount: 5, DirectoryCount: 1, SizeBytes: 6656, SearchDocumentCount: 6},
			ExtractionStates: []knowledge.NotesExtractionStateCount{
				{ExtractionStatus: knowledge.ExtractionStatusExtracted, Count: 2},
				{ExtractionStatus: knowledge.ExtractionStatusMetadataOnly, Count: 1},
				{ExtractionStatus: knowledge.ExtractionStatusTooLarge, Count: 1},
			},
			Roots: []knowledge.NotesRootOverview{boxRoot, projectRoot, archivedRoot},
		}},
		ExtractionStates: []knowledge.NotesExtractionStateCount{
			{ExtractionStatus: knowledge.ExtractionStatusExtracted, Count: 2},
			{ExtractionStatus: knowledge.ExtractionStatusMetadataOnly, Count: 1},
			{ExtractionStatus: knowledge.ExtractionStatusTooLarge, Count: 1},
		},
		Projection:  knowledge.NotesProjectionOverview{ProjectionRoot: "/srv/loom/loom-notes", Exists: true, LastRebuildAt: &rebuildAt, Entries: 4, Materialized: 4, ReadOnly: true},
		IndexHealth: knowledge.NotesIndexHealth{SearchDocumentCount: 6, Complete: 3, Queued: 1, LastIndexedAt: &indexedAt, LastPipelineUpdateAt: &indexedAt},
	}
	return response.Success("corr_test", overview), nil
}

func (c *fakePortalClient) SearchKnowledgeNotes(_ context.Context, _ string, input knowledge.NotesSearchInput) (response.Envelope[knowledge.NotesSearchResultSet], error) {
	c.notesSearchInput = input
	if c.optionalErr != nil {
		return response.Envelope[knowledge.NotesSearchResultSet]{}, c.optionalErr
	}
	return response.Success("corr_test", fakeNotesSearchResultSet(input.Query).ResultSet), nil
}

func (c *fakePortalClient) GetKnowledgeNotesEmbeddingStatus(context.Context, string) (response.Envelope[knowledge.EmbeddingStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[knowledge.EmbeddingStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", c.currentNotesEmbeddingStatus()), nil
}

func (c *fakePortalClient) EnableKnowledgeNotesEmbeddings(context.Context, string) (response.Envelope[knowledge.SetEmbeddingsEnabledResult], error) {
	c.notesEmbeddingsEnable = true
	if c.runErr != nil {
		return response.Envelope[knowledge.SetEmbeddingsEnabledResult]{}, c.runErr
	}
	c.notesEmbeddingStatus = fakeNotesEmbeddingStatus(true)
	result := knowledge.SetEmbeddingsEnabledResult{Settings: c.notesEmbeddingStatus.Settings, Queued: c.notesEmbeddingStatus.Queue.Queued, Status: c.notesEmbeddingStatus}
	return response.Success("corr_test", result), nil
}

func (c *fakePortalClient) DisableKnowledgeNotesEmbeddings(context.Context, string) (response.Envelope[knowledge.SetEmbeddingsEnabledResult], error) {
	c.notesEmbeddingsDisable = true
	if c.runErr != nil {
		return response.Envelope[knowledge.SetEmbeddingsEnabledResult]{}, c.runErr
	}
	c.notesEmbeddingStatus = fakeNotesEmbeddingStatus(false)
	result := knowledge.SetEmbeddingsEnabledResult{Settings: c.notesEmbeddingStatus.Settings, Queued: c.notesEmbeddingStatus.Queue.Queued, Status: c.notesEmbeddingStatus}
	return response.Success("corr_test", result), nil
}

func (c *fakePortalClient) currentNotesEmbeddingStatus() knowledge.EmbeddingStatus {
	if c.notesEmbeddingStatus.Settings.RuntimeKey == "" && c.notesEmbeddingStatus.Settings.ModelKey == "" {
		return fakeNotesEmbeddingStatus(false)
	}
	return c.notesEmbeddingStatus
}

func fakeNotesEmbeddingStatus(enabled bool) knowledge.EmbeddingStatus {
	generatedAt := time.Date(2026, 7, 4, 12, 45, 0, 0, time.UTC)
	return knowledge.EmbeddingStatus{
		Settings: knowledge.EmbeddingSettings{
			EmbeddingSettingsID: knowledge.EmbeddingSettingsID,
			Enabled:             enabled,
			RuntimeKey:          knowledge.EmbeddingRuntimeOllama,
			ModelKey:            knowledge.EmbeddingModelMXBAIEmbedLarge,
			Dimensions:          knowledge.DefaultEmbeddingDimensions,
			DistanceMetric:      knowledge.EmbeddingDistanceCosine,
			OllamaURL:           "http://127.0.0.1:11434",
			QuietWindowSeconds:  knowledge.DefaultEmbeddingQuietWindowSec,
			GlobalConcurrency:   knowledge.DefaultEmbeddingConcurrency,
			HistoryPerLineage:   knowledge.DefaultEmbeddingHistoryPerLineage,
			CreatedAt:           generatedAt.Add(-2 * time.Hour),
			UpdatedAt:           generatedAt.Add(-10 * time.Minute),
		},
		Queue:           knowledge.EmbeddingQueueCounts{Queued: 3, Ready: 1, Processing: 0, Failed: 1},
		Objects:         knowledge.EmbeddingObjectCounts{Queued: 2, Processing: 0, Complete: 5, Failed: 1},
		ActiveVectors:   12,
		Historical:      4,
		ReusableVectors: 2,
		GeneratedAt:     generatedAt,
	}
}

func fakeNotesSearchResultSet(query string) NotesSearchData {
	indexedAt := time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC)
	createdAt := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	modifiedAt := time.Date(2026, 7, 3, 9, 30, 0, 0, time.UTC)
	observedAt := time.Date(2026, 7, 4, 11, 0, 0, 0, time.UTC)
	result := knowledge.NotesSearchResult{
		SearchDocumentID:         "search_document_note",
		KnowledgeObjectID:        "knowledge_object_note",
		KnowledgeObjectVersionID: "knowledge_object_version_note",
		KnowledgeChunkID:         "knowledge_chunk_note",
		NotesSourceRootID:        "notes_root_project",
		RootKind:                 knowledge.RootKindProjectNotes,
		SourceNodeKey:            "main",
		ProjectID:                "project_osint",
		RelativePath:             "Projects/osint-tools/notes/threat-intel.md",
		SourcePath:               "/srv/loom/projects/osint-tools/notes/threat-intel.md",
		Title:                    "Threat Intel Runbook",
		FileClass:                knowledge.NotesFileClassBucketMarkdown,
		TextSource:               knowledge.TextSourceEmbeddedText,
		ExtractionStatus:         knowledge.ExtractionStatusExtracted,
		ChunkIndex:               0,
		StructuralPath:           "summary",
		Snippet:                  "Threat intelligence workflow for osint-tools.",
		RankScore:                1,
		BM25Score:                2.4,
		FTSScore:                 0.8,
		BoostScore:               3.2,
		LexicalRank:              1,
		SemanticRank:             2,
		SemanticDistance:         0.18,
		SemanticScore:            0.82,
		FinalScore:               6.4,
		MatchReasons:             []string{"title", "body", "semantic"},
		SourceCreatedAt:          &createdAt,
		SourceModifiedAt:         &modifiedAt,
		RecencyAt:                modifiedAt,
		RecencyBasis:             knowledge.AbsoluteTimeBasisSourceFilesystemMtime,
		RecencyScore:             0.94,
		RecencyRank:              1,
		ObservedAt:               observedAt,
		IndexedAt:                indexedAt,
		Citation:                 knowledge.NotesSearchCitation{Label: "Projects/osint-tools/notes/threat-intel.md#summary", SourceRef: "knowledge_chunk_note"},
	}
	input := knowledge.NotesSearchInput{Query: query, Limit: 10}
	return NotesSearchData{
		Query: query,
		Input: input,
		ResultSet: knowledge.NotesSearchResultSet{
			Query:             query,
			Mode:              knowledge.NotesSearchModeHybrid,
			RequestedMode:     knowledge.NotesSearchModeHybrid,
			SemanticAvailable: true,
			ResultCount:       1,
			Results:           []knowledge.NotesSearchResult{result},
		},
		Status:   ScreenLoadLoaded,
		LoadedAt: indexedAt,
	}
}

func (c *fakePortalClient) ListIndexStatus(context.Context, string, search.StatusFilter) (response.Envelope[[]search.IndexStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]search.IndexStatus]{}, c.optionalErr
	}
	now := time.Now().UTC()
	return response.Success("corr_test", []search.IndexStatus{{IndexStatusID: "index_status_recent", Status: "indexed", ObjectID: "object_test", IndexType: "full_text", UpdatedAt: now}}), nil
}

func (c *fakePortalClient) ListIndexQueue(context.Context, string, search.IndexQueueFilter) (response.Envelope[[]search.IndexStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]search.IndexStatus]{}, c.optionalErr
	}
	now := time.Now().UTC()
	return response.Success("corr_test", []search.IndexStatus{{IndexStatusID: "index_queue_test", Status: "queued", ObjectID: "object_queue_test", IndexType: "full_text", UpdatedAt: now}}), nil
}

func (c *fakePortalClient) GetIndexQueueItem(context.Context, string, string) (response.Envelope[search.IndexStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[search.IndexStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", search.IndexStatus{IndexStatusID: "index_queue_test", Status: "queued", ObjectID: "object_queue_test", IndexType: "full_text"}), nil
}

func (c *fakePortalClient) ListIndexFailures(context.Context, string, search.StatusFilter) (response.Envelope[[]search.IndexStatus], error) {
	c.recordSnapshotCall("index_failures")
	if c.optionalErr != nil {
		return response.Envelope[[]search.IndexStatus]{}, c.optionalErr
	}
	now := time.Now().UTC()
	return response.Success("corr_test", []search.IndexStatus{{IndexStatusID: "index_status_test", Status: "failed", ObjectID: "object_test", IndexType: "full_text", LastErrorCode: "indexing_failed", UpdatedAt: now}}), nil
}

func (c *fakePortalClient) ExplainIndexObject(context.Context, string, search.IndexExplainInput) (response.Envelope[search.IndexExplainResult], error) {
	if c.indexExplainErr != nil {
		return response.Envelope[search.IndexExplainResult]{}, c.indexExplainErr
	}
	if c.optionalErr != nil {
		return response.Envelope[search.IndexExplainResult]{}, c.optionalErr
	}
	return response.Success("corr_test", search.IndexExplainResult{
		ObjectRef:       "object_test",
		ObjectID:        "object_test",
		ObjectVersionID: "object_version_test",
		QueueStatus:     "failed",
		Statuses:        []search.IndexStatus{{IndexStatusID: "index_status_test", Status: "failed", ObjectID: "object_test"}},
	}), nil
}

func (c *fakePortalClient) RetryIndexWork(_ context.Context, _ string, input search.IndexStatusRefInput) (response.Envelope[search.IndexStatus], error) {
	c.retryIndexID = input.IndexStatusID
	if c.optionalErr != nil {
		return response.Envelope[search.IndexStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", search.IndexStatus{IndexStatusID: input.IndexStatusID, Status: "queued", ObjectID: "object_test", AttemptCount: 1}), nil
}

func (c *fakePortalClient) RetryFailedIndexWork(_ context.Context, _ string, input search.IndexRetryFailedInput) (response.Envelope[search.IndexRetrySummary], error) {
	c.retryFailedInput = input
	if c.optionalErr != nil {
		return response.Envelope[search.IndexRetrySummary]{}, c.optionalErr
	}
	return response.Success("corr_test", search.IndexRetrySummary{
		Retried: 1,
		Skipped: 0,
		Items:   []search.IndexStatus{{IndexStatusID: "index_status_test", Status: "queued"}},
	}), nil
}

func (c *fakePortalClient) RebuildIndexObject(_ context.Context, _ string, input search.RebuildInput) (response.Envelope[search.IndexResult], error) {
	c.rebuildObjectRef = input.ObjectRef
	if c.optionalErr != nil {
		return response.Envelope[search.IndexResult]{}, c.optionalErr
	}
	return response.Success("corr_test", search.IndexResult{ObjectID: input.ObjectRef, ObjectVersionID: "object_version_test", Status: "queued", Statuses: []search.IndexStatus{{IndexStatusID: "index_status_rebuilt", Status: "queued"}}}), nil
}

func (c *fakePortalClient) ListObjects(context.Context, string, objects.ListFilter) (response.Envelope[[]objects.Object], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]objects.Object]{}, c.optionalErr
	}
	return response.Success("corr_test", []objects.Object{{
		ObjectID:   "object_test",
		ObjectType: "document",
		Name:       "Portal Test Object",
		StateClass: "canonical",
		Status:     "active",
		CreatedAt:  time.Now().UTC().Add(-time.Hour),
		UpdatedAt:  time.Now().UTC(),
	}, {
		ObjectID:   "object_acceptance",
		ObjectType: "document",
		Name:       ".loom-acceptance smoke object",
		StateClass: "test",
		Status:     "active",
		CreatedAt:  time.Now().UTC().Add(-2 * time.Hour),
		UpdatedAt:  time.Now().UTC().Add(-2 * time.Hour),
	}}), nil
}

func (c *fakePortalClient) GetObject(_ context.Context, _ string, ref string) (response.Envelope[objects.ObjectDetail], error) {
	c.objectInspectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[objects.ObjectDetail]{}, c.optionalErr
	}
	sourceNode := "node_workspace"
	sourcePath := "/workspace/portal-test.md"
	size := int64(42)
	mime := "text/markdown"
	hash := "sha256:test"
	version := objects.ObjectVersion{
		ObjectVersionID: "object_version_test",
		ObjectID:        firstNonEmpty(ref, "object_test"),
		VersionNumber:   1,
		ContentHash:     &hash,
		SourceNodeID:    &sourceNode,
		SourcePath:      &sourcePath,
		SizeBytes:       &size,
		MimeType:        &mime,
		Status:          "active",
		CreatedAt:       time.Now().UTC(),
	}
	return response.Success("corr_test", objects.ObjectDetail{
		Object: objects.Object{
			ObjectID:   firstNonEmpty(ref, "object_test"),
			ObjectType: "document",
			Name:       "Portal Test Object",
			StateClass: "canonical",
			Status:     "active",
			CreatedAt:  time.Now().UTC().Add(-time.Hour),
			UpdatedAt:  time.Now().UTC(),
		},
		LatestVersion: &version,
		File: &objects.FileMetadata{
			ObjectID:        firstNonEmpty(ref, "object_test"),
			LogicalName:     "Portal Test Object",
			MimeType:        &mime,
			SourceNodeID:    &sourceNode,
			SourcePath:      &sourcePath,
			LatestVersionID: &version.ObjectVersionID,
			TextExtractable: true,
			RawBackupPolicy: "normal",
			IndexPolicy:     "text_later",
			CreatedAt:       time.Now().UTC().Add(-time.Hour),
			UpdatedAt:       time.Now().UTC(),
		},
		Locations: []objects.ObjectLocation{{
			ObjectLocationID:    "object_location_test",
			ObjectID:            firstNonEmpty(ref, "object_test"),
			VersionID:           &version.ObjectVersionID,
			NodeID:              sourceNode,
			LocationType:        "workspace",
			PathOrURI:           sourcePath,
			IsCanonicalLocation: true,
			FreshnessState:      "fresh",
			CreatedAt:           time.Now().UTC(),
		}},
	}), nil
}

func (c *fakePortalClient) ListObjectVersions(_ context.Context, _ string, ref string) (response.Envelope[[]objects.ObjectVersion], error) {
	c.objectVersionsRef = ref
	if c.optionalErr != nil {
		return response.Envelope[[]objects.ObjectVersion]{}, c.optionalErr
	}
	hash := "sha256:test"
	return response.Success("corr_test", []objects.ObjectVersion{{
		ObjectVersionID: "object_version_test",
		ObjectID:        firstNonEmpty(ref, "object_test"),
		VersionNumber:   1,
		ContentHash:     &hash,
		Status:          "active",
		CreatedAt:       time.Now().UTC(),
	}}), nil
}

func (c *fakePortalClient) Search(_ context.Context, _ string, input search.SearchInput) (response.Envelope[search.SearchResultSet], error) {
	c.searchInput = input
	if c.optionalErr != nil {
		return response.Envelope[search.SearchResultSet]{}, c.optionalErr
	}
	result := search.SearchResult{
		SearchDocumentID: "search_document_test",
		ResultKind:       "object",
		ObjectID:         "object_test",
		ObjectVersionID:  "object_version_test",
		Title:            "Portal Test Object",
		Snippet:          "portal database search snippet",
		RankScore:        1,
		FreshnessState:   "fresh",
		IndexedAt:        time.Now().UTC(),
	}
	return response.Success("corr_test", search.SearchResultSet{
		Query:       input.Query,
		ResultCount: 1,
		Results:     []search.SearchResult{result},
	}), nil
}

func (c *fakePortalClient) ListProjects(context.Context, string, int) (response.Envelope[[]projects.Project], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]projects.Project]{}, c.optionalErr
	}
	if c.noProjects {
		return response.Success("corr_test", []projects.Project{}), nil
	}
	homeNode := "main"
	return response.Success("corr_test", []projects.Project{{
		ProjectID:       "project_portal",
		ProjectScopeKey: "project.portal-project",
		Slug:            "portal-project",
		Name:            "Portal Project",
		HomeNodeID:      &homeNode,
		Status:          "active",
		ProjectType:     "automation",
	}}), nil
}

func (c *fakePortalClient) GetProjectRepositoryStatus(_ context.Context, _ string, ref string, options localclient.ProjectRepositoryPageOptions) (response.Envelope[localclient.ProjectRepositoryStatusResult], error) {
	c.projectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[localclient.ProjectRepositoryStatusResult]{}, c.optionalErr
	}
	observedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return response.Success("corr_test", localclient.ProjectRepositoryStatusResult{
		SchemaVersion: projectstate.SchemaVersion,
		Project:       localclient.ProjectRepositoryProject{ProjectID: "project_portal", Slug: "portal-project", Lifecycle: "active"},
		Source: localclient.ProjectRepositorySource{
			Posture:                      projectstate.SourcePostureRegistered,
			ProjectContractSchemaVersion: "project.contract.v0.4",
			ReposContractSchemaVersion:   "repos.contract.v0.4",
			SourceRevision:               1,
		},
		Observation: projectstate.ObservationSummary{Posture: projects.ProjectRepositoryObservationObserved, MemberCount: 1, Observed: 1},
		Repositories: []localclient.ProjectRepositoryItem{{
			RepositoryID:             "repo_portal",
			RepositoryOwnerProjectID: "project_portal",
			Key:                      "backend",
			RelativePath:             "backend",
			Role:                     "primary",
			MembershipLifecycle:      "active",
			RepositoryLifecycle:      "active",
			ObservationPosture:       "observed",
			ObservedAt:               &observedAt,
			DevelopmentState:         localclient.ProjectRepositoryDevelopmentState{Posture: projectstate.DevelopmentStateNotEnabled},
			Git:                      &localclient.ProjectRepositoryGit{CurrentBranch: "main", Dirty: localclient.ProjectRepositoryGitDirty{}},
		}},
		Page:       localclient.ProjectRepositoryPage{Limit: options.Limit},
		ObservedAt: observedAt,
	}), nil
}

func (c *fakePortalClient) GetProject(_ context.Context, _ string, ref string) (response.Envelope[projects.ProjectDetail], error) {
	c.projectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[projects.ProjectDetail]{}, c.optionalErr
	}
	homeNode := "main"
	return response.Success("corr_test", projects.ProjectDetail{Project: projects.Project{
		ProjectID:       "project_portal",
		ProjectScopeKey: "project.portal-project",
		Slug:            "portal-project",
		Name:            "Portal Project",
		HomeNodeID:      &homeNode,
		Status:          "active",
		ProjectType:     "automation",
	}}), nil
}

func (c *fakePortalClient) ScaffoldProject(_ context.Context, _ string, input projectcontracts.ScaffoldOptions) (response.Envelope[projectcontracts.ScaffoldResult], error) {
	c.scaffoldProjectInput = input
	if c.optionalErr != nil {
		return response.Envelope[projectcontracts.ScaffoldResult]{}, c.optionalErr
	}
	slug := input.Slug
	if slug == "" {
		slug = projectcontracts.DeriveProjectSlug(input.Name)
	}
	preset := input.Preset
	if preset == "" {
		preset = projectcontracts.PresetMinimal
	}
	ownerNode := input.OwnerNode
	if ownerNode == "" {
		ownerNode = "main"
	}
	facets := append([]string{}, input.Facets...)
	if len(facets) == 0 {
		facets = []string{"notes", "backup_policy", "portal"}
	}
	return response.Success("corr_test", projectcontracts.ScaffoldResult{
		OK:           true,
		ProjectRoot:  "/home/loomadmin/loom-box/Projects/" + slug,
		ParentDir:    "/home/loomadmin/loom-box/Projects",
		DirSource:    projectcontracts.ScaffoldDirectoryBox,
		BoxDefault:   true,
		BoxRoot:      "/home/loomadmin/loom-box",
		BoxProfile:   box.ProfileMain,
		ContractPath: "/home/loomadmin/loom-box/Projects/" + slug + "/" + projectcontracts.CanonicalRootContractPath,
		Name:         input.Name,
		Slug:         slug,
		OwnerNode:    ownerNode,
		Preset:       preset,
		Facets:       facets,
		Files:        []projectcontracts.ScaffoldFileResult{{Path: projectcontracts.CanonicalRootContractPath, Kind: "root_contract", Action: "created"}},
		Validation:   projectcontracts.ScaffoldValidationSummary{State: projectcontracts.ScaffoldValidationPassed, OK: true},
	}), nil
}

func (c *fakePortalClient) AddProjectFacets(_ context.Context, _ string, input projectcontracts.AddProjectFacetsOptions) (response.Envelope[projectcontracts.AddProjectFacetsResult], error) {
	c.addProjectFacetsInput = input
	if c.optionalErr != nil {
		return response.Envelope[projectcontracts.AddProjectFacetsResult]{}, c.optionalErr
	}
	facets := append([]string{}, input.Facets...)
	fileAction := "updated"
	validation := projectcontracts.ScaffoldValidationSummary{State: projectcontracts.ScaffoldValidationPassed, OK: true}
	if input.DryRun {
		fileAction = "planned_update"
		validation = projectcontracts.ScaffoldValidationSummary{State: projectcontracts.ScaffoldValidationPlannedOnly}
	}
	return response.Success("corr_test", projectcontracts.AddProjectFacetsResult{
		OK:              true,
		DryRun:          input.DryRun,
		ProjectRoot:     firstNonEmpty(input.ProjectRoot, "/home/loomadmin/loom-box/Projects/"+input.ProjectRef),
		ContractPath:    firstNonEmpty(input.ProjectRoot, "/home/loomadmin/loom-box/Projects/"+input.ProjectRef) + "/" + projectcontracts.CanonicalRootContractPath,
		Slug:            firstNonEmpty(input.ProjectRef, "portal-project"),
		Name:            "Portal Project",
		OwnerNode:       "main",
		RequestedFacets: facets,
		AddedFacets:     []string{"notes"},
		ExistingFacets:  []string{"scripts"},
		Facets:          []string{"notes", "scripts"},
		Files:           []projectcontracts.ScaffoldFileResult{{Path: projectcontracts.CanonicalRootContractPath, Kind: "root_contract", Action: fileAction}},
		Validation:      validation,
	}), nil
}

func (c *fakePortalClient) MigrateProjectLayout(_ context.Context, _ string, input projectcontracts.LayoutMigrationOptions) (response.Envelope[projectcontracts.LayoutMigrationResult], error) {
	c.layoutMigrationInput = input
	if c.optionalErr != nil {
		return response.Envelope[projectcontracts.LayoutMigrationResult]{}, c.optionalErr
	}
	actionStatus := "planned"
	if input.Apply {
		actionStatus = "applied"
	}
	result := projectcontracts.LayoutMigrationResult{
		OK:           true,
		DryRun:       !input.Apply,
		Applied:      input.Apply,
		ProjectRoot:  firstNonEmpty(input.ProjectRoot, "/home/loomadmin/loom-box/Projects/"+input.ProjectRef),
		BeforeLayout: projectcontracts.ProjectLayoutLegacy,
		Actions: []projectcontracts.LayoutMigrationAction{{
			Order:       1,
			Operation:   "move_contract",
			Kind:        "root_contract",
			Source:      projectcontracts.LegacyRootContractPath,
			Destination: projectcontracts.CanonicalRootContractPath,
			Status:      actionStatus,
		}},
		NextActions: []string{"loom project validate " + input.ProjectRef + " --backend", "loom project register " + input.ProjectRef + " --backend"},
	}
	if input.Apply {
		result.AfterLayout = projectcontracts.ProjectLayoutCanonical
		result.RecordPath = ".loom/state/layout-migrations/test.json"
	}
	return response.Success("corr_layout", result), nil
}

func (c *fakePortalClient) AnalyzeProjectContractBackend(_ context.Context, _ string, input projectcontracts.BackendAnalysisInput) (response.Envelope[projectdoctor.BackendAnalysisResult], error) {
	c.backendAnalysisInput = input
	if c.optionalErr != nil {
		return response.Envelope[projectdoctor.BackendAnalysisResult]{}, c.optionalErr
	}
	if c.backendAnalysisOverride != nil {
		return response.Success("corr_test", *c.backendAnalysisOverride), nil
	}
	ref := firstNonEmpty(input.ProjectRef, "portal-project")
	root := firstNonEmpty(input.ProjectRoot, "/home/loomadmin/loom-box/Projects/"+ref)
	raw := []byte("kind: loom.project\nproject:\n  slug: " + ref + "\n")
	analysis := projectcontracts.Analysis{
		Loaded: &projectcontracts.LoadedProject{
			RootPath:     root,
			ContractPath: root + "/" + projectcontracts.CanonicalRootContractPath,
			Layout:       projectcontracts.ProjectLayoutCanonical,
			Raw:          raw,
		},
		Report: projectcontracts.ValidationReport{
			SchemaVersion: projectcontracts.ReportSchemaV03,
			OK:            true,
			Registerable:  true,
			ProjectRoot:   root,
			Project:       projectcontracts.PlanProject{Slug: ref, Name: "Portal Project", OwnerNode: "main", Status: "active"},
			Summary:       projectcontracts.DiagnosticSummary{},
		},
		Plan: projectcontracts.ProjectPlan{
			SchemaVersion: projectcontracts.PlanSchemaV03,
			Registerable:  true,
			Project:       projectcontracts.PlanProject{Slug: ref, Name: "Portal Project", OwnerNode: "main", Status: "active"},
			Facets:        []projectcontracts.PlanFacet{{Key: "scripts", Enabled: true, Present: true}},
			Summary:       projectcontracts.DiagnosticSummary{},
		},
	}
	detail := fakeProjectRegistrationDetail()
	if input.ProjectRef != "" {
		detail.Project.Project.Slug = input.ProjectRef
	}
	if input.ProjectRoot != "" && detail.Registration != nil {
		detail.Registration.ProjectRoot = input.ProjectRoot
	}
	if detail.Registration != nil {
		hash := sha256.Sum256(raw)
		detail.Registration.ContractHash = fmt.Sprintf("sha256:%x", hash[:])
	}
	result := projectdoctor.BuildBackendAnalysisResult(analysis, &detail)
	result.DriftStatus = projectdoctor.BackendDriftCurrent
	result.Current = true
	result.NextAction = "no registration needed"
	result.Diff = projectdoctor.DiffReport{ProjectRef: ref, LocalRoot: root, Summary: projectdoctor.DiffSummary{Unchanged: 1}}
	result.Report = projectdoctor.Report{ProjectRef: ref, Source: "project.backend_analysis", Summary: projectdoctor.Summary{OK: 1}}
	return response.Success("corr_test", result), nil
}

func (c *fakePortalClient) RegisterProjectContractFromBackend(_ context.Context, _ string, input projects.RegisterProjectContractFromBackendInput) (response.Envelope[projects.RegisterProjectContractResult], error) {
	c.registerBackendInput = input
	if c.optionalErr != nil {
		return response.Envelope[projects.RegisterProjectContractResult]{}, c.optionalErr
	}
	detail := fakeProjectRegistrationDetail()
	if input.ProjectRef != "" {
		detail.Project.Project.Slug = input.ProjectRef
	}
	if input.ProjectRoot != "" && detail.Registration != nil {
		detail.Registration.ProjectRoot = input.ProjectRoot
	}
	return response.Success("corr_test", projects.RegisterProjectContractResult{
		Detail:    detail,
		Updated:   true,
		EventIDs:  []string{"event_project_registered"},
		Unchanged: false,
	}), nil
}

func (c *fakePortalClient) GetProjectRegistrationStatus(_ context.Context, _ string, ref string) (response.Envelope[projects.ProjectRegistrationDetail], error) {
	c.projectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[projects.ProjectRegistrationDetail]{}, c.optionalErr
	}
	return response.Success("corr_test", fakeProjectRegistrationDetail()), nil
}

func (c *fakePortalClient) ActivateProject(_ context.Context, _ string, ref string, input projects.ActivateProjectInput) (response.Envelope[projects.ProjectRegistrationDetail], error) {
	c.projectRef = ref
	c.activateProject = input
	if c.optionalErr != nil {
		return response.Envelope[projects.ProjectRegistrationDetail]{}, c.optionalErr
	}
	return response.Success("corr_test", fakeProjectRegistrationDetail()), nil
}

func (c *fakePortalClient) DeactivateProject(_ context.Context, _ string, ref string, input projects.DeactivateProjectInput) (response.Envelope[projects.ProjectDeactivationResult], error) {
	c.projectRef = ref
	c.deactivateProject = input
	if c.optionalErr != nil {
		return response.Envelope[projects.ProjectDeactivationResult]{}, c.optionalErr
	}
	return response.Success("corr_test", projects.ProjectDeactivationResult{
		Detail:  fakeProjectRegistrationDetail(),
		Facet:   input.Facet,
		Changed: true,
		Actions: []projects.ProjectDeactivationAction{{Key: input.Facet, Kind: "project_facet", Status: "disabled", Summary: "disabled"}},
	}), nil
}

func (c *fakePortalClient) ArchiveProject(_ context.Context, _ string, ref string, input storagearchive.ProjectArchiveInput) (response.Envelope[storagearchive.ProjectArchiveResult], error) {
	c.projectArchiveRef = ref
	c.projectArchiveInput = input
	if c.optionalErr != nil {
		return response.Envelope[storagearchive.ProjectArchiveResult]{}, c.optionalErr
	}
	return response.Success("corr_test", storagearchive.ProjectArchiveResult{
		Project: fakeProjectRegistrationDetail(),
		RuntimeManifest: storagearchive.ProjectRuntimeArchiveManifest{
			ProjectRuntimeArchiveID: "project_runtime_archive_portal",
			ProjectSlug:             ref,
			SourceRef:               firstNonEmpty(input.SourceRef, "/home/loomadmin/loom-box/Projects/"+ref),
			TargetPath:              firstNonEmpty(input.TargetPath, "main/Archive/Projects/"+ref),
			SuccessorPolicy:         storagearchive.ProjectRuntimeSuccessorPolicy{Status: "none"},
		},
		RuntimeManifestPath:   "/home/loomadmin/loom-box/Archive/Projects/" + ref + "/runtime.json",
		DryRun:                input.DryRun,
		StorageArchiveSkipped: input.SkipStorageArchive,
		SafeToDelete:          input.SkipStorageArchive,
	}), nil
}

func (c *fakePortalClient) InspectProjectArchive(_ context.Context, _ string, ref string) (response.Envelope[storagearchive.ProjectArchiveInspectResult], error) {
	c.projectArchiveInspectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[storagearchive.ProjectArchiveInspectResult]{}, c.optionalErr
	}
	return response.Success("corr_test", storagearchive.ProjectArchiveInspectResult{
		Project:             fakeProjectRegistrationDetail(),
		ArchiveState:        fakeProjectArchiveState(ref),
		RuntimeManifestPath: "/home/loomadmin/loom-box/Archive/Projects/" + ref + "/runtime.json",
		RuntimeManifest: &storagearchive.ProjectRuntimeArchiveManifest{
			ProjectRuntimeArchiveID: "project_runtime_archive_portal",
			ProjectSlug:             ref,
			SourceRef:               "/home/loomadmin/loom-box/Projects/" + ref,
			TargetPath:              "main/Archive/Projects/" + ref,
			SuccessorPolicy:         storagearchive.ProjectRuntimeSuccessorPolicy{Status: "not_migrated"},
			Scripts:                 []storagearchive.ProjectRuntimeSurface{{Key: "main", Status: "disabled"}},
			WatchedRoots:            []storagearchive.ProjectRuntimeWatchedRoot{{Key: "project", Status: "disabled"}},
		},
	}), nil
}

func (c *fakePortalClient) PlanProjectArchiveRestore(_ context.Context, _ string, ref string, input storagearchive.ProjectArchiveRestoreInput) (response.Envelope[storagearchive.ProjectArchiveRestorePlan], error) {
	c.projectArchiveRestoreRef = ref
	c.projectArchiveRestoreInput = input
	if c.optionalErr != nil {
		return response.Envelope[storagearchive.ProjectArchiveRestorePlan]{}, c.optionalErr
	}
	return response.Success("corr_test", storagearchive.ProjectArchiveRestorePlan{
		Project:      fakeProjectRegistrationDetail(),
		ArchiveState: fakeProjectArchiveState(ref),
		Steps: []storagearchive.ProjectArchivePlanStep{
			{Key: "storage", Kind: "restore", Status: "would_restore", Summary: "Restore files."},
			{Key: "runtime", Kind: "restore", Status: "not_migrated", Summary: "Runtime migration is separate."},
		},
		DryRun: true,
	}), nil
}

func (c *fakePortalClient) BuildProjectWatchPlan(_ context.Context, _ string, ref string, input projectwatch.BuildPlanInput) (response.Envelope[projectwatch.ProjectWatchPlan], error) {
	c.projectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[projectwatch.ProjectWatchPlan]{}, c.optionalErr
	}
	return response.Success("corr_test", projectwatch.ProjectWatchPlan{
		Detail:       ptrProjectRegistrationDetail(fakeProjectRegistrationDetail()),
		ProjectRoot:  "/tmp/portal-project",
		WatchedRoots: []projectcontracts.ProjectWatchedRootItem{{Key: "notes", DisplayName: "Notes", SyncMode: "sync", BackupMode: "backup"}},
		Report:       projectcontracts.ValidationReport{OK: true, Summary: projectcontracts.DiagnosticSummary{}},
	}), nil
}

func (c *fakePortalClient) GetProjectSyncStatus(_ context.Context, _ string, ref string) (response.Envelope[projectwatch.ProjectSyncStatus], error) {
	c.projectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[projectwatch.ProjectSyncStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", projectwatch.ProjectSyncStatus{ProjectRef: ref, SyncRoots: 1, Reported: 1}), nil
}

func (c *fakePortalClient) GetProjectBackupStatus(_ context.Context, _ string, ref string) (response.Envelope[projectwatch.ProjectBackupStatus], error) {
	c.projectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[projectwatch.ProjectBackupStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", projectwatch.ProjectBackupStatus{ProjectRef: ref, BackupRoots: 1, Reported: 1}), nil
}

func (c *fakePortalClient) ListNodes(context.Context, string, int) (response.Envelope[[]nodes.Node], error) {
	c.recordSnapshotCall("nodes")
	if c.optionalErr != nil {
		return response.Envelope[[]nodes.Node]{}, c.optionalErr
	}
	return response.Success("corr_test", []nodes.Node{{NodeID: "node_main", NodeKey: "main", Status: "active"}}), nil
}

func (c *fakePortalClient) GetNode(_ context.Context, _ string, ref string) (response.Envelope[nodes.Node], error) {
	c.nodeInspectRef = ref
	if c.optionalErr != nil {
		return response.Envelope[nodes.Node]{}, c.optionalErr
	}
	version := "test"
	return response.Success("corr_test", nodes.Node{NodeID: firstNonEmpty(ref, "node_main"), NodeKey: "main", DisplayName: "Main", NodeKind: "main", NodeRole: "main", RuntimeClass: "server", Status: "active", PresenceState: "online", RuntimeVersion: &version}), nil
}

func (c *fakePortalClient) GetNodeHealth(_ context.Context, _ string, ref string) (response.Envelope[nodes.NodeHealth], error) {
	c.nodeHealthRef = ref
	if c.optionalErr != nil {
		return response.Envelope[nodes.NodeHealth]{}, c.optionalErr
	}
	age := int64(3)
	return response.Success("corr_test", nodes.NodeHealth{
		Node:                nodes.Node{NodeID: firstNonEmpty(ref, "node_main"), NodeKey: "main", Status: "active", PresenceState: "online"},
		HeartbeatAgeSeconds: &age,
		PendingMessages:     1,
	}), nil
}

func (c *fakePortalClient) ListProtectedFolders(context.Context, string, backupcontracts.ProtectedFolderFilter) (response.Envelope[backupcontracts.ProtectedFolderListResult], error) {
	c.recordSnapshotCall("protected_folders")
	if c.protectedFolderErr != nil {
		return response.Envelope[backupcontracts.ProtectedFolderListResult]{}, c.protectedFolderErr
	}
	return response.Success("corr_test", c.protectedFolders), nil
}

func (c *fakePortalClient) GetProtectedFolder(_ context.Context, _ string, key string) (response.Envelope[backupcontracts.ProtectedFolderRecord], error) {
	c.protectedAction, c.protectedActionKey = "inspect", key
	if c.protectedFolderErr != nil {
		return response.Envelope[backupcontracts.ProtectedFolderRecord]{}, c.protectedFolderErr
	}
	for _, record := range c.protectedFolders.Folders {
		if record.Key == key {
			return response.Success("corr_test", record), nil
		}
	}
	return response.Success("corr_test", backupcontracts.ProtectedFolderRecord{Key: key, Lifecycle: backupcontracts.ProtectedFolderStatusActivating}), nil
}

func (c *fakePortalClient) CreateBackupContractPreflight(_ context.Context, _ string, input backupcontracts.PreflightCreateRequest) (response.Envelope[backupcontracts.PreflightRecord], error) {
	c.protectedPreflightInput = input
	if c.protectedFolderErr != nil {
		return response.Envelope[backupcontracts.PreflightRecord]{}, c.protectedFolderErr
	}
	record := c.protectedPreflight
	if record.PreflightID == "" {
		record = backupcontracts.PreflightRecord{PreflightID: "bpf_test", TargetNodeID: input.NodeRef, RequestedPath: input.Path, Status: backupcontracts.PreflightStatusPending}
	}
	c.protectedPreflight = record
	return response.Success("corr_test", record), nil
}

func (c *fakePortalClient) GetBackupContractPreflight(context.Context, string, string) (response.Envelope[backupcontracts.PreflightRecord], error) {
	if c.protectedFolderErr != nil {
		return response.Envelope[backupcontracts.PreflightRecord]{}, c.protectedFolderErr
	}
	return response.Success("corr_test", c.protectedPreflight), nil
}

func (c *fakePortalClient) CreateBackupContract(_ context.Context, _ string, input backupcontracts.CreateRequest) (response.Envelope[backupcontracts.LifecycleResult], error) {
	c.protectedCreateInput = input
	if c.protectedFolderErr != nil {
		return response.Envelope[backupcontracts.LifecycleResult]{}, c.protectedFolderErr
	}
	contract := backupcontracts.ContractFromCreateRequest(input, input.OwnerNode)
	contract.Key, contract.Status = input.Key, backupcontracts.StatusActive
	return response.Success("corr_test", backupcontracts.LifecycleResult{Action: "create", Contract: contract, DesiredStateRegistered: true, NodeApplyQueued: true}), nil
}

func (c *fakePortalClient) EnableBackupContract(_ context.Context, _ string, key string, _ backupcontracts.EnableRequest) (response.Envelope[backupcontracts.LifecycleResult], error) {
	return c.fakeProtectedLifecycle("enable", key)
}

func (c *fakePortalClient) DisableBackupContract(_ context.Context, _ string, key string, _ backupcontracts.DisableRequest) (response.Envelope[backupcontracts.LifecycleResult], error) {
	return c.fakeProtectedLifecycle("disable", key)
}

func (c *fakePortalClient) DeleteBackupContract(_ context.Context, _ string, key string, _ backupcontracts.DeleteRequest) (response.Envelope[backupcontracts.LifecycleResult], error) {
	envelope, err := c.fakeProtectedLifecycle("delete", key)
	if err == nil {
		folders := c.protectedFolders.Folders[:0]
		for _, record := range c.protectedFolders.Folders {
			if record.Key != key {
				folders = append(folders, record)
			}
		}
		c.protectedFolders.Folders = folders
		delete(c.protectedFolders.Counts, backupcontracts.ProtectedFolderStatusDisabled)
	}
	return envelope, err
}

func (c *fakePortalClient) fakeProtectedLifecycle(action, key string) (response.Envelope[backupcontracts.LifecycleResult], error) {
	c.protectedAction, c.protectedActionKey = action, key
	if c.protectedFolderErr != nil {
		return response.Envelope[backupcontracts.LifecycleResult]{}, c.protectedFolderErr
	}
	return response.Success("corr_test", backupcontracts.LifecycleResult{Action: action, Contract: backupcontracts.Contract{Key: key}, DesiredStateRegistered: action != "delete", NodeApplyQueued: true}), nil
}

func (c *fakePortalClient) RetryBackupContractActivation(_ context.Context, _ string, key string, _ backupcontracts.RetryActivationRequest) (response.Envelope[backupcontracts.ReconcileQueueResult], error) {
	c.protectedAction, c.protectedActionKey = "retry", key
	if c.protectedFolderErr != nil {
		return response.Envelope[backupcontracts.ReconcileQueueResult]{}, c.protectedFolderErr
	}
	return response.Success("corr_test", backupcontracts.ReconcileQueueResult{NodeID: "node_workspace", DesiredRevision: 2, DesiredHash: "sha256:test"}), nil
}

func (c *fakePortalClient) RecheckBackupContract(_ context.Context, _ string, key string) (response.Envelope[backupcontracts.PreflightRecord], error) {
	c.protectedAction, c.protectedActionKey = "recheck", key
	if c.protectedFolderErr != nil {
		return response.Envelope[backupcontracts.PreflightRecord]{}, c.protectedFolderErr
	}
	record := c.protectedPreflight
	if record.PreflightID == "" {
		record = backupcontracts.PreflightRecord{PreflightID: "bpf_recheck", Status: backupcontracts.PreflightStatusPending}
	}
	return response.Success("corr_test", record), nil
}

func (c *fakePortalClient) ListWatchedRootStatus(context.Context, string, mainwatchedroots.StatusFilter) (response.Envelope[[]mainwatchedroots.RootStatus], error) {
	c.recordSnapshotCall("watched_roots")
	if c.optionalErr != nil {
		return response.Envelope[[]mainwatchedroots.RootStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", []mainwatchedroots.RootStatus{{
		Root: mainwatchedroots.WatchedRoot{NodeID: "node_workspace", RootKey: "vault", Status: mainwatchedroots.StatusHealthy},
	}}), nil
}

func (c *fakePortalClient) GetWatchedRootBackupStatus(context.Context, string, mainwatchedroots.BackupFilter) (response.Envelope[mainwatchedroots.BackupStatus], error) {
	if c.optionalErr != nil {
		return response.Envelope[mainwatchedroots.BackupStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", mainwatchedroots.BackupStatus{
		Root:   mainwatchedroots.WatchedRoot{NodeID: "node_workspace", RootKey: "vault", Status: mainwatchedroots.StatusHealthy},
		Status: mainwatchedroots.StatusHealthy,
	}), nil
}

func (c *fakePortalClient) ListWatchedRootBackupBatches(context.Context, string, mainwatchedroots.BackupFilter) (response.Envelope[[]mainwatchedroots.BackupBatch], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]mainwatchedroots.BackupBatch]{}, c.optionalErr
	}
	return response.Success("corr_test", []mainwatchedroots.BackupBatch{{WatchedRootBackupBatchID: "backup_batch_test", NodeID: "node_workspace", RootKey: "vault", Status: mainwatchedroots.BackupBatchStatusAccepted}}), nil
}

func (c *fakePortalClient) ListWatchedRootBackupItems(context.Context, string, mainwatchedroots.BackupItemFilter) (response.Envelope[[]mainwatchedroots.BackupItem], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]mainwatchedroots.BackupItem]{}, c.optionalErr
	}
	return response.Success("corr_test", []mainwatchedroots.BackupItem{{WatchedRootBackupItemID: "backup_item_test", NodeID: "node_workspace", RootKey: "vault", Status: mainwatchedroots.BackupItemStatusAccepted}}), nil
}

func (c *fakePortalClient) GetSyncStatus(_ context.Context, _ string, filter loomsync.ListFilter) (response.Envelope[loomsync.SyncStatus], error) {
	c.syncStatusFilter = filter
	if c.optionalErr != nil {
		return response.Envelope[loomsync.SyncStatus]{}, c.optionalErr
	}
	return response.Success("corr_test", loomsync.SyncStatus{
		Node:    nodes.Node{NodeID: "node_workspace", NodeKey: "workspace"},
		Summary: loomsync.SyncSummary{RecentBatchCount: 1, OpenConflictCount: 1, ReplicaCount: 1},
	}), nil
}

func (c *fakePortalClient) ListSyncBatches(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.SyncBatch], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]loomsync.SyncBatch]{}, c.optionalErr
	}
	return response.Success("corr_test", []loomsync.SyncBatch{{SyncBatchID: "sync_batch_test", OriginNodeID: "node_workspace", Status: "accepted"}}), nil
}

func (c *fakePortalClient) ListSyncConflicts(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.SyncConflict], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]loomsync.SyncConflict]{}, c.optionalErr
	}
	return response.Success("corr_test", []loomsync.SyncConflict{{SyncConflictID: "sync_conflict_test", OriginNodeID: "node_workspace", Status: "open", Summary: "conflict"}}), nil
}

func (c *fakePortalClient) ListSyncReplicas(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.SyncReplica], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]loomsync.SyncReplica]{}, c.optionalErr
	}
	return response.Success("corr_test", []loomsync.SyncReplica{{ReplicaID: "sync_replica_test", SourceNodeID: "node_workspace", FreshnessState: "fresh"}}), nil
}

func (c *fakePortalClient) ListPrivateBackups(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.PrivateBackupOperation], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]loomsync.PrivateBackupOperation]{}, c.optionalErr
	}
	return response.Success("corr_test", []loomsync.PrivateBackupOperation{{PrivateBackupOperationID: "private_backup_test", OriginNodeID: "node_workspace", Status: "completed"}}), nil
}

func (c *fakePortalClient) ListDeletionRequests(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.DeletionRequest], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]loomsync.DeletionRequest]{}, c.optionalErr
	}
	return response.Success("corr_test", []loomsync.DeletionRequest{{DeletionRequestID: "deletion_request_test", OriginNodeID: "node_workspace", TargetKind: "object", TargetRef: "object_test", Status: loomsync.DeletionRequestStatusPendingReview}}), nil
}

func (c *fakePortalClient) GetDeletionRequest(_ context.Context, _ string, ref string) (response.Envelope[loomsync.DeletionRequest], error) {
	c.deletionRequestRef = ref
	if c.optionalErr != nil {
		return response.Envelope[loomsync.DeletionRequest]{}, c.optionalErr
	}
	return response.Success("corr_test", fakeDeletionRequestWithStatus(ref, loomsync.DeletionRequestStatusPendingReview)), nil
}

func (c *fakePortalClient) ReviewDeletionRequest(_ context.Context, _ string, input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
	return c.updateDeletionRequest(input, loomsync.DeletionRequestStatusPendingReview, "review")
}

func (c *fakePortalClient) ApproveDeletionRequest(_ context.Context, _ string, input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
	return c.updateDeletionRequest(input, loomsync.DeletionRequestStatusApproved, "approve")
}

func (c *fakePortalClient) DenyDeletionRequest(_ context.Context, _ string, input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
	return c.updateDeletionRequest(input, loomsync.DeletionRequestStatusDenied, "deny")
}

func (c *fakePortalClient) CompleteDeletionRequest(_ context.Context, _ string, input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
	return c.updateDeletionRequest(input, loomsync.DeletionRequestStatusCompleted, "complete")
}

func (c *fakePortalClient) updateDeletionRequest(input loomsync.DeletionRequestUpdateInput, status string, action string) (response.Envelope[loomsync.DeletionRequest], error) {
	c.deletionRequestRef = input.RequestRef
	c.deletionRequestAction = action
	if c.optionalErr != nil {
		return response.Envelope[loomsync.DeletionRequest]{}, c.optionalErr
	}
	return response.Success("corr_test", fakeDeletionRequestWithStatus(input.RequestRef, status)), nil
}

func fakeDeletionRequestWithStatus(ref string, status string) loomsync.DeletionRequest {
	return loomsync.DeletionRequest{
		DeletionRequestID: firstNonEmpty(ref, "deletion_request_test"),
		OriginNodeID:      "node_workspace",
		TargetKind:        "object",
		TargetRef:         "object_test",
		RequestedAction:   "tombstone",
		Status:            status,
		Reason:            "test request",
		RequestedAt:       time.Now().UTC().Add(-time.Hour),
	}
}

func (c *fakePortalClient) RunWorkerOnce(ctx context.Context, correlationID, ref string, input workers.RunOnceInput) (response.Envelope[workers.RunOnceResult], error) {
	c.runWorkerRef = ref
	c.runInput = input
	if c.runErr != nil {
		return response.Envelope[workers.RunOnceResult]{}, c.runErr
	}
	return response.SuccessWithIdempotency(correlationID, input.IdempotencyKey, workers.RunOnceResult{
		Worker: workers.WorkerDetail{
			Instance: workers.WorkerInstance{
				WorkerKey:  ref,
				WorkerKind: workers.KindSelfcheck,
			},
		},
		Run: workers.WorkerRun{
			WorkerRunID:    "worker_run_test",
			WorkerKind:     workers.KindSelfcheck,
			RunStatus:      workers.RunStatusSucceeded,
			CorrelationID:  correlationID,
			IdempotencyKey: input.IdempotencyKey,
		},
	}), nil
}

func (c *fakePortalClient) ListWatchedRootFindings(context.Context, string, mainwatchedroots.FindingFilter) (response.Envelope[[]mainwatchedroots.Finding], error) {
	c.recordSnapshotCall("watched_root_findings")
	if c.optionalErr != nil {
		return response.Envelope[[]mainwatchedroots.Finding]{}, c.optionalErr
	}
	return response.Success("corr_test", []mainwatchedroots.Finding{{
		NodeID:  "node_workspace",
		RootKey: "vault",
		Status:  mainwatchedroots.FindingStatusOpen,
		Summary: "scan stale",
	}}), nil
}

func (c *fakePortalClient) ListProviders(context.Context, string, capabilities.ProviderFilter) (response.Envelope[[]capabilities.ProviderListItem], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]capabilities.ProviderListItem]{}, c.optionalErr
	}
	return response.Success("corr_test", []capabilities.ProviderListItem{{
		Provider: capabilities.Provider{
			ProviderID:     "provider_test",
			ProviderKey:    "system",
			CompactAddress: "main@system",
			DisplayName:    "System",
			ProviderType:   capabilities.ProviderTypeSystem,
			Status:         "active",
		},
		HealthStatus:       "healthy",
		AvailabilityStatus: "available",
	}}), nil
}

func (c *fakePortalClient) ListServices(_ context.Context, _ string, filter serviceregistry.ServiceFilter) (response.Envelope[[]serviceregistry.ServiceListItem], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]serviceregistry.ServiceListItem]{}, c.optionalErr
	}
	item := serviceregistry.ServiceListItem{
		ProviderID: "provider_service_test", ProviderKey: "portal-service", ProviderAddress: "main@portal-service", DisplayName: "Portal Service",
		NodeID: "main", ScopeID: "scope_portal", RegistryState: serviceregistry.ProviderStateActive, ProcessState: serviceregistry.ProcessStateRunning,
		HealthStatus: "ok", AvailabilityStatus: "available", Provisioning: "external",
	}
	if filter.ProjectRef != "" && filter.ProjectRef != "project_portal" && filter.ProjectRef != "portal-project" {
		return response.Success("corr_test", []serviceregistry.ServiceListItem{}), nil
	}
	return response.Success("corr_test", []serviceregistry.ServiceListItem{item}), nil
}

func (c *fakePortalClient) GetService(_ context.Context, _ string, ref string) (response.Envelope[serviceregistry.ServiceInspection], error) {
	c.serviceRef = ref
	if c.optionalErr != nil {
		return response.Envelope[serviceregistry.ServiceInspection]{}, c.optionalErr
	}
	item := serviceregistry.ServiceListItem{
		ProviderID: "provider_service_test", ProviderKey: "portal-service", ProviderAddress: "main@portal-service", DisplayName: "Portal Service",
		NodeID: "main", ScopeID: "scope_portal", RegistryState: serviceregistry.ProviderStateActive, ProcessState: serviceregistry.ProcessStateRunning,
		HealthStatus: "ok", AvailabilityStatus: "available", Provisioning: "external",
	}
	return response.Success("corr_test", serviceregistry.ServiceInspection{
		ServiceListItem: item,
		RuntimeProfile:  serviceregistry.RuntimeProfile{SchemaVersion: "loom.service_runtime.v1", Manager: serviceregistry.ManagerLaunchd, Unit: "local.loom.portal-service", ServiceClass: serviceregistry.ServiceClassProject, Operations: serviceregistry.StandardOperations(), References: serviceregistry.RuntimeReferences{Protection: []string{"protected-folder:portal"}}},
		Endpoints: []capabilities.CapabilityEndpoint{
			{CapabilityEndpointID: "service_status", EndpointName: "service.status", CompactAddress: "main@portal-service.service.status", Status: capabilities.EndpointStatusActive},
			{CapabilityEndpointID: "service_start", EndpointName: "service.start", CompactAddress: "main@portal-service.service.start", Status: capabilities.EndpointStatusActive},
			{CapabilityEndpointID: "service_stop", EndpointName: "service.stop", CompactAddress: "main@portal-service.service.stop", Status: capabilities.EndpointStatusActive},
			{CapabilityEndpointID: "service_restart", EndpointName: "service.restart", CompactAddress: "main@portal-service.service.restart", Status: capabilities.EndpointStatusActive},
			{CapabilityEndpointID: "service_logs", EndpointName: "service.logs", CompactAddress: "main@portal-service.service.logs", Status: capabilities.EndpointStatusActive},
		},
	}), nil
}

func (c *fakePortalClient) GetProvider(_ context.Context, _ string, ref string) (response.Envelope[capabilities.ProviderInspection], error) {
	c.providerRef = ref
	if c.optionalErr != nil {
		return response.Envelope[capabilities.ProviderInspection]{}, c.optionalErr
	}
	return response.Success("corr_test", capabilities.ProviderInspection{
		Provider:       capabilities.Provider{ProviderID: firstNonEmpty(ref, "provider_test"), ProviderKey: "system", CompactAddress: "main@system", DisplayName: "System", ProviderType: capabilities.ProviderTypeSystem, Status: "active"},
		Health:         &capabilities.ProviderHealth{ProviderID: firstNonEmpty(ref, "provider_test"), HealthStatus: "healthy", AvailabilityStatus: "available", Message: "ok"},
		Endpoints:      []capabilities.CapabilityEndpoint{{CapabilityEndpointID: "capability_endpoint_test", CompactAddress: "main@system.status.read", Status: "active"}},
		UsageDocuments: []capabilities.UsageDocument{{CapabilityUsageDocumentID: "usage_doc_test", Title: "Status usage", BodyFormat: "markdown", ReviewStatus: "approved"}},
	}), nil
}

func (c *fakePortalClient) GetProviderHealth(_ context.Context, _ string, ref string) (response.Envelope[capabilities.ProviderHealth], error) {
	c.providerHealthRef = ref
	if c.optionalErr != nil {
		return response.Envelope[capabilities.ProviderHealth]{}, c.optionalErr
	}
	now := time.Now().UTC()
	return response.Success("corr_test", capabilities.ProviderHealth{ProviderID: firstNonEmpty(ref, "provider_test"), HealthStatus: "healthy", AvailabilityStatus: "available", LastCheckedAt: &now, LastOKAt: &now, Message: "ok"}), nil
}

func (c *fakePortalClient) ListProviderAdvertisements(context.Context, string, capabilities.ProviderAdvertisementFilter) (response.Envelope[[]capabilities.ProviderAdvertisement], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]capabilities.ProviderAdvertisement]{}, c.optionalErr
	}
	return response.Success("corr_test", []capabilities.ProviderAdvertisement{{ProviderAdvertisementID: "provider_advertisement_test", OriginNodeID: "node_workspace", Status: "pending"}}), nil
}

func (c *fakePortalClient) GetProviderAdvertisement(_ context.Context, _ string, ref string) (response.Envelope[capabilities.ProviderAdvertisementInspection], error) {
	c.providerAdRef = ref
	if c.optionalErr != nil {
		return response.Envelope[capabilities.ProviderAdvertisementInspection]{}, c.optionalErr
	}
	return response.Success("corr_test", capabilities.ProviderAdvertisementInspection{
		Advertisement: capabilities.ProviderAdvertisement{ProviderAdvertisementID: firstNonEmpty(ref, "provider_advertisement_test"), OriginNodeID: "node_workspace", Status: "pending", ReceivedAt: time.Now().UTC()},
		Endpoints:     []capabilities.CapabilityEndpoint{{CapabilityEndpointID: "capability_endpoint_test", CompactAddress: "workspace@provider.action", Status: "active"}},
	}), nil
}

func (c *fakePortalClient) ListCapabilities(context.Context, string, capabilities.CapabilityFilter) (response.Envelope[[]capabilities.CapabilityListItem], error) {
	if c.capabilitiesErr != nil {
		return response.Envelope[[]capabilities.CapabilityListItem]{}, c.capabilitiesErr
	}
	if c.optionalErr != nil {
		return response.Envelope[[]capabilities.CapabilityListItem]{}, c.optionalErr
	}
	return response.Success("corr_test", []capabilities.CapabilityListItem{{
		CapabilityEndpoint: capabilities.CapabilityEndpoint{
			CapabilityEndpointID:        "capability_endpoint_test",
			CompactAddress:              "main@system.status.read",
			EndpointName:                "status.read",
			Form:                        "query",
			RiskLevel:                   "low",
			ExecutionAuthorizationLevel: 1,
			Status:                      "active",
		},
		ProviderAddress: "main@system",
		ProviderKey:     "system",
		ProviderHealth:  "healthy",
		ClassName:       "status.read",
		DisplayName:     "Read System Status",
		Description:     "Read current system status.",
	}}), nil
}

func (c *fakePortalClient) GetCapability(_ context.Context, _ string, ref string) (response.Envelope[capabilities.CapabilityInspection], error) {
	c.capabilityRef = ref
	if c.optionalErr != nil {
		return response.Envelope[capabilities.CapabilityInspection]{}, c.optionalErr
	}
	version := capabilities.EndpointVersion{CapabilityEndpointVersionID: "capability_version_test", VersionLabel: "v1", Status: "active"}
	return response.Success("corr_test", capabilities.CapabilityInspection{
		Endpoint:       capabilities.CapabilityEndpoint{CapabilityEndpointID: firstNonEmpty(ref, "capability_endpoint_test"), CompactAddress: "main@system.status.read", EndpointName: "status.read", Form: "query", RiskLevel: "low", ExecutionAuthorizationLevel: 1, Status: "active", InputSchemaJSON: jsonRaw(`{"type":"object"}`), OutputSchemaJSON: jsonRaw(`{"type":"object"}`)},
		Class:          capabilities.CapabilityClass{CapabilityClassID: "capability_class_test", Name: "status.read", DisplayName: "Read System Status"},
		Provider:       capabilities.Provider{ProviderID: "provider_test", ProviderKey: "system", CompactAddress: "main@system", DisplayName: "System", Status: "active"},
		ProviderHealth: &capabilities.ProviderHealth{ProviderID: "provider_test", HealthStatus: "healthy"},
		ActiveVersion:  &version,
		UsageDocuments: []capabilities.UsageDocument{{CapabilityUsageDocumentID: "usage_doc_test", Title: "Status usage", BodyFormat: "markdown", ReviewStatus: "approved"}},
	}), nil
}

func (c *fakePortalClient) GetCapabilityUsageDocs(_ context.Context, _ string, ref string) (response.Envelope[[]capabilities.UsageDocument], error) {
	c.usageDocsRef = ref
	if c.optionalErr != nil {
		return response.Envelope[[]capabilities.UsageDocument]{}, c.optionalErr
	}
	return response.Success("corr_test", []capabilities.UsageDocument{{
		CapabilityUsageDocumentID: "usage_doc_test",
		TargetKind:                "capability",
		TargetID:                  firstNonEmpty(ref, "capability_endpoint_test"),
		Title:                     "Status usage",
		BodyFormat:                "markdown",
		Body:                      "Call this capability to read current system status.",
		ReviewStatus:              "approved",
	}}), nil
}

func (c *fakePortalClient) SearchCapabilities(context.Context, string, capabilities.CapabilitySearchInput) (response.Envelope[[]capabilities.CapabilityCandidate], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]capabilities.CapabilityCandidate]{}, c.optionalErr
	}
	return response.Success("corr_test", []capabilities.CapabilityCandidate{{
		CapabilityEndpointID: "capability_endpoint_test",
		CompactAddress:       "main@system.status.read",
		ProviderAddress:      "main@system",
		ProviderHealth:       "healthy",
		DisplayName:          "Read System Status",
		Form:                 "query",
		RiskLevel:            "low",
		Score:                1,
	}}), nil
}

func (c *fakePortalClient) ListRuntimeBindings(context.Context, string, capabilities.RuntimeBindingFilter) (response.Envelope[[]capabilities.RuntimeBindingInspection], error) {
	if c.runtimeBindingsErr != nil {
		return response.Envelope[[]capabilities.RuntimeBindingInspection]{}, c.runtimeBindingsErr
	}
	if c.optionalErr != nil {
		return response.Envelope[[]capabilities.RuntimeBindingInspection]{}, c.optionalErr
	}
	return response.Success("corr_test", []capabilities.RuntimeBindingInspection{fakeRuntimeBindingInspection("runtime_binding_test")}), nil
}

func (c *fakePortalClient) GetRuntimeBinding(_ context.Context, _ string, ref string) (response.Envelope[capabilities.RuntimeBindingInspection], error) {
	c.runtimeBindingRef = ref
	if c.optionalErr != nil {
		return response.Envelope[capabilities.RuntimeBindingInspection]{}, c.optionalErr
	}
	return response.Success("corr_test", fakeRuntimeBindingInspection(firstNonEmpty(ref, "runtime_binding_test"))), nil
}

func fakeRuntimeBindingInspection(ref string) capabilities.RuntimeBindingInspection {
	return capabilities.RuntimeBindingInspection{
		Binding:         capabilities.EndpointRuntimeBinding{RuntimeBindingID: ref, RuntimeKind: capabilities.RuntimeKindCommand, Status: capabilities.RuntimeBindingStatusActive},
		EndpointVersion: capabilities.EndpointVersion{CapabilityEndpointVersionID: "capability_version_test", VersionLabel: "v1", Status: "active"},
		Endpoint:        capabilities.CapabilityEndpoint{CapabilityEndpointID: "capability_endpoint_test", CompactAddress: "main@system.status.read", Status: "active"},
		Provider:        capabilities.Provider{ProviderID: "provider_test", CompactAddress: "main@system", ProviderKey: "system"},
		Class:           capabilities.CapabilityClass{CapabilityClassID: "capability_class_test", Name: "status.read", DisplayName: "Read System Status"},
	}
}

func (c *fakePortalClient) CallCapability(_ context.Context, _ string, input routing.CapabilityCallInput) (response.Envelope[routing.CapabilityCallOutcome], error) {
	c.callCapability = input
	if c.optionalErr != nil {
		return response.Envelope[routing.CapabilityCallOutcome]{}, c.optionalErr
	}
	call := routing.CapabilityCall{
		CapabilityCallID:     "capability_call_portal",
		RouteID:              "route_portal",
		ProviderID:           "provider_test",
		CapabilityEndpointID: "capability_endpoint_test",
		Operation:            input.Target,
		ExecutionMode:        routing.ExecutionModeImmediate,
		Status:               routing.CapabilityCallStatusCompleted,
	}
	return response.Success("corr_test", routing.CapabilityCallOutcome{
		CapabilityCall: call,
		Route:          routing.Route{RouteID: "route_portal", Status: routing.RouteStatusCompleted},
		Status:         routing.CapabilityCallStatusCompleted,
	}), nil
}

func (c *fakePortalClient) ListCapabilityCalls(context.Context, string, routing.CapabilityCallFilter) (response.Envelope[[]routing.CapabilityCall], error) {
	if c.optionalErr != nil {
		return response.Envelope[[]routing.CapabilityCall]{}, c.optionalErr
	}
	return response.Success("corr_test", []routing.CapabilityCall{{CapabilityCallID: "capability_call_recent", Status: routing.CapabilityCallStatusCompleted}}), nil
}

func (c *fakePortalClient) GetCapabilityCall(_ context.Context, _ string, ref string) (response.Envelope[routing.CapabilityCall], error) {
	c.capabilityCallRef = ref
	if c.optionalErr != nil {
		return response.Envelope[routing.CapabilityCall]{}, c.optionalErr
	}
	return response.Success("corr_test", routing.CapabilityCall{CapabilityCallID: firstNonEmpty(ref, "capability_call_recent"), RouteID: "route_recent", ProviderID: "provider_test", CapabilityEndpointID: "capability_endpoint_test", Operation: "main@system.status.read", ExecutionMode: routing.ExecutionModeImmediate, Status: routing.CapabilityCallStatusCompleted}), nil
}
