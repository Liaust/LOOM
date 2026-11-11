package storagearchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func workspaceV3Plan(t *testing.T, roots TrustedWorkspaceRoots, operation WorkspaceOperationKind) WorkspaceArchivePlan {
	t.Helper()
	plan := workspaceV2Plan(t, roots, operation, false, false)
	location := &WorkspaceActiveLocation{ProjectID: plan.ObjectID,
		Path:       workspaceV2Ref(roots, WorkspaceRootBox, "Projects/research/renamed-folder"),
		LocationID: "project_location_01ARZ3NDEKTSV4RRFFQ69G5FAV", RegistrationRevision: 7, RepositorySourceRevision: 4}
	plan.SchemaVersion, plan.ActiveLocation = WorkspaceArchivePlanSchemaVersionV3, location
	if operation == WorkspaceOperationArchive {
		plan.Source.Path = location.Path
		plan.SourceAncestors = workspaceV2Ancestors(location.Path)
		plan.Source.ParentIdentity = plan.SourceAncestors[len(plan.SourceAncestors)-1].Identity
	} else {
		plan.Destination.Path = location.Path
		plan.DestinationAncestors = workspaceV2Ancestors(location.Path)
		plan.Destination.ParentIdentity = plan.DestinationAncestors[len(plan.DestinationAncestors)-1].Identity
	}
	if err := SealWorkspaceArchivePlan(&plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func workspaceV3Manifest(t *testing.T, roots TrustedWorkspaceRoots, plan WorkspaceArchivePlan, operation WorkspaceArchiveOperation) WorkspaceArchiveManifest {
	t.Helper()
	manifest := manifestForOperation(t, roots, operation)
	manifest.SchemaVersion = WorkspaceArchiveManifestSchemaForPlan(plan)
	manifest.ActivePath, manifest.ArchivePayloadPath = plan.Source.Path, plan.Destination.Path
	manifest.InventoryDigest = plan.Inventory.Digest
	return manifest
}

func TestWorkspaceV3ContractsRoundTripAndRejectV2Downgrade(t *testing.T) {
	roots := newWorkspaceRoots(t)
	archivePlan := workspaceV3Plan(t, roots, WorkspaceOperationArchive)
	restorePlan := workspaceV3Plan(t, roots, WorkspaceOperationRestore)
	archive := workspaceV2Operation(t, archivePlan, PhaseArchiveComplete)
	restore := workspaceV2Operation(t, restorePlan, PhaseRestoreComplete)
	manifest := workspaceV3Manifest(t, roots, archivePlan, archive)
	for _, plan := range []WorkspaceArchivePlan{archivePlan, restorePlan} {
		copy := cloneProjectArchiveTestValue(t, plan)
		if err := ValidateWorkspaceArchivePlan(copy, roots); err != nil {
			t.Fatal(err)
		}
		paths, err := ResolveWorkspacePlanPaths(roots, copy)
		if err != nil || paths.Active != plan.ActiveLocation.Path || paths.Mapping.ActiveRelativePath != plan.ActiveLocation.Path.RelativePath {
			t.Fatalf("active location was guessed from slug: %#v %v", paths, err)
		}
		copy.SchemaVersion, copy.ActiveLocation = WorkspaceArchivePlanSchemaVersionV2, nil
		_ = SealWorkspaceArchivePlan(&copy)
		if ValidateWorkspaceArchivePlan(copy, roots) == nil {
			t.Fatal("v2 accepted a noncanonical active path")
		}
	}
	for _, operation := range []WorkspaceArchiveOperation{archive, restore} {
		operation = cloneProjectArchiveTestValue(t, operation)
		if err := ValidateWorkspaceArchiveOperationForRoots(operation, roots); err != nil {
			t.Fatal(err)
		}
		event := eventForOperation(operation)
		event.SchemaVersion = workspaceEventSchema(operation)
		event = cloneProjectArchiveTestValue(t, event)
		if err := ValidateWorkspaceLifecycleEventForRoots(event, roots); err != nil {
			t.Fatal(err)
		}
		if err := ValidateWorkspaceLifecycleEventForOperation(event, operation); err != nil {
			t.Fatal(err)
		}
		event.SchemaVersion = WorkspaceLifecycleEventSchemaVersionV2
		if ValidateWorkspaceLifecycleEvent(event) == nil {
			t.Fatal("v2 event accepted a noncanonical active path")
		}
		operation.SchemaVersion = WorkspaceArchiveOperationSchemaVersionV2
		if ValidateWorkspaceArchiveOperation(operation) == nil {
			t.Fatal("v2 operation accepted a noncanonical active path")
		}
	}
	if err := ValidateWorkspaceArchiveManifestForOperations(manifest, archive, nil); err != nil {
		t.Fatal(err)
	}
	for _, active := range []bool{false, true} {
		copy := manifest
		if active {
			copy.LifecycleState = WorkspaceLifecycleActive
			copy.RestoreOperationID, copy.RestorePlanDigest, copy.RestoredAt = restore.OperationID, restore.PlanDigest, restore.CompletedAt
			copy.RestoredFromPath = &restore.Source.Path
			if err := ValidateWorkspaceArchiveManifestForOperations(copy, archive, &restore); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := json.Marshal(copy)
		decoded, err := DecodePhysicalWorkspaceArchiveManifest(raw)
		if err != nil || !reflect.DeepEqual(copy, decoded) {
			t.Fatalf("v3 manifest roundtrip failed: %v", err)
		}
		if err := ValidateWorkspaceArchiveManifestForRoots(decoded, roots); err != nil {
			t.Fatal(err)
		}
		decoded.SchemaVersion = WorkspaceArchiveManifestSchemaVersionV2
		if ValidateWorkspaceArchiveManifest(decoded) == nil {
			t.Fatal("v2 manifest accepted a noncanonical active path")
		}
	}
}

func TestWorkspaceV3RejectsLocationSubstitution(t *testing.T) {
	roots := newWorkspaceRoots(t)
	for name, mutate := range map[string]func(*WorkspaceArchivePlan){
		"missing_proof":         func(p *WorkspaceArchivePlan) { p.ActiveLocation = nil },
		"foreign_project":       func(p *WorkspaceArchivePlan) { p.ActiveLocation.ProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAW" },
		"missing_location_id":   func(p *WorkspaceArchivePlan) { p.ActiveLocation.LocationID = "" },
		"invalid_location_id":   func(p *WorkspaceArchivePlan) { p.ActiveLocation.LocationID = "../location" },
		"registration_revision": func(p *WorkspaceArchivePlan) { p.ActiveLocation.RegistrationRevision = 0 },
		"source_revision":       func(p *WorkspaceArchivePlan) { p.ActiveLocation.RepositorySourceRevision = -1 },
		"different_path": func(p *WorkspaceArchivePlan) {
			p.ActiveLocation.Path = workspaceV2Ref(roots, WorkspaceRootBox, "Projects/other")
		},
		"outside_projects": func(p *WorkspaceArchivePlan) {
			p.ActiveLocation.Path = workspaceV2Ref(roots, WorkspaceRootBox, "Notes/renamed")
		},
		"projects_root": func(p *WorkspaceArchivePlan) {
			p.ActiveLocation.Path = workspaceV2Ref(roots, WorkspaceRootBox, "Projects")
		},
		"traversal":     func(p *WorkspaceArchivePlan) { p.ActiveLocation.Path.RelativePath = "Projects/../renamed" },
		"dot_component": func(p *WorkspaceArchivePlan) { p.ActiveLocation.Path.RelativePath = "Projects/./renamed" },
		"substituted_box": func(p *WorkspaceArchivePlan) {
			p.ActiveLocation.Path.AbsolutePath = "/other/" + p.ActiveLocation.Path.RelativePath
		},
		"storage_active":    func(p *WorkspaceArchivePlan) { p.ActiveLocation.Path.Root = WorkspaceRootStorage },
		"missing_placement": func(p *WorkspaceArchivePlan) { p.Placement = nil },
		"legacy_metadata": func(p *WorkspaceArchivePlan) {
			p.Placement.Metadata.Path = workspaceV2Ref(roots, WorkspaceRootStorage, "archive/projects/"+p.Slug)
		},
		"legacy_payload": func(p *WorkspaceArchivePlan) {
			p.Destination.Path = workspaceV2Ref(roots, WorkspaceRootStorage, "archive/projects/"+p.Slug+"/project")
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan := workspaceV3Plan(t, roots, WorkspaceOperationArchive)
			mutate(&plan)
			_ = SealWorkspaceArchivePlan(&plan)
			if ValidateWorkspaceArchivePlan(plan, roots) == nil {
				t.Fatal("invalid accepted active location was admitted")
			}
		})
	}
	plan := workspaceV3Plan(t, roots, WorkspaceOperationArchive)
	plan.ActiveLocation.RegistrationRevision++
	if ValidateWorkspaceArchivePlan(plan, roots) == nil {
		t.Fatal("location revision mutation did not invalidate plan digest")
	}
	for _, version := range []string{WorkspaceArchivePlanSchemaVersion, WorkspaceArchivePlanSchemaVersionV2} {
		plan := workspaceV3Plan(t, roots, WorkspaceOperationArchive)
		plan.SchemaVersion = version
		_ = SealWorkspaceArchivePlan(&plan)
		if ValidateWorkspaceArchivePlan(plan, roots) == nil {
			t.Fatal("historical plan accepted new location evidence")
		}
	}
}

func TestWorkspaceV3NilLocationPreservesHistoricalJSON(t *testing.T) {
	roots := newWorkspaceRoots(t)
	for _, plan := range []WorkspaceArchivePlan{validWorkspaceArchivePlan(t, roots, WorkspaceOperationArchive), workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)} {
		assertLocationFieldOmitted(t, plan)
		assertLocationFieldOmitted(t, ProjectPhysicalArchivePlan{Workspace: plan})
		assertLocationFieldOmitted(t, ProjectPhysicalRestorePlan{Workspace: plan})
	}
}

func assertLocationFieldOmitted(t *testing.T, item any) {
	t.Helper()
	v := reflect.ValueOf(item)
	var fields []reflect.StructField
	for i := 0; i < v.NumField(); i++ {
		if f := v.Type().Field(i); f.Name != "ActiveLocation" {
			fields = append(fields, f)
		}
	}
	old := reflect.New(reflect.StructOf(fields)).Elem()
	for i := 0; i < old.NumField(); i++ {
		old.Field(i).Set(v.FieldByName(old.Type().Field(i).Name))
	}
	before, _ := json.Marshal(old.Interface())
	after, _ := json.Marshal(item)
	if !bytes.Equal(before, after) {
		t.Fatalf("nil active location changed historical %T JSON", item)
	}
}

func TestWorkspaceV3ArchivePlannerUsesOnlyAcceptedLocation(t *testing.T) {
	for _, relative := range []string{"Projects/research/renamed-folder", "Projects/renamed-folder"} {
		t.Run(relative, func(t *testing.T) {
			f, in, location := workspaceV3PlannerFixture(t, relative)
			calls := 0
			f.service.ActiveProjectLocation = func(_ context.Context, id string) (*WorkspaceActiveLocation, error) {
				calls++
				if id != location.ProjectID {
					t.Fatal("callback project identity changed")
				}
				return location, nil
			}
			plan, err := f.service.PlanArchive(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if plan.SchemaVersion != WorkspaceArchivePlanSchemaVersionV3 || plan.Source.Path != location.Path || !sameWorkspaceActiveLocation(plan.ActiveLocation, location) || calls != 2 {
				t.Fatal("planner did not bind and recheck the accepted location")
			}
			location.RegistrationRevision++
			if plan.ActiveLocation.RegistrationRevision == location.RegistrationRevision {
				t.Fatal("plan retained mutable callback storage")
			}
			if _, err := f.service.comparePlacementArchive(context.Background(), plan); err == nil {
				t.Fatal("changed owner revision was not refused before archive")
			}
		})
	}
	f, in := placementFixture(t)
	f.service.NamedPlacement = true
	f.service.ActiveProjectLocation = func(context.Context, string) (*WorkspaceActiveLocation, error) { return nil, nil }
	plan, err := f.service.PlanArchive(context.Background(), in)
	if err != nil || plan.SchemaVersion != WorkspaceArchivePlanSchemaVersionV2 || plan.ActiveLocation != nil {
		t.Fatalf("unmoved project did not preserve v2: %v", err)
	}
	f.service.ActiveProjectLocation = func(context.Context, string) (*WorkspaceActiveLocation, error) {
		return nil, errors.New("owner unavailable")
	}
	if _, err := f.service.PlanArchive(context.Background(), in); err == nil {
		t.Fatal("unavailable owner silently fell back to slug custody")
	}
}

func workspaceV3PlannerFixture(t *testing.T, relative string) (*workspaceMoveFixture, WorkspaceArchivePlanInput, *WorkspaceActiveLocation) {
	t.Helper()
	f, in := placementFixture(t)
	f.service.NamedPlacement = true
	in.ObjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	location := &WorkspaceActiveLocation{ProjectID: in.ObjectID, Path: workspaceV2Ref(f.roots, WorkspaceRootBox, relative),
		LocationID: "project_location_01ARZ3NDEKTSV4RRFFQ69G5FAV", RegistrationRevision: 7, RepositorySourceRevision: 4}
	if err := os.MkdirAll(filepath.Dir(location.Path.AbsolutePath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.paths.Active.AbsolutePath, location.Path.AbsolutePath); err != nil {
		t.Fatal(err)
	}
	f.service.ActiveProjectLocation = func(context.Context, string) (*WorkspaceActiveLocation, error) { return location, nil }
	return f, in, location
}

func TestWorkspaceV3PlacementMoveUsesReviewedLeaf(t *testing.T) {
	f, in, _ := workspaceV3PlannerFixture(t, "Projects/research/renamed-folder")
	ctx := context.Background()
	plan, err := f.service.PlanArchive(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.moveNamedPayload(ctx, plan); err != nil {
		t.Fatal(err)
	}
	custody, _, _, err := f.service.inspectPlacementArchive(ctx, plan)
	if err != nil || custody != CustodyArchived {
		t.Fatalf("renamed source leaf did not reach named archive custody: %s %v", custody, err)
	}
	if _, err := os.Lstat(plan.Source.Path.AbsolutePath); !os.IsNotExist(err) {
		t.Fatal("reviewed renamed source remains after move")
	}
}

func TestWorkspaceV3BuildArchiveManifestUsesVersionedOwner(t *testing.T) {
	f, in, _ := workspaceV3PlannerFixture(t, "Projects/research/renamed-folder")
	plan, err := f.service.PlanArchive(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	operation := workspaceV3CompleteOperation(t, plan, PhaseArchiveComplete)
	manifest, err := f.service.buildArchiveManifest(context.Background(), operation, plan)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != WorkspaceArchiveManifestSchemaVersionV3 || manifest.ActivePath != plan.Source.Path {
		t.Fatal("archive manifest dropped accepted location version")
	}
}

func TestWorkspaceV3PlacementRestoreFreezesOriginalActiveLocation(t *testing.T) {
	ctx := context.Background()
	f, in, location := workspaceV3PlannerFixture(t, "Projects/research/renamed-folder")
	archive, err := f.service.PlanArchive(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	operation := workspaceV3CompleteOperation(t, archive, PhaseArchiveComplete)
	manifest := workspaceV3Manifest(t, f.roots, archive, operation)
	manifest.Authentication.KeyID = f.service.ManifestKeyID
	if err := f.service.signManifest(ctx, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archive.Placement.Metadata.Path.AbsolutePath, 0o750); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(archive.Placement.Metadata.Path.AbsolutePath, "archive.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(archive.Source.Path.AbsolutePath, archive.Destination.Path.AbsolutePath); err != nil {
		t.Fatal(err)
	}
	f.service.ActiveProjectLocation = func(context.Context, string) (*WorkspaceActiveLocation, error) {
		return nil, errors.New("restore must not query current location")
	}
	restore, err := f.service.planPlacementRestore(ctx, WorkspaceRestorePlanInput{OperationID: restoreTestOperationID, ActorID: in.ActorID, Reason: "restore inactive"}, archive, operation, manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	if restore.SchemaVersion != WorkspaceArchivePlanSchemaVersionV3 || restore.Destination.Path != location.Path || !sameWorkspaceActiveLocation(archive.ActiveLocation, restore.ActiveLocation) {
		t.Fatal("restore guessed its destination or lost archived owner proof")
	}
	restoredOperation := workspaceV3CompleteOperation(t, restore, PhaseRestoreComplete)
	restored, err := f.service.buildRestoredManifest(ctx, manifest, operation, restoredOperation, restore)
	if err != nil || restored.SchemaVersion != WorkspaceArchiveManifestSchemaVersionV3 || restored.ActivePath != location.Path {
		t.Fatalf("restored manifest lost frozen v3 location: %v", err)
	}
	if _, err := os.Lstat(location.Path.AbsolutePath); !os.IsNotExist(err) {
		t.Fatal("restore planning changed active custody")
	}
	if restore.ActiveLocation == archive.ActiveLocation {
		t.Fatal("restore retained mutable archive proof storage")
	}
	if path.Base(restore.Destination.Path.RelativePath) == restore.Slug {
		t.Fatal("fixture did not exercise renamed active leaf")
	}
}

func workspaceV3CompleteOperation(t *testing.T, plan WorkspaceArchivePlan, phase WorkspaceArchivePhase) WorkspaceArchiveOperation {
	t.Helper()
	operation := workspaceV2Operation(t, plan, phase)
	at := plan.PlannedAt.Add(time.Second)
	operation.IntentCommittedAt, operation.PayloadMovedAt, operation.ProjectionsCommittedAt, operation.CompletedAt, operation.UpdatedAt = &at, &at, &at, &at, at
	return operation
}
