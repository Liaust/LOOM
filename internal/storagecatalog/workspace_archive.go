package storagecatalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const maxWorkspaceArchiveCatalogEntries = 250_000

// ListWorkspaceArchiveCatalogEntries returns every live catalog entry whose
// entry paths or physical-ref URI is exactly at or below the reviewed source.
// It intentionally does not rely on entry display paths alone: a physical ref
// inside the moved tree must never be left pointing at the old custody path.
func (s Service) ListWorkspaceArchiveCatalogEntries(ctx context.Context, sourceAbsolutePath, sourceRelativePath string) ([]EntryDetail, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("storage catalog database is required")
	}
	if !filepath.IsAbs(sourceAbsolutePath) || filepath.Clean(sourceAbsolutePath) != sourceAbsolutePath {
		return nil, fmt.Errorf("%w: workspace archive source path must be absolute and clean", ErrInvalid)
	}
	relative, err := NormalizeLogicalPath(sourceRelativePath)
	if err != nil || relative != sourceRelativePath {
		return nil, fmt.Errorf("%w: workspace archive source relative path is invalid", ErrInvalid)
	}
	absPrefix := escapeSQLLike(sourceAbsolutePath) + string(filepath.Separator) + "%"
	relPrefix := escapeSQLLike(sourceRelativePath) + "/%"
	fileURI := (&url.URL{Scheme: "file", Path: sourceAbsolutePath}).String()
	fileURIPrefix := escapeSQLLike(fileURI) + "/%"
	rows, err := s.DB.QueryContext(ctx, `
		SELECT DISTINCT entry.storage_entry_id
		FROM storage.storage_entries AS entry
		LEFT JOIN storage.storage_physical_refs AS ref
			ON ref.storage_entry_id = entry.storage_entry_id
		WHERE entry.deleted_at IS NULL
		  AND (
			entry.original_source_path = $1 OR entry.original_source_path LIKE $2 ESCAPE E'\\'
			OR entry.original_source_path = $3 OR entry.original_source_path LIKE $4 ESCAPE E'\\'
			OR entry.original_source_path = $5 OR entry.original_source_path LIKE $6 ESCAPE E'\\'
			OR entry.current_view_path = $1 OR entry.current_view_path LIKE $2 ESCAPE E'\\'
			OR entry.current_view_path = $3 OR entry.current_view_path LIKE $4 ESCAPE E'\\'
			OR entry.current_view_path = $5 OR entry.current_view_path LIKE $6 ESCAPE E'\\'
			OR entry.logical_path = $1 OR entry.logical_path LIKE $2 ESCAPE E'\\'
			OR entry.logical_path = $3 OR entry.logical_path LIKE $4 ESCAPE E'\\'
			OR entry.logical_path = $5 OR entry.logical_path LIKE $6 ESCAPE E'\\'
			OR ref.uri = $1 OR ref.uri LIKE $2 ESCAPE E'\\'
			OR ref.uri = $3 OR ref.uri LIKE $4 ESCAPE E'\\'
			OR ref.uri = $5 OR ref.uri LIKE $6 ESCAPE E'\\'
		  )
		ORDER BY entry.storage_entry_id
		LIMIT $7
	`, sourceAbsolutePath, absPrefix, sourceRelativePath, relPrefix, fileURI, fileURIPrefix, maxWorkspaceArchiveCatalogEntries+1)
	if err != nil {
		return nil, fmt.Errorf("list workspace archive catalog candidates: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) > maxWorkspaceArchiveCatalogEntries {
		return nil, fmt.Errorf("workspace archive catalog evidence exceeds %d entries", maxWorkspaceArchiveCatalogEntries)
	}
	detailsByID, err := s.ListEntryDetails(ctx, ids)
	if err != nil {
		return nil, err
	}
	details := make([]EntryDetail, 0, len(ids))
	for _, id := range ids {
		detail, ok := detailsByID[id]
		if !ok {
			return nil, fmt.Errorf("workspace archive catalog entry %s disappeared during planning", id)
		}
		details = append(details, detail)
	}
	return details, nil
}

// WorkspaceArchiveJournalRecord is the storage-owned, package-neutral view of
// the physical workspace move journal. The archive package validates and
// interprets the versioned JSON contracts.
type WorkspaceArchiveJournalRecord struct {
	OperationID            string
	PlanDigest             string
	PlanJSON               json.RawMessage
	Phase                  string
	LastSafePhase          string
	TerminalStatus         string
	PlannedAt              time.Time
	IntentCommittedAt      *time.Time
	PayloadMovedAt         *time.Time
	ProjectionsCommittedAt *time.Time
	CompletedAt            *time.Time
	UpdatedAt              time.Time
	Findings               []WorkspaceArchiveFindingRecord
	ManifestJSON           json.RawMessage
	EventJSON              json.RawMessage
}

type WorkspaceArchiveFindingRecord struct {
	FindingID  string
	Code       string
	Severity   string
	AtPhase    string
	Summary    string
	Repairable bool
	Evidence   json.RawMessage
	CreatedAt  time.Time
}

type WorkspaceArchiveIntentInput struct {
	OperationID                   string
	SchemaVersion                 string
	EvidenceKind                  string
	OperationKind                 string
	WorkspaceKind                 string
	ObjectID                      string
	Slug                          string
	PlanDigest                    string
	PlanJSON                      json.RawMessage
	SourceRoot                    string
	SourceRelativePath            string
	SourceIdentityJSON            json.RawMessage
	SourceParentIdentityJSON      json.RawMessage
	DestinationRoot               string
	DestinationRelativePath       string
	DestinationIdentityJSON       json.RawMessage
	DestinationParentIdentityJSON json.RawMessage
	InventoryDigest               string
	ActorID                       string
	Reason                        string
	PlannedAt                     time.Time
	IntentCommittedAt             time.Time
}

