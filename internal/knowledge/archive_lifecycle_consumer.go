package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"loom.local/loom/internal/storagearchive"
)

type NotesCustodyFindingCode string

const (
	NotesCustodyUpstreamUnavailable NotesCustodyFindingCode = "upstream_evidence_unavailable"
	NotesCustodyProjectUnavailable  NotesCustodyFindingCode = "project_completion_unavailable"
	NotesCustodySourceConflict      NotesCustodyFindingCode = "source_evidence_conflict"
	NotesCustodyArchivePending      NotesCustodyFindingCode = "archive_projection_pending"
	NotesCustodyReceiptConflict     NotesCustodyFindingCode = "projection_receipt_conflict"
	NotesCustodyDatabaseRetryable   NotesCustodyFindingCode = "database_retryable"
	NotesCustodyProjectionFailed    NotesCustodyFindingCode = "projection_failed"
)

// Causes remain available internally; batch output exposes only closed codes.
type notesCustodyError struct {
	code  NotesCustodyFindingCode
	cause error
}

func (e *notesCustodyError) Error() string { return string(e.code) }
func (e *notesCustodyError) Unwrap() error { return e.cause }

func notesCustodyFailure(code NotesCustodyFindingCode, err error) error {
	return &notesCustodyError{code: code, cause: err}
}

func notesCustodyFinding(err error) NotesCustodyFindingCode {
	var state interface{ SQLState() string }
	if errors.As(err, &state) && (state.SQLState() == "40001" || state.SQLState() == "40P01") {
		return NotesCustodyDatabaseRetryable
	}
	var custody *notesCustodyError
	if errors.As(err, &custody) {
		return custody.code
	}
	return NotesCustodyProjectionFailed
}

type NotesCustodyBatchItem struct {
	NotesArchiveProjectionResult
	Finding NotesCustodyFindingCode `json:"finding,omitempty"`
}

type NotesCustodyBatchResult struct {
	Items      []NotesCustodyBatchItem `json:"items"`
	NextCursor string                  `json:"next_cursor"`
	Wrapped    bool                    `json:"wrapped"`
}

// ConsumeBatch uses the caller's durable worker checkpoint for fair traversal.
// No receipt means retry on a later wrap, including events that complete late.
func (p NotesArchiveProjector) ConsumeBatch(ctx context.Context, cursor string, limit int) (NotesCustodyBatchResult, error) {
	result := NotesCustodyBatchResult{Items: []NotesCustodyBatchItem{}}
	if p.DB == nil || strings.TrimSpace(p.NodeKey) == "" || limit < 1 || limit > 50 ||
		(cursor != "" && !notesCustodyEventID.MatchString(cursor)) {
		return result, fmt.Errorf("%w: invalid Notes custody batch configuration", ErrInvalid)
	}
	var nodeID string
	if err := p.DB.QueryRowContext(ctx, `SELECT node_id FROM nodes.nodes WHERE node_key=$1 AND node_role='main'`, p.NodeKey).Scan(&nodeID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return result, notesCustodyFailure(NotesCustodyProjectionFailed, err)
		}
		return result, fmt.Errorf("%w: trusted local Notes custody node unavailable", ErrInvalid)
	}
	rows, err := p.DB.QueryContext(ctx, `SELECT e.workspace_lifecycle_event_id,e.workspace_archive_operation_id
	 FROM storage.workspace_lifecycle_events e JOIN storage.workspace_archive_operations op USING(workspace_archive_operation_id)
	 WHERE op.terminal_status='complete' AND op.phase IN ('archive_complete','restore_complete')
	 AND e.workspace_lifecycle_event_id>$1 AND NOT EXISTS (
	 SELECT 1 FROM knowledge.notes_custody_projection_receipts r
	 WHERE r.node_id=$2 AND r.workspace_lifecycle_event_id=e.workspace_lifecycle_event_id)
	 ORDER BY e.workspace_lifecycle_event_id LIMIT $3`, cursor, nodeID, limit+1)
	if err != nil {
		return result, notesCustodyFailure(NotesCustodyProjectionFailed, err)
	}
	type pending struct{ event, operation string }
	var work []pending
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.event, &item.operation); err != nil {
			_ = rows.Close()
			return result, notesCustodyFailure(NotesCustodyProjectionFailed, err)
		}
		work = append(work, item)
	}
	err = rows.Err()
	_ = rows.Close() // Never hold a pooled connection while projecting.
	if err != nil {
		return result, notesCustodyFailure(NotesCustodyProjectionFailed, err)
	}
	more := len(work) > limit
	if more {
		work = work[:limit]
	}
	for _, item := range work {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		projection, err := p.ProjectOperation(ctx, item.operation)
		entry := NotesCustodyBatchItem{NotesArchiveProjectionResult: projection}
		entry.EventID = item.event
		if err != nil {
			entry.Finding = notesCustodyFinding(err)
		}
		result.Items = append(result.Items, entry)
		result.NextCursor = item.event
		if err := ctx.Err(); err != nil {
			return result, err
		}
	}
	if !more {
		result.NextCursor = ""
		result.Wrapped = true
	}
	return result, nil
}

