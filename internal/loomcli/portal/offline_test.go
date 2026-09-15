package portal

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/loomcli/actions"
)

func TestCollectSnapshotOfflineKeepsLocalStateAndStopsMainFanout(t *testing.T) {
	resolved := initializedTestBox(t)
	updateStateDir := filepath.Join(t.TempDir(), "update-state")
	client := newFakePortalClient()
	client.healthErr = errors.New("Get \"http://user:secret@127.0.0.1:1/health?token=hidden\": connection refused")

	snapshot, err := CollectSnapshot(context.Background(), client, "corr_test", SnapshotOptions{
		BoxResolved:    &resolved,
		UpdateStateDir: updateStateDir,
		MainTransport:  MainTransportHTTP,
		MainTarget:     "http://operator:password@127.0.0.1:1/api?token=hidden#fragment",
	})
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	if !reflect.DeepEqual(client.snapshotCalls, []string{"health"}) {
		t.Fatalf("main calls = %#v, want exactly one health probe", client.snapshotCalls)
	}
	if snapshot.BoxStatus.State != "ok" || snapshot.BoxStatus.RootPath != resolved.RootPath {
		t.Fatalf("local Box state missing: %#v", snapshot.BoxStatus)
	}
	if snapshot.UpdateStatus.StateDir != updateStateDir {
		t.Fatalf("update state dir = %q, want %q", snapshot.UpdateStatus.StateDir, updateStateDir)
	}
	availability := snapshot.MainAvailability
	if availability.State != MainAvailabilityOffline || availability.Source != "health" {
		t.Fatalf("availability = %#v, want health-established offline", availability)
	}
	if availability.Transport != MainTransportHTTP || availability.Target != "http://127.0.0.1:1/api" {
		t.Fatalf("unsafe or incorrect transport metadata: %#v", availability)
	}
	if availability.CheckedAt.IsZero() || availability.Summary != "Main node is unreachable." {
		t.Fatalf("incomplete availability: %#v", availability)
	}
	for _, secret := range []string{"operator", "password", "token=", "hidden", "fragment", "user", "secret"} {
		if strings.Contains(availability.Target+availability.Cause, secret) {
			t.Fatalf("availability leaked %q: %#v", secret, availability)
		}
	}
	if len(snapshot.PartialErrors) != 1 || snapshot.PartialErrors[0].Source != "main_availability" {
		t.Fatalf("offline findings = %#v, want one normalized finding", snapshot.PartialErrors)
	}
}

func TestCollectSnapshotStatusFailureIsDegradedAndContinuesBestEffort(t *testing.T) {
	client := newFakePortalClient()
	client.statusErr = errors.New("status unavailable")

	snapshot, err := CollectSnapshot(context.Background(), client, "corr_test")
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	if snapshot.MainAvailability.State != MainAvailabilityDegraded || snapshot.MainAvailability.Source != "status" {
		t.Fatalf("availability = %#v, want degraded from status", snapshot.MainAvailability)
	}
	if snapshot.Health.Status != "ok" {
		t.Fatalf("health report lost: %#v", snapshot.Health)
	}
	if !containsString(client.snapshotCalls, "workers") || !containsString(client.snapshotCalls, "nodes") {
		t.Fatalf("best-effort main collection did not continue: %#v", client.snapshotCalls)
	}
	if !hasPartialSource(snapshot.PartialErrors, "status") {
		t.Fatalf("status partial missing: %#v", snapshot.PartialErrors)
	}
}

func TestCollectSnapshotOptionalFailureMarksAvailabilityDegraded(t *testing.T) {
	client := newFakePortalClient()
	client.optionalErr = errors.New("optional unavailable")

	snapshot, err := CollectSnapshot(context.Background(), client, "corr_test")
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	if snapshot.MainAvailability.State != MainAvailabilityDegraded {
		t.Fatalf("availability = %#v, want degraded", snapshot.MainAvailability)
	}
	if snapshot.Health.Status != "ok" || snapshot.Status.Status != "ok" {
		t.Fatalf("successful required reports were lost: %#v", snapshot)
	}
}

func TestCollectSnapshotMissingClientRemainsHardError(t *testing.T) {
	if _, err := CollectSnapshot(context.Background(), nil, "corr_test"); !errors.Is(err, ErrMissingClient) {
		t.Fatalf("error = %v, want ErrMissingClient", err)
	}
}

