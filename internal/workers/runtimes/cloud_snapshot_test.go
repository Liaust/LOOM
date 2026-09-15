package runtimes

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/workers"
)

func TestCloudSnapshotUploadRuntimeDescriptorAndInstance(t *testing.T) {
	runtime := NewCloudSnapshotUploadRuntime(maintenance.Service{}, "/var/lib/loom", "loom-main", backupcoverage.Options{})
	if runtime.Kind() != workers.KindCloudSnapshotUpload {
		t.Fatalf("kind = %q", runtime.Kind())
	}
	assertManualTransitionTickPolicy(t, runtime.Describe().DefaultTickPolicyJSON)
	assertCloudSnapshotUploadTimeoutPolicy(t, runtime.Describe().DefaultTimeoutPolicyJSON)
	instances := runtime.DefaultInstances()
	if len(instances) != 1 || instances[0].WorkerKey != "main.cloud_snapshot_upload" {
		t.Fatalf("instances = %#v", instances)
	}
	assertManualTransitionTickPolicy(t, instances[0].TickPolicyJSON)
	assertCloudSnapshotUploadTimeoutPolicy(t, instances[0].TimeoutPolicyJSON)
	var config cloudSnapshotUploadConfig
	if err := json.Unmarshal(instances[0].ConfigJSON, &config); err != nil {
		t.Fatal(err)
	}
	if config.MaxRuntimeMS != cloudSnapshotUploadMaxRuntimeMS {
		t.Fatalf("seeded cloud worker max runtime = %dms, want %dms", config.MaxRuntimeMS, cloudSnapshotUploadMaxRuntimeMS)
	}
	if config.MaxRuntimeMS != cloudSnapshotUploadRunTimeoutSeconds*1000 {
		t.Fatalf("seeded cloud worker max runtime = %dms, timeout policy = %ds", config.MaxRuntimeMS, cloudSnapshotUploadRunTimeoutSeconds)
	}
}

