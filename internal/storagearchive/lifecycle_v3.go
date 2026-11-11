package storagearchive

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"loom.local/loom/internal/projects"
)

const (
	WorkspaceArchivePlanSchemaVersionV3       = "storage.workspace_archive_plan.v3"
	WorkspaceArchiveOperationSchemaVersionV3  = "storage.workspace_archive_operation.v3"
	WorkspaceArchiveManifestSchemaVersionV3   = "storage.workspace_archive_manifest.v3"
	WorkspaceLifecycleEventSchemaVersionV3    = "storage.workspace_lifecycle_event.v3"
	ProjectPhysicalArchivePlanSchemaVersionV3 = "storage.project_physical_archive_plan.v3"
	ProjectPhysicalRestorePlanSchemaVersionV3 = "storage.project_physical_restore_plan.v3"
)

var workspaceLocationID = regexp.MustCompile(`^[a-z][A-Za-z0-9_]{2,159}$`)

// WorkspaceActiveLocation is accepted by the project location owner, never
// inferred from a directory name. A restore preserves this exact archive proof.
type WorkspaceActiveLocation struct {
	ProjectID                string           `json:"project_id"`
	Path                     WorkspacePathRef `json:"path"`
	LocationID               string           `json:"location_id"`
	RegistrationRevision     int64            `json:"registration_revision"`
	RepositorySourceRevision int64            `json:"repository_source_revision"`
}

func ValidateWorkspaceActiveLocation(location WorkspaceActiveLocation, roots TrustedWorkspaceRoots) error {
	if !workspaceProjectIDPattern.MatchString(location.ProjectID) || !workspaceLocationID.MatchString(location.LocationID) || location.RegistrationRevision < 1 || location.RepositorySourceRevision < 0 {
		return fmt.Errorf("invalid accepted project location identity or revisions")
	}
	if err := validateWorkspaceV3ActivePath(location.Path); err != nil {
		return err
	}
	return validateWorkspaceRefForRoots(location.Path, roots)
}

// ResolveActiveProjectLocation reads only the trusted owner callback. Nil is an
// unmoved project; callers must not replace unavailable evidence with a guess.
func (s WorkspaceMoveService) ResolveActiveProjectLocation(ctx context.Context, projectID string) (*WorkspaceActiveLocation, error) {
	if s.ActiveProjectLocation == nil {
		return nil, nil
	}
	location, err := s.ActiveProjectLocation(ctx, projectID)
	if err != nil || location == nil {
		return nil, err
	}
	accepted := *location
	if err := ValidateWorkspaceActiveLocation(accepted, s.Roots); err != nil {
		return nil, err
	}
	if accepted.ProjectID != projectID {
		return nil, fmt.Errorf("accepted location belongs to another project")
	}
	return &accepted, nil
}

func cloneWorkspaceActiveLocation(location *WorkspaceActiveLocation) *WorkspaceActiveLocation {
	if location == nil {
		return nil
	}
	copy := *location
	return &copy
}

func sameWorkspaceActiveLocation(a, b *WorkspaceActiveLocation) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func validateWorkspaceV3ActivePath(ref WorkspacePathRef) error {
	relative := ref.RelativePath
	if !strings.HasPrefix(relative, "Projects/") || path.Clean(relative) != relative || strings.ContainsAny(relative, "\\\x00") {
		return fmt.Errorf("accepted active path must be a clean descendant of Box/Projects")
	}
	for _, component := range strings.Split(relative, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("invalid accepted active path component")
		}
	}
	return validateMappedPathRef("accepted active path", ref, WorkspaceRootBox, relative)
}

func validateWorkspaceV3Paths(operation WorkspaceOperationKind, kind WorkspaceKind, slug string, source, destination WorkspacePathRef) error {
	if kind != WorkspaceKindProject {
		return fmt.Errorf("v3 active locations support only projects")
	}
	if err := ValidateWorkspaceSlug(slug); err != nil {
		return err
	}
	active, payload := source, destination
	if operation == WorkspaceOperationRestore {
		active, payload = destination, source
	} else if operation != WorkspaceOperationArchive {
		return fmt.Errorf("unsupported workspace operation %q", operation)
	}
	if err := validateWorkspaceV3ActivePath(active); err != nil {
		return err
	}
	return validateWorkspaceV2Payload(payload, slug, false)
}

