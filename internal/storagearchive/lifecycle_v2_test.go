package storagearchive

import (
	"bytes"
	"encoding/json"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceV2PlacementPaths(t *testing.T) {
	roots := newWorkspaceRoots(t)
	for _, tc := range []struct {
		name                          string
		operation                     WorkspaceOperationKind
		legacyPayload, legacyMetadata bool
	}{
		{"archive", WorkspaceOperationArchive, false, false},
		{"named_restore", WorkspaceOperationRestore, false, false},
		{"legacy_restore", WorkspaceOperationRestore, true, true},
		{"relocated_legacy_restore", WorkspaceOperationRestore, false, true},
		{"legacy_payload_operation_metadata", WorkspaceOperationRestore, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := workspaceV2Plan(t, roots, tc.operation, tc.legacyPayload, tc.legacyMetadata)
			if err := ValidateWorkspaceArchivePlan(plan, roots); err != nil {
				t.Fatal(err)
			}
			paths, err := ResolveWorkspacePlanPaths(roots, plan)
			if err != nil {
				t.Fatal(err)
			}
			payload := plan.Destination.Path
			if tc.operation == WorkspaceOperationRestore {
				payload = plan.Source.Path
			}
			if paths.ArchivePayload != payload || paths.ArchiveContainer != plan.Placement.Metadata.Path ||
				paths.ArchiveManifest.RelativePath != plan.Placement.Metadata.Path.RelativePath+"/archive.json" ||
				paths.Mapping.ArchivePayloadPath != payload.RelativePath || paths.Mapping.ArchiveContainerPath != plan.Placement.Metadata.Path.RelativePath {
				t.Fatalf("incorrect resolved placement: %#v", paths)
			}
			phase := PhaseArchiveComplete
			if tc.operation == WorkspaceOperationRestore {
				phase = PhaseRestoreComplete
			}
			operation := workspaceV2Operation(t, plan, phase)
			if err := ValidateWorkspaceArchiveOperationForRoots(operation, roots); err != nil {
				t.Fatal(err)
			}
			event := eventForOperation(operation)
			event.SchemaVersion = WorkspaceLifecycleEventSchemaForPlan(plan)
			if err := ValidateWorkspaceLifecycleEventForRoots(event, roots); err != nil {
				t.Fatal(err)
			}
			if err := ValidateWorkspaceLifecycleEventForOperation(event, operation); err != nil {
				t.Fatal(err)
			}
			event.SchemaVersion = WorkspaceLifecycleEventSchemaVersion
			if err := ValidateWorkspaceLifecycleEventForOperation(event, operation); err == nil {
				t.Fatal("mixed event/operation versions accepted")
			}
		})
	}
	legacy := validWorkspaceArchivePlan(t, roots, WorkspaceOperationArchive)
	want, err := ResolveWorkspacePaths(roots, legacy.Kind, legacy.Slug)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWorkspacePlanPaths(roots, legacy)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy mapping changed: %#v, %v", got, err)
	}
	if WorkspaceArchiveOperationSchemaForPlan(legacy) != WorkspaceArchiveOperationSchemaVersion || WorkspaceLifecycleEventSchemaForPlan(legacy) != WorkspaceLifecycleEventSchemaVersion {
		t.Fatal("legacy schema selection changed")
	}
}

