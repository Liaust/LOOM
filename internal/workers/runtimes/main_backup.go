package runtimes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/hermesprofile"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/update"
	"loom.local/loom/internal/workers"
)

const (
	mainBackupConfigSchema       = "main_backup.config.v1"
	mainBackupCheckpointSchema   = "main_backup.checkpoint.v1"
	mainBackupResultSchema       = "main_backup.result.v1"
	mainBackupPhase              = "recovery_packages"
	mainBackupProducerMode       = "recovery_packages_v1"
	mainBackupLegacyProducerMode = "operational_package_v1"
)

type MainBackupRuntime struct {
	hermesPolicy            *hermesprofile.Policy
	DB                      *sql.DB
	Maintenance             maintenance.Service
	Health                  health.Service
	DBURL                   string
	Provenance              *provenance.Runtime
	ProvenanceDBURL         string
	DataDir                 string
	ObjectStoreRoot         string
	Version                 string
	Now                     func() time.Time
	Hostname                func() (string, error)
	ExecCommand             func(ctx context.Context, name string, args ...string) *exec.Cmd
	CreatePackage           func(context.Context, backupstrategy.OperationalPackageCreateInput) (backupstrategy.OperationalPackageCreateResult, error)
	CreateProvenancePackage func(context.Context, provenance.RecoveryPackageInput) (provenance.RecoveryPackageResult, error)
	recordArtifact          func(context.Context, maintenance.CreateArtifactInput) error
}

const boxNotesSnapshotArtifactKind = "box_notes_snapshot"
const maxUserBackupCustodyManifestBytes = int64(64 << 20)

type userBackupsSnapshotLayout string

const (
	userBackupsSnapshotCanonical userBackupsSnapshotLayout = "canonical_v0.7"
	userBackupsSnapshotLegacy    userBackupsSnapshotLayout = "legacy_private_backups"
	userBackupsSnapshotMixed     userBackupsSnapshotLayout = "canonical_v0.7_and_legacy_private_backups"
)

type userBackupsCanonicalBatch struct {
	RelativePath   string
	ManifestSHA256 string
	ManifestSize   int64
	ManifestMode   os.FileMode
	ManifestMTime  int64
}

type userBackupsLegacyPayload struct {
	RelativePath string
	SHA256       string
	Size         int64
	Mode         os.FileMode
	MTime        int64
}

type userBackupsSnapshotPlan struct {
	SourceExists      bool
	Layout            userBackupsSnapshotLayout
	CanonicalNodes    []string
	CanonicalRoots    []string
	CanonicalBatches  []userBackupsCanonicalBatch
	InProgressBatches []string
	LegacyNodes       []string
	LegacyPayloads    []userBackupsLegacyPayload
}

func (p userBackupsSnapshotPlan) includesCanonical() bool {
	return p.Layout == userBackupsSnapshotCanonical || p.Layout == userBackupsSnapshotMixed
}

func (p userBackupsSnapshotPlan) includesLegacy() bool {
	return p.Layout == userBackupsSnapshotLegacy || p.Layout == userBackupsSnapshotMixed
}

type mainBackupConfig struct {
	SchemaVersion           string              `json:"schema_version"`
	ProducerMode            string              `json:"producer_mode"`
	OperationalPackagesRoot string              `json:"operational_packages_root"`
	ProvenancePackagesRoot  string              `json:"provenance_packages_root"`
	ServiceConfigPath       string              `json:"service_config_path"`
	InstallConfigPath       string              `json:"install_config_path"`
	ReleaseConfigPath       string              `json:"release_config_path"`
	UpdateStateDir          string              `json:"update_state_dir"`
	Driver                  string              `json:"driver"`
	BackupRoot              string              `json:"backup_root"`
	DatabaseName            string              `json:"database_name"`
	IncludePostgres         bool                `json:"include_postgres"`
	IncludeObjectStore      bool                `json:"include_object_store"`
	IncludeImports          bool                `json:"include_imports"`
	ImportsRoot             string              `json:"imports_root"`
	ImportsPolicy           string              `json:"imports_policy"`
	ImportsSnapshotMethod   string              `json:"imports_snapshot_method"`
	IncludePrivateBackups   bool                `json:"include_private_backups"`
	UserBackupsRoot         string              `json:"user_backups_root"`
	IncludeMainDocuments    bool                `json:"include_main_documents"`
	MainDocumentsRoot       string              `json:"main_documents_root"`
	IncludeBoxNotes         bool                `json:"include_box_notes"`
	BoxNotesRoot            string              `json:"box_notes_root"`
	IncludeStorageRetention bool                `json:"include_storage_retention"`
	StorageRetentionRoot    string              `json:"storage_retention_root"`
	IncludeStorageArchive   bool                `json:"include_storage_archive"`
	StorageArchiveRoot      string              `json:"storage_archive_root"`
	IncludeNotesProjection  bool                `json:"include_notes_projection"`
	NotesProjectionRoot     string              `json:"notes_projection_root"`
	MainBoxPolicy           string              `json:"main_box_policy"`
	VerifyAfterWrite        bool                `json:"verify_after_write"`
	PgDumpPath              string              `json:"pg_dump_path"`
	MaxRuntimeMS            int                 `json:"max_runtime_ms"`
	Retention               mainBackupRetention `json:"retention"`
}

type mainBackupRetention struct {
	Mode        string `json:"mode"`
	KeepDaily   int    `json:"keep_daily"`
	KeepWeekly  int    `json:"keep_weekly"`
	KeepMonthly int    `json:"keep_monthly"`
}

type copySummary struct {
	SourceExists          bool  `json:"source_exists"`
	FileCount             int64 `json:"file_count"`
	TotalBytes            int64 `json:"total_bytes"`
	SharedObjectLinkCount int64 `json:"shared_object_link_count,omitempty"`
}

func NewMainBackupRuntime(db *sql.DB, maintenanceService maintenance.Service, healthService health.Service, dbURL, dataDir, objectStoreRoot, version string) MainBackupRuntime {
	return MainBackupRuntime{
		DB:              db,
		Maintenance:     maintenanceService,
		Health:          healthService,
		DBURL:           dbURL,
		DataDir:         dataDir,
		ObjectStoreRoot: objectStoreRoot,
		Version:         version,
	}
}

func (r MainBackupRuntime) WithProvenance(provenanceRuntime *provenance.Runtime, databaseURL string) MainBackupRuntime {
	r.Provenance = provenanceRuntime
	r.ProvenanceDBURL = strings.TrimSpace(databaseURL)
	return r
}

func (r MainBackupRuntime) Kind() string {
	return workers.KindMainBackup
}

func (r MainBackupRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindMainBackup,
		DisplayName:                  "Main backup",
		Description:                  "Creates and verifies bounded operational and isolated provenance recovery packages.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.maintenance",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":1800}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_heavy"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"main_backup.config_schema.v1","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"main_backup.checkpoint_schema.v1","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"main_backup.result_schema.v1","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r MainBackupRuntime) DefaultConfig() json.RawMessage {
	raw, err := json.Marshal(r.defaultMainBackupConfig())
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (r MainBackupRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.main_backup",
			WorkerKind:         workers.KindMainBackup,
			DisplayName:        "Main backup",
			Description:        "Creates and verifies bounded operational and isolated provenance recovery packages.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":1800}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_heavy"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r MainBackupRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := r.parseConfig(config)
	return err
}

func (r MainBackupRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.DB == nil {
		return workers.RunResult{}, fmt.Errorf("main backup database is not configured")
	}
	if r.Maintenance.DB == nil {
		return workers.RunResult{}, fmt.Errorf("main backup maintenance service is not configured")
	}
	config, err := r.parseConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}

	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	hermesEvidence, err := r.checkHermesRecovery(ctx, now)
	if err != nil {
		return workers.RunResult{}, err
	}
	if err := r.reconcileAbandonedBackupOperations(ctx, run, config); err != nil {
		return workers.RunResult{}, err
	}
	operation, err := r.Maintenance.CreateOperation(ctx, maintenance.CreateOperationInput{
		OperationKey:     "main_backup:" + run.Run.WorkerRunID,
		WorkerInstanceID: run.Instance.WorkerInstanceID,
		WorkerRunID:      run.Run.WorkerRunID,
		OperationKind:    maintenance.OperationKindMainBackup,
		Status:           maintenance.OperationRunning,
		SubjectKind:      "node",
		SubjectID:        run.Instance.OwnerNodeID,
		ConfigJSON:       run.Instance.ConfigJSON,
		Metadata:         json.RawMessage(`{"schema_version":"maintenance_operation.metadata.v1","source":"main_backup","phase":"operational_package"}`),
	})
	if err != nil {
		return workers.RunResult{}, err
	}

	packageID := operationalPackageID(run.Run.WorkerRunID)
	packageDir := filepath.Join(config.OperationalPackagesRoot, packageID)
	provenancePackageID := provenanceRecoveryPackageID(run.Run.WorkerRunID)
	provenancePackageDir := filepath.Join(config.ProvenancePackagesRoot, provenancePackageID)
	runCtx := ctx
	var cancel context.CancelFunc
	if config.MaxRuntimeMS > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(config.MaxRuntimeMS)*time.Millisecond)
		defer cancel()
	}
	packageResult, idempotent, runErr := r.createOperationalPackage(runCtx, config, packageID, now)
	completionCtx, cancelCompletion := detachedMainBackupCompletionContext(ctx)
	defer cancelCompletion()
	if runErr != nil {
		errorJSON := errorObject("main_backup.operational_package_failed", runErr, packageDir)
		_, _ = r.Maintenance.CompleteOperation(completionCtx, maintenance.CompleteOperationInput{
			OperationRef: operation.MaintenanceOperationID,
			Status:       maintenance.OperationFailed,
			ResultJSON: mustWorkerJSON(map[string]any{
				"schema_version":      mainBackupResultSchema,
				"status":              maintenance.OperationFailed,
				"phase":               mainBackupPhase,
				"phase_status":        maintenance.OperationFailed,
				"committed":           false,
				"backup_operation_id": operation.MaintenanceOperationID,
				"package_id":          packageID,
				"package_dir":         packageDir,
				"backup_dir":          packageDir,
				"worker_run_id":       run.Run.WorkerRunID,
				"complete_local_user_data_generation_created": false,
			}),
			ErrorJSON: errorJSON,
		})
		_ = r.recordBackupFailure(completionCtx, operation, run.Run.WorkerRunID, packageDir, runErr)
		return workers.RunResult{}, runErr
	}
	provenanceResult, runErr := r.createProvenanceRecoveryPackage(runCtx, config, provenancePackageID, operation.MaintenanceOperationID, run.Run.WorkerRunID, run.Instance.OwnerNodeID, now)
	if runErr != nil {
		errorJSON := errorObject("main_backup.provenance_package_failed", runErr, provenancePackageDir)
		_, _ = r.Maintenance.CompleteOperation(completionCtx, maintenance.CompleteOperationInput{
			OperationRef: operation.MaintenanceOperationID,
			Status:       maintenance.OperationFailed,
			ResultJSON: mustWorkerJSON(map[string]any{
				"schema_version": mainBackupResultSchema, "status": maintenance.OperationFailed, "phase": mainBackupPhase,
				"phase_status": maintenance.OperationFailed, "committed": false, "backup_operation_id": operation.MaintenanceOperationID,
				"package_id": packageResult.Verification.PackageID, "package_dir": packageResult.Verification.PackageDir,
				"manifest_sha256":       packageResult.Verification.ManifestSHA256,
				"provenance_package_id": provenancePackageID, "provenance_package_dir": provenancePackageDir,
				"provenance_manifest_sha256": "", "worker_run_id": run.Run.WorkerRunID,
				"complete_local_user_data_generation_created": false,
			}),
			ErrorJSON: errorJSON,
		})
		_ = r.recordBackupFailure(completionCtx, operation, run.Run.WorkerRunID, provenancePackageDir, runErr)
		return workers.RunResult{}, runErr
	}
	verification := packageResult.Verification
	resultData := map[string]any{
		"schema_version":                      mainBackupResultSchema,
		"status":                              maintenance.OperationSucceeded,
		"hermes_recovery":                     hermesEvidence,
		"phase":                               mainBackupPhase,
		"phase_status":                        maintenance.OperationSucceeded,
		"committed":                           true,
		"idempotent":                          idempotent,
		"backup_operation_id":                 operation.MaintenanceOperationID,
		"worker_run_id":                       run.Run.WorkerRunID,
		"package_id":                          verification.PackageID,
		"package_dir":                         verification.PackageDir,
		"backup_dir":                          verification.PackageDir,
		"manifest_path":                       verification.ManifestPath,
		"manifest_sha256":                     verification.ManifestSHA256,
		"operational_package_id":              verification.PackageID,
		"operational_package_dir":             verification.PackageDir,
		"operational_package_manifest_sha256": verification.ManifestSHA256,
		"provenance_package_id":               provenanceResult.PackageID,
		"provenance_package_dir":              provenanceResult.PackageDir,
		"provenance_manifest_path":            provenanceResult.ManifestPath,
		"provenance_manifest_sha256":          provenanceResult.ManifestSHA256,
		"provenance_schema_head":              provenanceResult.Snapshot.SchemaHead,
		"provenance_graph_digest":             provenanceResult.Snapshot.GraphDigest,
		"provenance_dump_size_bytes":          provenanceResult.DumpSizeBytes,
		"schema_head":                         verification.SchemaHead,
		"artifact_count":                      verification.ArtifactCount,
		"total_bytes":                         verification.TotalBytes,
		"verification_status":                 verification.Status,
		"retention_plan":                      packageResult.RetentionPlan,
		"retention_applied":                   false,
		"complete_local_user_data_generation_created": false,
		"maintenance_evidence_status":                 maintenance.OperationSucceeded,
	}
	result := mustWorkerJSON(resultData)
	manifestInfo, err := os.Stat(verification.ManifestPath)
	if err != nil {
		evidenceErr := fmt.Errorf("stat verified operational manifest: %w", err)
		resultData["status"] = maintenance.OperationFailed
		resultData["phase_status"] = maintenance.OperationSucceeded
		resultData["maintenance_evidence_status"] = maintenance.OperationFailed
		_, _ = r.Maintenance.CompleteOperation(completionCtx, maintenance.CompleteOperationInput{OperationRef: operation.MaintenanceOperationID, Status: maintenance.OperationFailed, ResultJSON: mustWorkerJSON(resultData), ErrorJSON: errorObject("main_backup.maintenance_evidence_failed", evidenceErr, verification.PackageDir)})
		_ = r.recordBackupFailure(completionCtx, operation, run.Run.WorkerRunID, verification.PackageDir, evidenceErr)
		return workers.RunResult{}, evidenceErr
	}
	manifestSize := manifestInfo.Size()
	if err := r.recordMaintenanceArtifact(completionCtx, maintenance.CreateArtifactInput{
		MaintenanceOperationID: operation.MaintenanceOperationID,
		ArtifactKind:           maintenance.ArtifactKindBackupManifest,
		URI:                    fileURI(verification.ManifestPath),
		SizeBytes:              &manifestSize,
		SHA256:                 verification.ManifestSHA256,
		Metadata: mustWorkerJSON(map[string]any{
			"schema_version": "main_backup.operational_package_artifact.v1",
			"phase":          mainBackupPhase,
			"package_id":     verification.PackageID,
			"package_dir":    verification.PackageDir,
			"schema_head":    verification.SchemaHead,
		}),
	}); err != nil {
		resultData["status"] = maintenance.OperationFailed
		resultData["phase_status"] = maintenance.OperationSucceeded
		resultData["maintenance_evidence_status"] = maintenance.OperationFailed
		failureResult := mustWorkerJSON(resultData)
		_, _ = r.Maintenance.CompleteOperation(completionCtx, maintenance.CompleteOperationInput{
			OperationRef: operation.MaintenanceOperationID,
			Status:       maintenance.OperationFailed,
			ResultJSON:   failureResult,
			ErrorJSON:    errorObject("main_backup.maintenance_evidence_failed", err, verification.PackageDir),
		})
		_ = r.recordBackupFailure(completionCtx, operation, run.Run.WorkerRunID, verification.PackageDir, err)
		return workers.RunResult{}, err
	}
	provenanceManifestInfo, err := os.Stat(provenanceResult.ManifestPath)
	if err != nil {
		evidenceErr := fmt.Errorf("stat verified provenance manifest: %w", err)
		resultData["status"] = maintenance.OperationFailed
		resultData["phase_status"] = maintenance.OperationSucceeded
		resultData["maintenance_evidence_status"] = maintenance.OperationFailed
		_, _ = r.Maintenance.CompleteOperation(completionCtx, maintenance.CompleteOperationInput{
			OperationRef: operation.MaintenanceOperationID, Status: maintenance.OperationFailed,
			ResultJSON: mustWorkerJSON(resultData), ErrorJSON: errorObject("main_backup.provenance_evidence_failed", evidenceErr, provenanceResult.PackageDir),
		})
		_ = r.recordBackupFailure(completionCtx, operation, run.Run.WorkerRunID, provenanceResult.PackageDir, evidenceErr)
		return workers.RunResult{}, evidenceErr
	}
	provenanceManifestSize := provenanceManifestInfo.Size()
	if err := r.recordMaintenanceArtifact(completionCtx, maintenance.CreateArtifactInput{
		MaintenanceOperationID: operation.MaintenanceOperationID,
		ArtifactKind:           maintenance.ArtifactKindBackupManifest,
		URI:                    fileURI(provenanceResult.ManifestPath),
		SizeBytes:              &provenanceManifestSize,
		SHA256:                 provenanceResult.ManifestSHA256,
		Metadata: mustWorkerJSON(map[string]any{
			"schema_version": "main_backup.provenance_package_artifact.v1", "phase": mainBackupPhase,
			"package_id": provenanceResult.PackageID, "package_dir": provenanceResult.PackageDir,
			"schema_head": provenanceResult.Snapshot.SchemaHead, "graph_digest": provenanceResult.Snapshot.GraphDigest,
		}),
	}); err != nil {
		resultData["status"] = maintenance.OperationFailed
		resultData["phase_status"] = maintenance.OperationSucceeded
		resultData["maintenance_evidence_status"] = maintenance.OperationFailed
		_, _ = r.Maintenance.CompleteOperation(completionCtx, maintenance.CompleteOperationInput{
			OperationRef: operation.MaintenanceOperationID, Status: maintenance.OperationFailed,
			ResultJSON: mustWorkerJSON(resultData), ErrorJSON: errorObject("main_backup.provenance_evidence_failed", err, provenanceResult.PackageDir),
		})
		_ = r.recordBackupFailure(completionCtx, operation, run.Run.WorkerRunID, provenanceResult.PackageDir, err)
		return workers.RunResult{}, err
	}

	if err := r.revalidateHermesRecovery(runCtx, hermesEvidence); err != nil {
		resultData["status"] = maintenance.OperationFailed
		resultData["phase_status"] = maintenance.OperationFailed
		resultData["committed"] = false
		_, _ = r.Maintenance.CompleteOperation(completionCtx, maintenance.CompleteOperationInput{
			OperationRef: operation.MaintenanceOperationID, Status: maintenance.OperationFailed,
			ResultJSON: mustWorkerJSON(resultData), ErrorJSON: errorObject("main_backup.hermes_recovery_changed", err, ""),
		})
		_ = r.recordBackupFailure(completionCtx, operation, run.Run.WorkerRunID, "", err)
		return workers.RunResult{}, err
	}
	completed, err := r.Maintenance.CompleteOperation(completionCtx, maintenance.CompleteOperationInput{
		OperationRef: operation.MaintenanceOperationID,
		Status:       maintenance.OperationSucceeded,
		ResultJSON:   result,
		ErrorJSON:    json.RawMessage(`{}`),
	})
	if err != nil {
		return workers.RunResult{}, err
	}
	_ = r.resolveBackupFailure(completionCtx, completed)

	checkpoint := mustWorkerJSON(map[string]any{
		"schema_version":                      mainBackupCheckpointSchema,
		"phase":                               mainBackupPhase,
		"phase_status":                        maintenance.OperationSucceeded,
		"committed":                           true,
		"idempotent":                          idempotent,
		"last_package_at":                     now.Format(time.RFC3339Nano),
		"hermes_recovery":                     hermesEvidence,
		"last_run_id":                         run.Run.WorkerRunID,
		"backup_operation_id":                 operation.MaintenanceOperationID,
		"package_id":                          verification.PackageID,
		"package_dir":                         verification.PackageDir,
		"manifest_sha256":                     verification.ManifestSHA256,
		"operational_package_manifest_sha256": verification.ManifestSHA256,
		"provenance_package_id":               provenanceResult.PackageID,
		"provenance_package_dir":              provenanceResult.PackageDir,
		"provenance_manifest_sha256":          provenanceResult.ManifestSHA256,
		"provenance_schema_head":              provenanceResult.Snapshot.SchemaHead,
		"provenance_graph_digest":             provenanceResult.Snapshot.GraphDigest,
		"schema_head":                         verification.SchemaHead,
		"artifact_count":                      verification.ArtifactCount,
		"total_bytes":                         verification.TotalBytes,
		"verification_status":                 maintenance.VerificationSucceeded,
		"complete_local_user_data_generation_created": false,
	})
	tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: result,
		Counters: map[string]int64{
			"operational_packages": 1,
			"provenance_packages":  1,
			"artifacts":            int64(verification.ArtifactCount),
			"total_bytes":          verification.TotalBytes,
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: mainBackupCheckpointSchema,
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: tickPolicy.NextAfter(now),
		Retryable:    false,
	}, nil
}

