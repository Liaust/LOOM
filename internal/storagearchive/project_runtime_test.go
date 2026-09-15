package storagearchive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageretention"
)

func TestProjectArchiveLegacyApplyIsRetired(t *testing.T) {
	now := time.Date(2026, 6, 5, 10, 30, 0, 0, time.UTC)
	projectSvc := &fakeProjectRuntimeProjectService{detail: projectRuntimeArchiveDetail()}
	activationSvc := &fakeProjectRuntimeActivationService{}
	archiveSvc := &fakeProjectStorageArchiver{}
	svc := NewProjectRuntimeService(ProjectRuntimeDeps{
		Projects:            projectSvc,
		Activation:          activationSvc,
		StorageArchive:      archiveSvc,
		RuntimeManifestRoot: t.TempDir(),
		Now:                 func() time.Time { return now },
	})

	if _, err := svc.ArchiveProject(context.Background(), projectRuntimeRequest(), "gmail-automation", ProjectArchiveInput{SourceRef: "macbook/Backups/Projects/gmail-automation/current", Reason: "done"}); err == nil || !strings.Contains(err.Error(), "legacy project archive writer is retired") {
		t.Fatalf("ArchiveProject error = %v, want retired writer", err)
	}
	if archiveSvc.called || len(activationSvc.facets) != 0 || len(projectSvc.archiveState) != 0 {
		t.Fatalf("retired writer mutated dependencies: archive=%t facets=%#v state=%s", archiveSvc.called, activationSvc.facets, projectSvc.archiveState)
	}
}

func TestProjectArchiveDryRunDoesNotWriteOrMutate(t *testing.T) {
	projectSvc := &fakeProjectRuntimeProjectService{detail: projectRuntimeArchiveDetail()}
	activationSvc := &fakeProjectRuntimeActivationService{}
	archiveSvc := &fakeProjectStorageArchiver{}
	root := t.TempDir()
	svc := NewProjectRuntimeService(ProjectRuntimeDeps{
		Projects:            projectSvc,
		Activation:          activationSvc,
		StorageArchive:      archiveSvc,
		RuntimeManifestRoot: root,
		Now:                 func() time.Time { return time.Date(2026, 6, 5, 10, 30, 0, 0, time.UTC) },
	})

	result, err := svc.ArchiveProject(context.Background(), projectRuntimeRequest(), "gmail-automation", ProjectArchiveInput{SourceRef: "macbook/Backups/Projects/gmail-automation/current", DryRun: true})
	if err != nil {
		t.Fatalf("ArchiveProject returned error: %v", err)
	}
	if !archiveSvc.input.DryRun {
		t.Fatal("storage archive was not called in dry-run mode")
	}
	if len(projectSvc.archiveState) != 0 {
		t.Fatal("dry-run mutated project archive_state")
	}
	if _, err := os.Stat(result.RuntimeManifestPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote runtime manifest, stat err=%v", err)
	}
}

func TestProjectArchiveLegacyCanonicalSnapshotWriterIsRetired(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(projectRoot, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "scripts", "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(projectRoot, "external-link")); err != nil {
		t.Fatal(err)
	}
	detail := projectRuntimeArchiveDetail()
	detail.Registration.ProjectRoot = projectRoot
	detail.WatchedRootRegistrations = nil
	projectSvc := &fakeProjectRuntimeProjectService{detail: detail}
	activationSvc := &fakeProjectRuntimeActivationService{}
	archiveSvc := &fakeProjectStorageArchiver{}
	svc := NewProjectRuntimeService(ProjectRuntimeDeps{
		Projects:            projectSvc,
		Activation:          activationSvc,
		StorageArchive:      archiveSvc,
		RuntimeManifestRoot: t.TempDir(),
		Now:                 func() time.Time { return time.Date(2026, 6, 5, 10, 30, 0, 0, time.UTC) },
	})

	if _, err := svc.ArchiveProject(context.Background(), projectRuntimeRequest(), "gmail-automation", ProjectArchiveInput{Reason: "main archive"}); err == nil || !strings.Contains(err.Error(), "legacy project archive writer is retired") {
		t.Fatalf("ArchiveProject error = %v, want retired writer", err)
	}
	if archiveSvc.called || len(activationSvc.facets) != 0 || len(projectSvc.archiveState) != 0 {
		t.Fatal("retired canonical snapshot writer mutated state")
	}
	if matches, err := filepath.Glob(filepath.Join(svc.RuntimeManifestRoot, "**", "*.tar")); err != nil || len(matches) != 0 {
		t.Fatalf("legacy tar artifacts exist: %#v err=%v", matches, err)
	}
}

