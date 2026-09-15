package projectactivation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/modules"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/serviceregistry"
	"loom.local/loom/internal/workflows"
)

func TestActivateDispatchesByFacet(t *testing.T) {
	req := activationRequest()
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, t.TempDir(), "sha256:"+fmt.Sprintf("%064x", 1), nil, nil)}
	svc := NewService(Deps{Projects: projectSvc})

	if _, err := svc.Activate(context.Background(), req, "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("base activation returned error: %v", err)
	}
	if !projectSvc.baseCalled {
		t.Fatal("expected base activation to be delegated")
	}
	if _, err := svc.Activate(context.Background(), req, "script-smoke", projects.ActivateProjectInput{Facet: "not_a_facet"}); !errors.Is(err, projects.ErrFacetActivationUnsupported) {
		t.Fatalf("unsupported facet error = %v", err)
	}
}

func TestActivateRejectsArchivedProjectBeforeMutation(t *testing.T) {
	detail := registeredProjectDetail(t, "unused-root", "sha256:"+fmt.Sprintf("%064x", 1), scriptsFacet(), nil)
	detail.Project.Project.Status = "archived"
	for _, input := range []projects.ActivateProjectInput{
		{},
		{Facet: "scripts"},
		{Facet: "all"},
	} {
		name := strings.TrimSpace(input.Facet)
		if name == "" {
			name = "base"
		}
		t.Run(name, func(t *testing.T) {
			projectSvc := &fakeProjectService{detail: detail}
			svc := NewService(Deps{Projects: projectSvc})
			_, err := svc.Activate(context.Background(), activationRequest(), "script-smoke", input)
			if !projects.IsProjectRuntimeArchived(err) {
				t.Fatalf("Activate error = %v, want archived project error", err)
			}
			if projectSvc.baseCalled || len(projectSvc.upserts) != 0 || projectSvc.marked {
				t.Fatalf("archived activation should not mutate base/upserts/marked: %#v", projectSvc)
			}
		})
	}
}

func TestActivateScriptsRejectsArchivedProjectDirectly(t *testing.T) {
	detail := registeredProjectDetail(t, "unused-root", "sha256:"+fmt.Sprintf("%064x", 1), scriptsFacet(), nil)
	detail.Project.Project.Status = "archived"
	projectSvc := &fakeProjectService{detail: detail}
	svc := NewService(Deps{Projects: projectSvc})

	_, err := svc.ActivateScripts(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{Facet: "scripts"})
	if !projects.IsProjectRuntimeArchived(err) {
		t.Fatalf("ActivateScripts error = %v, want archived project error", err)
	}
	if projectSvc.baseCalled || len(projectSvc.upserts) != 0 || projectSvc.marked {
		t.Fatalf("archived direct scripts activation should not mutate base/upserts/marked: %#v", projectSvc)
	}
}

func TestActivateServicesRechecksArchiveStateAfterAcquiringProjectLock(t *testing.T) {
	detail := registeredProjectDetail(t, "unused-root", "sha256:"+fmt.Sprintf("%064x", 1), nil, nil)
	projectSvc := &archiveLockingProjectService{fakeProjectService: fakeProjectService{detail: detail}}
	svc := NewService(Deps{Projects: projectSvc})

	_, err := svc.ActivateServices(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{Facet: "services"})
	if !projects.IsProjectArchiveInProgress(err) {
		t.Fatalf("ActivateServices error = %v, want archive-in-progress error", err)
	}
	if projectSvc.baseCalled || projectSvc.marked {
		t.Fatalf("activation raced past archive lock: %#v", projectSvc.fakeProjectService)
	}
	wantKeys := projects.ProjectArchiveLockKeys(detail.Project.Project.ProjectID, detail.Project.Project.Slug, "", nil, []string{"services"})
	if strings.Join(projectSvc.lockKeys, "\n") != strings.Join(wantKeys, "\n") {
		t.Fatalf("activation lock keys = %#v, want %#v", projectSvc.lockKeys, wantKeys)
	}
}

func TestActivateDispatchesModulesFacet(t *testing.T) {
	root, hash := writeModuleProject(t)
	projectSvc := &fakeProjectService{detail: registeredModuleProjectDetail(t, root, hash, nil)}
	moduleSvc := &fakeModuleService{}
	svc := NewService(Deps{Projects: projectSvc, Modules: moduleSvc, Now: fixedNow})

	if _, err := svc.Activate(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{Facet: "modules"}); err != nil {
		t.Fatalf("Activate modules facet returned error: %v", err)
	}
	if len(moduleSvc.registers) != 1 {
		t.Fatalf("module registrations = %d", len(moduleSvc.registers))
	}
}

func TestActivateDispatchesWorkflowsFacet(t *testing.T) {
	root, hash := writeWorkflowProject(t, "placeholder", false, false)
	projectSvc := &fakeProjectService{detail: registeredWorkflowProjectDetail(t, root, hash, nil)}
	svc := NewService(Deps{Projects: projectSvc, Now: fixedNow})

	_, err := svc.Activate(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{Facet: "workflows"})
	if !errors.Is(err, ErrWorkflowRuntimeUnsupported) {
		t.Fatalf("Activate workflows facet error = %v, want %v", err, ErrWorkflowRuntimeUnsupported)
	}
}

func TestActivateAllRuntimeFacetsRunsDependenciesFirst(t *testing.T) {
	root, hash := writeScriptAndScheduleProject(t)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsAndSchedulesFacets(), nil)}
	automationSvc := &fakeAutomationService{}
	svc := NewService(Deps{
		Projects:     projectSvc,
		Scripts:      &fakeScriptService{},
		Capabilities: &fakeCapabilityService{},
		Automation:   automationSvc,
		Events:       &fakeEventService{},
		Now:          fixedNow,
	})

	if _, err := svc.Activate(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{Facet: "all"}); err != nil {
		t.Fatalf("Activate all returned error: %v", err)
	}
	if len(projectSvc.upserts) != 1 || projectSvc.upserts[0].ActivationStatus != projects.ProjectScriptExposureStatusActive {
		t.Fatalf("expected script exposure before schedules, got %#v", projectSvc.upserts)
	}
	if len(automationSvc.ensures) != 1 {
		t.Fatalf("schedule ensures = %d", len(automationSvc.ensures))
	}
	if automationSvc.ensures[0].TargetCapability != projectSvc.upserts[0].CapabilityAddress {
		t.Fatalf("schedule target = %q, script capability = %q", automationSvc.ensures[0].TargetCapability, projectSvc.upserts[0].CapabilityAddress)
	}
}

func TestActivateScriptsPreconditions(t *testing.T) {
	req := activationRequest()
	root, hash := writeProject(t, false)

	tests := []struct {
		name   string
		detail projects.ProjectRegistrationDetail
		want   error
	}{
		{
			name: "no registration",
			detail: projects.ProjectRegistrationDetail{
				Project: projectDetail("main"),
			},
			want: ErrNoRegisteredContract,
		},
		{
			name:   "inactive scripts facet",
			detail: registeredProjectDetail(t, root, hash, []projects.ProjectContractFacet{{FacetKey: "scripts", Enabled: false, Present: true}}, nil),
			want:   ErrScriptsFacetInactive,
		},
		{
			name:   "remote project owner",
			detail: registeredProjectDetail(t, root, hash, scriptsFacet(), nil, "node_other"),
			want:   ErrRemoteProjectScriptsUnsupported,
		},
		{
			name:   "stale contract hash",
			detail: registeredProjectDetail(t, root, "sha256:"+fmt.Sprintf("%064x", 2), scriptsFacet(), nil),
			want:   ErrContractStale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(Deps{Projects: &fakeProjectService{detail: tt.detail}})
			_, err := svc.ActivateScripts(context.Background(), req, "script-smoke", projects.ActivateProjectInput{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("ActivateScripts error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestLoadProjectActivationAnalysisResolvesRelativeOverride(t *testing.T) {
	root, hash := writeProject(t, true)
	t.Chdir(root)

	detail := registeredProjectDetail(t, "registered-root-that-should-not-be-used", hash, scriptsFacet(), nil)
	resolvedRoot, analysis, err := loadProjectActivationAnalysis(detail, ".")
	if err != nil {
		t.Fatalf("loadProjectActivationAnalysis returned error: %v", err)
	}
	if resolvedRoot != filepath.Clean(root) {
		t.Fatalf("resolved root = %q, want %q", resolvedRoot, filepath.Clean(root))
	}
	if analysis.Loaded == nil {
		t.Fatal("expected project contract analysis to be loaded")
	}
}

func TestLoadProjectActivationAnalysisReportsResolvedRelativePath(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	detail := registeredProjectDetail(t, "missing-project", "sha256:"+fmt.Sprintf("%064x", 1), scriptsFacet(), nil)
	_, _, err := loadProjectActivationAnalysis(detail, "")
	if !errors.Is(err, ErrProjectRootUnreadable) {
		t.Fatalf("loadProjectActivationAnalysis error = %v, want %v", err, ErrProjectRootUnreadable)
	}
	wantResolved := filepath.Join(cwd, "missing-project")
	if !strings.Contains(err.Error(), wantResolved) {
		t.Fatalf("error should include resolved path %q, got: %v", wantResolved, err)
	}
	if !strings.Contains(err.Error(), `from "missing-project"`) {
		t.Fatalf("error should include original relative path, got: %v", err)
	}
}

func TestLoadProjectActivationAnalysisBlocksMissingRuntimeAccess(t *testing.T) {
	root, hash := writeProject(t, true)
	manifestPath := filepath.Join(root, "scripts", "hello_world", "loom.script.yaml")
	replaceInFile(t, manifestPath, "mode: read_only", "mode: read_write")
	packageRoot := filepath.Join(root, "scripts", "hello_world")
	if err := os.Chmod(packageRoot, 0o555); err != nil {
		t.Fatalf("chmod package root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(packageRoot, 0o755) })

	detail := registeredProjectDetail(t, root, hash, scriptsFacet(), nil)
	_, _, err := loadProjectActivationAnalysis(detail, "")
	if !errors.Is(err, ErrProjectRuntimeAccessBlocked) {
		t.Fatalf("loadProjectActivationAnalysis error = %v, want %v", err, ErrProjectRuntimeAccessBlocked)
	}
	if !strings.Contains(err.Error(), packageRoot) {
		t.Fatalf("error should include blocked package path %q, got: %v", packageRoot, err)
	}
}

func TestActivateScriptsRecordsDisabledExposure(t *testing.T) {
	root, hash := writeProject(t, false)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsFacet(), nil)}
	scriptSvc := &fakeScriptService{}
	capSvc := &fakeCapabilityService{}
	eventSvc := &fakeEventService{}
	svc := NewService(Deps{
		Projects:     projectSvc,
		Scripts:      scriptSvc,
		Capabilities: capSvc,
		Events:       eventSvc,
		Now:          fixedNow,
	})

	if _, err := svc.ActivateScripts(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateScripts returned error: %v", err)
	}
	if len(scriptSvc.registers) != 1 {
		t.Fatalf("script registrations = %d", len(scriptSvc.registers))
	}
	if len(projectSvc.upserts) != 1 {
		t.Fatalf("script exposure upserts = %d", len(projectSvc.upserts))
	}
	upsert := projectSvc.upserts[0]
	if upsert.ScriptKey != "hello_world" || upsert.ExposureEnabled || upsert.ActivationStatus != projects.ProjectScriptExposureStatusDisabled {
		t.Fatalf("unexpected disabled exposure upsert: %#v", upsert)
	}
	if len(capSvc.providers) != 0 || len(eventSvc.appended) != 0 {
		t.Fatalf("disabled exposure should not create capabilities/events: providers=%d events=%d", len(capSvc.providers), len(eventSvc.appended))
	}
	if !projectSvc.marked {
		t.Fatal("expected scripts facet to be marked activated")
	}
}

func TestActivateScriptsCreatesEnabledCapabilityExposure(t *testing.T) {
	root, hash := writeProject(t, true)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsFacet(), nil)}
	scriptSvc := &fakeScriptService{}
	capSvc := &fakeCapabilityService{}
	eventSvc := &fakeEventService{}
	svc := NewService(Deps{
		Projects:     projectSvc,
		Scripts:      scriptSvc,
		Capabilities: capSvc,
		Events:       eventSvc,
		Now:          fixedNow,
	})

	if _, err := svc.ActivateScripts(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateScripts returned error: %v", err)
	}
	if len(capSvc.providers) != 1 || len(capSvc.endpoints) != 1 || len(capSvc.versions) != 1 || len(capSvc.bindings) != 1 {
		t.Fatalf("capability creation calls providers=%d endpoints=%d versions=%d bindings=%d", len(capSvc.providers), len(capSvc.endpoints), len(capSvc.versions), len(capSvc.bindings))
	}
	if got := capSvc.providers[0].CompactAddress; got != "main@script-smoke" {
		t.Fatalf("provider address = %q", got)
	}
	if got := capSvc.endpoints[0].CompactAddress; got != "main@script-smoke.hello_world" {
		t.Fatalf("capability address = %q", got)
	}
	if len(projectSvc.upserts) != 1 || projectSvc.upserts[0].ActivationStatus != projects.ProjectScriptExposureStatusActive {
		t.Fatalf("expected active exposure upsert, got %#v", projectSvc.upserts)
	}
	if len(eventSvc.appended) != 1 || eventSvc.appended[0].EventType != events.TypeProjectScriptExposed {
		t.Fatalf("expected script exposed event, got %#v", eventSvc.appended)
	}
}

func TestActivateScriptsPropagatesCredentialRequirements(t *testing.T) {
	root, hash := writeProject(t, true)
	exposurePath := filepath.Join(root, "scripts", "hello_world", "loom.exposure.yaml")
	replaceInFile(t, exposurePath, "execution:\n  default_mode:", "credentials:\n  required:\n    - ref: telegram.bot_token\n      expose_as: TELEGRAM_BOT_TOKEN\n      kind: env\n\nexecution:\n  default_mode:")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	hash = addCredentialPolicy(t, root, "telegram.bot_token", "TELEGRAM_BOT_TOKEN")
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsFacet(), nil)}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{
		Projects:     projectSvc,
		Scripts:      &fakeScriptService{},
		Capabilities: capSvc,
		Events:       &fakeEventService{},
		Now:          fixedNow,
	})

	if _, err := svc.ActivateScripts(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateScripts returned error: %v", err)
	}
	assertCredentialRequirementsJSON(t, capSvc.endpoints[0].CredentialRequirementsJSON, "telegram.bot_token", "TELEGRAM_BOT_TOKEN")
	assertCredentialRequirementsJSON(t, capSvc.versions[0].CredentialRequirementsJSON, "telegram.bot_token", "TELEGRAM_BOT_TOKEN")
	assertRuntimeCredentialBinding(t, capSvc.bindings[0].RuntimeConfigJSON, "telegram.bot_token", "TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN")
}

func TestActivateScriptsRejectsMissingCredentialPolicy(t *testing.T) {
	root, hash := writeProject(t, true)
	exposurePath := filepath.Join(root, "scripts", "hello_world", "loom.exposure.yaml")
	replaceInFile(t, exposurePath, "execution:\n  default_mode:", "credentials:\n  required:\n    - ref: telegram.bot_token\n      expose_as: TELEGRAM_BOT_TOKEN\n      kind: env\n\nexecution:\n  default_mode:")
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsFacet(), nil)}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{
		Projects:     projectSvc,
		Scripts:      &fakeScriptService{},
		Capabilities: capSvc,
		Events:       &fakeEventService{},
		Now:          fixedNow,
	})

	_, err := svc.ActivateScripts(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrProjectCredentialUnavailable) {
		t.Fatalf("ActivateScripts error = %v, want %v", err, ErrProjectCredentialUnavailable)
	}
	if len(capSvc.endpoints) != 0 || len(capSvc.bindings) != 0 {
		t.Fatalf("credential failure should not expose capabilities: endpoints=%d bindings=%d", len(capSvc.endpoints), len(capSvc.bindings))
	}
}

func TestActivateScriptsRejectsMissingCredentialSource(t *testing.T) {
	root, hash := writeProject(t, true)
	exposurePath := filepath.Join(root, "scripts", "hello_world", "loom.exposure.yaml")
	replaceInFile(t, exposurePath, "execution:\n  default_mode:", "credentials:\n  required:\n    - ref: telegram.bot_token\n      expose_as: TELEGRAM_BOT_TOKEN\n      kind: env\n\nexecution:\n  default_mode:")
	const missingEnv = "LOOM_TEST_MISSING_TELEGRAM_BOT_TOKEN"
	restoreEnv(t, missingEnv)
	hash = addCredentialPolicy(t, root, "telegram.bot_token", missingEnv)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsFacet(), nil)}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{
		Projects:     projectSvc,
		Scripts:      &fakeScriptService{},
		Capabilities: capSvc,
		Events:       &fakeEventService{},
		Now:          fixedNow,
	})

	_, err := svc.ActivateScripts(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrProjectCredentialUnavailable) {
		t.Fatalf("ActivateScripts error = %v, want %v", err, ErrProjectCredentialUnavailable)
	}
	if len(capSvc.endpoints) != 0 || len(capSvc.bindings) != 0 {
		t.Fatalf("credential source failure should not expose capabilities: endpoints=%d bindings=%d", len(capSvc.endpoints), len(capSvc.bindings))
	}
}