func operationalPackageID(workerRunID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(workerRunID)))
	return "operational-" + hex.EncodeToString(digest[:12])
}

func provenanceRecoveryPackageID(workerRunID string) string {
	digest := sha256.Sum256([]byte("provenance\x00" + strings.TrimSpace(workerRunID)))
	return "provenance-" + hex.EncodeToString(digest[:12])
}

func (r MainBackupRuntime) createProvenanceRecoveryPackage(ctx context.Context, config mainBackupConfig, packageID, operationID, workerRunID, nodeID string, now time.Time) (provenance.RecoveryPackageResult, error) {
	if err := os.MkdirAll(config.ProvenancePackagesRoot, 0o700); err != nil {
		return provenance.RecoveryPackageResult{}, fmt.Errorf("create provenance package root: %w", err)
	}
	creator := r.CreateProvenancePackage
	if creator == nil {
		if r.Provenance == nil || strings.TrimSpace(r.ProvenanceDBURL) == "" {
			return provenance.RecoveryPackageResult{}, fmt.Errorf("isolated provenance recovery producer is not configured")
		}
		creator = r.Provenance.CreateRecoveryPackage
	}
	result, err := creator(ctx, provenance.RecoveryPackageInput{
		PackagesRoot: config.ProvenancePackagesRoot, PackageID: packageID,
		BackupOperationID: operationID, WorkerRunID: workerRunID, NodeID: nodeID, CreatedAt: now,
		Dump: func(ctx context.Context, destination, _, exportedSnapshot string) error {
			return r.runProvenancePgDump(ctx, config, destination, exportedSnapshot)
		},
		Verify: func(ctx context.Context, packageDir, manifestSHA256 string) error {
			verification, err := maintenance.VerifyProvenanceBackupPackage(ctx, packageDir, manifestSHA256)
			if err != nil {
				return err
			}
			if verification.Status != maintenance.VerificationSucceeded || verification.ManifestSHA256 != manifestSHA256 {
				return fmt.Errorf("provenance recovery package failed exact verification")
			}
			return nil
		},
	})
	if err != nil {
		return provenance.RecoveryPackageResult{}, err
	}
	expectedPackageDir := filepath.Join(config.ProvenancePackagesRoot, packageID)
	if result.PackageID != packageID || result.PackageDir != expectedPackageDir || result.ManifestPath != filepath.Join(expectedPackageDir, provenance.RecoveryManifestFile) || result.ManifestSHA256 == "" || result.Snapshot.SchemaHead != provenance.SchemaHead || result.BackupOperationID != operationID || result.WorkerRunID != workerRunID {
		return provenance.RecoveryPackageResult{}, fmt.Errorf("provenance recovery producer returned mismatched evidence")
	}
	verification, err := maintenance.VerifyProvenanceBackupPackage(ctx, result.PackageDir, result.ManifestSHA256)
	if err != nil {
		return provenance.RecoveryPackageResult{}, err
	}
	verifiedSnapshot := provenance.RecoverySnapshot{SchemaHead: verification.SchemaHead, RelationCounts: verification.LogicalCounts, GraphDigest: verification.GraphDigest}
	if verification.ManifestSHA256 != result.ManifestSHA256 || result.DumpSizeBytes != verification.DumpSizeBytes || provenance.CompareRecoverySnapshots(result.Snapshot, verifiedSnapshot) != nil {
		return provenance.RecoveryPackageResult{}, fmt.Errorf("provenance recovery package verification disagrees with producer evidence")
	}
	return result, nil
}

func (r MainBackupRuntime) createOperationalPackage(ctx context.Context, config mainBackupConfig, packageID string, now time.Time) (backupstrategy.OperationalPackageCreateResult, bool, error) {
	packageDir := filepath.Join(config.OperationalPackagesRoot, packageID)
	healthReport := r.Health.Check(ctx)
	schemaHead := healthReport.Checks.Migrations.CurrentVersion
	if _, err := os.Lstat(packageDir); err == nil {
		verification, verifyErr := backupstrategy.VerifyOperationalPackage(ctx, backupstrategy.OperationalPackageVerificationInput{
			PackageDir:         packageDir,
			ExpectedPackageID:  packageID,
			ExpectedSchemaHead: &schemaHead,
		})
		if verifyErr != nil {
			return backupstrategy.OperationalPackageCreateResult{}, false, verifyErr
		}
		if verification.Status != maintenance.VerificationSucceeded {
			return backupstrategy.OperationalPackageCreateResult{}, false, fmt.Errorf("%w: existing operational package %s failed exact verification", workers.ErrConflict, packageID)
		}
		retention, retentionErr := backupstrategy.PlanOperationalPackageRetention(ctx, config.OperationalPackagesRoot, nil)
		if retentionErr != nil {
			return backupstrategy.OperationalPackageCreateResult{}, false, retentionErr
		}
		return backupstrategy.OperationalPackageCreateResult{
			PackageDir:     verification.PackageDir,
			ManifestSHA256: verification.ManifestSHA256,
			Verification:   verification,
			RetentionPlan:  retention,
		}, true, nil
	} else if !os.IsNotExist(err) {
		return backupstrategy.OperationalPackageCreateResult{}, false, err
	}
	if err := os.MkdirAll(config.OperationalPackagesRoot, 0o700); err != nil {
		return backupstrategy.OperationalPackageCreateResult{}, false, fmt.Errorf("create operational package root: %w", err)
	}
	sourceRoot, err := os.MkdirTemp(config.OperationalPackagesRoot, ".producer-state-")
	if err != nil {
		return backupstrategy.OperationalPackageCreateResult{}, false, fmt.Errorf("create bounded operational state staging: %w", err)
	}
	defer os.RemoveAll(sourceRoot)
	if err := os.Chmod(sourceRoot, 0o700); err != nil {
		return backupstrategy.OperationalPackageCreateResult{}, false, err
	}
	migrationPath := filepath.Join(sourceRoot, "migration-state.json")
	updatePath := filepath.Join(sourceRoot, "update-state.json")
	healthPath := filepath.Join(sourceRoot, "health.json")
	if err := writeOperationalProducerJSON(migrationPath, backupstrategy.OperationalMigrationState{
		Schema: backupstrategy.OperationalMigrationStateSchema, CurrentVersion: schemaHead,
		LatestVersion: healthReport.Checks.Migrations.LatestVersion, Status: healthReport.Checks.Migrations.Status,
	}); err != nil {
		return backupstrategy.OperationalPackageCreateResult{}, false, err
	}
	if err := writeOperationalProducerJSON(updatePath, update.Status(update.StatusInput{StateDir: config.UpdateStateDir, Limit: 20, Now: func() time.Time { return now }})); err != nil {
		return backupstrategy.OperationalPackageCreateResult{}, false, err
	}
	if err := writeOperationalProducerJSON(healthPath, healthReport); err != nil {
		return backupstrategy.OperationalPackageCreateResult{}, false, err
	}
	creator := r.CreatePackage
	if creator == nil {
		creator = backupstrategy.CreateOperationalPackage
	}
	result, err := creator(ctx, backupstrategy.OperationalPackageCreateInput{
		PackagesRoot: config.OperationalPackagesRoot,
		PackageID:    packageID,
		CreatedAt:    now,
		SchemaHead:   schemaHead,
		PostgresDump: func(ctx context.Context, destination string) error {
			return r.runPgDump(ctx, config, destination)
		},
		ServiceConfig:  config.ServiceConfigPath,
		InstallConfig:  config.InstallConfigPath,
		ReleaseConfig:  config.ReleaseConfigPath,
		MigrationState: migrationPath,
		UpdateState:    updatePath,
		Health:         healthPath,
	})
	if err != nil {
		return result, false, err
	}
	if result.Verification.Status != maintenance.VerificationSucceeded || result.Verification.PackageID != packageID || result.Verification.SchemaHead != schemaHead {
		return result, false, fmt.Errorf("operational package producer returned unverified or mismatched evidence")
	}
	return result, false, nil
}

func writeOperationalProducerJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("operational producer state %s is empty", filepath.Base(path))
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

const mainBackupCompletionTimeout = 30 * time.Second

func detachedMainBackupCompletionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), mainBackupCompletionTimeout)
}

func (r MainBackupRuntime) reconcileAbandonedBackupOperations(ctx context.Context, run workers.RunContext, config mainBackupConfig) error {
	backups, err := r.Maintenance.ListBackups(ctx, maintenance.OperationFilter{
		Kind:   maintenance.OperationKindMainBackup,
		Status: maintenance.OperationRunning,
		Limit:  100,
	})
	if err != nil {
		return fmt.Errorf("list running main-backup operations: %w", err)
	}
	for _, backup := range backups {
		operation := backup.Operation
		if operation.WorkerInstanceID != run.Instance.WorkerInstanceID {
			continue
		}
		if operation.WorkerRunID == nil || strings.TrimSpace(*operation.WorkerRunID) == "" {
			return fmt.Errorf("%w: running main-backup operation %s has no worker-run identity", workers.ErrConflict, operation.MaintenanceOperationID)
		}
		workerRunID := *operation.WorkerRunID
		if workerRunID == run.Run.WorkerRunID {
			return fmt.Errorf("%w: current worker run %s already has running main-backup operation %s", workers.ErrConflict, workerRunID, operation.MaintenanceOperationID)
		}
		var runStatus string
		if err := r.DB.QueryRowContext(ctx, `SELECT run_status FROM workers.worker_runs WHERE worker_run_id = $1`, workerRunID).Scan(&runStatus); err != nil {
			return fmt.Errorf("inspect worker run %s for running backup %s: %w", workerRunID, operation.MaintenanceOperationID, err)
		}
		if !recoverableTerminalWorkerRunStatus(runStatus) {
			return fmt.Errorf("%w: prior main-backup operation %s belongs to non-terminal worker run %s (%s)", workers.ErrConflict, operation.MaintenanceOperationID, workerRunID, runStatus)
		}
		backupDir := filepath.Join(config.OperationalPackagesRoot, operationalPackageID(workerRunID))
		cause := fmt.Errorf("prior main-backup operation outlived terminal worker run %s (%s)", workerRunID, runStatus)
		completed, err := r.Maintenance.CompleteOperation(ctx, maintenance.CompleteOperationInput{
			OperationRef: operation.MaintenanceOperationID,
			Status:       maintenance.OperationFailed,
			ResultJSON: mustWorkerJSON(map[string]any{
				"schema_version":      mainBackupResultSchema,
				"status":              maintenance.OperationFailed,
				"phase":               mainBackupPhase,
				"phase_status":        maintenance.OperationFailed,
				"committed":           false,
				"backup_operation_id": operation.MaintenanceOperationID,
				"backup_dir":          backupDir,
				"worker_run_id":       workerRunID,
				"recovery":            "terminal_worker_run_reconciliation",
				"complete_local_user_data_generation_created": false,
			}),
			ErrorJSON: errorObject("main_backup.abandoned_operation", cause, backupDir),
		})
		if err != nil {
			return fmt.Errorf("fail abandoned main-backup operation %s: %w", operation.MaintenanceOperationID, err)
		}
		if err := r.recordBackupFailure(ctx, completed, workerRunID, backupDir, cause); err != nil {
			return fmt.Errorf("record abandoned main-backup finding %s: %w", operation.MaintenanceOperationID, err)
		}
	}
	return nil
}

func recoverableTerminalWorkerRunStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case workers.RunStatusSucceeded, workers.RunStatusFailed, workers.RunStatusCancelled, workers.RunStatusTimedOut:
		return true
	default:
		return false
	}
}

