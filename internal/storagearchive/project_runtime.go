package storagearchive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const (
	ProjectRuntimeArchiveManifestSchemaVersion    = "storage.project_runtime_archive_manifest.v0.6"
	ProjectHistoricalArchiveEvidenceSchemaVersion = "storage.project_historical_archive_evidence.v1"
	projectRuntimeArchiveStateSchemaVersion       = "project.archive_state.v0.6"
	projectRuntimeOwnershipRule                   = "Project archive moves storage custody only. Runtime ownership and capability URLs are not migrated unless migrate-runtime is explicitly applied."

	projectArchiveSourceKindStorageView      = "storage_view"
	projectArchiveSourceKindLocalProjectRoot = "local_project_root"
	projectHistoricalEvidenceRuntimeManifest = "historical_runtime_manifest"
	projectHistoricalEvidenceTarSnapshot     = "historical_tar_snapshot"
)

type ProjectRuntimeProjectService interface {
	GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error)
	UpdateProjectArchiveState(context.Context, requestctx.Context, string, json.RawMessage) (projects.Project, error)
}

type ProjectRuntimeActivationService interface {
	Deactivate(context.Context, requestctx.Context, string, projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error)
}

type ProjectPhysicalArchiveActivationService interface {
	DeactivateForPhysicalArchive(context.Context, requestctx.Context, string, projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error)
}

type ProjectStorageArchiver interface {
	Archive(context.Context, ArchiveInput) (ArchiveResult, error)
}

type ProjectRuntimeDeps struct {
	PlanEvidence        ProjectPhysicalPlanEvidenceStore
	Projects            ProjectRuntimeProjectService
	RepositoryState     ProjectRuntimeRepositoryStateService
	Activation          ProjectRuntimeActivationService
	StorageArchive      ProjectStorageArchiver
	WorkspaceMove       ProjectWorkspaceArchivePlanner
	RuntimeQuiescence   ProjectRuntimeQuiescenceVerifier
	WorkspaceRoots      TrustedWorkspaceRoots
	RuntimeManifestRoot string
	Now                 func() time.Time
	ArchiveFailureHook  ProjectPhysicalArchiveFailureHook
	RestoreFailureHook  ProjectPhysicalRestoreFailureHook
}

type ProjectRuntimeService struct {
	PlanEvidence        ProjectPhysicalPlanEvidenceStore
	Projects            ProjectRuntimeProjectService
	RepositoryState     ProjectRuntimeRepositoryStateService
	Activation          ProjectRuntimeActivationService
	StorageArchive      ProjectStorageArchiver
	WorkspaceMove       ProjectWorkspaceArchivePlanner
	RuntimeQuiescence   ProjectRuntimeQuiescenceVerifier
	WorkspaceRoots      TrustedWorkspaceRoots
	RuntimeManifestRoot string
	Now                 func() time.Time
	ArchiveFailureHook  ProjectPhysicalArchiveFailureHook
	RestoreFailureHook  ProjectPhysicalRestoreFailureHook
}

type ProjectArchiveInput struct {
	SourceRef          string `json:"source_ref,omitempty"`
	TargetPath         string `json:"target_path,omitempty"`
	Reason             string `json:"reason,omitempty"`
	SkipStorageArchive bool   `json:"skip_storage_archive,omitempty"`
	DryRun             bool   `json:"dry_run,omitempty"`
}

type ProjectArchiveResult struct {
	Project                projects.ProjectRegistrationDetail   `json:"project"`
	StorageArchive         ArchiveResult                        `json:"storage_archive,omitempty"`
	RuntimeManifest        ProjectRuntimeArchiveManifest        `json:"runtime_manifest"`
	RuntimeManifestPath    string                               `json:"runtime_manifest_path,omitempty"`
	FilesystemSnapshotPath string                               `json:"filesystem_snapshot_path,omitempty"`
	Deactivations          []projects.ProjectDeactivationResult `json:"deactivations,omitempty"`
	ArchiveState           json.RawMessage                      `json:"archive_state,omitempty"`
	SafeToDelete           bool                                 `json:"safe_to_delete"`
	DryRun                 bool                                 `json:"dry_run,omitempty"`
	StorageArchiveSkipped  bool                                 `json:"storage_archive_skipped,omitempty"`
	Warnings               []string                             `json:"warnings,omitempty"`
}

