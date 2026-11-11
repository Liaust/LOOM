package storagearchive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/requestctx"
)

// Uses the existing empty, owned loom_workspace_archive_* database gate. Run
// independently of the V2 migration test, which also requires an empty database.
func TestWorkspaceActiveLocationMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("LOOM_WORKSPACE_PLACEMENT_TEST_DB_URL")
	if dsn == "" {
		t.Skip("LOOM_WORKSPACE_PLACEMENT_TEST_DB_URL is not set; requires an empty owned disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := openWorkspaceArchiveAcceptanceDB(t, ctx, dsn)
	defer db.Close()
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "migrations")
	if err := goose.UpToContext(ctx, db, dir, 112); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_active_location_migration")
	if err != nil {
		t.Fatal(err)
	}
	roots := newWorkspaceRoots(t)
	legacy := workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)
	legacy.ActorID = req.ActorID
	legacy.SchemaVersion, legacy.Placement = WorkspaceArchivePlanSchemaVersion, nil
	legacy.Destination.Path = workspaceV2Ref(roots, WorkspaceRootStorage, "archive/projects/"+legacy.Slug+"/project")
	_ = SealWorkspaceArchivePlan(&legacy)
	if _, err := db.ExecContext(ctx, `INSERT INTO scopes.scopes(scope_id,scope_type,scope_key,slug,display_name,status)
 VALUES('scope_active_location_test','project','active-location-test','active-location-test','Active location test','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects.projects(project_id,project_scope_id,slug,name,owner_actor_id,created_by_actor_id,status)
 VALUES($1,'scope_active_location_test',$2,'Active location test',$3,$3,'active')`, legacy.ObjectID, legacy.Slug, req.ActorID); err != nil {
		t.Fatal(err)
	}
	named := workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)
	named.ActorID, named.OperationID = req.ActorID, newWorkspaceID("workspace_archive_operation")
	named.Placement.Metadata.Path = workspaceV2Ref(roots, WorkspaceRootStorage, ".loom/workspace-archives/"+named.OperationID)
	_ = SealWorkspaceArchivePlan(&named)
	for _, plan := range []WorkspaceArchivePlan{legacy, named} {
		if err := placementInsertOperation(ctx, db, plan); err != nil {
			t.Fatal(err)
		}
		op := workspaceV2Operation(t, plan, PhaseArchiveComplete)
		manifest := manifestForOperation(t, roots, op)
		manifest.SchemaVersion, manifest.ActivePath, manifest.ArchivePayloadPath = WorkspaceArchiveManifestSchemaForPlan(plan), plan.Source.Path, plan.Destination.Path
		manifest.LifecycleEventID = newWorkspaceID("workspace_lifecycle_event")
		if err := placementInsertManifest(ctx, db, manifest); err != nil {
			t.Fatal(err)
		}
		event := eventForOperation(op)
		event.EventID, event.SchemaVersion = manifest.LifecycleEventID, WorkspaceLifecycleEventSchemaForPlan(plan)
		if err := placementInsertEvent(ctx, db, event, plan.OperationKind); err != nil {
			t.Fatal(err)
		}
		if err := placementInsertProjectEvidence(ctx, db, plan, req, false); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() []string {
		t.Helper()
		var result []string
		for _, query := range []string{
			`SELECT jsonb_agg(to_jsonb(t) ORDER BY workspace_archive_operation_id)::text FROM storage.workspace_archive_operations t`,
			`SELECT jsonb_agg(to_jsonb(t) ORDER BY workspace_archive_operation_id)::text FROM storage.workspace_archive_manifests t`,
			`SELECT jsonb_agg(to_jsonb(t) ORDER BY workspace_lifecycle_event_id)::text FROM storage.workspace_lifecycle_events t`,
			`SELECT jsonb_agg(to_jsonb(t) ORDER BY operation_id)::text FROM projects.physical_archive_plan_evidence t`,
		} {
			var value string
			if err := db.QueryRowContext(ctx, query).Scan(&value); err != nil {
				t.Fatal(err)
			}
			result = append(result, value)
		}
		return result
	}
	before := snapshot()
	if err := goose.UpToContext(ctx, db, dir, 113); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("migration changed historical V1/V2 rows, generated tags or signed plan bytes")
	}
	plan := workspaceV3Plan(t, roots, WorkspaceOperationArchive)
	plan.ActorID, plan.OperationID = req.ActorID, newWorkspaceID("workspace_archive_operation")
	plan.Placement.Metadata.Path = workspaceV2Ref(roots, WorkspaceRootStorage, ".loom/workspace-archives/"+plan.OperationID)
	_ = SealWorkspaceArchivePlan(&plan)
	for name, mutate := range map[string]func(*WorkspaceArchivePlan){
		"valid":                     func(*WorkspaceArchivePlan) {},
		"proof_missing":             func(p *WorkspaceArchivePlan) { p.ActiveLocation = nil },
		"revision_zero":             func(p *WorkspaceArchivePlan) { p.ActiveLocation.RegistrationRevision = 0 },
		"foreign_project":           func(p *WorkspaceArchivePlan) { p.ActiveLocation.ProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAW" },
		"outside_projects":          func(p *WorkspaceArchivePlan) { p.ActiveLocation.Path.RelativePath = "Notes/renamed" },
		"incomplete_ancestors":      func(p *WorkspaceArchivePlan) { p.SourceAncestors = nil },
		"metadata_device":           func(p *WorkspaceArchivePlan) { p.Placement.Metadata.ParentIdentity.DeviceID++ },
		"substituted_shared_parent": func(p *WorkspaceArchivePlan) { p.Placement.MetadataAncestors[0].Identity.Inode++ },
	} {
		t.Run(name, func(t *testing.T) {
			copy := cloneProjectArchiveTestValue(t, plan)
			mutate(&copy)
			var valid bool
			if err := db.QueryRowContext(ctx, `SELECT storage.workspace_archive_v3_plan($1::jsonb)`, placementJSON(t, copy)).Scan(&valid); err != nil {
				t.Fatal(err)
			}
			if valid != (name == "valid") {
				t.Fatalf("unexpected SQL validation: %v", valid)
			}
		})
	}
	var valid bool
	if err := db.QueryRowContext(ctx, `SELECT storage.workspace_archive_v3_plan(jsonb_set($1::jsonb,'{active_location,registration_revision}','"7"'::jsonb))`, placementJSON(t, plan)).Scan(&valid); err != nil || valid {
		t.Fatalf("string revision was not rejected safely: %v", err)
	}
	if err := placementInsertOperation(ctx, db, plan); err != nil {
		t.Fatal(err)
	}
	op := workspaceV2Operation(t, plan, PhaseArchiveComplete)
	manifest := workspaceV3Manifest(t, roots, plan, op)
	manifest.LifecycleEventID = newWorkspaceID("workspace_lifecycle_event")
	if err := placementInsertManifest(ctx, db, manifest); err != nil {
		t.Fatal(err)
	}
	var archiveVersion string
	if err := db.QueryRowContext(ctx, `SELECT archive_schema_version FROM storage.workspace_archive_manifests WHERE workspace_archive_operation_id=$1`, plan.OperationID).Scan(&archiveVersion); err != nil || archiveVersion != WorkspaceArchiveOperationSchemaVersionV3 {
		t.Fatalf("incorrect V3 generated archive version: %s %v", archiveVersion, err)
	}
	event := eventForOperation(op)
	event.EventID, event.SchemaVersion = manifest.LifecycleEventID, WorkspaceLifecycleEventSchemaVersionV3
	if err := placementInsertEvent(ctx, db, event, WorkspaceOperationArchive); err != nil {
		t.Fatal(err)
	}
	badManifest := manifest
	badManifest.ActivePath = workspaceV2Ref(roots, WorkspaceRootBox, "Projects/other/renamed")
	assertPlacementSQLState(t, placementInsertManifest(ctx, db, badManifest), "23514")
	restore := workspaceV3Plan(t, roots, WorkspaceOperationRestore)
	restore.ActorID, restore.ArchiveOperationID = req.ActorID, plan.OperationID
	restore.Placement.Metadata.Path = plan.Placement.Metadata.Path
	_ = SealWorkspaceArchivePlan(&restore)
	badRestore := cloneProjectArchiveTestValue(t, restore)
	badRestore.OperationID = newWorkspaceID("workspace_archive_operation")
	badRestore.ActiveLocation.LocationID += "X"
	_ = SealWorkspaceArchivePlan(&badRestore)
	assertPlacementSQLState(t, placementInsertOperation(ctx, db, badRestore), "23514")
	if err := placementInsertOperation(ctx, db, restore); err != nil {
		t.Fatal(err)
	}
	restoredOp := workspaceV2Operation(t, restore, PhaseRestoreComplete)
	manifest.LifecycleState = WorkspaceLifecycleActive
	manifest.RestoreOperationID, manifest.RestorePlanDigest, manifest.RestoredAt = restore.OperationID, restore.PlanDigest, restoredOp.CompletedAt
	manifest.RestoredFromPath = &restore.Source.Path
	if err := placementInsertManifest(ctx, db, manifest); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO notes_workspace.source_scopes(scope_id,kind,source_path) VALUES('retained_test','retained','/storage/Projects/test')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO storage.retained_relocations(relocation_id,custody_id,original_prefix,source_prefix,destination_prefix,plan_digest,plan_json,state)
 VALUES('workspace_path_operation_test','project-location:test','/box/a','/box/a','/box/b',$1,'{}','planned')`, sha256Digest([]byte("test")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO storage.retained_relocation_events(relocation_id,phase,evidence) VALUES('workspace_path_operation_test','location_successor','{"operation_id":"workspace_path_operation_test","prior":{},"next":{}}')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `UPDATE storage.retained_relocation_events SET evidence='{}' WHERE relocation_id='workspace_path_operation_test'`)
	assertPlacementSQLState(t, err, "P0001")
	env, _, location := newProjectV3PlanEnvironment(t)
	physical, err := env.service.PlanProjectPhysicalArchive(ctx, projectArchivePlanRequest(), location.ProjectID, ProjectPhysicalArchivePlanInput{Reason: "SQL contract"})
	if err != nil {
		t.Fatal(err)
	}
	for _, mismatch := range []bool{false, true} {
		copy := cloneProjectArchiveTestValue(t, physical)
		if mismatch {
			copy.ActiveLocation.RepositorySourceRevision++
		}
		payload, _ := json.Marshal(copy)
		if err := db.QueryRowContext(ctx, `SELECT projects.physical_archive_v3_plan($1::jsonb)`, payload).Scan(&valid); err != nil || valid == mismatch {
			t.Fatalf("physical SQL proof binding mismatch=%v valid=%v err=%v", mismatch, valid, err)
		}
	}
}