func assertCloudSnapshotUploadTimeoutPolicy(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var policy struct {
		SchemaVersion     string `json:"schema_version"`
		RunTimeoutSeconds int    `json:"run_timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.SchemaVersion != "worker_timeout_policy.v0.2" || policy.RunTimeoutSeconds != cloudSnapshotUploadRunTimeoutSeconds {
		t.Fatalf("cloud worker timeout policy = %#v", policy)
	}
}

func TestCloudSnapshotUploadSeedRefreshesExistingManagedProductionInstanceTimeout(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("LOOM_TEST_DB_URL"))
	if databaseURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_cloud_snapshot_seed_refresh")
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewCloudSnapshotUploadRuntime(maintenance.Service{}, "/var/lib/loom", "main", backupcoverage.Options{})
	registry := workers.NewRegistry()
	if err := registry.Register(runtime); err != nil {
		t.Fatal(err)
	}
	service := workers.NewService(db, registry, nil)
	if _, err := service.SeedBuiltins(ctx, req, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE workers.worker_instances
		SET config_json = jsonb_set(config_json, '{max_runtime_ms}', '3600000'::jsonb, true),
		    tick_policy_json = '{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}'::jsonb,
		    timeout_policy_json = '{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":3600}'::jsonb,
		    metadata = metadata || '{"policy_control":{"source":"test-existing-managed-instance"}}'::jsonb
		WHERE worker_key = 'main.cloud_snapshot_upload'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE workers.worker_kinds
		SET default_timeout_policy_json = '{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":3600}'::jsonb
		WHERE worker_kind = 'cloud_snapshot_upload'
	`); err != nil {
		t.Fatal(err)
	}
	seeded, err := service.SeedBuiltins(ctx, req, "main")
	if err != nil {
		t.Fatal(err)
	}
	if seeded.InstancesUpdated != 1 || seeded.InstancesCreated != 0 || seeded.KindsUpdated != 1 {
		t.Fatalf("seed refresh result = %#v", seeded)
	}
	var maxRuntimeMS, instanceTimeoutSeconds, kindTimeoutSeconds int
	var tickMode string
	if err := db.QueryRowContext(ctx, `
		SELECT (config_json->>'max_runtime_ms')::int,
		       (timeout_policy_json->>'run_timeout_seconds')::int,
		       tick_policy_json->>'mode'
		FROM workers.worker_instances
		WHERE worker_key = 'main.cloud_snapshot_upload'
	`).Scan(&maxRuntimeMS, &instanceTimeoutSeconds, &tickMode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT (default_timeout_policy_json->>'run_timeout_seconds')::int
		FROM workers.worker_kinds
		WHERE worker_kind = 'cloud_snapshot_upload'
	`).Scan(&kindTimeoutSeconds); err != nil {
		t.Fatal(err)
	}
	if maxRuntimeMS != cloudSnapshotUploadMaxRuntimeMS || instanceTimeoutSeconds != cloudSnapshotUploadRunTimeoutSeconds || kindTimeoutSeconds != cloudSnapshotUploadRunTimeoutSeconds || tickMode != workers.TickModeManual {
		t.Fatalf("refreshed managed cloud worker = max_runtime_ms:%d instance_timeout:%d kind_timeout:%d tick:%s", maxRuntimeMS, instanceTimeoutSeconds, kindTimeoutSeconds, tickMode)
	}
}

func TestCloudSnapshotCompletionContextSurvivesRuntimeCancellation(t *testing.T) {
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	cancelRuntime()
	completionCtx, cancelCompletion := cloudSnapshotCompletionContext(runtimeCtx)
	defer cancelCompletion()
	if err := completionCtx.Err(); err != nil {
		t.Fatalf("completion context inherited cancellation: %v", err)
	}
	if deadline, ok := completionCtx.Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > cloudSnapshotCompletionTimeout {
		t.Fatalf("completion deadline = %v ok=%t", deadline, ok)
	}
}

func TestCloudSnapshotUploadRuntimeArchivesReviewedRootsAndVerifiedPackageDirectly(t *testing.T) {
	root := t.TempDir()
	coverage := runtimeDirectArchiveCoverage(t, root)
	cloudConfigPath := writeDirectArchiveCloudConfig(t, root)
	createdAt := time.Date(2026, 8, 30, 7, 0, 0, 0, time.UTC)
	packageRef := runtimeOperationalPackageReference(root, "a", "1", createdAt)
	packageRef.BackupOperationID = ids.NewMaintenanceOperationID()
	packageRef.PackageID = "operational-test"
	packageRef.SchemaHead = 72
	frozenManifestSHA256 := strings.Repeat("b", 64)
	var captured cloudstorage.DirectArchiveInput
	runtime := NewCloudSnapshotUploadRuntime(maintenance.Service{}, root, "loom-main", coverage)
	runtime.agentsRoot = runtimeDirectArchiveAgentsRoot(root)
	runtime.Now = func() time.Time { return createdAt.Add(time.Minute) }
	runtime.ResolvePackage = func(_ context.Context, requested string) (OperationalPackageReference, error) {
		if requested != packageRef.BackupOperationID {
			t.Fatalf("requested backup operation = %q, want %q", requested, packageRef.BackupOperationID)
		}
		return packageRef, nil
	}
	runtime.ArchiveCanonical = func(_ context.Context, input cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
		captured = input
		return cloudstorage.DirectArchiveResult{
			Status: cloudstorage.DirectArchiveStatusSucceeded, Backend: cloudstorage.SnapshotBackendBorg, Repository: "repo",
			Archive: "loom-main-history", ArchiveRef: input.RequestV2.ArchiveRef, RemoteURI: "borg://repo::loom-main-history",
			PendingArchive: "pending-authenticated", ResumedPending: true, VerificationPhase: "committed",
			ManifestSchema: backupstrategy.DirectArchiveManifestSchemaV2, VerificationProfile: backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1,
			ManifestSHA256: frozenManifestSHA256, Checks: map[string]string{"exact_archive": "succeeded"},
			BorgCommandCounts: map[string]int64{"create": 0, "rename": 1}, StageDurationsMS: map[string]int64{"metadata_check": 7},
			OperationalPackageID: packageRef.PackageID, OperationalPackageManifestSHA256: packageRef.ManifestSHA256,
			ProvenancePackageID: packageRef.ProvenancePackageID, ProvenancePackageManifestSHA256: packageRef.ProvenanceManifestSHA256,
			Committed: true, PackedBytes: 101, DeduplicatedBytes: 41,
		}, nil
	}
	run := runtimeDirectArchiveRunContext(t, cloudConfigPath, `{"schema_version":"cloud_snapshot_upload.config.v0.6.4","backup_root":"/tmp/operator-selected","data_dir":"/tmp/operator-selected"}`)
	run.Run.Metadata = cloudSnapshotRunMetadataWithSelector(packageRef.BackupOperationID)
	result, err := runtime.RunOnce(context.Background(), run)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Counters["archived"] != 1 || len(result.CheckpointUpdates) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if captured.RequestV2 == nil || captured.Request.Schema != "" {
		t.Fatalf("runtime did not send exactly one v2 request: %#v", captured)
	}
	request := captured.RequestV2
	if request.Schema != backupstrategy.DirectArchiveRequestSchemaV2 || request.VerificationProfile != backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1 || request.OperationalPackage.Path != packageRef.PackageDir || request.OperationalPackage.ManifestSHA256 != packageRef.ManifestSHA256 || request.ProvenancePackage.Path != packageRef.ProvenancePackageDir || request.ProvenancePackage.ManifestSHA256 != packageRef.ProvenanceManifestSHA256 || request.CreatedAt != createdAt {
		t.Fatalf("request package identity = %#v", request)
	}
	if request.ArchiveRef != recoveryArchiveRef(packageRef.ManifestSHA256, packageRef.ProvenanceManifestSHA256) {
		t.Fatalf("archive ref = %q", request.ArchiveRef)
	}
	if len(request.Roots) != 11 {
		t.Fatalf("roots = %#v", request.Roots)
	}
	wantRoots := map[string]string{
		"object_store": coverage.ObjectStoreRoot, "imports": coverage.ImportsRoot, "user_backups": coverage.UserBackupsRoot,
		"main_documents": coverage.MainDocumentsRoot, "box_notes": coverage.BoxNotesRoot, "box_projects": filepath.Join(coverage.MainBoxPath, "Projects"),
		"box_topics": filepath.Join(coverage.MainBoxPath, "Topics"), "box_library": filepath.Join(coverage.MainBoxPath, "Library"),
		"storage_retention": coverage.StorageRetentionRoot, "storage_archive": coverage.StorageArchiveRoot,
		"agents": runtimeDirectArchiveAgentsRoot(root),
	}
	for _, archiveRoot := range request.Roots {
		wantPath, ok := wantRoots[archiveRoot.Name]
		if !ok || archiveRoot.Path != wantPath {
			t.Fatalf("unexpected archive root: %#v", archiveRoot)
		}
		delete(wantRoots, archiveRoot.Name)
		if strings.HasPrefix(archiveRoot.Path, "/tmp/operator-selected") {
			t.Fatalf("historical config selected archive root: %#v", archiveRoot)
		}
		if archiveRoot.Path == "/home/agents" || strings.HasPrefix(archiveRoot.Path, "/home/agents/") {
			t.Fatalf("deprecated agents root entered request: %#v", archiveRoot)
		}
	}
	if len(wantRoots) != 0 {
		t.Fatalf("required roots missing from request: %#v", wantRoots)
	}
	if len(request.Exclusions) != 1 || request.Exclusions[0].Root != "main_documents" || request.Exclusions[0].RelativePath != ".loom-acceptance" {
		t.Fatalf("exclusions = %#v", request.Exclusions)
	}
	var summary map[string]any
	if err := json.Unmarshal(result.ResultSummary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary["phase"] != cloudSnapshotUploadPhase || summary["committed"] != true || summary["resumed_pending"] != true || summary["verification_phase"] != "committed" || summary["manifest_schema"] != backupstrategy.DirectArchiveManifestSchemaV2 || summary["verification_profile"] != backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1 || summary["manifest_sha256"] != frozenManifestSHA256 || summary["complete_local_user_data_generation_created"] != false || summary["requested_backup_operation_id"] != packageRef.BackupOperationID || summary["selected_backup_operation_id"] != packageRef.BackupOperationID || summary["backup_operation_selector_matched"] != true {
		t.Fatalf("summary = %#v", summary)
	}
	counts, _ := summary["borg_command_counts"].(map[string]any)
	if counts["create"] != float64(0) || counts["rename"] != float64(1) {
		t.Fatalf("summary command counts = %#v", counts)
	}
	var checkpoint map[string]any
	if err := json.Unmarshal(result.CheckpointUpdates[0].Value, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint["committed"] != true || checkpoint["resumed_pending"] != true || checkpoint["pending_archive"] != "pending-authenticated" || checkpoint["verification_phase"] != "committed" || checkpoint["manifest_schema"] != backupstrategy.DirectArchiveManifestSchemaV2 || checkpoint["verification_profile"] != backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1 || checkpoint["manifest_sha256"] != frozenManifestSHA256 || checkpoint["operational_package_manifest_sha256"] != packageRef.ManifestSHA256 || checkpoint["provenance_manifest_sha256"] != packageRef.ProvenanceManifestSHA256 || checkpoint["requested_backup_operation_id"] != packageRef.BackupOperationID || checkpoint["selected_backup_operation_id"] != packageRef.BackupOperationID || checkpoint["backup_operation_selector_matched"] != true {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	artifactInput := cloudDirectArchiveArtifactInput("maintenance_operation_cloud", cloudstorage.DirectArchiveResult{
		ManifestSHA256: frozenManifestSHA256, RemoteURI: "borg://repo::loom-main-history", Archive: "loom-main-history",
		ArchiveRef: request.ArchiveRef, Backend: cloudstorage.SnapshotBackendBorg, PendingArchive: "pending-authenticated",
		ManifestSchema: backupstrategy.DirectArchiveManifestSchemaV2, VerificationProfile: backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1,
		ResumedPending: true, VerificationPhase: "committed", BorgCommandCounts: map[string]int64{"create": 0, "rename": 1}, StageDurationsMS: map[string]int64{"metadata_check": 7}, PackedBytes: 101,
	}, packageRef, packageRef.BackupOperationID)
	if artifactInput.SHA256 != frozenManifestSHA256 {
		t.Fatalf("artifact manifest hash = %q", artifactInput.SHA256)
	}
	var artifactMetadata map[string]any
	if err := json.Unmarshal(artifactInput.Metadata, &artifactMetadata); err != nil {
		t.Fatal(err)
	}
	if artifactMetadata["manifest_schema"] != backupstrategy.DirectArchiveManifestSchemaV2 || artifactMetadata["verification_profile"] != backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1 || artifactMetadata["manifest_sha256"] != frozenManifestSHA256 || artifactMetadata["requested_backup_operation_id"] != packageRef.BackupOperationID || artifactMetadata["selected_backup_operation_id"] != packageRef.BackupOperationID || artifactMetadata["backup_operation_selector_matched"] != true {
		t.Fatalf("artifact metadata = %#v", artifactMetadata)
	}
}

func TestSelectCloudRecoveryPackageUsesExactOlderOperationAndRejectsInvalidSelections(t *testing.T) {
	selected := ids.NewMaintenanceOperationID()
	newer := ids.NewMaintenanceOperationID()
	newerBackup := runtimeMaintenanceBackupOperation(newer)
	selectedBackup := runtimeMaintenanceBackupOperation(selected)
	got, result, err := selectCloudRecoveryPackage([]maintenance.BackupOperation{newerBackup, selectedBackup}, selected)
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation.MaintenanceOperationID != selected || result.BackupOperationID != selected || result.PackageDir != "/operational/"+selected {
		t.Fatalf("selected operation = %#v result=%#v", got.Operation, result)
	}
	latest, _, err := selectCloudRecoveryPackage([]maintenance.BackupOperation{newerBackup, selectedBackup}, "")
	if err != nil || latest.Operation.MaintenanceOperationID != newer {
		t.Fatalf("default latest selection = %#v err=%v", latest.Operation, err)
	}

	tests := []struct {
		name    string
		backups []maintenance.BackupOperation
		want    string
	}{
		{name: "unknown", backups: []maintenance.BackupOperation{newerBackup}, want: "not found"},
		{name: "wrong_kind", backups: func() []maintenance.BackupOperation {
			candidate := runtimeMaintenanceBackupOperation(selected)
			candidate.Operation.OperationKind = maintenance.OperationKindCloudSnapshotUpload
			return []maintenance.BackupOperation{candidate}
		}(), want: "not a main backup"},
		{name: "failed", backups: func() []maintenance.BackupOperation {
			candidate := runtimeMaintenanceBackupOperation(selected)
			candidate.Operation.Status = maintenance.OperationFailed
			return []maintenance.BackupOperation{candidate}
		}(), want: "did not succeed"},
		{name: "noncommitted", backups: func() []maintenance.BackupOperation {
			candidate := runtimeMaintenanceBackupOperation(selected)
			var result map[string]any
			_ = json.Unmarshal(candidate.Operation.ResultJSON, &result)
			result["committed"] = false
			candidate.Operation.ResultJSON = mustWorkerJSON(result)
			return []maintenance.BackupOperation{candidate}
		}(), want: "no committed"},
		{name: "mismatched_result", backups: func() []maintenance.BackupOperation {
			candidate := runtimeMaintenanceBackupOperation(selected)
			var result map[string]any
			_ = json.Unmarshal(candidate.Operation.ResultJSON, &result)
			result["backup_operation_id"] = newer
			candidate.Operation.ResultJSON = mustWorkerJSON(result)
			return []maintenance.BackupOperation{candidate}
		}(), want: "no committed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := selectCloudRecoveryPackage(test.backups, selected); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("selection error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCloudSnapshotBackupOperationSelectorStrictlyParsesWorkerMetadata(t *testing.T) {
	selected := ids.NewMaintenanceOperationID()
	got, err := cloudSnapshotBackupOperationSelector(cloudSnapshotRunMetadataWithSelector(selected))
	if err != nil || got != selected {
		t.Fatalf("selector = %q err=%v", got, err)
	}
	if got, err := cloudSnapshotBackupOperationSelector(json.RawMessage(`{"schema_version":"worker_run.metadata.v0.2","request_metadata":{"source":"schedule"}}`)); err != nil || got != "" {
		t.Fatalf("default selector = %q err=%v", got, err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"schema_version":"worker_run.metadata.v0.1","request_metadata":{"backup_operation_id":"` + selected + `"}}`),
		json.RawMessage(`{"schema_version":"worker_run.metadata.v0.2","request_metadata":{"backup_operation_id":""}}`),
		json.RawMessage(`{"schema_version":"worker_run.metadata.v0.2","request_metadata":{"backup_operation_id":7}}`),
		json.RawMessage(`{"schema_version":"worker_run.metadata.v0.2","request_metadata":[]}`),
	} {
		if _, err := cloudSnapshotBackupOperationSelector(raw); err == nil {
			t.Fatalf("malformed selector metadata was accepted: %s", raw)
		}
	}
}