type ProjectArchiveInspectResult struct {
	Physical            *ProjectPhysicalArchiveInspection  `json:"physical,omitempty"`
	Project             projects.ProjectRegistrationDetail `json:"project"`
	ArchiveState        json.RawMessage                    `json:"archive_state,omitempty"`
	RuntimeManifest     *ProjectRuntimeArchiveManifest     `json:"runtime_manifest,omitempty"`
	RuntimeManifestPath string                             `json:"runtime_manifest_path,omitempty"`
	HistoricalEvidence  *ProjectHistoricalArchiveEvidence  `json:"historical_evidence,omitempty"`
	Warnings            []string                           `json:"warnings,omitempty"`
}

// ProjectHistoricalArchiveEvidence classifies v0.6 runtime/tar artifacts as
// inspectable history. These records are never accepted as evidence that the
// generic workspace kernel performed a physical custody move.
type ProjectHistoricalArchiveEvidence struct {
	SchemaVersion          string `json:"schema_version"`
	Classification         string `json:"classification"`
	ReadOnly               bool   `json:"read_only"`
	PhysicalMoveEvidence   bool   `json:"physical_move_evidence"`
	RuntimeManifestPath    string `json:"runtime_manifest_path"`
	FilesystemSnapshotPath string `json:"filesystem_snapshot_path,omitempty"`
}