func TestActivateScriptsHandlesRemovedAndDisabledPreviousExposures(t *testing.T) {
	t.Run("removed script becomes stale", func(t *testing.T) {
		root, _ := writeProject(t, false)
		if err := os.RemoveAll(filepath.Join(root, "scripts", "hello_world")); err != nil {
			t.Fatalf("remove script package: %v", err)
		}
		previous := projectScriptExposure("old_script", projects.ProjectScriptExposureStatusActive)
		projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hashForFile(t, filepath.Join(root, "loom.project.yaml")), scriptsFacet(), []projects.ProjectScriptExposure{previous})}
		eventSvc := &fakeEventService{}
		svc := NewService(Deps{Projects: projectSvc, Scripts: &fakeScriptService{}, Capabilities: &fakeCapabilityService{}, Events: eventSvc, Now: fixedNow})

		if _, err := svc.ActivateScripts(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
			t.Fatalf("ActivateScripts returned error: %v", err)
		}
		if len(projectSvc.upserts) != 1 || projectSvc.upserts[0].ActivationStatus != projects.ProjectScriptExposureStatusStale {
			t.Fatalf("expected stale exposure upsert, got %#v", projectSvc.upserts)
		}
		if len(eventSvc.appended) != 1 || eventSvc.appended[0].EventType != events.TypeProjectScriptExposureStale {
			t.Fatalf("expected stale event, got %#v", eventSvc.appended)
		}
	})

	t.Run("disabled exposure disables previous endpoint and binding", func(t *testing.T) {
		root, hash := writeProject(t, false)
		previous := projectScriptExposure("hello_world", projects.ProjectScriptExposureStatusActive)
		projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsFacet(), []projects.ProjectScriptExposure{previous})}
		capSvc := &fakeCapabilityService{}
		svc := NewService(Deps{Projects: projectSvc, Scripts: &fakeScriptService{}, Capabilities: capSvc, Events: &fakeEventService{}, Now: fixedNow})

		if _, err := svc.ActivateScripts(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
			t.Fatalf("ActivateScripts returned error: %v", err)
		}
		if len(capSvc.disabledEndpoints) != 1 || len(capSvc.disabledBindings) != 1 {
			t.Fatalf("expected endpoint and binding disable calls, endpoints=%#v bindings=%#v", capSvc.disabledEndpoints, capSvc.disabledBindings)
		}
	})
}

func TestScriptsFacetActivatable(t *testing.T) {
	if scriptsFacetActivatable(nil) {
		t.Fatal("missing scripts facet should not be activatable")
	}
	if scriptsFacetActivatable([]projects.ProjectContractFacet{{FacetKey: "scripts", Enabled: true, Present: false}}) {
		t.Fatal("missing scripts folder should not be activatable")
	}
	if scriptsFacetActivatable([]projects.ProjectContractFacet{{FacetKey: "scripts", Enabled: true, Present: true, Placeholder: true}}) {
		t.Fatal("placeholder scripts facet should not be activatable")
	}
	if !scriptsFacetActivatable(scriptsFacet()) {
		t.Fatal("enabled present scripts facet should be activatable")
	}
}

func TestActivateWorkflowsPreconditions(t *testing.T) {
	req := activationRequest()
	root, hash := writeWorkflowProject(t, "placeholder", false, false)
	remoteDetail := registeredWorkflowProjectDetail(t, root, hash, nil)
	remoteNodeID := "node_other"
	remoteDetail.Project.Project.HomeNodeID = &remoteNodeID

	tests := []struct {
		name   string
		detail projects.ProjectRegistrationDetail
		want   error
	}{
		{
			name: "no registration",
			detail: projects.ProjectRegistrationDetail{
				Project: projectDetail("main"),
			},
			want: ErrNoRegisteredContract,
		},
		{
			name:   "inactive workflows facet",
			detail: registeredProjectDetail(t, root, hash, []projects.ProjectContractFacet{{FacetKey: "workflows", Enabled: false, Present: true}}, nil),
			want:   ErrWorkflowsFacetInactive,
		},
		{
			name:   "remote project owner",
			detail: remoteDetail,
			want:   ErrRemoteProjectWorkflowsUnsupported,
		},
		{
			name:   "stale contract hash",
			detail: registeredWorkflowProjectDetail(t, root, "sha256:"+fmt.Sprintf("%064x", 2), nil),
			want:   ErrContractStale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(Deps{Projects: &fakeProjectService{detail: tt.detail}, Now: fixedNow})
			_, err := svc.ActivateWorkflows(context.Background(), req, "script-smoke", projects.ActivateProjectInput{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("ActivateWorkflows error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestActivateWorkflowsBlocksPlaceholderAndFirstClassRuntime(t *testing.T) {
	t.Run("placeholder", func(t *testing.T) {
		root, hash := writeWorkflowProject(t, "placeholder", false, false)
		projectSvc := &fakeProjectService{detail: registeredWorkflowProjectDetail(t, root, hash, nil)}
		svc := NewService(Deps{Projects: projectSvc, Now: fixedNow})

		_, err := svc.ActivateWorkflows(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
		if !errors.Is(err, ErrWorkflowRuntimeUnsupported) {
			t.Fatalf("ActivateWorkflows error = %v, want %v", err, ErrWorkflowRuntimeUnsupported)
		}
		if projectSvc.marked {
			t.Fatal("placeholder workflow must not mark workflows facet activated")
		}
	})

	t.Run("workflow_runtime", func(t *testing.T) {
		root, hash := writeWorkflowProject(t, "workflow", false, false)
		projectSvc := &fakeProjectService{detail: registeredWorkflowProjectDetail(t, root, hash, nil)}
		svc := NewService(Deps{Projects: projectSvc, Now: fixedNow})

		_, err := svc.ActivateWorkflows(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
		if !errors.Is(err, ErrWorkflowRuntimeUnsupported) {
			t.Fatalf("ActivateWorkflows error = %v, want %v", err, ErrWorkflowRuntimeUnsupported)
		}
		if projectSvc.marked {
			t.Fatal("first-class workflow runtime must not mark workflows facet activated")
		}
	})
}

func TestActivateWorkflowsRequiresActiveScriptCapability(t *testing.T) {
	root, hash := writeWorkflowProject(t, "script", true, true)
	projectSvc := &fakeProjectService{detail: registeredWorkflowProjectDetail(t, root, hash, nil)}
	svc := NewService(Deps{Projects: projectSvc, Now: fixedNow})

	_, err := svc.ActivateWorkflows(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrWorkflowScriptShimUnavailable) {
		t.Fatalf("ActivateWorkflows error = %v, want %v", err, ErrWorkflowScriptShimUnavailable)
	}
	if projectSvc.marked {
		t.Fatal("workflow shim without active script capability must not mark workflows facet activated")
	}
}

func TestActivateWorkflowsMarksScriptShimFacetActivated(t *testing.T) {
	root, hash := writeWorkflowProject(t, "script", true, true)
	projectSvc := &fakeProjectService{detail: registeredWorkflowProjectDetail(t, root, hash, []projects.ProjectScriptExposure{projectScriptExposure("hello_world", projects.ProjectScriptExposureStatusActive)})}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{Projects: projectSvc, Capabilities: capSvc, Now: fixedNow})

	if _, err := svc.ActivateWorkflows(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateWorkflows returned error: %v", err)
	}
	if !projectSvc.marked || projectSvc.markedFacet != "workflows" {
		t.Fatalf("expected workflows facet to be marked activated, facet=%q marked=%t", projectSvc.markedFacet, projectSvc.marked)
	}
	var metadata map[string]any
	if err := json.Unmarshal(projectSvc.markedMetadata, &metadata); err != nil {
		t.Fatalf("facet metadata is not JSON: %v", err)
	}
	if metadata["workflow_count"] != float64(1) || metadata["script_shim_count"] != float64(1) || metadata["blocked_count"] != float64(0) {
		t.Fatalf("unexpected workflows facet metadata: %#v", metadata)
	}
	if got := fmt.Sprint(metadata["capability_addresses"]); !strings.Contains(got, "main@script-smoke.example_workflow") {
		t.Fatalf("metadata should include workflow capability address, got %#v", metadata)
	}
	if len(projectSvc.workflowUpserts) != 1 || projectSvc.workflowUpserts[0].RuntimeKind != capabilities.RuntimeKindScript {
		t.Fatalf("expected script-backed workflow registration, got %#v", projectSvc.workflowUpserts)
	}
}

func TestActivateWorkflowsRegistersExecutableRuntime(t *testing.T) {
	root, hash := writeExecutableWorkflowProject(t)
	projectSvc := &fakeProjectService{detail: registeredWorkflowProjectDetail(t, root, hash, nil)}
	workflowSvc := &fakeWorkflowService{}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{Projects: projectSvc, Workflows: workflowSvc, Capabilities: capSvc, Now: fixedNow})

	if _, err := svc.ActivateWorkflows(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateWorkflows returned error: %v", err)
	}
	if len(workflowSvc.registers) != 1 {
		t.Fatalf("workflow package registrations = %d", len(workflowSvc.registers))
	}
	if got := workflowSvc.registers[0].SlugOverride; got != "script_smoke__example_workflow" {
		t.Fatalf("workflow slug override = %q", got)
	}
	if len(capSvc.providers) != 1 || len(capSvc.endpoints) != 1 || len(capSvc.versions) != 1 || len(capSvc.bindings) != 1 {
		t.Fatalf("capability creation calls providers=%d endpoints=%d versions=%d bindings=%d", len(capSvc.providers), len(capSvc.endpoints), len(capSvc.versions), len(capSvc.bindings))
	}
	if got := capSvc.providers[0].CompactAddress; got != "main@script-smoke" {
		t.Fatalf("workflow provider address = %q", got)
	}
	if got := capSvc.providers[0].ProviderType; got != capabilities.ProviderTypeWorkflowRunner {
		t.Fatalf("workflow provider type = %q", got)
	}
	if got := capSvc.endpoints[0].CompactAddress; got != "main@script-smoke.example_workflow" {
		t.Fatalf("workflow capability address = %q", got)
	}
	if got := capSvc.bindings[0].RuntimeKind; got != capabilities.RuntimeKindWorkflow {
		t.Fatalf("runtime binding kind = %q", got)
	}
	var runtimeConfig map[string]any
	if err := json.Unmarshal(capSvc.bindings[0].RuntimeConfigJSON, &runtimeConfig); err != nil {
		t.Fatalf("runtime config is not JSON: %v", err)
	}
	if runtimeConfig["workflow_ref"] != "workflow_test" || runtimeConfig["execution_mode"] != "wait_for_completion" {
		t.Fatalf("unexpected workflow runtime config: %#v", runtimeConfig)
	}
	if len(projectSvc.workflowUpserts) != 1 {
		t.Fatalf("workflow upserts = %d", len(projectSvc.workflowUpserts))
	}
	upsert := projectSvc.workflowUpserts[0]
	if upsert.RuntimeKind != capabilities.RuntimeKindWorkflow || upsert.WorkflowID != "workflow_test" || upsert.WorkflowVersionID != "workflow_version_test" || upsert.ActivationStatus != projects.ProjectWorkflowRegistrationStatusActive {
		t.Fatalf("unexpected executable workflow upsert: %#v", upsert)
	}
	if !projectSvc.marked || projectSvc.markedFacet != "workflows" {
		t.Fatalf("expected workflows facet to be marked activated, facet=%q marked=%t", projectSvc.markedFacet, projectSvc.marked)
	}
	var metadata map[string]any
	if err := json.Unmarshal(projectSvc.markedMetadata, &metadata); err != nil {
		t.Fatalf("facet metadata is not JSON: %v", err)
	}
	if metadata["executable_workflow_count"] != float64(1) || metadata["script_shim_count"] != float64(0) || metadata["blocked_count"] != float64(0) {
		t.Fatalf("unexpected workflows facet metadata: %#v", metadata)
	}
}

func TestActivateWorkflowsPropagatesCredentialRequirements(t *testing.T) {
	root, hash := writeExecutableWorkflowProject(t)
	workflowPath := filepath.Join(root, "workflows", "example_workflow", "loom.workflow.yaml")
	replaceInFile(t, workflowPath, "inputs:\n  schema:", "credentials:\n  required:\n    - ref: telegram.default_chat_id\n      expose_as: TELEGRAM_CHAT_ID\n      kind: env\n\ninputs:\n  schema:")
	t.Setenv("TELEGRAM_CHAT_ID", "12345")
	hash = addCredentialPolicy(t, root, "telegram.default_chat_id", "TELEGRAM_CHAT_ID")
	projectSvc := &fakeProjectService{detail: registeredWorkflowProjectDetail(t, root, hash, nil)}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{Projects: projectSvc, Workflows: &fakeWorkflowService{}, Capabilities: capSvc, Now: fixedNow})

	if _, err := svc.ActivateWorkflows(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateWorkflows returned error: %v", err)
	}
	assertCredentialRequirementsJSON(t, capSvc.endpoints[0].CredentialRequirementsJSON, "telegram.default_chat_id", "TELEGRAM_CHAT_ID")
	assertCredentialRequirementsJSON(t, capSvc.versions[0].CredentialRequirementsJSON, "telegram.default_chat_id", "TELEGRAM_CHAT_ID")
	assertRuntimeCredentialBinding(t, capSvc.bindings[0].RuntimeConfigJSON, "telegram.default_chat_id", "TELEGRAM_CHAT_ID", "TELEGRAM_CHAT_ID")
}

func TestWorkflowsFacetActivatable(t *testing.T) {
	if workflowsFacetActivatable(nil) {
		t.Fatal("missing workflows facet should not be activatable")
	}
	if workflowsFacetActivatable([]projects.ProjectContractFacet{{FacetKey: "workflows", Enabled: true, Present: false}}) {
		t.Fatal("missing workflows folder should not be activatable")
	}
	if !workflowsFacetActivatable(workflowsFacet()) {
		t.Fatal("enabled present workflows facet should be activatable even while first-class runtime is placeholder-only")
	}
}

func TestActivateSchedulesPreconditions(t *testing.T) {
	req := activationRequest()
	root, hash := writeScheduleProject(t)

	tests := []struct {
		name   string
		detail projects.ProjectRegistrationDetail
		want   error
	}{
		{
			name: "no registration",
			detail: projects.ProjectRegistrationDetail{
				Project: projectDetail("node_main"),
			},
			want: ErrNoRegisteredContract,
		},
		{
			name:   "inactive schedules facet",
			detail: registeredProjectDetail(t, root, hash, []projects.ProjectContractFacet{{FacetKey: "schedules", Enabled: false, Present: true}}, nil),
			want:   ErrSchedulesFacetInactive,
		},
		{
			name:   "stale contract hash",
			detail: registeredProjectDetail(t, root, "sha256:"+fmt.Sprintf("%064x", 2), schedulesFacet(), nil),
			want:   ErrContractStale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(Deps{Projects: &fakeProjectService{detail: tt.detail}, Automation: &fakeAutomationService{}})
			_, err := svc.ActivateSchedules(context.Background(), req, "script-smoke", projects.ActivateProjectInput{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("ActivateSchedules error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestActivateConnectorsCreatesProviderEndpointAndRegistration(t *testing.T) {
	root, hash := writeConnectorProject(t)
	projectSvc := &fakeProjectService{detail: registeredConnectorProjectDetail(t, root, hash, nil)}
	scriptSvc := &fakeScriptService{}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{
		Projects:     projectSvc,
		Scripts:      scriptSvc,
		Capabilities: capSvc,
		Now:          fixedNow,
	})

	if _, err := svc.ActivateConnectors(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateConnectors returned error: %v", err)
	}
	if len(scriptSvc.registers) != 1 {
		t.Fatalf("script registrations = %d", len(scriptSvc.registers))
	}
	if got := filepath.ToSlash(scriptSvc.registers[0].ManifestPath); !strings.HasSuffix(got, "connectors/example_connector/scripts/ping/loom.script.yaml") {
		t.Fatalf("connector script manifest = %q", got)
	}
	if len(capSvc.providers) != 1 || len(capSvc.endpoints) != 1 || len(capSvc.versions) != 1 || len(capSvc.bindings) != 1 {
		t.Fatalf("capability creation calls providers=%d endpoints=%d versions=%d bindings=%d", len(capSvc.providers), len(capSvc.endpoints), len(capSvc.versions), len(capSvc.bindings))
	}
	if got := capSvc.providers[0].CompactAddress; got != "main@example_connector" {
		t.Fatalf("provider address = %q", got)
	}
	if got := capSvc.providers[0].ProviderType; got != capabilities.ProviderTypeConnector {
		t.Fatalf("provider type = %q", got)
	}
	if got := capSvc.endpoints[0].CompactAddress; got != "main@example_connector.ping" {
		t.Fatalf("capability address = %q", got)
	}
	if len(capSvc.usageDocs) != 1 || capSvc.usageDocs[0].TargetKind != capabilities.UsageTargetKindProvider {
		t.Fatalf("provider usage docs = %#v", capSvc.usageDocs)
	}
	if len(projectSvc.connectorUpserts) != 1 || projectSvc.connectorUpserts[0].ActivationStatus != projects.ProjectConnectorRegistrationStatusActive {
		t.Fatalf("expected active connector upsert, got %#v", projectSvc.connectorUpserts)
	}
	if projectSvc.connectorUpserts[0].ActiveCapabilityCount != 1 || !projectSvc.marked {
		t.Fatalf("connector active count/marked mismatch: %#v marked=%t", projectSvc.connectorUpserts[0], projectSvc.marked)
	}
}

func TestActivateConnectorsBlocksUnsupportedRuntime(t *testing.T) {
	root, _ := writeConnectorProject(t)
	replaceInFile(t, filepath.Join(root, "connectors", "example_connector", "loom.connector.yaml"), "kind: script", "kind: native")
	projectSvc := &fakeProjectService{detail: registeredConnectorProjectDetail(t, root, hashForFile(t, filepath.Join(root, "loom.project.yaml")), nil)}
	svc := NewService(Deps{Projects: projectSvc, Scripts: &fakeScriptService{}, Capabilities: &fakeCapabilityService{}, Now: fixedNow})

	_, err := svc.ActivateConnectors(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrConnectorRuntimeUnsupported) {
		t.Fatalf("ActivateConnectors error = %v, want %v", err, ErrConnectorRuntimeUnsupported)
	}
	if len(projectSvc.connectorUpserts) != 1 || projectSvc.connectorUpserts[0].ActivationStatus != projects.ProjectConnectorRegistrationStatusBlocked {
		t.Fatalf("expected blocked connector upsert, got %#v", projectSvc.connectorUpserts)
	}
}

func TestActivateConnectorsRejectsRemoteOwnerNode(t *testing.T) {
	root, hash := writeConnectorProject(t)
	detail := registeredConnectorProjectDetail(t, root, hash, nil)
	remoteNodeID := "node_other"
	detail.Project.Project.HomeNodeID = &remoteNodeID
	projectSvc := &fakeProjectService{detail: detail}
	svc := NewService(Deps{Projects: projectSvc, Scripts: &fakeScriptService{}, Capabilities: &fakeCapabilityService{}, Now: fixedNow})

	_, err := svc.ActivateConnectors(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrConnectorScriptActivationUnsupported) {
		t.Fatalf("ActivateConnectors error = %v, want %v", err, ErrConnectorScriptActivationUnsupported)
	}
	if len(projectSvc.connectorUpserts) != 0 {
		t.Fatalf("remote connector activation should not upsert registrations: %#v", projectSvc.connectorUpserts)
	}
}

func TestActivateConnectorsMarksRemovedConnectorStale(t *testing.T) {
	root, hash := writeConnectorProject(t)
	if err := os.RemoveAll(filepath.Join(root, "connectors", "example_connector")); err != nil {
		t.Fatalf("remove connector package: %v", err)
	}
	previous := projects.ProjectConnectorRegistration{
		ConnectorKey:          "example_connector",
		ConnectorFolder:       "connectors/example_connector",
		ConnectorManifestPath: filepath.Join(root, "connectors", "example_connector", "loom.connector.yaml"),
		ConnectorHash:         "sha256:old",
		ProviderKey:           "example_connector",
		ProviderAddress:       "main@example_connector",
		ProviderStatus:        capabilities.ProviderStatusActive,
		RuntimeKind:           capabilities.RuntimeKindScript,
		CapabilityCount:       1,
		ActiveCapabilityCount: 1,
		ActivationStatus:      projects.ProjectConnectorRegistrationStatusActive,
		Metadata:              json.RawMessage(`{"endpoints":[{"capability_address":"main@example_connector.ping","capability_endpoint_id":"capability_endpoint_test","runtime_binding_id":"runtime_binding_test"}]}`),
	}
	projectSvc := &fakeProjectService{detail: registeredConnectorProjectDetail(t, root, hash, []projects.ProjectConnectorRegistration{previous})}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{Projects: projectSvc, Scripts: &fakeScriptService{}, Capabilities: capSvc, Now: fixedNow})

	if _, err := svc.ActivateConnectors(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateConnectors returned error: %v", err)
	}
	if len(capSvc.disabledEndpoints) != 1 || capSvc.disabledEndpoints[0] != "capability_endpoint_test" {
		t.Fatalf("expected stale connector endpoint disable, got %#v", capSvc.disabledEndpoints)
	}
	if len(capSvc.disabledBindings) != 1 || capSvc.disabledBindings[0] != "runtime_binding_test" {
		t.Fatalf("expected stale connector runtime binding disable, got %#v", capSvc.disabledBindings)
	}
	if len(projectSvc.connectorUpserts) != 1 || projectSvc.connectorUpserts[0].ActivationStatus != projects.ProjectConnectorRegistrationStatusStale {
		t.Fatalf("expected stale connector registration, got %#v", projectSvc.connectorUpserts)
	}
}

func TestActivateSchedulesCreatesPausedScheduleRegistration(t *testing.T) {
	root, hash := writeScheduleProject(t)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, schedulesFacet(), []projects.ProjectScriptExposure{projectScriptExposure("hello_world", projects.ProjectScriptExposureStatusActive)})}
	automationSvc := &fakeAutomationService{}
	svc := NewService(Deps{
		Projects:   projectSvc,
		Automation: automationSvc,
		Now:        fixedNow,
	})

	if _, err := svc.ActivateSchedules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateSchedules returned error: %v", err)
	}
	if len(automationSvc.ensures) != 1 {
		t.Fatalf("schedule ensure calls = %d", len(automationSvc.ensures))
	}
	ensure := automationSvc.ensures[0]
	if ensure.ScheduleKey != "script_smoke__daily_summary" || ensure.Status != automation.ScheduleStatusPaused {
		t.Fatalf("unexpected ensure input: %#v", ensure)
	}
	if ensure.TargetCapability != "main@script-smoke.hello_world" {
		t.Fatalf("target capability = %q", ensure.TargetCapability)
	}
	if len(projectSvc.scheduleUpserts) != 1 || projectSvc.scheduleUpserts[0].ActivationStatus != projects.ProjectScheduleRegistrationStatusPaused {
		t.Fatalf("expected paused schedule registration, got %#v", projectSvc.scheduleUpserts)
	}
	if !projectSvc.marked {
		t.Fatal("expected schedules facet to be marked activated")
	}
}

func TestActivateSchedulesWrapsTargetErrorsWithScheduleKey(t *testing.T) {
	root, hash := writeScheduleProject(t)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, schedulesFacet(), []projects.ProjectScriptExposure{projectScriptExposure("hello_world", projects.ProjectScriptExposureStatusActive)})}
	automationSvc := &fakeAutomationService{err: fmt.Errorf("target capability is not active or does not exist")}
	svc := NewService(Deps{Projects: projectSvc, Automation: automationSvc, Now: fixedNow})

	_, err := svc.ActivateSchedules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrScheduleTargetUnavailable) || !strings.Contains(err.Error(), "daily_summary") || !strings.Contains(err.Error(), "target capability") {
		t.Fatalf("expected contextual schedule target error, got %v", err)
	}
}

func TestActivateSchedulesRequiresActiveLocalTarget(t *testing.T) {
	root, hash := writeScheduleProject(t)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, schedulesFacet(), nil)}
	svc := NewService(Deps{Projects: projectSvc, Automation: &fakeAutomationService{}, Now: fixedNow})

	_, err := svc.ActivateSchedules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrScheduleTargetUnavailable) || !strings.Contains(err.Error(), "project-local provider") {
		t.Fatalf("ActivateSchedules error = %v, want local target dependency error", err)
	}
}