func (r MainBackupRuntime) parseConfig(raw json.RawMessage) (mainBackupConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return mainBackupConfig{}, err
	}
	config := r.defaultMainBackupConfig()
	if len(normalized) > 0 && strings.TrimSpace(string(normalized)) != "{}" {
		if err := json.Unmarshal(normalized, &config); err != nil {
			return mainBackupConfig{}, fmt.Errorf("%w: main_backup config_json is invalid JSON: %w", workers.ErrInvalid, err)
		}
	}
	config.SchemaVersion = strings.TrimSpace(config.SchemaVersion)
	switch config.SchemaVersion {
	case "", "main_backup.config.v0.2", mainBackupConfigSchema:
		config.SchemaVersion = mainBackupConfigSchema
	default:
		return mainBackupConfig{}, fmt.Errorf("%w: unsupported main_backup schema_version %q", workers.ErrInvalid, config.SchemaVersion)
	}
	config.ProducerMode = strings.TrimSpace(config.ProducerMode)
	switch config.ProducerMode {
	case "", mainBackupLegacyProducerMode, mainBackupProducerMode:
		config.ProducerMode = mainBackupProducerMode
	default:
		return mainBackupConfig{}, fmt.Errorf("%w: main_backup producer_mode %q is not supported", workers.ErrInvalid, config.ProducerMode)
	}
	config.Driver = strings.TrimSpace(config.Driver)
	if config.Driver == "" {
		config.Driver = "internal_local"
	}
	if config.Driver != "internal_local" {
		return mainBackupConfig{}, fmt.Errorf("%w: main_backup driver %q is not supported", workers.ErrInvalid, config.Driver)
	}
	var pathErr error
	if config.OperationalPackagesRoot, pathErr = exactRuntimePath(config.OperationalPackagesRoot, "operational_packages_root"); pathErr != nil {
		return mainBackupConfig{}, pathErr
	}
	if config.ProvenancePackagesRoot, pathErr = exactRuntimePath(config.ProvenancePackagesRoot, "provenance_packages_root"); pathErr != nil {
		return mainBackupConfig{}, pathErr
	}
	if cloudDirectArchivePathsOverlap(config.OperationalPackagesRoot, config.ProvenancePackagesRoot) {
		return mainBackupConfig{}, fmt.Errorf("%w: operational and provenance package roots must not overlap", workers.ErrInvalid)
	}
	if config.ServiceConfigPath, pathErr = exactRuntimePath(config.ServiceConfigPath, "service_config_path"); pathErr != nil {
		return mainBackupConfig{}, pathErr
	}
	if config.InstallConfigPath, pathErr = exactRuntimePath(config.InstallConfigPath, "install_config_path"); pathErr != nil {
		return mainBackupConfig{}, pathErr
	}
	if config.ReleaseConfigPath, pathErr = exactRuntimePath(config.ReleaseConfigPath, "release_config_path"); pathErr != nil {
		return mainBackupConfig{}, pathErr
	}
	if config.UpdateStateDir, pathErr = exactRuntimePath(config.UpdateStateDir, "update_state_dir"); pathErr != nil {
		return mainBackupConfig{}, pathErr
	}
	if config.PgDumpPath = strings.TrimSpace(config.PgDumpPath); config.PgDumpPath == "" {
		config.PgDumpPath = "pg_dump"
	}
	if config.DatabaseName = strings.TrimSpace(config.DatabaseName); config.DatabaseName == "" {
		config.DatabaseName = "loom_main"
	}
	// The remaining v0.9 fields are retained only so seeded historical config
	// continues to decode and legacy package readers/tests remain available.
	// New runs never use these fields to produce a complete local generation.
	if config.MainDocumentsRoot = filepath.Clean(strings.TrimSpace(config.MainDocumentsRoot)); config.MainDocumentsRoot == "." || config.MainDocumentsRoot == "" {
		config.MainDocumentsRoot = r.defaultMainDocumentsRoot()
	}
	if config.ImportsRoot = filepath.Clean(strings.TrimSpace(config.ImportsRoot)); config.ImportsRoot == "." || config.ImportsRoot == "" {
		config.ImportsRoot = r.defaultImportsRoot()
	}
	if config.ImportsPolicy = strings.TrimSpace(config.ImportsPolicy); config.ImportsPolicy == "" {
		config.ImportsPolicy = r.defaultImportsPolicy()
	}
	if config.ImportsSnapshotMethod = strings.TrimSpace(config.ImportsSnapshotMethod); config.ImportsSnapshotMethod == "" {
		config.ImportsSnapshotMethod = maintenance.ImportsSnapshotSharedStore
	}
	if config.BoxNotesRoot = filepath.Clean(strings.TrimSpace(config.BoxNotesRoot)); config.BoxNotesRoot == "." || config.BoxNotesRoot == "" {
		config.BoxNotesRoot = r.defaultBoxNotesRoot()
	}
	if config.StorageRetentionRoot = filepath.Clean(strings.TrimSpace(config.StorageRetentionRoot)); config.StorageRetentionRoot == "." || config.StorageRetentionRoot == "" {
		config.StorageRetentionRoot = r.defaultStorageRetentionRoot()
	}
	if config.UserBackupsRoot = filepath.Clean(strings.TrimSpace(config.UserBackupsRoot)); config.UserBackupsRoot == "." || config.UserBackupsRoot == "" {
		config.UserBackupsRoot = r.defaultUserBackupsRoot()
	}
	if config.StorageArchiveRoot = filepath.Clean(strings.TrimSpace(config.StorageArchiveRoot)); config.StorageArchiveRoot == "." || config.StorageArchiveRoot == "" {
		config.StorageArchiveRoot = r.defaultStorageArchiveRoot()
	}
	if config.NotesProjectionRoot = filepath.Clean(strings.TrimSpace(config.NotesProjectionRoot)); config.NotesProjectionRoot == "." || config.NotesProjectionRoot == "" {
		config.NotesProjectionRoot = r.defaultNotesProjectionRoot()
	}
	if config.MainBoxPolicy = strings.TrimSpace(config.MainBoxPolicy); config.MainBoxPolicy == "" {
		config.MainBoxPolicy = "selected_canonical_roots_copied_once"
	}
	if config.MaxRuntimeMS <= 0 {
		config.MaxRuntimeMS = 1800000
	}
	return config, nil
}

func exactRuntimePath(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", fmt.Errorf("%w: main_backup %s must be an exact absolute path", workers.ErrInvalid, field)
	}
	return value, nil
}

func (r MainBackupRuntime) defaultMainBackupConfig() mainBackupConfig {
	return mainBackupConfig{
		SchemaVersion:           mainBackupConfigSchema,
		ProducerMode:            mainBackupProducerMode,
		OperationalPackagesRoot: filepath.Join(r.DataDir, "backups", "operational"),
		ProvenancePackagesRoot:  filepath.Join(r.DataDir, "backups", "provenance"),
		ServiceConfigPath:       r.defaultServiceConfigPath(),
		InstallConfigPath:       "/etc/loom/install.yaml",
		ReleaseConfigPath:       filepath.Join(r.defaultServiceRoot(), "current", update.DefaultReleaseManifestFileYAML),
		UpdateStateDir:          update.DefaultStateDir(r.DataDir),
		Driver:                  "internal_local",
		BackupRoot:              filepath.Join(r.DataDir, "backups", "main"),
		DatabaseName:            "loom_main",
		IncludePostgres:         true,
		IncludeObjectStore:      true,
		IncludeImports:          true,
		ImportsRoot:             r.defaultImportsRoot(),
		ImportsPolicy:           r.defaultImportsPolicy(),
		ImportsSnapshotMethod:   maintenance.ImportsSnapshotSharedStore,
		IncludePrivateBackups:   true,
		UserBackupsRoot:         r.defaultUserBackupsRoot(),
		IncludeMainDocuments:    true,
		MainDocumentsRoot:       r.defaultMainDocumentsRoot(),
		IncludeBoxNotes:         true,
		BoxNotesRoot:            r.defaultBoxNotesRoot(),
		IncludeStorageRetention: true,
		StorageRetentionRoot:    r.defaultStorageRetentionRoot(),
		IncludeStorageArchive:   true,
		StorageArchiveRoot:      r.defaultStorageArchiveRoot(),
		IncludeNotesProjection:  false,
		NotesProjectionRoot:     r.defaultNotesProjectionRoot(),
		MainBoxPolicy:           "selected_canonical_roots_copied_once",
		VerifyAfterWrite:        true,
		PgDumpPath:              "pg_dump",
		MaxRuntimeMS:            1800000,
		Retention: mainBackupRetention{
			Mode:        "disabled_for_slice_03",
			KeepDaily:   14,
			KeepWeekly:  8,
			KeepMonthly: 6,
		},
	}
}

func (r MainBackupRuntime) defaultMainDocumentsRoot() string {
	if root := strings.TrimSpace(r.Health.Config.MainDocumentsRoot()); root != "" {
		return filepath.Clean(root)
	}
	return filepath.Join("/srv/loom/box", "Documents")
}

func (r MainBackupRuntime) defaultServiceConfigPath() string {
	if value := strings.TrimSpace(r.Health.Config.ConfigFile); value != "" && filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return "/etc/loom/loom.env"
}

func (r MainBackupRuntime) defaultServiceRoot() string {
	if value := strings.TrimSpace(r.Health.Config.ServiceRoot); value != "" && filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return "/srv/loom"
}

func (r MainBackupRuntime) defaultImportsRoot() string {
	if root := strings.TrimSpace(r.Health.Config.ImportsRoot); root != "" {
		return filepath.Clean(root)
	}
	return filepath.Join(r.DataDir, "lane", "accepted")
}

func (r MainBackupRuntime) defaultImportsPolicy() string {
	return r.Health.Config.EffectiveImportsBackupPolicy()
}

func (r MainBackupRuntime) defaultBoxNotesRoot() string {
	if root := strings.TrimSpace(r.Health.Config.BoxNotesRoot()); root != "" {
		return filepath.Clean(root)
	}
	return filepath.Join("/srv/loom/box", "Notes")
}

func (r MainBackupRuntime) defaultStorageRetentionRoot() string {
	if root := strings.TrimSpace(r.Health.Config.StorageRetention); root != "" {
		return filepath.Clean(root)
	}
	return filepath.Join(r.DataDir, "storage-retention")
}

func (r MainBackupRuntime) defaultUserBackupsRoot() string {
	if root := strings.TrimSpace(r.Health.Config.UserBackupsRoot); root != "" {
		return filepath.Clean(root)
	}
	return filepath.Join(r.DataDir, "private-backups")
}

func (r MainBackupRuntime) defaultStorageArchiveRoot() string {
	if root := strings.TrimSpace(r.Health.Config.ArchiveRoot); root != "" {
		return filepath.Clean(root)
	}
	return filepath.Join(r.DataDir, "storage-archive")
}

func (r MainBackupRuntime) defaultNotesProjectionRoot() string {
	if root := strings.TrimSpace(r.Health.Config.NotesProjectionRoot()); root != "" {
		return filepath.Clean(root)
	}
	return filepath.Join(r.DataDir, "generated", "notes")
}

func (r MainBackupRuntime) validateBackupRoot(backupRoot string, sourceRoots ...string) error {
	backupRoot = filepath.Clean(backupRoot)
	for _, forbidden := range append([]string{r.ObjectStoreRoot}, sourceRoots...) {
		forbidden = filepath.Clean(strings.TrimSpace(forbidden))
		if forbidden == "." || forbidden == "" {
			continue
		}
		if pathWithinRuntime(forbidden, backupRoot) {
			return fmt.Errorf("%w: backup_root must not be inside %s", workers.ErrInvalid, forbidden)
		}
	}
	return nil
}