func TestProjectArchiveFallsBackWhenWatchedRootsHaveNoBackupView(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	detail := projectRuntimeArchiveDetail()
	detail.Registration.ProjectRoot = projectRoot
	detail.WatchedRootRegistrations[0].OwnerNodeKey = "main"
	detail.WatchedRootRegistrations[0].BackupMode = "none"
	projectSvc := &fakeProjectRuntimeProjectService{detail: detail}
	activationSvc := &fakeProjectRuntimeActivationService{}
	archiveSvc := &fakeProjectStorageArchiver{}
	svc := NewProjectRuntimeService(ProjectRuntimeDeps{
		Projects:            projectSvc,
		Activation:          activationSvc,
		StorageArchive:      archiveSvc,
		RuntimeManifestRoot: t.TempDir(),
		Now:                 func() time.Time { return time.Date(2026, 6, 5, 10, 30, 0, 0, time.UTC) },
	})

	result, err := svc.ArchiveProject(context.Background(), projectRuntimeRequest(), "gmail-automation", ProjectArchiveInput{DryRun: true})
	if err != nil {
		t.Fatalf("ArchiveProject returned error: %v", err)
	}
	if archiveSvc.called {
		t.Fatalf("storage archive should not be called when watched roots have backup_mode=none: %#v", archiveSvc.input)
	}
	if !result.StorageArchiveSkipped {
		t.Fatal("backup_mode=none fallback should mark storage archive skipped")
	}
	if result.RuntimeManifest.SourceKind != projectArchiveSourceKindLocalProjectRoot {
		t.Fatalf("source kind = %q", result.RuntimeManifest.SourceKind)
	}
	if result.RuntimeManifest.SourceRef != projectRoot {
		t.Fatalf("source ref = %q, want %q", result.RuntimeManifest.SourceRef, projectRoot)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "item custody rather than a synthetic project backup view") {
		t.Fatalf("expected backup view fallback warning, got %#v", result.Warnings)
	}
	if len(projectSvc.archiveState) != 0 {
		t.Fatal("dry-run mutated project archive_state")
	}
}

func TestProjectArchiveDefaultsToRealProjectRootWithActiveBackupRoots(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	detail := projectRuntimeArchiveDetail()
	detail.Registration.ProjectRoot = projectRoot
	projectSvc := &fakeProjectRuntimeProjectService{detail: detail}
	archiveSvc := &fakeProjectStorageArchiver{}
	svc := NewProjectRuntimeService(ProjectRuntimeDeps{
		Projects:            projectSvc,
		Activation:          &fakeProjectRuntimeActivationService{},
		StorageArchive:      archiveSvc,
		RuntimeManifestRoot: t.TempDir(),
	})

	result, err := svc.ArchiveProject(context.Background(), projectRuntimeRequest(), "gmail-automation", ProjectArchiveInput{DryRun: true})
	if err != nil {
		t.Fatalf("ArchiveProject returned error: %v", err)
	}
	if archiveSvc.called {
		t.Fatal("default archive must not reconstruct a retired synthetic project backup view")
	}
	if result.RuntimeManifest.SourceKind != projectArchiveSourceKindLocalProjectRoot || result.RuntimeManifest.SourceRef != projectRoot {
		t.Fatalf("unexpected default source: kind=%q ref=%q", result.RuntimeManifest.SourceKind, result.RuntimeManifest.SourceRef)
	}
	if !result.StorageArchiveSkipped || result.SafeToDelete {
		t.Fatalf("unexpected default retention status: skipped=%t safe_to_delete=%t", result.StorageArchiveSkipped, result.SafeToDelete)
	}
}

func TestProjectArchiveDefaultRejectsSymlinkedRegisteredRoot(t *testing.T) {
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	detail := projectRuntimeArchiveDetail()
	detail.Registration.ProjectRoot = linkRoot
	svc := NewProjectRuntimeService(ProjectRuntimeDeps{
		Projects:       &fakeProjectRuntimeProjectService{detail: detail},
		Activation:     &fakeProjectRuntimeActivationService{},
		StorageArchive: &fakeProjectStorageArchiver{},
	})

	if _, err := svc.ArchiveProject(context.Background(), projectRuntimeRequest(), "gmail-automation", ProjectArchiveInput{DryRun: true}); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("ArchiveProject error = %v, want real-directory rejection", err)
	}
}

