package runtimes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/workers"
)

const (
	cloudSnapshotUploadConfigSchemaV063  = "cloud_snapshot_upload.config.v0.6.3"
	cloudSnapshotUploadConfigSchemaV064  = "cloud_snapshot_upload.config.v0.6.4"
	cloudSnapshotUploadConfigSchema      = "cloud_snapshot_upload.config.v1"
	cloudSnapshotUploadCheckpointSchema  = "cloud_snapshot_upload.checkpoint.v1"
	cloudSnapshotUploadResultSchema      = "cloud_snapshot_upload.result.v1"
	cloudSnapshotUploadPhase             = "direct_borg_archive"
	cloudSnapshotCompletionTimeout       = 15 * time.Second
	cloudSnapshotUploadRunTimeoutSeconds = 7200
	cloudSnapshotUploadMaxRuntimeMS      = 7200000
	cloudSnapshotScheduleTimezone        = "Europe/Amsterdam"
	cloudSnapshotPackageWindowHour       = 3
)

type OperationalPackageReference struct {
	BackupOperationID        string
	PackageDir               string
	PackageID                string
	ManifestSHA256           string
	SchemaHead               int64
	CreatedAt                time.Time
	ProvenancePackageDir     string
	ProvenancePackageID      string
	ProvenanceManifestSHA256 string
	ProvenanceSchemaHead     int
	ProvenanceGraphDigest    string
	ProvenanceCompletedAt    time.Time
	ProvenanceDumpSizeBytes  int64
}

type CloudSnapshotUploadRuntime struct {
	Maintenance            maintenance.Service
	DataDir                string
	NodeID                 string
	CloudConfigPath        string
	CoverageOptions        backupcoverage.Options
	Driver                 cloudstorage.Driver // v0.9 reader/test compatibility; new writes use Borg directly
	Now                    func() time.Time
	ResolvePackage         func(context.Context, string) (OperationalPackageReference, error)
	ResolveBackupOperation func(context.Context, string) (maintenance.BackupOperation, error)
	ArchiveCanonical       func(context.Context, cloudstorage.DirectArchiveInput) (cloudstorage.DirectArchiveResult, error)
	agentsRoot             string
}

type cloudSnapshotUploadConfig struct {
	SchemaVersion   string `json:"schema_version"`
	CloudConfigPath string `json:"cloud_config_path"`
	NodeID          string `json:"node_id"`
	MaxRuntimeMS    int    `json:"max_runtime_ms"`
	BackupRoot      string `json:"backup_root,omitempty"` // v0.9 decode-only
	DataDir         string `json:"data_dir,omitempty"`    // v0.9 decode-only
	SkipIfUploaded  bool   `json:"skip_if_uploaded,omitempty"`
}

func NewCloudSnapshotUploadRuntime(maintenanceService maintenance.Service, dataDir, nodeID string, coverageOptions backupcoverage.Options) CloudSnapshotUploadRuntime {
	return CloudSnapshotUploadRuntime{Maintenance: maintenanceService, DataDir: dataDir, NodeID: nodeID, CloudConfigPath: cloudstorage.DefaultConfigPath, CoverageOptions: coverageOptions, agentsRoot: "/srv/loom/agents"}
}

func (r CloudSnapshotUploadRuntime) Kind() string { return workers.KindCloudSnapshotUpload }

func (r CloudSnapshotUploadRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind: workers.KindCloudSnapshotUpload, DisplayName: "Cloud snapshot upload",
		Description:  "Archives configured canonical roots and the verified operational package directly into Borg history.",
		RuntimeOwner: workers.RuntimeOwnerLoomd, RuntimePackage: "loom.core.cloudstorage", Status: workers.KindStatusActive,
		SupportedLocalities: []string{workers.LocalityMainOwned}, MayTouchFilesystem: true,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     cloudSnapshotUploadTimeoutPolicy(),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_heavy"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"cloud_snapshot_upload.config_schema.v1","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"cloud_snapshot_upload.checkpoint_schema.v1","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"cloud_snapshot_upload.result_schema.v1","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r CloudSnapshotUploadRuntime) DefaultConfig() json.RawMessage {
	return mustWorkerJSON(r.defaultConfig())
}

func (r CloudSnapshotUploadRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{{
		WorkerKey: "main.cloud_snapshot_upload", WorkerKind: workers.KindCloudSnapshotUpload,
		DisplayName: "Cloud snapshot upload", Description: "Archives canonical roots directly into Borg history.",
		Locality: workers.LocalityMainOwned, ConfigJSON: r.DefaultConfig(),
		TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
		ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		TimeoutPolicyJSON:  cloudSnapshotUploadTimeoutPolicy(),
		ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_heavy"}`),
		VisibilityJSON:     json.RawMessage(`{}`), Metadata: json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
	}}
}

func cloudSnapshotUploadTimeoutPolicy() json.RawMessage {
	return mustWorkerJSON(map[string]any{
		"schema_version":      "worker_timeout_policy.v0.2",
		"run_timeout_seconds": cloudSnapshotUploadRunTimeoutSeconds,
	})
}

func (r CloudSnapshotUploadRuntime) ValidateConfig(_ context.Context, raw json.RawMessage) error {
	_, err := r.parseConfig(raw)
	return err
}