func (r MainBackupRuntime) createBackup(ctx context.Context, run workers.RunContext, operation maintenance.Operation, config mainBackupConfig, backupDir string, now time.Time) (json.RawMessage, error) {
	var err error
	userBackupsPlan := userBackupsSnapshotPlan{Layout: r.userBackupsSnapshotLayout()}
	if config.IncludePrivateBackups {
		userBackupsPlan, err = classifyUserBackupsSnapshot(config.UserBackupsRoot, userBackupsPlan.Layout)
		if err != nil {
			return nil, fmt.Errorf("classify user backup custody: %w", err)
		}
		if !userBackupsPlan.SourceExists {
			return nil, fmt.Errorf("classify user backup custody: required custody root %s does not exist", config.UserBackupsRoot)
		}
	}
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return nil, fmt.Errorf("create backup directory: %w", err)
	}
	manifestPath := filepath.Join(backupDir, "manifest.json")
	hostname := ""
	if r.Hostname != nil {
		if value, err := r.Hostname(); err == nil {
			hostname = value
		}
	} else if value, err := os.Hostname(); err == nil {
		hostname = value
	}

	initialManifest := r.backupManifest(run, operation, config, now, hostname, nil, userBackupsPlan.Layout)
	initialManifest.Verification = map[string]any{
		"status":     "running",
		"started_at": now.Format(time.RFC3339),
	}
	if err := writeJSONFile(manifestPath, initialManifest); err != nil {
		return nil, fmt.Errorf("write initial backup manifest: %w", err)
	}

	healthReport := r.Health.Check(ctx)
	healthRaw, err := json.MarshalIndent(healthReport, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal backup health report: %w", err)
	}
	healthPath := filepath.Join(backupDir, "health.json")
	if err := os.WriteFile(healthPath, healthRaw, 0o640); err != nil {
		return nil, fmt.Errorf("write backup health report: %w", err)
	}

	dumpPath := filepath.Join(backupDir, config.DatabaseName+".dump")
	if config.IncludePostgres {
		if err := r.runPgDump(ctx, config, dumpPath); err != nil {
			return nil, err
		}
	}

	objectStoreSummary := copySummary{}
	if config.IncludeObjectStore {
		objectStoreSummary, err = copyDirIfExists(r.ObjectStoreRoot, filepath.Join(backupDir, "object-store"))
		if err != nil {
			return nil, fmt.Errorf("copy object store: %w", err)
		}
	}
	importsSummary := copySummary{}
	importsEvidencePath := filepath.Join(backupDir, "imports-evidence.json")
	if config.IncludeImports {
		importsSummary, err = copyImportsCustody(ctx, config.ImportsRoot, filepath.Join(backupDir, "imports"), filepath.Join(config.BackupRoot, ".imports-objects"), config.ImportsPolicy, config.ImportsSnapshotMethod)
		if err != nil {
			return nil, fmt.Errorf("copy Imports custody: %w", err)
		}
	}
	userBackupsSummary := copySummary{}
	privateBackupsSummary := copySummary{}
	privateBackupsEvidenceRelativePath := ""
	privateBackupsEvidencePath := filepath.Join(backupDir, maintenance.PrivateBackupsEvidencePath)
	privateBackupsEvidenceHash := ""
	var privateBackupsEvidenceSize int64
	if config.IncludePrivateBackups {
		userBackupsSummary, privateBackupsSummary, err = copyUserBackupsSnapshot(
			config.UserBackupsRoot,
			filepath.Join(backupDir, userBackupsSnapshotRelativePath(userBackupsSnapshotCanonical)),
			filepath.Join(backupDir, userBackupsSnapshotRelativePath(userBackupsSnapshotLegacy)),
			userBackupsPlan,
		)
		if err != nil {
			return nil, fmt.Errorf("copy user backups: %w", err)
		}
		if userBackupsPlan.includesLegacy() {
			privateBackupsEvidenceRelativePath = maintenance.PrivateBackupsEvidencePath
			evidence, err := maintenance.WritePrivateBackupsEvidence(
				ctx,
				filepath.Join(backupDir, userBackupsSnapshotRelativePath(userBackupsSnapshotLegacy)),
				userBackupsSnapshotRelativePath(userBackupsSnapshotLegacy),
				privateBackupsEvidencePath,
			)
			if err != nil {
				return nil, fmt.Errorf("write private backups evidence: %w", err)
			}
			if evidence.FileCount != privateBackupsSummary.FileCount || evidence.TotalBytes != privateBackupsSummary.TotalBytes {
				return nil, fmt.Errorf("private backups evidence summary differs from copied custody")
			}
			privateBackupsEvidenceHash, privateBackupsEvidenceSize, err = maintenance.HashFile(privateBackupsEvidencePath)
			if err != nil {
				return nil, fmt.Errorf("hash private backups evidence: %w", err)
			}
		}
	}
	mainDocumentsSummary := copySummary{}
	if config.IncludeMainDocuments {
		mainDocumentsSummary, err = copyDirIfExists(config.MainDocumentsRoot, filepath.Join(backupDir, "main-documents"))
		if err != nil {
			return nil, fmt.Errorf("copy main documents: %w", err)
		}
	}
	boxNotesSummary := copySummary{}
	if config.IncludeBoxNotes {
		boxNotesSummary, err = copyDirIfExists(config.BoxNotesRoot, filepath.Join(backupDir, "box-notes"))
		if err != nil {
			return nil, fmt.Errorf("copy canonical Box Notes: %w", err)
		}
	}
	storageRetentionSummary := copySummary{}
	if config.IncludeStorageRetention {
		storageRetentionSummary, err = copyDirIfExists(config.StorageRetentionRoot, filepath.Join(backupDir, "storage-retention"))
		if err != nil {
			return nil, fmt.Errorf("copy storage retention: %w", err)
		}
	}
	storageArchiveSummary := copySummary{}
	if config.IncludeStorageArchive {
		storageArchiveDestination := filepath.Join(backupDir, "storage-archive")
		storageArchiveSummary, err = copyCommittedStorageArchives(config.StorageArchiveRoot, storageArchiveDestination)
		if err != nil {
			return nil, fmt.Errorf("copy storage archive: %w", err)
		}
		if err := ensureStorageArchiveBackupLayout(storageArchiveDestination); err != nil {
			return nil, fmt.Errorf("prepare storage archive backup layout: %w", err)
		}
	}
	notesProjectionSummary := copySummary{}
	if config.IncludeNotesProjection {
		notesProjectionSummary, err = copyDirIfExists(config.NotesProjectionRoot, filepath.Join(backupDir, "loom-notes"))
		if err != nil {
			return nil, fmt.Errorf("copy notes projection: %w", err)
		}
	}
	installManifestSummary, err := copyOptionalFileIfReadable("/etc/loom/install.yaml", filepath.Join(backupDir, "install.yaml"), 0o640)
	if err != nil {
		return nil, fmt.Errorf("copy install manifest snapshot: %w", err)
	}
	serviceEnvSummary, err := copyRedactedEnvIfReadable("/etc/loom/loom.env", filepath.Join(backupDir, "loom.env.redacted"))
	if err != nil {
		return nil, fmt.Errorf("copy redacted service env snapshot: %w", err)
	}

	healthHash, healthSize, err := maintenance.HashFile(healthPath)
	if err != nil {
		return nil, fmt.Errorf("hash health report: %w", err)
	}
	dumpHash := ""
	var dumpSize int64
	if config.IncludePostgres {
		dumpHash, dumpSize, err = maintenance.HashFile(dumpPath)
		if err != nil {
			return nil, fmt.Errorf("hash postgres dump: %w", err)
		}
	}
	importsEvidenceHash := ""
	var importsEvidenceSize int64
	if config.IncludeImports {
		importsEvidenceHash, importsEvidenceSize, err = maintenance.HashFile(importsEvidencePath)
		if err != nil {
			return nil, fmt.Errorf("hash Imports backup evidence: %w", err)
		}
	}

	artifacts := []maintenance.BackupManifestArtifact{
		{
			Kind:      maintenance.ArtifactKindBackupHealthReport,
			Path:      "health.json",
			SizeBytes: &healthSize,
			SHA256:    healthHash,
		},
	}
	if config.IncludePostgres {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      maintenance.ArtifactKindPostgresDump,
			Path:      config.DatabaseName + ".dump",
			SizeBytes: &dumpSize,
			SHA256:    dumpHash,
		})
	}
	if config.IncludeObjectStore {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      maintenance.ArtifactKindObjectStoreSnapshot,
			Path:      "object-store",
			SizeBytes: &objectStoreSummary.TotalBytes,
		})
	}
	if config.IncludeImports {
		artifacts = append(artifacts,
			maintenance.BackupManifestArtifact{
				Kind:      maintenance.ArtifactKindImportsSnapshot,
				Path:      "imports",
				SizeBytes: &importsSummary.TotalBytes,
			},
			maintenance.BackupManifestArtifact{
				Kind:      maintenance.ArtifactKindImportsEvidence,
				Path:      "imports-evidence.json",
				SizeBytes: &importsEvidenceSize,
				SHA256:    importsEvidenceHash,
			},
		)
	}
	if config.IncludePrivateBackups {
		artifacts = append(artifacts, userBackupsManifestArtifacts(userBackupsPlan, userBackupsSummary, privateBackupsSummary)...)
		if userBackupsPlan.includesLegacy() {
			artifacts = append(artifacts, maintenance.BackupManifestArtifact{
				Kind:      maintenance.ArtifactKindPrivateBackupsEvidence,
				Path:      maintenance.PrivateBackupsEvidencePath,
				SizeBytes: &privateBackupsEvidenceSize,
				SHA256:    privateBackupsEvidenceHash,
			})
		}
	}
	if config.IncludeMainDocuments {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      maintenance.ArtifactKindMainDocumentsSnapshot,
			Path:      "main-documents",
			SizeBytes: &mainDocumentsSummary.TotalBytes,
		})
	}
	if config.IncludeBoxNotes {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      boxNotesSnapshotArtifactKind,
			Path:      "box-notes",
			SizeBytes: &boxNotesSummary.TotalBytes,
		})
	}
	if config.IncludeStorageRetention {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      maintenance.ArtifactKindStorageRetentionSnapshot,
			Path:      "storage-retention",
			SizeBytes: &storageRetentionSummary.TotalBytes,
		})
	}
	if config.IncludeStorageArchive {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      maintenance.ArtifactKindStorageArchiveSnapshot,
			Path:      "storage-archive",
			SizeBytes: &storageArchiveSummary.TotalBytes,
		})
	}
	if config.IncludeNotesProjection {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      maintenance.ArtifactKindNotesProjectionSnapshot,
			Path:      "loom-notes",
			SizeBytes: &notesProjectionSummary.TotalBytes,
		})
	}
	if installManifestSummary.FileCount > 0 {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      "install_manifest_snapshot",
			Path:      "install.yaml",
			SizeBytes: &installManifestSummary.TotalBytes,
		})
	}
	if serviceEnvSummary.FileCount > 0 {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      "service_env_redacted_snapshot",
			Path:      "loom.env.redacted",
			SizeBytes: &serviceEnvSummary.TotalBytes,
		})
	}

	manifest := r.backupManifest(run, operation, config, now, hostname, &healthReport, userBackupsPlan.Layout)
	if installManifestSummary.FileCount > 0 {
		manifest.Paths.InstallManifest = "install.yaml"
	}
	if serviceEnvSummary.FileCount > 0 {
		manifest.Paths.ServiceEnvRedacted = "loom.env.redacted"
	}
	manifest.Artifacts = artifacts
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		return nil, fmt.Errorf("write backup manifest: %w", err)
	}

	verification := maintenance.BackupVerification{Status: maintenance.VerificationSucceeded}
	if config.VerifyAfterWrite {
		verification, err = maintenance.VerifyBackupDirectory(ctx, backupDir)
		if err != nil {
			return nil, err
		}
		verification.BackupOperationID = operation.MaintenanceOperationID
		if verification.Status != maintenance.VerificationSucceeded {
			return nil, fmt.Errorf("backup verification failed: %s", strings.Join(verification.Errors, "; "))
		}
		manifest.Verification = map[string]any{
			"status":     verification.Status,
			"checked_at": verification.CheckedAt.Format(time.RFC3339),
			"checks":     verification.Checks,
		}
		if err := writeJSONFile(manifestPath, manifest); err != nil {
			return nil, fmt.Errorf("write verified backup manifest: %w", err)
		}
	}
	coverage := backupcoverage.BoundedSummary{
		SchemaVersion: backupcoverage.SchemaVersion,
		Status:        backupcoverage.StatusUnknown,
		CapturedAt:    now,
		Unknown:       1,
	}
	if verification.Status == maintenance.VerificationSucceeded {
		report, coverageErr := backupcoverage.Check(ctx, backupcoverage.Options{
			Mode:                 backupcoverage.ModeMainBacked,
			DataDir:              r.DataDir,
			ObjectStoreRoot:      r.ObjectStoreRoot,
			ImportsRoot:          config.ImportsRoot,
			UserBackupsRoot:      config.UserBackupsRoot,
			MainDocumentsRoot:    config.MainDocumentsRoot,
			MainBoxPath:          filepath.Dir(config.MainDocumentsRoot),
			BoxNotesRoot:         config.BoxNotesRoot,
			StorageRetentionRoot: config.StorageRetentionRoot,
			StorageArchiveRoot:   config.StorageArchiveRoot,
			NotesProjectionRoot:  config.NotesProjectionRoot,
			MainBoxPolicy:        config.MainBoxPolicy,
			BackupRoot:           config.BackupRoot,
			ManifestPath:         manifestPath,
			Now:                  func() time.Time { return now },
		})
		if coverageErr == nil {
			coverage = backupcoverage.Bounded(report)
		}
	}

	manifestHash, manifestSize, err := maintenance.HashFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("hash backup manifest: %w", err)
	}

	totalBytes := manifestSize + healthSize + dumpSize + objectStoreSummary.TotalBytes + importsSummary.TotalBytes + importsEvidenceSize + userBackupsSummary.TotalBytes + privateBackupsSummary.TotalBytes + privateBackupsEvidenceSize + mainDocumentsSummary.TotalBytes + boxNotesSummary.TotalBytes + storageRetentionSummary.TotalBytes + storageArchiveSummary.TotalBytes + notesProjectionSummary.TotalBytes + installManifestSummary.TotalBytes + serviceEnvSummary.TotalBytes
	artifactInputs := []maintenance.CreateArtifactInput{
		{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           maintenance.ArtifactKindBackupManifest,
			URI:                    fileURI(manifestPath),
			SizeBytes:              &manifestSize,
			SHA256:                 manifestHash,
			Metadata:               artifactMetadata("manifest.json", backupDir),
		},
		{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           maintenance.ArtifactKindBackupHealthReport,
			URI:                    fileURI(healthPath),
			SizeBytes:              &healthSize,
			SHA256:                 healthHash,
			Metadata:               artifactMetadata("health.json", backupDir),
		},
	}
	if config.IncludePostgres {
		artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           maintenance.ArtifactKindPostgresDump,
			URI:                    fileURI(dumpPath),
			SizeBytes:              &dumpSize,
			SHA256:                 dumpHash,
			Metadata:               artifactMetadata(config.DatabaseName+".dump", backupDir),
		})
	}
	if config.IncludeObjectStore {
		artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           maintenance.ArtifactKindObjectStoreSnapshot,
			URI:                    fileURI(filepath.Join(backupDir, "object-store")),
			SizeBytes:              &objectStoreSummary.TotalBytes,
			Metadata:               copyArtifactMetadata("object-store", backupDir, objectStoreSummary),
		})
	}
	if config.IncludeImports {
		artifactInputs = append(artifactInputs,
			maintenance.CreateArtifactInput{
				MaintenanceOperationID: operation.MaintenanceOperationID,
				ArtifactKind:           maintenance.ArtifactKindImportsSnapshot,
				URI:                    fileURI(filepath.Join(backupDir, "imports")),
				SizeBytes:              &importsSummary.TotalBytes,
				Metadata:               copyArtifactMetadataWithSource("imports", backupDir, config.ImportsRoot, importsSummary),
			},
			maintenance.CreateArtifactInput{
				MaintenanceOperationID: operation.MaintenanceOperationID,
				ArtifactKind:           maintenance.ArtifactKindImportsEvidence,
				URI:                    fileURI(importsEvidencePath),
				SizeBytes:              &importsEvidenceSize,
				SHA256:                 importsEvidenceHash,
				Metadata:               artifactMetadata("imports-evidence.json", backupDir),
			},
		)
	}
	if config.IncludePrivateBackups {
		artifactInputs = append(artifactInputs, userBackupsMaintenanceArtifacts(operation.MaintenanceOperationID, backupDir, config.UserBackupsRoot, userBackupsPlan, userBackupsSummary, privateBackupsSummary)...)
		if userBackupsPlan.includesLegacy() {
			artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
				MaintenanceOperationID: operation.MaintenanceOperationID,
				ArtifactKind:           maintenance.ArtifactKindPrivateBackupsEvidence,
				URI:                    fileURI(privateBackupsEvidencePath),
				SizeBytes:              &privateBackupsEvidenceSize,
				SHA256:                 privateBackupsEvidenceHash,
				Metadata:               artifactMetadata(maintenance.PrivateBackupsEvidencePath, backupDir),
			})
		}
	}
	if config.IncludeMainDocuments {
		artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           maintenance.ArtifactKindMainDocumentsSnapshot,
			URI:                    fileURI(filepath.Join(backupDir, "main-documents")),
			SizeBytes:              &mainDocumentsSummary.TotalBytes,
			Metadata:               copyArtifactMetadataWithSource("main-documents", backupDir, config.MainDocumentsRoot, mainDocumentsSummary),
		})
	}
	if config.IncludeBoxNotes {
		artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           boxNotesSnapshotArtifactKind,
			URI:                    fileURI(filepath.Join(backupDir, "box-notes")),
			SizeBytes:              &boxNotesSummary.TotalBytes,
			Metadata:               copyArtifactMetadataWithSource("box-notes", backupDir, config.BoxNotesRoot, boxNotesSummary),
		})
	}
	if config.IncludeStorageRetention {
		artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           maintenance.ArtifactKindStorageRetentionSnapshot,
			URI:                    fileURI(filepath.Join(backupDir, "storage-retention")),
			SizeBytes:              &storageRetentionSummary.TotalBytes,
			Metadata:               copyArtifactMetadataWithSource("storage-retention", backupDir, config.StorageRetentionRoot, storageRetentionSummary),
		})
	}
	if config.IncludeStorageArchive {
		artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           maintenance.ArtifactKindStorageArchiveSnapshot,
			URI:                    fileURI(filepath.Join(backupDir, "storage-archive")),
			SizeBytes:              &storageArchiveSummary.TotalBytes,
			Metadata:               copyArtifactMetadataWithSource("storage-archive", backupDir, config.StorageArchiveRoot, storageArchiveSummary),
		})
	}
	if config.IncludeNotesProjection {
		artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           maintenance.ArtifactKindNotesProjectionSnapshot,
			URI:                    fileURI(filepath.Join(backupDir, "loom-notes")),
			SizeBytes:              &notesProjectionSummary.TotalBytes,
			Metadata:               copyArtifactMetadataWithSource("loom-notes", backupDir, config.NotesProjectionRoot, notesProjectionSummary),
		})
	}
	if installManifestSummary.FileCount > 0 {
		artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           "install_manifest_snapshot",
			URI:                    fileURI(filepath.Join(backupDir, "install.yaml")),
			SizeBytes:              &installManifestSummary.TotalBytes,
			Metadata:               copyArtifactMetadata("install.yaml", backupDir, installManifestSummary),
		})
	}
	if serviceEnvSummary.FileCount > 0 {
		artifactInputs = append(artifactInputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operation.MaintenanceOperationID,
			ArtifactKind:           "service_env_redacted_snapshot",
			URI:                    fileURI(filepath.Join(backupDir, "loom.env.redacted")),
			SizeBytes:              &serviceEnvSummary.TotalBytes,
			Metadata:               copyArtifactMetadata("loom.env.redacted", backupDir, serviceEnvSummary),
		})
	}
	for _, input := range artifactInputs {
		if err := r.recordMaintenanceArtifact(ctx, input); err != nil {
			return nil, err
		}
	}

	result, err := json.Marshal(map[string]any{
		"schema_version":      "main_backup.result.v0.2",
		"status":              maintenance.OperationSucceeded,
		"backup_operation_id": operation.MaintenanceOperationID,
		"worker_run_id":       run.Run.WorkerRunID,
		"backup_dir":          backupDir,
		"manifest_path":       manifestPath,
		"artifact_count":      len(artifactInputs),
		"total_bytes":         totalBytes,
		"object_store":        objectStoreSummary,
		"imports":             importsSummary,
		"user_backups":        userBackupsSummary,
		"private_backups":     privateBackupsSummary,
		"private_backups_evidence": map[string]any{
			"path":       privateBackupsEvidenceRelativePath,
			"size_bytes": privateBackupsEvidenceSize,
			"sha256":     privateBackupsEvidenceHash,
		},
		"user_backups_layout": userBackupsPlan.Layout,
		"main_documents":      mainDocumentsSummary,
		"box_notes":           boxNotesSummary,
		"storage_retention":   storageRetentionSummary,
		"storage_archive":     storageArchiveSummary,
		"notes_projection":    notesProjectionSummary,
		"verification":        verification,
		"coverage":            coverage,
	})
	if err != nil {
		return nil, err
	}
	return workers.JSONObject(result, "result_summary")
}

func (r MainBackupRuntime) recordMaintenanceArtifact(ctx context.Context, input maintenance.CreateArtifactInput) error {
	if r.recordArtifact != nil {
		return r.recordArtifact(ctx, input)
	}
	_, err := r.Maintenance.CreateArtifact(ctx, input)
	return err
}