func TestCloudSnapshotScheduledDailyRecoveryPackageWindowNormalAndDSTDays(t *testing.T) {
	policy := workers.TickPolicy{Mode: workers.TickModeDailyLocal, LocalTime: "03:15", Timezone: "Europe/Amsterdam"}
	for _, test := range []struct {
		name       string
		runStart   time.Time
		createdAt  time.Time
		wantReject bool
	}{
		{name: "normal window start", runStart: time.Date(2026, 2, 10, 2, 15, 0, 0, time.UTC), createdAt: time.Date(2026, 2, 10, 2, 0, 0, 0, time.UTC)},
		{name: "normal before window", runStart: time.Date(2026, 2, 10, 2, 15, 0, 0, time.UTC), createdAt: time.Date(2026, 2, 10, 1, 59, 59, 0, time.UTC), wantReject: true},
		{name: "spring window start", runStart: time.Date(2026, 3, 29, 1, 15, 0, 0, time.UTC), createdAt: time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)},
		{name: "fall window start", runStart: time.Date(2026, 10, 25, 2, 15, 0, 0, time.UTC), createdAt: time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)},
		{name: "yesterday", runStart: time.Date(2026, 8, 31, 1, 15, 0, 0, time.UTC), createdAt: time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC), wantReject: true},
		{name: "backup overrun", runStart: time.Date(2026, 8, 31, 1, 15, 0, 0, time.UTC), createdAt: time.Date(2026, 8, 31, 1, 16, 0, 0, time.UTC), wantReject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := workers.RunContext{StartedAt: test.runStart, Run: workers.WorkerRun{TriggerKind: workers.TriggerSupervisorTick, StartedAt: test.runStart}}
			err := validateScheduledCloudRecoveryPackage(run, policy, OperationalPackageReference{CreatedAt: test.createdAt})
			if test.wantReject && err == nil {
				t.Fatal("out-of-window scheduled package was accepted")
			}
			if !test.wantReject && err != nil {
				t.Fatalf("fresh scheduled package rejected: %v", err)
			}
		})
	}

	manualRun := workers.RunContext{StartedAt: time.Date(2026, 8, 31, 1, 15, 0, 0, time.UTC), Run: workers.WorkerRun{TriggerKind: workers.TriggerManual}}
	if err := validateScheduledCloudRecoveryPackage(manualRun, policy, OperationalPackageReference{CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("manual exact-selector semantics were changed: %v", err)
	}
}

