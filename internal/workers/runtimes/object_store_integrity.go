package runtimes

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/workers"
)

type ObjectStoreIntegrityRuntime struct {
	DB              *sql.DB
	Maintenance     maintenance.Service
	ObjectStoreRoot string
	Now             func() time.Time
}

type objectStoreIntegrityConfig struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"`
	SampleSize    int    `json:"sample_size"`
	BatchSize     int    `json:"batch_size"`
	VerifyHashes  bool   `json:"verify_hashes"`
	DetectOrphans bool   `json:"detect_orphans"`
	AutoRepair    bool   `json:"auto_repair"`
	MaxRuntimeMS  int    `json:"max_runtime_ms"`
}

type objectStoreBlobCandidate struct {
	BlobID        string
	HashAlgorithm string
	HashHex       string
	HashURI       string
	SizeBytes     int64
	StoragePath   string
	Status        string
	VerifiedAt    *time.Time
}

type objectStoreScanSummary struct {
	SchemaVersion    string `json:"schema_version"`
	Status           string `json:"status"`
	Mode             string `json:"mode"`
	TargetBlobRef    string `json:"target_blob_ref,omitempty"`
	Checked          int64  `json:"checked"`
	Verified         int64  `json:"verified"`
	Missing          int64  `json:"missing"`
	Corrupt          int64  `json:"corrupt"`
	Skipped          int64  `json:"skipped"`
	FindingsOpened   int64  `json:"findings_opened"`
	FindingsResolved int64  `json:"findings_resolved"`
	LastBlobID       string `json:"last_blob_id,omitempty"`
}

type objectStoreBlobCheck struct {
	Status       string
	FindingKind  string
	ErrorCode    string
	Message      string
	ResolvedPath string
	ActualSize   *int64
	ActualSHA256 string
}

type objectStoreScanner interface {
	Scan(...any) error
}

func NewObjectStoreIntegrityRuntime(db *sql.DB, maintenanceService maintenance.Service, objectStoreRoot string) ObjectStoreIntegrityRuntime {
	return ObjectStoreIntegrityRuntime{
		DB:              db,
		Maintenance:     maintenanceService,
		ObjectStoreRoot: objectStoreRoot,
	}
}

func (r ObjectStoreIntegrityRuntime) Kind() string {
	return workers.KindObjectStore
}

func (r ObjectStoreIntegrityRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindObjectStore,
		DisplayName:                  "Object-store integrity",
		Description:                  "Verifies managed blob metadata against files stored in the main object store.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.maintenance",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":21600,"run_on_startup":false}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":900}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_medium"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"object_store_integrity.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"object_store_integrity.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"object_store_integrity.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r ObjectStoreIntegrityRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"object_store_integrity.config.v0.2","mode":"sample","sample_size":100,"batch_size":100,"verify_hashes":true,"detect_orphans":false,"auto_repair":false,"max_runtime_ms":900000}`)
}

func (r ObjectStoreIntegrityRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.object_store_integrity_sample",
			WorkerKind:         workers.KindObjectStore,
			DisplayName:        "Object-store integrity sample",
			Description:        "Samples managed object-store blobs and verifies file presence, size, and hash.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":21600,"run_on_startup":false}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":900}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_medium"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r ObjectStoreIntegrityRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := r.parseConfig(config)
	return err
}

func (r ObjectStoreIntegrityRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.DB == nil {
		return workers.RunResult{}, fmt.Errorf("object-store integrity database is not configured")
	}
	if r.Maintenance.DB == nil {
		return workers.RunResult{}, fmt.Errorf("object-store integrity maintenance service is not configured")
	}
	config, err := r.parseConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	targetBlobRef := targetBlobRefFromRunMetadata(run.Run.Metadata)
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}

	operation, err := r.Maintenance.CreateOperation(ctx, maintenance.CreateOperationInput{
		OperationKey:     "object_store_integrity:" + run.Run.WorkerRunID,
		WorkerInstanceID: run.Instance.WorkerInstanceID,
		WorkerRunID:      run.Run.WorkerRunID,
		OperationKind:    maintenance.OperationKindObjectStoreSampleScan,
		Status:           maintenance.OperationRunning,
		SubjectKind:      "object_store",
		SubjectID:        filepath.Clean(r.ObjectStoreRoot),
		ConfigJSON:       run.Instance.ConfigJSON,
		Metadata:         json.RawMessage(`{"schema_version":"maintenance_operation.metadata.v0.2","source":"object_store_integrity"}`),
	})
	if err != nil {
		return workers.RunResult{}, err
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if config.MaxRuntimeMS > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(config.MaxRuntimeMS)*time.Millisecond)
		defer cancel()
	}
	summary, scanErr := r.scan(runCtx, run, operation, config, targetBlobRef, now)
	if scanErr != nil {
		errorJSON := errorObject("object_store_integrity.failed", scanErr, filepath.Clean(r.ObjectStoreRoot))
		_, _ = r.Maintenance.CompleteOperation(ctx, maintenance.CompleteOperationInput{
			OperationRef: operation.MaintenanceOperationID,
			Status:       maintenance.OperationFailed,
			ResultJSON: mustWorkerJSON(map[string]any{
				"schema_version":           "object_store_integrity.result.v0.2",
				"status":                   "failed",
				"maintenance_operation_id": operation.MaintenanceOperationID,
				"worker_run_id":            run.Run.WorkerRunID,
			}),
			ErrorJSON: errorJSON,
		})
		return workers.RunResult{}, scanErr
	}

	result := mustWorkerJSON(summary)
	if _, err := r.Maintenance.CompleteOperation(ctx, maintenance.CompleteOperationInput{
		OperationRef: operation.MaintenanceOperationID,
		Status:       maintenance.OperationSucceeded,
		ResultJSON:   result,
		ErrorJSON:    json.RawMessage(`{}`),
	}); err != nil {
		return workers.RunResult{}, err
	}

	checkpoint := mustWorkerJSON(map[string]any{
		"schema_version":  "object_store_integrity.checkpoint.v0.2",
		"mode":            config.Mode,
		"last_checked_at": now.Format(time.RFC3339Nano),
		"last_blob_id":    summary.LastBlobID,
		"checked":         summary.Checked,
		"missing":         summary.Missing,
		"corrupt":         summary.Corrupt,
	})
	tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: result,
		Counters: map[string]int64{
			"checked":            summary.Checked,
			"verified":           summary.Verified,
			"missing":            summary.Missing,
			"corrupt":            summary.Corrupt,
			"findings_opened":    summary.FindingsOpened,
			"findings_resolved":  summary.FindingsResolved,
			"object_store_scans": 1,
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "object_store_integrity.checkpoint.v0.2",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: tickPolicy.NextAfter(now),
		Retryable:    false,
	}, nil
}

func (r ObjectStoreIntegrityRuntime) parseConfig(raw json.RawMessage) (objectStoreIntegrityConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return objectStoreIntegrityConfig{}, err
	}
	config := objectStoreIntegrityConfig{
		SchemaVersion: "object_store_integrity.config.v0.2",
		Mode:          "sample",
		SampleSize:    100,
		BatchSize:     100,
		VerifyHashes:  true,
		DetectOrphans: false,
		AutoRepair:    false,
		MaxRuntimeMS:  900000,
	}
	if len(normalized) > 0 && strings.TrimSpace(string(normalized)) != "{}" {
		if err := json.Unmarshal(normalized, &config); err != nil {
			return objectStoreIntegrityConfig{}, fmt.Errorf("%w: object_store_integrity config_json is invalid JSON: %w", workers.ErrInvalid, err)
		}
	}
	config.Mode = strings.TrimSpace(config.Mode)
	if config.Mode == "" {
		config.Mode = "sample"
	}
	if config.Mode != "sample" {
		return objectStoreIntegrityConfig{}, fmt.Errorf("%w: object_store_integrity mode %q is not supported in slice 03", workers.ErrInvalid, config.Mode)
	}
	if config.DetectOrphans {
		return objectStoreIntegrityConfig{}, fmt.Errorf("%w: object_store_integrity detect_orphans is not implemented in slice 03", workers.ErrInvalid)
	}
	if config.AutoRepair {
		return objectStoreIntegrityConfig{}, fmt.Errorf("%w: object_store_integrity auto_repair is not allowed", workers.ErrInvalid)
	}
	if config.SampleSize <= 0 {
		config.SampleSize = 100
	}
	if config.BatchSize <= 0 {
		config.BatchSize = config.SampleSize
	}
	if config.BatchSize > config.SampleSize {
		config.BatchSize = config.SampleSize
	}
	if config.MaxRuntimeMS <= 0 {
		config.MaxRuntimeMS = 900000
	}
	return config, nil
}

func (r ObjectStoreIntegrityRuntime) scan(ctx context.Context, run workers.RunContext, operation maintenance.Operation, config objectStoreIntegrityConfig, targetBlobRef string, now time.Time) (objectStoreScanSummary, error) {
	candidates, err := r.loadCandidates(ctx, config, targetBlobRef)
	if err != nil {
		return objectStoreScanSummary{}, err
	}
	summary := objectStoreScanSummary{
		SchemaVersion: "object_store_integrity.result.v0.2",
		Status:        maintenance.OverallOK,
		Mode:          config.Mode,
		TargetBlobRef: targetBlobRef,
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return objectStoreScanSummary{}, err
		}
		summary.Checked++
		summary.LastBlobID = candidate.BlobID
		check := verifyObjectStoreBlob(r.ObjectStoreRoot, config, candidate)
		switch check.Status {
		case "verified":
			if err := r.markBlobVerified(ctx, candidate.BlobID, now); err != nil {
				return objectStoreScanSummary{}, err
			}
			summary.Verified++
			resolved, err := r.resolveBlobFindings(ctx, operation, candidate)
			if err != nil {
				return objectStoreScanSummary{}, err
			}
			summary.FindingsResolved += resolved
		case "missing":
			if err := r.markBlobStatus(ctx, candidate.BlobID, "missing"); err != nil {
				return objectStoreScanSummary{}, err
			}
			if err := r.openBlobFinding(ctx, operation, run.Run.WorkerRunID, candidate, check); err != nil {
				return objectStoreScanSummary{}, err
			}
			summary.Missing++
			summary.FindingsOpened++
		case "corrupt":
			if err := r.markBlobStatus(ctx, candidate.BlobID, "corrupt"); err != nil {
				return objectStoreScanSummary{}, err
			}
			if err := r.openBlobFinding(ctx, operation, run.Run.WorkerRunID, candidate, check); err != nil {
				return objectStoreScanSummary{}, err
			}
			summary.Corrupt++
			summary.FindingsOpened++
		default:
			summary.Skipped++
		}
	}
	if summary.Missing > 0 || summary.Corrupt > 0 {
		summary.Status = maintenance.OverallCritical
	}
	return summary, nil
}

func (r ObjectStoreIntegrityRuntime) loadCandidates(ctx context.Context, config objectStoreIntegrityConfig, targetBlobRef string) ([]objectStoreBlobCandidate, error) {
	if strings.TrimSpace(targetBlobRef) != "" {
		candidate, err := scanObjectStoreBlobCandidate(r.DB.QueryRowContext(ctx, `
			SELECT blob_id, hash_algorithm, hash_hex, hash_uri, size_bytes, storage_path, status, verified_at
			FROM files.blobs
			WHERE blob_id = $1 OR hash_uri = $1 OR hash_hex = $1
			LIMIT 1
		`, strings.TrimSpace(targetBlobRef)))
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: object-store blob %q", workers.ErrNotFound, targetBlobRef)
		}
		if err != nil {
			return nil, err
		}
		return []objectStoreBlobCandidate{candidate}, nil
	}

	rows, err := r.DB.QueryContext(ctx, `
		SELECT blob_id, hash_algorithm, hash_hex, hash_uri, size_bytes, storage_path, status, verified_at
		FROM files.blobs
		ORDER BY verified_at NULLS FIRST, created_at ASC
		LIMIT $1
	`, config.SampleSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := []objectStoreBlobCandidate{}
	for rows.Next() {
		candidate, err := scanObjectStoreBlobCandidate(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func scanObjectStoreBlobCandidate(s objectStoreScanner) (objectStoreBlobCandidate, error) {
	var candidate objectStoreBlobCandidate
	var verifiedAt sql.NullTime
	if err := s.Scan(
		&candidate.BlobID,
		&candidate.HashAlgorithm,
		&candidate.HashHex,
		&candidate.HashURI,
		&candidate.SizeBytes,
		&candidate.StoragePath,
		&candidate.Status,
		&verifiedAt,
	); err != nil {
		return objectStoreBlobCandidate{}, err
	}
	if verifiedAt.Valid {
		candidate.VerifiedAt = &verifiedAt.Time
	}
	return candidate, nil
}

func verifyObjectStoreBlob(objectStoreRoot string, config objectStoreIntegrityConfig, candidate objectStoreBlobCandidate) objectStoreBlobCheck {
	resolvedPath, err := resolveObjectStoreBlobPath(objectStoreRoot, candidate.StoragePath)
	if err != nil {
		return objectStoreBlobCheck{
			Status:      "corrupt",
			FindingKind: maintenance.FindingKindObjectStoreBlobCorrupt,
			ErrorCode:   "object_store_blob_path_invalid",
			Message:     err.Error(),
		}
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return objectStoreBlobCheck{
				Status:       "missing",
				FindingKind:  maintenance.FindingKindObjectStoreBlobMissing,
				ErrorCode:    "object_store_blob_missing",
				Message:      "object-store blob file is missing",
				ResolvedPath: resolvedPath,
			}
		}
		return objectStoreBlobCheck{
			Status:       "corrupt",
			FindingKind:  maintenance.FindingKindObjectStoreBlobCorrupt,
			ErrorCode:    "object_store_blob_stat_failed",
			Message:      err.Error(),
			ResolvedPath: resolvedPath,
		}
	}
	if !info.Mode().IsRegular() {
		return objectStoreBlobCheck{
			Status:       "corrupt",
			FindingKind:  maintenance.FindingKindObjectStoreBlobCorrupt,
			ErrorCode:    "object_store_blob_not_regular",
			Message:      "object-store blob path is not a regular file",
			ResolvedPath: resolvedPath,
		}
	}
	actualSize := info.Size()
	if candidate.SizeBytes != actualSize {
		return objectStoreBlobCheck{
			Status:       "corrupt",
			FindingKind:  maintenance.FindingKindObjectStoreBlobSizeMismatch,
			ErrorCode:    "object_store_blob_size_mismatch",
			Message:      "object-store blob file size does not match metadata",
			ResolvedPath: resolvedPath,
			ActualSize:   &actualSize,
		}
	}
	if config.VerifyHashes {
		actualHash, err := sha256File(resolvedPath)
		if err != nil {
			return objectStoreBlobCheck{
				Status:       "corrupt",
				FindingKind:  maintenance.FindingKindObjectStoreBlobCorrupt,
				ErrorCode:    "object_store_blob_hash_failed",
				Message:      err.Error(),
				ResolvedPath: resolvedPath,
				ActualSize:   &actualSize,
			}
		}
		if !strings.EqualFold(actualHash, strings.TrimSpace(candidate.HashHex)) {
			return objectStoreBlobCheck{
				Status:       "corrupt",
				FindingKind:  maintenance.FindingKindObjectStoreBlobCorrupt,
				ErrorCode:    "object_store_blob_hash_mismatch",
				Message:      "object-store blob sha256 does not match metadata",
				ResolvedPath: resolvedPath,
				ActualSize:   &actualSize,
				ActualSHA256: actualHash,
			}
		}
		return objectStoreBlobCheck{
			Status:       "verified",
			ResolvedPath: resolvedPath,
			ActualSize:   &actualSize,
			ActualSHA256: actualHash,
		}
	}
	return objectStoreBlobCheck{
		Status:       "verified",
		ResolvedPath: resolvedPath,
		ActualSize:   &actualSize,
	}
}

func resolveObjectStoreBlobPath(objectStoreRoot, storagePath string) (string, error) {
	objectStoreRoot = filepath.Clean(strings.TrimSpace(objectStoreRoot))
	storagePath = strings.TrimSpace(storagePath)
	if objectStoreRoot == "" || objectStoreRoot == "." {
		return "", fmt.Errorf("%w: object store root is required", workers.ErrInvalid)
	}
	if storagePath == "" {
		return "", fmt.Errorf("%w: blob storage_path is required", workers.ErrInvalid)
	}
	path := storagePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(objectStoreRoot, path)
	}
	path = filepath.Clean(path)
	if !pathWithinRuntime(objectStoreRoot, path) {
		return "", fmt.Errorf("%w: blob storage_path escapes object store root", workers.ErrInvalid)
	}
	return path, nil
}

func (r ObjectStoreIntegrityRuntime) markBlobVerified(ctx context.Context, blobID string, verifiedAt time.Time) error {
	_, err := r.DB.ExecContext(ctx, `
		UPDATE files.blobs
		SET status = 'verified',
		    verified_at = $2
		WHERE blob_id = $1
	`, blobID, verifiedAt)
	return err
}

func (r ObjectStoreIntegrityRuntime) markBlobStatus(ctx context.Context, blobID, status string) error {
	_, err := r.DB.ExecContext(ctx, `
		UPDATE files.blobs
		SET status = $2
		WHERE blob_id = $1
	`, blobID, status)
	return err
}

func (r ObjectStoreIntegrityRuntime) openBlobFinding(ctx context.Context, operation maintenance.Operation, workerRunID string, candidate objectStoreBlobCandidate, check objectStoreBlobCheck) error {
	details := mustWorkerJSON(map[string]any{
		"schema_version":            "object_store_integrity.finding.v0.2",
		"maintenance_operation_id":  operation.MaintenanceOperationID,
		"blob_id":                   candidate.BlobID,
		"hash_uri":                  candidate.HashURI,
		"storage_path":              candidate.StoragePath,
		"resolved_path":             check.ResolvedPath,
		"expected_size_bytes":       candidate.SizeBytes,
		"actual_size_bytes":         check.ActualSize,
		"expected_sha256":           candidate.HashHex,
		"actual_sha256":             check.ActualSHA256,
		"integrity_error_code":      check.ErrorCode,
		"integrity_error_message":   check.Message,
		"previous_blob_status":      candidate.Status,
		"previous_blob_verified_at": candidate.VerifiedAt,
	})
	_, _, err := r.Maintenance.UpsertFinding(ctx, maintenance.UpsertFindingInput{
		FindingKey:       objectStoreBlobFindingKey(operation.WorkerInstanceID, check.FindingKind, candidate.BlobID),
		WorkerInstanceID: operation.WorkerInstanceID,
		WorkerRunID:      workerRunID,
		FindingKind:      check.FindingKind,
		Severity:         maintenance.SeverityCritical,
		SubjectKind:      "blob",
		SubjectID:        candidate.BlobID,
		Summary:          objectStoreFindingSummary(check.FindingKind),
		DetailsJSON:      details,
		Metadata:         json.RawMessage(`{"schema_version":"maintenance_finding.metadata.v0.2","source":"object_store_integrity"}`),
	})
	return err
}

func (r ObjectStoreIntegrityRuntime) resolveBlobFindings(ctx context.Context, operation maintenance.Operation, candidate objectStoreBlobCandidate) (int64, error) {
	var resolved int64
	for _, kind := range []string{
		maintenance.FindingKindObjectStoreBlobMissing,
		maintenance.FindingKindObjectStoreBlobSizeMismatch,
		maintenance.FindingKindObjectStoreBlobCorrupt,
	} {
		_, err := r.Maintenance.ResolveFinding(ctx, objectStoreBlobFindingKey(operation.WorkerInstanceID, kind, candidate.BlobID), "Object-store blob verified.")
		if err == nil {
			resolved++
			continue
		}
		if errors.Is(err, maintenance.ErrNotFound) {
			continue
		}
		return resolved, err
	}
	return resolved, nil
}

func objectStoreBlobFindingKey(workerInstanceID, kind, blobID string) string {
	return workerInstanceID + ":" + kind + ":blob:" + blobID
}

func objectStoreFindingSummary(kind string) string {
	switch kind {
	case maintenance.FindingKindObjectStoreBlobMissing:
		return "Managed object-store blob file is missing."
	case maintenance.FindingKindObjectStoreBlobSizeMismatch:
		return "Managed object-store blob file size does not match metadata."
	default:
		return "Managed object-store blob hash does not match metadata."
	}
}

func targetBlobRefFromRunMetadata(raw json.RawMessage) string {
	var metadata struct {
		TargetBlobRef   string `json:"target_blob_ref"`
		RequestMetadata struct {
			TargetBlobRef string `json:"target_blob_ref"`
			BlobRef       string `json:"blob_ref"`
		} `json:"request_metadata"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return ""
	}
	if value := strings.TrimSpace(metadata.TargetBlobRef); value != "" {
		return value
	}
	if value := strings.TrimSpace(metadata.RequestMetadata.TargetBlobRef); value != "" {
		return value
	}
	return strings.TrimSpace(metadata.RequestMetadata.BlobRef)
}

func sha256File(path string) (string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