func TestActivateSchedulesRequiresActiveDeclaredLocalTarget(t *testing.T) {
	root, hash := writeScriptAndScheduleProject(t)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsAndSchedulesFacets(), nil)}
	svc := NewService(Deps{Projects: projectSvc, Automation: &fakeAutomationService{}, Now: fixedNow})

	_, err := svc.ActivateSchedules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrScheduleTargetUnavailable) {
		t.Fatalf("ActivateSchedules error = %v, want %v", err, ErrScheduleTargetUnavailable)
	}
	for _, want := range []string{"declared by the scripts facet", "activate scripts first"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ActivateSchedules error should contain %q, got %v", want, err)
		}
	}
}

func TestActivateSchedulesHandlesDisabledAndRemovedPreviousRegistrations(t *testing.T) {
	t.Run("disabled schedule disables previous backend schedule", func(t *testing.T) {
		root, hash := writeScheduleProject(t)
		replaceInFile(t, filepath.Join(root, "schedules", "daily_summary", "loom.schedule.yaml"), "status: draft", "status: disabled")
		previous := projectScheduleRegistration("daily_summary", projects.ProjectScheduleRegistrationStatusPaused)
		projectSvc := &fakeProjectService{detail: registeredScheduleProjectDetail(t, root, hash, []projects.ProjectScheduleRegistration{previous})}
		automationSvc := &fakeAutomationService{}
		svc := NewService(Deps{Projects: projectSvc, Automation: automationSvc, Now: fixedNow})

		if _, err := svc.ActivateSchedules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
			t.Fatalf("ActivateSchedules returned error: %v", err)
		}
		if len(automationSvc.ensures) != 0 {
			t.Fatalf("disabled schedule should not ensure backend schedule, got %#v", automationSvc.ensures)
		}
		if len(automationSvc.disabled) != 1 || automationSvc.disabled[0] != "schedule_daily_summary" {
			t.Fatalf("expected previous backend schedule to be disabled, got %#v", automationSvc.disabled)
		}
		if len(projectSvc.scheduleUpserts) != 1 || projectSvc.scheduleUpserts[0].ActivationStatus != projects.ProjectScheduleRegistrationStatusDisabled {
			t.Fatalf("expected disabled schedule registration, got %#v", projectSvc.scheduleUpserts)
		}
	})

	t.Run("removed schedule becomes stale and disables backend schedule", func(t *testing.T) {
		root, hash := writeScheduleProject(t)
		if err := os.RemoveAll(filepath.Join(root, "schedules", "daily_summary")); err != nil {
			t.Fatalf("remove schedule package: %v", err)
		}
		previous := projectScheduleRegistration("daily_summary", projects.ProjectScheduleRegistrationStatusPaused)
		projectSvc := &fakeProjectService{detail: registeredScheduleProjectDetail(t, root, hash, []projects.ProjectScheduleRegistration{previous})}
		automationSvc := &fakeAutomationService{}
		svc := NewService(Deps{Projects: projectSvc, Automation: automationSvc, Now: fixedNow})

		if _, err := svc.ActivateSchedules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
			t.Fatalf("ActivateSchedules returned error: %v", err)
		}
		if len(automationSvc.disabled) != 1 || automationSvc.disabled[0] != "schedule_daily_summary" {
			t.Fatalf("expected stale backend schedule to be disabled, got %#v", automationSvc.disabled)
		}
		if len(projectSvc.scheduleUpserts) != 1 || projectSvc.scheduleUpserts[0].ActivationStatus != projects.ProjectScheduleRegistrationStatusStale {
			t.Fatalf("expected stale schedule registration, got %#v", projectSvc.scheduleUpserts)
		}
		if !projectSvc.marked {
			t.Fatal("expected schedules facet to be marked activated")
		}
	})
}

