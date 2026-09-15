package portal

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func TestLoomignorePortalActionInventoryAndAvailability(t *testing.T) {
	root := t.TempDir()
	status := slice8BoxStatus(root)
	online := ScreenState{
		Screen:           ScreenBox,
		MainAvailability: MainAvailability{State: MainAvailabilityOnline},
		Data:             ScreenData{Box: BoxData{Status: status}},
	}
	inventory := ActionInventoryForScreen(online)
	for _, expected := range []struct {
		id         string
		dependency ExecutionDependency
	}{
		{"box.lane.send", ExecutionDependencyMain},
		{"box.lane.plan", ExecutionDependencyLocal},
		{"box.lane.send.source_only", ExecutionDependencyMain},
		{"box.lane.send.exact", ExecutionDependencyMain},
		{"box.ignore.inspect", ExecutionDependencyLocal},
	} {
		item, ok := slice8InventoryItem(inventory, expected.id)
		if !ok || !item.Available || item.ExecutionDependency != expected.dependency {
			t.Fatalf("online inventory %s = %#v", expected.id, item)
		}
	}
	primary, ok := findPortalAction(ScreenActions(online), "box.lane.send")
	if !ok || primary.Label != "Send Lane" || primary.Executor.Payload["lane_profile"] != string(filepolicy.ProfileFaithful) || primary.Executor.Payload["lane_transport"] != string(lane.TransportModeAuto) {
		t.Fatalf("primary faithful send = %#v", primary)
	}
	exact, ok := findPortalAction(ScreenActions(online), "box.lane.send.exact")
	if !ok || !strings.Contains(exact.ConfirmationPolicy.Prompt, "bypasses every .loomignore rule") {
		t.Fatalf("exact action did not expose bypass warning: %#v", exact)
	}

	degraded := online
	degraded.MainAvailability = MainAvailability{State: MainAvailabilityDegraded}
	if item, _ := slice8InventoryItem(ActionInventoryForScreen(degraded), "box.lane.send.exact"); !item.Available {
		t.Fatalf("degraded main should preserve executable send action: %#v", item)
	}

	offline := online
	offline.MainAvailability = MainAvailability{State: MainAvailabilityOffline}
	offlineInventory := ActionInventoryForScreen(offline)
	for _, id := range []string{"box.lane.send", "box.lane.send.source_only", "box.lane.send.exact"} {
		item, ok := slice8InventoryItem(offlineInventory, id)
		if !ok || item.Available || item.DisabledReason != mainOfflineExecutionReason {
			t.Fatalf("offline main action %s = %#v", id, item)
		}
	}
	for _, id := range []string{"box.lane.plan", "box.ignore.inspect"} {
		item, ok := slice8InventoryItem(offlineInventory, id)
		if !ok || !item.Available || item.ExecutionDependency != ExecutionDependencyLocal {
			t.Fatalf("offline local action %s = %#v", id, item)
		}
	}
}

func TestLanePlanRemainsLocalWhenTransferToolsAreMissing(t *testing.T) {
	status := slice8BoxStatus(t.TempDir())
	status.Lane.Preflight = lane.PreflightStatus{Status: lane.PreflightMissingTools}
	offline := ScreenState{
		Screen:           ScreenBox,
		MainAvailability: MainAvailability{State: MainAvailabilityOffline},
		Data:             ScreenData{Box: BoxData{Status: status}},
	}
	inventory := ActionInventoryForScreen(offline)
	plan, ok := slice8InventoryItem(inventory, "box.lane.plan")
	if !ok || !plan.Available || plan.ExecutionDependency != ExecutionDependencyLocal {
		t.Fatalf("offline missing-tools plan = %#v", plan)
	}
	for _, id := range []string{"box.lane.send", "box.lane.send.source_only", "box.lane.send.exact"} {
		item, ok := slice8InventoryItem(inventory, id)
		if !ok || item.Available {
			t.Fatalf("offline missing-tools send %s = %#v", id, item)
		}
	}

	online := offline
	online.MainAvailability = MainAvailability{State: MainAvailabilityOnline}
	for _, id := range []string{"box.lane.send", "box.lane.send.source_only", "box.lane.send.exact"} {
		item, ok := slice8InventoryItem(ActionInventoryForScreen(online), id)
		if !ok || item.Available || !strings.Contains(item.DisabledReason, "Install rsync and ssh") {
			t.Fatalf("online missing-tools send %s = %#v", id, item)
		}
	}
}