func (r CloudSnapshotUploadRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	cfg, err := r.parseConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	requestedBackupOperationID, err := cloudSnapshotBackupOperationSelector(run.Run.Metadata)
	if err != nil {
		return workers.RunResult{}, err
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	scheduledDaily := run.Run.TriggerKind == workers.TriggerSupervisorTick && tickPolicy.Mode == workers.TickModeDailyLocal
	nextRunAfter := tickPolicy.NextAfter(now)
	load, err := cloudstorage.LoadConfig(cfg.CloudConfigPath)
	if err != nil {
		return workers.RunResult{}, err
	}
	if !load.Config.Enabled {
		return r.skippedResult(run, now, nextRunAfter, "cloud_disabled", "Cloud storage is disabled."), nil
	}
	if load.Config.Snapshots.Backend != cloudstorage.SnapshotBackendBorg {
		return workers.RunResult{}, fmt.Errorf("cloud snapshot producer requires the configured Borg backend")
	}
	if remoteState, stateErr := cloudstorage.LoadRemoteState(load.Config); stateErr == nil && !cloudstorage.CanLiveProbe(remoteState, now) {
		return r.skippedResult(run, now, nextRunAfter, "cloud_cooling_down", cloudCooldownReason(remoteState)), nil
	}
	packageRef, err := r.resolveOperationalPackage(ctx, requestedBackupOperationID)
	if err != nil {
		if requestedBackupOperationID == "" && errors.Is(err, maintenance.ErrNotFound) && !scheduledDaily {
			return r.skippedResult(run, now, nextRunAfter, "no_operational_package", "No verified operational package is available."), nil
		}
		if scheduledDaily && errors.Is(err, maintenance.ErrNotFound) {
			return workers.RunResult{}, fmt.Errorf("scheduled daily cloud snapshot requires a fresh same-day recovery package: %w", err)
		}
		return workers.RunResult{}, err
	}
	if !validCloudRecoverySHA256(packageRef.ManifestSHA256) {
		return workers.RunResult{}, fmt.Errorf("operational package manifest identity is invalid")
	}
	if !validCloudRecoverySHA256(packageRef.ProvenanceManifestSHA256) || packageRef.ProvenanceManifestSHA256 == packageRef.ManifestSHA256 || packageRef.ProvenancePackageDir == "" || packageRef.ProvenancePackageID == "" || packageRef.ProvenanceSchemaHead != provenance.SchemaHead || !validCloudRecoveryGraphDigest(packageRef.ProvenanceGraphDigest) || packageRef.ProvenanceCompletedAt.IsZero() || packageRef.ProvenanceCompletedAt.Location() != time.UTC || packageRef.ProvenanceDumpSizeBytes <= 0 {
		return workers.RunResult{}, fmt.Errorf("provenance package manifest identity is invalid")
	}
	if err := validateScheduledCloudRecoveryPackage(run, tickPolicy, packageRef); err != nil {
		return workers.RunResult{}, err
	}
	roots, exclusions, err := r.directArchiveRoots()
	if err != nil {
		return workers.RunResult{}, err
	}
	request := backupstrategy.DirectArchiveRequestV2{
		Schema:              backupstrategy.DirectArchiveRequestSchemaV2,
		VerificationProfile: backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1,
		NodeID:              cfg.NodeID, ArchiveClass: backupstrategy.DirectArchiveClassUserData,
		ArchiveRef: recoveryArchiveRef(packageRef.ManifestSHA256, packageRef.ProvenanceManifestSHA256), CreatedAt: packageRef.CreatedAt.UTC(), Roots: roots, Exclusions: exclusions,
		OperationalPackage: backupstrategy.DirectArchiveOperationalPackage{Path: packageRef.PackageDir, ManifestSHA256: packageRef.ManifestSHA256, PackageID: packageRef.PackageID, ExpectedSchemaHead: &packageRef.SchemaHead},
		ProvenancePackage: backupstrategy.DirectArchiveProvenancePackage{
			Path: packageRef.ProvenancePackageDir, ManifestSHA256: packageRef.ProvenanceManifestSHA256, PackageID: packageRef.ProvenancePackageID,
			Verify: func(ctx context.Context, packageDir, expectedManifestSHA256 string) (backupstrategy.DirectArchiveProvenanceVerification, error) {
				verification, err := maintenance.VerifyProvenanceBackupPackage(ctx, packageDir, expectedManifestSHA256)
				if err != nil {
					return backupstrategy.DirectArchiveProvenanceVerification{}, err
				}
				return backupstrategy.DirectArchiveProvenanceVerification{
					ManifestSHA256: verification.ManifestSHA256, SchemaHead: verification.SchemaHead, GraphDigest: verification.GraphDigest,
					CompletedAt: verification.CompletedAt, DumpSizeBytes: verification.DumpSizeBytes,
				}, nil
			},
		},
	}
	operation, err := r.createOperation(ctx, run, cfg)
	if err != nil {
		return workers.RunResult{}, err
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if cfg.MaxRuntimeMS > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(cfg.MaxRuntimeMS)*time.Millisecond)
		defer cancel()
	}
	archive := r.ArchiveCanonical
	if archive == nil {
		archive = cloudstorage.ArchiveCanonicalRoots
	}
	directResult, archiveErr := archive(runCtx, cloudstorage.DirectArchiveInput{Config: load.Config, RequestV2: &request})
	resultSummary := cloudDirectArchiveSummary(directResult, packageRef, requestedBackupOperationID, run, operation)
	validV2Evidence := directResult.ManifestSchema == backupstrategy.DirectArchiveManifestSchemaV2 && directResult.VerificationProfile == backupstrategy.DirectArchiveVerificationProfileRoutineIncrementalV1
	validV1ReplayEvidence := directResult.ManifestSchema == backupstrategy.DirectArchiveManifestSchema && directResult.VerificationProfile == "" && (directResult.Idempotent || directResult.ResumedPending)
	if archiveErr == nil && ((!validV2Evidence && !validV1ReplayEvidence) ||
		directResult.OperationalPackageID != packageRef.PackageID || directResult.OperationalPackageManifestSHA256 != packageRef.ManifestSHA256 ||
		directResult.ProvenancePackageID != packageRef.ProvenancePackageID || directResult.ProvenancePackageManifestSHA256 != packageRef.ProvenanceManifestSHA256) {
		archiveErr = fmt.Errorf("direct Borg archive returned incomplete or mismatched v2 evidence")
	}
	if archiveErr != nil || directResult.Status != cloudstorage.DirectArchiveStatusSucceeded || !directResult.Committed {
		if archiveErr == nil {
			archiveErr = fmt.Errorf("direct Borg archive did not commit: %s", directResult.Status)
		}
		_ = cloudstorage.RecordRemoteFailureAt(load.Config, archiveErr, now, "worker.cloud_snapshot_upload")
		r.completeOperationBestEffort(ctx, operation, maintenance.OperationFailed, resultSummary, archiveErr)
		return workers.RunResult{}, archiveErr
	}
	_ = recordCloudWorkerSuccess(load.Config, now)
	if err := r.completeSuccessfulOperation(ctx, operation, directResult, resultSummary, packageRef, requestedBackupOperationID); err != nil {
		r.completeOperationBestEffort(ctx, operation, maintenance.OperationFailed, resultSummary, fmt.Errorf("record committed direct archive evidence: %w", err))
		return workers.RunResult{}, err
	}
	checkpoint := mustWorkerJSON(map[string]any{
		"schema_version": cloudSnapshotUploadCheckpointSchema, "phase": cloudSnapshotUploadPhase, "phase_status": maintenance.OperationSucceeded,
		"committed": true, "idempotent": directResult.Idempotent, "last_archived_at": now.Format(time.RFC3339Nano), "last_run_id": run.Run.WorkerRunID,
		"archive_ref": directResult.ArchiveRef, "archive": directResult.Archive, "remote_uri": directResult.RemoteURI,
		"pending_archive": directResult.PendingArchive, "resumed_pending": directResult.ResumedPending, "verification_phase": directResult.VerificationPhase,
		"manifest_schema": directResult.ManifestSchema, "verification_profile": directResult.VerificationProfile,
		"manifest_sha256": directResult.ManifestSHA256, "stage_durations_ms": directResult.StageDurationsMS,
		"borg_command_counts": directResult.BorgCommandCounts, "operational_package_id": packageRef.PackageID,
		"operational_package_manifest_sha256": packageRef.ManifestSHA256, "complete_local_user_data_generation_created": false,
		"provenance_package_id": packageRef.ProvenancePackageID, "provenance_manifest_sha256": packageRef.ProvenanceManifestSHA256,
		"requested_backup_operation_id": requestedBackupOperationID, "selected_backup_operation_id": packageRef.BackupOperationID,
		"backup_operation_selector_matched": requestedBackupOperationID == "" || requestedBackupOperationID == packageRef.BackupOperationID,
	})
	return workers.RunResult{
		Status: workers.RunStatusSucceeded, ResultSummary: resultSummary,
		Counters:      map[string]int64{"archived": 1, "packed_bytes": directResult.PackedBytes, "deduplicated_bytes": directResult.DeduplicatedBytes},
		ResourceUsage: json.RawMessage(`{}`), NextRunAfter: nextRunAfter, Retryable: false,
		CheckpointUpdates: []workers.CheckpointUpdate{{Key: "default", SchemaVersion: cloudSnapshotUploadCheckpointSchema, Value: checkpoint, Metadata: json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`)}},
	}, nil
}

func validateScheduledCloudRecoveryPackage(run workers.RunContext, policy workers.TickPolicy, packageRef OperationalPackageReference) error {
	if run.Run.TriggerKind != workers.TriggerSupervisorTick || policy.Mode != workers.TickModeDailyLocal {
		return nil
	}
	runStart := run.StartedAt
	if runStart.IsZero() {
		runStart = run.Run.StartedAt
	}
	if runStart.IsZero() {
		return fmt.Errorf("scheduled daily cloud snapshot run start is unavailable")
	}
	location, err := time.LoadLocation(cloudSnapshotScheduleTimezone)
	if err != nil {
		return fmt.Errorf("load scheduled cloud recovery-package timezone: %w", err)
	}
	localStart := runStart.In(location)
	windowStart := time.Date(localStart.Year(), localStart.Month(), localStart.Day(), cloudSnapshotPackageWindowHour, 0, 0, 0, location)
	createdAt := packageRef.CreatedAt
	if createdAt.IsZero() || createdAt.Before(windowStart) || createdAt.After(runStart) {
		return fmt.Errorf("scheduled daily cloud snapshot recovery package created_at %s is outside the same-day %02d:00-to-run-start %s window", createdAt.UTC().Format(time.RFC3339Nano), cloudSnapshotPackageWindowHour, cloudSnapshotScheduleTimezone)
	}
	return nil
}

func recoveryArchiveRef(operationalManifestSHA256, provenanceManifestSHA256 string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(operationalManifestSHA256) + "\x00" + strings.TrimSpace(provenanceManifestSHA256)))
	return "history-" + hex.EncodeToString(digest[:12])
}

func validCloudRecoverySHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validCloudRecoveryGraphDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validCloudRecoverySHA256(strings.TrimPrefix(value, "sha256:"))
}

func cloudSnapshotBackupOperationSelector(raw json.RawMessage) (string, error) {
	trimmedEnvelope := strings.TrimSpace(string(raw))
	if len(trimmedEnvelope) == 0 {
		return "", nil
	}
	if !strings.HasPrefix(trimmedEnvelope, "{") {
		return "", fmt.Errorf("cloud snapshot worker metadata must be an object")
	}
	var envelope struct {
		SchemaVersion   string          `json:"schema_version"`
		RequestMetadata json.RawMessage `json:"request_metadata"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("cloud snapshot worker metadata is invalid: %w", err)
	}
	if envelope.SchemaVersion == "" && len(envelope.RequestMetadata) == 0 {
		return "", nil
	}
	if envelope.SchemaVersion != "worker_run.metadata.v0.2" {
		return "", fmt.Errorf("cloud snapshot worker metadata schema is invalid")
	}
	if len(envelope.RequestMetadata) == 0 {
		envelope.RequestMetadata = json.RawMessage(`{}`)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(envelope.RequestMetadata)), "{") {
		return "", fmt.Errorf("cloud snapshot request metadata must be an object")
	}
	var requestMetadata struct {
		BackupOperationID *string `json:"backup_operation_id"`
	}
	if err := json.Unmarshal(envelope.RequestMetadata, &requestMetadata); err != nil {
		return "", fmt.Errorf("cloud snapshot request metadata is invalid: %w", err)
	}
	if requestMetadata.BackupOperationID == nil {
		return "", nil
	}
	selected := *requestMetadata.BackupOperationID
	if selected == "" || selected != strings.TrimSpace(selected) || ids.Validate(ids.MaintenanceOperationPrefix, selected) != nil {
		return "", fmt.Errorf("cloud snapshot backup operation selector is invalid")
	}
	return selected, nil
}

type cloudRecoveryPackageResult struct {
	SchemaVersion            string `json:"schema_version"`
	Phase                    string `json:"phase"`
	Status                   string `json:"status"`
	Committed                bool   `json:"committed"`
	BackupOperationID        string `json:"backup_operation_id"`
	PackageDir               string `json:"package_dir"`
	PackageID                string `json:"package_id"`
	ManifestSHA256           string `json:"manifest_sha256"`
	SchemaHead               int64  `json:"schema_head"`
	ProvenancePackageDir     string `json:"provenance_package_dir"`
	ProvenancePackageID      string `json:"provenance_package_id"`
	ProvenanceManifestSHA256 string `json:"provenance_manifest_sha256"`
}

func selectCloudRecoveryPackage(backups []maintenance.BackupOperation, requestedBackupOperationID string) (maintenance.BackupOperation, cloudRecoveryPackageResult, error) {
	for _, backup := range backups {
		selected := requestedBackupOperationID != ""
		if selected && backup.Operation.MaintenanceOperationID != requestedBackupOperationID {
			continue
		}
		if selected && backup.Operation.OperationKind != maintenance.OperationKindMainBackup {
			return maintenance.BackupOperation{}, cloudRecoveryPackageResult{}, fmt.Errorf("selected backup operation is not a main backup")
		}
		if selected && backup.Operation.Status != maintenance.OperationSucceeded {
			return maintenance.BackupOperation{}, cloudRecoveryPackageResult{}, fmt.Errorf("selected backup operation did not succeed")
		}
		var result cloudRecoveryPackageResult
		valid := json.Unmarshal(backup.Operation.ResultJSON, &result) == nil &&
			result.SchemaVersion == mainBackupResultSchema && result.Phase == mainBackupPhase &&
			result.Status == maintenance.OperationSucceeded && result.Committed &&
			result.BackupOperationID == backup.Operation.MaintenanceOperationID
		if !valid {
			if selected {
				return maintenance.BackupOperation{}, cloudRecoveryPackageResult{}, fmt.Errorf("selected backup operation has no committed recovery_packages_v1 result")
			}
			continue
		}
		return backup, result, nil
	}
	if requestedBackupOperationID != "" {
		return maintenance.BackupOperation{}, cloudRecoveryPackageResult{}, fmt.Errorf("selected backup operation was not found")
	}
	return maintenance.BackupOperation{}, cloudRecoveryPackageResult{}, maintenance.ErrNotFound
}

func (r CloudSnapshotUploadRuntime) resolveOperationalPackage(ctx context.Context, requestedBackupOperationID string) (OperationalPackageReference, error) {
	if r.ResolvePackage != nil {
		resolved, err := r.ResolvePackage(ctx, requestedBackupOperationID)
		if err != nil {
			return OperationalPackageReference{}, err
		}
		if requestedBackupOperationID != "" && resolved.BackupOperationID != requestedBackupOperationID {
			return OperationalPackageReference{}, fmt.Errorf("resolved backup operation does not match the requested selector")
		}
		return resolved, nil
	}
	if r.Maintenance.DB == nil {
		return OperationalPackageReference{}, fmt.Errorf("cloud snapshot maintenance service is not configured")
	}
	var backups []maintenance.BackupOperation
	if requestedBackupOperationID != "" {
		lookup := r.ResolveBackupOperation
		if lookup == nil {
			lookup = r.Maintenance.GetBackupOperation
		}
		backup, err := lookup(ctx, requestedBackupOperationID)
		if err != nil {
			return OperationalPackageReference{}, err
		}
		backups = []maintenance.BackupOperation{backup}
	} else {
		var err error
		backups, err = r.Maintenance.ListBackups(ctx, maintenance.OperationFilter{Kind: maintenance.OperationKindMainBackup, Status: maintenance.OperationSucceeded, Limit: 100})
		if err != nil {
			return OperationalPackageReference{}, err
		}
	}
	backup, result, err := selectCloudRecoveryPackage(backups, requestedBackupOperationID)
	if err != nil {
		return OperationalPackageReference{}, err
	}
	{
		expectedHead := result.SchemaHead
		verification, verifyErr := backupstrategy.VerifyOperationalPackage(ctx, backupstrategy.OperationalPackageVerificationInput{PackageDir: result.PackageDir, ExpectedManifestSHA256: result.ManifestSHA256, ExpectedPackageID: result.PackageID, ExpectedSchemaHead: &expectedHead})
		if verifyErr != nil {
			return OperationalPackageReference{}, verifyErr
		}
		if verification.Status != maintenance.VerificationSucceeded {
			if requestedBackupOperationID != "" {
				return OperationalPackageReference{}, fmt.Errorf("selected operational package failed exact verification")
			}
			return OperationalPackageReference{}, fmt.Errorf("latest operational package failed exact verification")
		}
		provenanceVerification, verifyErr := maintenance.VerifyProvenanceBackupPackage(ctx, result.ProvenancePackageDir, result.ProvenanceManifestSHA256)
		if verifyErr != nil {
			return OperationalPackageReference{}, verifyErr
		}
		return OperationalPackageReference{
			BackupOperationID: backup.Operation.MaintenanceOperationID, PackageDir: verification.PackageDir, PackageID: verification.PackageID,
			ManifestSHA256: verification.ManifestSHA256, SchemaHead: verification.SchemaHead, CreatedAt: verification.CreatedAt,
			ProvenancePackageDir: result.ProvenancePackageDir, ProvenancePackageID: result.ProvenancePackageID,
			ProvenanceManifestSHA256: provenanceVerification.ManifestSHA256, ProvenanceSchemaHead: provenanceVerification.SchemaHead,
			ProvenanceGraphDigest: provenanceVerification.GraphDigest, ProvenanceCompletedAt: provenanceVerification.CompletedAt,
			ProvenanceDumpSizeBytes: provenanceVerification.DumpSizeBytes,
		}, nil
	}
}

func (r CloudSnapshotUploadRuntime) directArchiveRoots() ([]backupstrategy.DirectArchiveRoot, []backupstrategy.DirectArchiveExclusion, error) {
	mainBoxPath, err := cloudDirectArchiveDirectory("main_box", r.CoverageOptions.MainBoxPath, false)
	if err != nil {
		return nil, nil, err
	}
	boxProjectsPath := filepath.Join(mainBoxPath, "Projects")
	boxProjectsRelative, err := filepath.Rel(mainBoxPath, boxProjectsPath)
	if err != nil || boxProjectsRelative != "Projects" {
		return nil, nil, fmt.Errorf("configured canonical root box_projects must be exactly contained by main_box")
	}
	userBackupsPath := r.CoverageOptions.UserBackupsRoot
	if userBackupsPath == "" {
		userBackupsPath = r.CoverageOptions.PrivateBackupsRoot
	}
	agentsRoot := strings.TrimSpace(r.agentsRoot)
	if agentsRoot == "" {
		agentsRoot = "/srv/loom/agents"
	}
	candidates := []backupstrategy.DirectArchiveRoot{
		{Name: "object_store", Path: r.CoverageOptions.ObjectStoreRoot}, {Name: "imports", Path: r.CoverageOptions.ImportsRoot},
		{Name: "user_backups", Path: userBackupsPath},
		{Name: "main_documents", Path: r.CoverageOptions.MainDocumentsRoot}, {Name: "box_notes", Path: r.CoverageOptions.BoxNotesRoot},
		{Name: "box_projects", Path: boxProjectsPath},
		{Name: "box_topics", Path: filepath.Join(mainBoxPath, "Topics")},
		{Name: "box_library", Path: filepath.Join(mainBoxPath, "Library")},
		{Name: "storage_retention", Path: r.CoverageOptions.StorageRetentionRoot}, {Name: "storage_archive", Path: r.CoverageOptions.StorageArchiveRoot},
		{Name: "agents", Path: agentsRoot},
	}
	roots := make([]backupstrategy.DirectArchiveRoot, 0, len(candidates))
	if r.CoverageOptions.ApplicationDataRoot != "" {
		candidates = append(candidates, backupstrategy.DirectArchiveRoot{Name: "application_data", Path: r.CoverageOptions.ApplicationDataRoot})
	}
	resolvedPaths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		allowEmpty := candidate.Name == "box_topics" || candidate.Name == "box_library" || candidate.Name == "application_data"
		path, err := cloudDirectArchiveDirectory(candidate.Name, candidate.Path, allowEmpty)
		if err != nil {
			return nil, nil, err
		}
		candidate.Path = path
		roots = append(roots, candidate)
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve configured canonical root %s: %w", candidate.Name, err)
		}
		resolvedPaths = append(resolvedPaths, resolved)
	}
	for left := range roots {
		for right := left + 1; right < len(roots); right++ {
			if cloudDirectArchivePathsOverlap(roots[left].Path, roots[right].Path) || cloudDirectArchivePathsOverlap(resolvedPaths[left], resolvedPaths[right]) {
				return nil, nil, fmt.Errorf("configured canonical roots %s and %s overlap", roots[left].Name, roots[right].Name)
			}
		}
	}
	exclusions := []backupstrategy.DirectArchiveExclusion{}
	for _, root := range roots {
		if root.Name == "main_documents" {
			exclusions = append(exclusions, backupstrategy.DirectArchiveExclusion{Root: root.Name, RelativePath: ".loom-acceptance"})
		}
	}
	return roots, exclusions, nil
}