func TestActivateDirectEventsPreconditions(t *testing.T) {
	req := activationRequest()
	root, hash := writeDirectEventProject(t)

	tests := []struct {
		name   string
		detail projects.ProjectRegistrationDetail
		want   error
	}{
		{
			name: "no registration",
			detail: projects.ProjectRegistrationDetail{
				Project: projectDetail("node_main"),
			},
			want: ErrNoRegisteredContract,
		},
		{
			name:   "inactive direct events facet",
			detail: registeredProjectDetail(t, root, hash, []projects.ProjectContractFacet{{FacetKey: "direct_events", Enabled: false, Present: true}}, nil),
			want:   ErrDirectEventsFacetInactive,
		},
		{
			name:   "stale contract hash",
			detail: registeredProjectDetail(t, root, "sha256:"+fmt.Sprintf("%064x", 2), directEventsFacet(), nil),
			want:   ErrContractStale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(Deps{Projects: &fakeProjectService{detail: tt.detail}, Automation: &fakeAutomationService{}})
			_, err := svc.ActivateDirectEvents(context.Background(), req, "script-smoke", projects.ActivateProjectInput{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("ActivateDirectEvents error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestActivateDirectEventsCreatesPausedEndpointRegistration(t *testing.T) {
	root, hash := writeDirectEventProject(t)
	detail := registeredDirectEventProjectDetail(t, root, hash, nil)
	detail.ScriptExposures = []projects.ProjectScriptExposure{projectScriptExposure("hello_world", projects.ProjectScriptExposureStatusActive)}
	projectSvc := &fakeProjectService{detail: detail}
	automationSvc := &fakeAutomationService{}
	svc := NewService(Deps{
		Projects:   projectSvc,
		Automation: automationSvc,
		Now:        fixedNow,
	})

	if _, err := svc.ActivateDirectEvents(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateDirectEvents returned error: %v", err)
	}
	if len(automationSvc.integrationEnsures) != 1 {
		t.Fatalf("integration ensure calls = %d", len(automationSvc.integrationEnsures))
	}
	if got := automationSvc.integrationEnsures[0].IntegrationKey; got != "script_smoke__gmail" {
		t.Fatalf("integration key = %q", got)
	}
	if len(automationSvc.authProfileEnsures) != 1 || automationSvc.authProfileEnsures[0].AuthKind != automation.IntegrationAuthPrivateNetwork {
		t.Fatalf("expected private-network auth profile ensure, got %#v", automationSvc.authProfileEnsures)
	}
	if len(automationSvc.endpointEnsures) != 1 {
		t.Fatalf("endpoint ensure calls = %d", len(automationSvc.endpointEnsures))
	}
	ensure := automationSvc.endpointEnsures[0]
	if ensure.EndpointSlug != "script_smoke__gmail_message" || ensure.Status != automation.DirectEventEndpointStatusPaused {
		t.Fatalf("unexpected endpoint ensure input: %#v", ensure)
	}
	if ensure.TargetCapability != "main@script-smoke.hello_world" {
		t.Fatalf("target capability = %q", ensure.TargetCapability)
	}
	if len(projectSvc.directEventUpserts) != 1 || projectSvc.directEventUpserts[0].ActivationStatus != projects.ProjectDirectEventRegistrationStatusPaused {
		t.Fatalf("expected paused direct-event registration, got %#v", projectSvc.directEventUpserts)
	}
	if !projectSvc.marked {
		t.Fatal("expected direct_events facet to be marked activated")
	}
}

func TestActivateDirectEventsRequiresActiveLocalTarget(t *testing.T) {
	root, hash := writeDirectEventProject(t)
	projectSvc := &fakeProjectService{detail: registeredDirectEventProjectDetail(t, root, hash, nil)}
	svc := NewService(Deps{Projects: projectSvc, Automation: &fakeAutomationService{}, Now: fixedNow})

	_, err := svc.ActivateDirectEvents(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrDirectEventTargetUnavailable) || !strings.Contains(err.Error(), "project-local provider") {
		t.Fatalf("ActivateDirectEvents error = %v, want local target dependency error", err)
	}
}

func TestActivateDirectEventsRequiresActiveDeclaredLocalTarget(t *testing.T) {
	root, hash := writeScriptAndDirectEventProject(t)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsAndDirectEventsFacets(), nil)}
	svc := NewService(Deps{Projects: projectSvc, Automation: &fakeAutomationService{}, Now: fixedNow})

	_, err := svc.ActivateDirectEvents(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrDirectEventTargetUnavailable) {
		t.Fatalf("ActivateDirectEvents error = %v, want %v", err, ErrDirectEventTargetUnavailable)
	}
	for _, want := range []string{"declared by the scripts facet", "activate scripts first"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ActivateDirectEvents error should contain %q, got %v", want, err)
		}
	}
}

func TestActivateDirectEventsHandlesDisabledAndRemovedPreviousRegistrations(t *testing.T) {
	t.Run("disabled event disables previous backend endpoint", func(t *testing.T) {
		root, hash := writeDirectEventProject(t)
		replaceInFile(t, filepath.Join(root, "direct_events", "gmail_message", "loom.direct_event.yaml"), "status: draft", "status: disabled")
		previous := projectDirectEventRegistration("gmail_message", projects.ProjectDirectEventRegistrationStatusPaused)
		projectSvc := &fakeProjectService{detail: registeredDirectEventProjectDetail(t, root, hash, []projects.ProjectDirectEventRegistration{previous})}
		automationSvc := &fakeAutomationService{}
		svc := NewService(Deps{Projects: projectSvc, Automation: automationSvc, Now: fixedNow})

		if _, err := svc.ActivateDirectEvents(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
			t.Fatalf("ActivateDirectEvents returned error: %v", err)
		}
		if len(automationSvc.endpointEnsures) != 0 {
			t.Fatalf("disabled direct event should not ensure backend endpoint, got %#v", automationSvc.endpointEnsures)
		}
		if len(automationSvc.disabledEndpoints) != 1 || automationSvc.disabledEndpoints[0] != "direct_event_endpoint_gmail_message" {
			t.Fatalf("expected previous backend endpoint to be disabled, got %#v", automationSvc.disabledEndpoints)
		}
		if len(projectSvc.directEventUpserts) != 1 || projectSvc.directEventUpserts[0].ActivationStatus != projects.ProjectDirectEventRegistrationStatusDisabled {
			t.Fatalf("expected disabled direct-event registration, got %#v", projectSvc.directEventUpserts)
		}
	})

	t.Run("removed event becomes stale and disables backend endpoint", func(t *testing.T) {
		root, hash := writeDirectEventProject(t)
		if err := os.RemoveAll(filepath.Join(root, "direct_events", "gmail_message")); err != nil {
			t.Fatalf("remove direct event package: %v", err)
		}
		previous := projectDirectEventRegistration("gmail_message", projects.ProjectDirectEventRegistrationStatusPaused)
		projectSvc := &fakeProjectService{detail: registeredDirectEventProjectDetail(t, root, hash, []projects.ProjectDirectEventRegistration{previous})}
		automationSvc := &fakeAutomationService{}
		svc := NewService(Deps{Projects: projectSvc, Automation: automationSvc, Now: fixedNow})

		if _, err := svc.ActivateDirectEvents(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
			t.Fatalf("ActivateDirectEvents returned error: %v", err)
		}
		if len(automationSvc.disabledEndpoints) != 1 || automationSvc.disabledEndpoints[0] != "direct_event_endpoint_gmail_message" {
			t.Fatalf("expected stale backend endpoint to be disabled, got %#v", automationSvc.disabledEndpoints)
		}
		if len(projectSvc.directEventUpserts) != 1 || projectSvc.directEventUpserts[0].ActivationStatus != projects.ProjectDirectEventRegistrationStatusStale {
			t.Fatalf("expected stale direct-event registration, got %#v", projectSvc.directEventUpserts)
		}
		if !projectSvc.marked {
			t.Fatal("expected direct_events facet to be marked activated")
		}
	})
}

func TestActivateModulesRegistersPackageAndProjectRegistration(t *testing.T) {
	root, hash := writeModuleProject(t)
	projectSvc := &fakeProjectService{detail: registeredModuleProjectDetail(t, root, hash, nil)}
	moduleSvc := &fakeModuleService{}
	svc := NewService(Deps{
		Projects: projectSvc,
		Modules:  moduleSvc,
		Now:      fixedNow,
	})

	if _, err := svc.ActivateModules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
		t.Fatalf("ActivateModules returned error: %v", err)
	}
	if len(moduleSvc.registers) != 1 {
		t.Fatalf("module registrations = %d", len(moduleSvc.registers))
	}
	if got := filepath.ToSlash(moduleSvc.registers[0].PackagePath); !strings.HasSuffix(got, "modules/example_module") {
		t.Fatalf("module package path = %q", got)
	}
	if len(projectSvc.moduleUpserts) != 1 || projectSvc.moduleUpserts[0].ActivationStatus != projects.ProjectModuleRegistrationStatusRegistered {
		t.Fatalf("expected registered module upsert, got %#v", projectSvc.moduleUpserts)
	}
	upsert := projectSvc.moduleUpserts[0]
	if upsert.ModuleID != "loom.script-smoke" || upsert.ModuleVersionID != "module_version_test" || upsert.ProviderCount != 1 || upsert.CapabilityCount != 1 {
		t.Fatalf("unexpected module registration upsert: %#v", upsert)
	}
	if !projectSvc.marked {
		t.Fatal("expected modules facet to be marked activated")
	}
	var metadata map[string]any
	if err := json.Unmarshal(projectSvc.markedMetadata, &metadata); err != nil {
		t.Fatalf("facet metadata is not JSON: %v", err)
	}
	if metadata["registered_count"] != float64(1) || metadata["disabled_count"] != float64(0) || metadata["blocked_count"] != float64(0) {
		t.Fatalf("unexpected modules facet metadata: %#v", metadata)
	}
}

func TestActivateModulesPreconditions(t *testing.T) {
	req := activationRequest()
	root, hash := writeModuleProject(t)
	remoteDetail := registeredModuleProjectDetail(t, root, hash, nil)
	remoteNodeID := "node_other"
	remoteDetail.Project.Project.HomeNodeID = &remoteNodeID

	tests := []struct {
		name   string
		detail projects.ProjectRegistrationDetail
		want   error
	}{
		{
			name: "no registration",
			detail: projects.ProjectRegistrationDetail{
				Project: projectDetail("main"),
			},
			want: ErrNoRegisteredContract,
		},
		{
			name:   "inactive modules facet",
			detail: registeredProjectDetail(t, root, hash, []projects.ProjectContractFacet{{FacetKey: "modules", Enabled: false, Present: true}}, nil),
			want:   ErrModulesFacetInactive,
		},
		{
			name:   "remote project owner",
			detail: remoteDetail,
			want:   ErrRemoteProjectModulesUnsupported,
		},
		{
			name:   "stale contract hash",
			detail: registeredModuleProjectDetail(t, root, "sha256:"+fmt.Sprintf("%064x", 2), nil),
			want:   ErrContractStale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(Deps{Projects: &fakeProjectService{detail: tt.detail}, Modules: &fakeModuleService{}, Now: fixedNow})
			_, err := svc.ActivateModules(context.Background(), req, "script-smoke", projects.ActivateProjectInput{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("ActivateModules error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestActivateModulesHandlesDisabledAndRemovedPreviousRegistrations(t *testing.T) {
	t.Run("disabled module does not register package", func(t *testing.T) {
		root, _ := writeModuleProject(t)
		replaceInFile(t, filepath.Join(root, "modules", "example_module", "loom.module_project.yaml"), "register: true", "register: false")
		projectSvc := &fakeProjectService{detail: registeredModuleProjectDetail(t, root, hashForFile(t, filepath.Join(root, "loom.project.yaml")), nil)}
		moduleSvc := &fakeModuleService{}
		svc := NewService(Deps{Projects: projectSvc, Modules: moduleSvc, Now: fixedNow})

		if _, err := svc.ActivateModules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
			t.Fatalf("ActivateModules returned error: %v", err)
		}
		if len(moduleSvc.registers) != 0 {
			t.Fatalf("disabled module should not register package, got %#v", moduleSvc.registers)
		}
		if len(projectSvc.moduleUpserts) != 1 || projectSvc.moduleUpserts[0].ActivationStatus != projects.ProjectModuleRegistrationStatusDisabled {
			t.Fatalf("expected disabled module registration, got %#v", projectSvc.moduleUpserts)
		}
	})

	t.Run("removed module becomes stale", func(t *testing.T) {
		root, hash := writeModuleProject(t)
		if err := os.RemoveAll(filepath.Join(root, "modules", "example_module")); err != nil {
			t.Fatalf("remove module package: %v", err)
		}
		previous := projectModuleRegistration("example_module", projects.ProjectModuleRegistrationStatusRegistered)
		projectSvc := &fakeProjectService{detail: registeredModuleProjectDetail(t, root, hash, []projects.ProjectModuleRegistration{previous})}
		moduleSvc := &fakeModuleService{}
		svc := NewService(Deps{Projects: projectSvc, Modules: moduleSvc, Now: fixedNow})

		if _, err := svc.ActivateModules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{}); err != nil {
			t.Fatalf("ActivateModules returned error: %v", err)
		}
		if len(moduleSvc.registers) != 0 {
			t.Fatalf("removed module should not register package, got %#v", moduleSvc.registers)
		}
		if len(projectSvc.moduleUpserts) != 1 || projectSvc.moduleUpserts[0].ActivationStatus != projects.ProjectModuleRegistrationStatusStale {
			t.Fatalf("expected stale module registration, got %#v", projectSvc.moduleUpserts)
		}
		if !projectSvc.marked {
			t.Fatal("expected modules facet to be marked activated")
		}
	})
}

func TestActivateModulesWrapsRegistrationErrors(t *testing.T) {
	root, hash := writeModuleProject(t)
	projectSvc := &fakeProjectService{detail: registeredModuleProjectDetail(t, root, hash, nil)}
	moduleSvc := &fakeModuleService{err: fmt.Errorf("manifest rejected")}
	svc := NewService(Deps{
		Projects: projectSvc,
		Modules:  moduleSvc,
		Now:      fixedNow,
	})

	_, err := svc.ActivateModules(context.Background(), activationRequest(), "script-smoke", projects.ActivateProjectInput{})
	if !errors.Is(err, ErrModuleRegistrationUnavailable) {
		t.Fatalf("ActivateModules error = %v, want %v", err, ErrModuleRegistrationUnavailable)
	}
	if !strings.Contains(err.Error(), "example_module") {
		t.Fatalf("ActivateModules error should include module key, got %v", err)
	}
}

func TestDeactivateScriptsDisablesEndpointRuntimeAndRegistration(t *testing.T) {
	root, hash := writeProject(t, true)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsFacet(), []projects.ProjectScriptExposure{projectScriptExposure("hello_world", projects.ProjectScriptExposureStatusActive)})}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{Projects: projectSvc, Capabilities: capSvc, Now: fixedNow})

	result, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "scripts", Reason: "test disable"})
	if err != nil {
		t.Fatalf("Deactivate scripts returned error: %v", err)
	}
	if !result.Changed || len(result.Actions) != 1 || result.Actions[0].Status != "disabled" {
		t.Fatalf("unexpected deactivation result: %#v", result)
	}
	if len(capSvc.disabledEndpoints) != 1 || capSvc.disabledEndpoints[0] != "capability_endpoint_hello_world" {
		t.Fatalf("disabled endpoints = %#v", capSvc.disabledEndpoints)
	}
	if len(capSvc.disabledBindings) != 1 || capSvc.disabledBindings[0] != "capability_runtime_binding_hello_world" {
		t.Fatalf("disabled runtime bindings = %#v", capSvc.disabledBindings)
	}
	if projectSvc.deactivatedFacet != "scripts" || strings.Join(projectSvc.deactivatedRows, ",") != "hello_world" {
		t.Fatalf("project deactivation markers facet=%q rows=%#v", projectSvc.deactivatedFacet, projectSvc.deactivatedRows)
	}
}

func TestDeactivateScriptsIsIdempotentWhenAlreadyDisabled(t *testing.T) {
	root, hash := writeProject(t, true)
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, root, hash, scriptsFacet(), []projects.ProjectScriptExposure{projectScriptExposure("hello_world", projects.ProjectScriptExposureStatusDisabled)})}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{Projects: projectSvc, Capabilities: capSvc, Now: fixedNow})

	result, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "scripts"})
	if err != nil {
		t.Fatalf("Deactivate scripts returned error: %v", err)
	}
	if result.Changed {
		t.Fatalf("already disabled script should not report changed: %#v", result)
	}
	if len(result.Actions) != 1 || result.Actions[0].Status != "already_disabled" {
		t.Fatalf("expected already_disabled action: %#v", result.Actions)
	}
	if len(capSvc.disabledEndpoints) != 0 || len(capSvc.disabledBindings) != 0 {
		t.Fatalf("already disabled script should not call disable endpoints=%#v bindings=%#v", capSvc.disabledEndpoints, capSvc.disabledBindings)
	}
}

func TestDeactivateSchedulesAndDirectEventsDisableBackendSurfaces(t *testing.T) {
	root, hash := writeScheduleProject(t)

	t.Run("schedules", func(t *testing.T) {
		projectSvc := &fakeProjectService{detail: registeredScheduleProjectDetail(t, root, hash, []projects.ProjectScheduleRegistration{projectScheduleRegistration("daily_summary", projects.ProjectScheduleRegistrationStatusPaused)})}
		automationSvc := &fakeAutomationService{}
		svc := NewService(Deps{Projects: projectSvc, Automation: automationSvc, Now: fixedNow})

		result, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "schedules"})
		if err != nil {
			t.Fatalf("Deactivate schedules returned error: %v", err)
		}
		if !result.Changed || len(automationSvc.disabled) != 1 || automationSvc.disabled[0] != "schedule_daily_summary" {
			t.Fatalf("schedule deactivation mismatch result=%#v disabled=%#v", result, automationSvc.disabled)
		}
		if projectSvc.deactivatedFacet != "schedules" || strings.Join(projectSvc.deactivatedRows, ",") != "daily_summary" {
			t.Fatalf("project deactivation markers facet=%q rows=%#v", projectSvc.deactivatedFacet, projectSvc.deactivatedRows)
		}
	})

	t.Run("direct events", func(t *testing.T) {
		eventRoot, eventHash := writeDirectEventProject(t)
		projectSvc := &fakeProjectService{detail: registeredDirectEventProjectDetail(t, eventRoot, eventHash, []projects.ProjectDirectEventRegistration{projectDirectEventRegistration("gmail_message", projects.ProjectDirectEventRegistrationStatusPaused)})}
		automationSvc := &fakeAutomationService{}
		svc := NewService(Deps{Projects: projectSvc, Automation: automationSvc, Now: fixedNow})

		result, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "direct-events"})
		if err != nil {
			t.Fatalf("Deactivate direct events returned error: %v", err)
		}
		if !result.Changed || len(automationSvc.disabledEndpoints) != 1 || automationSvc.disabledEndpoints[0] != "direct_event_endpoint_gmail_message" {
			t.Fatalf("direct-event deactivation mismatch result=%#v disabled=%#v", result, automationSvc.disabledEndpoints)
		}
		if projectSvc.deactivatedFacet != "direct_events" || strings.Join(projectSvc.deactivatedRows, ",") != "gmail_message" {
			t.Fatalf("project deactivation markers facet=%q rows=%#v", projectSvc.deactivatedFacet, projectSvc.deactivatedRows)
		}
	})
}

func TestDeactivateConnectorsUsesProjectOwnedEndpointMetadata(t *testing.T) {
	root, hash := writeConnectorProject(t)
	registration := projects.ProjectConnectorRegistration{
		ConnectorKey:          "example_connector",
		ProviderAddress:       "main@example_connector",
		ActivationStatus:      projects.ProjectConnectorRegistrationStatusActive,
		ActiveCapabilityCount: 1,
		Metadata:              json.RawMessage(`{"endpoints":[{"capability_address":"main@example_connector.ping","capability_endpoint_id":"capability_endpoint_ping","runtime_binding_id":"runtime_binding_ping"}]}`),
	}
	projectSvc := &fakeProjectService{detail: registeredConnectorProjectDetail(t, root, hash, []projects.ProjectConnectorRegistration{registration})}
	capSvc := &fakeCapabilityService{}
	svc := NewService(Deps{Projects: projectSvc, Capabilities: capSvc, Now: fixedNow})

	result, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "connectors"})
	if err != nil {
		t.Fatalf("Deactivate connectors returned error: %v", err)
	}
	if !result.Changed || len(capSvc.disabledEndpoints) != 1 || capSvc.disabledEndpoints[0] != "capability_endpoint_ping" {
		t.Fatalf("connector endpoint disable mismatch result=%#v endpoints=%#v", result, capSvc.disabledEndpoints)
	}
	if len(capSvc.disabledBindings) != 1 || capSvc.disabledBindings[0] != "runtime_binding_ping" {
		t.Fatalf("connector runtime binding disable mismatch: %#v", capSvc.disabledBindings)
	}
	if projectSvc.deactivatedFacet != "connectors" || strings.Join(projectSvc.deactivatedRows, ",") != "example_connector" {
		t.Fatalf("project deactivation markers facet=%q rows=%#v", projectSvc.deactivatedFacet, projectSvc.deactivatedRows)
	}
}

func TestDeactivateModulesAndWorkflowsAreNonDestructive(t *testing.T) {
	root, hash := writeModuleProject(t)
	projectSvc := &fakeProjectService{detail: registeredModuleProjectDetail(t, root, hash, []projects.ProjectModuleRegistration{projectModuleRegistration("example_module", projects.ProjectModuleRegistrationStatusRegistered)})}
	svc := NewService(Deps{Projects: projectSvc, Modules: &fakeModuleService{}, Now: fixedNow})

	result, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "modules"})
	if err != nil {
		t.Fatalf("Deactivate modules returned error: %v", err)
	}
	if !result.Changed || strings.Join(projectSvc.deactivatedRows, ",") != "example_module" || projectSvc.deactivatedFacet != "modules" {
		t.Fatalf("module deactivation should mark project registration only: result=%#v facet=%q rows=%#v", result, projectSvc.deactivatedFacet, projectSvc.deactivatedRows)
	}

	workflowRegistration := projects.ProjectWorkflowRegistration{
		WorkflowKey:       "example_workflow",
		CapabilityAddress: "main@script-smoke.example_workflow",
		ActivationStatus:  projects.ProjectWorkflowRegistrationStatusActive,
		RuntimeKind:       capabilities.RuntimeKindWorkflow,
	}
	workflowDetail := registeredWorkflowProjectDetail(t, root, hash, nil)
	workflowDetail.WorkflowRegistrations = []projects.ProjectWorkflowRegistration{workflowRegistration}
	workflowProjectSvc := &fakeProjectService{detail: workflowDetail}
	workflowCapSvc := &fakeCapabilityService{}
	workflowSvc := NewService(Deps{Projects: workflowProjectSvc, Capabilities: workflowCapSvc, Now: fixedNow})
	workflowResult, err := workflowSvc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "workflows"})
	if err != nil {
		t.Fatalf("Deactivate workflows returned error: %v", err)
	}
	if !workflowResult.Changed || workflowProjectSvc.deactivatedFacet != "workflows" || strings.Join(workflowProjectSvc.deactivatedRows, ",") != "example_workflow" {
		t.Fatalf("workflow deactivation should disable workflow registration: result=%#v facet=%q rows=%#v", workflowResult, workflowProjectSvc.deactivatedFacet, workflowProjectSvc.deactivatedRows)
	}
}

func TestDeactivateWatchedRootsSupportsDryRunWithoutMutating(t *testing.T) {
	root, hash := writeProject(t, false)
	detail := registeredProjectDetail(t, root, hash, []projects.ProjectContractFacet{{FacetKey: "sync_policy", Enabled: true, Present: true}}, nil)
	detail.WatchedRootRegistrations = []projects.ProjectWatchedRootRegistration{{
		LocalRootKey:     "script_smoke__notes",
		BackendRootKey:   "script_smoke__notes",
		ActivationStatus: projects.ProjectWatchedRootRegistrationStatusPendingAgentApply,
	}}
	projectSvc := &fakeProjectService{detail: detail}
	svc := NewService(Deps{Projects: projectSvc, Now: fixedNow})

	result, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "watched-roots", DryRun: true})
	if err != nil {
		t.Fatalf("Deactivate watched roots dry-run returned error: %v", err)
	}
	if !result.Changed || !result.DryRun || result.Actions[0].Status != "would_disable" {
		t.Fatalf("unexpected watched-root dry-run result: %#v", result)
	}
	if !strings.Contains(result.Actions[0].Summary, "loom-node-agent watched-roots disable script_smoke__notes") || !strings.Contains(result.Actions[0].Summary, "preserving evidence") {
		t.Fatalf("watched-root dry-run omitted supported owner-node disable action: %#v", result.Actions[0])
	}
	if projectSvc.deactivatedFacet != "" || len(projectSvc.deactivatedRows) != 0 {
		t.Fatalf("dry-run should not mark project rows facet=%q rows=%#v", projectSvc.deactivatedFacet, projectSvc.deactivatedRows)
	}
}