func validateWorkspacePlanActiveLocation(plan WorkspaceArchivePlan, roots TrustedWorkspaceRoots) error {
	if plan.SchemaVersion != WorkspaceArchivePlanSchemaVersionV3 {
		if plan.ActiveLocation != nil {
			return fmt.Errorf("historical workspace plan cannot contain active location evidence")
		}
		return nil
	}
	if plan.ActiveLocation == nil || plan.Kind != WorkspaceKindProject || plan.ActiveLocation.ProjectID != plan.ObjectID {
		return fmt.Errorf("v3 plan requires its exact project active location")
	}
	if err := ValidateWorkspaceActiveLocation(*plan.ActiveLocation, roots); err != nil {
		return err
	}
	active := plan.Source.Path
	if plan.OperationKind == WorkspaceOperationRestore {
		active = plan.Destination.Path
	}
	if active != plan.ActiveLocation.Path {
		return fmt.Errorf("v3 plan contradicts accepted active location")
	}
	if err := validateWorkspaceV3Paths(plan.OperationKind, plan.Kind, plan.Slug, plan.Source.Path, plan.Destination.Path); err != nil {
		return err
	}
	if plan.Placement == nil {
		return fmt.Errorf("v3 plan requires independent archive metadata placement")
	}
	id := plan.OperationID
	if plan.OperationKind == WorkspaceOperationRestore {
		id = plan.ArchiveOperationID
	}
	if plan.Placement.Metadata.Path.RelativePath != ".loom/workspace-archives/"+id || plan.Placement.RelocationID != "" {
		return fmt.Errorf("v3 plan must preserve its named archive payload and operation metadata")
	}
	return nil
}

// The plan digest commits the accepted location proof. Operations, manifests
// and events retain that digest/path chain rather than minting new authority.
func WorkspaceArchiveManifestSchemaForPlan(plan WorkspaceArchivePlan) string {
	if plan.SchemaVersion == WorkspaceArchivePlanSchemaVersionV3 {
		return WorkspaceArchiveManifestSchemaVersionV3
	}
	if plan.Placement != nil {
		return WorkspaceArchiveManifestSchemaVersionV2
	}
	return WorkspaceArchiveManifestSchemaVersion
}

func validateProjectActiveLocationSnapshot(location *WorkspaceActiveLocation, custody projects.ProjectArchiveCustodyBinding, detail projects.ProjectRegistrationDetail, repository projects.ProjectRepositoryReadModel) error {
	if location == nil {
		return nil
	}
	registration := detail.Registration
	if registration == nil || location.ProjectID != custody.ProjectID || location.ProjectID != detail.Project.Project.ProjectID || location.ProjectID != repository.Project.ProjectID ||
		registration.ProjectID != location.ProjectID || registration.ProjectContractRegistrationID != custody.RegistrationID ||
		location.Path.AbsolutePath != custody.CanonicalRoot || location.Path.AbsolutePath != custody.RegisteredRoot || location.Path.AbsolutePath != registration.ProjectRoot ||
		location.RegistrationRevision != int64(registration.RegistrationRevision) || location.RegistrationRevision != int64(custody.RegistrationRevision) ||
		location.RepositorySourceRevision != custody.RepositorySourceRevision {
		return fmt.Errorf("accepted active location contradicts project registration or custody revisions")
	}
	if repository.Source == nil {
		if location.RepositorySourceRevision != 0 {
			return fmt.Errorf("accepted active location invents a repository source revision")
		}
	} else if repository.Source.SourceRevision != location.RepositorySourceRevision || repository.Source.ProjectContractRegistrationID != custody.RegistrationID ||
		(repository.Source.ProjectRoot != "" && repository.Source.ProjectRoot != location.Path.AbsolutePath) ||
		custody.RepositoryProjectRoot != location.Path.AbsolutePath {
		return fmt.Errorf("accepted active location contradicts repository source")
	}
	return nil
}