func cloudDirectArchiveDirectory(name, value string, allowEmpty bool) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", fmt.Errorf("configured canonical root %s must be an exact absolute path", name)
	}
	info, err := os.Lstat(value)
	if err != nil {
		return "", fmt.Errorf("configured canonical root %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("configured canonical root %s must be a real directory", name)
	}
	directory, err := os.Open(value)
	if err != nil {
		return "", fmt.Errorf("configured canonical root %s: %w", name, err)
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		return "", fmt.Errorf("configured canonical root %s changed while opening", name)
	}
	if _, err := directory.Readdirnames(1); errors.Is(err, io.EOF) {
		if !allowEmpty {
			return "", fmt.Errorf("configured canonical root %s must not be empty", name)
		}
	} else if err != nil {
		return "", fmt.Errorf("configured canonical root %s: %w", name, err)
	}
	return value, nil
}

func cloudDirectArchivePathsOverlap(left, right string) bool {
	if left == right {
		return true
	}
	separator := string(filepath.Separator)
	return strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}

func (r CloudSnapshotUploadRuntime) defaultConfig() cloudSnapshotUploadConfig {
	return cloudSnapshotUploadConfig{SchemaVersion: cloudSnapshotUploadConfigSchema, CloudConfigPath: cloudFirstNonEmpty(r.CloudConfigPath, cloudstorage.DefaultConfigPath), NodeID: cloudFirstNonEmpty(r.NodeID, "main"), MaxRuntimeMS: cloudSnapshotUploadMaxRuntimeMS}
}