func TestDeactivateServicesPagesEveryProvider(t *testing.T) {
	items := make([]capabilities.ProviderListItem, 205)
	records := make([]serviceregistry.AllowlistRecord, 205)
	for index := range items {
		key := fmt.Sprintf("service_%03d", index)
		providerID := ids.NewProviderID()
		profile, err := serviceregistry.BuildRuntimeProfile(serviceregistry.RuntimeProfileInput{
			Manager: serviceregistry.ManagerSystemd, Unit: fmt.Sprintf("loom-service-%03d.service", index), ServiceClass: serviceregistry.ServiceClassProject,
			Operations: []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStop},
		})
		if err != nil {
			t.Fatal(err)
		}
		profileJSON, _ := json.Marshal(profile)
		profileDigest := sha256.Sum256(profileJSON)
		items[index] = capabilities.ProviderListItem{
			Provider: capabilities.Provider{
				ProviderID:         providerID,
				ProviderKey:        key,
				CompactAddress:     "macbook@" + key,
				DisplayName:        fmt.Sprintf("Service %03d", index),
				ProviderType:       capabilities.ProviderTypeService,
				NodeID:             "node_main",
				ScopeID:            "scope_project",
				Version:            "0.1.0",
				Status:             capabilities.ProviderStatusActive,
				RuntimeProfileJSON: profileJSON,
			},
		}
		records[index] = serviceregistry.AllowlistRecord{
			SchemaVersion: serviceregistry.AllowlistSchemaV1, Key: key, NodeKey: "macbook", Manager: profile.Manager, Unit: profile.Unit,
			Operations: []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStop}, Health: serviceregistry.AllowlistHealth{Kind: serviceregistry.HealthKindManager},
			ProjectArchiveIdentity: &serviceregistry.ProjectArchiveServiceIdentity{
				ProviderKey: key, ProviderAddress: "macbook@" + key, ProviderID: providerID, RuntimeProfileDigest: fmt.Sprintf("sha256:%x", profileDigest[:]),
			},
		}
	}
	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry_run_%t", dryRun), func(t *testing.T) {
			projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, "unused", "sha256:"+fmt.Sprintf("%064x", 1), []projects.ProjectContractFacet{{FacetKey: "services", Enabled: true, Present: true}}, nil)}
			capabilitySvc := &fakeCapabilityService{providerItems: items}
			svc := NewService(Deps{Projects: projectSvc, Capabilities: capabilitySvc, Allowlists: serviceregistry.StaticAllowlistResolver{Allowlist: serviceregistry.Allowlist{Records: records}}})
			result, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "services", DryRun: dryRun})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Actions) != len(items) || len(capabilitySvc.providerFilters) != 2 {
				t.Fatalf("actions=%d filters=%#v", len(result.Actions), capabilitySvc.providerFilters)
			}
			if capabilitySvc.providerFilters[0].Offset != 0 || capabilitySvc.providerFilters[1].Offset != 200 {
				t.Fatalf("page offsets = %#v", capabilitySvc.providerFilters)
			}
			if dryRun {
				if len(capabilitySvc.providers) != 0 || projectSvc.deactivatedFacet != "" {
					t.Fatalf("dry-run mutated providers=%d facet=%q", len(capabilitySvc.providers), projectSvc.deactivatedFacet)
				}
			} else if len(capabilitySvc.providers) != len(items) || projectSvc.deactivatedFacet != "services" {
				t.Fatalf("apply providers=%d facet=%q", len(capabilitySvc.providers), projectSvc.deactivatedFacet)
			}
		})
	}
}

func TestDeactivateServicesSealsExactArchiveIdentityOnPlanAndApply(t *testing.T) {
	item, record := projectArchiveServiceTargetFixture(t, "service_one", capabilities.ProviderStatusActive)
	capabilitySvc := &fakeCapabilityService{providerItems: []capabilities.ProviderListItem{item}}
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, "unused", "sha256:"+fmt.Sprintf("%064x", 1), []projects.ProjectContractFacet{{FacetKey: "services", Enabled: true, Present: true}}, nil)}
	svc := NewService(Deps{
		Projects: projectSvc, Capabilities: capabilitySvc,
		Allowlists: serviceregistry.StaticAllowlistResolver{Allowlist: serviceregistry.Allowlist{Records: []serviceregistry.AllowlistRecord{record}}},
	})
	dryRun, err := svc.DeactivateForPhysicalArchive(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "services", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := svc.DeactivateForPhysicalArchive(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "services"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dryRun.Actions) != 1 || len(applied.Actions) != 1 || dryRun.Actions[0].Status != "would_disable" || applied.Actions[0].Status != "disabled" {
		t.Fatalf("unexpected service actions: dry=%#v apply=%#v", dryRun.Actions, applied.Actions)
	}
	if string(dryRun.Actions[0].Metadata) != string(applied.Actions[0].Metadata) {
		t.Fatalf("dry-run/apply metadata drifted: dry=%s apply=%s", dryRun.Actions[0].Metadata, applied.Actions[0].Metadata)
	}
	metadata, err := DecodeProjectArchiveServiceActionMetadata(dryRun.Actions[0].Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.OwnerNode != record.NodeKey || metadata.ProviderID != item.ProviderID || metadata.ProviderAddress != item.CompactAddress ||
		metadata.AllowlistKey != record.Key || metadata.Manager != string(record.Manager) || metadata.Unit != record.Unit ||
		metadata.RuntimeProfileDigest != record.ProjectArchiveIdentity.RuntimeProfileDigest {
		t.Fatalf("sealed service identity = %#v", metadata)
	}
}

func TestDeactivateServicesOrdinaryPathDoesNotRequireArchiveIdentity(t *testing.T) {
	item, _ := projectArchiveServiceTargetFixture(t, "service_one", capabilities.ProviderStatusActive)
	capabilitySvc := &fakeCapabilityService{providerItems: []capabilities.ProviderListItem{item}}
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, "unused", "sha256:"+fmt.Sprintf("%064x", 1), []projects.ProjectContractFacet{{FacetKey: "services", Enabled: true, Present: true}}, nil)}
	svc := NewService(Deps{Projects: projectSvc, Capabilities: capabilitySvc})

	dryRun, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "services", Reason: "project archive", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "services", Reason: "project archive"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dryRun.Actions) != 1 || len(applied.Actions) != 1 || string(dryRun.Actions[0].Metadata) != `{}` || string(applied.Actions[0].Metadata) != `{}` {
		t.Fatalf("ordinary service deactivation acquired archive metadata: dry=%#v apply=%#v", dryRun.Actions, applied.Actions)
	}
	if len(capabilitySvc.providers) != 1 || projectSvc.deactivatedFacet != "services" {
		t.Fatalf("ordinary apply did not deactivate services: providers=%d facet=%q", len(capabilitySvc.providers), projectSvc.deactivatedFacet)
	}
}

func TestDeactivateServicesRejectsProviderAndAllowlistIdentitySubstitution(t *testing.T) {
	tests := map[string]func(*capabilities.ProviderListItem, *serviceregistry.AllowlistRecord){
		"missing allowlist identity": func(_ *capabilities.ProviderListItem, record *serviceregistry.AllowlistRecord) {
			record.ProjectArchiveIdentity = nil
		},
		"provider inspection": func(item *capabilities.ProviderListItem, _ *serviceregistry.AllowlistRecord) {
			item.ProviderID = ids.NewProviderID()
		},
		"allowlist provider id": func(_ *capabilities.ProviderListItem, record *serviceregistry.AllowlistRecord) {
			record.ProjectArchiveIdentity.ProviderID = ids.NewProviderID()
		},
		"allowlist runtime digest": func(_ *capabilities.ProviderListItem, record *serviceregistry.AllowlistRecord) {
			record.ProjectArchiveIdentity.RuntimeProfileDigest = "sha256:" + strings.Repeat("f", 64)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			item, record := projectArchiveServiceTargetFixture(t, "service_one", capabilities.ProviderStatusActive)
			inspectionItem := item
			mutate(&inspectionItem, &record)
			capabilitySvc := &fakeCapabilityService{
				providerItems:       []capabilities.ProviderListItem{item},
				providerInspections: map[string]capabilities.ProviderInspection{item.ProviderID: {Provider: inspectionItem.Provider}},
			}
			projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, "unused", "sha256:"+fmt.Sprintf("%064x", 1), []projects.ProjectContractFacet{{FacetKey: "services", Enabled: true, Present: true}}, nil)}
			svc := NewService(Deps{
				Projects: projectSvc, Capabilities: capabilitySvc,
				Allowlists: serviceregistry.StaticAllowlistResolver{Allowlist: serviceregistry.Allowlist{Records: []serviceregistry.AllowlistRecord{record}}},
			})
			if _, err := svc.DeactivateForPhysicalArchive(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "services", DryRun: true}); err == nil {
				t.Fatal("substituted service identity was accepted")
			}
			if len(capabilitySvc.providers) != 0 || projectSvc.deactivatedFacet != "" {
				t.Fatal("service identity failure mutated registry state")
			}
		})
	}
}

func TestDeactivateServicesArchivePreflightsEveryIdentityBeforeMutation(t *testing.T) {
	firstItem, firstRecord := projectArchiveServiceTargetFixture(t, "service_one", capabilities.ProviderStatusActive)
	secondItem, secondRecord := projectArchiveServiceTargetFixture(t, "service_two", capabilities.ProviderStatusActive)
	secondRecord.ProjectArchiveIdentity = nil
	capabilitySvc := &fakeCapabilityService{providerItems: []capabilities.ProviderListItem{firstItem, secondItem}}
	projectSvc := &fakeProjectService{detail: registeredProjectDetail(t, "unused", "sha256:"+fmt.Sprintf("%064x", 1), []projects.ProjectContractFacet{{FacetKey: "services", Enabled: true, Present: true}}, nil)}
	svc := NewService(Deps{
		Projects: projectSvc, Capabilities: capabilitySvc,
		Allowlists: serviceregistry.StaticAllowlistResolver{Allowlist: serviceregistry.Allowlist{Records: []serviceregistry.AllowlistRecord{firstRecord, secondRecord}}},
	})

	if _, err := svc.DeactivateForPhysicalArchive(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "services"}); err == nil {
		t.Fatal("later missing archive identity was accepted")
	}
	if len(capabilitySvc.providers) != 0 || len(capabilitySvc.disabledEndpoints) != 0 || len(capabilitySvc.disabledBindings) != 0 || projectSvc.deactivatedFacet != "" {
		t.Fatalf("archive service mutation occurred before all identities passed: providers=%d endpoints=%d bindings=%d facet=%q", len(capabilitySvc.providers), len(capabilitySvc.disabledEndpoints), len(capabilitySvc.disabledBindings), projectSvc.deactivatedFacet)
	}
}

func projectArchiveServiceTargetFixture(t *testing.T, key, status string) (capabilities.ProviderListItem, serviceregistry.AllowlistRecord) {
	t.Helper()
	providerID := ids.NewProviderID()
	profile, err := serviceregistry.BuildRuntimeProfile(serviceregistry.RuntimeProfileInput{
		Manager: serviceregistry.ManagerSystemd, Unit: "loom-" + strings.ReplaceAll(key, "_", "-") + ".service", ServiceClass: serviceregistry.ServiceClassProject,
		Operations: []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStop},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(profile)
	digest := sha256.Sum256(raw)
	item := capabilities.ProviderListItem{Provider: capabilities.Provider{
		ProviderID: providerID, ProviderKey: key, CompactAddress: "main@" + key, ProviderType: capabilities.ProviderTypeService,
		NodeID: "node_main", ScopeID: "scope_project", Version: "1.0.0", Status: status, RuntimeProfileJSON: raw,
	}}
	record := serviceregistry.AllowlistRecord{
		SchemaVersion: serviceregistry.AllowlistSchemaV1, Key: key, NodeKey: "main", Manager: profile.Manager, Unit: profile.Unit,
		Operations: []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStop}, Health: serviceregistry.AllowlistHealth{Kind: serviceregistry.HealthKindManager},
		ProjectArchiveIdentity: &serviceregistry.ProjectArchiveServiceIdentity{
			ProviderKey: key, ProviderAddress: item.CompactAddress, ProviderID: providerID, RuntimeProfileDigest: fmt.Sprintf("sha256:%x", digest[:]),
		},
	}
	return item, record
}

func TestDeactivateWatchedRootsDisablesOwnerNotesFacet(t *testing.T) {
	root, hash := writeProject(t, false)
	detail := registeredProjectDetail(t, root, hash, notesFacet(projects.ProjectFacetStatusActivated), nil)
	detail.WatchedRootRegistrations = []projects.ProjectWatchedRootRegistration{{
		LocalRootKey:     "notes",
		BackendRootKey:   "script_smoke__notes",
		SourceKinds:      json.RawMessage(`["notes_contract"]`),
		RootRelativePath: "notes",
		ActivationStatus: projects.ProjectWatchedRootRegistrationStatusApplied,
	}}
	projectSvc := &fakeProjectService{detail: detail}
	svc := NewService(Deps{Projects: projectSvc, Now: fixedNow})

	result, err := svc.Deactivate(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "watched-roots"})
	if err != nil {
		t.Fatalf("Deactivate watched roots returned error: %v", err)
	}
	if !result.Changed || len(result.Actions) != 2 {
		t.Fatalf("unexpected watched-root deactivation result: %#v", result)
	}
	if result.Actions[0].Kind != "watched_root" || result.Actions[0].Status != "disabled" {
		t.Fatalf("unexpected watched-root action: %#v", result.Actions[0])
	}
	if result.Actions[1].Kind != "project_facet" || result.Actions[1].Key != "notes" || result.Actions[1].Status != "disabled" {
		t.Fatalf("unexpected owner-facet action: %#v", result.Actions[1])
	}
	if strings.Join(projectSvc.deactivatedRows, ",") != "notes" {
		t.Fatalf("watched root rows = %#v, want notes", projectSvc.deactivatedRows)
	}
	if projectSvc.deactivatedFacet != "notes" || strings.Join(projectSvc.deactivatedFacets, ",") != "notes" {
		t.Fatalf("deactivated facets facet=%q facets=%#v", projectSvc.deactivatedFacet, projectSvc.deactivatedFacets)
	}
	if got := projectSvc.detail.Facets[0].FacetStatus; got != projects.ProjectFacetStatusDisabled {
		t.Fatalf("notes facet status = %q, want disabled", got)
	}
}

type fakeProjectService struct {
	detail             projects.ProjectRegistrationDetail
	baseCalled         bool
	upserts            []projects.UpsertProjectScriptExposureInput
	scheduleUpserts    []projects.UpsertProjectScheduleRegistrationInput
	directEventUpserts []projects.UpsertProjectDirectEventRegistrationInput
	connectorUpserts   []projects.UpsertProjectConnectorRegistrationInput
	moduleUpserts      []projects.UpsertProjectModuleRegistrationInput
	workflowUpserts    []projects.UpsertProjectWorkflowRegistrationInput
	marked             bool
	markedFacet        string
	markedMetadata     json.RawMessage
	deactivatedFacet   string
	deactivatedFacets  []string
	deactivatedRows    []string
}

type archiveLockingProjectService struct {
	fakeProjectService
	lockKeys []string
}

func (f *archiveLockingProjectService) AcquireProjectArchiveLocks(_ context.Context, keys []string) (func() error, error) {
	f.lockKeys = append([]string(nil), keys...)
	project := f.detail.Project.Project
	started := fixedNow()
	state := projects.ProjectPhysicalArchiveState{
		SchemaVersion:       projects.ProjectPhysicalArchiveStateSchemaVersion,
		Status:              projects.ProjectPhysicalArchiveStatusInProgress,
		Phase:               projects.ProjectArchivePhaseDeactivationPending,
		ProjectID:           project.ProjectID,
		ProjectSlug:         project.Slug,
		OperationID:         "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		PlanDigest:          "sha256:" + strings.Repeat("a", 64),
		WorkspacePlanDigest: "sha256:" + strings.Repeat("b", 64),
		ActivePath:          "/fixture/active",
		ArchivePath:         "/fixture/archive",
		ActorID:             "actor_test",
		Reason:              "archive project",
		StartedAt:           started,
		MutationBlocked:     true,
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	f.detail.Project.Project.ArchiveState = payload
	return func() error { return nil }, nil
}

func (f *fakeProjectService) GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error) {
	return f.detail, nil
}

func (f *fakeProjectService) ActivateProjectBase(context.Context, requestctx.Context, string) (projects.ProjectRegistrationDetail, error) {
	f.baseCalled = true
	if f.detail.Registration != nil {
		f.detail.Registration.ActivationStatus = projects.ProjectActivationStatusBaseActive
	}
	return f.detail, nil
}

func (f *fakeProjectService) UpsertProjectScriptExposure(_ context.Context, _ requestctx.Context, input projects.UpsertProjectScriptExposureInput) (projects.ProjectScriptExposure, error) {
	f.upserts = append(f.upserts, input)
	exposure := projects.ProjectScriptExposure{
		ProjectScriptExposureID: "project_script_exposure_test",
		ScriptKey:               input.ScriptKey,
		CapabilityAddress:       input.CapabilityAddress,
		ActivationStatus:        input.ActivationStatus,
	}
	f.detail.ScriptExposures = appendProjectScriptExposure(f.detail.ScriptExposures, exposure)
	return exposure, nil
}