type ProjectArchiveRestoreInput struct {
	ToNode string `json:"to_node,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

type ProjectArchiveRestorePlan struct {
	Project      projects.ProjectRegistrationDetail `json:"project"`
	ArchiveState json.RawMessage                    `json:"archive_state,omitempty"`
	Steps        []ProjectArchivePlanStep           `json:"steps"`
	Warnings     []string                           `json:"warnings,omitempty"`
	DryRun       bool                               `json:"dry_run"`
}

type ProjectRuntimeMigrationInput struct {
	ToNode string `json:"to_node,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

type ProjectRuntimeMigrationPlan struct {
	Project      projects.ProjectRegistrationDetail `json:"project"`
	ArchiveState json.RawMessage                    `json:"archive_state,omitempty"`
	Steps        []ProjectArchivePlanStep           `json:"steps"`
	Warnings     []string                           `json:"warnings,omitempty"`
	DryRun       bool                               `json:"dry_run"`
}

type ProjectArchivePlanStep struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type ProjectRuntimeArchiveState struct {
	SchemaVersion            string                        `json:"schema_version,omitempty"`
	Status                   string                        `json:"status,omitempty"`
	ProjectRuntimeArchiveID  string                        `json:"project_runtime_archive_id,omitempty"`
	RuntimeManifestPath      string                        `json:"runtime_manifest_path,omitempty"`
	RuntimeOwnershipRule     string                        `json:"runtime_ownership_rule,omitempty"`
	SuccessorPolicy          ProjectRuntimeSuccessorPolicy `json:"successor_policy,omitempty"`
	SourceKind               string                        `json:"source_kind,omitempty"`
	SourceRef                string                        `json:"source_ref,omitempty"`
	TargetPath               string                        `json:"target_path,omitempty"`
	ArchivedAt               time.Time                     `json:"archived_at,omitempty"`
	StorageArchiveSkipped    bool                          `json:"storage_archive_skipped,omitempty"`
	FilesystemSnapshotPath   string                        `json:"filesystem_snapshot_path,omitempty"`
	DryRun                   bool                          `json:"dry_run,omitempty"`
	StorageArchiveManifestID string                        `json:"storage_archive_manifest_id,omitempty"`
	StorageManifestPath      string                        `json:"storage_manifest_path,omitempty"`
	SafeToDelete             bool                          `json:"safe_to_delete,omitempty"`
	Warnings                 []string                      `json:"warnings,omitempty"`
}

type ProjectRuntimeArchiveManifest struct {
	SchemaVersion            string                        `json:"schema_version"`
	ProjectRuntimeArchiveID  string                        `json:"project_runtime_archive_id"`
	StorageArchiveManifestID string                        `json:"storage_archive_manifest_id,omitempty"`
	ProjectID                string                        `json:"project_id"`
	ProjectSlug              string                        `json:"project_slug"`
	ProjectName              string                        `json:"project_name"`
	RegistrationID           string                        `json:"registration_id,omitempty"`
	RegistrationRevision     int                           `json:"registration_revision,omitempty"`
	SourceKind               string                        `json:"source_kind,omitempty"`
	SourceRef                string                        `json:"source_ref"`
	TargetPath               string                        `json:"target_path"`
	FilesystemSnapshotPath   string                        `json:"filesystem_snapshot_path,omitempty"`
	Reason                   string                        `json:"reason,omitempty"`
	RuntimeOwnershipRule     string                        `json:"runtime_ownership_rule"`
	SuccessorPolicy          ProjectRuntimeSuccessorPolicy `json:"successor_policy"`
	GeneratedAt              time.Time                     `json:"generated_at"`
	Scripts                  []ProjectRuntimeSurface       `json:"scripts,omitempty"`
	Workflows                []ProjectRuntimeSurface       `json:"workflows,omitempty"`
	Connectors               []ProjectRuntimeSurface       `json:"connectors,omitempty"`
	Modules                  []ProjectRuntimeSurface       `json:"modules,omitempty"`
	Schedules                []ProjectRuntimeSurface       `json:"schedules,omitempty"`
	DirectEvents             []ProjectRuntimeSurface       `json:"direct_events,omitempty"`
	WatchedRoots             []ProjectRuntimeWatchedRoot   `json:"watched_roots,omitempty"`
	DeactivationActions      []ProjectRuntimeArchiveAction `json:"deactivation_actions,omitempty"`
	Warnings                 []string                      `json:"warnings,omitempty"`
}

type ProjectRuntimeSurface struct {
	Kind                      string          `json:"kind"`
	Key                       string          `json:"key"`
	Status                    string          `json:"status"`
	Address                   string          `json:"address,omitempty"`
	ProviderAddress           string          `json:"provider_address,omitempty"`
	CapabilityEndpointID      string          `json:"capability_endpoint_id,omitempty"`
	CapabilityEndpointVersion string          `json:"capability_endpoint_version_id,omitempty"`
	RuntimeBindingID          string          `json:"runtime_binding_id,omitempty"`
	BackendRef                string          `json:"backend_ref,omitempty"`
	TargetCapability          string          `json:"target_capability,omitempty"`
	Metadata                  json.RawMessage `json:"metadata,omitempty"`
}

type ProjectRuntimeSuccessorPolicy struct {
	Status                       string `json:"status"`
	RuntimeMigrationRequired     bool   `json:"runtime_migration_required"`
	HistoricalRuntimeDisposition string `json:"historical_runtime_disposition"`
	FutureSuccessorMetadataKey   string `json:"future_successor_metadata_key"`
	FutureDeprecationMetadataKey string `json:"future_deprecation_metadata_key"`
}

type ProjectRuntimeWatchedRoot struct {
	Key              string          `json:"key"`
	Status           string          `json:"status"`
	OwnerNodeKey     string          `json:"owner_node_key,omitempty"`
	BackendRootKey   string          `json:"backend_root_key,omitempty"`
	WorkerKey        string          `json:"worker_key,omitempty"`
	RootRelativePath string          `json:"root_relative_path,omitempty"`
	SyncMode         string          `json:"sync_mode,omitempty"`
	BackupMode       string          `json:"backup_mode,omitempty"`
	IndexMode        string          `json:"index_mode,omitempty"`
	DeleteMode       string          `json:"delete_mode,omitempty"`
	WatchedRootID    string          `json:"watched_root_id,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type ProjectRuntimeArchiveAction struct {
	Facet   string `json:"facet"`
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Ref     string `json:"ref,omitempty"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

func NewProjectRuntimeService(deps ProjectRuntimeDeps) ProjectRuntimeService {
	if deps.PlanEvidence == nil {
		deps.PlanEvidence, _ = deps.Projects.(ProjectPhysicalPlanEvidenceStore)
	}
	return ProjectRuntimeService{
		PlanEvidence:        deps.PlanEvidence,
		Projects:            deps.Projects,
		RepositoryState:     deps.RepositoryState,
		Activation:          deps.Activation,
		StorageArchive:      deps.StorageArchive,
		WorkspaceMove:       deps.WorkspaceMove,
		RuntimeQuiescence:   deps.RuntimeQuiescence,
		WorkspaceRoots:      deps.WorkspaceRoots,
		RuntimeManifestRoot: deps.RuntimeManifestRoot,
		Now:                 deps.Now,
		ArchiveFailureHook:  deps.ArchiveFailureHook,
		RestoreFailureHook:  deps.RestoreFailureHook,
	}
}

func (s ProjectRuntimeService) ArchiveProject(ctx context.Context, req requestctx.Context, projectRef string, input ProjectArchiveInput) (ProjectArchiveResult, error) {
	if !input.DryRun {
		return ProjectArchiveResult{}, fmt.Errorf("legacy project archive writer is retired; build and apply a reviewed physical archive plan")
	}
	if s.Projects == nil {
		return ProjectArchiveResult{}, fmt.Errorf("project archive projects service is not configured")
	}
	if s.Activation == nil {
		return ProjectArchiveResult{}, fmt.Errorf("project archive activation service is not configured")
	}
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, strings.TrimSpace(projectRef))
	if err != nil {
		return ProjectArchiveResult{}, err
	}
	if detail.Registration == nil {
		return ProjectArchiveResult{}, fmt.Errorf("project has no registered project contract")
	}
	sourcePlan := defaultProjectArchiveSource(detail, input.SourceRef)
	if sourcePlan.Err != nil {
		return ProjectArchiveResult{}, sourcePlan.Err
	}
	sourceRef := sourcePlan.SourceRef
	targetPath := strings.Trim(strings.ReplaceAll(input.TargetPath, "\\", "/"), "/")
	if targetPath == "" {
		targetPath = path.Join("main", "Archive", "Projects", detail.Project.Project.Slug)
	}
	now := s.now()
	runtimeManifest := s.buildRuntimeManifest(detail, ids.NewProjectRuntimeArchiveID(), sourceRef, targetPath, strings.TrimSpace(input.Reason), now)
	runtimeManifest.SourceKind = sourcePlan.SourceKind
	warnings := append([]string{}, sourcePlan.Warnings...)

	var storageResult ArchiveResult
	storageArchiveSkipped := input.SkipStorageArchive
	if input.SkipStorageArchive {
		warnings = append(warnings, "storage archive was skipped by request; runtime archive manifest may not correspond to retained project files")
	} else if sourcePlan.SourceKind == projectArchiveSourceKindLocalProjectRoot {
		storageArchiveSkipped = true
		warnings = append(warnings, "the legacy canonical-project tar writer is retired; apply requires a reviewed physical archive plan")
	} else {
		if s.StorageArchive == nil {
			return ProjectArchiveResult{}, fmt.Errorf("project archive storage archive service is not configured")
		}
		storageResult, err = s.StorageArchive.Archive(ctx, ArchiveInput{
			SourceRef:            sourceRef,
			TargetPath:           targetPath,
			ArchiveKey:           "project-" + detail.Project.Project.Slug,
			ArchiveKind:          "project_archive",
			OwnerNodeKey:         "main",
			MarkSourceSuperseded: !input.DryRun,
			DryRun:               input.DryRun,
		})
		if err != nil {
			return ProjectArchiveResult{}, err
		}
		runtimeManifest.StorageArchiveManifestID = storageResult.ArchiveManifest.ArchiveManifestID
	}

	deactivations := []projects.ProjectDeactivationResult{}
	for _, facet := range projectArchiveDeactivationFacets(detail) {
		result, err := s.Activation.Deactivate(ctx, req, detail.Project.Project.ProjectID, projects.DeactivateProjectInput{
			Facet:  facet,
			Reason: firstNonEmpty(strings.TrimSpace(input.Reason), "project archive"),
			DryRun: input.DryRun,
		})
		if err != nil {
			return ProjectArchiveResult{}, err
		}
		deactivations = append(deactivations, result)
		runtimeManifest.DeactivationActions = append(runtimeManifest.DeactivationActions, runtimeArchiveActions(facet, result.Actions)...)
		warnings = append(warnings, result.Warnings...)
	}
	runtimeManifest.Warnings = append(runtimeManifest.Warnings, warnings...)

	manifestPath := s.runtimeManifestPath(detail.Project.Project.Slug, runtimeManifest.ProjectRuntimeArchiveID)
	if !input.DryRun {
		payload, err := json.Marshal(runtimeManifest)
		if err != nil {
			return ProjectArchiveResult{}, fmt.Errorf("encode project runtime archive manifest: %w", err)
		}
		if err := writeManifestFile(manifestPath, payload); err != nil {
			return ProjectArchiveResult{}, fmt.Errorf("write project runtime archive manifest: %w", err)
		}
	}
	archiveState, err := projectRuntimeArchiveState(runtimeManifest, storageResult, manifestPath, input, storageArchiveSkipped, warnings)
	if err != nil {
		return ProjectArchiveResult{}, err
	}
	if !input.DryRun {
		updated, err := s.Projects.UpdateProjectArchiveState(ctx, req, detail.Project.Project.ProjectID, archiveState)
		if err != nil {
			return ProjectArchiveResult{}, err
		}
		detail.Project.Project = updated
	}
	return ProjectArchiveResult{
		Project:                detail,
		StorageArchive:         storageResult,
		RuntimeManifest:        runtimeManifest,
		RuntimeManifestPath:    manifestPath,
		FilesystemSnapshotPath: runtimeManifest.FilesystemSnapshotPath,
		Deactivations:          deactivations,
		ArchiveState:           archiveState,
		SafeToDelete:           projectArchiveSafeToDelete(storageResult, storageArchiveSkipped),
		DryRun:                 input.DryRun,
		StorageArchiveSkipped:  storageArchiveSkipped,
		Warnings:               warnings,
	}, nil
}

func (s ProjectRuntimeService) InspectProjectArchive(ctx context.Context, projectRef string) (ProjectArchiveInspectResult, error) {
	if s.Projects == nil {
		return ProjectArchiveInspectResult{}, fmt.Errorf("project archive projects service is not configured")
	}
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, strings.TrimSpace(projectRef))
	if err != nil {
		return ProjectArchiveInspectResult{}, err
	}
	result := ProjectArchiveInspectResult{
		Project:      detail,
		ArchiveState: detail.Project.Project.ArchiveState,
	}
	if result.Physical = s.inspectProjectPhysicalArchive(ctx, detail); result.Physical != nil {
		return result, nil
	}
	pathValue := runtimeManifestPathFromArchiveState(detail.Project.Project.ArchiveState)
	result.RuntimeManifestPath = pathValue
	if pathValue == "" {
		return result, nil
	}
	payload, err := os.ReadFile(pathValue)
	if err != nil {
		result.Warnings = append(result.Warnings, "runtime archive manifest could not be read: "+err.Error())
		return result, nil
	}
	var manifest ProjectRuntimeArchiveManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		result.Warnings = append(result.Warnings, "runtime archive manifest is not valid JSON: "+err.Error())
		return result, nil
	}
	result.RuntimeManifest = &manifest
	if manifest.SchemaVersion == ProjectRuntimeArchiveManifestSchemaVersion {
		classification := projectHistoricalEvidenceRuntimeManifest
		if strings.TrimSpace(manifest.FilesystemSnapshotPath) != "" {
			classification = projectHistoricalEvidenceTarSnapshot
		}
		result.HistoricalEvidence = &ProjectHistoricalArchiveEvidence{
			SchemaVersion:          ProjectHistoricalArchiveEvidenceSchemaVersion,
			Classification:         classification,
			ReadOnly:               true,
			PhysicalMoveEvidence:   false,
			RuntimeManifestPath:    pathValue,
			FilesystemSnapshotPath: strings.TrimSpace(manifest.FilesystemSnapshotPath),
		}
	}
	return result, nil
}