func (r MainBackupRuntime) backupManifest(run workers.RunContext, operation maintenance.Operation, config mainBackupConfig, now time.Time, hostname string, healthReport *health.Report, userBackupsLayout userBackupsSnapshotLayout) maintenance.BackupManifest {
	var currentMigration *int64
	var latestMigration *int64
	if healthReport != nil {
		currentMigration = int64Ptr(healthReport.Checks.Migrations.CurrentVersion)
		latestMigration = int64Ptr(healthReport.Checks.Migrations.LatestVersion)
	}
	manifest := maintenance.BackupManifest{
		Schema:            maintenance.BackupManifestSchemaV09,
		BackupKind:        "loom_main_state",
		BackupOperationID: operation.MaintenanceOperationID,
		WorkerRunID:       run.Run.WorkerRunID,
		CreatedAt:         now.Format(time.RFC3339),
		Source: maintenance.BackupManifestSource{
			Hostname:    hostname,
			NodeID:      run.Instance.OwnerNodeID,
			NodeRole:    "main",
			Environment: r.Health.Config.Env,
			IsVPS:       false,
		},
		Loom: maintenance.BackupManifestLoom{
			Version:          r.Version,
			CurrentMigration: currentMigration,
			LatestMigration:  latestMigration,
		},
		System: maintenance.BackupManifestSystem{
			NixGeneration: currentNixGenerationPath(),
		},
		Exclusions: []string{
			"wireguard_private_keys",
			"ssh_private_keys",
			"node_credential_tokens",
			"enrollment_tokens",
			"environment_secret_files",
			"raw_loom_db_url",
		},
		Retention: maintenance.BackupManifestRetention{
			Mode:        config.Retention.Mode,
			KeepDaily:   config.Retention.KeepDaily,
			KeepWeekly:  config.Retention.KeepWeekly,
			KeepMonthly: config.Retention.KeepMonthly,
		},
	}
	if !config.IncludeNotesProjection {
		manifest.Exclusions = append(manifest.Exclusions, "generated_notes_projection")
	}
	if config.IncludePostgres {
		manifest.Database = maintenance.BackupManifestDatabase{
			Name:     config.DatabaseName,
			DumpFile: config.DatabaseName + ".dump",
			Format:   "pg_dump_custom",
		}
	}
	if config.IncludeObjectStore {
		manifest.Paths.ObjectStore = "object-store"
	}
	if config.IncludeImports {
		manifest.Paths.Imports = "imports"
		manifest.Paths.ImportsEvidence = "imports-evidence.json"
		manifest.Policies.Imports = config.ImportsPolicy
		manifest.Policies.ImportsSnapshot = config.ImportsSnapshotMethod
	}
	if config.IncludePrivateBackups {
		switch userBackupsLayout {
		case userBackupsSnapshotLegacy:
			manifest.Paths.PrivateBackups = userBackupsSnapshotRelativePath(userBackupsLayout)
			manifest.Paths.PrivateBackupsEvidence = maintenance.PrivateBackupsEvidencePath
		case userBackupsSnapshotMixed:
			manifest.Paths.UserBackups = userBackupsSnapshotRelativePath(userBackupsSnapshotCanonical)
			manifest.Paths.PrivateBackups = userBackupsSnapshotRelativePath(userBackupsSnapshotLegacy)
			manifest.Paths.PrivateBackupsEvidence = maintenance.PrivateBackupsEvidencePath
		default:
			manifest.Paths.UserBackups = userBackupsSnapshotRelativePath(userBackupsSnapshotCanonical)
		}
	}
	if config.IncludeMainDocuments {
		manifest.Paths.MainDocuments = "main-documents"
	}
	if config.IncludeStorageRetention {
		manifest.Paths.StorageRetention = "storage-retention"
	}
	if config.IncludeStorageArchive {
		manifest.Paths.StorageArchive = "storage-archive"
	}
	if config.IncludeNotesProjection {
		manifest.Paths.NotesProjection = "loom-notes"
	}
	manifest.Policies.MainBox = config.MainBoxPolicy
	manifest.Policies.NotesSourceRoots = "box_notes_snapshot_and_project_notes_private_backups_or_watched_roots"
	manifest.Policies.NotesProjection = "excluded_rebuildable_generated_output"
	if config.IncludeNotesProjection {
		manifest.Policies.NotesProjection = "explicitly_copied_generated_projection_and_rebuildable_from_canonical_notes_roots"
	}
	manifest.Policies.KnowledgeIndex = "captured_by_postgres_dump_as_knowledge_schema_tables"
	return manifest
}

func (r MainBackupRuntime) userBackupsSnapshotLayout() userBackupsSnapshotLayout {
	if r.Health.Config.LegacySplitRoots {
		return userBackupsSnapshotLegacy
	}
	return userBackupsSnapshotCanonical
}

func userBackupsSnapshotRelativePath(layout userBackupsSnapshotLayout) string {
	if layout == userBackupsSnapshotLegacy {
		return "private-backups"
	}
	return "user-backups"
}

func userBackupsManifestArtifacts(plan userBackupsSnapshotPlan, canonical, legacy copySummary) []maintenance.BackupManifestArtifact {
	artifacts := make([]maintenance.BackupManifestArtifact, 0, 2)
	if plan.includesCanonical() {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      maintenance.ArtifactKindPrivateBackupsSnapshot,
			Path:      userBackupsSnapshotRelativePath(userBackupsSnapshotCanonical),
			FileCount: &canonical.FileCount,
			SizeBytes: &canonical.TotalBytes,
		})
	}
	if plan.includesLegacy() {
		artifacts = append(artifacts, maintenance.BackupManifestArtifact{
			Kind:      maintenance.ArtifactKindPrivateBackupsSnapshot,
			Path:      userBackupsSnapshotRelativePath(userBackupsSnapshotLegacy),
			FileCount: &legacy.FileCount,
			SizeBytes: &legacy.TotalBytes,
		})
	}
	return artifacts
}

func userBackupsMaintenanceArtifacts(operationID, backupDir, sourceRoot string, plan userBackupsSnapshotPlan, canonical, legacy copySummary) []maintenance.CreateArtifactInput {
	inputs := make([]maintenance.CreateArtifactInput, 0, 2)
	if plan.includesCanonical() {
		pathValue := userBackupsSnapshotRelativePath(userBackupsSnapshotCanonical)
		inputs = append(inputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operationID,
			ArtifactKind:           maintenance.ArtifactKindPrivateBackupsSnapshot,
			URI:                    fileURI(filepath.Join(backupDir, pathValue)),
			SizeBytes:              &canonical.TotalBytes,
			Metadata:               copyArtifactMetadataWithSource(pathValue, backupDir, sourceRoot, canonical),
		})
	}
	if plan.includesLegacy() {
		pathValue := userBackupsSnapshotRelativePath(userBackupsSnapshotLegacy)
		inputs = append(inputs, maintenance.CreateArtifactInput{
			MaintenanceOperationID: operationID,
			ArtifactKind:           maintenance.ArtifactKindPrivateBackupsSnapshot,
			URI:                    fileURI(filepath.Join(backupDir, pathValue)),
			SizeBytes:              &legacy.TotalBytes,
			Metadata:               copyArtifactMetadataWithSource(pathValue, backupDir, sourceRoot, legacy),
		})
	}
	return inputs
}

func (r MainBackupRuntime) runPgDump(ctx context.Context, config mainBackupConfig, dumpPath string) error {
	command := exec.CommandContext
	if r.ExecCommand != nil {
		command = r.ExecCommand
	}
	cmd := command(ctx, config.PgDumpPath, "-Fc", "-d", r.DBURL, "-f", dumpPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_dump failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (r MainBackupRuntime) runProvenancePgDump(ctx context.Context, config mainBackupConfig, dumpPath, exportedSnapshot string) error {
	if strings.TrimSpace(r.ProvenanceDBURL) == "" || strings.TrimSpace(exportedSnapshot) == "" {
		return fmt.Errorf("isolated provenance pg_dump configuration is incomplete")
	}
	command := exec.CommandContext
	if r.ExecCommand != nil {
		command = r.ExecCommand
	}
	cmd := command(ctx, config.PgDumpPath, "--format=custom", "--snapshot="+exportedSnapshot, "--dbname="+r.ProvenanceDBURL, "--file="+dumpPath)
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("isolated provenance pg_dump failed: %w", err)
	}
	return nil
}

func (r MainBackupRuntime) recordBackupFailure(ctx context.Context, operation maintenance.Operation, workerRunID, backupDir string, cause error) error {
	details, err := workers.JSONObject(mustWorkerJSON(map[string]any{
		"schema_version":      "main_backup.failure_finding.v0.2",
		"backup_operation_id": operation.MaintenanceOperationID,
		"backup_dir":          backupDir,
		"error":               cause.Error(),
	}), "details_json")
	if err != nil {
		return err
	}
	_, _, err = r.Maintenance.UpsertFinding(ctx, maintenance.UpsertFindingInput{
		FindingKey:       backupFailureFindingKey(operation),
		WorkerInstanceID: operation.WorkerInstanceID,
		WorkerRunID:      workerRunID,
		FindingKind:      maintenance.FindingKindBackupFailed,
		Severity:         maintenance.SeverityCritical,
		SubjectKind:      "backup",
		SubjectID:        operation.MaintenanceOperationID,
		Summary:          "Main backup failed.",
		DetailsJSON:      details,
		Metadata:         json.RawMessage(`{"schema_version":"maintenance_finding.metadata.v0.2","source":"main_backup"}`),
	})
	return err
}

func (r MainBackupRuntime) resolveBackupFailure(ctx context.Context, operation maintenance.Operation) error {
	_, err := r.Maintenance.ResolveFinding(ctx, backupFailureFindingKey(operation), "Main backup succeeded.")
	if err == nil || errors.Is(err, maintenance.ErrNotFound) {
		return nil
	}
	return err
}

func backupFailureFindingKey(operation maintenance.Operation) string {
	return operation.WorkerInstanceID + ":" + maintenance.FindingKindBackupFailed + ":backup:main"
}

func backupDirName(now time.Time, operationID string) string {
	suffix := operationID
	if len(suffix) > 16 {
		suffix = suffix[len(suffix)-16:]
	}
	return now.UTC().Format("20060102T150405Z") + "-" + suffix
}

func copyOptionalFileIfReadable(source, destination string, mode os.FileMode) (copySummary, error) {
	source = filepath.Clean(strings.TrimSpace(source))
	destination = filepath.Clean(strings.TrimSpace(destination))
	info, err := os.Stat(source)
	if err != nil {
		return copySummary{SourceExists: !os.IsNotExist(err)}, nil
	}
	if !info.Mode().IsRegular() {
		return copySummary{SourceExists: true}, nil
	}
	if err := copyFile(source, destination, mode); err != nil {
		if os.IsPermission(err) {
			return copySummary{SourceExists: true}, nil
		}
		return copySummary{}, err
	}
	written, err := os.Stat(destination)
	if err != nil {
		return copySummary{}, err
	}
	return copySummary{SourceExists: true, FileCount: 1, TotalBytes: written.Size()}, nil
}

func copyRedactedEnvIfReadable(source, destination string) (copySummary, error) {
	source = filepath.Clean(strings.TrimSpace(source))
	destination = filepath.Clean(strings.TrimSpace(destination))
	raw, err := os.ReadFile(source)
	if err != nil {
		return copySummary{SourceExists: !os.IsNotExist(err)}, nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return copySummary{}, err
	}
	redacted := redactEnvSnapshot(string(raw))
	if err := os.WriteFile(destination, []byte(redacted), 0o640); err != nil {
		return copySummary{}, err
	}
	return copySummary{SourceExists: true, FileCount: 1, TotalBytes: int64(len(redacted))}, nil
}

func redactEnvSnapshot(raw string) string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && isSensitiveBackupKey(key) {
			out = append(out, strings.TrimSpace(key)+"=[REDACTED]")
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func isSensitiveBackupKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, token := range []string{"db_url", "password", "token", "secret", "api_key", "apikey", "credential"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}

func copyDirIfExists(sourceRoot, destinationRoot string) (copySummary, error) {
	return copyDirIfExistsFiltered(sourceRoot, destinationRoot, nil)
}

func copyDirIfExistsFiltered(sourceRoot, destinationRoot string, skip func(relativePath string, entry os.DirEntry) bool) (copySummary, error) {
	sourceRoot = filepath.Clean(strings.TrimSpace(sourceRoot))
	destinationRoot = filepath.Clean(strings.TrimSpace(destinationRoot))
	if sourceRoot == "." || sourceRoot == "" {
		if err := os.MkdirAll(destinationRoot, 0o750); err != nil {
			return copySummary{}, err
		}
		return copySummary{SourceExists: false}, nil
	}
	info, err := os.Stat(sourceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(destinationRoot, 0o750); err != nil {
				return copySummary{}, err
			}
			return copySummary{SourceExists: false}, nil
		}
		return copySummary{}, err
	}
	if !info.IsDir() {
		return copySummary{}, fmt.Errorf("source %s is not a directory", sourceRoot)
	}
	if pathWithinRuntime(sourceRoot, destinationRoot) {
		return copySummary{}, fmt.Errorf("destination must not be inside source")
	}
	if err := os.MkdirAll(destinationRoot, backupDirectoryMode(info.Mode().Perm())); err != nil {
		return copySummary{}, err
	}

	summary := copySummary{SourceExists: true}
	err = filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if skip != nil && skip(rel, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dst := filepath.Join(destinationRoot, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(dst, backupDirectoryMode(info.Mode().Perm()))
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := copyFile(path, dst, backupFileMode(info.Mode().Perm())); err != nil {
			return err
		}
		summary.FileCount++
		summary.TotalBytes += info.Size()
		return nil
	})
	return summary, err
}

const importsLaneCommitManifest = ".loom-lane-custody.json"

const importsStoreSafetyReserveBytes = uint64(2 << 30)

type importsSnapshotHooks struct {
	availableBytes       func(string) (uint64, error)
	beforeObjectWrite    func(string)
	afterSourcePreflight func() error
}

type importsSharedObjectPlan struct {
	sourceRoot string
	relative   string
	expected   os.FileInfo
	hashHex    string
	storeRoot  string
	objectPath string
	missing    bool
}

type importsSharedSnapshotPlan struct {
	bySourcePath map[string]importsSharedObjectPlan
	missingBytes uint64
}

func copyImportsCustody(ctx context.Context, sourceRoot, destinationRoot, contentStoreRoot, policy, snapshotMethod string) (copySummary, error) {
	return copyImportsCustodyWithHooks(ctx, sourceRoot, destinationRoot, contentStoreRoot, policy, snapshotMethod, importsSnapshotHooks{availableBytes: importsFilesystemAvailableBytes})
}

func copyImportsCustodyWithHooks(ctx context.Context, sourceRoot, destinationRoot, contentStoreRoot, policy, snapshotMethod string, hooks importsSnapshotHooks) (copySummary, error) {
	sourceRoot = filepath.Clean(strings.TrimSpace(sourceRoot))
	destinationRoot = filepath.Clean(strings.TrimSpace(destinationRoot))
	contentStoreRoot = filepath.Clean(strings.TrimSpace(contentStoreRoot))
	info, err := os.Lstat(sourceRoot)
	if err != nil {
		return copySummary{}, fmt.Errorf("Imports custody root is unavailable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return copySummary{}, fmt.Errorf("Imports custody root must be a real directory")
	}
	if pathWithinRuntime(sourceRoot, destinationRoot) {
		return copySummary{}, fmt.Errorf("Imports backup destination must not be inside source")
	}
	var canonicalBatches []string
	switch policy {
	case maintenance.ImportsBackupPolicyLegacy:
	case maintenance.ImportsBackupPolicyCanonical:
		canonicalBatches, err = committedImportsBatches(sourceRoot)
		if err != nil {
			return copySummary{}, err
		}
	default:
		return copySummary{}, fmt.Errorf("unsupported Imports backup policy %q", policy)
	}
	var sharedPlan importsSharedSnapshotPlan
	var sourceEvidence maintenance.ImportsBackupEvidence
	if snapshotMethod == maintenance.ImportsSnapshotSharedStore {
		if contentStoreRoot == "." || contentStoreRoot == "" || !filepath.IsAbs(contentStoreRoot) {
			return copySummary{}, fmt.Errorf("Imports shared content store root must be absolute")
		}
		if pathWithinRuntime(sourceRoot, contentStoreRoot) {
			return copySummary{}, fmt.Errorf("Imports shared content store must not be inside source custody")
		}
		var inspectErr error
		sourceEvidence, inspectErr = maintenance.InspectImportsCustody(ctx, sourceRoot, policy)
		if inspectErr != nil {
			return copySummary{}, inspectErr
		}
		sharedPlan, err = preflightImportsSharedSnapshot(ctx, sourceRoot, sourceEvidence, contentStoreRoot, hooks)
		if err != nil {
			return copySummary{}, err
		}
		if hooks.afterSourcePreflight != nil {
			if err := hooks.afterSourcePreflight(); err != nil {
				return copySummary{}, err
			}
		}
		if policy == maintenance.ImportsBackupPolicyCanonical {
			currentBatches, err := committedImportsBatches(sourceRoot)
			if err != nil {
				return copySummary{}, err
			}
			if !equalImportsBatchLists(canonicalBatches, currentBatches) {
				return copySummary{}, fmt.Errorf("canonical Imports batch inventory changed after shared snapshot preflight")
			}
		}
	} else {
		return copySummary{}, fmt.Errorf("unsupported Imports snapshot method %q", snapshotMethod)
	}
	if err := os.MkdirAll(destinationRoot, info.Mode().Perm()); err != nil {
		return copySummary{}, err
	}
	if snapshotMethod == maintenance.ImportsSnapshotSharedStore {
		if err := ensureImportsStoreDirectory(contentStoreRoot, contentStoreRoot); err != nil {
			return copySummary{}, err
		}
		if err := ensureImportsStoreDirectory(contentStoreRoot, filepath.Join(contentStoreRoot, "sha256")); err != nil {
			return copySummary{}, err
		}
	}
	var sharedObjectLinkCount int64
	switch policy {
	case maintenance.ImportsBackupPolicyLegacy:
		links, err := copyImportsTree(ctx, sourceRoot, destinationRoot, contentStoreRoot, snapshotMethod, hooks, sharedPlan)
		if err != nil {
			return copySummary{}, err
		}
		sharedObjectLinkCount += links
	case maintenance.ImportsBackupPolicyCanonical:
		for _, relativeBatch := range canonicalBatches {
			links, err := copyImportsTree(ctx, filepath.Join(sourceRoot, relativeBatch), filepath.Join(destinationRoot, relativeBatch), contentStoreRoot, snapshotMethod, hooks, sharedPlan)
			if err != nil {
				return copySummary{}, err
			}
			sharedObjectLinkCount += links
		}
		if err := preserveImportsCanonicalParents(sourceRoot, destinationRoot, canonicalBatches); err != nil {
			return copySummary{}, err
		}
	}
	evidence, err := maintenance.WriteImportsBackupEvidenceMatching(ctx, destinationRoot, filepath.Join(filepath.Dir(destinationRoot), "imports-evidence.json"), policy, snapshotMethod, sharedObjectLinkCount, &sourceEvidence)
	if err != nil {
		return copySummary{}, err
	}
	return copySummary{SourceExists: true, FileCount: evidence.FileCount, TotalBytes: evidence.TotalBytes, SharedObjectLinkCount: sharedObjectLinkCount}, nil
}

func equalImportsBatchLists(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func preserveImportsCanonicalParents(sourceRoot, destinationRoot string, batches []string) error {
	parents := map[string]struct{}{".": {}}
	for _, batch := range batches {
		parts := strings.Split(filepath.ToSlash(batch), "/")
		if len(parts) != 3 {
			return fmt.Errorf("invalid committed Imports batch path %q", batch)
		}
		parents[parts[0]] = struct{}{}
		parents[filepath.Join(parts[0], parts[1])] = struct{}{}
	}
	paths := make([]string, 0, len(parents))
	for relative := range parents {
		paths = append(paths, relative)
	}
	sort.Slice(paths, func(i, j int) bool {
		return strings.Count(filepath.ToSlash(paths[i]), "/") > strings.Count(filepath.ToSlash(paths[j]), "/")
	})
	for _, relative := range paths {
		source := sourceRoot
		destination := destinationRoot
		if relative != "." {
			source = filepath.Join(sourceRoot, relative)
			destination = filepath.Join(destinationRoot, relative)
		}
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("Imports custody parent %s is not a real directory", source)
		}
		if err := os.Chmod(destination, info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Chtimes(destination, info.ModTime(), info.ModTime()); err != nil {
			return err
		}
	}
	return nil
}

func committedImportsBatches(root string) ([]string, error) {
	var batches []string
	sources, err := strictCustodyDirectories(root)
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		dates, err := strictCustodyDirectories(filepath.Join(root, source))
		if err != nil {
			return nil, err
		}
		for _, date := range dates {
			dateRoot := filepath.Join(root, source, date)
			items, err := strictCustodyDirectories(dateRoot)
			if err != nil {
				return nil, err
			}
			for _, batch := range items {
				batchRoot := filepath.Join(dateRoot, batch)
				marker, err := os.Lstat(filepath.Join(batchRoot, importsLaneCommitManifest))
				if os.IsNotExist(err) {
					return nil, fmt.Errorf("canonical Imports batch %s has no custody commit manifest", batchRoot)
				}
				if err != nil {
					return nil, err
				}
				if marker.Mode()&os.ModeSymlink != 0 || !marker.Mode().IsRegular() {
					return nil, fmt.Errorf("Imports commit marker %s must be a regular no-follow file", filepath.Join(batchRoot, importsLaneCommitManifest))
				}
				batches = append(batches, filepath.Join(source, date, batch))
			}
		}
	}
	sort.Strings(batches)
	return batches, nil
}

func strictCustodyDirectories(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			return nil, fmt.Errorf("canonical Imports contains unexpected hidden component %s", filepath.Join(root, entry.Name()))
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Imports custody component %s is a symlink", filepath.Join(root, entry.Name()))
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("Imports custody component %s is not a directory", filepath.Join(root, entry.Name()))
		}
		result = append(result, entry.Name())
	}
	sort.Strings(result)
	return result, nil
}

