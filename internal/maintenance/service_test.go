package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeFindingInputDefaultsKeyAndJSON(t *testing.T) {
	input, err := normalizeFindingInput(UpsertFindingInput{
		WorkerInstanceID: "worker_instance_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		FindingKind:      "migration_not_current",
		SubjectKind:      "database",
		SubjectID:        "loom_main",
		Summary:          "Migrations are not current.",
	})
	if err != nil {
		t.Fatalf("normalizeFindingInput returned error: %v", err)
	}
	if input.Severity != SeverityWarning {
		t.Fatalf("severity = %q, want %q", input.Severity, SeverityWarning)
	}
	wantKey := "worker_instance_01ARZ3NDEKTSV4RRFFQ69G5FAV:migration_not_current:database:loom_main"
	if input.FindingKey != wantKey {
		t.Fatalf("finding key = %q, want %q", input.FindingKey, wantKey)
	}
	if string(input.DetailsJSON) != `{}` {
		t.Fatalf("details json = %s, want {}", input.DetailsJSON)
	}
	if string(input.Metadata) != `{}` {
		t.Fatalf("metadata = %s, want {}", input.Metadata)
	}
}

func TestNormalizeFindingInputRejectsInvalidSeverity(t *testing.T) {
	_, err := normalizeFindingInput(UpsertFindingInput{
		WorkerInstanceID: "worker_instance_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		FindingKind:      "test",
		Severity:         "severe",
		Summary:          "bad severity",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeCreateOperationInputDefaults(t *testing.T) {
	input, err := normalizeCreateOperationInput(CreateOperationInput{
		WorkerInstanceID: "worker_instance_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OperationKind:    OperationKindMainBackup,
	})
	if err != nil {
		t.Fatalf("normalizeCreateOperationInput returned error: %v", err)
	}
	if input.Status != OperationRunning {
		t.Fatalf("status = %q, want %q", input.Status, OperationRunning)
	}
	if input.OperationKey == "" {
		t.Fatal("operation key was not defaulted")
	}
	if string(input.ConfigJSON) != `{}` || string(input.ResultJSON) != `{}` || string(input.ErrorJSON) != `{}` {
		t.Fatalf("json defaults = config:%s result:%s error:%s", input.ConfigJSON, input.ResultJSON, input.ErrorJSON)
	}
}

func TestNormalizeCreateArtifactInputRejectsNegativeSize(t *testing.T) {
	size := int64(-1)
	_, err := normalizeCreateArtifactInput(CreateArtifactInput{
		MaintenanceOperationID: "maintenance_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ArtifactKind:           ArtifactKindPostgresDump,
		URI:                    "file:///tmp/loom_main.dump",
		SizeBytes:              &size,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestGetBackupOperationRequiresExactTypedIDBeforeDatabaseAccess(t *testing.T) {
	service := Service{}
	for _, operationID := range []string{"", " maintenance_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV", "operation-key", "maintenance_operation_bad"} {
		if _, err := service.GetBackupOperation(context.Background(), operationID); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "exact maintenance operation ID") {
			t.Fatalf("operation %q error = %v", operationID, err)
		}
	}
}

func TestNormalizeJSONObjectRejectsArrays(t *testing.T) {
	_, err := normalizeJSONObject(json.RawMessage(`[]`), "details_json")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestDeriveBackupStatus(t *testing.T) {
	if got := deriveBackupStatus(nil, nil, nil, FindingSummary{}); got != OverallUnknown {
		t.Fatalf("status = %q, want %q", got, OverallUnknown)
	}
	success := &BackupOperation{Operation: Operation{Status: OperationSucceeded}}
	if got := deriveBackupStatus(nil, success, nil, FindingSummary{}); got != OverallOK {
		t.Fatalf("status = %q, want %q", got, OverallOK)
	}
	if got := deriveBackupStatus(nil, success, nil, FindingSummary{Warning: 1}); got != OverallWarning {
		t.Fatalf("status = %q, want %q", got, OverallWarning)
	}
	if got := deriveBackupStatus(nil, success, nil, FindingSummary{Critical: 1}); got != OverallCritical {
		t.Fatalf("status = %q, want %q", got, OverallCritical)
	}
}

func TestDeriveObjectStoreStatus(t *testing.T) {
	if got := deriveObjectStoreStatus(nil, ObjectStoreBlobCounts{}, FindingSummary{}); got != OverallOK {
		t.Fatalf("status = %q, want %q", got, OverallOK)
	}
	if got := deriveObjectStoreStatus(nil, ObjectStoreBlobCounts{Missing: 1}, FindingSummary{}); got != OverallCritical {
		t.Fatalf("status = %q, want %q", got, OverallCritical)
	}
	if got := deriveObjectStoreStatus(nil, ObjectStoreBlobCounts{}, FindingSummary{Warning: 1}); got != OverallWarning {
		t.Fatalf("status = %q, want %q", got, OverallWarning)
	}
}

func TestDeriveOverallStatus(t *testing.T) {
	if got := deriveOverallStatus(nil, FindingSummary{}); got != OverallOK {
		t.Fatalf("overall = %q, want %q", got, OverallOK)
	}
	if got := deriveOverallStatus(nil, FindingSummary{Warning: 1}); got != OverallWarning {
		t.Fatalf("overall = %q, want %q", got, OverallWarning)
	}
	if got := deriveOverallStatus(nil, FindingSummary{Error: 1}); got != OverallCritical {
		t.Fatalf("overall = %q, want %q", got, OverallCritical)
	}
	if got := deriveOverallStatus([]WorkerStatus{{HealthStatus: "failed"}}, FindingSummary{}); got != OverallWarning {
		t.Fatalf("overall = %q, want %q", got, OverallWarning)
	}
}

func TestOperationResultWithVerification(t *testing.T) {
	verification := BackupVerification{
		Status:    VerificationSucceeded,
		BackupDir: "/tmp/backup",
		Checks:    map[string]string{"manifest": VerificationSucceeded},
		Errors:    []string{},
	}
	raw, err := operationResultWithVerification(json.RawMessage(`{"schema_version":"main_backup.result.v0.2"}`), verification)
	if err != nil {
		t.Fatalf("operationResultWithVerification returned error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if decoded["verification_status"] != VerificationSucceeded {
		t.Fatalf("verification_status = %v, want %s", decoded["verification_status"], VerificationSucceeded)
	}
	if decoded["verification"] == nil {
		t.Fatal("verification field was not set")
	}
}

func TestBackupOperationMatchesRefUsesRecordedBackupDirectory(t *testing.T) {
	tests := []struct {
		name      string
		operation Operation
		artifacts []Artifact
		ref       string
		want      bool
	}{
		{
			name: "result backup dir",
			operation: Operation{
				MaintenanceOperationID: "maintenance_operation_result",
				ResultJSON:             json.RawMessage(`{"backup_dir":"/var/lib/loom/backups/main/20260706T183825Z-GAV77GF8KWBENQE8"}`),
			},
			ref:  "/var/lib/loom/backups/main/20260706T183825Z-GAV77GF8KWBENQE8",
			want: true,
		},
		{
			name: "result manifest path",
			operation: Operation{
				MaintenanceOperationID: "maintenance_operation_manifest_path",
				ResultJSON:             json.RawMessage(`{"manifest_path":"/var/lib/loom/backups/main/20260706T183825Z-GAV77GF8KWBENQE8/manifest.json"}`),
			},
			ref:  "/var/lib/loom/backups/main/20260706T183825Z-GAV77GF8KWBENQE8/",
			want: true,
		},
		{
			name: "manifest artifact uri",
			operation: Operation{
				MaintenanceOperationID: "maintenance_operation_artifact",
				ResultJSON:             json.RawMessage(`{}`),
			},
			artifacts: []Artifact{{
				ArtifactKind: ArtifactKindBackupManifest,
				URI:          "file:///var/lib/loom/backups/main/20260706T183825Z-GAV77GF8KWBENQE8/manifest.json",
			}},
			ref:  "/var/lib/loom/backups/main/20260706T183825Z-GAV77GF8KWBENQE8",
			want: true,
		},
		{
			name: "different backup dir",
			operation: Operation{
				MaintenanceOperationID: "maintenance_operation_other",
				ResultJSON:             json.RawMessage(`{"backup_dir":"/var/lib/loom/backups/main/other"}`),
			},
			ref:  "/var/lib/loom/backups/main/20260706T183825Z-GAV77GF8KWBENQE8",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := backupOperationMatchesRef(tc.operation, tc.artifacts, tc.ref); got != tc.want {
				t.Fatalf("backupOperationMatchesRef() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestDBStatusApplyRunSummary(t *testing.T) {
	status := DBStatus{Status: "unknown", MigrationStatus: "unknown"}
	status.applyRunSummary(json.RawMessage(`{"status":"ok","migration_status":"ok","current_version":18,"latest_version":18,"pending":0}`))
	if status.Status != "ok" {
		t.Fatalf("status = %q, want ok", status.Status)
	}
	if status.MigrationStatus != "ok" {
		t.Fatalf("migration status = %q, want ok", status.MigrationStatus)
	}
	if status.CurrentVersion != 18 || status.LatestVersion != 18 {
		t.Fatalf("versions = %d/%d, want 18/18", status.CurrentVersion, status.LatestVersion)
	}
}

func TestDatabaseRetentionPlansSeparateCandidatesAndAuditExclusions(t *testing.T) {
	tables := []DatabaseTable{
		{QualifiedName: "events.events", Rows: 1000, TotalBytes: 10_000, RetentionRole: databaseRetentionRole("events.events")},
		{QualifiedName: "workers.worker_runs", Rows: 200, TotalBytes: 20_000, RetentionRole: databaseRetentionRole("workers.worker_runs")},
		{QualifiedName: "maintenance.operations", Rows: 10, TotalBytes: 1_000, RetentionRole: databaseRetentionRole("maintenance.operations")},
	}
	candidates := map[string]int64{
		"events.events":       100,
		"workers.worker_runs": 20,
	}
	plans := databaseRetentionPlans(tables, candidates)
	eventsPlan := retentionPlanByTable(plans, "events.events")
	if eventsPlan.CandidateClass != "routine_success_events" || eventsPlan.CandidateRows != 100 || eventsPlan.EstimatedBytes != 1000 || !eventsPlan.CompactableLater {
		t.Fatalf("unexpected events retention plan: %#v", eventsPlan)
	}
	workerPlan := retentionPlanByTable(plans, "workers.worker_runs")
	if workerPlan.CandidateRows != 20 || workerPlan.CompactableLater {
		t.Fatalf("worker runs should be rollup-only until reference safety is proven: %#v", workerPlan)
	}
	maintenancePlan := retentionPlanByTable(plans, "maintenance.operations")
	if maintenancePlan.CandidateRows != 0 || maintenancePlan.CompactableLater || maintenancePlan.FutureAction != "no destructive retention action planned" {
		t.Fatalf("maintenance operations should stay audit evidence: %#v", maintenancePlan)
	}
	exclusions := databaseAuditCriticalExclusions()
	if len(exclusions) == 0 || exclusions[0].Table == "" || exclusions[0].Rule == "" {
		t.Fatalf("audit exclusions missing: %#v", exclusions)
	}
}

func TestDatabaseTableWarningsPointToRetentionDryRun(t *testing.T) {
	warnings := databaseTableWarnings([]DatabaseTable{
		{QualifiedName: "events.events", TotalBytes: 6 << 30, Pressure: databasePressure(6 << 30)},
		{QualifiedName: "workers.worker_runs", TotalBytes: 2 << 30, Pressure: databasePressure(2 << 30)},
	})
	if len(warnings) != 2 {
		t.Fatalf("warnings = %#v", warnings)
	}
	for _, warning := range warnings {
		if !strings.Contains(warning, "loom maintenance retention dry-run --json") {
			t.Fatalf("warning should point to retention dry-run: %q", warning)
		}
	}
}

func TestValidateDatabaseRetentionApplyAllowsOnlyReviewedClasses(t *testing.T) {
	plans := []DatabaseRetentionPlan{
		{Table: "events.events", CandidateClass: "routine_success_events", CandidateRows: 10},
		{Table: "workers.worker_leases", CandidateClass: "settled_worker_leases", CandidateRows: 5},
		{Table: "workers.worker_runs", CandidateClass: "routine_succeeded_non_manual_runs", CandidateRows: 3},
		{Table: "maintenance.operations", CandidateClass: "none_audit_evidence", CandidateRows: 0},
	}
	if err := validateDatabaseRetentionApply(plans); err != nil {
		t.Fatalf("validateDatabaseRetentionApply returned error: %v", err)
	}
	if err := validateDatabaseRetentionApply([]DatabaseRetentionPlan{
		{Table: "unknown.table", CandidateClass: "routine", CandidateRows: 1},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown table error = %v, want ErrInvalid", err)
	}
	if err := validateDatabaseRetentionApply([]DatabaseRetentionPlan{
		{Table: "events.events", CandidateClass: "audit_events", CandidateRows: 1},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong event class error = %v, want ErrInvalid", err)
	}
}

func TestDatabaseRetentionApplyWarningsCallOutPreservedTables(t *testing.T) {
	warnings := databaseRetentionApplyWarnings([]DatabaseRetentionPlan{
		{Table: "workers.worker_runs", CandidateRows: 4},
		{Table: "workers.worker_controls", CandidateRows: 2},
	})
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"worker-run deletion remains disabled", "control deletion remains disabled", "audit/security events", "manual actions"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warnings missing %q:\n%s", want, joined)
		}
	}
}

func TestDatabaseCompactionLimitsAreBoundedAndTableScoped(t *testing.T) {
	limits, warnings := databaseCompactionLimits(DatabaseCompactInput{
		MaxRowsPerBatch: maxDBCompactMaxRowsPerBatch + 1,
		MaxTotalRows:    maxDBCompactMaxTotalRows + 1,
	}, []DatabaseRetentionPlan{
		{Table: "events.events", CandidateRows: maxDBCompactMaxTotalRows + 1000},
		{Table: "workers.worker_leases", CandidateRows: 0},
		{Table: "workers.worker_controls", CandidateRows: 20},
	})
	if limits.MaxRowsPerBatch != maxDBCompactMaxRowsPerBatch {
		t.Fatalf("max rows per batch = %d, want cap %d", limits.MaxRowsPerBatch, maxDBCompactMaxRowsPerBatch)
	}
	if limits.MaxTotalRows != maxDBCompactMaxTotalRows {
		t.Fatalf("max total rows = %d, want cap %d", limits.MaxTotalRows, maxDBCompactMaxTotalRows)
	}
	if limits.RowLimits["events.events"] != maxDBCompactMaxTotalRows {
		t.Fatalf("events row limit = %d, want capped total %d", limits.RowLimits["events.events"], maxDBCompactMaxTotalRows)
	}
	if limits.RowLimits["workers.worker_leases"] != 0 {
		t.Fatalf("zero-candidate leases row limit = %d, want 0", limits.RowLimits["workers.worker_leases"])
	}
	if _, ok := limits.RowLimits["workers.worker_controls"]; ok {
		t.Fatalf("worker controls should not be in destructive row limits: %#v", limits.RowLimits)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"max_rows_per_batch", "max_total_rows"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warnings missing %q: %#v", want, warnings)
		}
	}
}

func TestDatabaseCompactPlanHashRejectsTampering(t *testing.T) {
	plan := DatabaseCompactResult{
		Status:            "planned",
		DryRun:            true,
		RecentSuccessDays: 14,
		CutoffAt:          time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		AllowedTables:     []string{"events.events", "workers.worker_leases"},
		RowLimits:         map[string]int64{"events.events": 100, "workers.worker_leases": 10},
		MaxRowsPerBatch:   50,
		MaxTotalRows:      110,
		Candidates:        map[string]int64{"events.events": 100, "workers.worker_leases": 10},
		Policy:            databaseRetentionPolicy(),
		Plans: []DatabaseRetentionPlan{
			{Table: "events.events", CandidateClass: "routine_success_events", CandidateRows: 100},
			{Table: "workers.worker_leases", CandidateClass: "settled_worker_leases", CandidateRows: 10},
		},
		AuditCriticalExclusions: databaseAuditCriticalExclusions(),
	}
	plan.PlanHash = databaseCompactPlanHash(plan)
	plan.PlanID = databaseCompactPlanID(plan.PlanHash)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	decoded, ok, err := decodeDatabaseCompactPlan(raw)
	if err != nil {
		t.Fatalf("decode plan returned error: %v", err)
	}
	if !ok || decoded.PlanHash != plan.PlanHash || decoded.PlanID != plan.PlanID {
		t.Fatalf("decoded plan mismatch: ok=%t decoded=%#v plan=%#v", ok, decoded, plan)
	}

	plan.Candidates["events.events"] = 101
	tampered, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal tampered plan: %v", err)
	}
	if _, _, err := decodeDatabaseCompactPlan(tampered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered plan error = %v, want ErrInvalid", err)
	}
}

func TestDatabaseCandidateDriftReportsAddedChangedAndMissing(t *testing.T) {
	drift := databaseCandidateDrift(
		map[string]int64{"events.events": 10, "workers.worker_leases": 5, "workers.worker_runs": 2},
		map[string]int64{"events.events": 13, "workers.worker_leases": 5, "workers.worker_controls": 1},
	)
	if drift["events.events"] != 3 || drift["workers.worker_runs"] != -2 || drift["workers.worker_controls"] != 1 {
		t.Fatalf("unexpected drift: %#v", drift)
	}
	if got := databaseCandidateDrift(map[string]int64{"events.events": 10}, map[string]int64{"events.events": 10}); got != nil {
		t.Fatalf("unchanged candidates drift = %#v, want nil", got)
	}
}

func retentionPlanByTable(plans []DatabaseRetentionPlan, table string) DatabaseRetentionPlan {
	for _, plan := range plans {
		if plan.Table == table {
			return plan
		}
	}
	return DatabaseRetentionPlan{}
}