func TestWorkspaceV2PlacementRejectsInvalidBindings(t *testing.T) {
	roots := newWorkspaceRoots(t)
	for name, mutate := range map[string]func(*WorkspaceArchivePlan){
		"missing_placement": func(p *WorkspaceArchivePlan) { p.Placement = nil },
		"other_kind":        func(p *WorkspaceArchivePlan) { p.Kind = WorkspaceKindTopic },
		"legacy_new_archive": func(p *WorkspaceArchivePlan) {
			p.Destination.Path = workspaceV2Ref(roots, WorkspaceRootStorage, "archive/projects/"+p.Slug+"/project")
		},
		"metadata_wrong_operation": func(p *WorkspaceArchivePlan) {
			p.Placement.Metadata.Path = workspaceV2Ref(roots, WorkspaceRootStorage, ".loom/workspace-archives/"+testWorkspaceRestoreID)
		},
		"metadata_wrong_root": func(p *WorkspaceArchivePlan) { p.Placement.Metadata.Path.Root = WorkspaceRootBox },
		"metadata_substituted_base": func(p *WorkspaceArchivePlan) {
			p.Placement.Metadata.Path.AbsolutePath = "/other/" + p.Placement.Metadata.Path.RelativePath
		},
		"source_substituted_base": func(p *WorkspaceArchivePlan) { p.Source.Path.AbsolutePath = "/other/" + p.Source.Path.RelativePath },
		"destination_substituted_base": func(p *WorkspaceArchivePlan) {
			p.Destination.Path.AbsolutePath = "/other/" + p.Destination.Path.RelativePath
		},
		"archive_metadata_present": func(p *WorkspaceArchivePlan) {
			p.Placement.Metadata.Identity = presentIdentity(10, 400, InventoryEntryDirectory)
		},
		"metadata_parent_device":            func(p *WorkspaceArchivePlan) { p.Placement.Metadata.ParentIdentity.DeviceID++ },
		"source_parent_device":              func(p *WorkspaceArchivePlan) { p.Source.ParentIdentity.DeviceID++ },
		"destination_parent_device":         func(p *WorkspaceArchivePlan) { p.Destination.ParentIdentity.DeviceID++ },
		"metadata_ancestor_device":          func(p *WorkspaceArchivePlan) { p.Placement.MetadataAncestors[0].Identity.DeviceID++ },
		"source_ancestor_device":            func(p *WorkspaceArchivePlan) { p.SourceAncestors[0].Identity.DeviceID++ },
		"destination_ancestor_device":       func(p *WorkspaceArchivePlan) { p.DestinationAncestors[0].Identity.DeviceID++ },
		"source_ancestors_missing":          func(p *WorkspaceArchivePlan) { p.SourceAncestors = nil },
		"destination_ancestors_missing":     func(p *WorkspaceArchivePlan) { p.DestinationAncestors = nil },
		"metadata_ancestors_missing":        func(p *WorkspaceArchivePlan) { p.Placement.MetadataAncestors = nil },
		"metadata_ancestor_skipped":         func(p *WorkspaceArchivePlan) { p.Placement.MetadataAncestors = p.Placement.MetadataAncestors[1:] },
		"metadata_ancestor_wrong_name":      func(p *WorkspaceArchivePlan) { p.Placement.MetadataAncestors[1].RelativePath = "other" },
		"metadata_ancestor_parent_mismatch": func(p *WorkspaceArchivePlan) { p.Placement.MetadataAncestors[2].Identity.Inode++ },
		"shared_ancestor_disagrees":         func(p *WorkspaceArchivePlan) { p.Placement.MetadataAncestors[0].Identity.Inode++ },
		"archive_relocation": func(p *WorkspaceArchivePlan) {
			p.Placement.RelocationID = "retained_relocation_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		},
		"bad_actor":               func(p *WorkspaceArchivePlan) { p.ActorID = "../../actor" },
		"empty_reason":            func(p *WorkspaceArchivePlan) { p.Reason = "" },
		"source_absent":           func(p *WorkspaceArchivePlan) { p.Source.Identity = PathIdentity{Presence: PathAbsent} },
		"destination_present":     func(p *WorkspaceArchivePlan) { p.Destination.Identity = p.Source.Identity },
		"inventory_root_mismatch": func(p *WorkspaceArchivePlan) { p.Source.Identity.Inode++ },
		"invalid_inventory":       func(p *WorkspaceArchivePlan) { p.Inventory.Digest = sha256Digest([]byte("other")) },
		"plan_time_missing":       func(p *WorkspaceArchivePlan) { p.PlannedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			p := workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)
			mutate(&p)
			if err := SealWorkspaceArchivePlan(&p); err != nil {
				t.Fatal(err)
			}
			if err := ValidateWorkspaceArchivePlan(p, roots); err == nil {
				t.Fatal("invalid v2 plan accepted")
			}
		})
	}
	p := workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)
	p.Placement.Metadata.ParentIdentity.Inode++
	if err := ValidateWorkspaceArchivePlan(p, roots); err == nil {
		t.Fatal("metadata mutation bypassed digest/identity validation")
	}
	for name, mutate := range map[string]func(*WorkspaceArchivePlan){
		"missing_archive_id":      func(p *WorkspaceArchivePlan) { p.ArchiveOperationID = "" },
		"wrong_archive_id":        func(p *WorkspaceArchivePlan) { p.ArchiveOperationID = newWorkspaceID("workspace_archive_operation") },
		"missing_manifest_digest": func(p *WorkspaceArchivePlan) { p.ArchiveManifestDigest = "" },
		"missing_metadata":        func(p *WorkspaceArchivePlan) { p.Placement.Metadata.Identity = PathIdentity{Presence: PathAbsent} },
		"bad_relocation_id":       func(p *WorkspaceArchivePlan) { p.Placement.RelocationID = "not-a-relocation" },
		"metadata_device":         func(p *WorkspaceArchivePlan) { p.Placement.Metadata.Identity.DeviceID++ },
	} {
		t.Run("restore/"+name, func(t *testing.T) {
			p := workspaceV2Plan(t, roots, WorkspaceOperationRestore, false, false)
			mutate(&p)
			if err := SealWorkspaceArchivePlan(&p); err != nil {
				t.Fatal(err)
			}
			if err := ValidateWorkspaceArchivePlan(p, roots); err == nil {
				t.Fatal("invalid v2 restore accepted")
			}
		})
	}
	p = workspaceV2Plan(t, roots, WorkspaceOperationRestore, false, true)
	p.Placement.RelocationID = ""
	_ = SealWorkspaceArchivePlan(&p)
	if err := ValidateWorkspaceArchivePlan(p, roots); err == nil {
		t.Fatal("relocated legacy restore omitted relocation evidence")
	}
}