type importsCopiedMetadata struct {
	path         string
	mode         os.FileMode
	modifiedAt   time.Time
	isDir        bool
	isSymlink    bool
	sharedObject bool
}

func preflightImportsSharedSnapshot(ctx context.Context, sourceRootPath string, sourceEvidence maintenance.ImportsBackupEvidence, contentStoreRoot string, hooks importsSnapshotHooks) (importsSharedSnapshotPlan, error) {
	plan := importsSharedSnapshotPlan{bySourcePath: make(map[string]importsSharedObjectPlan)}
	missingObjects := make(map[string]uint64)
	sourceRoot, err := os.OpenRoot(sourceRootPath)
	if err != nil {
		return importsSharedSnapshotPlan{}, err
	}
	defer sourceRoot.Close()
	for _, evidence := range sourceEvidence.Entries {
		if err := ctx.Err(); err != nil {
			return importsSharedSnapshotPlan{}, err
		}
		if evidence.Kind != "regular_file" {
			continue
		}
		relative := filepath.FromSlash(evidence.RelativePath)
		objectPlan, err := planImportsSharedObject(ctx, sourceRootPath, sourceRoot, relative, contentStoreRoot, evidence)
		if err != nil {
			return importsSharedSnapshotPlan{}, err
		}
		plan.bySourcePath[filepath.Clean(filepath.Join(sourceRootPath, relative))] = objectPlan
		if objectPlan.missing {
			missingObjects[objectPlan.objectPath] = uint64(objectPlan.expected.Size())
		}
	}
	for _, size := range missingObjects {
		if ^uint64(0)-plan.missingBytes < size {
			return importsSharedSnapshotPlan{}, fmt.Errorf("Imports missing-object bytes overflow capacity calculation")
		}
		plan.missingBytes += size
	}
	availableBytes := hooks.availableBytes
	if availableBytes == nil {
		availableBytes = importsFilesystemAvailableBytes
	}
	probePath, err := existingImportsCapacityProbe(contentStoreRoot)
	if err != nil {
		return importsSharedSnapshotPlan{}, err
	}
	available, err := availableBytes(probePath)
	if err != nil {
		return importsSharedSnapshotPlan{}, fmt.Errorf("inspect Imports shared-store capacity: %w", err)
	}
	if ^uint64(0)-plan.missingBytes < importsStoreSafetyReserveBytes {
		return importsSharedSnapshotPlan{}, fmt.Errorf("Imports missing-object bytes overflow safety-reserve calculation")
	}
	required := plan.missingBytes + importsStoreSafetyReserveBytes
	if available < required {
		return importsSharedSnapshotPlan{}, fmt.Errorf("Imports shared content store has %d bytes available; all missing objects require %d bytes including %d-byte safety reserve", available, required, importsStoreSafetyReserveBytes)
	}
	return plan, nil
}

func planImportsSharedObject(ctx context.Context, sourceRootPath string, sourceRoot *os.Root, relative, contentStoreRoot string, evidence maintenance.ImportsBackupEvidenceEntry) (importsSharedObjectPlan, error) {
	source, err := sourceRoot.Open(relative)
	if err != nil {
		return importsSharedObjectPlan{}, err
	}
	defer source.Close()
	before, err := source.Stat()
	if err != nil {
		return importsSharedObjectPlan{}, err
	}
	if !before.Mode().IsRegular() || before.Size() != evidence.SizeBytes || uint32(before.Mode().Perm()) != evidence.Mode || !before.ModTime().UTC().Equal(evidence.ModifiedAt) {
		return importsSharedObjectPlan{}, fmt.Errorf("Imports source changed before shared snapshot preflight: %s", filepath.Join(sourceRootPath, relative))
	}
	hashHex := evidence.SHA256
	if len(hashHex) != sha256.Size*2 {
		return importsSharedObjectPlan{}, fmt.Errorf("Imports source evidence has invalid checksum for %s", filepath.Join(sourceRootPath, relative))
	}
	objectName := fmt.Sprintf("%s-%016x-%04o-%016x", hashHex, uint64(before.Size()), uint32(before.Mode().Perm()), uint64(before.ModTime().UnixNano()))
	objectPath := filepath.Join(contentStoreRoot, "sha256", hashHex[:2], objectName)
	objectPlan := importsSharedObjectPlan{sourceRoot: sourceRootPath, relative: relative, expected: before, hashHex: hashHex, storeRoot: contentStoreRoot, objectPath: objectPath, missing: true}
	parentExists, err := importsStoreDirectoryExists(contentStoreRoot, filepath.Dir(objectPath))
	if err != nil {
		return importsSharedObjectPlan{}, err
	}
	if !parentExists {
		return objectPlan, nil
	}
	if _, err := os.Lstat(objectPath); err == nil {
		if err := verifyImportsSharedObject(ctx, objectPath, before, hashHex); err != nil {
			return importsSharedObjectPlan{}, err
		}
		objectPlan.missing = false
	} else if !os.IsNotExist(err) {
		return importsSharedObjectPlan{}, err
	}
	return objectPlan, nil
}

func existingImportsCapacityProbe(contentStoreRoot string) (string, error) {
	exists, err := importsStoreDirectoryExists(contentStoreRoot, contentStoreRoot)
	if err != nil {
		return "", err
	}
	if exists {
		return contentStoreRoot, nil
	}
	backupRoot := filepath.Dir(filepath.Clean(contentStoreRoot))
	info, err := os.Lstat(backupRoot)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("Imports shared-store backup root %s must be a real directory", backupRoot)
	}
	return backupRoot, nil
}