func (f *fakeProjectService) UpsertProjectScheduleRegistration(_ context.Context, _ requestctx.Context, input projects.UpsertProjectScheduleRegistrationInput) (projects.ProjectScheduleRegistration, error) {
	f.scheduleUpserts = append(f.scheduleUpserts, input)
	registration := projects.ProjectScheduleRegistration{
		ProjectScheduleRegistrationID: "project_schedule_registration_test",
		ScheduleKey:                   input.ScheduleKey,
		BackendScheduleKey:            input.BackendScheduleKey,
		TargetCapability:              input.TargetCapability,
		ActivationStatus:              input.ActivationStatus,
	}
	f.detail.ScheduleRegistrations = appendProjectScheduleRegistration(f.detail.ScheduleRegistrations, registration)
	return registration, nil
}

func (f *fakeProjectService) UpsertProjectDirectEventRegistration(_ context.Context, _ requestctx.Context, input projects.UpsertProjectDirectEventRegistrationInput) (projects.ProjectDirectEventRegistration, error) {
	f.directEventUpserts = append(f.directEventUpserts, input)
	registration := projects.ProjectDirectEventRegistration{
		ProjectDirectEventRegistrationID: "project_direct_event_registration_test",
		EventKey:                         input.EventKey,
		BackendIntegrationKey:            input.BackendIntegrationKey,
		BackendEndpointSlug:              input.BackendEndpointSlug,
		TargetCapability:                 input.TargetCapability,
		ActivationStatus:                 input.ActivationStatus,
	}
	f.detail.DirectEventRegistrations = appendProjectDirectEventRegistration(f.detail.DirectEventRegistrations, registration)
	return registration, nil
}

func (f *fakeProjectService) UpsertProjectConnectorRegistration(_ context.Context, _ requestctx.Context, input projects.UpsertProjectConnectorRegistrationInput) (projects.ProjectConnectorRegistration, error) {
	f.connectorUpserts = append(f.connectorUpserts, input)
	providerID := input.ProviderID
	return projects.ProjectConnectorRegistration{
		ProjectConnectorRegistrationID: "project_connector_registration_test",
		ConnectorKey:                   input.ConnectorKey,
		ProviderKey:                    input.ProviderKey,
		ProviderAddress:                input.ProviderAddress,
		ProviderID:                     &providerID,
		RuntimeKind:                    input.RuntimeKind,
		CapabilityCount:                input.CapabilityCount,
		ActiveCapabilityCount:          input.ActiveCapabilityCount,
		UsageDocumentCount:             input.UsageDocumentCount,
		ActivationStatus:               input.ActivationStatus,
		Metadata:                       input.Metadata,
	}, nil
}

func (f *fakeProjectService) UpsertProjectModuleRegistration(_ context.Context, _ requestctx.Context, input projects.UpsertProjectModuleRegistrationInput) (projects.ProjectModuleRegistration, error) {
	f.moduleUpserts = append(f.moduleUpserts, input)
	packageID := input.ModulePackageID
	versionID := input.ModuleVersionID
	return projects.ProjectModuleRegistration{
		ProjectModuleRegistrationID: "project_module_registration_test",
		ModuleKey:                   input.ModuleKey,
		ModuleID:                    input.ModuleID,
		ModuleVersion:               input.ModuleVersion,
		ModulePackageID:             &packageID,
		ModuleVersionID:             &versionID,
		ProviderCount:               input.ProviderCount,
		CapabilityCount:             input.CapabilityCount,
		ActivationStatus:            input.ActivationStatus,
		Metadata:                    input.Metadata,
	}, nil
}

func (f *fakeProjectService) UpsertProjectWorkflowRegistration(_ context.Context, _ requestctx.Context, input projects.UpsertProjectWorkflowRegistrationInput) (projects.ProjectWorkflowRegistration, error) {
	f.workflowUpserts = append(f.workflowUpserts, input)
	workflowID := input.WorkflowID
	workflowVersionID := input.WorkflowVersionID
	providerID := input.ProviderID
	endpointID := input.CapabilityEndpointID
	endpointVersionID := input.CapabilityEndpointVersionID
	runtimeBindingID := input.RuntimeBindingID
	return projects.ProjectWorkflowRegistration{
		ProjectWorkflowRegistrationID: "project_workflow_registration_test",
		WorkflowKey:                   input.WorkflowKey,
		WorkflowID:                    &workflowID,
		WorkflowVersionID:             &workflowVersionID,
		ProviderID:                    &providerID,
		ProviderAddress:               input.ProviderAddress,
		CapabilityEndpointID:          &endpointID,
		CapabilityEndpointVersionID:   &endpointVersionID,
		RuntimeBindingID:              &runtimeBindingID,
		CapabilityAddress:             input.CapabilityAddress,
		ActivationStatus:              input.ActivationStatus,
		RuntimeKind:                   input.RuntimeKind,
		Metadata:                      input.Metadata,
	}, nil
}

func (f *fakeProjectService) MarkStaleProjectModuleRegistrations(context.Context, requestctx.Context, string, string, []string) error {
	return nil
}

func (f *fakeProjectService) MarkProjectFacetActivated(_ context.Context, _ requestctx.Context, _ string, _ string, facet string, metadata json.RawMessage) error {
	f.marked = true
	f.markedFacet = facet
	f.markedMetadata = metadata
	for idx := range f.detail.Facets {
		if f.detail.Facets[idx].FacetKey == facet {
			f.detail.Facets[idx].FacetStatus = projects.ProjectFacetStatusActivated
		}
	}
	return nil
}

func (f *fakeProjectService) MarkProjectFacetDeactivated(_ context.Context, _ requestctx.Context, _ string, _ string, facet string, metadata json.RawMessage) error {
	f.deactivatedFacet = facet
	f.deactivatedFacets = append(f.deactivatedFacets, facet)
	f.markedMetadata = metadata
	for idx := range f.detail.Facets {
		if f.detail.Facets[idx].FacetKey == facet {
			f.detail.Facets[idx].FacetStatus = projects.ProjectFacetStatusDisabled
		}
	}
	return nil
}

func (f *fakeProjectService) MarkProjectScriptExposuresDeactivated(_ context.Context, _ requestctx.Context, _ string, keys []string, _ json.RawMessage) error {
	f.deactivatedRows = append(f.deactivatedRows, keys...)
	return nil
}

func (f *fakeProjectService) MarkProjectScheduleRegistrationsDeactivated(_ context.Context, _ requestctx.Context, _ string, keys []string, _ json.RawMessage) error {
	f.deactivatedRows = append(f.deactivatedRows, keys...)
	return nil
}

func (f *fakeProjectService) MarkProjectDirectEventRegistrationsDeactivated(_ context.Context, _ requestctx.Context, _ string, keys []string, _ json.RawMessage) error {
	f.deactivatedRows = append(f.deactivatedRows, keys...)
	return nil
}

func (f *fakeProjectService) MarkProjectWatchedRootsDeactivated(_ context.Context, _ requestctx.Context, _ string, keys []string, _ json.RawMessage) error {
	f.deactivatedRows = append(f.deactivatedRows, keys...)
	return nil
}

func (f *fakeProjectService) MarkProjectConnectorRegistrationsDeactivated(_ context.Context, _ requestctx.Context, _ string, keys []string, _ json.RawMessage) error {
	f.deactivatedRows = append(f.deactivatedRows, keys...)
	return nil
}

func (f *fakeProjectService) MarkProjectModuleRegistrationsDeactivated(_ context.Context, _ requestctx.Context, _ string, keys []string, _ json.RawMessage) error {
	f.deactivatedRows = append(f.deactivatedRows, keys...)
	return nil
}

func (f *fakeProjectService) MarkProjectWorkflowRegistrationsDeactivated(_ context.Context, _ requestctx.Context, _ string, keys []string, _ json.RawMessage) error {
	f.deactivatedRows = append(f.deactivatedRows, keys...)
	return nil
}

func appendProjectScriptExposure(rows []projects.ProjectScriptExposure, row projects.ProjectScriptExposure) []projects.ProjectScriptExposure {
	for idx := range rows {
		if rows[idx].ScriptKey == row.ScriptKey {
			rows[idx] = row
			return rows
		}
	}
	return append(rows, row)
}

func appendProjectScheduleRegistration(rows []projects.ProjectScheduleRegistration, row projects.ProjectScheduleRegistration) []projects.ProjectScheduleRegistration {
	for idx := range rows {
		if rows[idx].ScheduleKey == row.ScheduleKey {
			rows[idx] = row
			return rows
		}
	}
	return append(rows, row)
}

func appendProjectDirectEventRegistration(rows []projects.ProjectDirectEventRegistration, row projects.ProjectDirectEventRegistration) []projects.ProjectDirectEventRegistration {
	for idx := range rows {
		if rows[idx].EventKey == row.EventKey {
			rows[idx] = row
			return rows
		}
	}
	return append(rows, row)
}

type fakeScriptService struct {
	registers []scripts.RegisterInput
}

func (f *fakeScriptService) RegisterScript(_ context.Context, _ requestctx.Context, input scripts.RegisterInput) (scripts.RegisterResult, error) {
	f.registers = append(f.registers, input)
	return scripts.RegisterResult{
		Script: scripts.Script{
			ScriptID: "script_test",
			Slug:     input.SlugOverride,
		},
		Version: scripts.ScriptVersion{
			ScriptVersionID: "script_version_test",
		},
	}, nil
}

type fakeWorkflowService struct {
	registers []workflows.RegisterInput
}

func (f *fakeWorkflowService) RegisterWorkflow(_ context.Context, _ requestctx.Context, input workflows.RegisterInput) (workflows.RegisterResult, error) {
	f.registers = append(f.registers, input)
	return workflows.RegisterResult{
		Workflow: workflows.Workflow{
			WorkflowID: "workflow_test",
			Slug:       input.SlugOverride,
		},
		Version: workflows.WorkflowVersion{
			WorkflowVersionID: "workflow_version_test",
			WorkflowID:        "workflow_test",
			VersionLabel:      "0.1.0",
			ContentHash:       "sha256:" + fmt.Sprintf("%064x", 5),
		},
		WorkflowCreated: true,
		VersionCreated:  true,
		Activated:       true,
	}, nil
}

type fakeModuleService struct {
	registers []modules.RegisterPackageInput
	err       error
}

func (f *fakeModuleService) RegisterPackage(_ context.Context, _ requestctx.Context, input modules.RegisterPackageInput) (modules.ModuleRegistration, error) {
	f.registers = append(f.registers, input)
	if f.err != nil {
		return modules.ModuleRegistration{}, f.err
	}
	return modules.ModuleRegistration{
		Package: modules.ModulePackage{
			ModulePackageID: "module_package_test",
			ContentHash:     "sha256:" + fmt.Sprintf("%064x", 3),
		},
		Version: modules.ModuleVersion{
			ModuleVersionID: "module_version_test",
			ModuleID:        "loom.script-smoke",
			ModuleName:      "Script Smoke Module",
			Version:         "0.1.0",
			ManifestHash:    "sha256:" + fmt.Sprintf("%064x", 4),
			ModuleKind:      modules.ModuleKindNative,
		},
		Providers:    []modules.ProviderDeclaration{{ProviderKey: "example_module"}},
		Capabilities: []modules.CapabilityDeclaration{{ProviderKey: "example_module", EndpointName: "ping"}},
		Registered:   true,
		Message:      "registered",
	}, nil
}

type fakeCapabilityService struct {
	providers           []capabilities.RegisterProviderInput
	providerItems       []capabilities.ProviderListItem
	providerInspections map[string]capabilities.ProviderInspection
	providerFilters     []capabilities.ProviderFilter
	classes             []capabilities.RegisterCapabilityClassInput
	endpoints           []capabilities.RegisterCapabilityEndpointInput
	versions            []capabilities.RegisterEndpointVersionInput
	bindings            []capabilities.RegisterRuntimeBindingInput
	usageDocs           []capabilities.RegisterUsageDocumentInput
	disabledEndpoints   []string
	disabledBindings    []string
}

func (f *fakeCapabilityService) EnsureProvider(_ context.Context, _ requestctx.Context, input capabilities.RegisterProviderInput) (capabilities.Provider, bool, error) {
	f.providers = append(f.providers, input)
	return capabilities.Provider{ProviderID: "provider_test", ProviderKey: input.ProviderKey, CompactAddress: input.CompactAddress, ProviderType: input.ProviderType, Status: input.Status}, true, nil
}

func (f *fakeCapabilityService) UpsertProviderHealth(context.Context, requestctx.Context, string, capabilities.ProviderHealthInput) (capabilities.ProviderHealth, error) {
	return capabilities.ProviderHealth{HealthStatus: capabilities.HealthStatusOK}, nil
}

func (f *fakeCapabilityService) ListProviders(_ context.Context, filter capabilities.ProviderFilter) ([]capabilities.ProviderListItem, error) {
	f.providerFilters = append(f.providerFilters, filter)
	start := filter.Offset
	if start >= len(f.providerItems) {
		return []capabilities.ProviderListItem{}, nil
	}
	end := start + filter.Limit
	if end > len(f.providerItems) {
		end = len(f.providerItems)
	}
	return append([]capabilities.ProviderListItem(nil), f.providerItems[start:end]...), nil
}

func (f *fakeCapabilityService) InspectProvider(_ context.Context, ref string) (capabilities.ProviderInspection, error) {
	if inspection, ok := f.providerInspections[ref]; ok {
		return inspection, nil
	}
	for _, item := range f.providerItems {
		if item.ProviderID == ref || item.ProviderKey == ref || item.CompactAddress == ref {
			return capabilities.ProviderInspection{Provider: item.Provider}, nil
		}
	}
	return capabilities.ProviderInspection{}, nil
}
func (f *fakeCapabilityService) InspectCapability(context.Context, string) (capabilities.CapabilityInspection, error) {
	return capabilities.CapabilityInspection{}, nil
}

func (f *fakeCapabilityService) EnsureCapabilityClass(_ context.Context, _ requestctx.Context, input capabilities.RegisterCapabilityClassInput) (capabilities.CapabilityClass, bool, error) {
	f.classes = append(f.classes, input)
	return capabilities.CapabilityClass{CapabilityClassID: "capability_class_test", Namespace: input.Namespace, Name: input.Name}, true, nil
}

func (f *fakeCapabilityService) EnsureCapabilityEndpoint(_ context.Context, _ requestctx.Context, input capabilities.RegisterCapabilityEndpointInput) (capabilities.CapabilityEndpoint, bool, error) {
	f.endpoints = append(f.endpoints, input)
	return capabilities.CapabilityEndpoint{CapabilityEndpointID: "capability_endpoint_test", CompactAddress: input.CompactAddress}, true, nil
}

func (f *fakeCapabilityService) EnsureEndpointVersion(_ context.Context, _ requestctx.Context, input capabilities.RegisterEndpointVersionInput) (capabilities.EndpointVersion, bool, error) {
	f.versions = append(f.versions, input)
	return capabilities.EndpointVersion{CapabilityEndpointVersionID: "capability_endpoint_version_test"}, true, nil
}

func (f *fakeCapabilityService) RegisterRuntimeBinding(_ context.Context, _ requestctx.Context, input capabilities.RegisterRuntimeBindingInput) (capabilities.RuntimeBindingInspection, error) {
	f.bindings = append(f.bindings, input)
	return capabilities.RuntimeBindingInspection{
		Binding: capabilities.EndpointRuntimeBinding{
			RuntimeBindingID: "capability_runtime_binding_test",
			RuntimeKind:      input.RuntimeKind,
			Status:           input.Status,
		},
	}, nil
}

func (f *fakeCapabilityService) EnsureUsageDocument(_ context.Context, _ requestctx.Context, input capabilities.RegisterUsageDocumentInput) (capabilities.UsageDocument, bool, error) {
	f.usageDocs = append(f.usageDocs, input)
	sourceRef := input.SourceRef
	return capabilities.UsageDocument{
		CapabilityUsageDocumentID: "usage_doc_test",
		TargetKind:                input.TargetKind,
		TargetID:                  input.TargetID,
		Title:                     input.Title,
		SourceKind:                input.SourceKind,
		SourceRef:                 &sourceRef,
	}, true, nil
}

func (f *fakeCapabilityService) DisableCapabilityEndpoint(_ context.Context, _ requestctx.Context, ref string, _ json.RawMessage) (capabilities.CapabilityEndpoint, error) {
	f.disabledEndpoints = append(f.disabledEndpoints, ref)
	return capabilities.CapabilityEndpoint{CapabilityEndpointID: ref, Status: capabilities.EndpointStatusDisabled}, nil
}

func (f *fakeCapabilityService) DisableRuntimeBinding(_ context.Context, _ requestctx.Context, ref string, _ json.RawMessage) (capabilities.EndpointRuntimeBinding, error) {
	f.disabledBindings = append(f.disabledBindings, ref)
	return capabilities.EndpointRuntimeBinding{RuntimeBindingID: ref, Status: capabilities.RuntimeBindingStatusDisabled}, nil
}

type fakeEventService struct {
	appended []events.AppendInput
}

func (f *fakeEventService) Append(_ context.Context, input events.AppendInput) (events.Event, error) {
	f.appended = append(f.appended, input)
	return events.Event{EventID: "event_test", EventType: input.EventType}, nil
}

type fakeAutomationService struct {
	ensures            []automation.CreateScheduleInput
	disabled           []string
	integrationEnsures []automation.CreateIntegrationInput
	authProfileEnsures []automation.CreateIntegrationAuthProfileInput
	endpointEnsures    []automation.CreateDirectEventEndpointInput
	disabledEndpoints  []string
	err                error
}