func TestCloudSnapshotScheduledDailyRejectsStaleOrMissingPackageBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		resolve func(string, time.Time) (OperationalPackageReference, error)
		want    string
	}{
		{
			name: "stale yesterday after failed package run",
			resolve: func(root string, _ time.Time) (OperationalPackageReference, error) {
				return runtimeOperationalPackageReference(root, "a", "b", time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC)), nil
			},
			want: "outside the same-day",
		},
		{
			name: "missing after failed package run",
			resolve: func(string, time.Time) (OperationalPackageReference, error) {
				return OperationalPackageReference{}, maintenance.ErrNotFound
			},
			want: "requires a fresh same-day recovery package",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			mutationCount := 0
			db := sql.OpenDB(cloudMutationCountingConnector{count: &mutationCount})
			t.Cleanup(func() { _ = db.Close() })
			archiveCalls := 0
			runStart := time.Date(2026, 8, 31, 1, 15, 0, 0, time.UTC)
			runtime := NewCloudSnapshotUploadRuntime(maintenance.NewService(db), root, "main", runtimeDirectArchiveCoverage(t, root))
			runtime.agentsRoot = runtimeDirectArchiveAgentsRoot(root)
			runtime.ResolvePackage = func(_ context.Context, _ string) (OperationalPackageReference, error) {
				return test.resolve(root, runStart)
			}
			runtime.ArchiveCanonical = func(context.Context, cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
				archiveCalls++
				return cloudstorage.DirectArchiveResult{}, nil
			}
			run := runtimeDirectArchiveRunContext(t, writeDirectArchiveCloudConfig(t, root), `{}`)
			run.Instance.TickPolicyJSON = json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"daily_local","local_time":"03:15","timezone":"Europe/Amsterdam"}`)
			run.Run.TriggerKind = workers.TriggerSupervisorTick
			run.Run.StartedAt = runStart
			run.StartedAt = runStart
			if _, err := runtime.RunOnce(context.Background(), run); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("scheduled stale-package error = %v, want %q", err, test.want)
			}
			if archiveCalls != 0 || mutationCount != 0 {
				t.Fatalf("stale scheduled package mutated state: archive=%d maintenance=%d", archiveCalls, mutationCount)
			}
		})
	}
}

func TestCloudSnapshotUploadRuntimeRejectsResolvedSelectorMismatchBeforeMutation(t *testing.T) {
	root := t.TempDir()
	selected := ids.NewMaintenanceOperationID()
	resolved := runtimeOperationalPackageReference(root, "a", "b", time.Now().UTC())
	resolved.BackupOperationID = ids.NewMaintenanceOperationID()
	mutationCount := 0
	db := sql.OpenDB(cloudMutationCountingConnector{count: &mutationCount})
	t.Cleanup(func() { _ = db.Close() })
	archiveCalls := 0
	runtime := NewCloudSnapshotUploadRuntime(maintenance.NewService(db), root, "main", runtimeDirectArchiveCoverage(t, root))
	runtime.ResolvePackage = func(context.Context, string) (OperationalPackageReference, error) { return resolved, nil }
	runtime.ArchiveCanonical = func(context.Context, cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
		archiveCalls++
		return cloudstorage.DirectArchiveResult{}, nil
	}
	run := runtimeDirectArchiveRunContext(t, writeDirectArchiveCloudConfig(t, root), `{}`)
	run.Run.Metadata = cloudSnapshotRunMetadataWithSelector(selected)
	if _, err := runtime.RunOnce(context.Background(), run); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("selector mismatch error = %v", err)
	}
	if archiveCalls != 0 || mutationCount != 0 {
		t.Fatalf("selector mismatch mutated state: archive=%d maintenance=%d", archiveCalls, mutationCount)
	}
}

func TestCloudSnapshotUploadRuntimeRejectsUnusableExactSelectionsBeforeMaintenanceOrBorg(t *testing.T) {
	selected := ids.NewMaintenanceOperationID()
	tests := []struct {
		name    string
		resolve func(context.Context, string) (maintenance.BackupOperation, error)
		want    string
	}{
		{name: "unknown", resolve: func(context.Context, string) (maintenance.BackupOperation, error) {
			return maintenance.BackupOperation{}, maintenance.ErrNotFound
		}, want: "not found"},
		{name: "wrong_kind", resolve: func(context.Context, string) (maintenance.BackupOperation, error) {
			candidate := runtimeMaintenanceBackupOperation(selected)
			candidate.Operation.OperationKind = maintenance.OperationKindCloudSnapshotUpload
			return candidate, nil
		}, want: "not a main backup"},
		{name: "failed", resolve: func(context.Context, string) (maintenance.BackupOperation, error) {
			candidate := runtimeMaintenanceBackupOperation(selected)
			candidate.Operation.Status = maintenance.OperationFailed
			return candidate, nil
		}, want: "did not succeed"},
		{name: "noncommitted", resolve: func(context.Context, string) (maintenance.BackupOperation, error) {
			candidate := runtimeMaintenanceBackupOperation(selected)
			var result map[string]any
			_ = json.Unmarshal(candidate.Operation.ResultJSON, &result)
			result["committed"] = false
			candidate.Operation.ResultJSON = mustWorkerJSON(result)
			return candidate, nil
		}, want: "no committed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			mutationCount := 0
			db := sql.OpenDB(cloudMutationCountingConnector{count: &mutationCount})
			t.Cleanup(func() { _ = db.Close() })
			archiveCalls := 0
			runtime := NewCloudSnapshotUploadRuntime(maintenance.NewService(db), root, "main", runtimeDirectArchiveCoverage(t, root))
			runtime.ResolveBackupOperation = test.resolve
			runtime.ArchiveCanonical = func(context.Context, cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
				archiveCalls++
				return cloudstorage.DirectArchiveResult{}, nil
			}
			run := runtimeDirectArchiveRunContext(t, writeDirectArchiveCloudConfig(t, root), `{}`)
			run.Run.Metadata = cloudSnapshotRunMetadataWithSelector(selected)
			if _, err := runtime.RunOnce(context.Background(), run); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("selection error = %v, want %q", err, test.want)
			}
			if archiveCalls != 0 || mutationCount != 0 {
				t.Fatalf("invalid selection mutated state: archive=%d maintenance=%d", archiveCalls, mutationCount)
			}
		})
	}
}

func TestCloudSnapshotUploadRuntimeRawBoxRoots(t *testing.T) {
	for _, area := range []string{"Topics", "Library"} {
		for _, state := range []string{"empty", "file", "symlink", "unreadable", "overlap", "resolved-alias"} {
			t.Run(area+"/"+state, func(t *testing.T) {
				root := t.TempDir()
				coverage := runtimeDirectArchiveCoverage(t, root)
				areaPath := filepath.Join(coverage.MainBoxPath, area)
				if err := os.RemoveAll(areaPath); err != nil {
					t.Fatal(err)
				}
				if state == "file" {
					if err := os.WriteFile(areaPath, []byte("not a root"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else if state == "symlink" {
					if err := os.Symlink(coverage.BoxNotesRoot, areaPath); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(areaPath, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				switch state {
				case "unreadable":
					if os.Geteuid() == 0 {
						t.Skip("root bypasses directory read permissions")
					}
					if err := os.Chmod(areaPath, 0); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(areaPath, 0o700) })
				case "overlap":
					coverage.ObjectStoreRoot = coverage.MainBoxPath
				case "resolved-alias":
					alias := filepath.Join(root, "box-alias")
					if err := os.Symlink(coverage.MainBoxPath, alias); err != nil {
						t.Fatal(err)
					}
					coverage.ObjectStoreRoot = filepath.Join(alias, area)
					if err := os.WriteFile(filepath.Join(areaPath, "unindexed.bin"), []byte{0, 1, 2}, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				mutationCount := 0
				db := sql.OpenDB(cloudMutationCountingConnector{count: &mutationCount})
				t.Cleanup(func() { _ = db.Close() })
				runtime := NewCloudSnapshotUploadRuntime(maintenance.NewService(db), root, "main", coverage)
				runtime.agentsRoot = runtimeDirectArchiveAgentsRoot(root)
				if state == "empty" {
					roots, exclusions, err := runtime.directArchiveRoots()
					if err != nil || len(roots) != 11 || len(exclusions) != 1 {
						t.Fatalf("empty raw Box root: roots=%v exclusions=%v err=%v", roots, exclusions, err)
					}
					return
				}
				runtime.ResolvePackage = func(context.Context, string) (OperationalPackageReference, error) {
					return runtimeOperationalPackageReference(root, "a", "2", time.Now().UTC()), nil
				}
				archiveCalls := 0
				runtime.ArchiveCanonical = func(context.Context, cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
					archiveCalls++
					return cloudstorage.DirectArchiveResult{}, nil
				}
				_, err := runtime.RunOnce(context.Background(), runtimeDirectArchiveRunContext(t, writeDirectArchiveCloudConfig(t, root), `{}`))
				if err == nil || archiveCalls != 0 || mutationCount != 0 {
					t.Fatalf("unsafe raw root: err=%v archive=%d maintenance=%d", err, archiveCalls, mutationCount)
				}
			})
		}
	}
}

func TestCloudSnapshotUploadRuntimeRejectsEveryRequiredRootOmissionBeforeMutation(t *testing.T) {
	omissions := []struct {
		name   string
		omit   func(*backupcoverage.Options)
		remove func(backupcoverage.Options) error
	}{
		{name: "object_store", omit: func(options *backupcoverage.Options) { options.ObjectStoreRoot = "" }},
		{name: "imports", omit: func(options *backupcoverage.Options) { options.ImportsRoot = "" }},
		{name: "user_backups", omit: func(options *backupcoverage.Options) { options.UserBackupsRoot, options.PrivateBackupsRoot = "", "" }},
		{name: "main_documents", omit: func(options *backupcoverage.Options) { options.MainDocumentsRoot = "" }},
		{name: "box_notes", omit: func(options *backupcoverage.Options) { options.BoxNotesRoot = "" }},
		{name: "box_projects", remove: func(options backupcoverage.Options) error {
			return os.RemoveAll(filepath.Join(options.MainBoxPath, "Projects"))
		}},
		{name: "box_topics", remove: func(options backupcoverage.Options) error {
			return os.RemoveAll(filepath.Join(options.MainBoxPath, "Topics"))
		}},
		{name: "box_library", remove: func(options backupcoverage.Options) error {
			return os.RemoveAll(filepath.Join(options.MainBoxPath, "Library"))
		}},
		{name: "storage_retention", omit: func(options *backupcoverage.Options) { options.StorageRetentionRoot = "" }},
		{name: "storage_archive", omit: func(options *backupcoverage.Options) { options.StorageArchiveRoot = "" }},
		{name: "agents", remove: func(options backupcoverage.Options) error {
			return os.RemoveAll(runtimeDirectArchiveAgentsRoot(filepath.Dir(options.ObjectStoreRoot)))
		}},
	}
	for _, omission := range omissions {
		t.Run(omission.name, func(t *testing.T) {
			root := t.TempDir()
			coverage := runtimeDirectArchiveCoverage(t, root)
			if omission.omit != nil {
				omission.omit(&coverage)
			}
			if omission.remove != nil {
				if err := omission.remove(coverage); err != nil {
					t.Fatal(err)
				}
			}
			mutationCount := 0
			db := sql.OpenDB(cloudMutationCountingConnector{count: &mutationCount})
			t.Cleanup(func() { _ = db.Close() })
			archiveCalls := 0
			runtime := NewCloudSnapshotUploadRuntime(maintenance.NewService(db), root, "main", coverage)
			runtime.agentsRoot = runtimeDirectArchiveAgentsRoot(root)
			runtime.ResolvePackage = func(context.Context, string) (OperationalPackageReference, error) {
				return runtimeOperationalPackageReference(root, "a", "2", time.Now().UTC()), nil
			}
			runtime.ArchiveCanonical = func(context.Context, cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
				archiveCalls++
				return cloudstorage.DirectArchiveResult{}, nil
			}
			_, err := runtime.RunOnce(context.Background(), runtimeDirectArchiveRunContext(t, writeDirectArchiveCloudConfig(t, root), `{}`))
			if err == nil || !strings.Contains(err.Error(), omission.name) {
				t.Fatalf("omission error = %v", err)
			}
			if archiveCalls != 0 || mutationCount != 0 {
				t.Fatalf("preflight mutated state: archive calls=%d maintenance calls=%d", archiveCalls, mutationCount)
			}
		})
	}
}

func TestCloudSnapshotUploadRuntimeRejectsReinterpretedProvenanceIdentityBeforeMutation(t *testing.T) {
	root := t.TempDir()
	coverage := runtimeDirectArchiveCoverage(t, root)
	mutationCount := 0
	db := sql.OpenDB(cloudMutationCountingConnector{count: &mutationCount})
	t.Cleanup(func() { _ = db.Close() })
	archiveCalls := 0
	runtime := NewCloudSnapshotUploadRuntime(maintenance.NewService(db), root, "main", coverage)
	runtime.agentsRoot = runtimeDirectArchiveAgentsRoot(root)
	packageRef := runtimeOperationalPackageReference(root, "a", "2", time.Now().UTC())
	packageRef.ProvenanceManifestSHA256 = packageRef.ManifestSHA256
	runtime.ResolvePackage = func(context.Context, string) (OperationalPackageReference, error) { return packageRef, nil }
	runtime.ArchiveCanonical = func(context.Context, cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
		archiveCalls++
		return cloudstorage.DirectArchiveResult{}, nil
	}
	_, err := runtime.RunOnce(context.Background(), runtimeDirectArchiveRunContext(t, writeDirectArchiveCloudConfig(t, root), `{}`))
	if err == nil || !strings.Contains(err.Error(), "provenance package manifest identity") {
		t.Fatalf("reinterpreted provenance identity error = %v", err)
	}
	if archiveCalls != 0 || mutationCount != 0 {
		t.Fatalf("invalid package identity mutated state: archive calls=%d maintenance calls=%d", archiveCalls, mutationCount)
	}
}

func TestCloudSnapshotUploadRuntimeRejectsUnsafeRequiredRootsBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*backupcoverage.Options, string) error
	}{
		{name: "non-directory", mutate: func(options *backupcoverage.Options, root string) error {
			path := filepath.Join(root, "not-a-directory")
			if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
				return err
			}
			options.ObjectStoreRoot = path
			return nil
		}},
		{name: "symlinked-root", mutate: func(options *backupcoverage.Options, root string) error {
			target := filepath.Join(root, "symlink-target")
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			link := filepath.Join(root, "symlink-root")
			if err := os.Symlink(target, link); err != nil {
				return err
			}
			options.ObjectStoreRoot = link
			return nil
		}},
		{name: "symlinked-main-box", mutate: func(options *backupcoverage.Options, root string) error {
			target := options.MainBoxPath
			link := filepath.Join(root, "box-link")
			if err := os.Symlink(target, link); err != nil {
				return err
			}
			options.MainBoxPath = link
			return nil
		}},
		{name: "symlinked-box-projects", mutate: func(options *backupcoverage.Options, root string) error {
			projects := filepath.Join(options.MainBoxPath, "Projects")
			if err := os.RemoveAll(projects); err != nil {
				return err
			}
			target := filepath.Join(root, "projects-target")
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			return os.Symlink(target, projects)
		}},
		{name: "overlap", mutate: func(options *backupcoverage.Options, _ string) error {
			options.StorageArchiveRoot = filepath.Join(options.ObjectStoreRoot, "nested")
			return os.Mkdir(options.StorageArchiveRoot, 0o700)
		}},
		{name: "empty", mutate: func(options *backupcoverage.Options, root string) error {
			path := filepath.Join(root, "empty-root")
			if err := os.Mkdir(path, 0o700); err != nil {
				return err
			}
			options.ObjectStoreRoot = path
			return nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			coverage := runtimeDirectArchiveCoverage(t, root)
			if err := test.mutate(&coverage, root); err != nil {
				t.Fatal(err)
			}
			mutationCount := 0
			db := sql.OpenDB(cloudMutationCountingConnector{count: &mutationCount})
			t.Cleanup(func() { _ = db.Close() })
			archiveCalls := 0
			runtime := NewCloudSnapshotUploadRuntime(maintenance.NewService(db), root, "main", coverage)
			runtime.agentsRoot = runtimeDirectArchiveAgentsRoot(root)
			runtime.ResolvePackage = func(context.Context, string) (OperationalPackageReference, error) {
				return runtimeOperationalPackageReference(root, "b", "3", time.Now().UTC()), nil
			}
			runtime.ArchiveCanonical = func(context.Context, cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
				archiveCalls++
				return cloudstorage.DirectArchiveResult{}, nil
			}
			if _, err := runtime.RunOnce(context.Background(), runtimeDirectArchiveRunContext(t, writeDirectArchiveCloudConfig(t, root), `{}`)); err == nil {
				t.Fatal("unsafe roots were accepted")
			}
			if archiveCalls != 0 || mutationCount != 0 {
				t.Fatalf("preflight mutated state: archive calls=%d maintenance calls=%d", archiveCalls, mutationCount)
			}
		})
	}
}

func TestCloudSnapshotUploadRuntimePropagatesIdempotentReplayTruth(t *testing.T) {
	root := t.TempDir()
	cloudConfigPath := writeDirectArchiveCloudConfig(t, root)
	runtime := NewCloudSnapshotUploadRuntime(maintenance.Service{}, root, "main", runtimeDirectArchiveCoverage(t, root))
	runtime.agentsRoot = runtimeDirectArchiveAgentsRoot(root)
	packageRef := runtimeOperationalPackageReference(root, "c", "4", time.Now().UTC())
	runtime.ResolvePackage = func(context.Context, string) (OperationalPackageReference, error) {
		return packageRef, nil
	}
	runtime.ArchiveCanonical = func(_ context.Context, input cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
		return cloudstorage.DirectArchiveResult{
			Status: cloudstorage.DirectArchiveStatusSucceeded, Backend: cloudstorage.SnapshotBackendBorg, Archive: "archive", ArchiveRef: input.RequestV2.ArchiveRef,
			RemoteURI: "borg://archive", ManifestSchema: backupstrategy.DirectArchiveManifestSchemaV2,
			VerificationProfile: backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1,
			ManifestSHA256:      strings.Repeat("d", 64), Checks: map[string]string{}, Idempotent: true, Committed: true,
			OperationalPackageID: packageRef.PackageID, OperationalPackageManifestSHA256: packageRef.ManifestSHA256,
			ProvenancePackageID: packageRef.ProvenancePackageID, ProvenancePackageManifestSHA256: packageRef.ProvenanceManifestSHA256,
		}, nil
	}
	result, err := runtime.RunOnce(context.Background(), runtimeDirectArchiveRunContext(t, cloudConfigPath, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	_ = json.Unmarshal(result.ResultSummary, &summary)
	if summary["idempotent"] != true || summary["committed"] != true {
		t.Fatalf("replay summary = %#v", summary)
	}
}

func TestCloudSnapshotUploadRuntimeSkippedAttemptPreservesCommittedArchiveCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	runtime := CloudSnapshotUploadRuntime{}
	run := workers.RunContext{
		Run: workers.WorkerRun{WorkerRunID: "worker_run_skipped"},
		Checkpoints: map[string]workers.WorkerCheckpoint{
			"default": {
				SchemaVersion: cloudSnapshotUploadCheckpointSchema,
				CheckpointJSON: mustWorkerJSON(map[string]any{
					"schema_version":  cloudSnapshotUploadCheckpointSchema,
					"phase":           cloudSnapshotUploadPhase,
					"phase_status":    maintenance.OperationSucceeded,
					"committed":       true,
					"archive_ref":     "history-existing",
					"manifest_sha256": strings.Repeat("f", 64),
				}),
			},
		},
	}
	result := runtime.skippedResult(run, now, nil, "cloud_cooling_down", "cooling down")
	var summary map[string]any
	if err := json.Unmarshal(result.ResultSummary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary["committed"] != false || summary["phase_status"] != "skipped" {
		t.Fatalf("attempt summary = %#v", summary)
	}
	var checkpoint map[string]any
	if err := json.Unmarshal(result.CheckpointUpdates[0].Value, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint["committed"] != true || checkpoint["phase_status"] != maintenance.OperationSucceeded || checkpoint["archive_ref"] != "history-existing" {
		t.Fatalf("committed checkpoint was not preserved: %#v", checkpoint)
	}
	if checkpoint["last_attempt_status"] != "skipped" || checkpoint["last_attempt_committed"] != false || checkpoint["last_reason"] != "cooling down" {
		t.Fatalf("latest attempt truth = %#v", checkpoint)
	}
}

func TestCloudSnapshotUploadRuntimeRecordsReplaySafeFailureTruth(t *testing.T) {
	root := t.TempDir()
	cloudConfigPath := writeDirectArchiveCloudConfig(t, root)
	runtime := NewCloudSnapshotUploadRuntime(maintenance.Service{}, root, "main", runtimeDirectArchiveCoverage(t, root))
	runtime.agentsRoot = runtimeDirectArchiveAgentsRoot(root)
	packageRef := runtimeOperationalPackageReference(root, "e", "5", time.Now().UTC())
	runtime.ResolvePackage = func(context.Context, string) (OperationalPackageReference, error) {
		return packageRef, nil
	}
	failedResult := cloudstorage.DirectArchiveResult{
		Status: cloudstorage.DirectArchiveStatusFailed, Code: cloudstorage.DirectArchiveCodeInterrupted,
		ArchiveRef: "history-failed", PendingArchive: "pending", ManifestSchema: backupstrategy.DirectArchiveManifestSchemaV2,
		VerificationProfile: backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1,
		ManifestSHA256:      strings.Repeat("a", 64), Checks: map[string]string{"create": "ambiguous"},
		StageDurationsMS: map[string]int64{"create": 13}, BorgCommandCounts: map[string]int64{"create": 1, "rename": 0},
		OperationalPackageID: packageRef.PackageID, OperationalPackageManifestSHA256: packageRef.ManifestSHA256,
		ProvenancePackageID: packageRef.ProvenancePackageID, ProvenancePackageManifestSHA256: packageRef.ProvenanceManifestSHA256,
		Retryable: true, Committed: false,
	}
	runtime.ArchiveCanonical = func(_ context.Context, input cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error) {
		failedResult.ArchiveRef = input.RequestV2.ArchiveRef
		return failedResult, cloudstorage.ErrDirectArchiveRetryable
	}
	run := runtimeDirectArchiveRunContext(t, cloudConfigPath, `{}`)
	if _, err := runtime.RunOnce(context.Background(), run); !errors.Is(err, cloudstorage.ErrDirectArchiveRetryable) {
		t.Fatalf("failure error = %v", err)
	}
	var summary map[string]any
	if err := json.Unmarshal(cloudDirectArchiveSummary(failedResult, packageRef, "", run, nil), &summary); err != nil {
		t.Fatal(err)
	}
	durations, _ := summary["stage_durations_ms"].(map[string]any)
	counts, _ := summary["borg_command_counts"].(map[string]any)
	if summary["committed"] != false || summary["manifest_schema"] != backupstrategy.DirectArchiveManifestSchemaV2 || summary["verification_profile"] != backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1 || durations["create"] != float64(13) || counts["create"] != float64(1) || counts["rename"] != float64(0) {
		t.Fatalf("failure evidence = %#v", summary)
	}
}

func TestCloudSnapshotUploadRuntimeHistoricalConfigDecodesButCannotSelectRoots(t *testing.T) {
	runtime := NewCloudSnapshotUploadRuntime(maintenance.Service{}, "/var/lib/loom", "main", backupcoverage.Options{})
	cfg, err := runtime.parseConfig(json.RawMessage(`{"schema_version":"cloud_snapshot_upload.config.v0.6.3","backup_root":"/tmp/arbitrary","data_dir":"/tmp/arbitrary","skip_if_uploaded":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != cloudSnapshotUploadConfigSchema || cfg.BackupRoot != "" || cfg.DataDir != "" || cfg.SkipIfUploaded {
		t.Fatalf("normalized historical config = %#v", cfg)
	}
}