func (r CloudSnapshotUploadRuntime) parseConfig(raw json.RawMessage) (cloudSnapshotUploadConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return cloudSnapshotUploadConfig{}, err
	}
	cfg := r.defaultConfig()
	if err := json.Unmarshal(normalized, &cfg); err != nil {
		return cfg, fmt.Errorf("%w: cloud_snapshot_upload config_json is invalid JSON: %w", workers.ErrInvalid, err)
	}
	cfg.SchemaVersion = strings.TrimSpace(cfg.SchemaVersion)
	switch cfg.SchemaVersion {
	case "", cloudSnapshotUploadConfigSchemaV063, cloudSnapshotUploadConfigSchemaV064, cloudSnapshotUploadConfigSchema:
		cfg.SchemaVersion = cloudSnapshotUploadConfigSchema
	default:
		return cfg, fmt.Errorf("%w: unsupported cloud_snapshot_upload schema_version %q", workers.ErrInvalid, cfg.SchemaVersion)
	}
	cfg.CloudConfigPath = strings.TrimSpace(cloudFirstNonEmpty(cfg.CloudConfigPath, cloudstorage.DefaultConfigPath))
	if !filepath.IsAbs(cfg.CloudConfigPath) || filepath.Clean(cfg.CloudConfigPath) != cfg.CloudConfigPath {
		return cfg, fmt.Errorf("%w: cloud_config_path must be an exact absolute path", workers.ErrInvalid)
	}
	cfg.NodeID = strings.TrimSpace(cloudFirstNonEmpty(cfg.NodeID, r.NodeID, "main"))
	if cfg.NodeID == "" {
		return cfg, fmt.Errorf("%w: node_id is required", workers.ErrInvalid)
	}
	if cfg.MaxRuntimeMS <= 0 {
		cfg.MaxRuntimeMS = cloudSnapshotUploadMaxRuntimeMS
	}
	// Historical roots decode but are deliberately ignored. Direct-archive roots
	// come exclusively from the reviewed daemon config captured in CoverageOptions.
	cfg.BackupRoot, cfg.DataDir, cfg.SkipIfUploaded = "", "", false
	return cfg, nil
}