func TestWorkspaceV2OperationKeepsCommonInvariants(t *testing.T) {
	roots := newWorkspaceRoots(t)
	plan := workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)
	for name, mutate := range map[string]func(*WorkspaceArchiveOperation){
		"missing_placement": func(o *WorkspaceArchiveOperation) { o.Placement = nil },
		"status_mismatch":   func(o *WorkspaceArchiveOperation) { o.Status = OperationStatusPending },
		"missing_timestamp": func(o *WorkspaceArchiveOperation) { o.PayloadMovedAt = nil },
		"invalid_last_safe": func(o *WorkspaceArchiveOperation) { o.LastSafePhase = PhaseArchivePlanned },
		"source_identity":   func(o *WorkspaceArchiveOperation) { o.Source.Identity.Inode = 0 },
		"bad_digest":        func(o *WorkspaceArchiveOperation) { o.PlanDigest = "invalid" },
		"invalid_finding":   func(o *WorkspaceArchiveOperation) { o.Findings = []WorkspaceArchiveFinding{{}} },
	} {
		t.Run(name, func(t *testing.T) {
			o := workspaceV2Operation(t, plan, PhaseArchiveComplete)
			mutate(&o)
			if err := ValidateWorkspaceArchiveOperation(o); err == nil {
				t.Fatal("invalid v2 operation accepted")
			}
		})
	}
	operation := workspaceV2Operation(t, plan, PhaseArchiveComplete)
	operation.Source.Path.AbsolutePath = "/other/" + operation.Source.Path.RelativePath
	if err := ValidateWorkspaceArchiveOperationForRoots(operation, roots); err == nil {
		t.Fatal("operation accepted a substituted trusted root")
	}
}