func TestRunBootChecksOfflineCompletesWithSingleWarning(t *testing.T) {
	resolved := initializedTestBox(t)
	client := newFakePortalClient()
	client.healthErr = errors.New("connection refused")

	results, snapshot, err := RunBootChecks(context.Background(), client, "corr_test", SnapshotOptions{
		BoxResolved:   &resolved,
		MainTransport: MainTransportUnix,
		MainTarget:    "/tmp/loom-main.sock",
	})
	if err != nil {
		t.Fatalf("RunBootChecks returned error: %v", err)
	}
	if snapshot.MainAvailability.State != MainAvailabilityOffline {
		t.Fatalf("availability = %q, want offline", snapshot.MainAvailability.State)
	}
	if len(results) != 1 || results[0].ID != "main_availability" || results[0].Status != "warning" {
		t.Fatalf("boot results = %#v, want one completed warning", results)
	}
	if !reflect.DeepEqual(client.snapshotCalls, []string{"health"}) {
		t.Fatalf("main calls = %#v, want exactly one health probe", client.snapshotCalls)
	}
}

func TestOfflineHomeRendersConnectionAndLocalStateWithoutGlobalZeros(t *testing.T) {
	snapshot, _ := collectOfflineTestSnapshot(t)
	snapshot.Status.Jobs.CompletedRecent = 9
	snapshot.Status.Events.Recent = 8
	snapshot.JobStatus.RunningCount = 7

	output := RenderScreen(testMode(), snapshot, actions.DefaultRegistry(), ScreenHome)
	for _, want := range []string{"Main Connection", "offline", "http://127.0.0.1:1", "LOOM Box", "loom-box", "lane", "unavailable", "requires main"} {
		if !strings.Contains(output, want) {
			t.Fatalf("offline Home missing %q:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"9 completed in the last 24h", "8 received in the last 24h", "7 running"} {
		if strings.Contains(output, notWant) {
			t.Fatalf("offline Home rendered stale global value %q:\n%s", notWant, output)
		}
	}
}

func TestOfflineDoctorHasOneActionableConnectionFinding(t *testing.T) {
	snapshot, _ := collectOfflineTestSnapshot(t)
	data := BuildDoctorData(snapshot)
	connectionFindings := 0
	for _, finding := range data.Findings {
		if finding.SourceKind == "main_availability" {
			connectionFindings++
		}
	}
	if connectionFindings != 1 {
		t.Fatalf("connection findings = %d, want 1: %#v", connectionFindings, data.Findings)
	}
	for _, partial := range data.PartialErrors {
		if partial.Source == "main_availability" {
			t.Fatalf("offline cause was duplicated as a partial: %#v", data.PartialErrors)
		}
	}
	output := RenderScreenWithState(RenderInput{
		Mode:         testMode(),
		HomeSnapshot: snapshot,
		Screen:       ScreenDoctor,
		State:        ScreenStateFromSnapshot(ScreenDoctor, snapshot),
	})
	for _, want := range []string{"Main node is unreachable", "http://127.0.0.1:1", "Last checked", "press r"} {
		if !strings.Contains(output, want) {
			t.Fatalf("offline Doctor missing %q:\n%s", want, output)
		}
	}
}

func TestOfflineBoxKeepsLocalDataAndMarksBackendStatusUnavailable(t *testing.T) {
	snapshot, _ := collectOfflineTestSnapshot(t)
	state := ScreenStateFromSnapshot(ScreenBox, snapshot)
	if state.Status != ScreenLoadPartial || state.Data.Box.Status.State != "ok" {
		t.Fatalf("offline Box state = %#v", state)
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), HomeSnapshot: snapshot, Screen: ScreenBox, State: state})
	for _, want := range []string{"LOOM Box", snapshot.BoxStatus.RootPath, "LOOM Lane", "Main is offline", "Backend watch and registration status is unavailable"} {
		if !strings.Contains(output, want) {
			t.Fatalf("offline Box missing %q:\n%s", want, output)
		}
	}
}

func TestOfflineMainOwnedScreensAreUnavailableWithoutLoaderCalls(t *testing.T) {
	snapshot, client := collectOfflineTestSnapshot(t)
	for _, screen := range []string{ScreenProjects, ScreenJobs} {
		t.Run(screen, func(t *testing.T) {
			client.snapshotCalls = nil
			result := LoadScreen(context.Background(), client, "corr_test", screen, snapshot)
			if result.Err != nil || result.State.Status != ScreenLoadUnavailable {
				t.Fatalf("load result = %#v, want unavailable without error", result)
			}
			if len(client.snapshotCalls) != 0 {
				t.Fatalf("offline loader called main: %#v", client.snapshotCalls)
			}
			output := RenderScreenWithState(RenderInput{Mode: testMode(), HomeSnapshot: snapshot, Screen: screen, State: result.State})
			for _, want := range []string{screenTitle(screen), "Main is offline", "This surface requires the main node", "Press r"} {
				if !strings.Contains(output, want) {
					t.Fatalf("unavailable %s screen missing %q:\n%s", screen, want, output)
				}
			}
		})
	}
}