type notesCustodyReceipt struct {
	ManifestDigest   string
	ProjectEventID   string
	PreviousEventID  string
	ObjectCount      int
	MembershipDigest string
}

func notesCustodyArchivePredecessor(ctx context.Context, tx *sql.Tx, nodeID string, e storagearchive.WorkspaceLifecycleEvidence) (string, error) {
	if e.Plan.OperationKind != storagearchive.WorkspaceOperationRestore {
		return "", nil
	}
	var event, manifest string
	err := tx.QueryRowContext(ctx, `SELECT r.workspace_lifecycle_event_id,r.manifest_digest
	 FROM knowledge.notes_custody_projection_receipts r JOIN storage.workspace_lifecycle_events e USING(workspace_lifecycle_event_id)
	 WHERE r.node_id=$1 AND e.workspace_archive_operation_id=$2 AND e.operation_kind='archive'`, nodeID, e.ArchivePlan.OperationID).Scan(&event, &manifest)
	if err != nil || manifest != e.Plan.ArchiveManifestDigest {
		return "", notesCustodyFailure(NotesCustodyArchivePending, err)
	}
	return event, nil
}

func recordNotesCustodyReceipt(ctx context.Context, tx *sql.Tx, nodeID, eventID string, receipt notesCustodyReceipt, expectedCount int) error {
	rows, err := tx.QueryContext(ctx, `SELECT knowledge_object_id,transition_digest FROM knowledge.notes_custody_transitions
	 WHERE workspace_lifecycle_event_id=$1 ORDER BY knowledge_object_id LIMIT $2`, eventID, maxNotesCustodyObjects+1)
	if err != nil {
		return err
	}
	members := [][2]string{}
	for rows.Next() {
		var member [2]string
		if err := rows.Scan(&member[0], &member[1]); err != nil {
			_ = rows.Close()
			return err
		}
		members = append(members, member)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if len(members) != expectedCount {
		return notesCustodyFailure(NotesCustodyReceiptConflict, nil)
	}
	data, err := json.Marshal(members)
	if err != nil {
		return err
	}
	receipt.ObjectCount = len(members)
	receipt.MembershipDigest = hashArtifactValue(string(data))
	var existing notesCustodyReceipt
	err = tx.QueryRowContext(ctx, `SELECT manifest_digest,COALESCE(project_event_id,''),COALESCE(previous_event_id,''),object_count,membership_digest
	 FROM knowledge.notes_custody_projection_receipts WHERE node_id=$1 AND workspace_lifecycle_event_id=$2`, nodeID, eventID).Scan(
		&existing.ManifestDigest, &existing.ProjectEventID, &existing.PreviousEventID, &existing.ObjectCount, &existing.MembershipDigest)
	if err == nil {
		if !reflect.DeepEqual(receipt, existing) {
			return notesCustodyFailure(NotesCustodyReceiptConflict, nil)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowledge.notes_custody_projection_receipts
	 (node_id,workspace_lifecycle_event_id,manifest_digest,project_event_id,previous_event_id,object_count,membership_digest)
	 VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7)`, nodeID, eventID, receipt.ManifestDigest, receipt.ProjectEventID,
		receipt.PreviousEventID, receipt.ObjectCount, receipt.MembershipDigest)
	return err
}