func TestWorkspaceV2ManifestPreservesHistoricalArchivePath(t *testing.T) {
	roots := newWorkspaceRoots(t)
	for _, legacyArchive := range []bool{false, true} {
		archivePlan := workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)
		if legacyArchive {
			archivePlan.SchemaVersion = WorkspaceArchivePlanSchemaVersion
			archivePlan.Placement = nil
			archivePlan.Destination.Path = workspaceV2Ref(roots, WorkspaceRootStorage, "archive/projects/"+archivePlan.Slug+"/project")
			_ = SealWorkspaceArchivePlan(&archivePlan)
		}
		archive := workspaceV2Operation(t, archivePlan, PhaseArchiveComplete)
		manifest := manifestForOperation(t, roots, archive)
		manifest.ArchivePayloadPath = archive.Destination.Path
		if !legacyArchive {
			manifest.SchemaVersion = WorkspaceArchiveManifestSchemaVersionV2
		}
		if err := ValidateWorkspaceArchiveManifestForOperations(manifest, archive, nil); err != nil {
			t.Fatal(err)
		}
		historical := manifest.ArchivePayloadPath
		restorePlan := workspaceV2Plan(t, roots, WorkspaceOperationRestore, false, legacyArchive)
		restore := workspaceV2Operation(t, restorePlan, PhaseRestoreComplete)
		manifest.SchemaVersion = WorkspaceArchiveManifestSchemaVersionV2
		manifest.LifecycleState = WorkspaceLifecycleActive
		manifest.RestoreOperationID = restore.OperationID
		manifest.RestorePlanDigest = restore.PlanDigest
		manifest.RestoredAt = restore.CompletedAt
		manifest.RestoredFromPath = &restore.Source.Path
		if err := ValidateWorkspaceArchiveManifestForRoots(manifest, roots); err != nil {
			t.Fatal(err)
		}
		if err := ValidateWorkspaceArchiveManifestForOperations(manifest, archive, &restore); err != nil {
			t.Fatal(err)
		}
		if manifest.ArchivePayloadPath != historical {
			t.Fatal("restore rewrote historical archive path")
		}
		withoutSource := manifest
		withoutSource.RestoredFromPath = nil
		err := ValidateWorkspaceArchiveManifestForOperations(withoutSource, archive, &restore)
		if legacyArchive && err == nil {
			t.Fatal("relocated restore claimed the historical source")
		}
		if !legacyArchive && err != nil {
			t.Fatalf("unrelocated v2 restore requires an unnecessary source override: %v", err)
		}
		bad := manifest
		bad.ArchivePayloadPath = *manifest.RestoredFromPath
		if legacyArchive && ValidateWorkspaceArchiveManifestForOperations(bad, archive, &restore) == nil {
			t.Fatal("manifest accepted rewritten historical path")
		}
		bad = manifest
		bad.RestoredFromPath = &WorkspacePathRef{Root: WorkspaceRootStorage, RelativePath: "Projects/other", AbsolutePath: filepath.Join(roots.StorageRoot, "Projects/other")}
		if ValidateWorkspaceArchiveManifest(bad) == nil {
			t.Fatal("manifest accepted another project's source")
		}
		bad = manifest
		bad.SchemaVersion = WorkspaceArchiveManifestSchemaVersion
		if ValidateWorkspaceArchiveManifest(bad) == nil {
			t.Fatal("v1 manifest accepted v2 restore evidence")
		}
		bad = manifest
		bad.Authentication.Tag = "invalid"
		if ValidateWorkspaceArchiveManifest(bad) == nil {
			t.Fatal("v2 manifest bypassed authentication validation")
		}
	}
}

func TestWorkspaceV1OptionalFieldsPreserveBytes(t *testing.T) {
	roots := newWorkspaceRoots(t)
	plan := validWorkspaceArchivePlan(t, roots, WorkspaceOperationArchive)
	operation := operationAtPhase(t, plan, PhaseArchiveComplete, "")
	manifest := manifestForOperation(t, roots, operation)
	// Build the original struct layouts, including field order and tags, with
	// the newly introduced optional field absent from the Go type itself.
	for _, item := range []struct {
		value    any
		newField string
	}{
		{plan, "Placement"}, {operation, "Placement"}, {manifest, "RestoredFromPath"},
	} {
		v := reflect.ValueOf(item.value)
		var fields []reflect.StructField
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.Name != item.newField {
				fields = append(fields, f)
			}
		}
		old := reflect.New(reflect.StructOf(fields)).Elem()
		for i := 0; i < old.NumField(); i++ {
			old.Field(i).Set(v.FieldByName(old.Type().Field(i).Name))
		}
		before, err := json.Marshal(old.Interface())
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(item.value)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("v1 %T bytes changed", item.value)
		}
		if sha256Digest(before) != sha256Digest(after) {
			t.Fatal("v1 digest changed")
		}
	}
	plan.Placement = &WorkspaceArchivePlacement{}
	_ = SealWorkspaceArchivePlan(&plan)
	if ValidateWorkspaceArchivePlan(plan, roots) == nil {
		t.Fatal("v1 plan accepted placement")
	}
	operation.Placement = plan.Placement
	if ValidateWorkspaceArchiveOperation(operation) == nil {
		t.Fatal("v1 operation accepted placement")
	}
}