type WorkspaceArchiveProjectionInput struct {
	OperationID               string
	PlanDigest                string
	SourceAbsolutePath        string
	Rebind                    RebindPathsInput
	ManifestSchemaVersion     string
	EvidenceKind              string
	WorkspaceKind             string
	ObjectID                  string
	Slug                      string
	InventoryDigest           string
	ArchiveSourceIdentityJSON json.RawMessage
	ManifestJSON              json.RawMessage
	AuthenticationKeyID       string
	AuthenticationTag         string
	ArchivedAt                time.Time
	EventID                   string
	EventSchemaVersion        string
	EventKind                 string
	Transition                string
	FromState                 string
	ToState                   string
	SourceRoot                string
	SourceRelativePath        string
	DestinationRoot           string
	DestinationRelativePath   string
	ActorID                   string
	Reason                    string
	EventJSON                 json.RawMessage
	CommittedAt               time.Time
}

type WorkspaceRestoreProjectionInput struct {
	ArchiveOperationID        string
	OperationID               string
	PlanDigest                string
	SourceAbsolutePath        string
	CatalogSourceRelativePath string
	Rebind                    RebindPathsInput
	ExpectedManifestJSON      json.RawMessage
	ManifestJSON              json.RawMessage
	RestorePlanDigest         string
	RestoreOperationID        string
	AuthenticationKeyID       string
	AuthenticationTag         string
	RestoredAt                time.Time
	EventID                   string
	EventSchemaVersion        string
	EventKind                 string
	WorkspaceKind             string
	ObjectID                  string
	Slug                      string
	Transition                string
	FromState                 string
	ToState                   string
	SourceRoot                string
	SourceRelativePath        string
	DestinationRoot           string
	DestinationRelativePath   string
	ActorID                   string
	Reason                    string
	EventJSON                 json.RawMessage
	CommittedAt               time.Time
}

type WorkspaceArchiveFailureInput struct {
	OperationID   string
	PlanDigest    string
	Phase         string
	LastSafePhase string
	Status        string
	FindingID     string
	FindingCode   string
	Severity      string
	AtPhase       string
	Summary       string
	Repairable    bool
	EvidenceJSON  json.RawMessage
	RecordedAt    time.Time
}

func (s Service) LoadWorkspaceArchiveJournal(ctx context.Context, operationID string) (WorkspaceArchiveJournalRecord, bool, error) {
	if s.DB == nil {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("storage catalog database is required")
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("%w: workspace archive operation id is required", ErrInvalid)
	}
	return loadWorkspaceArchiveJournal(ctx, s.DB, operationID)
}

// LoadWorkspaceArchiveJournalTx reads within the caller's existing transaction.
// It neither mutates the journal nor commits/rolls back the caller's work.
func LoadWorkspaceArchiveJournalTx(ctx context.Context, tx *sql.Tx, operationID string) (WorkspaceArchiveJournalRecord, bool, error) {
	if tx == nil || strings.TrimSpace(operationID) == "" {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("%w: transaction and workspace operation are required", ErrInvalid)
	}
	return loadWorkspaceArchiveJournal(ctx, tx, operationID)
}

func loadWorkspaceArchiveJournal(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, operationID string) (WorkspaceArchiveJournalRecord, bool, error) {
	var record WorkspaceArchiveJournalRecord
	var lastSafe sql.NullString
	var intent, moved, projected, completed sql.NullTime
	var plan []byte
	err := queryer.QueryRowContext(ctx, `
		SELECT workspace_archive_operation_id, plan_digest, plan_json, phase,
			last_safe_phase, terminal_status, planned_at, intent_committed_at,
			payload_moved_at, projections_committed_at, completed_at, updated_at
		FROM storage.workspace_archive_operations
		WHERE workspace_archive_operation_id = $1
	`, operationID).Scan(
		&record.OperationID, &record.PlanDigest, &plan, &record.Phase,
		&lastSafe, &record.TerminalStatus, &record.PlannedAt, &intent,
		&moved, &projected, &completed, &record.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return WorkspaceArchiveJournalRecord{}, false, nil
	}
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("load workspace archive operation: %w", err)
	}
	record.PlanJSON = normalizedScannedJSON(plan)
	record.LastSafePhase = lastSafe.String
	record.IntentCommittedAt = nullableTimePtr(intent)
	record.PayloadMovedAt = nullableTimePtr(moved)
	record.ProjectionsCommittedAt = nullableTimePtr(projected)
	record.CompletedAt = nullableTimePtr(completed)

	rows, err := queryer.QueryContext(ctx, `
		SELECT workspace_archive_finding_id, finding_code, severity, at_phase,
			summary, repairable, evidence_json, created_at
		FROM storage.workspace_archive_findings
		WHERE workspace_archive_operation_id = $1
		ORDER BY created_at, workspace_archive_finding_id
	`, operationID)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("load workspace archive findings: %w", err)
	}
	for rows.Next() {
		var finding WorkspaceArchiveFindingRecord
		var evidence []byte
		if err := rows.Scan(&finding.FindingID, &finding.Code, &finding.Severity, &finding.AtPhase, &finding.Summary, &finding.Repairable, &evidence, &finding.CreatedAt); err != nil {
			_ = rows.Close()
			return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("scan workspace archive finding: %w", err)
		}
		finding.Evidence = normalizedScannedJSON(evidence)
		record.Findings = append(record.Findings, finding)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return WorkspaceArchiveJournalRecord{}, false, err
	}
	if err := rows.Close(); err != nil {
		return WorkspaceArchiveJournalRecord{}, false, err
	}

	var manifest []byte
	if err := queryer.QueryRowContext(ctx, `
		SELECT manifest_json
		FROM storage.workspace_archive_manifests
		WHERE workspace_archive_operation_id = $1 OR restore_operation_id = $1
		ORDER BY CASE WHEN workspace_archive_operation_id = $1 THEN 0 ELSE 1 END
		LIMIT 1
	`, operationID).Scan(&manifest); err != nil && err != sql.ErrNoRows {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("load workspace archive manifest: %w", err)
	} else if err == nil {
		record.ManifestJSON = normalizedScannedJSON(manifest)
	}
	var event []byte
	if err := queryer.QueryRowContext(ctx, `SELECT details FROM storage.workspace_lifecycle_events WHERE workspace_archive_operation_id = $1`, operationID).Scan(&event); err != nil && err != sql.ErrNoRows {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("load workspace lifecycle event: %w", err)
	} else if err == nil {
		record.EventJSON = normalizedScannedJSON(event)
	}
	return record, true, nil
}