func (s ProjectRuntimeService) PlanProjectArchiveRestore(ctx context.Context, projectRef string, input ProjectArchiveRestoreInput) (ProjectArchiveRestorePlan, error) {
	if !input.DryRun {
		return ProjectArchiveRestorePlan{}, fmt.Errorf("project archive restore is dry-run only in this slice")
	}
	inspect, err := s.InspectProjectArchive(ctx, projectRef)
	if err != nil {
		return ProjectArchiveRestorePlan{}, err
	}
	if inspect.Physical != nil {
		return ProjectArchiveRestorePlan{}, fmt.Errorf("physical project archive requires its exact reviewed restore plan; legacy restore planning is unsupported")
	}
	toNode := strings.TrimSpace(input.ToNode)
	if toNode == "" {
		toNode = "original owner node"
	}
	steps := []ProjectArchivePlanStep{
		{Key: "storage", Kind: "restore", Status: "would_restore", Summary: "Restore archived project files from main archive to " + toNode + "."},
		{Key: "watch_policy", Kind: "restore", Status: "manual_review_required", Summary: "Re-create watched-root policy from the runtime archive manifest before resuming autonomous sync or backup."},
		{Key: "runtime", Kind: "restore", Status: "not_migrated", Summary: "Capability URLs and runtime ownership remain unchanged until explicit activation or migrate-runtime is performed."},
	}
	warnings := append([]string{}, inspect.Warnings...)
	if len(inspect.ArchiveState) == 0 || string(inspect.ArchiveState) == "{}" {
		warnings = append(warnings, "project has no archive_state recorded")
	}
	return ProjectArchiveRestorePlan{Project: inspect.Project, ArchiveState: inspect.ArchiveState, Steps: steps, Warnings: warnings, DryRun: true}, nil
}