func TestProjectArchiveInspectAndPlans(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "gmail-automation", "project_runtime_archive_test.json")
	manifest := ProjectRuntimeArchiveManifest{
		SchemaVersion:           ProjectRuntimeArchiveManifestSchemaVersion,
		ProjectRuntimeArchiveID: "project_runtime_archive_test",
		ProjectID:               "project_test",
		ProjectSlug:             "gmail-automation",
		RuntimeOwnershipRule:    projectRuntimeOwnershipRule,
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManifestFile(manifestPath, payload); err != nil {
		t.Fatal(err)
	}
	detail := projectRuntimeArchiveDetail()
	state, err := json.Marshal(map[string]any{"runtime_manifest_path": manifestPath})
	if err != nil {
		t.Fatal(err)
	}
	detail.Project.Project.ArchiveState = state
	projectSvc := &fakeProjectRuntimeProjectService{detail: detail}
	svc := NewProjectRuntimeService(ProjectRuntimeDeps{Projects: projectSvc})

	inspect, err := svc.InspectProjectArchive(context.Background(), "gmail-automation")
	if err != nil {
		t.Fatalf("InspectProjectArchive returned error: %v", err)
	}
	if inspect.RuntimeManifest == nil || inspect.RuntimeManifest.ProjectRuntimeArchiveID != "project_runtime_archive_test" {
		t.Fatalf("runtime manifest not loaded: %#v", inspect.RuntimeManifest)
	}
	restore, err := svc.PlanProjectArchiveRestore(context.Background(), "gmail-automation", ProjectArchiveRestoreInput{ToNode: "macbook", DryRun: true})
	if err != nil {
		t.Fatalf("PlanProjectArchiveRestore returned error: %v", err)
	}
	if len(restore.Steps) == 0 || !restore.DryRun {
		t.Fatalf("unexpected restore plan: %#v", restore)
	}
	migrate, err := svc.PlanProjectRuntimeMigration(context.Background(), "gmail-automation", ProjectRuntimeMigrationInput{ToNode: "main", DryRun: true})
	if err != nil {
		t.Fatalf("PlanProjectRuntimeMigration returned error: %v", err)
	}
	if len(migrate.Steps) == 0 || !migrate.DryRun {
		t.Fatalf("unexpected migrate plan: %#v", migrate)
	}
}

func projectRuntimeArchiveDetail() projects.ProjectRegistrationDetail {
	endpointID := ids.NewCapabilityEndpointID()
	endpointVersionID := ids.NewCapabilityEndpointVersionID()
	runtimeBindingID := ids.NewCapabilityRuntimeBindingID()
	providerID := ids.NewProviderID()
	scheduleID := ids.NewScheduleID()
	endpoint := ids.NewDirectEventEndpointID()
	watchedRootID := ids.NewWatchedRootID()
	modulePackageID := ids.NewModulePackageID()
	projectID := "project_test"
	registrationID := "project_contract_registration_test"
	return projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{
			ProjectID:       projectID,
			ProjectScopeID:  "scope_test",
			ProjectScopeKey: "project:gmail-automation",
			Slug:            "gmail-automation",
			Name:            "Gmail Automation",
			Status:          "active",
			ArchiveState:    json.RawMessage(`{}`),
		}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: registrationID,
			ProjectID:                     projectID,
			ProjectRoot:                   "/Users/test/LOOM BOX/Projects/gmail-automation",
			RegistrationStatus:            projects.ProjectRegistrationStatusRegistered,
			ActivationStatus:              projects.ProjectActivationStatusBaseActive,
			RegistrationRevision:          3,
		},
		ScriptExposures: []projects.ProjectScriptExposure{{
			ProjectID:                   projectID,
			ScriptKey:                   "fetch",
			ProviderAddress:             "macbook@gmail-automation",
			CapabilityEndpointID:        &endpointID,
			CapabilityEndpointVersionID: &endpointVersionID,
			RuntimeBindingID:            &runtimeBindingID,
			CapabilityAddress:           "macbook@gmail-automation.fetch",
			ActivationStatus:            projects.ProjectScriptExposureStatusActive,
			Metadata:                    json.RawMessage(`{}`),
		}},
		WorkflowRegistrations: []projects.ProjectWorkflowRegistration{{
			ProjectID:                   projectID,
			WorkflowKey:                 "triage",
			ProviderAddress:             "macbook@gmail-automation",
			CapabilityEndpointID:        &endpointID,
			CapabilityEndpointVersionID: &endpointVersionID,
			RuntimeBindingID:            &runtimeBindingID,
			CapabilityAddress:           "macbook@gmail-automation.triage",
			ActivationStatus:            projects.ProjectWorkflowRegistrationStatusActive,
			Metadata:                    json.RawMessage(`{}`),
		}},
		ConnectorRegistrations: []projects.ProjectConnectorRegistration{{
			ProjectID:        projectID,
			ConnectorKey:     "gmail",
			ProviderAddress:  "macbook@gmail",
			ProviderID:       &providerID,
			ActivationStatus: projects.ProjectConnectorRegistrationStatusActive,
			CapabilityCount:  2,
			RuntimeKind:      "script",
			Metadata:         json.RawMessage(`{}`),
		}},
		ScheduleRegistrations: []projects.ProjectScheduleRegistration{{
			ProjectID:          projectID,
			ScheduleKey:        "daily",
			BackendScheduleKey: "gmail-daily",
			ScheduleID:         &scheduleID,
			TargetCapability:   "macbook@gmail-automation.fetch",
			ActivationStatus:   projects.ProjectScheduleRegistrationStatusActive,
			Metadata:           json.RawMessage(`{}`),
		}},
		DirectEventRegistrations: []projects.ProjectDirectEventRegistration{{
			ProjectID:           projectID,
			EventKey:            "gmail-received",
			BackendEndpointSlug: "gmail-received",
			EndpointID:          &endpoint,
			TargetCapability:    "macbook@gmail-automation.fetch",
			ActivationStatus:    projects.ProjectDirectEventRegistrationStatusActive,
			Metadata:            json.RawMessage(`{}`),
		}},
		WatchedRootRegistrations: []projects.ProjectWatchedRootRegistration{{
			ProjectID:        projectID,
			OwnerNodeKey:     "macbook",
			LocalRootKey:     "notes",
			BackendRootKey:   "gmail_automation__notes",
			WorkerKey:        "gmail_automation__notes",
			SourceKinds:      json.RawMessage(`["notes_contract"]`),
			RootRelativePath: "notes",
			SyncMode:         "mirror",
			BackupMode:       "private_backup",
			IndexMode:        "text",
			DeleteMode:       "tombstone",
			WatchedRootID:    &watchedRootID,
			ActivationStatus: projects.ProjectWatchedRootRegistrationStatusApplied,
			Metadata:         json.RawMessage(`{}`),
		}},
		ModuleRegistrations: []projects.ProjectModuleRegistration{{
			ProjectID:        projectID,
			ModuleKey:        "gmail-tools",
			ModuleID:         "gmail-tools",
			ModulePackageID:  &modulePackageID,
			ActivationStatus: projects.ProjectModuleRegistrationStatusRegistered,
			Metadata:         json.RawMessage(`{}`),
		}},
	}
}