func TestLanePlanExecutesWithoutTransferToolsMainOrFilesystemMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "loom-box")
	resolved := box.Resolved{
		RootPath:      root,
		PathSource:    "test",
		Profile:       box.ProfileWorkspace,
		ProfileSource: "test",
		OwnerNode:     "macbook",
		NodeRole:      "workspace",
	}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loomignore"), []byte("loom-lane/ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, box.DefaultLaneDirName, "kept.txt"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, box.DefaultLaneDirName, "ignored.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := box.Inspect(resolved)
	if status.State != "ok" || status.Lane == nil {
		t.Fatalf("fixture Box status = %#v", status)
	}
	status.Lane.Preflight = lane.PreflightStatus{Status: lane.PreflightMissingTools}
	action := NewBoxLanePlanAction(status)
	if action.State != ActionAvailable || action.ExecutionDependency != ExecutionDependencyLocal {
		t.Fatalf("missing-tools Lane plan action = %#v", action)
	}
	before := slice8TreeSnapshot(t, root)
	t.Setenv("PATH", t.TempDir())
	result, err := ExecutePortalAction(context.Background(), nil, "corr_local_plan", action, true, MainAvailability{State: MainAvailabilityOffline})
	if err != nil || result.Status != ActionLifecycleSucceeded {
		t.Fatalf("local Lane plan failed: result=%#v err=%v", result, err)
	}
	after := slice8TreeSnapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Lane plan changed Box filesystem:\nbefore=%#v\nafter=%#v", before, after)
	}
	if slice8ResultField(result, "Policy Profile") != string(filepolicy.ProfileFaithful) ||
		slice8ResultField(result, "Policy Files") != "1" ||
		slice8ResultField(result, "Protected Files") != "1" ||
		slice8ResultField(result, "Ignored Files") != "2" ||
		slice8ResultField(result, "Requested Transport") != string(lane.TransportModeAuto) ||
		slice8ResultField(result, "Recommended Transport") != string(lane.TransportModeFileTree) ||
		slice8ResultField(result, "Selected Transport") != string(lane.TransportModeFileTree) ||
		slice8ResultField(result, "Estimated Temporary Space") == "" {
		t.Fatalf("Lane plan policy/count fields = %#v", result.Fields)
	}
	if slice8ResultField(result, "Batch") != "" || slice8ResultField(result, "Removed Local Items") != "" ||
		!strings.Contains(result.Summary, "no transfer tools or main connection were used") {
		t.Fatalf("Lane plan exposed transfer side effects: %#v", result)
	}
}

