package portal

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/loomcli/actions"
)

func TestEveryPortalExecutorConstantHasDependencyDecision(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "action_model.go", nil, 0)
	if err != nil {
		t.Fatalf("parse action_model.go: %v", err)
	}
	seen := map[string]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for index, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "PortalExecutor") || index >= len(spec.Values) {
				continue
			}
			literal, ok := spec.Values[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", name.Name, err)
			}
			seen[name.Name] = value
		}
		return true
	})
	if len(seen) == 0 {
		t.Fatal("no PortalExecutor constants found")
	}
	for name, value := range seen {
		dependency, known := portalExecutorDependencyDecision(value)
		if !known {
			t.Errorf("%s (%q) has no explicit dependency decision", name, value)
		}
		if dependency == "" {
			t.Errorf("%s (%q) has an empty dependency", name, value)
		}
	}
	if dependency, known := portalExecutorDependencyDecision("future.executor"); known || dependency != ExecutionDependencyMain {
		t.Fatalf("unknown dependency = %q, known=%v; want main, false", dependency, known)
	}
	if dependency := portalExecutorDependency(PortalExecutorProjectMigrateLayout); dependency != ExecutionDependencyMain {
		t.Fatalf("project layout migration dependency = %q, want main", dependency)
	}
}

func TestOfflineActionDependencyGatingPreservesLocalAndLifecycleReasons(t *testing.T) {
	offline := MainAvailability{State: MainAvailabilityOffline}
	local := applyMainAvailabilityToAction(NewBoxInitAction(box.Status{State: "missing", RootPath: filepath.Join(t.TempDir(), "loom-box"), Profile: box.ProfileWorkspace}), offline)
	if local.Disabled() || local.ExecutionDependency != ExecutionDependencyLocal {
		t.Fatalf("local Box init should remain available offline: %#v", local)
	}

	state := ScreenState{
		Screen:           ScreenBox,
		MainAvailability: offline,
		Data: ScreenData{Box: BoxData{Status: box.Status{
			State:    "ok",
			RootPath: filepath.Join(t.TempDir(), "loom-box"),
			Profile:  box.ProfileWorkspace,
		}}},
	}
	watch, ok := findPortalAction(ScreenActions(state), "box.watch_policy.apply")
	if !ok || !watch.Disabled() || watch.DisabledReason != mainOfflineExecutionReason || watch.ExecutionDependency != ExecutionDependencyMain {
		t.Fatalf("main-backed watch action was not offline-gated: %#v", watch)
	}

	archived := PortalAction{
		ID:                  "project.archived.layout.migrate",
		State:               ActionDisabled,
		DisabledReason:      "Archived projects are immutable.",
		Executor:            PortalActionExecutor{Kind: PortalExecutorProjectMigrateLayout},
		ExecutionDependency: ExecutionDependencyMain,
	}
	archived = applyMainAvailabilityToAction(archived, offline)
	if archived.DisabledReason != "Archived projects are immutable." {
		t.Fatalf("offline gating replaced lifecycle guard: %#v", archived)
	}
}

func TestOfflineActionExecutorRejectsStaleAndUnknownMainActions(t *testing.T) {
	offline := MainAvailability{State: MainAvailabilityOffline}
	client := newFakePortalClient()
	for _, action := range []PortalAction{
		{ID: "worker.inspect", Label: "Inspect worker", Risk: ActionRiskInspect, State: ActionAvailable, Executor: PortalActionExecutor{Kind: PortalExecutorWorkerInspect, Target: "main.worker"}},
		{ID: "future.action", Label: "Future action", Risk: ActionRiskInspect, State: ActionAvailable, Executor: PortalActionExecutor{Kind: "future.executor"}},
	} {
		result := (ActionExecutor{Client: client, MainAvailability: offline}).Execute(context.Background(), action)
		if result.ErrorCode != "portal.main_offline" || result.Status != ActionLifecycleFailed {
			t.Fatalf("offline result for %s = %#v", action.ID, result)
		}
	}
	if client.inspectWorkerRef != "" {
		t.Fatalf("offline action reached client: inspect worker ref = %q", client.inspectWorkerRef)
	}

	navigation := PortalAction{ID: "projects.open", Label: "Projects", Executor: PortalActionExecutor{Kind: PortalExecutorNavigate, Target: ScreenProjects}}
	result := (ActionExecutor{MainAvailability: offline}).Execute(context.Background(), navigation)
	if result.Status != ActionLifecycleSucceeded {
		t.Fatalf("navigation should remain available offline: %#v", result)
	}
}