func runtimeDirectArchiveCoverage(t *testing.T, root string) backupcoverage.Options {
	t.Helper()
	boxRoot := filepath.Join(root, "box")
	paths := map[string]string{"object": filepath.Join(root, "object"), "imports": filepath.Join(root, "imports"), "backups": filepath.Join(root, "backups"), "documents": filepath.Join(boxRoot, "Documents"), "notes": filepath.Join(boxRoot, "Notes"), "projects": filepath.Join(boxRoot, "Projects"), "topics": filepath.Join(boxRoot, "Topics"), "library": filepath.Join(boxRoot, "Library"), "retention": filepath.Join(root, "retention"), "archive": filepath.Join(root, "archive"), "agents": runtimeDirectArchiveAgentsRoot(root)}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ".loom-required-root"), []byte("required\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return backupcoverage.Options{ObjectStoreRoot: paths["object"], ImportsRoot: paths["imports"], UserBackupsRoot: paths["backups"], MainDocumentsRoot: paths["documents"], MainBoxPath: boxRoot, BoxNotesRoot: paths["notes"], StorageRetentionRoot: paths["retention"], StorageArchiveRoot: paths["archive"]}
}

func runtimeDirectArchiveAgentsRoot(root string) string {
	return filepath.Join(root, "agents")
}