func TestProjectExportActionsGroupModesAndGateLocalVersusBackend(t *testing.T) {
	root := t.TempDir()
	state := slice8ProjectState(root, MainAvailabilityOnline)
	inventory := ActionInventoryForScreen(state)
	for _, mode := range []projectexport.Mode{projectexport.ModeHuman, projectexport.ModePortable, projectexport.ModeArchival} {
		localID := "project.portal_project.export." + string(mode) + ".local"
		backendID := "project.portal_project.export." + string(mode) + ".backend"
		local, localOK := slice8InventoryItem(inventory, localID)
		backend, backendOK := slice8InventoryItem(inventory, backendID)
		if !localOK || !local.Available || local.ExecutionDependency != ExecutionDependencyLocal || !local.RequiresInput {
			t.Fatalf("local %s export = %#v", mode, local)
		}
		if !backendOK || !backend.Available || backend.ExecutionDependency != ExecutionDependencyMain || !backend.RequiresInput {
			t.Fatalf("backend %s export = %#v", mode, backend)
		}
	}

	records := ScreenRecordItems(state)
	groupFound := false
	for _, item := range records {
		if item.RecordRef == "projects.project_export" && item.Label == "Export Project..." && len(item.RelatedActions) == 6 {
			groupFound = true
		}
	}
	if !groupFound {
		t.Fatalf("project export group missing from detail records: %#v", selectableRecordKinds(records))
	}

	offline := slice8ProjectState(root, MainAvailabilityOffline)
	for _, mode := range []projectexport.Mode{projectexport.ModeHuman, projectexport.ModePortable, projectexport.ModeArchival} {
		local, _ := slice8InventoryItem(ActionInventoryForScreen(offline), "project.portal_project.export."+string(mode)+".local")
		backend, _ := slice8InventoryItem(ActionInventoryForScreen(offline), "project.portal_project.export."+string(mode)+".backend")
		if !local.Available || backend.Available || backend.DisabledReason != mainOfflineExecutionReason {
			t.Fatalf("offline %s exports: local=%#v backend=%#v", mode, local, backend)
		}
	}

	unreadable := slice8ProjectState(filepath.Join(root, "missing"), MainAvailabilityOnline)
	local, _ := slice8InventoryItem(ActionInventoryForScreen(unreadable), "project.portal_project.export.portable.local")
	if local.Available || !strings.Contains(local.DisabledReason, "not readable") {
		t.Fatalf("unreadable local export = %#v", local)
	}
}

func TestLoomignorePortalLocalExecutors(t *testing.T) {
	boxRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(boxRoot, ".loomignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boxRoot, "kept.txt"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boxRoot, "ignored.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspect := NewBoxIgnoreInspectAction(box.Status{State: "ok", RootPath: boxRoot, Profile: box.ProfileWorkspace})
	inspectResult, err := ExecutePortalAction(context.Background(), nil, "corr_local", inspect, true, MainAvailability{State: MainAvailabilityOffline})
	if err != nil || inspectResult.Status != ActionLifecycleSucceeded {
		t.Fatalf("local ignore inspect failed: result=%#v err=%v", inspectResult, err)
	}
	if slice8ResultField(inspectResult, "Policy Profile") != "managed" || slice8ResultField(inspectResult, "Ignored Files") != "1" || slice8ResultField(inspectResult, "Protected Files") != "2" {
		t.Fatalf("ignore inspection counts = %#v", inspectResult.Fields)
	}

	parent := t.TempDir()
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{Name: "Portal Export", Slug: "portal-export", OwnerNode: "macbook", Preset: projectcontracts.PresetMinimal, Directory: parent})
	if err != nil {
		t.Fatal(err)
	}
	detail := projects.ProjectRegistrationDetail{
		Project:      projects.ProjectDetail{Project: projects.Project{ProjectID: "project_export", Slug: "portal-export", Name: "Portal Export"}},
		Registration: &projects.ProjectContractRegistration{ProjectRoot: scaffold.ProjectRoot},
	}
	var exportAction PortalAction
	for _, action := range NewProjectExportActions(detail, ScreenProjects) {
		if action.Executor.Kind == PortalExecutorProjectExportLocal && action.Executor.Payload["mode"] == string(projectexport.ModePortable) {
			exportAction = action
			break
		}
	}
	output := filepath.Join(t.TempDir(), "portal-export.tar")
	exportAction.SetFieldValue("output_path", output)
	exportResult, err := ExecutePortalAction(context.Background(), nil, "corr_local", exportAction, true, MainAvailability{State: MainAvailabilityOffline})
	if err != nil || exportResult.Status != ActionLifecycleSucceeded {
		t.Fatalf("local project export failed: result=%#v err=%v", exportResult, err)
	}
	if info, err := os.Stat(output); err != nil || info.Size() == 0 {
		t.Fatalf("local project export output: info=%#v err=%v", info, err)
	}
	if slice8ResultField(exportResult, "Mode") != "portable" || slice8ResultField(exportResult, "Output") != output {
		t.Fatalf("local project export fields = %#v", exportResult.Fields)
	}
}