func (s Service) CommitWorkspaceArchiveIntent(ctx context.Context, input WorkspaceArchiveIntentInput) (WorkspaceArchiveJournalRecord, bool, error) {
	if s.DB == nil {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("storage catalog database is required")
	}
	plan, err := normalizeWorkspaceJSONObject(input.PlanJSON)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("%w: invalid workspace archive plan JSON: %v", ErrInvalid, err)
	}
	if input.OperationID == "" || input.PlanDigest == "" || input.IntentCommittedAt.IsZero() || input.PlannedAt.IsZero() {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("%w: incomplete workspace archive intent", ErrInvalid)
	}
	phase := ""
	switch input.OperationKind {
	case "archive":
		phase = "archive_intent_committed"
	case "restore":
		phase = "restore_intent_committed"
	default:
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("%w: unsupported workspace operation kind", ErrInvalid)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, false, err
	}
	defer tx.Rollback()
	if err := LockWorkspaceCustodyWriterTx(ctx, tx); err != nil {
		return WorkspaceArchiveJournalRecord{}, false, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO storage.workspace_archive_operations (
			workspace_archive_operation_id, schema_version, evidence_kind,
			operation_kind, workspace_kind, object_id, slug, phase,
			terminal_status, plan_digest, plan_json, source_root,
			source_relative_path, source_identity_json, source_parent_identity_json,
			destination_root, destination_relative_path, destination_identity_json,
			destination_parent_identity_json, inventory_digest, actor_id, reason,
			planned_at, intent_committed_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $23,
			'running', $8, $9, $10, $11, $12, $13, $14, $15, $16,
			$17, $18, $19, $20, $21, $22, $22
		)
		ON CONFLICT (workspace_archive_operation_id) DO NOTHING
	`, input.OperationID, input.SchemaVersion, input.EvidenceKind, input.OperationKind,
		input.WorkspaceKind, input.ObjectID, input.Slug, input.PlanDigest, plan,
		input.SourceRoot, input.SourceRelativePath, input.SourceIdentityJSON,
		input.SourceParentIdentityJSON, input.DestinationRoot,
		input.DestinationRelativePath, input.DestinationIdentityJSON,
		input.DestinationParentIdentityJSON, input.InventoryDigest, input.ActorID,
		input.Reason, input.PlannedAt, input.IntentCommittedAt, phase)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, false, fmt.Errorf("commit workspace archive intent: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceArchiveJournalRecord{}, false, err
	}
	record, exists, err := s.LoadWorkspaceArchiveJournal(ctx, input.OperationID)
	if err != nil || !exists {
		return WorkspaceArchiveJournalRecord{}, false, err
	}
	return record, rows == 1, nil
}

// Short database writers finish before durable move intent becomes visible.
// The mover releases this fence before doing any physical filesystem operation.
func LockWorkspaceCustodyWriterTx(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return fmt.Errorf("%w: workspace custody transaction is required", ErrInvalid)
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('storage.workspace_custody',0))`)
	return err
}

func LockWorkspaceCustodyReaderTx(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return fmt.Errorf("%w: workspace custody transaction is required", ErrInvalid)
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('storage.workspace_custody',0))`)
	return err
}

func (s Service) MarkWorkspaceArchivePayloadMoved(ctx context.Context, operationID, planDigest string, movedAt time.Time) (WorkspaceArchiveJournalRecord, error) {
	if s.DB == nil {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("storage catalog database is required")
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE storage.workspace_archive_operations
		SET phase = 'archive_payload_moved', last_safe_phase = NULL,
			terminal_status = 'running', payload_moved_at = COALESCE(payload_moved_at, $3),
			updated_at = GREATEST(updated_at, $3)
		WHERE workspace_archive_operation_id = $1 AND plan_digest = $2
			AND operation_kind = 'archive'
			AND (phase = 'archive_intent_committed'
				OR (phase = 'blocked' AND last_safe_phase = 'archive_intent_committed'))
	`, operationID, planDigest, movedAt)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("mark workspace archive payload moved: %w", err)
	}
	record, exists, err := s.LoadWorkspaceArchiveJournal(ctx, operationID)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, err
	}
	if !exists || record.PlanDigest != planDigest {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("workspace archive operation is missing or conflicts with reviewed plan")
	}
	if record.Phase != "archive_payload_moved" && record.Phase != "archive_projections_committed" && record.Phase != "archive_complete" {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("workspace archive operation cannot advance payload from phase %q", record.Phase)
	}
	return record, nil
}