func runtimeOperationalPackageReference(root, operationalMarker, provenanceMarker string, createdAt time.Time) OperationalPackageReference {
	return OperationalPackageReference{
		PackageDir: filepath.Join(root, "operational-package"), PackageID: "operational-package",
		ManifestSHA256: strings.Repeat(operationalMarker, 64), SchemaHead: 5, CreatedAt: createdAt,
		ProvenancePackageDir: filepath.Join(root, "provenance-package"), ProvenancePackageID: "provenance-package",
		ProvenanceManifestSHA256: strings.Repeat(provenanceMarker, 64), ProvenanceSchemaHead: provenance.SchemaHead,
		ProvenanceGraphDigest: "sha256:" + strings.Repeat("f", 64), ProvenanceCompletedAt: createdAt,
		ProvenanceDumpSizeBytes: 1,
	}
}

func runtimeMaintenanceBackupOperation(operationID string) maintenance.BackupOperation {
	return maintenance.BackupOperation{Operation: maintenance.Operation{
		MaintenanceOperationID: operationID,
		OperationKind:          maintenance.OperationKindMainBackup,
		Status:                 maintenance.OperationSucceeded,
		ResultJSON: mustWorkerJSON(map[string]any{
			"schema_version": mainBackupResultSchema, "phase": mainBackupPhase,
			"status": maintenance.OperationSucceeded, "committed": true,
			"backup_operation_id": operationID, "package_dir": "/operational/" + operationID,
			"package_id": "operational", "manifest_sha256": strings.Repeat("a", 64), "schema_head": 62,
			"provenance_package_dir": "/provenance/" + operationID, "provenance_package_id": "provenance",
			"provenance_manifest_sha256": strings.Repeat("b", 64),
		}),
	}}
}