func (r CloudSnapshotUploadRuntime) createOperation(ctx context.Context, run workers.RunContext, cfg cloudSnapshotUploadConfig) (*maintenance.Operation, error) {
	if r.Maintenance.DB == nil {
		return nil, nil
	}
	op, err := r.Maintenance.CreateOperation(ctx, maintenance.CreateOperationInput{
		OperationKey: "cloud_snapshot_upload:" + run.Run.WorkerRunID, WorkerInstanceID: run.Instance.WorkerInstanceID,
		WorkerRunID: run.Run.WorkerRunID, OperationKind: maintenance.OperationKindCloudSnapshotUpload,
		Status: maintenance.OperationRunning, SubjectKind: "node", SubjectID: run.Instance.OwnerNodeID,
		ConfigJSON: mustWorkerJSON(cfg), Metadata: json.RawMessage(`{"schema_version":"maintenance_operation.metadata.v1","source":"cloud_snapshot_upload","phase":"direct_borg_archive"}`),
	})
	if err != nil {
		return nil, err
	}
	return &op, nil
}

func cloudDirectArchiveSummary(result cloudstorage.DirectArchiveResult, packageRef OperationalPackageReference, requestedBackupOperationID string, run workers.RunContext, operation *maintenance.Operation) json.RawMessage {
	operationID := ""
	if operation != nil {
		operationID = operation.MaintenanceOperationID
	}
	return mustWorkerJSON(map[string]any{
		"root_coverage":  cloudDirectArchiveRootCoverage(result),
		"schema_version": cloudSnapshotUploadResultSchema, "status": result.Status, "phase": cloudSnapshotUploadPhase,
		"phase_status": result.Status, "committed": result.Committed, "idempotent": result.Idempotent, "retryable": result.Retryable,
		"code": result.Code, "error": result.Error, "worker_run_id": run.Run.WorkerRunID, "backup_operation_id": packageRef.BackupOperationID,
		"requested_backup_operation_id": requestedBackupOperationID, "selected_backup_operation_id": packageRef.BackupOperationID,
		"backup_operation_selector_matched": requestedBackupOperationID == "" || requestedBackupOperationID == packageRef.BackupOperationID,
		"cloud_operation_id":                operationID, "backend": result.Backend, "repository": result.Repository, "archive": result.Archive,
		"archive_ref": result.ArchiveRef, "remote_uri": result.RemoteURI, "pending_archive": result.PendingArchive,
		"resumed_pending": result.ResumedPending, "verification_phase": result.VerificationPhase,
		"manifest_schema": result.ManifestSchema, "verification_profile": result.VerificationProfile,
		"manifest_sha256": result.ManifestSHA256, "checks": result.Checks, "stage_durations_ms": result.StageDurationsMS,
		"borg_command_counts": result.BorgCommandCounts, "packed_bytes": result.PackedBytes, "deduplicated_bytes": result.DeduplicatedBytes,
		"operational_package_id": packageRef.PackageID, "operational_package_dir": packageRef.PackageDir,
		"operational_package_manifest_sha256": packageRef.ManifestSHA256, "schema_head": packageRef.SchemaHead,
		"provenance_package_id": packageRef.ProvenancePackageID, "provenance_package_dir": packageRef.ProvenancePackageDir,
		"provenance_manifest_sha256": packageRef.ProvenanceManifestSHA256, "provenance_schema_head": packageRef.ProvenanceSchemaHead,
		"provenance_graph_digest":                     packageRef.ProvenanceGraphDigest,
		"complete_local_user_data_generation_created": false,
	})
}