func (s ProjectRuntimeService) PlanProjectRuntimeMigration(ctx context.Context, projectRef string, input ProjectRuntimeMigrationInput) (ProjectRuntimeMigrationPlan, error) {
	if !input.DryRun {
		return ProjectRuntimeMigrationPlan{}, fmt.Errorf("project runtime migration is dry-run only in this slice")
	}
	inspect, err := s.InspectProjectArchive(ctx, projectRef)
	if err != nil {
		return ProjectRuntimeMigrationPlan{}, err
	}
	toNode := strings.TrimSpace(input.ToNode)
	if toNode == "" {
		toNode = "target node"
	}
	steps := []ProjectArchivePlanStep{
		{Key: "runtime_urls", Kind: "migrate-runtime", Status: "would_create_successor_urls", Summary: "Create successor capability URLs on " + toNode + " after runtime compatibility checks."},
		{Key: "historical_urls", Kind: "migrate-runtime", Status: "preserved_disabled", Summary: "Archived capability URLs stay inspectable but disabled; they are not rewritten in place."},
		{Key: "activation", Kind: "migrate-runtime", Status: "manual_review_required", Summary: "Validate scripts, workflows, connectors, modules, schedules, and direct event credentials on the target node before activation."},
	}
	warnings := append([]string{"runtime migration does not transfer ownership in this slice; this is a compatibility report only"}, inspect.Warnings...)
	return ProjectRuntimeMigrationPlan{Project: inspect.Project, ArchiveState: inspect.ArchiveState, Steps: steps, Warnings: warnings, DryRun: true}, nil
}

