package storagearchive

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	WorkspaceArchivePlanSchemaVersionV2      = "storage.workspace_archive_plan.v2"
	WorkspaceArchiveOperationSchemaVersionV2 = "storage.workspace_archive_operation.v2"
	WorkspaceArchiveManifestSchemaVersionV2  = "storage.workspace_archive_manifest.v2"
	WorkspaceLifecycleEventSchemaVersionV2   = "storage.workspace_lifecycle_event.v2"
)

var workspaceRelocationID = regexp.MustCompile(`^retained_relocation_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

func WorkspaceArchiveOperationSchemaForPlan(plan WorkspaceArchivePlan) string {
	if plan.SchemaVersion == WorkspaceArchivePlanSchemaVersionV3 {
		return WorkspaceArchiveOperationSchemaVersionV3
	}
	if plan.SchemaVersion == WorkspaceArchivePlanSchemaVersionV2 {
		return WorkspaceArchiveOperationSchemaVersionV2
	}
	return WorkspaceArchiveOperationSchemaVersion
}

func WorkspaceLifecycleEventSchemaForPlan(plan WorkspaceArchivePlan) string {
	if plan.SchemaVersion == WorkspaceArchivePlanSchemaVersionV3 {
		return WorkspaceLifecycleEventSchemaVersionV3
	}
	if plan.SchemaVersion == WorkspaceArchivePlanSchemaVersionV2 {
		return WorkspaceLifecycleEventSchemaVersionV2
	}
	return WorkspaceLifecycleEventSchemaVersion
}

// ResolveWorkspacePlanPaths never infers placement from filesystem existence.
// ArchiveContainer and ArchiveManifest refer to metadata, not a payload parent.
func ResolveWorkspacePlanPaths(roots TrustedWorkspaceRoots, plan WorkspaceArchivePlan) (ResolvedWorkspacePaths, error) {
	if err := validateWorkspaceBoxBinding(plan.BoxID, roots); err != nil {
		return ResolvedWorkspacePaths{}, err
	}
	if err := validateWorkspacePlanActiveLocation(plan, roots); err != nil {
		return ResolvedWorkspacePaths{}, err
	}
	paths, err := ResolveWorkspacePaths(roots, plan.Kind, plan.Slug)
	if err != nil {
		return ResolvedWorkspacePaths{}, err
	}
	if plan.SchemaVersion == WorkspaceArchivePlanSchemaVersion {
		if plan.Placement != nil {
			return ResolvedWorkspacePaths{}, fmt.Errorf("v1 plan cannot contain placement evidence")
		}
		return paths, nil
	}
	if plan.SchemaVersion != WorkspaceArchivePlanSchemaVersionV2 && plan.SchemaVersion != WorkspaceArchivePlanSchemaVersionV3 {
		return ResolvedWorkspacePaths{}, fmt.Errorf("unsupported workspace archive plan schema %q", plan.SchemaVersion)
	}
	if plan.SchemaVersion == WorkspaceArchivePlanSchemaVersionV3 {
		paths.Active = plan.ActiveLocation.Path
		paths.Mapping.ActiveRelativePath = paths.Active.RelativePath
	} else {
		if err := validateWorkspaceV2Paths(plan.OperationKind, plan.Kind, plan.Slug, plan.Source.Path, plan.Destination.Path); err != nil {
			return ResolvedWorkspacePaths{}, err
		}
	}
	if err := validateWorkspaceMetadataPath(plan.OperationKind, plan.OperationID, plan.ArchiveOperationID, plan.Slug, plan.Placement); err != nil {
		return ResolvedWorkspacePaths{}, err
	}
	for _, ref := range []WorkspacePathRef{plan.Source.Path, plan.Destination.Path, plan.Placement.Metadata.Path} {
		if err := validateWorkspaceRefForRoots(ref, roots); err != nil {
			return ResolvedWorkspacePaths{}, err
		}
	}
	payload := plan.Destination.Path
	if plan.OperationKind == WorkspaceOperationRestore {
		payload = plan.Source.Path
	}
	paths.ArchivePayload = payload
	paths.ArchiveContainer = plan.Placement.Metadata.Path
	paths.ArchiveManifest = WorkspacePathRef{
		Root: WorkspaceRootStorage, RelativePath: path.Join(paths.ArchiveContainer.RelativePath, "archive.json"),
		AbsolutePath: filepath.Join(paths.ArchiveContainer.AbsolutePath, "archive.json"),
	}
	paths.Mapping.ArchivePayloadPath = paths.ArchivePayload.RelativePath
	paths.Mapping.ArchiveContainerPath = paths.ArchiveContainer.RelativePath
	paths.Mapping.ArchiveManifestPath = paths.ArchiveManifest.RelativePath
	return paths, nil
}

func validateWorkspaceV2Paths(operation WorkspaceOperationKind, kind WorkspaceKind, slug string, source, destination WorkspacePathRef) error {
	if kind != WorkspaceKindProject {
		return fmt.Errorf("v2 workspace placement supports only projects")
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
	if err := validateMappedPathRef("active payload", active, WorkspaceRootBox, "Projects/"+slug); err != nil {
		return err
	}
	return validateWorkspaceV2Payload(payload, slug, operation == WorkspaceOperationRestore)
}

func validateWorkspaceV2Payload(ref WorkspacePathRef, slug string, allowLegacy bool) error {
	relative := "Projects/" + slug
	if allowLegacy && ref.RelativePath == "archive/projects/"+slug+"/project" {
		relative = ref.RelativePath
	}
	return validateMappedPathRef("archive payload", ref, WorkspaceRootStorage, relative)
}

func validateWorkspaceMetadataPath(operation WorkspaceOperationKind, operationID, archiveID, slug string, placement *WorkspaceArchivePlacement) error {
	if placement == nil {
		return fmt.Errorf("v2 workspace placement is required")
	}
	if !workspaceOperationID.MatchString(operationID) {
		return fmt.Errorf("invalid workspace archive operation_id")
	}
	ref := placement.Metadata.Path
	relative := ".loom/workspace-archives/" + operationID
	if operation == WorkspaceOperationRestore {
		if ref.RelativePath == "archive/projects/"+slug {
			relative = ref.RelativePath
		} else {
			id := strings.TrimPrefix(ref.RelativePath, ".loom/workspace-archives/")
			if !workspaceOperationID.MatchString(id) || id == operationID || (archiveID != "" && id != archiveID) {
				return fmt.Errorf("metadata must name the original archive operation")
			}
			relative = ".loom/workspace-archives/" + id
		}
		if placement.RelocationID != "" && !workspaceRelocationID.MatchString(placement.RelocationID) {
			return fmt.Errorf("invalid placement relocation_id")
		}
	} else if operation != WorkspaceOperationArchive || placement.RelocationID != "" {
		return fmt.Errorf("only restore placement may bind a relocation")
	}
	return validateMappedPathRef("metadata", ref, WorkspaceRootStorage, relative)
}

func validateWorkspacePlanPlacement(plan WorkspaceArchivePlan) error {
	if err := validateWorkspaceOperationPlacement(WorkspaceArchiveOperation{
		OperationID: plan.OperationID, OperationKind: plan.OperationKind, Slug: plan.Slug,
		Source: plan.Source, Destination: plan.Destination, Placement: plan.Placement,
	}); err != nil {
		return err
	}
	device := plan.Source.Identity.DeviceID
	if err := validateWorkspaceAncestorChain("source", plan.Source.Path, plan.Source.ParentIdentity, plan.SourceAncestors, device); err != nil {
		return err
	}
	if err := validateWorkspaceAncestorChain("destination", plan.Destination.Path, plan.Destination.ParentIdentity, plan.DestinationAncestors, device); err != nil {
		return err
	}
	// Shared ancestors must describe one object, even when payload and metadata
	// have different immediate parents on the same trusted root.
	seen := map[string]PathIdentity{}
	for _, item := range []struct {
		binding   WorkspacePathBinding
		ancestors []WorkspaceAncestorBinding
	}{
		{plan.Source, plan.SourceAncestors}, {plan.Destination, plan.DestinationAncestors},
		{plan.Placement.Metadata, plan.Placement.MetadataAncestors},
	} {
		bindings := append([]WorkspaceAncestorBinding(nil), item.ancestors...)
		if item.binding.Identity.Presence == PathPresent {
			bindings = append(bindings, WorkspaceAncestorBinding{RelativePath: item.binding.Path.RelativePath, Identity: item.binding.Identity})
		}
		for _, ancestor := range bindings {
			key := string(item.binding.Path.Root) + ":" + ancestor.RelativePath
			if previous, ok := seen[key]; ok && !SamePathIdentity(previous, ancestor.Identity) {
				return fmt.Errorf("contradictory identity for shared ancestor %q", key)
			}
			seen[key] = ancestor.Identity
		}
	}
	return nil
}

func validateWorkspaceOperationPaths(operation WorkspaceArchiveOperation) error {
	if operation.SchemaVersion == WorkspaceArchiveOperationSchemaVersionV3 {
		if !workspaceProjectIDPattern.MatchString(operation.ObjectID) {
			return fmt.Errorf("v3 operation requires a stable project identity")
		}
		return validateWorkspaceV3Paths(operation.OperationKind, operation.Kind, operation.Slug, operation.Source.Path, operation.Destination.Path)
	}
	if operation.SchemaVersion == WorkspaceArchiveOperationSchemaVersionV2 {
		return validateWorkspaceV2Paths(operation.OperationKind, operation.Kind, operation.Slug, operation.Source.Path, operation.Destination.Path)
	}
	return validateMappedBindings(operation.OperationKind, operation.Kind, operation.Slug, operation.Source, operation.Destination)
}

func validateWorkspaceOperationPlacement(operation WorkspaceArchiveOperation) error {
	if operation.SchemaVersion == WorkspaceArchiveOperationSchemaVersionV3 {
		if operation.Placement == nil || operation.Placement.RelocationID != "" {
			return fmt.Errorf("v3 operation requires original named placement")
		}
		metadata := operation.Placement.Metadata.Path.RelativePath
		if !strings.HasPrefix(metadata, ".loom/workspace-archives/") {
			return fmt.Errorf("v3 operation requires operation-scoped metadata")
		}
	}
	if err := validateWorkspaceMetadataPath(operation.OperationKind, operation.OperationID, "", operation.Slug, operation.Placement); err != nil {
		return err
	}
	presence := PathAbsent
	if operation.OperationKind == WorkspaceOperationRestore {
		presence = PathPresent
	}
	metadata := operation.Placement.Metadata
	if err := validatePathBinding("metadata", metadata, metadata.Path, presence); err != nil {
		return err
	}
	device := operation.Source.Identity.DeviceID
	if operation.Source.ParentIdentity.DeviceID != device || operation.Destination.ParentIdentity.DeviceID != device ||
		metadata.ParentIdentity.DeviceID != device || (presence == PathPresent && metadata.Identity.DeviceID != device) {
		return fmt.Errorf("payload and metadata parents must be on the same filesystem")
	}
	if err := validateWorkspaceAncestorChain("metadata", metadata.Path, metadata.ParentIdentity, operation.Placement.MetadataAncestors, device); err != nil {
		return err
	}
	// The two Storage references must have the same base even without roots.
	payload := operation.Destination
	if operation.OperationKind == WorkspaceOperationRestore {
		payload = operation.Source
		if payload.Path.RelativePath == "Projects/"+operation.Slug && metadata.Path.RelativePath == "archive/projects/"+operation.Slug && operation.Placement.RelocationID == "" {
			return fmt.Errorf("relocated legacy payload requires placement relocation_id")
		}
	}
	if workspaceRefBase(payload.Path) != workspaceRefBase(metadata.Path) {
		return fmt.Errorf("payload and metadata do not share a Storage root")
	}
	if path.Dir(payload.Path.RelativePath) == metadata.Path.RelativePath && !SamePathIdentity(payload.ParentIdentity, metadata.Identity) {
		return fmt.Errorf("legacy payload parent does not match metadata identity")
	}
	return nil
}

func validateWorkspaceAncestorChain(label string, ref WorkspacePathRef, parent PathIdentity, bindings []WorkspaceAncestorBinding, device uint64) error {
	parts := strings.Split(path.Dir(ref.RelativePath), "/")
	if len(bindings) != len(parts)+1 {
		return fmt.Errorf("%s ancestors must bind the complete chain to its parent", label)
	}
	relative := "."
	for i, binding := range bindings {
		if i > 0 {
			relative = path.Join(relative, parts[i-1])
		}
		if binding.RelativePath != relative {
			return fmt.Errorf("%s ancestor %d does not match its named parent chain", label, i)
		}
		if err := validatePathIdentity(binding.Identity, PathPresent, InventoryEntryDirectory); err != nil {
			return fmt.Errorf("%s ancestor %q: %w", label, relative, err)
		}
		if binding.Identity.DeviceID != device {
			return fmt.Errorf("%s ancestors cross a filesystem boundary", label)
		}
	}
	if !SamePathIdentity(bindings[len(bindings)-1].Identity, parent) {
		return fmt.Errorf("%s ancestor chain does not match its parent identity", label)
	}
	return nil
}

func workspaceRefBase(ref WorkspacePathRef) string {
	return strings.TrimSuffix(ref.AbsolutePath, filepath.FromSlash(ref.RelativePath))
}

func validateWorkspaceRefForRoots(ref WorkspacePathRef, roots TrustedWorkspaceRoots) error {
	base := roots.BoxRoot
	if ref.Root == WorkspaceRootStorage {
		base = roots.StorageRoot
	} else if ref.Root != WorkspaceRootBox {
		return fmt.Errorf("unsupported workspace root %q", ref.Root)
	}
	if ref.AbsolutePath != filepath.Join(base, filepath.FromSlash(ref.RelativePath)) {
		return fmt.Errorf("path does not match exact trusted workspace root")
	}
	return nil
}

func validateWorkspaceOperationRoots(operation WorkspaceArchiveOperation, roots TrustedWorkspaceRoots) error {
	if _, err := ResolveWorkspacePaths(roots, operation.Kind, operation.Slug); err != nil {
		return err
	}
	for _, ref := range []WorkspacePathRef{operation.Source.Path, operation.Destination.Path, operation.Placement.Metadata.Path} {
		if err := validateWorkspaceRefForRoots(ref, roots); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspaceManifestPaths(manifest WorkspaceArchiveManifest) error {
	if manifest.SchemaVersion == WorkspaceArchiveManifestSchemaVersion {
		mapping, _ := WorkspaceMapping(manifest.Kind, manifest.Slug)
		return validateMappedPathRef("archive_payload_path", manifest.ArchivePayloadPath, mapping.ArchiveRoot, mapping.ArchivePayloadPath)
	}
	if manifest.SchemaVersion == WorkspaceArchiveManifestSchemaVersionV3 {
		if !workspaceProjectIDPattern.MatchString(manifest.ObjectID) {
			return fmt.Errorf("v3 manifest requires a stable project identity")
		}
		if err := validateWorkspaceV2Payload(manifest.ArchivePayloadPath, manifest.Slug, false); err != nil {
			return err
		}
		if manifest.RestoredFromPath != nil && *manifest.RestoredFromPath != manifest.ArchivePayloadPath {
			return fmt.Errorf("v3 restore must use its original named archive payload")
		}
	}
	if manifest.Kind != WorkspaceKindProject {
		return fmt.Errorf("v2 workspace manifests support only projects")
	}
	if err := validateWorkspaceV2Payload(manifest.ArchivePayloadPath, manifest.Slug, manifest.LifecycleState == WorkspaceLifecycleActive); err != nil {
		return err
	}
	if manifest.RestoredFromPath != nil {
		if manifest.LifecycleState != WorkspaceLifecycleActive {
			return fmt.Errorf("archived manifest cannot contain restored_from_path")
		}
		if err := validateWorkspaceV2Payload(*manifest.RestoredFromPath, manifest.Slug, true); err != nil {
			return err
		}
		if workspaceRefBase(*manifest.RestoredFromPath) != workspaceRefBase(manifest.ArchivePayloadPath) {
			return fmt.Errorf("restored_from_path does not share the historical Storage root")
		}
	}
	if manifest.RestoreOperationID == manifest.ArchiveOperationID {
		return fmt.Errorf("restore and archive operation identities must be distinct")
	}
	return nil
}

func validateWorkspaceManifestRoots(manifest WorkspaceArchiveManifest, roots TrustedWorkspaceRoots) error {
	if _, err := ResolveWorkspacePaths(roots, manifest.Kind, manifest.Slug); err != nil {
		return err
	}
	refs := []WorkspacePathRef{manifest.ActivePath, manifest.ArchivePayloadPath}
	if manifest.RestoredFromPath != nil {
		refs = append(refs, *manifest.RestoredFromPath)
	}
	for _, ref := range refs {
		if err := validateWorkspaceRefForRoots(ref, roots); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspaceManifestOperationSchemas(manifest WorkspaceArchiveManifest, archive WorkspaceArchiveOperation, restore *WorkspaceArchiveOperation) error {
	if manifest.SchemaVersion == WorkspaceArchiveManifestSchemaVersionV3 || archive.SchemaVersion == WorkspaceArchiveOperationSchemaVersionV3 || restore != nil && restore.SchemaVersion == WorkspaceArchiveOperationSchemaVersionV3 {
		if manifest.SchemaVersion != WorkspaceArchiveManifestSchemaVersionV3 || archive.SchemaVersion != WorkspaceArchiveOperationSchemaVersionV3 || (restore != nil && restore.SchemaVersion != WorkspaceArchiveOperationSchemaVersionV3) {
			return fmt.Errorf("v3 manifest requires its v3 archive and restore operations")
		}
		return nil
	}
	archiveV2 := archive.SchemaVersion == WorkspaceArchiveOperationSchemaVersionV2
	manifestV2 := manifest.SchemaVersion == WorkspaceArchiveManifestSchemaVersionV2
	if manifest.LifecycleState == WorkspaceLifecycleArchived {
		if archiveV2 != manifestV2 {
			return fmt.Errorf("manifest schema does not match its archive operation")
		}
	} else if restore != nil {
		restoreV2 := restore.SchemaVersion == WorkspaceArchiveOperationSchemaVersionV2
		if restoreV2 != manifestV2 || (archiveV2 && !restoreV2) {
			return fmt.Errorf("manifest schema does not match its restore operation")
		}
	}
	return nil
}

func workspaceLifecycleMapping(event WorkspaceLifecycleEvent) (WorkspacePathMapping, error) {
	mapping, err := WorkspaceMapping(event.Kind, event.Slug)
	if err != nil || event.SchemaVersion == WorkspaceLifecycleEventSchemaVersion {
		return mapping, err
	}
	operation := WorkspaceOperationArchive
	if event.Transition == WorkspaceTransitionRestored {
		operation = WorkspaceOperationRestore
	}
	if event.SchemaVersion == WorkspaceLifecycleEventSchemaVersionV3 {
		if !workspaceProjectIDPattern.MatchString(event.ObjectID) {
			return WorkspacePathMapping{}, fmt.Errorf("v3 event requires a stable project identity")
		}
		if err := validateWorkspaceV3Paths(operation, event.Kind, event.Slug, event.SourcePath, event.DestinationPath); err != nil {
			return WorkspacePathMapping{}, err
		}
		mapping.ActiveRelativePath = event.SourcePath.RelativePath
		if operation == WorkspaceOperationRestore {
			mapping.ActiveRelativePath = event.DestinationPath.RelativePath
		}
	} else {
		if err := validateWorkspaceV2Paths(operation, event.Kind, event.Slug, event.SourcePath, event.DestinationPath); err != nil {
			return WorkspacePathMapping{}, err
		}
	}
	mapping.ArchivePayloadPath = event.DestinationPath.RelativePath
	if operation == WorkspaceOperationRestore {
		mapping.ArchivePayloadPath = event.SourcePath.RelativePath
	}
	return mapping, nil
}

func validateWorkspaceEventRoots(event WorkspaceLifecycleEvent, roots TrustedWorkspaceRoots) error {
	if _, err := ResolveWorkspacePaths(roots, event.Kind, event.Slug); err != nil {
		return err
	}
	if err := validateWorkspaceRefForRoots(event.SourcePath, roots); err != nil {
		return err
	}
	return validateWorkspaceRefForRoots(event.DestinationPath, roots)
}