func (r CloudSnapshotUploadRuntime) completeSuccessfulOperation(ctx context.Context, operation *maintenance.Operation, result cloudstorage.DirectArchiveResult, summary json.RawMessage, packageRef OperationalPackageReference, requestedBackupOperationID string) error {
	if operation == nil || r.Maintenance.DB == nil {
		return nil
	}
	if _, err := r.Maintenance.CreateArtifact(ctx, cloudDirectArchiveArtifactInput(operation.MaintenanceOperationID, result, packageRef, requestedBackupOperationID)); err != nil {
		return err
	}
	_, err := r.Maintenance.CompleteOperation(ctx, maintenance.CompleteOperationInput{OperationRef: operation.MaintenanceOperationID, Status: maintenance.OperationSucceeded, ResultJSON: summary, ErrorJSON: json.RawMessage(`{}`)})
	return err
}

func cloudDirectArchiveArtifactInput(operationID string, result cloudstorage.DirectArchiveResult, packageRef OperationalPackageReference, requestedBackupOperationID string) maintenance.CreateArtifactInput {
	return maintenance.CreateArtifactInput{
		MaintenanceOperationID: operationID, ArtifactKind: maintenance.ArtifactKindCloudSnapshot,
		URI: result.RemoteURI, SizeBytes: &result.PackedBytes, SHA256: result.ManifestSHA256,
		Metadata: mustWorkerJSON(map[string]any{
			"root_coverage":  cloudDirectArchiveRootCoverage(result),
			"schema_version": "cloud_snapshot_upload.artifact.v1", "archive_ref": result.ArchiveRef,
			"archive": result.Archive, "backend": result.Backend, "pending_archive": result.PendingArchive,
			"resumed_pending": result.ResumedPending, "verification_phase": result.VerificationPhase,
			"manifest_schema": result.ManifestSchema, "verification_profile": result.VerificationProfile,
			"manifest_sha256": result.ManifestSHA256, "stage_durations_ms": result.StageDurationsMS,
			"borg_command_counts": result.BorgCommandCounts, "packed_bytes": result.PackedBytes, "deduplicated_bytes": result.DeduplicatedBytes,
			"operational_package_id": packageRef.PackageID, "provenance_package_id": packageRef.ProvenancePackageID,
			"provenance_manifest_sha256":    packageRef.ProvenanceManifestSHA256,
			"requested_backup_operation_id": requestedBackupOperationID, "selected_backup_operation_id": packageRef.BackupOperationID,
			"backup_operation_selector_matched": requestedBackupOperationID == "" || requestedBackupOperationID == packageRef.BackupOperationID,
		}),
	}
}

