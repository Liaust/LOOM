package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceActiveLocationMigrationContract(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	version, err := LatestVersion(dir)
	if err != nil || version < 113 {
		t.Fatalf("active-location migration is not discoverable: version=%d err=%v", version, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "00113_workspace_active_location.sql"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(raw), "-- +goose Down")
	if len(parts) != 2 {
		t.Fatal("migration needs explicit up/down sections")
	}
	for _, required := range []string{
		"CREATE FUNCTION storage.workspace_archive_v3_active",
		"CREATE FUNCTION storage.workspace_archive_v3_location",
		"CREATE FUNCTION storage.workspace_archive_v3_plan",
		"CREATE FUNCTION storage.workspace_archive_v3_manifest",
		"CREATE FUNCTION projects.physical_archive_v3_plan",
		"c.contype='c' AND c.convalidated",
		`ORDER BY a.attname::text COLLATE "C"`,
		`ORDER BY col COLLATE "C"`,
		"cardinality(names) IS DISTINCT FROM 1",
		"legacy_gate,expressions[1],v3_gate,v3_expression",
		"schema_version IN ('storage.workspace_archive_operation.v1','storage.workspace_archive_operation.v2')",
		"schema_version IN ('storage.workspace_archive_manifest.v1','storage.workspace_archive_manifest.v2')",
		"schema_version IN ('storage.workspace_lifecycle_event.v1','storage.workspace_lifecycle_event.v2')",
		"'storage.project_physical_'||operation_kind||'_plan.v3'",
		"9223372036854775807",
		"source_revision IS DISTINCT FROM COALESCE((c->>'repository_source_revision')::numeric,0)",
		"l IS DISTINCT FROM w->'active_location'",
		"l IS DISTINCT FROM archive->'active_location'",
		"p->>'activation_state' IS DISTINCT FROM 'inactive'",
		"workspace_archive_manifests_archive_version_fkey",
		"WHEN schema_version='storage.workspace_archive_manifest.v3' THEN 'storage.workspace_archive_operation.v3'",
		"CREATE TRIGGER workspace_archive_v3_operation",
		"NEW.plan_json->'active_location' IS DISTINCT FROM archived->'active_location'",
		"CREATE TRIGGER workspace_archive_v3_manifest_paths",
		"CREATE TRIGGER workspace_archive_v3_event_paths",
		"CHECK(kind IN ('notes','box','retained'))",
		"CHECK(phase IN ('planned','prepared','complete','cancelled','location_successor'))",
	} {
		if !strings.Contains(parts[0], required) {
			t.Errorf("missing active-location migration contract: %s", required)
		}
	}
	for _, forbidden := range []string{
		"CREATE OR REPLACE FUNCTION storage.workspace_archive_v2",
		"DROP FUNCTION storage.workspace_archive_v2",
		"DROP TRIGGER workspace_archive_v2_manifest_paths",
		"UPDATE storage.workspace_archive_",
		"UPDATE projects.physical_archive_plan_evidence",
		"DELETE FROM storage.",
		"DELETE FROM projects.",
	} {
		if strings.Contains(parts[0], forbidden) {
			t.Errorf("migration changes historical interpretation/evidence: %s", forbidden)
		}
	}
	if !strings.Contains(parts[1], "RAISE EXCEPTION") || !strings.Contains(parts[1], "forward-only") {
		t.Fatal("downgrade must refuse rather than remove v3 or successor history")
	}
	if strings.Count(string(raw), "-- +goose StatementBegin") != strings.Count(string(raw), "-- +goose StatementEnd") {
		t.Fatal("unbalanced goose statement blocks")
	}
}
