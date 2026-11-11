package storagearchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/requestctx"
)

func TestWorkspacePlacementMigrationSelectorContract(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00110_workspace_archive_placement.sql"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, required := range []string{
		`ORDER BY a.attname::text COLLATE "C"`,
		`ORDER BY col COLLATE "C"`,
		`c.contype = 'c' AND c.convalidated`,
		`cardinality(names) IS DISTINCT FROM 1`,
		`'storage.workspace_archive_plan.v1', 'storage.workspace_archive_plan.v2'`,
		`'storage.workspace_archive_manifest.v1', 'storage.workspace_archive_manifest.v2'`,
		`'_plan.v1', '_plan.v2'`,
		`workspace_lifecycle_events_operation_version_fkey`,
		`workspace_archive_manifests_archive_version_fkey`,
		`workspace_archive_manifests_restore_version_fkey`,
		`guard_workspace_archive_v2_manifest_paths`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("missing migration contract: %s", required)
		}
	}
}

// Run only against a new, empty loom_workspace_archive_* disposable database.
// This exercises the real migrations, not a reconstructed table definition.
func TestWorkspacePlacementMigrationPostgres(t *testing.T) {
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
	if err := goose.UpToContext(ctx, db, dir, 109); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_placement_migration")
	if err != nil {
		t.Fatal(err)
	}
	roots := newWorkspaceRoots(t)
	legacy := workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)
	legacy.SchemaVersion, legacy.Placement, legacy.ActorID = WorkspaceArchivePlanSchemaVersion, nil, req.ActorID
	legacy.Destination.Path = workspaceV2Ref(roots, WorkspaceRootStorage, "archive/projects/"+legacy.Slug+"/project")
	if err := SealWorkspaceArchivePlan(&legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO scopes.scopes(scope_id,scope_type,scope_key,slug,display_name,status)
 VALUES('scope_placement_test','project','placement-test','placement-test','Placement test','active');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects.projects(project_id,project_scope_id,slug,name,owner_actor_id,created_by_actor_id,status)
 VALUES($1,'scope_placement_test',$2,'Placement test',$3,$3,'active')`, legacy.ObjectID, legacy.Slug, req.ActorID); err != nil {
		t.Fatal(err)
	}
	if err := placementInsertOperation(ctx, db, legacy); err != nil {
		t.Fatal(err)
	}
	archive := workspaceV2Operation(t, legacy, PhaseArchiveComplete)
	manifest := manifestForOperation(t, roots, archive)
	if err := placementInsertManifest(ctx, db, manifest); err != nil {
		t.Fatal(err)
	}
	if err := placementInsertEvent(ctx, db, eventForOperation(archive), legacy.OperationKind); err != nil {
		t.Fatal(err)
	}
	if err := placementInsertProjectEvidence(ctx, db, legacy, req, false); err != nil {
		t.Fatal(err)
	}
	snapshot := func() []string {
		t.Helper()
		var result []string
		for _, query := range []string{
			`SELECT plan_json::text FROM storage.workspace_archive_operations ORDER BY workspace_archive_operation_id`,
			`SELECT manifest_json::text FROM storage.workspace_archive_manifests ORDER BY workspace_archive_operation_id`,
			`SELECT details::text FROM storage.workspace_lifecycle_events ORDER BY workspace_lifecycle_event_id`,
			`SELECT encode(payload,'hex') FROM projects.physical_archive_plan_evidence ORDER BY operation_id`,
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
	if err := goose.UpToContext(ctx, db, dir, 110); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("migration rewrote legacy evidence")
	}

	for name, mutate := range map[string]func(*WorkspaceArchivePlan){
		"metadata_missing":               func(p *WorkspaceArchivePlan) { p.Placement = nil },
		"metadata_present_on_archive":    func(p *WorkspaceArchivePlan) { p.Placement.Metadata.Identity = p.Source.Identity },
		"metadata_cross_device":          func(p *WorkspaceArchivePlan) { p.Placement.Metadata.ParentIdentity.DeviceID++ },
		"metadata_ancestor_cross_device": func(p *WorkspaceArchivePlan) { p.Placement.MetadataAncestors[0].Identity.DeviceID++ },
		"metadata_ancestor_incomplete":   func(p *WorkspaceArchivePlan) { p.Placement.MetadataAncestors = nil },
		"metadata_wrong_operation": func(p *WorkspaceArchivePlan) {
			p.Placement.Metadata.Path = workspaceV2Ref(roots, WorkspaceRootStorage, ".loom/workspace-archives/"+testWorkspaceRestoreID)
		},
		"payload_legacy_on_archive": func(p *WorkspaceArchivePlan) { p.Destination.Path = legacy.Destination.Path },
		"partial_workspace_schema":  func(p *WorkspaceArchivePlan) { p.SchemaVersion = WorkspaceArchivePlanSchemaVersion },
		"wrong_kind":                func(p *WorkspaceArchivePlan) { p.Kind = WorkspaceKindTopic },
	} {
		t.Run(name, func(t *testing.T) {
			p := workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)
			mutate(&p)
			var valid bool
			if err := db.QueryRowContext(ctx, `SELECT storage.workspace_archive_v2_plan($1::jsonb)`, placementJSON(t, p)).Scan(&valid); err != nil {
				t.Fatal(err)
			}
			if valid {
				t.Fatal("invalid v2 placement accepted by SQL")
			}
		})
	}

	named := workspaceV2Plan(t, roots, WorkspaceOperationArchive, false, false)
	named.ActorID = req.ActorID
	named.OperationID = newWorkspaceID("workspace_archive_operation")
	named.Placement.Metadata.Path = workspaceV2Ref(roots, WorkspaceRootStorage, ".loom/workspace-archives/"+named.OperationID)
	_ = SealWorkspaceArchivePlan(&named)
	if err := placementInsertOperation(ctx, db, named); err != nil {
		t.Fatal(err)
	}
	namedOperation := workspaceV2Operation(t, named, PhaseArchiveComplete)
	namedManifest := manifestForOperation(t, roots, namedOperation)
	namedManifest.SchemaVersion, namedManifest.ArchivePayloadPath = WorkspaceArchiveManifestSchemaVersionV2, named.Destination.Path
	if err := placementInsertManifest(ctx, db, namedManifest); err != nil {
		t.Fatal(err)
	}
	namedEvent := eventForOperation(namedOperation)
	namedEvent.EventID, namedEvent.SchemaVersion = newWorkspaceID("workspace_lifecycle_event"), WorkspaceLifecycleEventSchemaVersionV2
	if err := placementInsertEvent(ctx, db, namedEvent, named.OperationKind); err != nil {
		t.Fatal(err)
	}
	assertPlacementSQLState(t, placementInsertProjectEvidence(ctx, db, named, req, true), "23514")
	if err := placementInsertProjectEvidence(ctx, db, named, req, false); err != nil {
		t.Fatal(err)
	}

	// V1 retains its fixed path checks; a V1 row cannot borrow V2 payload paths.
	_, err = db.ExecContext(ctx, `UPDATE storage.workspace_archive_operations SET destination_relative_path=$2,
 plan_json=jsonb_set(plan_json,'{destination,path}', $3::jsonb) WHERE workspace_archive_operation_id=$1`, legacy.OperationID, named.Destination.Path.RelativePath, placementJSON(t, named.Destination.Path))
	assertPlacementSQLState(t, err, "23514")
	_, err = db.ExecContext(ctx, `UPDATE storage.workspace_archive_operations SET schema_version='storage.workspace_archive_operation.v1'
 WHERE workspace_archive_operation_id=$1`, named.OperationID)
	assertPlacementSQLState(t, err, "23514")
	_, err = db.ExecContext(ctx, `UPDATE storage.workspace_lifecycle_events SET schema_version='storage.workspace_lifecycle_event.v1'
 WHERE workspace_archive_operation_id=$1`, named.OperationID)
	if err == nil {
		t.Fatal("event admitted a partial version downgrade")
	}

	// A V2 restore of a relocated V1 archive records the actual source separately.
	restore := workspaceV2Plan(t, roots, WorkspaceOperationRestore, false, true)
	restore.ActorID = req.ActorID
	_ = SealWorkspaceArchivePlan(&restore)
	if err := placementInsertOperation(ctx, db, restore); err != nil {
		t.Fatal(err)
	}
	restoreOperation := workspaceV2Operation(t, restore, PhaseRestoreComplete)
	restoreEvent := eventForOperation(restoreOperation)
	restoreEvent.EventID, restoreEvent.SchemaVersion = newWorkspaceID("workspace_lifecycle_event"), WorkspaceLifecycleEventSchemaVersionV2
	if err := placementInsertEvent(ctx, db, restoreEvent, restore.OperationKind); err != nil {
		t.Fatal(err)
	}
	if err := placementInsertProjectEvidence(ctx, db, restore, req, false); err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion, manifest.LifecycleState = WorkspaceArchiveManifestSchemaVersionV2, WorkspaceLifecycleActive
	manifest.RestoreOperationID, manifest.RestorePlanDigest, manifest.RestoredAt = restore.OperationID, restore.PlanDigest, restoreOperation.CompletedAt
	manifest.RestoredFromPath = &restore.Source.Path
	if err := placementInsertManifest(ctx, db, manifest); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `UPDATE storage.workspace_archive_manifests SET manifest_json=manifest_json-'restored_from_path'
 WHERE workspace_archive_operation_id=$1`, legacy.OperationID)
	assertPlacementSQLState(t, err, "23514")
	_, err = db.ExecContext(ctx, `UPDATE storage.workspace_archive_manifests SET manifest_json=jsonb_set(manifest_json,'{archive_payload_path}',$2::jsonb)
 WHERE workspace_archive_operation_id=$1`, legacy.OperationID, placementJSON(t, restore.Source.Path))
	assertPlacementSQLState(t, err, "23514")
}

func placementJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertPlacementSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != code {
		t.Fatalf("expected SQLSTATE %s, got %v", code, err)
	}
}

func placementInsertOperation(ctx context.Context, db *sql.DB, p WorkspaceArchivePlan) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `WITH p AS (SELECT $1::jsonb AS j)
 INSERT INTO storage.workspace_archive_operations(workspace_archive_operation_id,schema_version,evidence_kind,
 operation_kind,workspace_kind,object_id,slug,phase,terminal_status,plan_digest,plan_json,
 source_root,source_relative_path,source_identity_json,source_parent_identity_json,
 destination_root,destination_relative_path,destination_identity_json,destination_parent_identity_json,
 inventory_digest,actor_id,reason,planned_at,intent_committed_at,payload_moved_at,projections_committed_at,completed_at,updated_at)
 SELECT j->>'operation_id',$2,j->>'evidence_kind',j->>'operation_kind',j->>'kind',j->>'object_id',j->>'slug',
 (j->>'operation_kind')||'_complete','complete',j->>'plan_digest',j,
 j#>>'{source,path,root}',j#>>'{source,path,relative_path}',j#>'{source,identity}',j#>'{source,parent_identity}',
 j#>>'{destination,path,root}',j#>>'{destination,path,relative_path}',j#>'{destination,identity}',j#>'{destination,parent_identity}',
 j#>>'{inventory,digest}',j->>'actor_id',j->>'reason',
 (j->>'planned_at')::timestamptz,(j->>'planned_at')::timestamptz,(j->>'planned_at')::timestamptz,
 (j->>'planned_at')::timestamptz,(j->>'planned_at')::timestamptz,(j->>'planned_at')::timestamptz FROM p`, payload, WorkspaceArchiveOperationSchemaForPlan(p))
	return err
}

func placementInsertManifest(ctx context.Context, db *sql.DB, m WorkspaceArchiveManifest) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `WITH m AS (SELECT $1::jsonb AS j)
 INSERT INTO storage.workspace_archive_manifests(workspace_archive_operation_id,schema_version,evidence_kind,
 workspace_kind,object_id,slug,lifecycle_state,plan_digest,restore_plan_digest,inventory_digest,archive_source_identity_json,
 manifest_json,authentication_key_id,authentication_tag,archived_at,restore_operation_id,restore_operation_kind,restored_at)
 SELECT j->>'archive_operation_id',j->>'schema_version',j->>'evidence_kind',j->>'kind',j->>'object_id',j->>'slug',
 j->>'lifecycle_state',j->>'plan_digest',j->>'restore_plan_digest',j->>'inventory_digest',j->'archive_source_identity',j,
 j#>>'{authentication,key_id}',j#>>'{authentication,tag}',(j->>'archived_at')::timestamptz,j->>'restore_operation_id',
 CASE WHEN j->>'restore_operation_id' IS NOT NULL THEN 'restore' END,(j->>'restored_at')::timestamptz FROM m
 ON CONFLICT(workspace_archive_operation_id) DO UPDATE SET schema_version=excluded.schema_version,
 lifecycle_state=excluded.lifecycle_state,restore_plan_digest=excluded.restore_plan_digest,manifest_json=excluded.manifest_json,
 restore_operation_id=excluded.restore_operation_id,restore_operation_kind=excluded.restore_operation_kind,restored_at=excluded.restored_at`, payload)
	return err
}

func placementInsertEvent(ctx context.Context, db *sql.DB, e WorkspaceLifecycleEvent, kind WorkspaceOperationKind) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `WITH e AS (SELECT $1::jsonb AS j)
 INSERT INTO storage.workspace_lifecycle_events(workspace_lifecycle_event_id,schema_version,event_kind,
 workspace_archive_operation_id,operation_kind,workspace_kind,object_id,slug,transition,from_state,to_state,
 source_root,source_relative_path,destination_root,destination_relative_path,actor_id,reason,details,occurred_at)
 SELECT j->>'event_id',j->>'schema_version',j->>'event_kind',j->>'operation_id',$2,j->>'kind',j->>'object_id',j->>'slug',
 j->>'transition',j->>'from_state',j->>'to_state',j#>>'{source_path,root}',j#>>'{source_path,relative_path}',
 j#>>'{destination_path,root}',j#>>'{destination_path,relative_path}',j->>'actor_id',j->>'reason',j,(j->>'occurred_at')::timestamptz FROM e`, payload, kind)
	return err
}

func placementInsertProjectEvidence(ctx context.Context, db *sql.DB, p WorkspaceArchivePlan, req requestctx.Context, mixed bool) error {
	version := "storage.project_physical_" + string(p.OperationKind) + "_plan.v1"
	if p.SchemaVersion == WorkspaceArchivePlanSchemaVersionV2 && !mixed {
		version = "storage.project_physical_" + string(p.OperationKind) + "_plan.v2"
	}
	digest := sha256Digest([]byte("project " + p.OperationID))
	payload, err := json.Marshal(map[string]any{"schema_version": version, "workspace": p, "request": req, "plan_digest": digest})
	if err != nil {
		return err
	}
	request, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO projects.physical_archive_plan_evidence(operation_id,project_id,operation_kind,
 actor_id,origin_node_id,scope_id,request_json,plan_digest,payload_sha256,payload)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, p.OperationID, p.ObjectID, p.OperationKind, req.ActorID, req.OriginNodeID, req.ScopeID, request, digest, sha256Digest(payload), payload)
	return err
}