func cloudDirectArchiveRootCoverage(result cloudstorage.DirectArchiveResult) []backupcoverage.CloudRootEvidence {
	if result.Status != cloudstorage.DirectArchiveStatusSucceeded || !result.Committed || result.Retryable || result.ManifestV2 == nil || result.Archive != result.ManifestV2.ArchiveName || result.ArchiveRef != result.ManifestV2.ArchiveRef {
		return nil
	}
	return backupcoverage.CloudRootsFromManifest(result.ManifestV2, result.ManifestSHA256)
}

func (r CloudSnapshotUploadRuntime) completeOperationBestEffort(ctx context.Context, operation *maintenance.Operation, status string, result any, cause error) {
	if operation == nil || r.Maintenance.DB == nil {
		return
	}
	completionCtx, cancel := cloudSnapshotCompletionContext(ctx)
	defer cancel()
	_, _ = r.Maintenance.CompleteOperation(completionCtx, maintenance.CompleteOperationInput{OperationRef: operation.MaintenanceOperationID, Status: status, ResultJSON: mustWorkerJSON(result), ErrorJSON: mustWorkerJSON(map[string]any{"schema_version": "cloud_snapshot_upload.error.v1", "message": cause.Error()})})
}

func cloudSnapshotCompletionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), cloudSnapshotCompletionTimeout)
}