func workspaceV2Plan(t *testing.T, roots TrustedWorkspaceRoots, operation WorkspaceOperationKind, legacyPayload, legacyMetadata bool) WorkspaceArchivePlan {
	t.Helper()
	p := validWorkspaceArchivePlan(t, roots, operation)
	p.SchemaVersion, p.Kind, p.ObjectID = WorkspaceArchivePlanSchemaVersionV2, WorkspaceKindProject, "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	active := workspaceV2Ref(roots, WorkspaceRootBox, "Projects/"+p.Slug)
	payloadName := "Projects/" + p.Slug
	if legacyPayload {
		payloadName = "archive/projects/" + p.Slug + "/project"
	}
	payload := workspaceV2Ref(roots, WorkspaceRootStorage, payloadName)
	metadataName := ".loom/workspace-archives/" + testWorkspaceOperationID
	if legacyMetadata {
		metadataName = "archive/projects/" + p.Slug
	}
	p.Source.Path, p.Destination.Path = active, payload
	metadataPresence := PathAbsent
	if operation == WorkspaceOperationRestore {
		p.Source.Path, p.Destination.Path = payload, active
		p.ArchiveOperationID, p.ArchiveManifestDigest = testWorkspaceOperationID, sha256Digest([]byte("original manifest"))
		metadataPresence = PathPresent
	}
	p.SourceAncestors = workspaceV2Ancestors(p.Source.Path)
	p.DestinationAncestors = workspaceV2Ancestors(p.Destination.Path)
	p.Source.ParentIdentity = p.SourceAncestors[len(p.SourceAncestors)-1].Identity
	p.Destination.ParentIdentity = p.DestinationAncestors[len(p.DestinationAncestors)-1].Identity
	metadataRef := workspaceV2Ref(roots, WorkspaceRootStorage, metadataName)
	metadataAncestors := workspaceV2Ancestors(metadataRef)
	metadataIdentity := PathIdentity{Presence: metadataPresence}
	if metadataPresence == PathPresent {
		metadataIdentity = workspaceV2DirectoryIdentity(WorkspaceRootStorage, metadataName)
	}
	p.Placement = &WorkspaceArchivePlacement{
		Metadata:          WorkspacePathBinding{Path: metadataRef, Identity: metadataIdentity, ParentIdentity: metadataAncestors[len(metadataAncestors)-1].Identity},
		MetadataAncestors: metadataAncestors,
	}
	if operation == WorkspaceOperationRestore && legacyMetadata && !legacyPayload {
		p.Placement.RelocationID = "retained_relocation_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	}
	if err := SealWorkspaceArchivePlan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func workspaceV2Ref(roots TrustedWorkspaceRoots, root WorkspaceRoot, relative string) WorkspacePathRef {
	base := roots.BoxRoot
	if root == WorkspaceRootStorage {
		base = roots.StorageRoot
	}
	return WorkspacePathRef{Root: root, RelativePath: relative, AbsolutePath: filepath.Join(base, filepath.FromSlash(relative))}
}

func workspaceV2DirectoryIdentity(root WorkspaceRoot, relative string) PathIdentity {
	// Deterministic identities let shared named ancestors agree across fixtures.
	var inode uint64 = 500
	for _, c := range string(root) + ":" + relative {
		inode = inode*31 + uint64(c)
	}
	return presentIdentity(10, inode, InventoryEntryDirectory)
}

func workspaceV2Ancestors(ref WorkspacePathRef) []WorkspaceAncestorBinding {
	result := []WorkspaceAncestorBinding{{RelativePath: ".", Identity: workspaceV2DirectoryIdentity(ref.Root, ".")}}
	relative := "."
	for _, component := range strings.Split(path.Dir(ref.RelativePath), "/") {
		relative = path.Join(relative, component)
		result = append(result, WorkspaceAncestorBinding{RelativePath: relative, Identity: workspaceV2DirectoryIdentity(ref.Root, relative)})
	}
	return result
}

func workspaceV2Operation(t *testing.T, plan WorkspaceArchivePlan, phase WorkspaceArchivePhase) WorkspaceArchiveOperation {
	o := operationAtPhase(t, plan, phase, "")
	o.SchemaVersion, o.Placement = WorkspaceArchiveOperationSchemaForPlan(plan), plan.Placement
	return o
}