func projectRuntimeRequest() requestctx.Context {
	return requestctx.Context{
		ActorID:       "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID:  "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		CorrelationID: "corr_test",
	}
}

type fakeProjectRuntimeProjectService struct {
	detail       projects.ProjectRegistrationDetail
	archiveState json.RawMessage
}

func (f *fakeProjectRuntimeProjectService) GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error) {
	detail := f.detail
	if len(f.archiveState) > 0 {
		detail.Project.Project.ArchiveState = f.archiveState
	}
	return detail, nil
}

func (f *fakeProjectRuntimeProjectService) UpdateProjectArchiveState(_ context.Context, _ requestctx.Context, _ string, archiveState json.RawMessage) (projects.Project, error) {
	f.archiveState = append(json.RawMessage(nil), archiveState...)
	project := f.detail.Project.Project
	project.Status = "archived"
	project.ArchiveState = f.archiveState
	return project, nil
}

type fakeProjectRuntimeActivationService struct {
	facets []string
}

func (f *fakeProjectRuntimeActivationService) Deactivate(_ context.Context, _ requestctx.Context, _ string, input projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error) {
	f.facets = append(f.facets, input.Facet)
	return projects.ProjectDeactivationResult{
		Facet:   input.Facet,
		Changed: !input.DryRun,
		DryRun:  input.DryRun,
		Actions: []projects.ProjectDeactivationAction{{
			Key:     input.Facet,
			Kind:    input.Facet,
			Status:  "disabled",
			Summary: "disabled by archive",
		}},
	}, nil
}

type fakeProjectStorageArchiver struct {
	input  ArchiveInput
	called bool
}

func (f *fakeProjectStorageArchiver) Archive(_ context.Context, input ArchiveInput) (ArchiveResult, error) {
	f.called = true
	f.input = input
	return ArchiveResult{
		ArchiveManifest: storagecatalog.ArchiveManifest{
			ArchiveManifestID: "storage_archive_manifest_test",
			ArchiveKey:        input.ArchiveKey,
			ArchiveKind:       input.ArchiveKind,
			OwnerNodeKey:      input.OwnerNodeKey,
			SourceRef:         input.SourceRef,
			Status:            "complete",
		},
		ManifestPath: "/tmp/storage-manifest.json",
		Entries: []ArchivedEntry{{
			SourceStorageEntryID:  "storage_entry_source",
			ArchiveStorageEntryID: "storage_entry_archive",
			SourceViewPath:        input.SourceRef + "/README.md",
			ArchiveViewPath:       input.TargetPath + "/README.md",
		}},
		SafeToDelete: []storageretention.SafeToDeleteResult{{Ref: "storage_entry_source", Safe: true}},
	}, nil
}