func cloudSnapshotRunMetadataWithSelector(operationID string) json.RawMessage {
	return mustWorkerJSON(map[string]any{
		"schema_version":  "worker_run.metadata.v0.2",
		"run_once_reason": "test exact backup selection",
		"request_metadata": map[string]any{
			"source": "loom_cloud_cli", "production_requested": true, "backup_operation_id": operationID,
		},
	})
}

type cloudMutationCountingConnector struct{ count *int }

func (connector cloudMutationCountingConnector) Connect(context.Context) (driver.Conn, error) {
	return cloudMutationCountingConn{count: connector.count}, nil
}

func (cloudMutationCountingConnector) Driver() driver.Driver { return cloudMutationCountingDriver{} }

type cloudMutationCountingDriver struct{}

func (cloudMutationCountingDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("unexpected direct driver open")
}

type cloudMutationCountingConn struct{ count *int }

func (conn cloudMutationCountingConn) Prepare(string) (driver.Stmt, error) {
	(*conn.count)++
	return nil, fmt.Errorf("unexpected maintenance mutation")
}

func (cloudMutationCountingConn) Close() error { return nil }

func (conn cloudMutationCountingConn) Begin() (driver.Tx, error) {
	(*conn.count)++
	return nil, fmt.Errorf("unexpected maintenance mutation")
}

