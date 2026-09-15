package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

const (
	defaultFindingLimit                 = 50
	maxFindingLimit                     = 200
	defaultOperationLimit               = 50
	maxOperationLimit                   = 200
	defaultDBRetentionRecentSuccessDays = 14
	defaultDBCompactMaxRowsPerBatch     = int64(5000)
	defaultDBCompactMaxTotalRows        = int64(50000)
	maxDBCompactMaxRowsPerBatch         = int64(50000)
	maxDBCompactMaxTotalRows            = int64(250000)
)

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) Status(ctx context.Context) (Status, error) {
	if s.DB == nil {
		return Status{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}

	workers, err := listMaintenanceWorkers(ctx, s.DB)
	if err != nil {
		return Status{}, err
	}
	summary, err := findingSummary(ctx, s.DB)
	if err != nil {
		return Status{}, err
	}

	return Status{
		OverallStatus: deriveOverallStatus(workers, summary),
		Workers:       workers,
		Findings:      summary,
	}, nil
}

func (s Service) ListFindings(ctx context.Context, filter FindingFilter) ([]Finding, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	return listFindings(ctx, s.DB, filter)
}

func (s Service) CreateOperation(ctx context.Context, input CreateOperationInput) (Operation, error) {
	if s.DB == nil {
		return Operation{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	input, err := normalizeCreateOperationInput(input)
	if err != nil {
		return Operation{}, err
	}
	return createOperation(ctx, s.DB, input)
}

func (s Service) CompleteOperation(ctx context.Context, input CompleteOperationInput) (Operation, error) {
	if s.DB == nil {
		return Operation{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	input, err := normalizeCompleteOperationInput(input)
	if err != nil {
		return Operation{}, err
	}
	return completeOperation(ctx, s.DB, input)
}

func (s Service) CreateArtifact(ctx context.Context, input CreateArtifactInput) (Artifact, error) {
	if s.DB == nil {
		return Artifact{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	input, err := normalizeCreateArtifactInput(input)
	if err != nil {
		return Artifact{}, err
	}
	return insertArtifact(ctx, s.DB, input)
}

func (s Service) BackupStatus(ctx context.Context) (BackupStatus, error) {
	if s.DB == nil {
		return BackupStatus{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	workers, err := listMaintenanceWorkers(ctx, s.DB)
	if err != nil {
		return BackupStatus{}, err
	}
	worker := workerStatusByKind(workers, WorkerKindMainBackup)
	findings, err := findingSummaryForKinds(ctx, s.DB, WorkerKindMainBackup)
	if err != nil {
		return BackupStatus{}, err
	}

	latestSuccessful, err := latestOperationByKind(ctx, s.DB, OperationKindMainBackup, OperationSucceeded)
	if err != nil {
		return BackupStatus{}, err
	}
	latestFailed, err := latestOperationByKind(ctx, s.DB, OperationKindMainBackup, OperationFailed)
	if err != nil {
		return BackupStatus{}, err
	}

	var successful *BackupOperation
	if latestSuccessful != nil {
		operation, err := backupOperationWithArtifacts(ctx, s.DB, *latestSuccessful)
		if err != nil {
			return BackupStatus{}, err
		}
		successful = &operation
	}
	var failed *BackupOperation
	if latestFailed != nil {
		operation, err := backupOperationWithArtifacts(ctx, s.DB, *latestFailed)
		if err != nil {
			return BackupStatus{}, err
		}
		failed = &operation
	}

	return BackupStatus{
		Status:           deriveBackupStatus(worker, successful, failed, findings),
		Worker:           worker,
		LatestSuccessful: successful,
		LatestFailed:     failed,
		OpenFindings:     findings,
	}, nil
}

// CloudProtectionStatus returns only display-safe cloud snapshot evidence.
// It performs database reads and never probes the cloud backend.
func (s Service) CloudProtectionStatus(ctx context.Context) (CloudProtectionStatus, error) {
	if s.DB == nil {
		return CloudProtectionStatus{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	workers, err := listMaintenanceWorkers(ctx, s.DB)
	if err != nil {
		return CloudProtectionStatus{}, err
	}
	worker := workerStatusByKind(workers, WorkerKindCloudSnapshotUpload)
	findings, err := findingSummaryForKinds(ctx, s.DB, WorkerKindCloudSnapshotUpload)
	if err != nil {
		return CloudProtectionStatus{}, err
	}
	success, err := latestOperationByKind(ctx, s.DB, OperationKindCloudSnapshotUpload, OperationSucceeded)
	if err != nil {
		return CloudProtectionStatus{}, err
	}
	failed, err := latestOperationByKind(ctx, s.DB, OperationKindCloudSnapshotUpload, OperationFailed)
	if err != nil {
		return CloudProtectionStatus{}, err
	}
	result := CloudProtectionStatus{Available: worker != nil || success != nil || failed != nil, Verification: "unknown", OpenFindingCount: findings.Open, WarningFindings: findings.Warning + findings.Error, CriticalFindings: findings.Critical}
	if worker != nil {
		result.WorkerState = worker.HealthStatus
		result.LastSuccessAt = worker.LastSuccessAt
		result.LastFailureAt = worker.LastFailureAt
	}
	if success != nil {
		result.LastSuccessAt = success.FinishedAt
		result.UpdatedAt = success.UpdatedAt
		result.Verification = OperationSucceeded
	}
	if failed != nil {
		result.LastFailureAt = failed.FinishedAt
		if failed.UpdatedAt.After(result.UpdatedAt) {
			result.UpdatedAt = failed.UpdatedAt
			result.Verification = OperationFailed
		}
	}
	return result, nil
}

func (s Service) ListBackups(ctx context.Context, filter OperationFilter) ([]BackupOperation, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	if strings.TrimSpace(filter.Kind) == "" {
		filter.Kind = OperationKindMainBackup
	}
	operations, err := listOperations(ctx, s.DB, filter)
	if err != nil {
		return nil, err
	}
	backups := make([]BackupOperation, 0, len(operations))
	for _, operation := range operations {
		backup, err := backupOperationWithArtifacts(ctx, s.DB, operation)
		if err != nil {
			return nil, err
		}
		backups = append(backups, backup)
	}
	return backups, nil
}

// GetBackupOperation reads one exact durable maintenance operation and its
// artifacts. It accepts only a typed maintenance operation ID; operation keys,
// package paths, and artifact identities are deliberately not lookup aliases.
func (s Service) GetBackupOperation(ctx context.Context, operationID string) (BackupOperation, error) {
	trimmed := strings.TrimSpace(operationID)
	if trimmed == "" || trimmed != operationID || ids.Validate(ids.MaintenanceOperationPrefix, trimmed) != nil {
		return BackupOperation{}, fmt.Errorf("%w: exact maintenance operation ID is required", ErrInvalid)
	}
	if s.DB == nil {
		return BackupOperation{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	operation, err := getOperation(ctx, s.DB, trimmed)
	if err != nil {
		return BackupOperation{}, err
	}
	if operation.MaintenanceOperationID != trimmed {
		return BackupOperation{}, fmt.Errorf("%w: operation %q was not found", ErrNotFound, trimmed)
	}
	return backupOperationWithArtifacts(ctx, s.DB, operation)
}

func (s Service) VerifyBackup(ctx context.Context, _ requestctx.Context, ref string) (BackupVerification, error) {
	if s.DB == nil {
		return BackupVerification{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return BackupVerification{}, fmt.Errorf("%w: backup ref is required", ErrInvalid)
	}
	operation, artifacts, err := s.resolveBackupOperation(ctx, ref)
	if err != nil {
		return BackupVerification{}, err
	}
	if operation.OperationKind != OperationKindMainBackup {
		return BackupVerification{}, fmt.Errorf("%w: operation %q is not a main backup", ErrInvalid, ref)
	}
	backupDir, err := backupDirFromOperation(operation, artifacts)
	if err != nil {
		return BackupVerification{}, err
	}

	verification, err := VerifyBackupDirectory(ctx, backupDir)
	if err != nil {
		return BackupVerification{}, err
	}
	verification.BackupOperationID = operation.MaintenanceOperationID

	resultJSON, err := operationResultWithVerification(operation.ResultJSON, verification)
	if err != nil {
		return BackupVerification{}, err
	}
	status := OperationSucceeded
	errorJSON := json.RawMessage(`{}`)
	if verification.Status != VerificationSucceeded {
		status = OperationRequiresManualAction
		errorJSON = verificationErrorJSON(verification)
	}
	if _, err := s.CompleteOperation(ctx, CompleteOperationInput{
		OperationRef: operation.MaintenanceOperationID,
		Status:       status,
		ResultJSON:   resultJSON,
		ErrorJSON:    errorJSON,
	}); err != nil {
		return BackupVerification{}, err
	}
	if verification.Status == VerificationSucceeded {
		_, err := s.ResolveFinding(ctx, backupVerificationFindingKey(operation), "Backup verification succeeded.")
		if errors.Is(err, ErrNotFound) {
			return verification, nil
		}
		return verification, err
	}

	details, err := normalizeJSONObject(mustJSON(map[string]any{
		"schema_version":          "main_backup.verification_finding.v0.2",
		"backup_operation_id":     operation.MaintenanceOperationID,
		"backup_dir":              verification.BackupDir,
		"verification_status":     verification.Status,
		"errors":                  verification.Errors,
		"checks":                  verification.Checks,
		"verified_artifact_count": verification.VerifiedArtifactNum,
		"total_artifact_bytes":    verification.TotalArtifactBytes,
	}), "details_json")
	if err != nil {
		return BackupVerification{}, err
	}
	if _, _, err := s.UpsertFinding(ctx, UpsertFindingInput{
		FindingKey:       backupVerificationFindingKey(operation),
		WorkerInstanceID: operation.WorkerInstanceID,
		FindingKind:      FindingKindBackupUnverified,
		Severity:         SeverityCritical,
		SubjectKind:      "backup",
		SubjectID:        operation.MaintenanceOperationID,
		Summary:          "Main backup verification failed.",
		DetailsJSON:      details,
		Metadata:         json.RawMessage(`{"schema_version":"maintenance_finding.metadata.v0.2","source":"main_backup_verify"}`),
	}); err != nil {
		return BackupVerification{}, err
	}
	return verification, nil
}

func (s Service) resolveBackupOperation(ctx context.Context, ref string) (Operation, []Artifact, error) {
	operation, err := getOperation(ctx, s.DB, ref)
	if err == nil {
		artifacts, artifactErr := listArtifacts(ctx, s.DB, operation.MaintenanceOperationID)
		return operation, artifacts, artifactErr
	}
	if !errors.Is(err, ErrNotFound) {
		return Operation{}, nil, err
	}

	operations, listErr := listOperations(ctx, s.DB, OperationFilter{
		Kind:  OperationKindMainBackup,
		Limit: maxOperationLimit,
	})
	if listErr != nil {
		return Operation{}, nil, listErr
	}
	for _, candidate := range operations {
		artifacts, artifactErr := listArtifacts(ctx, s.DB, candidate.MaintenanceOperationID)
		if artifactErr != nil {
			return Operation{}, nil, artifactErr
		}
		if backupOperationMatchesRef(candidate, artifacts, ref) {
			return candidate, artifacts, nil
		}
	}
	return Operation{}, nil, err
}

func (s Service) ObjectStoreStatus(ctx context.Context) (ObjectStoreStatus, error) {
	if s.DB == nil {
		return ObjectStoreStatus{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	workers, err := listMaintenanceWorkers(ctx, s.DB)
	if err != nil {
		return ObjectStoreStatus{}, err
	}
	worker := workerStatusByKind(workers, WorkerKindObjectStore)
	counts, err := objectStoreBlobCounts(ctx, s.DB)
	if err != nil {
		return ObjectStoreStatus{}, err
	}
	findings, err := findingSummaryForKinds(ctx, s.DB, WorkerKindObjectStore)
	if err != nil {
		return ObjectStoreStatus{}, err
	}
	run, err := latestObjectStoreScanRun(ctx, s.DB)
	if err != nil {
		return ObjectStoreStatus{}, err
	}
	return ObjectStoreStatus{
		Status:     deriveObjectStoreStatus(worker, counts, findings),
		Worker:     worker,
		BlobCounts: counts,
		LatestRun:  run,
		Findings:   findings,
	}, nil
}

func (s Service) DBStatus(ctx context.Context) (DBStatus, error) {
	if s.DB == nil {
		return DBStatus{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	workers, err := listMaintenanceWorkers(ctx, s.DB)
	if err != nil {
		return DBStatus{}, err
	}
	var dbWorker *WorkerStatus
	for i := range workers {
		if workers[i].WorkerKind == WorkerKindDBMaintenance {
			dbWorker = &workers[i]
			break
		}
	}

	run, err := latestDBMaintenanceRun(ctx, s.DB)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DBStatus{}, err
	}

	status := DBStatus{
		Status:          "unknown",
		Worker:          dbWorker,
		MigrationStatus: "unknown",
		LatestRun:       run,
	}
	if run != nil {
		status.applyRunSummary(run.ResultSummaryJSON)
	}
	if dbWorker != nil {
		switch dbWorker.HealthStatus {
		case "failed", "degraded", "lagging":
			if status.Status == "ok" || status.Status == "unknown" {
				status.Status = OverallWarning
			}
		}
		if dbWorker.AttentionRequired && status.Status == "ok" {
			status.Status = OverallWarning
		}
	}
	tables, err := databaseTableStatus(ctx, s.DB)
	if err != nil {
		status.Warnings = append(status.Warnings, "database table status unavailable: "+err.Error())
	} else {
		status.Tables = tables
		status.Warnings = append(status.Warnings, databaseTableWarnings(tables)...)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -defaultDBRetentionRecentSuccessDays)
	candidates, err := databaseRetentionCandidates(ctx, s.DB, cutoff)
	if err != nil {
		status.Warnings = append(status.Warnings, "database retention candidates unavailable: "+err.Error())
	} else {
		status.Retention = DatabaseRetention{
			RecentSuccessDays:       defaultDBRetentionRecentSuccessDays,
			CutoffAt:                cutoff,
			Candidates:              candidates,
			Policy:                  databaseRetentionPolicy(),
			Plans:                   databaseRetentionPlans(tables, candidates),
			AuditCriticalExclusions: databaseAuditCriticalExclusions(),
		}
	}
	rollups, err := databaseRollupStatus(ctx, s.DB, 20)
	if err != nil {
		status.Warnings = append(status.Warnings, "database rollups unavailable: "+err.Error())
	} else {
		status.Rollups = rollups
	}
	if len(status.Warnings) > 0 && status.Status == "ok" {
		status.Status = OverallWarning
	}
	return status, nil
}

func (s Service) CompactDatabase(ctx context.Context, input DatabaseCompactInput) (DatabaseCompactResult, error) {
	if s.DB == nil {
		return DatabaseCompactResult{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	reviewedPlan, hasReviewedPlan, err := decodeDatabaseCompactPlan(input.Plan)
	if err != nil {
		return DatabaseCompactResult{}, err
	}
	if hasReviewedPlan && strings.TrimSpace(input.PlanHash) != "" && strings.TrimSpace(input.PlanHash) != reviewedPlan.PlanHash {
		return DatabaseCompactResult{}, fmt.Errorf("%w: reviewed plan hash %s does not match requested plan hash %s", ErrInvalid, reviewedPlan.PlanHash, strings.TrimSpace(input.PlanHash))
	}
	days := input.RecentSuccessDays
	if hasReviewedPlan && reviewedPlan.RecentSuccessDays > 0 {
		days = reviewedPlan.RecentSuccessDays
	}
	if days <= 0 {
		days = defaultDBRetentionRecentSuccessDays
	}
	if days < 1 {
		days = 1
	}
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -days)
	if hasReviewedPlan && !reviewedPlan.CutoffAt.IsZero() {
		cutoff = reviewedPlan.CutoffAt.UTC()
	}
	apply := input.Confirm && !input.DryRun
	limitInput := input
	if hasReviewedPlan {
		if limitInput.MaxRowsPerBatch <= 0 {
			limitInput.MaxRowsPerBatch = reviewedPlan.MaxRowsPerBatch
		}
		if limitInput.MaxTotalRows <= 0 {
			limitInput.MaxTotalRows = reviewedPlan.MaxTotalRows
		}
	}
	result := DatabaseCompactResult{
		Status:                  "planned",
		DryRun:                  !apply,
		Confirmed:               input.Confirm,
		MutatesDatabase:         apply,
		RecentSuccessDays:       days,
		CutoffAt:                cutoff,
		PlannedAt:               now,
		Candidates:              map[string]int64{},
		Deleted:                 map[string]int64{},
		Policy:                  databaseRetentionPolicy(),
		AuditCriticalExclusions: databaseAuditCriticalExclusions(),
		Reason:                  strings.TrimSpace(input.Reason),
		PhysicalStorageNote:     "Logical row deletion may not immediately reduce PostgreSQL table or database file size; autovacuum can reuse freed space, while VACUUM FULL/table rewrites are intentionally out of scope.",
	}

	candidates, err := databaseRetentionCandidates(ctx, s.DB, cutoff)
	if err != nil {
		return DatabaseCompactResult{}, err
	}
	result.Candidates = candidates
	if tables, tableErr := databaseTableStatus(ctx, s.DB); tableErr != nil {
		result.Warnings = append(result.Warnings, "database table estimates unavailable: "+tableErr.Error())
	} else {
		result.Plans = databaseRetentionPlans(tables, candidates)
	}
	limits, limitWarnings := databaseCompactionLimits(limitInput, result.Plans)
	result.AllowedTables = limits.AllowedTables
	result.RowLimits = limits.RowLimits
	result.MaxRowsPerBatch = limits.MaxRowsPerBatch
	result.MaxTotalRows = limits.MaxTotalRows
	result.Warnings = append(result.Warnings, limitWarnings...)
	result.Warnings = append(result.Warnings, databaseRetentionApplyWarnings(result.Plans)...)
	result.PlanHash = databaseCompactPlanHash(result)
	result.PlanID = databaseCompactPlanID(result.PlanHash)
	if apply {
		expectedHash := strings.TrimSpace(input.PlanHash)
		if hasReviewedPlan {
			expectedHash = reviewedPlan.PlanHash
		}
		if expectedHash != "" && expectedHash != result.PlanHash {
			result.CandidateDrift = databaseCandidateDrift(reviewedPlan.Candidates, result.Candidates)
			return DatabaseCompactResult{}, fmt.Errorf("%w: reviewed database compaction plan is stale or does not match current candidates; expected %s got %s", ErrInvalid, expectedHash, result.PlanHash)
		}
		if expectedHash == "" {
			result.Warnings = append(result.Warnings, "No reviewed plan hash was supplied; applying the current plan for backward compatibility.")
		}
		if err := validateDatabaseRetentionApply(result.Plans); err != nil {
			return DatabaseCompactResult{}, err
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DatabaseCompactResult{}, err
	}
	defer tx.Rollback()

	rollups, err := databaseCompactionRollups(ctx, tx, cutoff, result.DryRun)
	if err != nil {
		return DatabaseCompactResult{}, err
	}
	result.Rollups = rollups
	if result.DryRun {
		result.Status = "dry_run"
		result.Warnings = append(result.Warnings, "No rows were deleted. This report is diagnostic; run `loom maintenance retention apply --yes` only after reviewing the dry-run plan.")
		return result, nil
	}
	deleted, err := deleteRoutineDatabaseRows(ctx, tx, cutoff, limits)
	if err != nil {
		return DatabaseCompactResult{}, err
	}
	result.Deleted = deleted
	completed := time.Now().UTC()
	result.CompletedAt = &completed
	result.Status = "applied"
	if err := tx.Commit(); err != nil {
		return DatabaseCompactResult{}, err
	}
	if err := recordDatabaseCompactionEvidence(ctx, s.DB, &result); err != nil {
		result.EvidenceStatus = "unavailable"
		result.Warnings = append(result.Warnings, "Maintenance operation evidence unavailable: "+err.Error())
	}
	return result, nil
}

func (s Service) UpsertFinding(ctx context.Context, input UpsertFindingInput) (Finding, bool, error) {
	if s.DB == nil {
		return Finding{}, false, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	input, err := normalizeFindingInput(input)
	if err != nil {
		return Finding{}, false, err
	}

	var existed bool
	if err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM maintenance.findings
			WHERE finding_key = $1
		)
	`, input.FindingKey).Scan(&existed); err != nil {
		return Finding{}, false, err
	}

	workerRunID := sql.NullString{String: input.WorkerRunID, Valid: input.WorkerRunID != ""}
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO maintenance.findings (
			maintenance_finding_id, finding_key, worker_instance_id, worker_run_id,
			finding_kind, severity, status, subject_kind, subject_id, summary,
			details_json, metadata
		)
		VALUES (
			$1, $2, $3, nullif($4, ''),
			$5, $6, 'open', $7, $8, $9,
			$10, $11
		)
		ON CONFLICT (finding_key) DO UPDATE
		SET worker_instance_id = EXCLUDED.worker_instance_id,
		    worker_run_id = EXCLUDED.worker_run_id,
		    finding_kind = EXCLUDED.finding_kind,
		    severity = EXCLUDED.severity,
		    status = CASE
		        WHEN maintenance.findings.status IN ('resolved', 'ignored') THEN 'open'
		        ELSE maintenance.findings.status
		    END,
		    subject_kind = EXCLUDED.subject_kind,
		    subject_id = EXCLUDED.subject_id,
		    last_seen_at = now(),
		    resolved_at = NULL,
		    summary = EXCLUDED.summary,
		    details_json = EXCLUDED.details_json,
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
	`,
		newMaintenanceFindingID(),
		input.FindingKey,
		input.WorkerInstanceID,
		workerRunID.String,
		input.FindingKind,
		input.Severity,
		input.SubjectKind,
		input.SubjectID,
		input.Summary,
		[]byte(input.DetailsJSON),
		[]byte(input.Metadata),
	); err != nil {
		return Finding{}, false, err
	}

	finding, err := getFindingByKey(ctx, s.DB, input.FindingKey)
	if err != nil {
		return Finding{}, false, err
	}
	return finding, !existed, nil
}

func (s Service) ResolveFinding(ctx context.Context, key, resolution string) (Finding, error) {
	if s.DB == nil {
		return Finding{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return Finding{}, fmt.Errorf("%w: finding key is required", ErrInvalid)
	}
	resolutionJSON, err := normalizeJSONObject(json.RawMessage(fmt.Sprintf(`{"summary":%q}`, strings.TrimSpace(resolution))), "resolution_json")
	if err != nil {
		return Finding{}, err
	}

	result, err := s.DB.ExecContext(ctx, `
		UPDATE maintenance.findings
		SET status = 'resolved',
		    resolved_at = now(),
		    resolution_json = $2,
		    updated_at = now()
		WHERE finding_key = $1
		  AND status <> 'resolved'
	`, key, []byte(resolutionJSON))
	if err != nil {
		return Finding{}, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return Finding{}, fmt.Errorf("%w: finding %q was not found or already resolved", ErrNotFound, key)
	}
	return getFindingByKey(ctx, s.DB, key)
}

func normalizeFindingInput(input UpsertFindingInput) (UpsertFindingInput, error) {
	input.WorkerInstanceID = strings.TrimSpace(input.WorkerInstanceID)
	input.WorkerRunID = strings.TrimSpace(input.WorkerRunID)
	input.FindingKind = strings.TrimSpace(input.FindingKind)
	input.Severity = strings.TrimSpace(input.Severity)
	input.SubjectKind = strings.TrimSpace(input.SubjectKind)
	input.SubjectID = strings.TrimSpace(input.SubjectID)
	input.Summary = strings.TrimSpace(input.Summary)
	input.FindingKey = strings.TrimSpace(input.FindingKey)

	if input.WorkerInstanceID == "" {
		return UpsertFindingInput{}, fmt.Errorf("%w: worker_instance_id is required", ErrInvalid)
	}
	if input.FindingKind == "" {
		return UpsertFindingInput{}, fmt.Errorf("%w: finding_kind is required", ErrInvalid)
	}
	if input.Severity == "" {
		input.Severity = SeverityWarning
	}
	if !validSeverity(input.Severity) {
		return UpsertFindingInput{}, fmt.Errorf("%w: severity %q is invalid", ErrInvalid, input.Severity)
	}
	if input.Summary == "" {
		return UpsertFindingInput{}, fmt.Errorf("%w: summary is required", ErrInvalid)
	}
	if input.FindingKey == "" {
		input.FindingKey = strings.Join([]string{
			input.WorkerInstanceID,
			input.FindingKind,
			input.SubjectKind,
			input.SubjectID,
		}, ":")
	}

	var err error
	if input.DetailsJSON, err = normalizeJSONObject(input.DetailsJSON, "details_json"); err != nil {
		return UpsertFindingInput{}, err
	}
	if input.Metadata, err = normalizeJSONObject(input.Metadata, "metadata"); err != nil {
		return UpsertFindingInput{}, err
	}
	return input, nil
}

func normalizeCreateOperationInput(input CreateOperationInput) (CreateOperationInput, error) {
	input.OperationKey = strings.TrimSpace(input.OperationKey)
	input.WorkerInstanceID = strings.TrimSpace(input.WorkerInstanceID)
	input.WorkerRunID = strings.TrimSpace(input.WorkerRunID)
	input.OperationKind = strings.TrimSpace(input.OperationKind)
	input.Status = strings.TrimSpace(input.Status)
	input.SubjectKind = strings.TrimSpace(input.SubjectKind)
	input.SubjectID = strings.TrimSpace(input.SubjectID)
	if input.WorkerInstanceID == "" {
		return CreateOperationInput{}, fmt.Errorf("%w: worker_instance_id is required", ErrInvalid)
	}
	if input.OperationKind == "" {
		return CreateOperationInput{}, fmt.Errorf("%w: operation_kind is required", ErrInvalid)
	}
	if input.Status == "" {
		input.Status = OperationRunning
	}
	if !validOperationStatus(input.Status) {
		return CreateOperationInput{}, fmt.Errorf("%w: operation status %q is invalid", ErrInvalid, input.Status)
	}
	if input.OperationKey == "" {
		input.OperationKey = input.OperationKind + ":" + newMaintenanceOperationID()
	}
	var err error
	if input.ConfigJSON, err = normalizeJSONObject(input.ConfigJSON, "config_json"); err != nil {
		return CreateOperationInput{}, err
	}
	if input.ResultJSON, err = normalizeJSONObject(input.ResultJSON, "result_json"); err != nil {
		return CreateOperationInput{}, err
	}
	if input.ErrorJSON, err = normalizeJSONObject(input.ErrorJSON, "error_json"); err != nil {
		return CreateOperationInput{}, err
	}
	if input.Metadata, err = normalizeJSONObject(input.Metadata, "metadata"); err != nil {
		return CreateOperationInput{}, err
	}
	return input, nil
}

func normalizeCompleteOperationInput(input CompleteOperationInput) (CompleteOperationInput, error) {
	input.OperationRef = strings.TrimSpace(input.OperationRef)
	input.Status = strings.TrimSpace(input.Status)
	if input.OperationRef == "" {
		return CompleteOperationInput{}, fmt.Errorf("%w: operation_ref is required", ErrInvalid)
	}
	if input.Status == "" {
		input.Status = OperationSucceeded
	}
	if !validOperationStatus(input.Status) {
		return CompleteOperationInput{}, fmt.Errorf("%w: operation status %q is invalid", ErrInvalid, input.Status)
	}
	var err error
	if input.ResultJSON, err = normalizeJSONObject(input.ResultJSON, "result_json"); err != nil {
		return CompleteOperationInput{}, err
	}
	if input.ErrorJSON, err = normalizeJSONObject(input.ErrorJSON, "error_json"); err != nil {
		return CompleteOperationInput{}, err
	}
	return input, nil
}

func normalizeCreateArtifactInput(input CreateArtifactInput) (CreateArtifactInput, error) {
	input.MaintenanceOperationID = strings.TrimSpace(input.MaintenanceOperationID)
	input.ArtifactKind = strings.TrimSpace(input.ArtifactKind)
	input.URI = strings.TrimSpace(input.URI)
	input.SHA256 = strings.TrimSpace(input.SHA256)
	if input.MaintenanceOperationID == "" {
		return CreateArtifactInput{}, fmt.Errorf("%w: maintenance_operation_id is required", ErrInvalid)
	}
	if input.ArtifactKind == "" {
		return CreateArtifactInput{}, fmt.Errorf("%w: artifact_kind is required", ErrInvalid)
	}
	if input.URI == "" {
		return CreateArtifactInput{}, fmt.Errorf("%w: artifact uri is required", ErrInvalid)
	}
	if input.SizeBytes != nil && *input.SizeBytes < 0 {
		return CreateArtifactInput{}, fmt.Errorf("%w: size_bytes must be non-negative", ErrInvalid)
	}
	var err error
	if input.Metadata, err = normalizeJSONObject(input.Metadata, "metadata"); err != nil {
		return CreateArtifactInput{}, err
	}
	return input, nil
}

func deriveOverallStatus(workers []WorkerStatus, summary FindingSummary) string {
	if summary.Critical > 0 || summary.Error > 0 {
		return OverallCritical
	}
	if summary.Warning > 0 {
		return OverallWarning
	}
	for _, worker := range workers {
		switch worker.HealthStatus {
		case "failed", "degraded", "lagging":
			return OverallWarning
		}
		if worker.AttentionRequired || worker.OpenFindings > 0 {
			return OverallWarning
		}
	}
	return OverallOK
}

func deriveBackupStatus(worker *WorkerStatus, latestSuccessful, latestFailed *BackupOperation, findings FindingSummary) string {
	if findings.Critical > 0 || findings.Error > 0 {
		return OverallCritical
	}
	status := OverallUnknown
	if latestSuccessful != nil {
		status = OverallOK
	}
	if latestFailed != nil {
		if latestSuccessful == nil || latestFailed.Operation.StartedAt.After(latestSuccessful.Operation.StartedAt) {
			status = OverallWarning
		}
	}
	if findings.Warning > 0 && status == OverallOK {
		status = OverallWarning
	}
	if worker != nil {
		switch worker.HealthStatus {
		case "failed", "degraded", "lagging":
			if status == OverallOK || status == OverallUnknown {
				status = OverallWarning
			}
		}
		if worker.AttentionRequired && status == OverallOK {
			status = OverallWarning
		}
	}
	return status
}

func deriveObjectStoreStatus(worker *WorkerStatus, counts ObjectStoreBlobCounts, findings FindingSummary) string {
	if counts.Missing > 0 || counts.Corrupt > 0 || findings.Critical > 0 || findings.Error > 0 {
		return OverallCritical
	}
	status := OverallOK
	if findings.Warning > 0 {
		status = OverallWarning
	}
	if worker != nil {
		switch worker.HealthStatus {
		case "failed", "degraded", "lagging":
			if status == OverallOK {
				status = OverallWarning
			}
		}
		if worker.AttentionRequired && status == OverallOK {
			status = OverallWarning
		}
	}
	return status
}

func (s *DBStatus) applyRunSummary(raw json.RawMessage) {
	var summary struct {
		Status          string `json:"status"`
		MigrationStatus string `json:"migration_status"`
		CurrentVersion  int64  `json:"current_version"`
		LatestVersion   int64  `json:"latest_version"`
		Pending         int64  `json:"pending"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return
	}
	if strings.TrimSpace(summary.Status) != "" {
		s.Status = strings.TrimSpace(summary.Status)
	}
	if strings.TrimSpace(summary.MigrationStatus) != "" {
		s.MigrationStatus = strings.TrimSpace(summary.MigrationStatus)
	}
	s.CurrentVersion = summary.CurrentVersion
	s.LatestVersion = summary.LatestVersion
	s.Pending = summary.Pending
}

func validSeverity(value string) bool {
	switch value {
	case SeverityInfo, SeverityWarning, SeverityError, SeverityCritical:
		return true
	default:
		return false
	}
}

func validOperationStatus(value string) bool {
	switch value {
	case OperationPending, OperationRunning, OperationSucceeded, OperationFailed, OperationCancelled, OperationRequiresManualAction, OperationSkipped:
		return true
	default:
		return false
	}
}

func backupOperationWithArtifacts(ctx context.Context, q queryer, operation Operation) (BackupOperation, error) {
	artifacts, err := listArtifacts(ctx, q, operation.MaintenanceOperationID)
	if err != nil {
		return BackupOperation{}, err
	}
	return BackupOperation{
		Operation: operation,
		Artifacts: artifacts,
	}, nil
}

func workerStatusByKind(workers []WorkerStatus, kind string) *WorkerStatus {
	for i := range workers {
		if workers[i].WorkerKind == kind {
			return &workers[i]
		}
	}
	return nil
}

func backupDirFromOperation(operation Operation, artifacts []Artifact) (string, error) {
	var result struct {
		BackupDir    string `json:"backup_dir"`
		ManifestPath string `json:"manifest_path"`
	}
	_ = json.Unmarshal(operation.ResultJSON, &result)
	if strings.TrimSpace(result.BackupDir) != "" {
		return filepath.Clean(strings.TrimSpace(result.BackupDir)), nil
	}
	if strings.TrimSpace(result.ManifestPath) != "" {
		return filepath.Dir(filepath.Clean(strings.TrimSpace(result.ManifestPath))), nil
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactKind != ArtifactKindBackupManifest {
			continue
		}
		path, err := fileURIPath(artifact.URI)
		if err != nil {
			return "", err
		}
		return filepath.Dir(path), nil
	}
	return "", fmt.Errorf("%w: backup directory is not recorded for operation %s", ErrInvalid, operation.MaintenanceOperationID)
}

func backupOperationMatchesRef(operation Operation, artifacts []Artifact, ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	backupDir, err := backupDirFromOperation(operation, artifacts)
	if err != nil {
		return false
	}
	return filepath.Clean(ref) == filepath.Clean(backupDir)
}

func fileURIPath(rawURI string) (string, error) {
	rawURI = strings.TrimSpace(rawURI)
	if rawURI == "" {
		return "", fmt.Errorf("%w: artifact uri is empty", ErrInvalid)
	}
	parsed, err := url.Parse(rawURI)
	if err == nil && parsed.Scheme == "file" {
		return filepath.Clean(parsed.Path), nil
	}
	if err == nil && parsed.Scheme != "" {
		return "", fmt.Errorf("%w: artifact uri scheme %q is not supported", ErrInvalid, parsed.Scheme)
	}
	return filepath.Clean(rawURI), nil
}

func operationResultWithVerification(raw json.RawMessage, verification BackupVerification) (json.RawMessage, error) {
	result := map[string]any{}
	if len(raw) > 0 && strings.TrimSpace(string(raw)) != "{}" {
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("%w: operation result_json is invalid JSON: %w", ErrInvalid, err)
		}
	}
	result["verification"] = verification
	result["verification_status"] = verification.Status
	result["status"] = verification.Status
	out, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return normalizeJSONObject(out, "result_json")
}

func verificationErrorJSON(verification BackupVerification) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"schema_version":      "main_backup.verification_error.v0.2",
		"verification_status": verification.Status,
		"errors":              verification.Errors,
		"checks":              verification.Checks,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func backupVerificationFindingKey(operation Operation) string {
	return operation.OperationKey + ":backup_unverified:backup:" + operation.MaintenanceOperationID
}