func ParseProjectRuntimeArchiveState(raw json.RawMessage) (ProjectRuntimeArchiveState, bool) {
	if len(raw) == 0 || string(raw) == "{}" {
		return ProjectRuntimeArchiveState{}, false
	}
	var state ProjectRuntimeArchiveState
	if err := json.Unmarshal(raw, &state); err != nil {
		return ProjectRuntimeArchiveState{}, false
	}
	if strings.TrimSpace(state.Status) == "" &&
		strings.TrimSpace(state.ProjectRuntimeArchiveID) == "" &&
		strings.TrimSpace(state.RuntimeManifestPath) == "" &&
		strings.TrimSpace(state.SourceRef) == "" &&
		strings.TrimSpace(state.TargetPath) == "" {
		return ProjectRuntimeArchiveState{}, false
	}
	return state, true
}

func (s ProjectRuntimeService) buildRuntimeManifest(detail projects.ProjectRegistrationDetail, archiveID, sourceRef, targetPath, reason string, generatedAt time.Time) ProjectRuntimeArchiveManifest {
	manifest := ProjectRuntimeArchiveManifest{
		SchemaVersion:           ProjectRuntimeArchiveManifestSchemaVersion,
		ProjectRuntimeArchiveID: archiveID,
		ProjectID:               detail.Project.Project.ProjectID,
		ProjectSlug:             detail.Project.Project.Slug,
		ProjectName:             detail.Project.Project.Name,
		SourceRef:               sourceRef,
		TargetPath:              targetPath,
		Reason:                  reason,
		RuntimeOwnershipRule:    projectRuntimeOwnershipRule,
		SuccessorPolicy: ProjectRuntimeSuccessorPolicy{
			Status:                       "not_migrated",
			RuntimeMigrationRequired:     true,
			HistoricalRuntimeDisposition: "preserve_disabled_inspectable",
			FutureSuccessorMetadataKey:   "successor_runtime",
			FutureDeprecationMetadataKey: "deprecated_by_project_runtime_archive",
		},
		GeneratedAt: generatedAt,
	}
	if detail.Registration != nil {
		manifest.RegistrationID = detail.Registration.ProjectContractRegistrationID
		manifest.RegistrationRevision = detail.Registration.RegistrationRevision
	}
	for _, exposure := range detail.ScriptExposures {
		manifest.Scripts = append(manifest.Scripts, ProjectRuntimeSurface{
			Kind:                      "script",
			Key:                       exposure.ScriptKey,
			Status:                    exposure.ActivationStatus,
			Address:                   exposure.CapabilityAddress,
			ProviderAddress:           exposure.ProviderAddress,
			CapabilityEndpointID:      stringPtrValue(exposure.CapabilityEndpointID),
			CapabilityEndpointVersion: stringPtrValue(exposure.CapabilityEndpointVersionID),
			RuntimeBindingID:          stringPtrValue(exposure.RuntimeBindingID),
			Metadata:                  exposure.Metadata,
		})
	}
	for _, registration := range detail.WorkflowRegistrations {
		manifest.Workflows = append(manifest.Workflows, ProjectRuntimeSurface{
			Kind:                      "workflow",
			Key:                       registration.WorkflowKey,
			Status:                    registration.ActivationStatus,
			Address:                   registration.CapabilityAddress,
			ProviderAddress:           registration.ProviderAddress,
			CapabilityEndpointID:      stringPtrValue(registration.CapabilityEndpointID),
			CapabilityEndpointVersion: stringPtrValue(registration.CapabilityEndpointVersionID),
			RuntimeBindingID:          stringPtrValue(registration.RuntimeBindingID),
			Metadata:                  registration.Metadata,
		})
	}
	for _, registration := range detail.ConnectorRegistrations {
		manifest.Connectors = append(manifest.Connectors, ProjectRuntimeSurface{
			Kind:            "connector",
			Key:             registration.ConnectorKey,
			Status:          registration.ActivationStatus,
			Address:         registration.ProviderAddress,
			ProviderAddress: registration.ProviderAddress,
			BackendRef:      stringPtrValue(registration.ProviderID),
			Metadata:        registration.Metadata,
		})
	}
	for _, registration := range detail.ModuleRegistrations {
		manifest.Modules = append(manifest.Modules, ProjectRuntimeSurface{
			Kind:       "module",
			Key:        registration.ModuleKey,
			Status:     registration.ActivationStatus,
			Address:    registration.ModuleID,
			BackendRef: stringPtrValue(registration.ModulePackageID),
			Metadata:   registration.Metadata,
		})
	}
	for _, registration := range detail.ScheduleRegistrations {
		manifest.Schedules = append(manifest.Schedules, ProjectRuntimeSurface{
			Kind:             "schedule",
			Key:              registration.ScheduleKey,
			Status:           registration.ActivationStatus,
			BackendRef:       firstNonEmpty(stringPtrValue(registration.ScheduleID), registration.BackendScheduleKey),
			TargetCapability: registration.TargetCapability,
			Metadata:         registration.Metadata,
		})
	}
	for _, registration := range detail.DirectEventRegistrations {
		manifest.DirectEvents = append(manifest.DirectEvents, ProjectRuntimeSurface{
			Kind:             "direct_event",
			Key:              registration.EventKey,
			Status:           registration.ActivationStatus,
			BackendRef:       firstNonEmpty(stringPtrValue(registration.EndpointID), registration.BackendEndpointSlug),
			TargetCapability: registration.TargetCapability,
			Metadata:         registration.Metadata,
		})
	}
	for _, registration := range detail.WatchedRootRegistrations {
		manifest.WatchedRoots = append(manifest.WatchedRoots, ProjectRuntimeWatchedRoot{
			Key:              registration.LocalRootKey,
			Status:           registration.ActivationStatus,
			OwnerNodeKey:     registration.OwnerNodeKey,
			BackendRootKey:   registration.BackendRootKey,
			WorkerKey:        registration.WorkerKey,
			RootRelativePath: registration.RootRelativePath,
			SyncMode:         registration.SyncMode,
			BackupMode:       registration.BackupMode,
			IndexMode:        registration.IndexMode,
			DeleteMode:       registration.DeleteMode,
			WatchedRootID:    stringPtrValue(registration.WatchedRootID),
			Metadata:         registration.Metadata,
		})
	}
	return manifest
}