func (s Service) MarkWorkspaceRestorePayloadMoved(ctx context.Context, operationID, planDigest string, movedAt time.Time) (WorkspaceArchiveJournalRecord, error) {
	if s.DB == nil {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("storage catalog database is required")
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE storage.workspace_archive_operations
		SET phase = 'restore_payload_moved', last_safe_phase = NULL,
			terminal_status = 'running', payload_moved_at = COALESCE(payload_moved_at, $3),
			updated_at = GREATEST(updated_at, $3)
		WHERE workspace_archive_operation_id = $1 AND plan_digest = $2
			AND operation_kind = 'restore'
			AND (phase = 'restore_intent_committed'
				OR (phase = 'blocked' AND last_safe_phase = 'restore_intent_committed'))
	`, operationID, planDigest, movedAt)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("mark workspace restore payload moved: %w", err)
	}
	record, exists, err := s.LoadWorkspaceArchiveJournal(ctx, operationID)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, err
	}
	if !exists || record.PlanDigest != planDigest {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("workspace restore operation is missing or conflicts with reviewed plan")
	}
	if record.Phase != "restore_payload_moved" && record.Phase != "restore_projections_committed" && record.Phase != "restore_complete" {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("workspace restore operation cannot advance payload from phase %q", record.Phase)
	}
	return record, nil
}

func (s Service) CommitWorkspaceArchiveProjection(ctx context.Context, input WorkspaceArchiveProjectionInput) (record WorkspaceArchiveJournalRecord, err error) {
	if s.DB == nil {
		return record, fmt.Errorf("storage catalog database is required")
	}
	manifest, err := normalizeWorkspaceJSONObject(input.ManifestJSON)
	if err != nil {
		return record, fmt.Errorf("%w: invalid physical archive manifest JSON: %v", ErrInvalid, err)
	}
	eventJSON, err := normalizeWorkspaceJSONObject(input.EventJSON)
	if err != nil {
		return record, fmt.Errorf("%w: invalid workspace lifecycle event JSON: %v", ErrInvalid, err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return record, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var phase, digest string
	var lastSafe sql.NullString
	if err = tx.QueryRowContext(ctx, `
		SELECT phase, plan_digest, last_safe_phase FROM storage.workspace_archive_operations
		WHERE workspace_archive_operation_id = $1 FOR UPDATE
	`, input.OperationID).Scan(&phase, &digest, &lastSafe); err != nil {
		return record, fmt.Errorf("lock workspace archive operation: %w", err)
	}
	if digest != input.PlanDigest {
		return record, fmt.Errorf("workspace archive projection conflicts with reviewed plan")
	}
	if phase != "archive_payload_moved" && phase != "archive_projections_committed" && phase != "archive_complete" && !(phase == "blocked" && lastSafe.String == "archive_payload_moved") {
		return record, fmt.Errorf("workspace archive projection cannot commit from phase %q", phase)
	}
	if _, err = tx.ExecContext(ctx, `LOCK TABLE storage.storage_entries, storage.storage_physical_refs IN SHARE MODE`); err != nil {
		return record, fmt.Errorf("lock workspace archive catalog writers: %w", err)
	}
	if err = verifyWorkspaceArchiveCatalogEvidenceTx(ctx, tx, workspaceCatalogProjectionEvidence{
		SourceAbsolutePath: input.SourceAbsolutePath,
		SourceRelativePath: input.SourceRelativePath,
		Rebind:             input.Rebind,
		AlreadyCommitted:   phase == "archive_projections_committed" || phase == "archive_complete",
	}); err != nil {
		return record, err
	}
	if len(input.Rebind.PhysicalRefs) > 0 || len(input.Rebind.Entries) > 0 {
		normalized, normalizeErr := normalizeRebindPathsInput(input.Rebind)
		if normalizeErr != nil {
			return record, normalizeErr
		}
		var rebindResult RebindPathsResult
		if err = rebindPathsTx(ctx, tx, normalized, &rebindResult); err != nil {
			return record, err
		}
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO storage.workspace_archive_manifests (
			workspace_archive_operation_id, schema_version, evidence_kind,
			workspace_kind, object_id, slug, lifecycle_state, plan_digest,
			inventory_digest, archive_source_identity_json, manifest_json,
			authentication_key_id, authentication_tag, archived_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,'archived',$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (workspace_archive_operation_id) DO NOTHING
	`, input.OperationID, input.ManifestSchemaVersion, input.EvidenceKind,
		input.WorkspaceKind, input.ObjectID, input.Slug, input.PlanDigest,
		input.InventoryDigest, input.ArchiveSourceIdentityJSON, manifest,
		input.AuthenticationKeyID, input.AuthenticationTag, input.ArchivedAt,
		input.CommittedAt); err != nil {
		return record, fmt.Errorf("insert workspace archive manifest: %w", err)
	}
	var storedManifest []byte
	if err = tx.QueryRowContext(ctx, `SELECT manifest_json FROM storage.workspace_archive_manifests WHERE workspace_archive_operation_id = $1`, input.OperationID).Scan(&storedManifest); err != nil {
		return record, err
	}
	if !jsonDocumentsEqual(storedManifest, manifest) {
		return record, fmt.Errorf("workspace archive manifest replay conflict")
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO storage.workspace_lifecycle_events (
			workspace_lifecycle_event_id, schema_version, event_kind,
			workspace_archive_operation_id, operation_kind, workspace_kind,
			object_id, slug, transition, from_state, to_state, source_root,
			source_relative_path, destination_root, destination_relative_path,
			actor_id, reason, details, occurred_at
		) VALUES ($1,$2,$3,$4,'archive',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (workspace_archive_operation_id) DO NOTHING
	`, input.EventID, input.EventSchemaVersion, input.EventKind, input.OperationID,
		input.WorkspaceKind, input.ObjectID, input.Slug, input.Transition,
		input.FromState, input.ToState, input.SourceRoot, input.SourceRelativePath,
		input.DestinationRoot, input.DestinationRelativePath, input.ActorID,
		input.Reason, eventJSON, input.CommittedAt); err != nil {
		return record, fmt.Errorf("insert workspace lifecycle event: %w", err)
	}
	var storedEvent []byte
	if err = tx.QueryRowContext(ctx, `SELECT details FROM storage.workspace_lifecycle_events WHERE workspace_archive_operation_id = $1`, input.OperationID).Scan(&storedEvent); err != nil {
		return record, err
	}
	if !jsonDocumentsEqual(storedEvent, eventJSON) {
		return record, fmt.Errorf("workspace lifecycle event replay conflict")
	}
	if phase != "archive_complete" {
		if _, err = tx.ExecContext(ctx, `
			UPDATE storage.workspace_archive_operations
			SET phase = 'archive_projections_committed', last_safe_phase = NULL,
				terminal_status = 'running', projections_committed_at = COALESCE(projections_committed_at, $3),
				updated_at = GREATEST(updated_at, $3)
			WHERE workspace_archive_operation_id = $1 AND plan_digest = $2
		`, input.OperationID, input.PlanDigest, input.CommittedAt); err != nil {
			return record, err
		}
	}
	if err = tx.Commit(); err != nil {
		return record, err
	}
	record, exists, err := s.LoadWorkspaceArchiveJournal(ctx, input.OperationID)
	if err != nil {
		return record, err
	}
	if !exists {
		return record, fmt.Errorf("workspace archive operation disappeared after projection commit")
	}
	return record, nil
}

func (s Service) CommitWorkspaceRestoreProjection(ctx context.Context, input WorkspaceRestoreProjectionInput) (record WorkspaceArchiveJournalRecord, err error) {
	if s.DB == nil {
		return record, fmt.Errorf("storage catalog database is required")
	}
	expectedManifest, err := normalizeWorkspaceJSONObject(input.ExpectedManifestJSON)
	if err != nil {
		return record, fmt.Errorf("%w: invalid expected workspace archive manifest JSON: %v", ErrInvalid, err)
	}
	manifest, err := normalizeWorkspaceJSONObject(input.ManifestJSON)
	if err != nil {
		return record, fmt.Errorf("%w: invalid restored workspace manifest JSON: %v", ErrInvalid, err)
	}
	eventJSON, err := normalizeWorkspaceJSONObject(input.EventJSON)
	if err != nil {
		return record, fmt.Errorf("%w: invalid workspace restore lifecycle event JSON: %v", ErrInvalid, err)
	}
	if input.ArchiveOperationID == "" || input.OperationID == "" || input.OperationID != input.RestoreOperationID || input.PlanDigest == "" || input.RestorePlanDigest != input.PlanDigest || input.RestoredAt.IsZero() || input.CommittedAt.IsZero() {
		return record, fmt.Errorf("%w: incomplete workspace restore projection", ErrInvalid)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return record, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var phase, digest string
	var lastSafe sql.NullString
	if err = tx.QueryRowContext(ctx, `
		SELECT phase, plan_digest, last_safe_phase
		FROM storage.workspace_archive_operations
		WHERE workspace_archive_operation_id = $1 AND operation_kind = 'restore'
		FOR UPDATE
	`, input.OperationID).Scan(&phase, &digest, &lastSafe); err != nil {
		return record, fmt.Errorf("lock workspace restore operation: %w", err)
	}
	if digest != input.PlanDigest {
		return record, fmt.Errorf("workspace restore projection conflicts with reviewed plan")
	}
	alreadyCommitted := phase == "restore_projections_committed" || phase == "restore_complete"
	if phase != "restore_payload_moved" && !alreadyCommitted && !(phase == "blocked" && lastSafe.String == "restore_payload_moved") {
		return record, fmt.Errorf("workspace restore projection cannot commit from phase %q", phase)
	}
	if _, err = tx.ExecContext(ctx, `LOCK TABLE storage.storage_entries, storage.storage_physical_refs IN SHARE MODE`); err != nil {
		return record, fmt.Errorf("lock workspace restore catalog writers: %w", err)
	}
	if err = verifyWorkspaceArchiveCatalogEvidenceTx(ctx, tx, workspaceCatalogProjectionEvidence{
		SourceAbsolutePath: input.SourceAbsolutePath,
		SourceRelativePath: input.CatalogSourceRelativePath,
		Rebind:             input.Rebind,
		AlreadyCommitted:   alreadyCommitted,
	}); err != nil {
		return record, err
	}
	if !alreadyCommitted && (len(input.Rebind.PhysicalRefs) > 0 || len(input.Rebind.Entries) > 0) {
		normalized, normalizeErr := normalizeRebindPathsInput(input.Rebind)
		if normalizeErr != nil {
			return record, normalizeErr
		}
		var rebindResult RebindPathsResult
		if err = rebindPathsTx(ctx, tx, normalized, &rebindResult); err != nil {
			return record, err
		}
	}
	var lifecycleState string
	var storedRestoreOperationID, storedRestorePlanDigest sql.NullString
	var storedManifest []byte
	if err = tx.QueryRowContext(ctx, `
		SELECT lifecycle_state, restore_operation_id, restore_plan_digest, manifest_json
		FROM storage.workspace_archive_manifests
		WHERE workspace_archive_operation_id = $1
		FOR UPDATE
	`, input.ArchiveOperationID).Scan(&lifecycleState, &storedRestoreOperationID, &storedRestorePlanDigest, &storedManifest); err != nil {
		return record, fmt.Errorf("lock workspace archive manifest for restore: %w", err)
	}
	switch lifecycleState {
	case "archived":
		if !jsonDocumentsEqual(storedManifest, expectedManifest) {
			return record, fmt.Errorf("workspace archive manifest changed after restore review")
		}
		result, updateErr := tx.ExecContext(ctx, `
			UPDATE storage.workspace_archive_manifests
			SET lifecycle_state = 'active', restore_operation_id = $2,
				restore_operation_kind = 'restore', restore_plan_digest = $3,
				manifest_json = $4, authentication_key_id = $5,
				authentication_tag = $6, restored_at = $7,
				updated_at = GREATEST(updated_at, $8)
			WHERE workspace_archive_operation_id = $1
				AND lifecycle_state = 'archived' AND manifest_json = $9
		`, input.ArchiveOperationID, input.RestoreOperationID, input.RestorePlanDigest,
			manifest, input.AuthenticationKeyID, input.AuthenticationTag,
			input.RestoredAt, input.CommittedAt, expectedManifest)
		if updateErr != nil {
			return record, fmt.Errorf("update workspace archive manifest for restore: %w", updateErr)
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			return record, fmt.Errorf("workspace archive manifest changed during restore projection")
		}
	case "active":
		if storedRestoreOperationID.String != input.RestoreOperationID || storedRestorePlanDigest.String != input.RestorePlanDigest || !jsonDocumentsEqual(storedManifest, manifest) {
			return record, fmt.Errorf("workspace restore manifest replay conflict")
		}
	default:
		return record, fmt.Errorf("workspace archive manifest has unsupported lifecycle state %q", lifecycleState)
	}
	if err = tx.QueryRowContext(ctx, `
		SELECT manifest_json
		FROM storage.workspace_archive_manifests
		WHERE workspace_archive_operation_id = $1
	`, input.ArchiveOperationID).Scan(&storedManifest); err != nil {
		return record, err
	}
	if !jsonDocumentsEqual(storedManifest, manifest) {
		return record, fmt.Errorf("workspace restored manifest replay conflict")
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO storage.workspace_lifecycle_events (
			workspace_lifecycle_event_id, schema_version, event_kind,
			workspace_archive_operation_id, operation_kind, workspace_kind,
			object_id, slug, transition, from_state, to_state, source_root,
			source_relative_path, destination_root, destination_relative_path,
			actor_id, reason, details, occurred_at
		) VALUES ($1,$2,$3,$4,'restore',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (workspace_archive_operation_id) DO NOTHING
	`, input.EventID, input.EventSchemaVersion, input.EventKind, input.OperationID,
		input.WorkspaceKind, input.ObjectID, input.Slug, input.Transition,
		input.FromState, input.ToState, input.SourceRoot, input.SourceRelativePath,
		input.DestinationRoot, input.DestinationRelativePath, input.ActorID,
		input.Reason, eventJSON, input.CommittedAt); err != nil {
		return record, fmt.Errorf("insert workspace restore lifecycle event: %w", err)
	}
	var storedEvent []byte
	if err = tx.QueryRowContext(ctx, `SELECT details FROM storage.workspace_lifecycle_events WHERE workspace_archive_operation_id = $1`, input.OperationID).Scan(&storedEvent); err != nil {
		return record, err
	}
	if !jsonDocumentsEqual(storedEvent, eventJSON) {
		return record, fmt.Errorf("workspace restore lifecycle event replay conflict")
	}
	if !alreadyCommitted {
		if _, err = tx.ExecContext(ctx, `
			UPDATE storage.workspace_archive_operations
			SET phase = 'restore_projections_committed', last_safe_phase = NULL,
				terminal_status = 'running', projections_committed_at = COALESCE(projections_committed_at, $3),
				updated_at = GREATEST(updated_at, $3)
			WHERE workspace_archive_operation_id = $1 AND plan_digest = $2
		`, input.OperationID, input.PlanDigest, input.CommittedAt); err != nil {
			return record, err
		}
	}
	if err = tx.Commit(); err != nil {
		return record, err
	}
	record, exists, err := s.LoadWorkspaceArchiveJournal(ctx, input.OperationID)
	if err != nil {
		return record, err
	}
	if !exists {
		return record, fmt.Errorf("workspace restore operation disappeared after projection commit")
	}
	return record, nil
}

type workspaceCatalogProjectionEvidence struct {
	SourceAbsolutePath string
	SourceRelativePath string
	Rebind             RebindPathsInput
	AlreadyCommitted   bool
}

func verifyWorkspaceArchiveCatalogEvidenceTx(ctx context.Context, tx *sql.Tx, input workspaceCatalogProjectionEvidence) error {
	if !filepath.IsAbs(input.SourceAbsolutePath) || filepath.Clean(input.SourceAbsolutePath) != input.SourceAbsolutePath {
		return fmt.Errorf("%w: workspace archive projection source path must be absolute and clean", ErrInvalid)
	}
	relative, err := NormalizeLogicalPath(input.SourceRelativePath)
	if err != nil || relative != input.SourceRelativePath {
		return fmt.Errorf("%w: workspace archive projection source relative path is invalid", ErrInvalid)
	}
	expected := RebindPathsInput{}
	if len(input.Rebind.PhysicalRefs) > 0 || len(input.Rebind.Entries) > 0 {
		expected, err = normalizeRebindPathsInput(input.Rebind)
		if err != nil {
			return err
		}
	}
	absPrefix := escapeSQLLike(input.SourceAbsolutePath) + string(filepath.Separator) + "%"
	relPrefix := escapeSQLLike(input.SourceRelativePath) + "/%"
	fileURI := (&url.URL{Scheme: "file", Path: input.SourceAbsolutePath}).String()
	fileURIPrefix := escapeSQLLike(fileURI) + "/%"

	entryRows, err := tx.QueryContext(ctx, `
		SELECT storage_entry_id, original_source_path, current_view_path
		FROM storage.storage_entries
		WHERE deleted_at IS NULL
		  AND (
			original_source_path = $1 OR original_source_path LIKE $2 ESCAPE E'\\'
			OR original_source_path = $3 OR original_source_path LIKE $4 ESCAPE E'\\'
			OR original_source_path = $5 OR original_source_path LIKE $6 ESCAPE E'\\'
			OR current_view_path = $1 OR current_view_path LIKE $2 ESCAPE E'\\'
			OR current_view_path = $3 OR current_view_path LIKE $4 ESCAPE E'\\'
			OR current_view_path = $5 OR current_view_path LIKE $6 ESCAPE E'\\'
		  )
		ORDER BY storage_entry_id
		LIMIT $7
	`, input.SourceAbsolutePath, absPrefix, input.SourceRelativePath, relPrefix, fileURI, fileURIPrefix, maxWorkspaceArchiveCatalogEntries+1)
	if err != nil {
		return fmt.Errorf("recheck workspace archive catalog entries: %w", err)
	}
	defer entryRows.Close()
	entryByID := make(map[string]EntryPathRebind, len(expected.Entries))
	for _, item := range expected.Entries {
		entryByID[item.StorageEntryID] = item
	}
	observed := RebindPathsInput{}
	for entryRows.Next() {
		var id, original, current string
		if err := entryRows.Scan(&id, &original, &current); err != nil {
			return err
		}
		item, ok := entryByID[id]
		if !ok || item.ExpectedOriginalSourcePath != original || item.ExpectedCurrentViewPath != current {
			return fmt.Errorf("workspace archive catalog entries changed after review")
		}
		observed.Entries = append(observed.Entries, item)
		if len(observed.Entries) > maxWorkspaceArchiveCatalogEntries {
			return fmt.Errorf("workspace archive catalog evidence exceeds %d entries", maxWorkspaceArchiveCatalogEntries)
		}
	}
	if err := entryRows.Err(); err != nil {
		return err
	}
	if err := entryRows.Close(); err != nil {
		return err
	}

	refRows, err := tx.QueryContext(ctx, `
		SELECT ref.storage_physical_ref_id, ref.storage_entry_id, ref.uri
		FROM storage.storage_physical_refs AS ref
		JOIN storage.storage_entries AS entry
		  ON entry.storage_entry_id = ref.storage_entry_id
		WHERE entry.deleted_at IS NULL
		  AND (
			ref.uri = $1 OR ref.uri LIKE $2 ESCAPE E'\\'
			OR ref.uri = $3 OR ref.uri LIKE $4 ESCAPE E'\\'
			OR ref.uri = $5 OR ref.uri LIKE $6 ESCAPE E'\\'
		  )
		ORDER BY ref.storage_entry_id, ref.storage_physical_ref_id
		LIMIT $7
	`, input.SourceAbsolutePath, absPrefix, input.SourceRelativePath, relPrefix, fileURI, fileURIPrefix, maxWorkspaceArchiveCatalogEntries+1)
	if err != nil {
		return fmt.Errorf("recheck workspace archive catalog physical refs: %w", err)
	}
	defer refRows.Close()
	refByID := make(map[string]PhysicalRefPathRebind, len(expected.PhysicalRefs))
	for _, item := range expected.PhysicalRefs {
		refByID[item.StoragePhysicalRefID] = item
	}
	for refRows.Next() {
		var id, entryID, uri string
		if err := refRows.Scan(&id, &entryID, &uri); err != nil {
			return err
		}
		item, ok := refByID[id]
		if !ok || item.StorageEntryID != entryID || item.ExpectedURI != uri {
			return fmt.Errorf("workspace archive catalog physical refs changed after review")
		}
		observed.PhysicalRefs = append(observed.PhysicalRefs, item)
		if len(observed.PhysicalRefs) > maxWorkspaceArchiveCatalogEntries {
			return fmt.Errorf("workspace archive catalog evidence exceeds %d physical refs", maxWorkspaceArchiveCatalogEntries)
		}
	}
	if err := refRows.Err(); err != nil {
		return err
	}
	if err := refRows.Close(); err != nil {
		return err
	}

	if len(observed.PhysicalRefs) > 0 || len(observed.Entries) > 0 {
		observed, err = normalizeRebindPathsInput(observed)
		if err != nil {
			return err
		}
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	observedJSON, err := json.Marshal(observed)
	if err != nil {
		return err
	}
	if input.AlreadyCommitted {
		emptyJSON, _ := json.Marshal(RebindPathsInput{})
		if !bytes.Equal(observedJSON, emptyJSON) {
			return fmt.Errorf("workspace archive catalog source paths remain after projection")
		}
		return nil
	}
	if !bytes.Equal(observedJSON, expectedJSON) {
		return fmt.Errorf("workspace archive catalog evidence changed after review")
	}
	return nil
}

func (s Service) CompleteWorkspaceArchive(ctx context.Context, operationID, planDigest string, completedAt time.Time) (WorkspaceArchiveJournalRecord, error) {
	if s.DB == nil {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("storage catalog database is required")
	}
	if _, err := s.DB.ExecContext(ctx, `
		UPDATE storage.workspace_archive_operations
		SET phase = 'archive_complete', last_safe_phase = NULL,
			terminal_status = 'complete', completed_at = COALESCE(completed_at, $3),
			updated_at = GREATEST(updated_at, $3)
		WHERE workspace_archive_operation_id = $1 AND plan_digest = $2
			AND operation_kind = 'archive'
			AND (phase = 'archive_projections_committed'
				OR (phase = 'blocked' AND last_safe_phase = 'archive_projections_committed'))
	`, operationID, planDigest, completedAt); err != nil {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("complete workspace archive operation: %w", err)
	}
	record, exists, err := s.LoadWorkspaceArchiveJournal(ctx, operationID)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, err
	}
	if !exists || record.PlanDigest != planDigest || record.Phase != "archive_complete" {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("workspace archive operation did not reach complete")
	}
	return record, nil
}

func (s Service) CompleteWorkspaceRestore(ctx context.Context, operationID, planDigest string, completedAt time.Time) (WorkspaceArchiveJournalRecord, error) {
	if s.DB == nil {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("storage catalog database is required")
	}
	if _, err := s.DB.ExecContext(ctx, `
		UPDATE storage.workspace_archive_operations
		SET phase = 'restore_complete', last_safe_phase = NULL,
			terminal_status = 'complete', completed_at = COALESCE(completed_at, $3),
			updated_at = GREATEST(updated_at, $3)
		WHERE workspace_archive_operation_id = $1 AND plan_digest = $2
			AND operation_kind = 'restore'
			AND (phase = 'restore_projections_committed'
				OR (phase = 'blocked' AND last_safe_phase = 'restore_projections_committed'))
	`, operationID, planDigest, completedAt); err != nil {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("complete workspace restore operation: %w", err)
	}
	record, exists, err := s.LoadWorkspaceArchiveJournal(ctx, operationID)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, err
	}
	if !exists || record.PlanDigest != planDigest || record.Phase != "restore_complete" {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("workspace restore operation did not reach complete")
	}
	return record, nil
}