func copyImportsTree(ctx context.Context, sourceRoot, destinationRoot, contentStoreRoot, snapshotMethod string, hooks importsSnapshotHooks, sharedPlan importsSharedSnapshotPlan) (int64, error) {
	rootInfo, err := os.Lstat(sourceRoot)
	if err != nil {
		return 0, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return 0, fmt.Errorf("Imports copy source %s must be a real directory", sourceRoot)
	}
	if err := os.MkdirAll(destinationRoot, rootInfo.Mode().Perm()); err != nil {
		return 0, err
	}
	sourceHandle, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return 0, err
	}
	defer sourceHandle.Close()
	metadata := []importsCopiedMetadata{{path: destinationRoot, mode: rootInfo.Mode().Perm(), modifiedAt: rootInfo.ModTime(), isDir: true}}
	var sharedObjectLinkCount int64
	err = filepath.WalkDir(sourceRoot, func(pathValue string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(sourceRoot, pathValue)
		if err != nil || relative == "." {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		destination := filepath.Join(destinationRoot, relative)
		switch {
		case info.IsDir():
			if err := os.Mkdir(destination, info.Mode().Perm()); err != nil && !os.IsExist(err) {
				return err
			}
			metadata = append(metadata, importsCopiedMetadata{path: destination, mode: info.Mode().Perm(), modifiedAt: info.ModTime(), isDir: true})
		case info.Mode().IsRegular():
			sharedObject, err := copyImportsRegularFile(ctx, sourceRoot, sourceHandle, relative, destination, contentStoreRoot, info, snapshotMethod, hooks, sharedPlan)
			if err != nil {
				return err
			}
			if sharedObject {
				sharedObjectLinkCount++
			}
			metadata = append(metadata, importsCopiedMetadata{path: destination, mode: info.Mode().Perm(), modifiedAt: info.ModTime(), sharedObject: sharedObject})
		case info.Mode()&os.ModeSymlink != 0:
			target, err := sourceHandle.Readlink(relative)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, destination); err != nil {
				return err
			}
			metadata = append(metadata, importsCopiedMetadata{path: destination, modifiedAt: info.ModTime(), isSymlink: true})
		default:
			return fmt.Errorf("Imports custody entry %s has unsupported type %s", pathValue, info.Mode().Type())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for index := len(metadata) - 1; index >= 0; index-- {
		item := metadata[index]
		if item.isSymlink {
			times := []unix.Timespec{unix.NsecToTimespec(item.modifiedAt.UnixNano()), unix.NsecToTimespec(item.modifiedAt.UnixNano())}
			if err := unix.UtimesNanoAt(unix.AT_FDCWD, item.path, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return 0, err
			}
			continue
		}
		if item.sharedObject {
			info, err := os.Lstat(item.path)
			if err != nil {
				return 0, err
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != item.mode.Perm() || !info.ModTime().Equal(item.modifiedAt) {
				return 0, fmt.Errorf("Imports shared object metadata changed before snapshot commit: %s", item.path)
			}
			continue
		}
		if err := os.Chmod(item.path, item.mode); err != nil {
			return 0, err
		}
		if err := os.Chtimes(item.path, item.modifiedAt, item.modifiedAt); err != nil {
			return 0, err
		}
	}
	return sharedObjectLinkCount, nil
}

func copyImportsRegularFile(ctx context.Context, sourceRootPath string, sourceRoot *os.Root, relative, destination, contentStoreRoot string, expected os.FileInfo, snapshotMethod string, hooks importsSnapshotHooks, sharedPlan importsSharedSnapshotPlan) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return false, err
	}
	if snapshotMethod == maintenance.ImportsSnapshotSharedStore {
		planned, ok := sharedPlan.bySourcePath[filepath.Clean(filepath.Join(sourceRootPath, relative))]
		if !ok {
			return false, fmt.Errorf("Imports source %s was not included in aggregate shared-store preflight", filepath.Join(sourceRootPath, relative))
		}
		objectPath, err := publishImportsSharedObject(ctx, sourceRootPath, sourceRoot, relative, expected, planned, hooks)
		if err != nil {
			return false, err
		}
		if err := os.Link(objectPath, destination); err != nil {
			return false, fmt.Errorf("link Imports shared object %s into snapshot: %w", objectPath, err)
		}
		objectInfo, objectErr := os.Lstat(objectPath)
		destinationInfo, destinationErr := os.Lstat(destination)
		if objectErr != nil || destinationErr != nil || !objectInfo.Mode().IsRegular() || !destinationInfo.Mode().IsRegular() || !os.SameFile(objectInfo, destinationInfo) {
			return false, fmt.Errorf("Imports shared object identity verification failed for %s", objectPath)
		}
		return true, nil
	}
	return false, fmt.Errorf("unsupported Imports snapshot method %q", snapshotMethod)
}

func publishImportsSharedObject(ctx context.Context, sourceRootPath string, sourceRoot *os.Root, relative string, expected os.FileInfo, planned importsSharedObjectPlan, hooks importsSnapshotHooks) (string, error) {
	objectsRoot := filepath.Dir(filepath.Dir(planned.objectPath))
	source, err := sourceRoot.Open(relative)
	if err != nil {
		return "", err
	}
	defer source.Close()
	before, err := source.Stat()
	if err != nil {
		return "", err
	}
	if !sameImportsSource(before, expected) || !sameImportsSource(before, planned.expected) {
		return "", fmt.Errorf("Imports source changed before shared snapshot: %s", filepath.Join(sourceRootPath, relative))
	}
	hashHex := planned.hashHex
	objectPath := planned.objectPath
	shardRoot := filepath.Dir(objectPath)
	if err := ensureImportsStoreDirectory(planned.storeRoot, shardRoot); err != nil {
		return "", err
	}
	if _, err := os.Lstat(objectPath); err == nil {
		digest := sha256.New()
		hashedBytes, hashErr := copyImportsWithContext(ctx, digest, source)
		after, statErr := source.Stat()
		if hashErr != nil {
			return "", hashErr
		}
		if statErr != nil {
			return "", statErr
		}
		if !sameImportsSource(before, after) || hashedBytes != before.Size() || hex.EncodeToString(digest.Sum(nil)) != hashHex {
			return "", fmt.Errorf("Imports source changed after aggregate shared snapshot preflight: %s", filepath.Join(sourceRootPath, relative))
		}
		if err := verifyImportsSharedObject(ctx, objectPath, before, hashHex); err != nil {
			return "", err
		}
		return objectPath, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if !planned.missing {
		return "", fmt.Errorf("Imports shared object disappeared after aggregate preflight: %s", objectPath)
	}
	if hooks.beforeObjectWrite != nil {
		hooks.beforeObjectWrite(objectPath)
	}
	temporary, err := os.CreateTemp(objectsRoot, ".imports-object-")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	copyDigest := sha256.New()
	written, copyErr := copyImportsWithContext(ctx, io.MultiWriter(temporary, copyDigest), source)
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	afterCopy, err := source.Stat()
	if err != nil {
		return "", err
	}
	if !sameImportsSource(before, afterCopy) || written != before.Size() || hex.EncodeToString(copyDigest.Sum(nil)) != hashHex {
		return "", fmt.Errorf("Imports source changed while publishing shared snapshot object: %s", filepath.Join(sourceRootPath, relative))
	}
	if err := os.Chmod(temporaryPath, before.Mode().Perm()); err != nil {
		return "", err
	}
	if err := os.Chtimes(temporaryPath, before.ModTime(), before.ModTime()); err != nil {
		return "", err
	}
	if err := syncImportsFile(temporaryPath); err != nil {
		return "", err
	}
	if err := os.Link(temporaryPath, objectPath); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("publish Imports shared object: %w", err)
	}
	if err := verifyImportsSharedObject(ctx, objectPath, before, hashHex); err != nil {
		return "", err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return "", err
	}
	keepTemporary = false
	if err := syncImportsDirectory(shardRoot); err != nil {
		return "", err
	}
	return objectPath, nil
}

func sameImportsSource(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Mode().IsRegular() && right.Mode().IsRegular() && os.SameFile(left, right) && left.Size() == right.Size() && left.Mode().Perm() == right.Mode().Perm() && left.ModTime().Equal(right.ModTime())
}

func importsFilesystemAvailableBytes(pathValue string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(pathValue, &stat); err != nil {
		return 0, err
	}
	if stat.Bavail < 0 || stat.Bsize < 0 {
		return 0, fmt.Errorf("filesystem reported negative available capacity")
	}
	availableBlocks := uint64(stat.Bavail)
	blockSize := uint64(stat.Bsize)
	if blockSize != 0 && availableBlocks > ^uint64(0)/blockSize {
		return ^uint64(0), nil
	}
	return availableBlocks * blockSize, nil
}

func verifyImportsSharedObject(ctx context.Context, objectPath string, expected os.FileInfo, expectedHash string) error {
	info, err := os.Lstat(objectPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != expected.Size() || info.Mode().Perm() != expected.Mode().Perm() || !info.ModTime().Equal(expected.ModTime()) {
		return fmt.Errorf("Imports shared object metadata mismatch: %s", objectPath)
	}
	file, err := os.Open(objectPath)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := copyImportsWithContext(ctx, digest, file); err != nil {
		return err
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != expectedHash {
		return fmt.Errorf("Imports shared object checksum mismatch: %s", objectPath)
	}
	return nil
}

func copyImportsWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func ensureImportsStoreDirectory(contentStoreRoot, pathValue string) error {
	_, err := walkImportsStoreDirectories(contentStoreRoot, pathValue, true)
	return err
}

func importsStoreDirectoryExists(contentStoreRoot, pathValue string) (bool, error) {
	return walkImportsStoreDirectories(contentStoreRoot, pathValue, false)
}

func walkImportsStoreDirectories(contentStoreRoot, pathValue string, create bool) (bool, error) {
	contentStoreRoot = filepath.Clean(contentStoreRoot)
	pathValue = filepath.Clean(pathValue)
	backupRoot := filepath.Dir(contentStoreRoot)
	if !filepath.IsAbs(contentStoreRoot) || !filepath.IsAbs(pathValue) || !pathWithinRuntime(backupRoot, pathValue) || !pathWithinRuntime(contentStoreRoot, pathValue) {
		return false, fmt.Errorf("Imports shared content store path %s is outside %s", pathValue, contentStoreRoot)
	}
	backupInfo, err := os.Lstat(backupRoot)
	if err != nil || backupInfo.Mode()&os.ModeSymlink != 0 || !backupInfo.IsDir() {
		return false, fmt.Errorf("Imports shared-store backup root %s must be a real directory", backupRoot)
	}
	current, err := os.OpenFile(backupRoot, os.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("open Imports shared-store backup root: %w", err)
	}
	defer func() { _ = current.Close() }()
	relative, err := filepath.Rel(backupRoot, pathValue)
	if err != nil || relative == "." {
		return relative == ".", err
	}
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		fd, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil && errors.Is(openErr, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(int(current.Fd()), component, 0o750); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return false, fmt.Errorf("create confined Imports store component %q: %w", component, mkdirErr)
			}
			fd, openErr = unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			if errors.Is(openErr, unix.ENOENT) && !create {
				return false, nil
			}
			return false, fmt.Errorf("Imports shared content store component %s must be a real no-follow directory: %w", filepath.Join(backupRoot, filepath.FromSlash(component)), openErr)
		}
		next := os.NewFile(uintptr(fd), component)
		if err := current.Close(); err != nil {
			_ = next.Close()
			return false, err
		}
		current = next
	}
	return true, nil
}

func syncImportsFile(pathValue string) error {
	file, err := os.OpenFile(pathValue, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncImportsDirectory(pathValue string) error {
	directory, err := os.Open(pathValue)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func classifyUserBackupsSnapshot(sourceRoot string, defaultLayout userBackupsSnapshotLayout) (userBackupsSnapshotPlan, error) {
	if defaultLayout != userBackupsSnapshotCanonical && defaultLayout != userBackupsSnapshotLegacy && defaultLayout != userBackupsSnapshotMixed {
		return userBackupsSnapshotPlan{}, fmt.Errorf("unsupported default user backup snapshot layout %q", defaultLayout)
	}
	plan := userBackupsSnapshotPlan{Layout: defaultLayout}
	sourceRoot = filepath.Clean(strings.TrimSpace(sourceRoot))
	if sourceRoot == "." || sourceRoot == "" || !filepath.IsAbs(sourceRoot) {
		return userBackupsSnapshotPlan{}, fmt.Errorf("user backup custody root must be absolute")
	}
	info, err := os.Lstat(sourceRoot)
	if os.IsNotExist(err) {
		return plan, nil
	}
	if err != nil {
		return userBackupsSnapshotPlan{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return userBackupsSnapshotPlan{}, fmt.Errorf("user backup custody root %s must be a real directory", sourceRoot)
	}
	plan.SourceExists = true
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return userBackupsSnapshotPlan{}, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			return userBackupsSnapshotPlan{}, fmt.Errorf("unclassified hidden user backup custody component %q", entry.Name())
		}
		entryPath := filepath.Join(sourceRoot, entry.Name())
		if err := requireRealUserBackupDirectory(entryPath); err != nil {
			return userBackupsSnapshotPlan{}, err
		}
		legacyPayloads, legacyErr := classifyLegacyPrivateBackupNode(sourceRoot, entry.Name())
		canonicalRoots, canonicalBatches, inProgress, canonicalErr := classifyCanonicalUserBackupNode(sourceRoot, entry.Name())
		switch {
		case legacyErr == nil && canonicalErr == nil:
			return userBackupsSnapshotPlan{}, fmt.Errorf("user backup custody component %q is ambiguous between canonical and legacy layouts", entry.Name())
		case legacyErr == nil:
			plan.LegacyNodes = append(plan.LegacyNodes, entry.Name())
			plan.LegacyPayloads = append(plan.LegacyPayloads, legacyPayloads...)
		case canonicalErr == nil:
			plan.CanonicalNodes = append(plan.CanonicalNodes, entry.Name())
			plan.CanonicalRoots = append(plan.CanonicalRoots, canonicalRoots...)
			plan.CanonicalBatches = append(plan.CanonicalBatches, canonicalBatches...)
			plan.InProgressBatches = append(plan.InProgressBatches, inProgress...)
		default:
			return userBackupsSnapshotPlan{}, fmt.Errorf("unclassified user backup custody component %q: canonical: %v; legacy: %v", entry.Name(), canonicalErr, legacyErr)
		}
	}
	switch {
	case len(plan.CanonicalNodes) > 0 && len(plan.LegacyNodes) > 0:
		plan.Layout = userBackupsSnapshotMixed
	case len(plan.CanonicalNodes) > 0:
		plan.Layout = userBackupsSnapshotCanonical
	case len(plan.LegacyNodes) > 0:
		plan.Layout = userBackupsSnapshotLegacy
	}
	return plan, nil
}

func classifyCanonicalUserBackupNode(sourceRoot, node string) ([]string, []userBackupsCanonicalBatch, []string, error) {
	nodeRoot := filepath.Join(sourceRoot, node)
	protectedRoots, err := os.ReadDir(nodeRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(protectedRoots) == 0 {
		return nil, nil, nil, fmt.Errorf("canonical node has no protected roots")
	}
	roots := make([]string, 0, len(protectedRoots))
	var batches []userBackupsCanonicalBatch
	var inProgress []string
	for _, protectedRoot := range protectedRoots {
		if strings.HasPrefix(protectedRoot.Name(), ".") {
			return nil, nil, nil, fmt.Errorf("canonical protected root %q uses an internal namespace", filepath.Join(node, protectedRoot.Name()))
		}
		rootPath := filepath.Join(nodeRoot, protectedRoot.Name())
		if err := requireRealUserBackupDirectory(rootPath); err != nil {
			return nil, nil, nil, err
		}
		roots = append(roots, filepath.Join(node, protectedRoot.Name()))
		batchEntries, err := os.ReadDir(rootPath)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, batchEntry := range batchEntries {
			batchRelative := filepath.Join(node, protectedRoot.Name(), batchEntry.Name())
			batchPath := filepath.Join(sourceRoot, batchRelative)
			if err := requireRealUserBackupDirectory(batchPath); err != nil {
				return nil, nil, nil, err
			}
			manifestPath := filepath.Join(batchPath, "manifest.json")
			manifestInfo, err := os.Lstat(manifestPath)
			if os.IsNotExist(err) {
				if err := validateCanonicalInProgressBatch(batchPath, batchEntry.Name()); err != nil {
					return nil, nil, nil, fmt.Errorf("canonical no-manifest batch %q is not a valid writer-owned in-progress batch: %w", batchRelative, err)
				}
				inProgress = append(inProgress, batchRelative)
				continue
			}
			if err != nil {
				return nil, nil, nil, err
			}
			if strings.HasPrefix(batchEntry.Name(), ".") {
				return nil, nil, nil, fmt.Errorf("hidden canonical batch %q cannot be committed", batchRelative)
			}
			if !manifestInfo.Mode().IsRegular() {
				return nil, nil, nil, fmt.Errorf("canonical batch manifest %q must be a no-follow regular file", filepath.Join(batchRelative, "manifest.json"))
			}
			if manifestInfo.Size() > maxUserBackupCustodyManifestBytes {
				return nil, nil, nil, fmt.Errorf("canonical batch manifest %q exceeds the bounded size limit", filepath.Join(batchRelative, "manifest.json"))
			}
			fingerprint, err := fingerprintUserBackupRegularFile(manifestPath)
			if err != nil {
				return nil, nil, nil, err
			}
			batches = append(batches, userBackupsCanonicalBatch{
				RelativePath:   batchRelative,
				ManifestSHA256: fingerprint.SHA256,
				ManifestSize:   fingerprint.Size,
				ManifestMode:   fingerprint.Mode,
				ManifestMTime:  fingerprint.MTime,
			})
		}
	}
	return roots, batches, inProgress, nil
}

func validateCanonicalInProgressBatch(batchPath, batchName string) error {
	if identity, ok := privateBackupTempIdentity(batchName); ok {
		return validateInProgressPrivateArtifactDirectory(batchPath, batchName, identity, true)
	}
	if err := ids.Validate(ids.LocalBackupBatchPrefix, batchName); err != nil {
		return fmt.Errorf("batch identity is not producer-owned: %w", err)
	}
	entries, err := os.ReadDir(batchPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath := filepath.Join(batchPath, entry.Name())
		switch entry.Name() {
		case "payload":
			if err := requireRealUserBackupDirectory(entryPath); err != nil {
				return err
			}
			if err := validateInProgressPayloadTree(entryPath); err != nil {
				return err
			}
		case "artifacts":
			if err := requireRealUserBackupDirectory(entryPath); err != nil {
				return err
			}
			if err := validateInProgressPrivateArtifacts(entryPath); err != nil {
				return err
			}
		default:
			if !validCustodyManifestTempName(entry.Name()) {
				return fmt.Errorf("unexpected in-progress component %q", entry.Name())
			}
			info, err := os.Lstat(entryPath)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > maxUserBackupCustodyManifestBytes {
				return fmt.Errorf("in-progress custody manifest temp %q must be a bounded no-follow regular file", entry.Name())
			}
		}
	}
	return nil
}

func validCustodyManifestTempName(name string) bool {
	const prefix = ".manifest.tmp-"
	encoded := strings.TrimPrefix(name, prefix)
	if encoded == name || len(encoded) != 16 {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func validateInProgressPayloadTree(root string) error {
	return filepath.WalkDir(root, func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if pathValue == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("in-progress payload component %q must be a real directory or regular file", pathValue)
		}
		return nil
	})
}

func validateInProgressPrivateArtifacts(root string) error {
	operations, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		name := operation.Name()
		identity := name
		temporary := false
		if value, ok := privateBackupTempIdentity(name); ok {
			identity = value
			temporary = true
		}
		if err := ids.Validate(ids.PrivateBackupPrefix, identity); err != nil {
			return fmt.Errorf("private backup artifact %q is outside the writer-owned namespace: %w", name, err)
		}
		operationPath := filepath.Join(root, name)
		if err := validateInProgressPrivateArtifactDirectory(operationPath, name, identity, temporary); err != nil {
			return err
		}
	}
	return nil
}

func privateBackupTempIdentity(name string) (string, bool) {
	if !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".tmp") {
		return "", false
	}
	identity := strings.TrimSuffix(strings.TrimPrefix(name, "."), ".tmp")
	if err := ids.Validate(ids.PrivateBackupPrefix, identity); err != nil {
		return "", false
	}
	return identity, true
}

func validateInProgressPrivateArtifactDirectory(operationPath, displayName, identity string, temporary bool) error {
	if err := ids.Validate(ids.PrivateBackupPrefix, identity); err != nil {
		return fmt.Errorf("private backup artifact %q is outside the writer-owned namespace: %w", displayName, err)
	}
	if err := requireRealUserBackupDirectory(operationPath); err != nil {
		return err
	}
	files, err := os.ReadDir(operationPath)
	if err != nil {
		return err
	}
	if !temporary && len(files) != 2 {
		return fmt.Errorf("published in-progress private backup artifact %q must contain payload.tar and manifest.json", displayName)
	}
	seen := map[string]bool{}
	for _, fileEntry := range files {
		if fileEntry.Name() != "payload.tar" && fileEntry.Name() != "manifest.json" {
			return fmt.Errorf("private backup artifact %q has unexpected component %q", displayName, fileEntry.Name())
		}
		info, err := os.Lstat(filepath.Join(operationPath, fileEntry.Name()))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("private backup artifact component %q must be a no-follow regular file", filepath.Join(displayName, fileEntry.Name()))
		}
		seen[fileEntry.Name()] = true
	}
	if !temporary && (!seen["payload.tar"] || !seen["manifest.json"]) {
		return fmt.Errorf("published in-progress private backup artifact %q is incomplete", displayName)
	}
	return nil
}

func classifyLegacyPrivateBackupNode(sourceRoot, node string) ([]userBackupsLegacyPayload, error) {
	if !strings.HasPrefix(node, "node_") || strings.TrimPrefix(node, "node_") == "" {
		return nil, fmt.Errorf("legacy node name is invalid")
	}
	nodeRoot := filepath.Join(sourceRoot, node)
	operations, err := os.ReadDir(nodeRoot)
	if err != nil {
		return nil, err
	}
	if len(operations) == 0 {
		return nil, fmt.Errorf("legacy node has no operations")
	}
	payloads := make([]userBackupsLegacyPayload, 0, len(operations))
	for _, operation := range operations {
		if !strings.HasPrefix(operation.Name(), "private_backup_") || strings.TrimPrefix(operation.Name(), "private_backup_") == "" {
			return nil, fmt.Errorf("legacy operation %q has an invalid name", filepath.Join(node, operation.Name()))
		}
		operationRoot := filepath.Join(nodeRoot, operation.Name())
		if err := requireRealUserBackupDirectory(operationRoot); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(operationRoot)
		if err != nil {
			return nil, err
		}
		if len(entries) != 1 || entries[0].Name() != "payload.tar" {
			return nil, fmt.Errorf("legacy operation %q must contain only payload.tar", filepath.Join(node, operation.Name()))
		}
		relative := filepath.Join(node, operation.Name(), "payload.tar")
		fingerprint, err := fingerprintUserBackupRegularFile(filepath.Join(sourceRoot, relative))
		if err != nil {
			return nil, err
		}
		if fingerprint.Size <= 0 {
			return nil, fmt.Errorf("legacy payload %q must be non-empty", relative)
		}
		payloads = append(payloads, userBackupsLegacyPayload{
			RelativePath: relative,
			SHA256:       fingerprint.SHA256,
			Size:         fingerprint.Size,
			Mode:         fingerprint.Mode,
			MTime:        fingerprint.MTime,
		})
	}
	return payloads, nil
}

type userBackupsFileFingerprint struct {
	SHA256 string
	Size   int64
	Mode   os.FileMode
	MTime  int64
}

func fingerprintUserBackupRegularFile(pathValue string) (userBackupsFileFingerprint, error) {
	before, err := os.Lstat(pathValue)
	if err != nil {
		return userBackupsFileFingerprint{}, err
	}
	if !before.Mode().IsRegular() {
		return userBackupsFileFingerprint{}, fmt.Errorf("user backup custody file %q must be a no-follow regular file", pathValue)
	}
	fd, err := unix.Open(pathValue, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return userBackupsFileFingerprint{}, err
	}
	file := os.NewFile(uintptr(fd), pathValue)
	if file == nil {
		_ = unix.Close(fd)
		return userBackupsFileFingerprint{}, fmt.Errorf("open user backup custody file %q", pathValue)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return userBackupsFileFingerprint{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return userBackupsFileFingerprint{}, fmt.Errorf("user backup custody file %q changed before hashing", pathValue)
	}
	hasher := sha256.New()
	bytesRead, err := io.Copy(hasher, file)
	if err != nil {
		return userBackupsFileFingerprint{}, err
	}
	afterOpened, err := file.Stat()
	if err != nil {
		return userBackupsFileFingerprint{}, err
	}
	afterPath, err := os.Lstat(pathValue)
	if err != nil {
		return userBackupsFileFingerprint{}, err
	}
	if !afterPath.Mode().IsRegular() || !os.SameFile(opened, afterOpened) || !os.SameFile(afterOpened, afterPath) ||
		bytesRead != opened.Size() || afterOpened.Size() != opened.Size() || afterOpened.Mode() != opened.Mode() || !afterOpened.ModTime().Equal(opened.ModTime()) {
		return userBackupsFileFingerprint{}, fmt.Errorf("user backup custody file %q changed while hashing", pathValue)
	}
	return userBackupsFileFingerprint{
		SHA256: hex.EncodeToString(hasher.Sum(nil)),
		Size:   opened.Size(),
		Mode:   opened.Mode(),
		MTime:  opened.ModTime().UnixNano(),
	}, nil
}

func requireRealUserBackupDirectory(pathValue string) error {
	info, err := os.Lstat(pathValue)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("user backup custody component %q must be a real directory", pathValue)
	}
	return nil
}

func copyUserBackupsSnapshot(sourceRoot, canonicalDestination, legacyDestination string, plan userBackupsSnapshotPlan) (copySummary, copySummary, error) {
	canonicalSummary := copySummary{SourceExists: plan.SourceExists}
	legacySummary := copySummary{SourceExists: plan.SourceExists}
	if plan.includesCanonical() {
		var err error
		canonicalSummary, err = copyCanonicalUserBackupsPlan(sourceRoot, canonicalDestination, plan)
		if err != nil {
			return copySummary{}, copySummary{}, err
		}
	}
	if plan.includesLegacy() {
		var err error
		legacySummary, err = copyLegacyPrivateBackupsPlan(sourceRoot, legacyDestination, plan)
		if err != nil {
			return copySummary{}, copySummary{}, err
		}
	}
	current, err := classifyUserBackupsSnapshot(sourceRoot, plan.Layout)
	if err != nil {
		return copySummary{}, copySummary{}, fmt.Errorf("reclassify user backup custody after copy: %w", err)
	}
	if !equalUserBackupsSnapshotPlans(plan, current) {
		return copySummary{}, copySummary{}, fmt.Errorf("user backup custody classification changed during copy")
	}
	return canonicalSummary, legacySummary, nil
}

func copyCanonicalUserBackupsPlan(sourceRoot, destinationRoot string, plan userBackupsSnapshotPlan) (copySummary, error) {
	if err := os.MkdirAll(destinationRoot, 0o750); err != nil {
		return copySummary{}, err
	}
	summary := copySummary{SourceExists: plan.SourceExists}
	for _, batch := range plan.CanonicalBatches {
		manifestPath := filepath.Join(sourceRoot, batch.RelativePath, "manifest.json")
		if err := requireCanonicalBatchFingerprint(manifestPath, batch); err != nil {
			return copySummary{}, err
		}
		copied, err := copyDirIfExists(filepath.Join(sourceRoot, batch.RelativePath), filepath.Join(destinationRoot, batch.RelativePath))
		if err != nil {
			return copySummary{}, err
		}
		if err := requireCanonicalBatchFingerprint(manifestPath, batch); err != nil {
			return copySummary{}, fmt.Errorf("canonical batch changed during copy: %w", err)
		}
		destinationFingerprint, err := fingerprintUserBackupRegularFile(filepath.Join(destinationRoot, batch.RelativePath, "manifest.json"))
		if err != nil {
			return copySummary{}, err
		}
		if destinationFingerprint.SHA256 != batch.ManifestSHA256 || destinationFingerprint.Size != batch.ManifestSize {
			return copySummary{}, fmt.Errorf("copied canonical batch %q manifest differs from reviewed source", batch.RelativePath)
		}
		summary.FileCount += copied.FileCount
		summary.TotalBytes += copied.TotalBytes
	}
	if err := maintenance.ValidateUserBackupsCustody(destinationRoot); err != nil {
		return copySummary{}, fmt.Errorf("copied canonical user backups are invalid: %w", err)
	}
	return summary, nil
}

func requireCanonicalBatchFingerprint(pathValue string, expected userBackupsCanonicalBatch) error {
	actual, err := fingerprintUserBackupRegularFile(pathValue)
	if err != nil {
		return err
	}
	if actual.SHA256 != expected.ManifestSHA256 || actual.Size != expected.ManifestSize || actual.Mode != expected.ManifestMode || actual.MTime != expected.ManifestMTime {
		return fmt.Errorf("canonical batch manifest %q changed after classification", pathValue)
	}
	return nil
}

func copyLegacyPrivateBackupsPlan(sourceRoot, destinationRoot string, plan userBackupsSnapshotPlan) (copySummary, error) {
	if err := os.MkdirAll(destinationRoot, 0o750); err != nil {
		return copySummary{}, err
	}
	summary := copySummary{SourceExists: plan.SourceExists}
	for _, payload := range plan.LegacyPayloads {
		sourcePath := filepath.Join(sourceRoot, payload.RelativePath)
		if err := requireLegacyPayloadFingerprint(sourcePath, payload); err != nil {
			return copySummary{}, err
		}
		destinationPath := filepath.Join(destinationRoot, payload.RelativePath)
		if err := copyFile(sourcePath, destinationPath, backupFileMode(payload.Mode.Perm())); err != nil {
			return copySummary{}, err
		}
		if err := requireLegacyPayloadFingerprint(sourcePath, payload); err != nil {
			return copySummary{}, fmt.Errorf("legacy payload changed during copy: %w", err)
		}
		copied, err := fingerprintUserBackupRegularFile(destinationPath)
		if err != nil {
			return copySummary{}, err
		}
		if copied.SHA256 != payload.SHA256 || copied.Size != payload.Size {
			return copySummary{}, fmt.Errorf("copied legacy payload %q differs from reviewed source", payload.RelativePath)
		}
		summary.FileCount++
		summary.TotalBytes += payload.Size
	}
	if err := maintenance.ValidateLegacyPrivateBackupsCustody(destinationRoot); err != nil {
		return copySummary{}, fmt.Errorf("copied legacy private backups are invalid: %w", err)
	}
	return summary, nil
}

func requireLegacyPayloadFingerprint(pathValue string, expected userBackupsLegacyPayload) error {
	actual, err := fingerprintUserBackupRegularFile(pathValue)
	if err != nil {
		return err
	}
	if actual.SHA256 != expected.SHA256 || actual.Size != expected.Size || actual.Mode != expected.Mode || actual.MTime != expected.MTime {
		return fmt.Errorf("legacy payload %q changed after classification", pathValue)
	}
	return nil
}

func equalUserBackupsSnapshotPlans(left, right userBackupsSnapshotPlan) bool {
	if left.SourceExists != right.SourceExists || left.Layout != right.Layout ||
		len(left.CanonicalNodes) != len(right.CanonicalNodes) || len(left.CanonicalRoots) != len(right.CanonicalRoots) || len(left.CanonicalBatches) != len(right.CanonicalBatches) ||
		len(left.InProgressBatches) != len(right.InProgressBatches) || len(left.LegacyNodes) != len(right.LegacyNodes) ||
		len(left.LegacyPayloads) != len(right.LegacyPayloads) {
		return false
	}
	for index := range left.CanonicalNodes {
		if left.CanonicalNodes[index] != right.CanonicalNodes[index] {
			return false
		}
	}
	for index := range left.CanonicalBatches {
		if left.CanonicalBatches[index] != right.CanonicalBatches[index] {
			return false
		}
	}
	for index := range left.CanonicalRoots {
		if left.CanonicalRoots[index] != right.CanonicalRoots[index] {
			return false
		}
	}
	for index := range left.InProgressBatches {
		if left.InProgressBatches[index] != right.InProgressBatches[index] {
			return false
		}
	}
	for index := range left.LegacyNodes {
		if left.LegacyNodes[index] != right.LegacyNodes[index] {
			return false
		}
	}
	for index := range left.LegacyPayloads {
		if left.LegacyPayloads[index] != right.LegacyPayloads[index] {
			return false
		}
	}
	return true
}

func copyCommittedUserBackupBatches(sourceRoot, destinationRoot string) (copySummary, error) {
	plan, err := classifyUserBackupsSnapshot(sourceRoot, userBackupsSnapshotCanonical)
	if err != nil {
		return copySummary{}, err
	}
	if plan.includesLegacy() {
		return copySummary{}, fmt.Errorf("canonical-only user backup copy encountered legacy custody")
	}
	canonical, _, err := copyUserBackupsSnapshot(sourceRoot, destinationRoot, filepath.Join(filepath.Dir(destinationRoot), "unused-private-backups"), plan)
	return canonical, err
}

func copyLegacyPrivateBackups(sourceRoot, destinationRoot string) (copySummary, error) {
	plan, err := classifyUserBackupsSnapshot(sourceRoot, userBackupsSnapshotLegacy)
	if err != nil {
		return copySummary{}, err
	}
	if plan.includesCanonical() {
		return copySummary{}, fmt.Errorf("legacy-only private backup copy encountered canonical custody")
	}
	_, legacy, err := copyUserBackupsSnapshot(sourceRoot, filepath.Join(filepath.Dir(destinationRoot), "unused-user-backups"), destinationRoot, plan)
	return legacy, err
}

func copyCommittedStorageArchives(sourceRoot, destinationRoot string) (copySummary, error) {
	sourceExists, err := prepareCommittedCopyRoots(sourceRoot, destinationRoot)
	if err != nil || !sourceExists {
		return copySummary{SourceExists: sourceExists}, err
	}
	summary := copySummary{SourceExists: true}
	children, err := realCustodyDirectories(sourceRoot)
	if err != nil {
		return copySummary{}, err
	}
	childSet := make(map[string]struct{}, len(children))
	for _, child := range children {
		childSet[child] = struct{}{}
	}
	// Preserve explicit pre-v0.7 archive compatibility when the legacy global
	// objects/manifests pair is complete.
	_, legacyObjects := childSet["objects"]
	_, legacyManifests := childSet["manifests"]
	if legacyObjects != legacyManifests {
		return copySummary{}, fmt.Errorf("legacy archive custody requires both objects and manifests")
	}
	if legacyObjects {
		for _, legacy := range []string{"objects", "manifests"} {
			copied, err := copyDirIfExists(filepath.Join(sourceRoot, legacy), filepath.Join(destinationRoot, legacy))
			if err != nil {
				return copySummary{}, err
			}
			summary.FileCount += copied.FileCount
			summary.TotalBytes += copied.TotalBytes
		}
	}
	for _, archiveKey := range children {
		if archiveKey == "objects" || archiveKey == "manifests" {
			continue
		}
		keyPath := filepath.Join(sourceRoot, archiveKey)
		committed, err := hasRegularCommitManifest(keyPath)
		if err != nil {
			return copySummary{}, err
		}
		if !committed {
			continue
		}
		copied, err := copyDirIfExists(keyPath, filepath.Join(destinationRoot, archiveKey))
		if err != nil {
			return copySummary{}, err
		}
		summary.FileCount += copied.FileCount
		summary.TotalBytes += copied.TotalBytes
	}
	return summary, nil
}

func prepareCommittedCopyRoots(sourceRoot, destinationRoot string) (bool, error) {
	sourceRoot = filepath.Clean(strings.TrimSpace(sourceRoot))
	destinationRoot = filepath.Clean(strings.TrimSpace(destinationRoot))
	if err := os.MkdirAll(destinationRoot, 0o750); err != nil {
		return false, err
	}
	if sourceRoot == "." || sourceRoot == "" {
		return false, nil
	}
	info, err := os.Lstat(sourceRoot)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("custody source %s is not a real directory", sourceRoot)
	}
	if pathWithinRuntime(sourceRoot, destinationRoot) {
		return false, fmt.Errorf("destination must not be inside source")
	}
	return true, nil
}