func (conn cloudMutationCountingConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	(*conn.count)++
	return nil, fmt.Errorf("unexpected maintenance mutation")
}

func (conn cloudMutationCountingConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	(*conn.count)++
	return nil, fmt.Errorf("unexpected maintenance mutation")
}

func writeDirectArchiveCloudConfig(t *testing.T, root string) string {
	t.Helper()
	cfg := cloudstorage.DefaultConfig()
	cfg.Enabled = true
	cfg.StateDir = filepath.Join(root, "cloud-state")
	cfg.Snapshots.Backend = cloudstorage.SnapshotBackendBorg
	cfg.Snapshots.Borg.Repository = "ssh://backup.invalid/./repo"
	path := filepath.Join(root, "cloud.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runtimeDirectArchiveRunContext(t *testing.T, configPath, overrides string) workers.RunContext {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(overrides), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["cloud_config_path"] = configPath
	cfg["node_id"] = "loom-main"
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return workers.RunContext{Instance: workers.WorkerInstance{WorkerInstanceID: "worker_instance_cloud", WorkerKey: "main.cloud_snapshot_upload", WorkerKind: workers.KindCloudSnapshotUpload, OwnerNodeID: "loom-main", ConfigJSON: raw, TickPolicyJSON: json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`)}, Run: workers.WorkerRun{WorkerRunID: "worker_run_cloud"}}
}