func (f *fakeAutomationService) EnsureSchedule(_ context.Context, _ requestctx.Context, input automation.CreateScheduleInput) (automation.ScheduleDetail, bool, error) {
	f.ensures = append(f.ensures, input)
	if f.err != nil {
		return automation.ScheduleDetail{}, false, f.err
	}
	return automation.ScheduleDetail{
		Automation: automation.Automation{
			AutomationID:  "automation_test",
			AutomationKey: input.ScheduleKey,
			Status:        automation.AutomationStatusPaused,
		},
		Schedule: automation.Schedule{
			ScheduleID:        "schedule_test",
			AutomationID:      "automation_test",
			ScheduleKey:       input.ScheduleKey,
			Status:            input.Status,
			ScheduleKind:      input.ScheduleKind,
			ScheduleExpr:      input.ScheduleExpr,
			TargetProfileJSON: json.RawMessage(`{}`),
		},
	}, true, nil
}

func (f *fakeAutomationService) DisableSchedule(_ context.Context, _ requestctx.Context, ref string, _ automation.UpdateScheduleStatusInput) (automation.ScheduleDetail, error) {
	f.disabled = append(f.disabled, ref)
	return automation.ScheduleDetail{Schedule: automation.Schedule{ScheduleID: ref, Status: automation.ScheduleStatusDisabled}}, nil
}

func (f *fakeAutomationService) EnsureIntegration(_ context.Context, _ requestctx.Context, input automation.CreateIntegrationInput) (automation.IntegrationDetail, bool, error) {
	f.integrationEnsures = append(f.integrationEnsures, input)
	if f.err != nil {
		return automation.IntegrationDetail{}, false, f.err
	}
	return automation.IntegrationDetail{
		Integration: automation.Integration{
			IntegrationID:  "integration_test",
			IntegrationKey: input.IntegrationKey,
			Status:         automation.IntegrationStatusActive,
		},
	}, true, nil
}

func (f *fakeAutomationService) EnsureIntegrationAuthProfile(_ context.Context, _ requestctx.Context, _ string, input automation.CreateIntegrationAuthProfileInput) (automation.IntegrationAuthProfile, bool, error) {
	f.authProfileEnsures = append(f.authProfileEnsures, input)
	if f.err != nil {
		return automation.IntegrationAuthProfile{}, false, f.err
	}
	return automation.IntegrationAuthProfile{
		AuthProfileID: "integration_auth_profile_test",
		AuthKind:      input.AuthKind,
		Status:        automation.IntegrationAuthStatusActive,
	}, true, nil
}

func (f *fakeAutomationService) EnsureDirectEventEndpoint(_ context.Context, _ requestctx.Context, input automation.CreateDirectEventEndpointInput) (automation.DirectEventEndpointDetail, bool, error) {
	f.endpointEnsures = append(f.endpointEnsures, input)
	if f.err != nil {
		return automation.DirectEventEndpointDetail{}, false, f.err
	}
	return automation.DirectEventEndpointDetail{
		Integration: automation.Integration{
			IntegrationID: "integration_test",
			Status:        automation.IntegrationStatusActive,
		},
		Automation: automation.Automation{
			AutomationID:  "automation_test",
			AutomationKey: input.EndpointSlug,
			Status:        automation.AutomationStatusPaused,
		},
		Endpoint: automation.DirectEventEndpoint{
			EndpointID:   "direct_event_endpoint_test",
			EndpointSlug: input.EndpointSlug,
			EndpointPath: "/v1/direct-events/ingest/" + input.EndpointSlug,
			Status:       input.Status,
			EventType:    input.EventType,
		},
	}, true, nil
}

func (f *fakeAutomationService) DisableDirectEventEndpoint(_ context.Context, _ requestctx.Context, ref string, _ automation.UpdateDirectEventEndpointStatusInput) (automation.DirectEventEndpointDetail, error) {
	f.disabledEndpoints = append(f.disabledEndpoints, ref)
	return automation.DirectEventEndpointDetail{Endpoint: automation.DirectEventEndpoint{EndpointID: ref, Status: automation.DirectEventEndpointStatusDisabled}}, nil
}

func activationRequest() requestctx.Context {
	return requestctx.Context{
		ActorID:      "actor_test",
		OriginNodeID: "node_main",
	}
}

func registeredProjectDetail(t *testing.T, root, hash string, facets []projects.ProjectContractFacet, exposures []projects.ProjectScriptExposure, homeNodeOverride ...string) projects.ProjectRegistrationDetail {
	t.Helper()
	homeNodeID := "node_main"
	if len(homeNodeOverride) > 0 {
		homeNodeID = homeNodeOverride[0]
	}
	if facets == nil {
		facets = scriptsFacet()
	}
	return projects.ProjectRegistrationDetail{
		Project: projectDetail(homeNodeID),
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_test",
			ProjectID:                     "project_test",
			ProjectRoot:                   root,
			ContractPath:                  filepath.Join(root, "loom.project.yaml"),
			ContractHash:                  hash,
			ActivationStatus:              projects.ProjectActivationStatusInactive,
		},
		Facets:          facets,
		ScriptExposures: exposures,
	}
}

func registeredScheduleProjectDetail(t *testing.T, root, hash string, schedules []projects.ProjectScheduleRegistration) projects.ProjectRegistrationDetail {
	t.Helper()
	detail := registeredProjectDetail(t, root, hash, schedulesFacet(), nil)
	detail.ScheduleRegistrations = schedules
	return detail
}

func registeredDirectEventProjectDetail(t *testing.T, root, hash string, registrations []projects.ProjectDirectEventRegistration) projects.ProjectRegistrationDetail {
	t.Helper()
	detail := registeredProjectDetail(t, root, hash, directEventsFacet(), nil)
	detail.DirectEventRegistrations = registrations
	return detail
}

func registeredConnectorProjectDetail(t *testing.T, root, hash string, registrations []projects.ProjectConnectorRegistration) projects.ProjectRegistrationDetail {
	t.Helper()
	detail := registeredProjectDetail(t, root, hash, connectorsFacet(), nil)
	detail.ConnectorRegistrations = registrations
	return detail
}

func registeredModuleProjectDetail(t *testing.T, root, hash string, registrations []projects.ProjectModuleRegistration) projects.ProjectRegistrationDetail {
	t.Helper()
	detail := registeredProjectDetail(t, root, hash, modulesFacet(), nil)
	detail.ModuleRegistrations = registrations
	return detail
}

func registeredWorkflowProjectDetail(t *testing.T, root, hash string, exposures []projects.ProjectScriptExposure) projects.ProjectRegistrationDetail {
	t.Helper()
	return registeredProjectDetail(t, root, hash, workflowsFacet(), exposures)
}

func projectDetail(homeNodeID string) projects.ProjectDetail {
	return projects.ProjectDetail{
		Project: projects.Project{
			ProjectID:       "project_test",
			ProjectScopeID:  "scope_test",
			ProjectScopeKey: "project/script-smoke",
			Slug:            "script-smoke",
			Name:            "Script Smoke",
			HomeNodeID:      &homeNodeID,
		},
	}
}

func scriptsFacet() []projects.ProjectContractFacet {
	return []projects.ProjectContractFacet{{
		FacetKey:    "scripts",
		Enabled:     true,
		Present:     true,
		FacetStatus: projects.ProjectFacetStatusPendingLaterSlice,
	}}
}

func workflowsFacet() []projects.ProjectContractFacet {
	return []projects.ProjectContractFacet{{
		FacetKey:    "workflows",
		Enabled:     true,
		Present:     true,
		Placeholder: true,
		FacetStatus: projects.ProjectFacetStatusPlaceholder,
	}}
}

func schedulesFacet() []projects.ProjectContractFacet {
	return []projects.ProjectContractFacet{{
		FacetKey:    "schedules",
		Enabled:     true,
		Present:     true,
		FacetStatus: projects.ProjectFacetStatusPendingLaterSlice,
	}}
}

func scriptsAndSchedulesFacets() []projects.ProjectContractFacet {
	return []projects.ProjectContractFacet{
		{
			FacetKey:    "scripts",
			Enabled:     true,
			Present:     true,
			FacetStatus: projects.ProjectFacetStatusPendingLaterSlice,
		},
		{
			FacetKey:    "schedules",
			Enabled:     true,
			Present:     true,
			FacetStatus: projects.ProjectFacetStatusPendingLaterSlice,
		},
	}
}

func scriptsAndDirectEventsFacets() []projects.ProjectContractFacet {
	return []projects.ProjectContractFacet{
		{
			FacetKey:    "scripts",
			Enabled:     true,
			Present:     true,
			FacetStatus: projects.ProjectFacetStatusPendingLaterSlice,
		},
		{
			FacetKey:    "direct_events",
			Enabled:     true,
			Present:     true,
			FacetStatus: projects.ProjectFacetStatusPendingLaterSlice,
		},
	}
}

func directEventsFacet() []projects.ProjectContractFacet {
	return []projects.ProjectContractFacet{{
		FacetKey:    "direct_events",
		Enabled:     true,
		Present:     true,
		FacetStatus: projects.ProjectFacetStatusPendingLaterSlice,
	}}
}

func connectorsFacet() []projects.ProjectContractFacet {
	return []projects.ProjectContractFacet{{
		FacetKey:    "connectors",
		Enabled:     true,
		Present:     true,
		FacetStatus: projects.ProjectFacetStatusPendingLaterSlice,
	}}
}

func modulesFacet() []projects.ProjectContractFacet {
	return []projects.ProjectContractFacet{{
		FacetKey:    "modules",
		Enabled:     true,
		Present:     true,
		FacetStatus: projects.ProjectFacetStatusPendingLaterSlice,
	}}
}

func notesFacet(status string) []projects.ProjectContractFacet {
	return []projects.ProjectContractFacet{{
		FacetKey:    "notes",
		Enabled:     true,
		Present:     true,
		FacetStatus: status,
	}}
}

func projectScriptExposure(scriptKey, status string) projects.ProjectScriptExposure {
	endpointID := "capability_endpoint_" + scriptKey
	versionID := "capability_endpoint_version_" + scriptKey
	bindingID := "capability_runtime_binding_" + scriptKey
	scriptID := "script_" + scriptKey
	scriptVersionID := "script_version_" + scriptKey
	return projects.ProjectScriptExposure{
		ProjectScriptExposureID:       "project_script_exposure_" + scriptKey,
		ProjectContractRegistrationID: "project_contract_registration_test",
		ProjectID:                     "project_test",
		ScriptKey:                     scriptKey,
		ScriptFolder:                  "scripts/" + scriptKey,
		ScriptManifestPath:            "scripts/" + scriptKey + "/loom.script.yaml",
		ExposureEnabled:               true,
		ScriptID:                      &scriptID,
		ScriptVersionID:               &scriptVersionID,
		CapabilityEndpointID:          &endpointID,
		CapabilityEndpointVersionID:   &versionID,
		RuntimeBindingID:              &bindingID,
		CapabilityAddress:             "main@script-smoke." + scriptKey,
		ActivationStatus:              status,
	}
}

func projectScheduleRegistration(scheduleKey, status string) projects.ProjectScheduleRegistration {
	automationID := "automation_" + scheduleKey
	scheduleID := "schedule_" + scheduleKey
	return projects.ProjectScheduleRegistration{
		ProjectScheduleRegistrationID: "project_schedule_registration_" + scheduleKey,
		ProjectContractRegistrationID: "project_contract_registration_test",
		ProjectID:                     "project_test",
		ScheduleKey:                   scheduleKey,
		BackendScheduleKey:            "script_smoke__" + scheduleKey,
		ScheduleFolder:                "schedules/" + scheduleKey,
		ScheduleManifestPath:          "schedules/" + scheduleKey + "/loom.schedule.yaml",
		ScheduleHash:                  "sha256:" + fmt.Sprintf("%064x", 1),
		InputPath:                     "schedules/" + scheduleKey + "/input.example.json",
		InputHash:                     "sha256:" + fmt.Sprintf("%064x", 2),
		TargetCapability:              "main@script-smoke.hello_world",
		AutomationID:                  &automationID,
		ScheduleID:                    &scheduleID,
		ActivationStatus:              status,
	}
}

func projectDirectEventRegistration(eventKey, status string) projects.ProjectDirectEventRegistration {
	integrationID := "integration_" + eventKey
	authProfileID := "integration_auth_profile_" + eventKey
	endpointID := "direct_event_endpoint_" + eventKey
	automationID := "automation_" + eventKey
	return projects.ProjectDirectEventRegistration{
		ProjectDirectEventRegistrationID: "project_direct_event_registration_" + eventKey,
		ProjectContractRegistrationID:    "project_contract_registration_test",
		ProjectID:                        "project_test",
		EventKey:                         eventKey,
		IntegrationKey:                   "gmail",
		BackendIntegrationKey:            "script_smoke__gmail",
		EndpointSlug:                     eventKey,
		BackendEndpointSlug:              "script_smoke__" + eventKey,
		EndpointPath:                     "/v1/direct-events/ingest/script_smoke__" + eventKey,
		EventFolder:                      "direct_events/" + eventKey,
		EventManifestPath:                "direct_events/" + eventKey + "/loom.direct_event.yaml",
		EventHash:                        "sha256:" + fmt.Sprintf("%064x", 1),
		PayloadExamplePath:               "direct_events/" + eventKey + "/examples/payload.json",
		PayloadExampleHash:               "sha256:" + fmt.Sprintf("%064x", 2),
		ExpectedInputPath:                "direct_events/" + eventKey + "/examples/expected_mapped_input.json",
		ExpectedInputHash:                "sha256:" + fmt.Sprintf("%064x", 3),
		EventType:                        "gmail.message.received",
		TargetCapability:                 "main@script-smoke.hello_world",
		ResponseMode:                     automation.DirectEventResponseAccepted,
		IntegrationID:                    &integrationID,
		AuthProfileID:                    &authProfileID,
		EndpointID:                       &endpointID,
		AutomationID:                     &automationID,
		ActivationStatus:                 status,
	}
}

func projectModuleRegistration(moduleKey, status string) projects.ProjectModuleRegistration {
	packageID := "module_package_" + moduleKey
	versionID := "module_version_" + moduleKey
	return projects.ProjectModuleRegistration{
		ProjectModuleRegistrationID:   "project_module_registration_" + moduleKey,
		ProjectContractRegistrationID: "project_contract_registration_test",
		ProjectID:                     "project_test",
		ModuleKey:                     moduleKey,
		ModuleFolder:                  "modules/" + moduleKey,
		ModuleManifestPath:            "modules/" + moduleKey + "/module.json",
		ModuleProjectContractPath:     "modules/" + moduleKey + "/loom.module_project.yaml",
		ModuleManifestHash:            "sha256:" + fmt.Sprintf("%064x", 1),
		ModuleProjectContractHash:     "sha256:" + fmt.Sprintf("%064x", 2),
		ModulePackageHash:             "sha256:" + fmt.Sprintf("%064x", 3),
		ModuleID:                      "loom.script-smoke",
		ModuleName:                    "Script Smoke Module",
		ModuleVersion:                 "0.1.0",
		ModuleKind:                    modules.ModuleKindNative,
		ModulePackageID:               &packageID,
		ModuleVersionID:               &versionID,
		ProviderCount:                 1,
		CapabilityCount:               1,
		RegistrationEnabled:           true,
		InstallPlanJSON:               json.RawMessage(`{"plan":"explicit_only"}`),
		ExposurePlanJSON:              json.RawMessage(`{"plan":"explicit_only"}`),
		ActivationStatus:              status,
	}
}