func cloudCooldownReason(state cloudstorage.RemoteState) string {
	if state.NextLiveCheckAfter != nil {
		return "Cloud remote is cooling down until " + state.NextLiveCheckAfter.UTC().Format(time.RFC3339) + "."
	}
	return "Cloud remote is cooling down after a recent transport failure."
}

func recordCloudWorkerSuccess(cfg cloudstorage.Config, now time.Time) error {
	return cloudstorage.RecordRemoteTransportSuccessAt(cfg, now, "worker.cloud_snapshot_upload")
}

func (r CloudSnapshotUploadRuntime) skippedResult(run workers.RunContext, now time.Time, nextRunAfter *time.Time, code, reason string) workers.RunResult {
	summary := mustWorkerJSON(map[string]any{"schema_version": cloudSnapshotUploadResultSchema, "status": "skipped", "phase": cloudSnapshotUploadPhase, "phase_status": "skipped", "committed": false, "code": code, "reason": reason, "complete_local_user_data_generation_created": false})
	checkpoint := map[string]any{}
	if previous, ok := run.Checkpoints["default"]; ok && previous.SchemaVersion == cloudSnapshotUploadCheckpointSchema {
		_ = json.Unmarshal(previous.CheckpointJSON, &checkpoint)
	}
	if checkpoint["schema_version"] != cloudSnapshotUploadCheckpointSchema {
		checkpoint = map[string]any{
			"schema_version": cloudSnapshotUploadCheckpointSchema,
			"phase":          cloudSnapshotUploadPhase,
			"phase_status":   "not_run",
			"committed":      false,
		}
	}
	checkpoint["last_attempt_status"] = "skipped"
	checkpoint["last_attempt_committed"] = false
	checkpoint["last_checked_at"] = now.Format(time.RFC3339Nano)
	checkpoint["last_reason"] = reason
	checkpoint["last_run_id"] = run.Run.WorkerRunID
	checkpoint["complete_local_user_data_generation_created"] = false
	return workers.RunResult{Status: workers.RunStatusSucceeded, ResultSummary: summary, Counters: map[string]int64{"skipped": 1}, ResourceUsage: json.RawMessage(`{}`), NextRunAfter: nextRunAfter, Retryable: false,
		CheckpointUpdates: []workers.CheckpointUpdate{{Key: "default", SchemaVersion: cloudSnapshotUploadCheckpointSchema, Value: mustWorkerJSON(checkpoint), Metadata: json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`)}},
	}
}

func cloudFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