func (s ProjectRuntimeService) runtimeManifestPath(projectSlug, archiveID string) string {
	root := strings.TrimSpace(s.RuntimeManifestRoot)
	if root == "" {
		root = filepath.Join(".loom", "storage-archive", "runtime-manifests")
	}
	return filepath.Join(root, sanitizeArchivePathSegment(projectSlug), archiveID+".json")
}

func (s ProjectRuntimeService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type projectArchiveSourcePlan struct {
	SourceRef  string
	SourceKind string
	LocalPath  string
	Warnings   []string
	Err        error
}

func defaultProjectArchiveSource(detail projects.ProjectRegistrationDetail, override string) projectArchiveSourcePlan {
	override = strings.Trim(strings.ReplaceAll(override, "\\", "/"), "/")
	if override != "" {
		return projectArchiveSourcePlan{SourceRef: override, SourceKind: projectArchiveSourceKindStorageView}
	}
	warnings := []string{}
	if len(detail.WatchedRootRegistrations) == 0 {
		warnings = append(warnings, "project has no watched-root registrations; archive captures the registered local project root")
	} else {
		warnings = append(warnings, "project watched roots publish item custody rather than a synthetic project backup view; archive captures the registered local project root")
	}
	if detail.Registration == nil {
		return projectArchiveSourcePlan{Err: fmt.Errorf("project archive requires a registered project root")}
	}
	projectRoot := strings.TrimSpace(detail.Registration.ProjectRoot)
	if projectRoot == "" {
		return projectArchiveSourcePlan{Err: fmt.Errorf("project archive registered project root is empty")}
	}
	info, err := os.Lstat(projectRoot)
	if err != nil {
		return projectArchiveSourcePlan{Err: fmt.Errorf("inspect registered project root: %w", err)}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return projectArchiveSourcePlan{Err: fmt.Errorf("registered project root must be a real directory: %s", projectRoot)}
	}
	return projectArchiveSourcePlan{
		SourceRef:  projectRoot,
		SourceKind: projectArchiveSourceKindLocalProjectRoot,
		LocalPath:  projectRoot,
		Warnings:   warnings,
	}
}

func projectArchiveDeactivationFacets(detail projects.ProjectRegistrationDetail) []string {
	facets := []string{}
	if len(detail.ScriptExposures) > 0 {
		facets = append(facets, "scripts")
	}
	if len(detail.WorkflowRegistrations) > 0 {
		facets = append(facets, "workflows")
	}
	if len(detail.ConnectorRegistrations) > 0 {
		facets = append(facets, "connectors")
	}
	if len(detail.ScheduleRegistrations) > 0 {
		facets = append(facets, "schedules")
	}
	if len(detail.DirectEventRegistrations) > 0 {
		facets = append(facets, "direct_events")
	}
	if len(detail.WatchedRootRegistrations) > 0 {
		facets = append(facets, "watched_roots")
	}
	if len(detail.ModuleRegistrations) > 0 {
		facets = append(facets, "modules")
	}
	for _, facet := range detail.Facets {
		if facet.FacetKey == "services" && facet.Enabled && facet.Present && facet.FacetStatus == projects.ProjectFacetStatusActivated {
			facets = append(facets, "services")
			break
		}
	}
	return facets
}

func runtimeArchiveActions(facet string, actions []projects.ProjectDeactivationAction) []ProjectRuntimeArchiveAction {
	out := make([]ProjectRuntimeArchiveAction, 0, len(actions))
	for _, action := range actions {
		out = append(out, ProjectRuntimeArchiveAction{
			Facet:   facet,
			Kind:    action.Kind,
			Key:     action.Key,
			Ref:     action.Ref,
			Status:  action.Status,
			Summary: action.Summary,
		})
	}
	return out
}

func projectRuntimeArchiveState(manifest ProjectRuntimeArchiveManifest, storageResult ArchiveResult, runtimeManifestPath string, input ProjectArchiveInput, storageArchiveSkipped bool, warnings []string) (json.RawMessage, error) {
	state := map[string]any{
		"schema_version":             projectRuntimeArchiveStateSchemaVersion,
		"status":                     "archived",
		"project_runtime_archive_id": manifest.ProjectRuntimeArchiveID,
		"runtime_manifest_path":      runtimeManifestPath,
		"runtime_ownership_rule":     projectRuntimeOwnershipRule,
		"successor_policy":           manifest.SuccessorPolicy,
		"source_kind":                manifest.SourceKind,
		"source_ref":                 manifest.SourceRef,
		"target_path":                manifest.TargetPath,
		"archived_at":                manifest.GeneratedAt,
		"storage_archive_skipped":    storageArchiveSkipped,
		"warnings":                   warnings,
	}
	if strings.TrimSpace(manifest.FilesystemSnapshotPath) != "" {
		state["filesystem_snapshot_path"] = manifest.FilesystemSnapshotPath
	}
	if input.DryRun {
		state["status"] = "planned"
		state["dry_run"] = true
	}
	if storageResult.ArchiveManifest.ArchiveManifestID != "" {
		state["storage_archive_manifest_id"] = storageResult.ArchiveManifest.ArchiveManifestID
		state["storage_manifest_path"] = storageResult.ManifestPath
		state["safe_to_delete"] = projectArchiveSafeToDelete(storageResult, storageArchiveSkipped)
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(payload), nil
}

func projectArchiveSafeToDelete(result ArchiveResult, skipped bool) bool {
	if skipped {
		return false
	}
	if len(result.Entries) == 0 {
		return false
	}
	if len(result.SafeToDelete) == 0 {
		return false
	}
	for _, safe := range result.SafeToDelete {
		if !safe.Safe {
			return false
		}
	}
	return true
}

func runtimeManifestPathFromArchiveState(raw json.RawMessage) string {
	state, ok := ParseProjectRuntimeArchiveState(raw)
	if !ok {
		return ""
	}
	return strings.TrimSpace(state.RuntimeManifestPath)
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func sanitizeArchivePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "project"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "..", "-")
	return strings.Trim(replacer.Replace(value), ". ")
}