func TestRunOfflineOneShotScreensSucceed(t *testing.T) {
	resolved := initializedTestBox(t)
	for _, screen := range []string{ScreenHome, ScreenDoctor, ScreenBox, ScreenProjects, ScreenJobs} {
		t.Run(screen, func(t *testing.T) {
			client := newFakePortalClient()
			client.healthErr = errors.New("connection refused")
			var out bytes.Buffer
			err := Run(context.Background(), Options{
				Mode:            testMode(),
				Client:          client,
				CorrelationID:   "corr_test",
				BoxResolved:     &resolved,
				UpdateStateDir:  filepath.Join(t.TempDir(), "updates"),
				Out:             &out,
				StartScreen:     screen,
				ExitAfterRender: true,
				NoBootAnimation: true,
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if !reflect.DeepEqual(client.snapshotCalls, []string{"health"}) {
				t.Fatalf("main calls = %#v, want one health probe", client.snapshotCalls)
			}
			offlineSignal := "offline"
			if screen == ScreenDoctor {
				offlineSignal = "Main node is unreachable"
			}
			if !strings.Contains(out.String(), offlineSignal) {
				t.Fatalf("offline render did not expose connection state:\n%s", out.String())
			}
		})
	}
}

func TestRunOfflineOneShotActionAndCommandCannotBypassDependencyGate(t *testing.T) {
	resolved := initializedTestBox(t)

	t.Run("main action", func(t *testing.T) {
		client := newFakePortalClient()
		client.healthErr = errors.New("connection refused")
		var out bytes.Buffer
		err := Run(context.Background(), Options{
			Mode:            testMode(),
			Client:          client,
			CorrelationID:   "corr_offline",
			BoxResolved:     &resolved,
			Out:             &out,
			StartScreen:     ScreenBackground,
			RunActionID:     "worker.selfcheck.run_once",
			ConfirmAction:   true,
			ExitAfterRender: true,
			Registry:        actions.DefaultRegistry(),
		})
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
		if client.runWorkerRef != "" {
			t.Fatalf("offline one-shot action reached main: %q", client.runWorkerRef)
		}
		if !strings.Contains(out.String(), "portal.main_offline") {
			t.Fatalf("offline action result missing stable code:\n%s", out.String())
		}
	})

	t.Run("main command", func(t *testing.T) {
		client := newFakePortalClient()
		client.healthErr = errors.New("connection refused")
		runner := &recordingCommandRunner{}
		var out bytes.Buffer
		err := Run(context.Background(), Options{
			Mode:            testMode(),
			Client:          client,
			CorrelationID:   "corr_offline",
			BoxResolved:     &resolved,
			Out:             &out,
			CommandRunner:   runner,
			CommandRunInput: "$ health",
			ExitAfterRender: true,
		})
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
		if runner.executeCalls != 0 {
			t.Fatalf("offline one-shot command invoked runner %d time(s)", runner.executeCalls)
		}
		if !strings.Contains(out.String(), "portal.main_offline") {
			t.Fatalf("offline command result missing stable code:\n%s", out.String())
		}
	})

	t.Run("local command", func(t *testing.T) {
		client := newFakePortalClient()
		client.healthErr = errors.New("connection refused")
		runner := &recordingCommandRunner{}
		var out bytes.Buffer
		err := Run(context.Background(), Options{
			Mode:            testMode(),
			Client:          client,
			CorrelationID:   "corr_offline",
			BoxResolved:     &resolved,
			Out:             &out,
			CommandRunner:   runner,
			CommandRunInput: "$ version",
			ExitAfterRender: true,
		})
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
		if runner.executeCalls != 1 || !strings.Contains(out.String(), "Command Complete") {
			t.Fatalf("local command did not execute: calls=%d\n%s", runner.executeCalls, out.String())
		}
	})
}

func TestModelRefreshOfflineToOfflineUpdatesCheckedAt(t *testing.T) {
	resolved := initializedTestBox(t)
	client := newFakePortalClient()
	client.healthErr = errors.New("connection refused")
	snapshot, err := CollectSnapshot(context.Background(), client, "corr_refresh", SnapshotOptions{BoxResolved: &resolved})
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	model := refreshTestModel(t, client, snapshot, ScreenBox, SnapshotOptions{BoxResolved: &resolved})
	previousCheckedAt := model.homeSnapshot.MainAvailability.CheckedAt
	client.snapshotCalls = nil
	model = runModelRefresh(t, model)
	if model.screen != ScreenBox || model.homeSnapshot.MainAvailability.State != MainAvailabilityOffline {
		t.Fatalf("offline refresh changed screen or availability: screen=%s availability=%#v", model.screen, model.homeSnapshot.MainAvailability)
	}
	if !model.homeSnapshot.MainAvailability.CheckedAt.After(previousCheckedAt) {
		t.Fatalf("checked-at was not refreshed: before=%s after=%s", previousCheckedAt, model.homeSnapshot.MainAvailability.CheckedAt)
	}
	if !reflect.DeepEqual(client.snapshotCalls, []string{"health"}) {
		t.Fatalf("offline refresh calls = %#v, want one health probe", client.snapshotCalls)
	}
}

func TestModelRefreshOfflineToOnlineReloadsCurrentScreenAndActions(t *testing.T) {
	resolved := initializedTestBox(t)
	client := newFakePortalClient()
	client.healthErr = errors.New("connection refused")
	snapshot, err := CollectSnapshot(context.Background(), client, "corr_refresh", SnapshotOptions{BoxResolved: &resolved})
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	model := refreshTestModel(t, client, snapshot, ScreenBackground, SnapshotOptions{BoxResolved: &resolved})
	if model.currentScreenState().Status != ScreenLoadUnavailable {
		t.Fatalf("initial background state = %s, want unavailable", model.currentScreenState().Status)
	}

	client.healthErr = nil
	model = runModelRefresh(t, model)
	state := model.currentScreenState()
	if model.screen != ScreenBackground || model.homeSnapshot.MainAvailability.State != MainAvailabilityOnline || state.Status != ScreenLoadLoaded {
		t.Fatalf("offline-to-online state = screen=%s availability=%#v state=%#v", model.screen, model.homeSnapshot.MainAvailability, state)
	}
	if len(state.Data.Background.Workers) == 0 {
		t.Fatalf("reconnected background did not repopulate: %#v", state.Data.Background)
	}
	action, ok := findPortalAction(ScreenActions(state), "worker.main_worker_selfcheck.run_once")
	if !ok || action.Disabled() || action.ExecutionDependency != ExecutionDependencyMain {
		t.Fatalf("main action was not re-enabled after reconnect: %#v", action)
	}
}

func TestModelRefreshOnlineToDegradedPreservesScreenAndSelection(t *testing.T) {
	resolved := initializedTestBox(t)
	client := newFakePortalClient()
	snapshot, err := CollectSnapshot(context.Background(), client, "corr_refresh", SnapshotOptions{BoxResolved: &resolved})
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	model := refreshTestModel(t, client, snapshot, ScreenBackground, SnapshotOptions{BoxResolved: &resolved})
	state := model.currentScreenState()
	items := ScreenSelectableItems(state)
	if len(items) < 2 {
		t.Fatalf("background fixture has too few selectable items: %#v", items)
	}
	state.SelectedIndex = 1
	selectedIdentity := selectableItemIdentity(items[1])
	model.setScreenState(ScreenBackground, state)

	client.statusErr = errors.New("status unavailable")
	model = runModelRefresh(t, model)
	state = model.currentScreenState()
	if model.screen != ScreenBackground || model.homeSnapshot.MainAvailability.State != MainAvailabilityDegraded || state.Status == ScreenLoadUnavailable {
		t.Fatalf("online-to-degraded state = screen=%s availability=%#v state=%#v", model.screen, model.homeSnapshot.MainAvailability, state)
	}
	items = ScreenSelectableItems(state)
	if state.SelectedIndex >= len(items) || selectableItemIdentity(items[state.SelectedIndex]) != selectedIdentity {
		t.Fatalf("selection was not preserved: index=%d items=%#v want=%q", state.SelectedIndex, items, selectedIdentity)
	}
}

func TestModelRefreshOnlineOrDegradedToOfflineClearsStaleGlobalState(t *testing.T) {
	for _, startDegraded := range []bool{false, true} {
		name := "online"
		if startDegraded {
			name = "degraded"
		}
		t.Run(name, func(t *testing.T) {
			resolved := initializedTestBox(t)
			client := newFakePortalClient()
			if startDegraded {
				client.statusErr = errors.New("status unavailable")
			}
			snapshot, err := CollectSnapshot(context.Background(), client, "corr_refresh", SnapshotOptions{BoxResolved: &resolved})
			if err != nil {
				t.Fatalf("CollectSnapshot returned error: %v", err)
			}
			model := refreshTestModel(t, client, snapshot, ScreenJobs, SnapshotOptions{BoxResolved: &resolved})
			background := LoadScreen(context.Background(), client, "corr_refresh", ScreenBackground, snapshot)
			model.setScreenState(ScreenBackground, background.State)
			model.setScreenState(ScreenBox, ScreenStateFromSnapshot(ScreenBox, snapshot))
			if len(model.currentScreenState().Data.Jobs.Jobs) == 0 || len(model.homeSnapshot.Workers) == 0 {
				t.Fatal("online fixture did not contain stale-prone global rows")
			}

			client.statusErr = nil
			client.healthErr = errors.New("connection refused")
			model = runModelRefresh(t, model)
			state := model.currentScreenState()
			if model.screen != ScreenJobs || state.Status != ScreenLoadUnavailable || model.homeSnapshot.MainAvailability.State != MainAvailabilityOffline {
				t.Fatalf("disconnect state = screen=%s state=%#v availability=%#v", model.screen, state, model.homeSnapshot.MainAvailability)
			}
			if len(state.Data.Jobs.Jobs) != 0 || len(model.homeSnapshot.Workers) != 0 || len(model.homeSnapshot.IndexFailures) != 0 {
				t.Fatalf("stale global data survived disconnect: state=%#v snapshot=%#v", state.Data.Jobs, model.homeSnapshot)
			}
			if cached := model.screenStates[ScreenBackground]; cached.Status != ScreenLoadUnavailable || len(cached.Data.Background.Workers) != 0 {
				t.Fatalf("cached main screen retained stale data: %#v", cached)
			}
			boxState := model.screenStates[ScreenBox]
			if boxState.Data.Box.Status.State != "ok" {
				t.Fatalf("local Box state was not preserved: %#v", boxState.Data.Box.Status)
			}
			watch, ok := findPortalAction(ScreenActions(boxState), "box.watch_policy.apply")
			if !ok || watch.DisabledReason != mainOfflineExecutionReason {
				t.Fatalf("cached Box actions were not re-evaluated: %#v", watch)
			}
		})
	}
}

func refreshTestModel(t *testing.T, client *fakePortalClient, snapshot Snapshot, screen string, options SnapshotOptions) Model {
	t.Helper()
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          client,
		CorrelationID:   "corr_refresh",
		Snapshot:        snapshot,
		Registry:        actions.DefaultRegistry(),
		StartScreen:     screen,
		NoBootAnimation: true,
		SnapshotOptions: options,
	})
	var result ScreenLoadResult
	if snapshot.MainAvailability.State == MainAvailabilityOffline || !mainOwnedScreen(screen) {
		result = LoadScreenFromSnapshot(screen, snapshot)
	} else {
		result = LoadScreen(context.Background(), client, "corr_refresh", screen, snapshot, options)
	}
	model.setScreenState(screen, result.State)
	return model
}

func runModelRefresh(t *testing.T, model Model) Model {
	t.Helper()
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("refresh did not return a command")
	}
	updated, _ = model.Update(cmd())
	return updated.(Model)
}

func collectOfflineTestSnapshot(t *testing.T) (Snapshot, *fakePortalClient) {
	t.Helper()
	resolved := initializedTestBox(t)
	client := newFakePortalClient()
	client.healthErr = errors.New("connection refused")
	snapshot, err := CollectSnapshot(context.Background(), client, "corr_test", SnapshotOptions{
		BoxResolved:   &resolved,
		MainTransport: MainTransportHTTP,
		MainTarget:    "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatalf("CollectSnapshot returned error: %v", err)
	}
	return snapshot, client
}

func initializedTestBox(t *testing.T) box.Resolved {
	t.Helper()
	resolved := box.Resolved{
		RootPath:      filepath.Join(t.TempDir(), "loom-box"),
		PathSource:    "test",
		Profile:       box.ProfileWorkspace,
		ProfileSource: "test",
		OwnerNode:     "workspace-test",
		NodeRole:      "workspace",
	}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	return resolved
}