func realCustodyDirectories(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("custody component %s is a symlink", filepath.Join(root, entry.Name()))
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("custody component %s is a symlink", filepath.Join(root, entry.Name()))
		}
		if !info.IsDir() {
			continue
		}
		result = append(result, entry.Name())
	}
	return result, nil
}

func hasRegularCommitManifest(directory string) (bool, error) {
	info, err := os.Lstat(filepath.Join(directory, "manifest.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("custody commit marker %s must be a regular file", filepath.Join(directory, "manifest.json"))
	}
	return true, nil
}

func ensureStorageArchiveBackupLayout(destinationRoot string) error {
	return os.MkdirAll(destinationRoot, 0o750)
}

func backupDirectoryMode(mode os.FileMode) os.FileMode {
	mode = mode.Perm() | 0o750
	return mode & 0o770
}

func backupFileMode(mode os.FileMode) os.FileMode {
	normalized := mode.Perm() | 0o640
	if normalized&0o100 != 0 {
		normalized |= 0o010
	}
	return normalized & 0o770
}

func currentNixGenerationPath() string {
	value, err := os.Readlink("/run/current-system")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func copyFile(source, destination string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return nil
}

func pathWithinRuntime(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o640)
}

func artifactMetadata(relativePath, backupDir string) json.RawMessage {
	return mustWorkerJSON(map[string]any{
		"schema_version": "maintenance_artifact.metadata.v0.2",
		"relative_path":  relativePath,
		"backup_dir":     backupDir,
	})
}

func copyArtifactMetadata(relativePath, backupDir string, summary copySummary) json.RawMessage {
	return mustWorkerJSON(map[string]any{
		"schema_version":           "maintenance_artifact.metadata.v0.2",
		"relative_path":            relativePath,
		"backup_dir":               backupDir,
		"source_exists":            summary.SourceExists,
		"file_count":               summary.FileCount,
		"total_bytes":              summary.TotalBytes,
		"shared_object_link_count": summary.SharedObjectLinkCount,
	})
}

func copyArtifactMetadataWithSource(relativePath, backupDir, sourceRoot string, summary copySummary) json.RawMessage {
	return mustWorkerJSON(map[string]any{
		"schema_version":           "maintenance_artifact.metadata.v0.2",
		"relative_path":            relativePath,
		"backup_dir":               backupDir,
		"source_root":              filepath.Clean(strings.TrimSpace(sourceRoot)),
		"source_exists":            summary.SourceExists,
		"file_count":               summary.FileCount,
		"total_bytes":              summary.TotalBytes,
		"shared_object_link_count": summary.SharedObjectLinkCount,
	})
}

func fileURI(path string) string {
	return "file://" + filepath.Clean(path)
}

func int64Ptr(value int64) *int64 {
	out := value
	return &out
}

func errorObject(code string, err error, backupDir string) json.RawMessage {
	return mustWorkerJSON(map[string]any{
		"schema_version": "main_backup.error.v0.2",
		"code":           code,
		"message":        err.Error(),
		"backup_dir":     backupDir,
	})
}

func mustWorkerJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (r MainBackupRuntime) checkHermesRecovery(ctx context.Context, now time.Time) ([]hermesprofile.Evidence, error) {
	var policy hermesprofile.Policy
	if r.hermesPolicy != nil {
		policy = *r.hermesPolicy
	} else {
		var err error
		policy, err = config.LoadHermesRecoveryPolicy()
		if err != nil {
			return nil, err
		}
	}
	return hermesprofile.Check(ctx, policy, now)
}

func (r MainBackupRuntime) revalidateHermesRecovery(ctx context.Context, original []hermesprofile.Evidence) error {
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	current, err := r.checkHermesRecovery(ctx, now)
	a, _ := json.Marshal(original)
	b, _ := json.Marshal(current)
	if err != nil || !bytes.Equal(a, b) {
		return fmt.Errorf("Hermes recovery evidence changed or expired during Main backup")
	}
	return nil
}