func writeProject(t *testing.T, exposureEnabled bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	contract := []byte(`kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
  scripts: true
`)
	if err := os.WriteFile(filepath.Join(root, "loom.project.yaml"), contract, 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	packageRoot := filepath.Join(root, "scripts", "hello_world")
	if err := os.MkdirAll(packageRoot, 0o755); err != nil {
		t.Fatalf("mkdir package: %v", err)
	}
	writeTestFile(t, packageRoot, "loom.script.yaml", `kind: loom.script
id: hello_world
name: Hello World
version: 0.1.0
description: Test script.
entrypoint:
  command:
    - ./run.sh
execution:
  timeout_seconds: 30
  network: false
  filesystem:
    mode: read_only
usage_documents:
  - path: README.md
`, 0o600)
	writeTestFile(t, packageRoot, "run.sh", "#!/usr/bin/env bash\nprintf 'hello\\n'\n", 0o700)
	writeTestFile(t, packageRoot, "README.md", "usage\n", 0o600)
	enabled := "false"
	if exposureEnabled {
		enabled = "true"
	}
	writeTestFile(t, packageRoot, "loom.exposure.yaml", `kind: loom.script_exposure
schema_version: script.exposure.v0.3
expose:
  enabled: `+enabled+`
  provider: project
  endpoint: hello_world
  display_name: Hello World
  description: Test script capability.
capability:
  class_namespace: project
  class_name: script_smoke
  form: job
  risk_level: low
  execution_authorization_level: 1
  input_schema:
    type: object
  output_schema:
    type: object
execution:
  default_mode: wait_until_started
  wait_timeout_seconds: 10
`, 0o600)
	sum := sha256.Sum256(contract)
	return root, fmt.Sprintf("sha256:%x", sum[:])
}

func writeScriptAndScheduleProject(t *testing.T) (string, string) {
	t.Helper()
	root, _ := writeProject(t, true)
	contract := []byte(`kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
  scripts: true
  schedules: true
`)
	if err := os.WriteFile(filepath.Join(root, "loom.project.yaml"), contract, 0o600); err != nil {
		t.Fatalf("write combined contract: %v", err)
	}
	scheduleRoot := filepath.Join(root, "schedules", "daily_summary")
	if err := os.MkdirAll(scheduleRoot, 0o755); err != nil {
		t.Fatalf("mkdir schedule package: %v", err)
	}
	writeTestFile(t, scheduleRoot, "loom.schedule.yaml", `kind: loom.schedule
schema_version: schedule.contract.v0.3
schedule:
  key: daily_summary
  display_name: Daily Summary
  status: draft
target:
  capability: main@script-smoke.hello_world
  input_file: input.example.json
  run_as: owner
timing:
  kind: interval
  expression: 24h
  timezone: UTC
misfire:
  policy: mark_missed
concurrency:
  policy: allow_parallel
approval:
  policy: none
timeout:
  seconds: 60
retry:
  max_attempts: 1
`, 0o600)
	writeTestFile(t, scheduleRoot, "input.example.json", `{"message":"scheduled"}`, 0o600)
	sum := sha256.Sum256(contract)
	return root, fmt.Sprintf("sha256:%x", sum[:])
}

func writeWorkflowProject(t *testing.T, implementationKind string, withScript bool, exposureEnabled bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	facets := `  workflows: true
`
	if withScript {
		facets = `  scripts: true
  workflows: true
`
	}
	contract := []byte(`kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
` + facets)
	if err := os.WriteFile(filepath.Join(root, "loom.project.yaml"), contract, 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}

	if withScript {
		packageRoot := filepath.Join(root, "scripts", "hello_world")
		if err := os.MkdirAll(packageRoot, 0o755); err != nil {
			t.Fatalf("mkdir script package: %v", err)
		}
		writeTestFile(t, packageRoot, "loom.script.yaml", `kind: loom.script
id: hello_world
name: Hello World
version: 0.1.0
description: Test script.
entrypoint:
  command:
    - ./run.sh
execution:
  timeout_seconds: 30
  network: false
  filesystem:
    mode: read_only
`, 0o600)
		writeTestFile(t, packageRoot, "run.sh", "#!/usr/bin/env bash\nprintf 'hello\\n'\n", 0o700)
		enabled := "false"
		if exposureEnabled {
			enabled = "true"
		}
		writeTestFile(t, packageRoot, "loom.exposure.yaml", `kind: loom.script_exposure
schema_version: script.exposure.v0.3
expose:
  enabled: `+enabled+`
  provider: project
  endpoint: hello_world
  display_name: Hello World
  description: Test script capability.
capability:
  class_namespace: project
  class_name: script_smoke
  form: job
  risk_level: low
  execution_authorization_level: 1
  input_schema:
    type: object
  output_schema:
    type: object
execution:
  default_mode: wait_until_started
  wait_timeout_seconds: 10
`, 0o600)
	}

	implementation := "  kind: " + implementationKind + "\n"
	steps := "steps: []\n"
	if implementationKind == "script" {
		implementation = `  kind: script
  script_ref: ../../scripts/hello_world/loom.script.yaml
`
		steps = `steps:
  - id: call_script
    kind: capability_call
    target: main@script-smoke.hello_world
    input: {}
`
	}
	workflowExposeEnabled := "false"
	if exposureEnabled {
		workflowExposeEnabled = "true"
	}
	workflowRoot := filepath.Join(root, "workflows", "example_workflow")
	if err := os.MkdirAll(workflowRoot, 0o755); err != nil {
		t.Fatalf("mkdir workflow package: %v", err)
	}
	writeTestFile(t, workflowRoot, "loom.workflow.yaml", `kind: loom.workflow
schema_version: workflow.contract.v0.3
workflow:
  id: example_workflow
  name: Example Workflow
  status: draft
  description: Test workflow.
implementation:
`+implementation+`expose:
  enabled: `+workflowExposeEnabled+`
  provider: project
  endpoint: example_workflow
inputs:
  schema:
    type: object
outputs:
  schema:
    type: object
`+steps, 0o600)

	sum := sha256.Sum256(contract)
	return root, fmt.Sprintf("sha256:%x", sum[:])
}

func writeExecutableWorkflowProject(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	contract := []byte(`kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
  workflows: true
`)
	if err := os.WriteFile(filepath.Join(root, "loom.project.yaml"), contract, 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	workflowRoot := filepath.Join(root, "workflows", "example_workflow")
	if err := os.MkdirAll(workflowRoot, 0o755); err != nil {
		t.Fatalf("mkdir workflow package: %v", err)
	}
	writeTestFile(t, workflowRoot, "loom.workflow.yaml", `kind: loom.workflow
schema_version: workflow.contract.v0.3.1
workflow:
  id: example_workflow
  name: Example Workflow
  status: active
  version: 0.1.0
  description: Test executable workflow.
implementation:
  kind: workflow
entrypoint:
  command:
    - ./run.sh
runtime:
  shell: bash
execution:
  timeout_seconds: 30
  network: false
  filesystem:
    mode: read_only
expose:
  enabled: true
  provider: project
  endpoint: example_workflow
  display_name: Example Workflow
  description: Test workflow capability.
capability:
  class_namespace: project.workflow
  class_name: example_workflow
  form: job
  risk_level: medium
  execution_authorization_level: 2
  requires_approval: false
  side_effects: []
  input_schema:
    type: object
  output_schema:
    type: object
inputs:
  schema:
    type: object
outputs:
  schema:
    type: object
artifacts: []
usage_documents:
  - path: README.md
    target: endpoint
steps: []
`, 0o600)
	writeTestFile(t, workflowRoot, "run.sh", "#!/usr/bin/env bash\nprintf '{\"status\":\"ok\",\"outputs\":{\"message\":\"workflow\"},\"artifacts\":[]}'\n", 0o700)
	writeTestFile(t, workflowRoot, "README.md", "workflow usage\n", 0o600)
	sum := sha256.Sum256(contract)
	return root, fmt.Sprintf("sha256:%x", sum[:])
}

func writeScheduleProject(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	contract := []byte(`kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
  schedules: true
`)
	if err := os.WriteFile(filepath.Join(root, "loom.project.yaml"), contract, 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	scheduleRoot := filepath.Join(root, "schedules", "daily_summary")
	if err := os.MkdirAll(scheduleRoot, 0o755); err != nil {
		t.Fatalf("mkdir schedule package: %v", err)
	}
	writeTestFile(t, scheduleRoot, "loom.schedule.yaml", `kind: loom.schedule
schema_version: schedule.contract.v0.3
schedule:
  key: daily_summary
  display_name: Daily Summary
  status: draft
target:
  capability: main@script-smoke.hello_world
  input_file: input.example.json
  run_as: owner
timing:
  kind: interval
  expression: 24h
  timezone: UTC
misfire:
  policy: mark_missed
concurrency:
  policy: allow_parallel
approval:
  policy: none
timeout:
  seconds: 60
retry:
  max_attempts: 1
`, 0o600)
	writeTestFile(t, scheduleRoot, "input.example.json", `{"message":"scheduled"}`, 0o600)
	sum := sha256.Sum256(contract)
	return root, fmt.Sprintf("sha256:%x", sum[:])
}

func writeDirectEventProject(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	contract := []byte(`kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
  direct_events: true
`)
	if err := os.WriteFile(filepath.Join(root, "loom.project.yaml"), contract, 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	eventRoot := filepath.Join(root, "direct_events", "gmail_message")
	if err := os.MkdirAll(filepath.Join(eventRoot, "examples"), 0o755); err != nil {
		t.Fatalf("mkdir direct event package: %v", err)
	}
	writeTestFile(t, eventRoot, "loom.direct_event.yaml", `kind: loom.direct_event
schema_version: direct_event.contract.v0.3
event:
  key: gmail_message
  display_name: Gmail Message
  status: draft
integration:
  key: gmail
  display_name: Gmail
  main_auth_level: 3
endpoint:
  slug: gmail_message
  display_name: Gmail Message
  event_type: gmail.message.received
  status: draft
target:
  capability: main@script-smoke.hello_world
auth:
  profiles:
    - name: private_network
      kind: private_network
      create_if_missing: true
mapping:
  fields:
    input.url:
      source: body
      expr: $.url
  required:
    - input.url
idempotency:
  strategy: payload_path
  path: $.id
response:
  mode: accepted
timeout:
  seconds: 60
retry:
  max_attempts: 1
storage:
  payload_limit_bytes: 1048576
  store_raw_body: true
examples:
  payload: examples/payload.json
  expected_mapped_input: examples/expected_mapped_input.json
`, 0o600)
	writeTestFile(t, eventRoot, "examples/payload.json", `{"id":"msg-1","url":"https://example.invalid"}`, 0o600)
	writeTestFile(t, eventRoot, "examples/expected_mapped_input.json", `{"input":{"url":"https://example.invalid"}}`, 0o600)
	sum := sha256.Sum256(contract)
	return root, fmt.Sprintf("sha256:%x", sum[:])
}

func writeScriptAndDirectEventProject(t *testing.T) (string, string) {
	t.Helper()
	root, _ := writeProject(t, true)
	contract := []byte(`kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
  scripts: true
  direct_events: true
`)
	if err := os.WriteFile(filepath.Join(root, "loom.project.yaml"), contract, 0o600); err != nil {
		t.Fatalf("write combined direct-event contract: %v", err)
	}
	eventRoot := filepath.Join(root, "direct_events", "gmail_message")
	if err := os.MkdirAll(filepath.Join(eventRoot, "examples"), 0o755); err != nil {
		t.Fatalf("mkdir direct event package: %v", err)
	}
	writeTestFile(t, eventRoot, "loom.direct_event.yaml", `kind: loom.direct_event
schema_version: direct_event.contract.v0.3
event:
  key: gmail_message
  display_name: Gmail Message
  status: draft
integration:
  key: gmail
  display_name: Gmail
  main_auth_level: 3
endpoint:
  slug: gmail_message
  display_name: Gmail Message
  event_type: gmail.message.received
  status: draft
target:
  capability: main@script-smoke.hello_world
auth:
  profiles:
    - name: private_network
      kind: private_network
      create_if_missing: true
mapping:
  fields:
    input.url:
      source: body
      expr: $.url
  required:
    - input.url
idempotency:
  strategy: payload_path
  path: $.id
response:
  mode: accepted
timeout:
  seconds: 60
retry:
  max_attempts: 1
storage:
  payload_limit_bytes: 1048576
  store_raw_body: true
examples:
  payload: examples/payload.json
  expected_mapped_input: examples/expected_mapped_input.json
`, 0o600)
	writeTestFile(t, eventRoot, "examples/payload.json", `{"id":"msg-1","url":"https://example.invalid"}`, 0o600)
	writeTestFile(t, eventRoot, "examples/expected_mapped_input.json", `{"input":{"url":"https://example.invalid"}}`, 0o600)
	sum := sha256.Sum256(contract)
	return root, fmt.Sprintf("sha256:%x", sum[:])
}

func writeConnectorProject(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	contract := []byte(`kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
  connectors: true
`)
	if err := os.WriteFile(filepath.Join(root, "loom.project.yaml"), contract, 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	connectorRoot := filepath.Join(root, "connectors", "example_connector")
	scriptRoot := filepath.Join(connectorRoot, "scripts", "ping")
	if err := os.MkdirAll(scriptRoot, 0o755); err != nil {
		t.Fatalf("mkdir connector package: %v", err)
	}
	writeTestFile(t, connectorRoot, "loom.connector.yaml", `kind: loom.connector
schema_version: connector.contract.v0.3
provider:
  key: example_connector
  display_name: Example Connector
  type: connector
  version: 0.1.0
  status: draft
runtime:
  kind: script
  base_dir: scripts
capabilities:
  - endpoint: ping
    display_name: Ping
    form: job
    risk_level: low
    runtime:
      kind: script
      script: scripts/ping
      default_mode: wait_for_completion
      wait_timeout_seconds: 30
    input_schema:
      type: object
    output_schema:
      type: object
usage_documents:
  - path: README.md
    target: provider
`, 0o600)
	writeTestFile(t, connectorRoot, "README.md", "connector usage\n", 0o600)
	writeTestFile(t, scriptRoot, "loom.script.yaml", `kind: loom.script
id: connector_ping
name: Connector Ping
version: 0.1.0
entrypoint:
  command:
    - ./run.sh
execution:
  timeout_seconds: 30
  network: false
`, 0o600)
	writeTestFile(t, scriptRoot, "run.sh", "#!/usr/bin/env bash\nprintf '{\"ok\":true}\\n'\n", 0o700)
	sum := sha256.Sum256(contract)
	return root, fmt.Sprintf("sha256:%x", sum[:])
}

func writeModuleProject(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	contract := []byte(`kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
  modules: true
`)
	if err := os.WriteFile(filepath.Join(root, "loom.project.yaml"), contract, 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	moduleRoot := filepath.Join(root, "modules", "example_module")
	if err := os.MkdirAll(moduleRoot, 0o755); err != nil {
		t.Fatalf("mkdir module package: %v", err)
	}
	writeTestFile(t, moduleRoot, "module.json", `{
  "module": {
    "id": "loom.script-smoke",
    "name": "Script Smoke Module",
    "version": "0.1.0",
    "kind": "native",
    "description": "Test module."
  },
  "requires": {},
  "storage": {
    "database": {"required": false},
    "filesystem": {"required": false}
  },
  "provides": {
    "object_types": [],
    "providers": [
      {
        "provider_key": "example_module",
        "display_name": "Example Module"
      }
    ],
    "capabilities": [
      {
        "provider_key": "example_module",
        "endpoint_name": "ping",
        "capability_class_namespace": "module.example",
        "capability_class_name": "Ping",
        "display_name": "Ping",
        "form": "job",
        "execution_authorization_level": 1,
        "risk_level": "low"
      }
    ],
    "usage_documents": [],
    "backup_hooks": []
  }
}`, 0o600)
	writeTestFile(t, moduleRoot, "loom.module_project.yaml", `kind: loom.module_project
schema_version: module_project.contract.v0.3
module:
  manifest: module.json
  status: draft
project:
  owned_by_project: true
  expose_in_project_portal: true
registration:
  register: true
install:
  plan: explicit_only
  target_node: main
  scope: system
  install_after_register: false
  enable_after_install: false
exposure:
  plan: explicit_only
  expose_after_enable: false
  capabilities: []
validation:
  require_usage_docs: false
  require_backup_hooks_for_stateful_storage: true
`, 0o600)
	sum := sha256.Sum256(contract)
	return root, fmt.Sprintf("sha256:%x", sum[:])
}

func writeTestFile(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func replaceInFile(t *testing.T, path, old, new string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	next := strings.Replace(string(payload), old, new, 1)
	if next == string(payload) {
		t.Fatalf("expected to replace %q in %s", old, path)
	}
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func addCredentialPolicy(t *testing.T, root, ref, env string) string {
	t.Helper()
	contractPath := filepath.Join(root, "loom.project.yaml")
	payload, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read project contract: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, "policies:\n") {
		text += "policies:\n  credentials: policies/credentials.yaml\n"
	} else if !strings.Contains(text, "credentials: policies/credentials.yaml") {
		text += "  credentials: policies/credentials.yaml\n"
	}
	if err := os.WriteFile(contractPath, []byte(text), 0o600); err != nil {
		t.Fatalf("write project contract: %v", err)
	}
	policyPath := filepath.Join(root, "policies")
	if err := os.MkdirAll(policyPath, 0o755); err != nil {
		t.Fatalf("mkdir policies: %v", err)
	}
	writeTestFile(t, policyPath, "credentials.yaml", `kind: loom.credentials_policy
schema_version: credentials.policy.v0.3

credentials:
  inline_secrets_allowed: false
  references:
    - ref: `+ref+`
      source:
        kind: env
        env: `+env+`
      status: required
`, 0o600)
	return hashForFile(t, contractPath)
}

func restoreEnv(t *testing.T, key string) {
	t.Helper()
	previous, hadPrevious := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset env %s: %v", key, err)
	}
	t.Cleanup(func() {
		if hadPrevious {
			_ = os.Setenv(key, previous)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func assertCredentialRequirementsJSON(t *testing.T, payload json.RawMessage, wantRef, wantExposeAs string) {
	t.Helper()
	var doc struct {
		Required []struct {
			Ref      string `json:"ref"`
			ExposeAs string `json:"expose_as"`
			Kind     string `json:"kind"`
		} `json:"required"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("credential requirements are not JSON: %v payload=%s", err, payload)
	}
	if len(doc.Required) != 1 {
		t.Fatalf("credential requirement count = %d, payload=%s", len(doc.Required), payload)
	}
	got := doc.Required[0]
	if got.Ref != wantRef || got.ExposeAs != wantExposeAs || got.Kind != "env" {
		t.Fatalf("credential requirement = %#v, want ref=%q expose_as=%q kind=env", got, wantRef, wantExposeAs)
	}
}

func assertRuntimeCredentialBinding(t *testing.T, payload json.RawMessage, wantRef, wantExposeAs, wantSourceEnv string) {
	t.Helper()
	var doc struct {
		CredentialBindings []struct {
			Ref      string         `json:"ref"`
			Kind     string         `json:"kind"`
			ExposeAs string         `json:"expose_as"`
			Source   map[string]any `json:"source"`
		} `json:"credential_bindings"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("runtime config is not JSON: %v payload=%s", err, payload)
	}
	if len(doc.CredentialBindings) != 1 {
		t.Fatalf("credential binding count = %d payload=%s", len(doc.CredentialBindings), payload)
	}
	got := doc.CredentialBindings[0]
	if got.Ref != wantRef || got.ExposeAs != wantExposeAs || got.Kind != "env" || got.Source["env"] != wantSourceEnv {
		t.Fatalf("credential binding = %#v, want ref=%q expose_as=%q source env=%q", got, wantRef, wantExposeAs, wantSourceEnv)
	}
	if strings.Contains(string(payload), "test-token") || strings.Contains(string(payload), "12345") {
		t.Fatalf("runtime config leaked a secret value: %s", payload)
	}
}

func hashForFile(t *testing.T, path string) string {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
}