func (s Service) RecordWorkspaceArchiveFailure(ctx context.Context, input WorkspaceArchiveFailureInput) (WorkspaceArchiveJournalRecord, error) {
	if s.DB == nil {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("storage catalog database is required")
	}
	evidence := input.EvidenceJSON
	if len(evidence) == 0 {
		evidence = json.RawMessage(`[]`)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE storage.workspace_archive_operations
		SET phase = $3, last_safe_phase = $4, terminal_status = $5,
			updated_at = GREATEST(updated_at, $6)
		WHERE workspace_archive_operation_id = $1 AND plan_digest = $2
			AND (phase = $4 OR (phase IN ('blocked', 'manual_repair_required') AND last_safe_phase = $4))
	`, input.OperationID, input.PlanDigest, input.Phase, input.LastSafePhase,
		input.Status, input.RecordedAt)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, err
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("workspace archive failure did not bind an existing reviewed operation")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO storage.workspace_archive_findings (
			workspace_archive_finding_id, workspace_archive_operation_id,
			schema_version, finding_code, severity, at_phase, summary,
			repairable, evidence_json, created_at
		)
		SELECT $1,$2,'storage.workspace_archive_finding.v1',$3,$4,$5,$6,$7,$8,$9
		WHERE NOT EXISTS (
			SELECT 1 FROM storage.workspace_archive_findings
			WHERE workspace_archive_operation_id = $2 AND finding_code = $3
				AND at_phase = $5 AND summary = $6 AND evidence_json = $8
		)
	`, input.FindingID, input.OperationID, input.FindingCode, input.Severity,
		input.AtPhase, input.Summary, input.Repairable, evidence, input.RecordedAt); err != nil {
		return WorkspaceArchiveJournalRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceArchiveJournalRecord{}, err
	}
	record, exists, err := s.LoadWorkspaceArchiveJournal(ctx, input.OperationID)
	if err != nil {
		return WorkspaceArchiveJournalRecord{}, err
	}
	if !exists {
		return WorkspaceArchiveJournalRecord{}, fmt.Errorf("workspace archive operation disappeared after failure record")
	}
	return record, nil
}

func nullableTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

// normalizeWorkspaceJSONObject validates the top-level contract without
// decoding numbers through float64. Filesystem device and inode identities are
// uint64 evidence and must reach PostgreSQL byte-exactly.
func normalizeWorkspaceJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		return nil, fmt.Errorf("must be an object")
	}
	return append(json.RawMessage(nil), raw...), nil
}

func jsonDocumentsEqual(left, right []byte) bool {
	leftValue, leftOK := decodeLosslessJSON(left)
	rightValue, rightOK := decodeLosslessJSON(right)
	return leftOK && rightOK && losslessJSONValuesEqual(leftValue, rightValue)
}

func decodeLosslessJSON(payload []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return value, true
}

func losslessJSONValuesEqual(left, right any) bool {
	switch leftValue := left.(type) {
	case nil:
		return right == nil
	case bool:
		rightValue, ok := right.(bool)
		return ok && leftValue == rightValue
	case string:
		rightValue, ok := right.(string)
		return ok && leftValue == rightValue
	case json.Number:
		rightValue, ok := right.(json.Number)
		if !ok {
			return false
		}
		leftNumber, leftOK := new(big.Rat).SetString(string(leftValue))
		rightNumber, rightOK := new(big.Rat).SetString(string(rightValue))
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for index := range leftValue {
			if !losslessJSONValuesEqual(leftValue[index], rightValue[index]) {
				return false
			}
		}
		return true
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, leftItem := range leftValue {
			rightItem, exists := rightValue[key]
			if !exists || !losslessJSONValuesEqual(leftItem, rightItem) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