func TestProjectBackendExportExecutorDownloadsToCallerPath(t *testing.T) {
	root := t.TempDir()
	state := slice8ProjectState(root, MainAvailabilityOnline)
	action, ok := findPortalAction(ScreenActions(state), "project.portal_project.export.archival.backend")
	if !ok {
		t.Fatal("backend archival export action missing")
	}
	output := filepath.Join(t.TempDir(), "archival.tar")
	action.SetFieldValue("output_path", output)
	client := &slice8ExportClient{fakePortalClient: newFakePortalClient()}
	result, err := ExecutePortalAction(context.Background(), client, "corr_backend", action, true, state.MainAvailability)
	if err != nil || result.Status != ActionLifecycleSucceeded {
		t.Fatalf("backend export failed: result=%#v err=%v", result, err)
	}
	if client.request.ProjectRef != "portal-project" || client.request.Mode != projectexport.ModeArchival || client.outputPath != output {
		t.Fatalf("backend export call = request=%#v output=%q", client.request, client.outputPath)
	}
}

func TestLoomignorePortalNarrowRenderingKeepsPolicySignals(t *testing.T) {
	root := t.TempDir()
	boxState := ScreenState{
		Screen:              ScreenBox,
		Status:              ScreenLoadLoaded,
		MainAvailability:    MainAvailability{State: MainAvailabilityOnline},
		ExpandedActionGroup: "box.lane_send",
		Data: ScreenData{Box: BoxData{
			Status:               slice8BoxStatus(root),
			WatchStatusAvailable: true,
			WatchStatus: box.WatchStatusResult{Statuses: []mainwatchedroots.RootStatus{{Root: mainwatchedroots.WatchedRoot{
				NodeID:      "macbook",
				RootKey:     "documents",
				WorkerKey:   "documents-worker",
				Status:      "healthy",
				SummaryJSON: json.RawMessage(`{"included":7,"excluded":3}`),
			}}}},
		}},
	}
	boxOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenBox, State: boxState, Width: 44, Height: 28})
	for _, want := range []string{"Send Lane", "Send Lane...", "Inspect Transfer Plan", "profile=faithful", "protected=7 ignored=3"} {
		if !strings.Contains(boxOutput, want) {
			t.Fatalf("narrow Box render missing %q:\n%s", want, boxOutput)
		}
	}

	projectState := slice8ProjectState(root, MainAvailabilityOnline)
	projectState.Status = ScreenLoadLoaded
	projectState.ExpandedActionGroup = "projects.project_export"
	projectRoot := mainwatchedroots.WatchedRoot{
		NodeID:      "main",
		RootKey:     "project-root",
		WorkerKey:   "project-root-worker",
		Status:      "healthy",
		SummaryJSON: json.RawMessage(`{"included":7,"excluded":3}`),
	}
	projectState.Data.Projects.WatchedRoots = []mainwatchedroots.RootStatus{{Root: projectRoot}}
	projectState.Data.Projects.BackupStatus = projectwatch.ProjectBackupStatus{
		BackupRoots: 1,
		Backups:     []mainwatchedroots.BackupStatus{{Root: projectRoot}},
	}
	projectOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: projectState, Width: 44, Height: 28})
	for _, want := range []string{"Export Project...", "Export Human (Local)", "Export Portable (From Main)", "Export Archival (From Main)"} {
		if !strings.Contains(projectOutput, want) {
			t.Fatalf("narrow Projects render missing %q:\n%s", want, projectOutput)
		}
	}
	projectState.ExpandedActionGroup = ""
	projectState.Data.Projects.Explorer.Level = ProjectExplorerStorage
	policyOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: projectState, Width: 44, Height: 80})
	if !strings.Contains(policyOutput, "protected=7 ignored=3") {
		t.Fatalf("narrow project policy render omitted protection counts:\n%s", policyOutput)
	}
}

type slice8ExportClient struct {
	*fakePortalClient
	request    projectexport.Request
	outputPath string
}