func TestOfflineLocalBoxInitExecutesInControlledFixture(t *testing.T) {
	root := filepath.Join(t.TempDir(), "loom-box")
	action := NewBoxInitAction(box.Status{State: "missing", RootPath: root, Profile: box.ProfileWorkspace})
	result, err := ExecutePortalAction(context.Background(), nil, "corr_offline", action, true, MainAvailability{State: MainAvailabilityOffline})
	if err != nil {
		t.Fatalf("ExecutePortalAction returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded {
		t.Fatalf("local Box init result = %#v", result)
	}
	if status := box.Inspect(box.Resolved{RootPath: root, Profile: box.ProfileWorkspace}); status.State != "ok" {
		t.Fatalf("initialized Box status = %#v", status)
	}
}

func TestPortalActionFromRegistryRiskMapping(t *testing.T) {
	tests := []struct {
		risk actions.RiskLevel
		want PortalActionRisk
	}{
		{actions.RiskReadOnly, ActionRiskInspect},
		{actions.RiskSafeRun, ActionRiskSafeRun},
		{actions.RiskStateChange, ActionRiskSensitive},
		{actions.RiskDestructive, ActionRiskDangerous},
		{actions.RiskLevel("custom"), ActionRiskBlocked},
	}
	for _, tc := range tests {
		action := PortalActionFromRegistry(actions.Action{
			ID:              "test.action",
			Title:           "Test Action",
			Description:     "test action",
			Domain:          "test",
			Risk:            tc.risk,
			ExecutionKind:   PortalExecutorWorkerRunOnce,
			ExecutionTarget: "main.worker_selfcheck",
			Enabled:         true,
		})
		if action.Risk != tc.want {
			t.Fatalf("risk %s mapped to %s, want %s", tc.risk, action.Risk, tc.want)
		}
		if action.RequiresConfirmation() != (tc.want != ActionRiskInspect) {
			t.Fatalf("confirmation for %s = %v", tc.want, action.RequiresConfirmation())
		}
	}
}

func TestDestructivePortalActionIsDisabled(t *testing.T) {
	action := PortalActionFromRegistry(actions.Action{
		ID:              "test.destroy",
		Title:           "Destroy",
		Risk:            actions.RiskDestructive,
		ExecutionKind:   PortalExecutorUnsupported,
		ExecutionTarget: "target",
		Enabled:         true,
	})
	if !action.Disabled() || action.DisabledReason == "" {
		t.Fatalf("destructive action should be disabled: %#v", action)
	}
}

func TestRenderPortalActionPreviewHidesRawDetailsByDefault(t *testing.T) {
	action := PortalActionFromRegistry(actions.Action{
		ID:              "test.inspect",
		Title:           "Inspect",
		Description:     "Read test data.",
		Domain:          "test",
		Risk:            actions.RiskReadOnly,
		RawCommand:      []string{"loom", "health"},
		ExecutionKind:   PortalExecutorUnsupported,
		ExecutionTarget: "health",
		Enabled:         true,
	})
	output := RenderPortalActionPreview(testMode(), ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action})
	if strings.Contains(output, "Raw Details") || strings.Contains(output, "loom health") {
		t.Fatalf("raw details should be hidden by default:\n%s", output)
	}
	output = RenderPortalActionPreview(testMode(), ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action, RawDetails: true})
	if !strings.Contains(output, "Raw Details") || !strings.Contains(output, "loom health") {
		t.Fatalf("raw details should be visible when requested:\n%s", output)
	}
}

func TestErrActionNotFoundIncludesActionID(t *testing.T) {
	if got := ErrActionNotFound("missing.action").Error(); !strings.Contains(got, "missing.action") {
		t.Fatalf("error does not include action id: %s", got)
	}
	if got := ErrActionNotFoundOnScreen("missing.action", ScreenCapabilities).Error(); !strings.Contains(got, "missing.action") || !strings.Contains(got, ScreenCapabilities) {
		t.Fatalf("screen-aware not found error missing context: %s", got)
	}
}

func TestResolvePortalActionFindsRegistryAndLiveDynamicActions(t *testing.T) {
	resolver := ActionResolver{
		Registry:      actions.DefaultRegistry(),
		Client:        newFakePortalClient(),
		CorrelationID: "corr_test",
	}
	tests := []struct {
		name     string
		id       string
		screen   string
		executor string
	}{
		{"registry", "background.open", ScreenHome, PortalExecutorNavigate},
		{"worker", "worker.main_worker_selfcheck.run_once", ScreenBackground, PortalExecutorWorkerRunOnce},
		{"database object", "database.object.object_test.inspect", ScreenDatabase, PortalExecutorObjectInspect},
		{"provider", "capability.provider.provider_test.health", ScreenCapabilities, PortalExecutorProviderHealth},
		{"capability", "capability.endpoint.capability_endpoint_test.usage_docs", ScreenCapabilities, PortalExecutorCapabilityUsageDocs},
		{"job", "job.job_recent.inspect", ScreenJobs, PortalExecutorJobInspect},
		{"node", "node.node_main.health", ScreenNodes, PortalExecutorNodeHealth},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			action, err := ResolvePortalAction(context.Background(), resolver, tc.id, tc.screen, Snapshot{})
			if err != nil {
				t.Fatalf("ResolvePortalAction(%s) returned error: %v", tc.id, err)
			}
			if action.Executor.Kind != tc.executor {
				t.Fatalf("executor = %s, want %s: %#v", action.Executor.Kind, tc.executor, action)
			}
		})
	}
}

func TestMissingClientErrorsDescribeOperation(t *testing.T) {
	if got := ErrMissingClientFor("load database screen").Error(); !strings.Contains(got, ErrMissingClient.Error()) || !strings.Contains(got, "load database screen") {
		t.Fatalf("missing client operation error = %s", got)
	}
	action := PortalAction{ID: "test.action", Label: "Test Action", Executor: PortalActionExecutor{Kind: PortalExecutorWorkerInspect}}
	result := failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	if !strings.Contains(result.ErrorMessage, PortalExecutorWorkerInspect) {
		t.Fatalf("missing client action result lacks executor context: %#v", result)
	}
}