func (c *slice8ExportClient) ExportProject(_ context.Context, _ string, request projectexport.Request, outputPath string, _ bool) (projectexport.Summary, error) {
	c.request = request
	c.outputPath = outputPath
	return projectexport.Summary{
		SchemaVersion:     projectexport.SchemaVersion,
		ProjectSlug:       request.ProjectRef,
		Mode:              request.Mode,
		PolicyVersion:     filepolicy.BuiltInPolicyVersion,
		PolicyFingerprint: "sha256:test",
		PolicyHashes:      map[string]string{".loomignore": "sha256:policy"},
		Included:          filepolicy.CountSummary{Count: 4, Bytes: 40},
		Ignored:           filepolicy.CountSummary{Count: 2, Bytes: 20},
		ArchiveBytes:      60,
		ArchiveChecksum:   "sha256:archive",
		OutputPath:        outputPath,
	}, nil
}

func slice8BoxStatus(root string) box.Status {
	return box.Status{
		State:     "ok",
		RootPath:  root,
		Profile:   box.ProfileWorkspace,
		OwnerNode: "macbook",
		Lane: &lane.Status{
			State:             lane.StatePending,
			PendingItems:      1,
			PendingFiles:      5,
			PendingBytes:      50,
			IgnoredFileCount:  2,
			IgnoredBytes:      20,
			Profile:           filepolicy.ProfileFaithful,
			PolicyVersion:     filepolicy.BuiltInPolicyVersion,
			PolicyFingerprint: "sha256:lane",
			Preflight:         lane.PreflightStatus{Status: lane.PreflightReady},
		},
	}
}

func slice8ProjectState(root string, availability MainAvailabilityState) ScreenState {
	project := projects.Project{ProjectID: "project_portal", Slug: "portal-project", Name: "Portal Project", Status: "active"}
	detail := projects.ProjectRegistrationDetail{
		Project:      projects.ProjectDetail{Project: project},
		Registration: &projects.ProjectContractRegistration{ProjectRoot: root},
	}
	return ScreenState{
		Screen:           ScreenProjects,
		Status:           ScreenLoadLoaded,
		MainAvailability: MainAvailability{State: availability},
		Data: ScreenData{Projects: ProjectsData{
			Projects:           []projects.Project{project},
			SelectedProjectRef: "portal-project",
			SelectedProject:    projects.ProjectDetail{Project: project},
			RegistrationDetail: detail,
			Explorer:           ProjectExplorerState{Level: ProjectExplorerDetail, SelectedProjectRef: "portal-project"},
		}},
	}
}

func slice8InventoryItem(items []PortalActionInventoryItem, id string) (PortalActionInventoryItem, bool) {
	for _, item := range items {
		if item.ActionID == id {
			return item, true
		}
	}
	return PortalActionInventoryItem{}, false
}

func slice8ResultField(result PortalActionResult, label string) string {
	for _, field := range result.Fields {
		if field.Label == label {
			return field.Value
		}
	}
	return ""
}

func slice8TreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	if err := filepath.WalkDir(root, func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, pathValue)
		if err != nil {
			return err
		}
		info, err := os.Lstat(pathValue)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(relative)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(pathValue)
			if err != nil {
				return err
			}
			snapshot[key] = fmt.Sprintf("symlink:%s:%d", target, info.ModTime().UnixNano())
		case info.IsDir():
			snapshot[key] = fmt.Sprintf("dir:%o:%d", info.Mode().Perm(), info.ModTime().UnixNano())
		case info.Mode().IsRegular():
			payload, err := os.ReadFile(pathValue)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(payload)
			snapshot[key] = fmt.Sprintf("file:%o:%d:%d:%x", info.Mode().Perm(), info.Size(), info.ModTime().UnixNano(), digest)
		default:
			snapshot[key] = fmt.Sprintf("other:%s:%d", info.Mode(), info.ModTime().UnixNano())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

var _ Client = (*slice8ExportClient)(nil)
var _ portalProjectExportClient = (*slice8ExportClient)(nil)
